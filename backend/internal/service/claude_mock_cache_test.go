package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestClaudeMockCacheMemoryLifecycle(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID: 1,
		PrefixTTL: 5 * time.Minute,
		Prefixes:  []ClaudeMockCachePrefix{{Hash: "prefix", Tokens: 80}},
	}

	first := simulator.classifyMemory(req, base)
	if first.CacheCreationTokens != 80 || first.CacheReadTokens != 0 {
		t.Fatalf("first request = %+v, want 80 creation tokens", first)
	}

	second := simulator.classifyMemory(req, base.Add(4*time.Minute))
	if second.CacheReadTokens != 80 || second.CacheCreationTokens != 0 {
		t.Fatalf("second request = %+v, want 80 read tokens", second)
	}

	// The hit at minute four refreshes the five-minute TTL.
	third := simulator.classifyMemory(req, base.Add(8*time.Minute))
	if third.CacheReadTokens != 80 || third.CacheCreationTokens != 0 {
		t.Fatalf("third request = %+v, want refreshed cache hit", third)
	}
}

func TestClaudeMockCacheExpiredPrefixIsCreation(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID: 2,
		PrefixTTL: 5 * time.Minute,
		Prefixes:  []ClaudeMockCachePrefix{{Hash: "prefix", Tokens: 70}},
	}

	simulator.classifyMemory(req, base)
	got := simulator.classifyMemory(req, base.Add(5*time.Minute+time.Second))
	if got.CacheCreationTokens != 70 || got.CacheReadTokens != 0 {
		t.Fatalf("expired request = %+v, want cache recreation", got)
	}
}

func TestClaudeMockCacheMixedReadAndCreation(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	stable := ClaudeMockCacheClassifyRequest{
		AccountID: 3,
		PrefixTTL: 5 * time.Minute,
		Prefixes: []ClaudeMockCachePrefix{
			{Hash: "stable", Tokens: 60},
			{Hash: "tail-v1", Tokens: 40},
		},
	}

	first := simulator.classifyMemory(stable, base)
	if first.CacheCreationTokens != 100 || first.CacheReadTokens != 0 {
		t.Fatalf("first request = %+v, want full creation", first)
	}

	changed := stable
	changed.Prefixes = []ClaudeMockCachePrefix{
		{Hash: "stable", Tokens: 60},
		{Hash: "tail-v2", Tokens: 40},
	}
	second := simulator.classifyMemory(changed, base.Add(time.Minute))
	if second.CacheReadTokens != 60 || second.CacheCreationTokens != 40 {
		t.Fatalf("changed tail = %+v, want stable read and tail creation", second)
	}
}

func TestClaudeMockCacheForcedMissAppliesToWholeRequestAndRefreshesTTL(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID: 4,
		PrefixTTL: 5 * time.Minute,
		Prefixes: []ClaudeMockCachePrefix{
			{Hash: "first", Tokens: 60},
			{Hash: "second", Tokens: 40},
		},
	}

	simulator.classifyMemory(req, base)
	hit := simulator.classifyMemory(req, base.Add(time.Minute))
	if hit.CacheReadTokens != 100 || hit.CacheCreationTokens != 0 {
		t.Fatalf("warm request = %+v, want full read", hit)
	}

	req.ForceMiss = true
	miss := simulator.classifyMemory(req, base.Add(4*time.Minute))
	if miss.CacheReadTokens != 0 || miss.CacheCreationTokens != 100 {
		t.Fatalf("forced miss = %+v, want full creation", miss)
	}

	req.ForceMiss = false
	after := simulator.classifyMemory(req, base.Add(8*time.Minute))
	if after.CacheReadTokens != 100 || after.CacheCreationTokens != 0 {
		t.Fatalf("request after forced miss = %+v, want refreshed hit", after)
	}
}

func TestClaudeMockCacheApplyConservesTokensAndPreservesRealUsage(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	simulator := newClaudeMockCacheSimulator(nil)
	simulator.now = func() time.Time { return base }
	simulator.miss = func() bool { return false }
	account := &Account{
		ID:       6,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			"mock_cache_enabled": true,
		},
	}
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-5",
		Body: NewRequestBodyRef([]byte(`{
			"model":"claude-sonnet-4-5",
			"system":[{"type":"text","text":"stable prefix","cache_control":{"type":"ephemeral"}}],
			"messages":[{"role":"user","content":"hello"}]
		}`)),
	}
	usage := ClaudeUsage{InputTokens: 100, OutputTokens: 10}
	if !simulator.apply(context.Background(), account, parsed, parsed.Model, &usage) {
		t.Fatal("mock cache usage was not applied")
	}
	if total := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens; total != 100 {
		t.Fatalf("token total = %d, want 100; usage=%+v", total, usage)
	}
	if usage.CacheCreationInputTokens == 0 || usage.CacheCreation5mTokens != usage.CacheCreationInputTokens || usage.CacheCreation1hTokens != 0 {
		t.Fatalf("unexpected first-request cache creation: %+v", usage)
	}

	realUsage := ClaudeUsage{InputTokens: 20, CacheReadInputTokens: 80}
	if simulator.apply(context.Background(), account, parsed, parsed.Model, &realUsage) {
		t.Fatal("real upstream cache usage was overwritten")
	}
	if realUsage.InputTokens != 20 || realUsage.CacheReadInputTokens != 80 {
		t.Fatalf("real upstream usage changed: %+v", realUsage)
	}
}

