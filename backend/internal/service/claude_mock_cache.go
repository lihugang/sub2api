package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tiktoken-go/tokenizer"
)

const (
	claudeMockCacheTTL            = 5 * time.Minute
	claudeMockCacheMaxCheckpoints = 4
	claudeMockCacheMissRate       = 0.02
)

type ClaudeMockCachePrefix struct {
	Hash   string
	Tokens int
}

type ClaudeMockCacheClassifyRequest struct {
	AccountID int64
	ForceMiss bool
	PrefixTTL time.Duration
	Prefixes  []ClaudeMockCachePrefix
}

type ClaudeMockCacheClassifyResult struct {
	CacheReadTokens     int
	CacheCreationTokens int
}

// claudeMockCacheMetrics is deliberately process-internal. It is intended for
// admin/operations diagnostics and must not be added to regular-user usage DTOs.
type claudeMockCacheMetrics struct {
	EligibleRequests        atomic.Uint64
	AppliedRequests         atomic.Uint64
	ForcedMissRequests      atomic.Uint64
	ReadRequests            atomic.Uint64
	CreationRequests        atomic.Uint64
	PartialCreationRequests atomic.Uint64
	NaturalCreationRequests atomic.Uint64
	StoreFallbackRequests   atomic.Uint64
	CacheReadTokens         atomic.Uint64
	CacheCreationTokens     atomic.Uint64
	ForcedCreationTokens    atomic.Uint64
}

