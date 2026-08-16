package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tiktoken-go/tokenizer"
)

const (
	claudeMockCacheTTL          = 5 * time.Minute
	claudeMockCacheHourStatsTTL = 2 * time.Hour
	claudeMockCacheTargetSigma  = 2.0
	claudeMockCacheMaxDeviation = 5.0
	claudeMockCacheTargetScale  = 100
)

type ClaudeMockCachePrefix struct {
	Hash   string
	Tokens int
}

type ClaudeMockCacheClassifyRequest struct {
	AccountID      int64
	HourBucket     int64
	TargetBasisPts int
	TotalTokens    int
	PrefixTTL      time.Duration
	StatsTTL       time.Duration
	Prefixes       []ClaudeMockCachePrefix
}

type ClaudeMockCacheClassifyResult struct {
	CacheReadTokens     int
	CacheCreationTokens int
}

// ClaudeMockCacheStore is an optional capability implemented by the concrete
// gateway cache. GatewayService falls back to a process-local store when it is
// unavailable or returns an error.
type ClaudeMockCacheStore interface {
	ClassifyClaudeMockCache(context.Context, ClaudeMockCacheClassifyRequest) (ClaudeMockCacheClassifyResult, error)
}

type claudeMockCacheRequestContextKey struct{}

func withClaudeMockCacheRequest(ctx context.Context, parsed *ParsedRequest) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, claudeMockCacheRequestContextKey{}, parsed)
}

func claudeMockCacheRequestFromContext(ctx context.Context) *ParsedRequest {
	if ctx == nil {
		return nil
	}
	parsed, _ := ctx.Value(claudeMockCacheRequestContextKey{}).(*ParsedRequest)
	return parsed
}

func claudeMockCacheModelFromContext(ctx context.Context) string {
	if parsed := claudeMockCacheRequestFromContext(ctx); parsed != nil {
		return parsed.Model
	}
	return ""
}

type claudeMockCacheMemoryPrefix struct {
	expiresAt time.Time
}

type claudeMockCacheMemoryStats struct {
	naturalReadTokens  int64
	reportedReadTokens int64
	totalTokens        int64
	expiresAt          time.Time
}

type claudeMockCacheSimulator struct {
	store ClaudeMockCacheStore
	now   func() time.Time

	mu       sync.Mutex
	prefixes map[string]claudeMockCacheMemoryPrefix
	stats    map[string]claudeMockCacheMemoryStats
}

func newClaudeMockCacheSimulator(cache GatewayCache) *claudeMockCacheSimulator {
	store, _ := cache.(ClaudeMockCacheStore)
	return &claudeMockCacheSimulator{
		store:    store,
		now:      time.Now,
		prefixes: make(map[string]claudeMockCacheMemoryPrefix),
		stats:    make(map[string]claudeMockCacheMemoryStats),
	}
}

func (s *claudeMockCacheSimulator) apply(ctx context.Context, account *Account, parsed *ParsedRequest, model string, usage *ClaudeUsage) bool {
	if s == nil || account == nil || usage == nil || !account.IsAnthropicMockCacheEnabled() {
		return false
	}
	if !isClaudeMockCacheModel(model) || usage.InputTokens <= 0 || claudeUsageHasCacheTokens(*usage) {
		return false
	}
	prefixes := analyzeClaudeMockCachePrefixes(parsed, model, usage.InputTokens)

	now := s.now()
	target := claudeMockCacheTargetBasisPoints(account.ID, account.GetAnthropicMockCacheTargetPercent(), now)
	req := ClaudeMockCacheClassifyRequest{
		AccountID:      account.ID,
		HourBucket:     now.Unix() / int64(time.Hour/time.Second),
		TargetBasisPts: target,
		TotalTokens:    usage.InputTokens,
		PrefixTTL:      claudeMockCacheTTL,
		StatsTTL:       claudeMockCacheHourStatsTTL,
		Prefixes:       prefixes,
	}

	result, err := s.classify(ctx, req, now)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "mock_claude_cache store fallback: account=%d error=%v", account.ID, err)
		result = s.classifyMemory(req, now)
	}
	classified := result.CacheReadTokens + result.CacheCreationTokens
	if classified <= 0 || classified >= usage.InputTokens+1 {
		return false
	}

	before := usage.InputTokens
	usage.InputTokens -= classified
	usage.CacheReadInputTokens = result.CacheReadTokens
	usage.CacheCreationInputTokens = result.CacheCreationTokens
	usage.CacheCreation5mTokens = result.CacheCreationTokens
	usage.CacheCreation1hTokens = 0
	logger.LegacyPrintf(
		"service.gateway",
		"mock_claude_cache_usage: account=%d model=%s target_bps=%d input:%d->%d cache_read=%d cache_creation_5m=%d",
		account.ID,
		model,
		target,
		before,
		usage.InputTokens,
		usage.CacheReadInputTokens,
		usage.CacheCreationInputTokens,
	)
	return true
}