func TestClaudeMockCacheApplySamplesMissOnce(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	simulator := newClaudeMockCacheSimulator(nil)
	simulator.now = func() time.Time { return base }
	calls := 0
	simulator.miss = func() bool {
		calls++
		return true
	}
	account := &Account{
		ID:       7,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"mock_cache_enabled": true},
	}
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-5",
		Body: NewRequestBodyRef([]byte(`{
			"system":[{"type":"text","text":"one","cache_control":{"type":"ephemeral"}}],
			"messages":[{"role":"user","content":[{"type":"text","text":"two","cache_control":{"type":"ephemeral"}}]}]
		}`)),
	}
	usage := ClaudeUsage{InputTokens: 100}
	if !simulator.apply(context.Background(), account, parsed, parsed.Model, &usage) {
		t.Fatal("mock cache usage was not applied")
	}
	if calls != 1 {
		t.Fatalf("miss sampler calls = %d, want exactly one per request", calls)
	}
	if usage.CacheReadInputTokens != 0 || usage.CacheCreationInputTokens == 0 {
		t.Fatalf("forced request miss was not reported as creation: %+v", usage)
	}
}

func TestClaudeMockCacheMissRateRemainsTwoPercent(t *testing.T) {
	if claudeMockCacheMissRate != 0.02 {
		t.Fatalf("mock cache miss rate = %v, want 0.02", claudeMockCacheMissRate)
	}
}

func TestClaudeMockCacheMetricsClassifyReadNaturalAndForcedCreation(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	simulator := newClaudeMockCacheSimulator(nil)
	now := base
	simulator.now = func() time.Time { return now }
	forceMiss := false
	simulator.miss = func() bool { return forceMiss }
	account := &Account{
		ID:       9,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"mock_cache_enabled": true},
	}
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-5",
		Body: NewRequestBodyRef([]byte(`{
			"system":[{"type":"text","text":"stable prefix","cache_control":{"type":"ephemeral"}}],
			"messages":[{"role":"user","content":"hello"}]
		}`)),
	}

	first := ClaudeUsage{InputTokens: 100}
	if !simulator.apply(context.Background(), account, parsed, parsed.Model, &first) {
		t.Fatal("natural creation was not applied")
	}
	now = base.Add(time.Minute)
	second := ClaudeUsage{InputTokens: 100}
	if !simulator.apply(context.Background(), account, parsed, parsed.Model, &second) {
		t.Fatal("cache read was not applied")
	}
	forceMiss = true
	now = base.Add(2 * time.Minute)
	third := ClaudeUsage{InputTokens: 100}
	if !simulator.apply(context.Background(), account, parsed, parsed.Model, &third) {
		t.Fatal("forced creation was not applied")
	}

	metrics := simulator.snapshotMetrics()
	if metrics.EligibleRequests != 3 || metrics.AppliedRequests != 3 {
		t.Fatalf("request metrics = %+v, want three eligible and applied requests", metrics)
	}
	if metrics.ForcedMissRequests != 1 || metrics.ReadRequests != 1 || metrics.CreationRequests != 2 {
		t.Fatalf("classification metrics = %+v, want one forced miss, one read, two creations", metrics)
	}
	if metrics.NaturalCreationRequests != 1 || metrics.PartialCreationRequests != 0 {
		t.Fatalf("natural creation metrics = %+v", metrics)
	}
	if metrics.CacheReadTokens != uint64(second.CacheReadInputTokens) {
		t.Fatalf("read token metrics = %d, want %d", metrics.CacheReadTokens, second.CacheReadInputTokens)
	}
	if metrics.CacheCreationTokens != uint64(first.CacheCreationInputTokens+third.CacheCreationInputTokens) {
		t.Fatalf("creation token metrics = %d, want %d", metrics.CacheCreationTokens, first.CacheCreationInputTokens+third.CacheCreationInputTokens)
	}
	if metrics.ForcedCreationTokens != uint64(third.CacheCreationInputTokens) {
		t.Fatalf("forced creation token metrics = %d, want %d", metrics.ForcedCreationTokens, third.CacheCreationInputTokens)
	}
}