type claudeMockCacheMetricsSnapshot struct {
	EligibleRequests        uint64
	AppliedRequests         uint64
	ForcedMissRequests      uint64
	ReadRequests            uint64
	CreationRequests        uint64
	PartialCreationRequests uint64
	NaturalCreationRequests uint64
	StoreFallbackRequests   uint64
	CacheReadTokens         uint64
	CacheCreationTokens     uint64
	ForcedCreationTokens    uint64
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

type claudeMockCacheSimulator struct {
	store ClaudeMockCacheStore
	now   func() time.Time
	miss  func() bool

	mu       sync.Mutex
	prefixes map[string]claudeMockCacheMemoryPrefix
	metrics  claudeMockCacheMetrics
}

func newClaudeMockCacheSimulator(cache GatewayCache) *claudeMockCacheSimulator {
	store, _ := cache.(ClaudeMockCacheStore)
	return &claudeMockCacheSimulator{
		store:    store,
		now:      time.Now,
		miss:     func() bool { return rand.Float64() < claudeMockCacheMissRate },
		prefixes: make(map[string]claudeMockCacheMemoryPrefix),
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
	if len(prefixes) == 0 {
		return false
	}
	s.metrics.EligibleRequests.Add(1)

	now := s.now()
	forceMiss := s.miss != nil && s.miss()
	if forceMiss {
		s.metrics.ForcedMissRequests.Add(1)
	}
	req := ClaudeMockCacheClassifyRequest{
		AccountID: account.ID,
		ForceMiss: forceMiss,
		PrefixTTL: claudeMockCacheTTL,
		Prefixes:  prefixes,
	}

	result, err := s.classify(ctx, req, now)
	if err != nil {
		s.metrics.StoreFallbackRequests.Add(1)
		logger.LegacyPrintf("service.gateway", "mock_claude_cache store fallback: account=%d error=%v", account.ID, err)
		result = s.classifyMemory(req, now)
	}
	classified := result.CacheReadTokens + result.CacheCreationTokens
	if classified <= 0 || classified >= usage.InputTokens+1 {
		return false
	}
	s.recordClassificationMetrics(result, forceMiss)

	before := usage.InputTokens
	usage.InputTokens -= classified
	usage.CacheReadInputTokens = result.CacheReadTokens
	usage.CacheCreationInputTokens = result.CacheCreationTokens
	usage.CacheCreation5mTokens = result.CacheCreationTokens
	usage.CacheCreation1hTokens = 0
	reason := "cache_read"
	if forceMiss {
		reason = "forced_miss"
	} else if result.CacheReadTokens > 0 && result.CacheCreationTokens > 0 {
		reason = "partial_natural_creation"
	} else if result.CacheCreationTokens > 0 {
		reason = "natural_creation"
	}
	logger.LegacyPrintf(
		"service.gateway",
		"mock_claude_cache_usage: account=%d model=%s checkpoints=%d reason=%s force_miss=%t input:%d->%d cache_read=%d cache_creation_5m=%d",
		account.ID,
		model,
		len(prefixes),
		reason,
		forceMiss,
		before,
		usage.InputTokens,
		usage.CacheReadInputTokens,
		usage.CacheCreationInputTokens,
	)
	return true
}

func (s *claudeMockCacheSimulator) recordClassificationMetrics(result ClaudeMockCacheClassifyResult, forceMiss bool) {
	if s == nil {
		return
	}
	s.metrics.AppliedRequests.Add(1)
	if result.CacheReadTokens > 0 {
		s.metrics.ReadRequests.Add(1)
		s.metrics.CacheReadTokens.Add(uint64(result.CacheReadTokens))
	}
	if result.CacheCreationTokens > 0 {
		s.metrics.CreationRequests.Add(1)
		s.metrics.CacheCreationTokens.Add(uint64(result.CacheCreationTokens))
		if forceMiss {
			s.metrics.ForcedCreationTokens.Add(uint64(result.CacheCreationTokens))
		} else {
			s.metrics.NaturalCreationRequests.Add(1)
		}
	}
	if result.CacheReadTokens > 0 && result.CacheCreationTokens > 0 {
		s.metrics.PartialCreationRequests.Add(1)
	}
}

func (s *claudeMockCacheSimulator) snapshotMetrics() claudeMockCacheMetricsSnapshot {
	if s == nil {
		return claudeMockCacheMetricsSnapshot{}
	}
	return claudeMockCacheMetricsSnapshot{
		EligibleRequests:        s.metrics.EligibleRequests.Load(),
		AppliedRequests:         s.metrics.AppliedRequests.Load(),
		ForcedMissRequests:      s.metrics.ForcedMissRequests.Load(),
		ReadRequests:            s.metrics.ReadRequests.Load(),
		CreationRequests:        s.metrics.CreationRequests.Load(),
		PartialCreationRequests: s.metrics.PartialCreationRequests.Load(),
		NaturalCreationRequests: s.metrics.NaturalCreationRequests.Load(),
		StoreFallbackRequests:   s.metrics.StoreFallbackRequests.Load(),
		CacheReadTokens:         s.metrics.CacheReadTokens.Load(),
		CacheCreationTokens:     s.metrics.CacheCreationTokens.Load(),
		ForcedCreationTokens:    s.metrics.ForcedCreationTokens.Load(),
	}
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

	result := ClaudeMockCacheClassifyResult{}
	for _, prefix := range req.Prefixes {
		if prefix.Tokens <= 0 || prefix.Hash == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", req.AccountID, prefix.Hash)
		entry, exists := s.prefixes[key]
		if !req.ForceMiss && exists && entry.expiresAt.After(now) {
			result.CacheReadTokens += prefix.Tokens
		} else {
			result.CacheCreationTokens += prefix.Tokens
		}
		s.prefixes[key] = claudeMockCacheMemoryPrefix{expiresAt: now.Add(req.PrefixTTL)}
	}
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
}

func isClaudeMockCacheModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(claude.NormalizeModelID(model)))
	return strings.Contains(normalized, "claude-")
}

func claudeUsageHasCacheTokens(usage ClaudeUsage) bool {
	return usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 ||
		usage.CacheCreation5mTokens > 0 || usage.CacheCreation1hTokens > 0
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
	// Anthropic constructs the cacheable prompt prefix in tools → system →
	// messages order. The synthetic cache must hash the same logical order;
	// otherwise a tool change can incorrectly retain a later system checkpoint.
	analyzer.addTools(body["tools"])
	analyzer.addSystem(body["system"])
	analyzer.addMessages(body["messages"])
	if len(analyzer.breakpoints) == 0 || analyzer.totalWeight <= 0 {
		return nil
	}

	start := 0
	if len(analyzer.breakpoints) > claudeMockCacheMaxCheckpoints {
		start = len(analyzer.breakpoints) - claudeMockCacheMaxCheckpoints
	}
	selected := analyzer.breakpoints[start:]
	result := make([]ClaudeMockCachePrefix, 0, len(selected))
	previousScaled := 0
	for _, breakpoint := range selected {
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