func (s *GatewayService) applyClaudeMockCacheUsage(ctx context.Context, account *Account, model string, usage *ClaudeUsage) bool {
	if s == nil || s.claudeMockCache == nil || IsForceCacheBilling(ctx) {
		return false
	}
	return s.claudeMockCache.apply(ctx, account, claudeMockCacheRequestFromContext(ctx), model, usage)
}

func rewriteClaudeMockCacheUsageMap(ctx context.Context, account *Account, model string, usage map[string]any, simulator *claudeMockCacheSimulator) bool {
	if simulator == nil || usage == nil || IsForceCacheBilling(ctx) {
		return false
	}
	parsed := claudeMockCacheRequestFromContext(ctx)
	value := ClaudeUsage{
		InputTokens:              usageInt(usage["input_tokens"]),
		OutputTokens:             usageInt(usage["output_tokens"]),
		CacheCreationInputTokens: usageInt(usage["cache_creation_input_tokens"]),
		CacheReadInputTokens:     usageInt(usage["cache_read_input_tokens"]),
	}
	if value.CacheReadInputTokens == 0 {
		value.CacheReadInputTokens = usageInt(usage["cached_tokens"])
	}
	if cacheCreation, ok := usage["cache_creation"].(map[string]any); ok {
		value.CacheCreation5mTokens = usageInt(cacheCreation["ephemeral_5m_input_tokens"])
		value.CacheCreation1hTokens = usageInt(cacheCreation["ephemeral_1h_input_tokens"])
	}
	if !simulator.apply(ctx, account, parsed, model, &value) {
		return false
	}
	usage["input_tokens"] = value.InputTokens
	usage["cache_read_input_tokens"] = value.CacheReadInputTokens
	usage["cache_creation_input_tokens"] = value.CacheCreationInputTokens
	cacheCreation, _ := usage["cache_creation"].(map[string]any)
	if cacheCreation == nil {
		cacheCreation = make(map[string]any, 2)
		usage["cache_creation"] = cacheCreation
	}
	cacheCreation["ephemeral_5m_input_tokens"] = value.CacheCreation5mTokens
	cacheCreation["ephemeral_1h_input_tokens"] = value.CacheCreation1hTokens
	return true
}

func usageInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		parsed, _ := v.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func rewriteClaudeMockCacheUsageBytes(ctx context.Context, account *Account, model string, body []byte, simulator *claudeMockCacheSimulator) ([]byte, bool) {
	var root map[string]any
	if len(body) == 0 || json.Unmarshal(body, &root) != nil {
		return body, false
	}
	usage, ok := root["usage"].(map[string]any)
	if !ok || !rewriteClaudeMockCacheUsageMap(ctx, account, model, usage, simulator) {
		return body, false
	}
	updated, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return updated, true
}

func rewriteClaudeUsageBytes(body []byte, usage ClaudeUsage) ([]byte, bool) {
	var root map[string]any
	if len(body) == 0 || json.Unmarshal(body, &root) != nil {
		return body, false
	}
	usageObject, ok := root["usage"].(map[string]any)
	if !ok {
		return body, false
	}
	rewriteClaudeUsageMap(usageObject, usage)
	updated, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return updated, true
}