func TestAnalyzeClaudeMockCachePrefixesToolChangeInvalidatesSystemCheckpoint(t *testing.T) {
	makeRequest := func(toolDescription string) *ParsedRequest {
		body := []byte(fmt.Sprintf(`{
			"tools":[{"name":"lookup","description":%q,"input_schema":{"type":"object"}}],
			"system":[{"type":"text","text":"stable system","cache_control":{"type":"ephemeral"}}],
			"messages":[{"role":"user","content":"hello"}]
		}`, toolDescription))
		return &ParsedRequest{Model: "claude-sonnet-4-5", Body: NewRequestBodyRef(body)}
	}

	before := analyzeClaudeMockCachePrefixes(makeRequest("version one"), "claude-sonnet-4-5", 100)
	after := analyzeClaudeMockCachePrefixes(makeRequest("version two"), "claude-sonnet-4-5", 100)
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("prefix counts = %d and %d, want one system checkpoint each", len(before), len(after))
	}
	if before[0].Hash == after[0].Hash {
		t.Fatal("tool change did not invalidate the later system checkpoint")
	}
}

func TestAnalyzeClaudeMockCachePrefixesKeepsLastFourCheckpoints(t *testing.T) {
	body := []byte(`{
		"system":[
			{"type":"text","text":"checkpoint one","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"checkpoint two","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"checkpoint three","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"checkpoint four","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"checkpoint five","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"checkpoint six","cache_control":{"type":"ephemeral"}}
		]
	}`)
	parsed := &ParsedRequest{Model: "claude-sonnet-4-5", Body: NewRequestBodyRef(body)}
	prefixes := analyzeClaudeMockCachePrefixes(parsed, parsed.Model, 600)
	if len(prefixes) != claudeMockCacheMaxCheckpoints {
		t.Fatalf("prefix count = %d, want %d", len(prefixes), claudeMockCacheMaxCheckpoints)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode test request: %v", err)
	}
	analyzer := &claudeMockCacheAnalyzer{model: parsed.Model}
	analyzer.addSystem(decoded["system"])
	expected := analyzer.breakpoints[len(analyzer.breakpoints)-claudeMockCacheMaxCheckpoints:]
	total := 0
	for i, prefix := range prefixes {
		if prefix.Hash != expected[i].hash {
			t.Fatalf("prefix %d hash does not match checkpoint %d", i, i+3)
		}
		total += prefix.Tokens
	}
	if total != 600 {
		t.Fatalf("classified tokens = %d, want 600 through the final checkpoint", total)
	}
	if prefixes[0].Tokens <= prefixes[1].Tokens {
		t.Fatalf("first retained segment = %d, want it to include earlier discarded checkpoints", prefixes[0].Tokens)
	}
}

func TestNormalizeAnthropicMockCacheExtra(t *testing.T) {
	normalized, err := normalizeAnthropicMockCacheExtra(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		"mock_cache_enabled":        true,
		"mock_cache_target_percent": "legacy-invalid-value",
	})
	if err != nil {
		t.Fatalf("normalize enabled config: %v", err)
	}
	if normalized["mock_cache_enabled"] != true {
		t.Fatalf("mock cache switch = %#v, want true", normalized["mock_cache_enabled"])
	}
	if _, ok := normalized["mock_cache_target_percent"]; ok {
		t.Fatal("legacy target percentage was retained")
	}

	ineligible, err := normalizeAnthropicMockCacheExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
		"mock_cache_enabled":        true,
		"mock_cache_target_percent": 91,
		"keep":                      true,
	})
	if err != nil {
		t.Fatalf("normalize ineligible config: %v", err)
	}
	if _, ok := ineligible["mock_cache_enabled"]; ok {
		t.Fatal("mock cache switch was retained for an ineligible account")
	}
	if _, ok := ineligible["mock_cache_target_percent"]; ok {
		t.Fatal("legacy target percentage was retained for an ineligible account")
	}
	if ineligible["keep"] != true {
		t.Fatal("unrelated extra field was removed")
	}
}

type failingClaudeMockCacheStore struct{}

func (failingClaudeMockCacheStore) ClassifyClaudeMockCache(context.Context, ClaudeMockCacheClassifyRequest) (ClaudeMockCacheClassifyResult, error) {
	return ClaudeMockCacheClassifyResult{}, errors.New("redis unavailable")
}

func TestClaudeMockCacheStoreFailureFallsBackToMemory(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	simulator := newClaudeMockCacheSimulator(nil)
	simulator.store = failingClaudeMockCacheStore{}
	simulator.now = func() time.Time { return base }
	simulator.miss = func() bool { return false }
	account := &Account{
		ID:       8,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			"mock_cache_enabled": true,
		},
	}
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-5",
		Body: NewRequestBodyRef([]byte(`{
			"system":[{"type":"text","text":"fallback prefix","cache_control":{"type":"ephemeral"}}],
			"messages":[{"role":"user","content":"hello"}]
		}`)),
	}
	usage := ClaudeUsage{InputTokens: 100}
	if !simulator.apply(context.Background(), account, parsed, parsed.Model, &usage) {
		t.Fatal("store failure did not fall back to memory simulation")
	}
	if usage.CacheCreationInputTokens == 0 {
		t.Fatalf("memory fallback did not classify the new prefix: %+v", usage)
	}
	if got := simulator.snapshotMetrics().StoreFallbackRequests; got != 1 {
		t.Fatalf("store fallback requests = %d, want 1", got)
	}
}