func rewriteClaudeUsageMap(usage map[string]any, value ClaudeUsage) {
	usage["input_tokens"] = value.InputTokens
	usage["cache_read_input_tokens"] = value.CacheReadInputTokens
	usage["cache_creation_input_tokens"] = value.CacheCreationInputTokens
	cacheCreation, _ := usage["cache_creation"].(map[string]any)
	if cacheCreation == nil {
		cacheCreation = make(map[string]any, 2)
		usage["cache_creation"] = cacheCreation
	}
	cacheCreation["ephemeral_5m_input_tokens"] = value.CacheCreation5mTokens
	cacheCreation["ephemeral_1h_input_tokens"] = value.CacheCreation1hTokens
}

func rewriteClaudeMockCacheSSEData(ctx context.Context, account *Account, model, data string, simulator *claudeMockCacheSimulator) (string, bool) {
	var event map[string]any
	if strings.TrimSpace(data) == "" || json.Unmarshal([]byte(data), &event) != nil {
		return data, false
	}
	if eventType, _ := event["type"].(string); eventType != "message_start" {
		return data, false
	}
	message, ok := event["message"].(map[string]any)
	if !ok {
		return data, false
	}
	usage, ok := message["usage"].(map[string]any)
	if !ok || !rewriteClaudeMockCacheUsageMap(ctx, account, model, usage, simulator) {
		return data, false
	}
	updated, err := json.Marshal(event)
	if err != nil {
		return data, false
	}
	return string(updated), true
}

func (s *claudeMockCacheSimulator) classify(ctx context.Context, req ClaudeMockCacheClassifyRequest, now time.Time) (ClaudeMockCacheClassifyResult, error) {
	if s.store == nil {
		return s.classifyMemory(req, now), nil
	}
	return s.store.ClassifyClaudeMockCache(ctx, req)
}

func (s *claudeMockCacheSimulator) classifyMemory(req ClaudeMockCacheClassifyRequest, now time.Time) ClaudeMockCacheClassifyResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	statsKey := fmt.Sprintf("%d:%d", req.AccountID, req.HourBucket)
	stats := s.stats[statsKey]
	if !stats.expiresAt.IsZero() && !stats.expiresAt.After(now) {
		stats = claudeMockCacheMemoryStats{}
	}
	finalTotal := stats.totalTokens + int64(req.TotalTokens)
	result := ClaudeMockCacheClassifyResult{}
	eligible := make([]bool, len(req.Prefixes))
	naturalReadDelta := int64(0)
	for i, prefix := range req.Prefixes {
		if prefix.Tokens <= 0 || prefix.Hash == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", req.AccountID, prefix.Hash)
		entry, exists := s.prefixes[key]
		eligible[i] = exists && entry.expiresAt.After(now)
		if eligible[i] {
			naturalReadDelta += int64(prefix.Tokens)
		}
	}
	naturalRateAtOrBelowTarget := (stats.naturalReadTokens+naturalReadDelta)*10000 <= int64(req.TargetBasisPts)*finalTotal
	for i, prefix := range req.Prefixes {
		if prefix.Tokens <= 0 || prefix.Hash == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", req.AccountID, prefix.Hash)
		if eligible[i] && (naturalRateAtOrBelowTarget || mockCacheHitCloser(stats.reportedReadTokens+int64(result.CacheReadTokens), int64(prefix.Tokens), finalTotal, req.TargetBasisPts)) {
			result.CacheReadTokens += prefix.Tokens
		} else {
			result.CacheCreationTokens += prefix.Tokens
		}
		s.prefixes[key] = claudeMockCacheMemoryPrefix{expiresAt: now.Add(req.PrefixTTL)}
	}
	stats.naturalReadTokens += naturalReadDelta
	stats.reportedReadTokens += int64(result.CacheReadTokens)
	stats.totalTokens = finalTotal
	stats.expiresAt = now.Add(req.StatsTTL)
	s.stats[statsKey] = stats
	s.pruneMemory(now)
	return result
}

func (s *claudeMockCacheSimulator) pruneMemory(now time.Time) {
	if len(s.prefixes) > 4096 {
		for key, entry := range s.prefixes {
			if !entry.expiresAt.After(now) {
				delete(s.prefixes, key)
			}
		}
	}
	if len(s.stats) > 256 {
		for key, entry := range s.stats {
			if !entry.expiresAt.After(now) {
				delete(s.stats, key)
			}
		}
	}
}

func mockCacheHitCloser(currentRead, candidateRead, finalTotal int64, targetBasisPts int) bool {
	if finalTotal <= 0 {
		return false
	}
	hitError := absInt64((currentRead+candidateRead)*10000 - int64(targetBasisPts)*finalTotal)
	missError := absInt64(currentRead*10000 - int64(targetBasisPts)*finalTotal)
	return hitError <= missError
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func isClaudeMockCacheModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(claude.NormalizeModelID(model)))
	return strings.Contains(normalized, "claude-")
}

func claudeUsageHasCacheTokens(usage ClaudeUsage) bool {
	return usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 ||
		usage.CacheCreation5mTokens > 0 || usage.CacheCreation1hTokens > 0
}

func claudeMockCacheTargetBasisPoints(accountID int64, configured int, now time.Time) int {
	hourBucket := now.Unix() / int64(time.Hour/time.Second)
	hourSeed := claudeMockCacheSeed(accountID, hourBucket, -1)
	offset := int(hourSeed%3) - 1
	hourTarget := float64(configured + offset)

	secondsIntoHour := now.Unix() % int64(time.Hour/time.Second)
	if secondsIntoHour < 0 {
		secondsIntoHour += int64(time.Hour / time.Second)
	}
	slot := secondsIntoHour / int64(5*time.Minute/time.Second)
	fraction := float64(secondsIntoHour%int64(5*time.Minute/time.Second)) / float64(5*time.Minute/time.Second)
	left := claudeMockCacheGaussian(accountID, hourBucket, slot)
	rightHourBucket := hourBucket
	rightSlot := slot + 1
	if rightSlot >= int64(time.Hour/(5*time.Minute)) {
		rightHourBucket++
		rightSlot = 0
	}
	right := claudeMockCacheGaussian(accountID, rightHourBucket, rightSlot)
	deviation := left + (right-left)*fraction
	target := hourTarget + deviation
	if target < 1 {
		target = 1
	}
	if target > 99 {
		target = 99
	}
	return int(math.Round(target * claudeMockCacheTargetScale))
}

func claudeMockCacheGaussian(accountID, hourBucket, slot int64) float64 {
	seedA := claudeMockCacheSeed(accountID, hourBucket, slot*2)
	seedB := claudeMockCacheSeed(accountID, hourBucket, slot*2+1)
	u1 := (float64(seedA) + 1) / (float64(^uint64(0)) + 2)
	u2 := (float64(seedB) + 1) / (float64(^uint64(0)) + 2)
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	deviation := z * claudeMockCacheTargetSigma
	return math.Max(-claudeMockCacheMaxDeviation, math.Min(claudeMockCacheMaxDeviation, deviation))
}

func claudeMockCacheSeed(accountID, hourBucket, slot int64) uint64 {
	var data [24]byte
	binary.LittleEndian.PutUint64(data[0:8], uint64(accountID))
	binary.LittleEndian.PutUint64(data[8:16], uint64(hourBucket))
	binary.LittleEndian.PutUint64(data[16:24], uint64(slot))
	sum := sha256.Sum256(data[:])
	return binary.LittleEndian.Uint64(sum[:8])
}

type claudeMockCacheAnalyzer struct {
	model       string
	units       []any
	totalWeight int
	breakpoints []claudeMockCacheBreakpoint
}

type claudeMockCacheBreakpoint struct {
	hash             string
	cumulativeWeight int
}

func analyzeClaudeMockCachePrefixes(parsed *ParsedRequest, model string, inputTokens int) []ClaudeMockCachePrefix {
	if parsed == nil || parsed.Body == nil || inputTokens <= 0 {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(parsed.Body.Bytes(), &body); err != nil {
		return nil
	}
	analyzer := &claudeMockCacheAnalyzer{model: strings.TrimSpace(model)}
	analyzer.addSystem(body["system"])
	analyzer.addTools(body["tools"])
	analyzer.addMessages(body["messages"])
	if len(analyzer.breakpoints) == 0 || analyzer.totalWeight <= 0 {
		return nil
	}

	result := make([]ClaudeMockCachePrefix, 0, len(analyzer.breakpoints))
	previousScaled := 0
	for _, breakpoint := range analyzer.breakpoints {
		scaled := int(math.Round(float64(inputTokens) * float64(breakpoint.cumulativeWeight) / float64(analyzer.totalWeight)))
		if scaled > inputTokens {
			scaled = inputTokens
		}
		segmentTokens := scaled - previousScaled
		if segmentTokens > 0 {
			result = append(result, ClaudeMockCachePrefix{Hash: breakpoint.hash, Tokens: segmentTokens})
			previousScaled = scaled
		}
	}
	return result
}

func (a *claudeMockCacheAnalyzer) addSystem(system any) {
	switch value := system.(type) {
	case string:
		a.addUnit("system", value, false)
	case []any:
		for _, block := range value {
			a.addUnit("system", block, hasEphemeralMockCacheControl(block))
		}
	}
}

func (a *claudeMockCacheAnalyzer) addTools(tools any) {
	items, _ := tools.([]any)
	for _, tool := range items {
		a.addUnit("tool", tool, hasEphemeralMockCacheControl(tool))
	}
}

func (a *claudeMockCacheAnalyzer) addMessages(messages any) {
	items, _ := messages.([]any)
	for index, raw := range items {
		message, ok := raw.(map[string]any)
		if !ok {
			a.addUnit("message", raw, false)
			continue
		}
		role, _ := message["role"].(string)
		a.addUnit("message_role", map[string]any{"index": index, "role": role}, false)
		switch content := message["content"].(type) {
		case string:
			a.addUnit("message_content", content, hasEphemeralMockCacheControl(message))
		case []any:
			for _, block := range content {
				a.addUnit("message_content", block, hasEphemeralMockCacheControl(block))
			}
		}
	}
}

func (a *claudeMockCacheAnalyzer) addUnit(section string, value any, breakpoint bool) {
	unit := map[string]any{"section": section, "value": value}
	a.units = append(a.units, unit)
	a.totalWeight += estimateClaudeMockCacheTokens(value) + 1
	if !breakpoint {
		return
	}
	payload, err := json.Marshal(map[string]any{"model": a.model, "prefix": a.units})
	if err != nil {
		return
	}
	sum := sha256.Sum256(payload)
	a.breakpoints = append(a.breakpoints, claudeMockCacheBreakpoint{
		hash:             hex.EncodeToString(sum[:]),
		cumulativeWeight: a.totalWeight,
	})
}

func hasEphemeralMockCacheControl(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	control, ok := object["cache_control"].(map[string]any)
	if !ok {
		return false
	}
	typeName, _ := control["type"].(string)
	return strings.EqualFold(strings.TrimSpace(typeName), "ephemeral")
}

var (
	claudeMockCacheTokenizerOnce sync.Once
	claudeMockCacheTokenizer     tokenizer.Codec
)

func estimateClaudeMockCacheTokens(value any) int {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 {
		return 1
	}
	claudeMockCacheTokenizerOnce.Do(func() {
		codec, codecErr := tokenizer.Get(tokenizer.O200kBase)
		if codecErr == nil {
			claudeMockCacheTokenizer = codec
		}
	})
	if claudeMockCacheTokenizer != nil {
		if count, countErr := claudeMockCacheTokenizer.Count(string(raw)); countErr == nil && count > 0 {
			return count
		}
	}
	return max(1, len([]rune(string(raw)))/4)
}
