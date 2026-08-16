package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClaudeMockCacheMemoryLifecycle(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID:      1,
		HourBucket:     base.Unix() / int64(time.Hour/time.Second),
		TargetBasisPts: 9100,
		TotalTokens:    100,
		PrefixTTL:      5 * time.Minute,
		StatsTTL:       2 * time.Hour,
		Prefixes:       []ClaudeMockCachePrefix{{Hash: "prefix", Tokens: 80}},
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
		AccountID:      2,
		HourBucket:     base.Unix() / int64(time.Hour/time.Second),
		TargetBasisPts: 9900,
		TotalTokens:    100,
		PrefixTTL:      5 * time.Minute,
		StatsTTL:       2 * time.Hour,
		Prefixes:       []ClaudeMockCachePrefix{{Hash: "prefix", Tokens: 70}},
	}

	simulator.classifyMemory(req, base)
	got := simulator.classifyMemory(req, base.Add(5*time.Minute+time.Second))
	if got.CacheCreationTokens != 70 || got.CacheReadTokens != 0 {
		t.Fatalf("expired request = %+v, want cache recreation", got)
	}
}

func TestClaudeMockCacheDoesNotRaiseNaturalRate(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID:      3,
		HourBucket:     base.Unix() / int64(time.Hour/time.Second),
		TargetBasisPts: 9000,
		TotalTokens:    100,
		PrefixTTL:      5 * time.Minute,
		StatsTTL:       2 * time.Hour,
		Prefixes:       []ClaudeMockCachePrefix{{Hash: "small-prefix", Tokens: 20}},
	}

	first := simulator.classifyMemory(req, base)
	second := simulator.classifyMemory(req, base.Add(time.Minute))
	if first.CacheReadTokens != 0 {
		t.Fatalf("new prefix was forced into a hit: %+v", first)
	}
	if second.CacheReadTokens != 20 {
		t.Fatalf("natural hit below target was suppressed: %+v", second)
	}
}

func TestClaudeMockCacheReducesOnlyNaturallyHighRate(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID:      4,
		HourBucket:     base.Unix() / int64(time.Hour/time.Second),
		TargetBasisPts: 5000,
		TotalTokens:    100,
		PrefixTTL:      5 * time.Minute,
		StatsTTL:       2 * time.Hour,
		Prefixes:       []ClaudeMockCachePrefix{{Hash: "large-prefix", Tokens: 100}},
	}

	results := make([]ClaudeMockCacheClassifyResult, 0, 4)
	for i := 0; i < 4; i++ {
		results = append(results, simulator.classifyMemory(req, base.Add(time.Duration(i)*time.Minute)))
	}

	reportedRead := 0
	for _, result := range results {
		reportedRead += result.CacheReadTokens
	}
	if reportedRead != 200 {
		t.Fatalf("reported reads = %d, want 200 of 400 tokens near the 50%% cap; results=%+v", reportedRead, results)
	}
	if results[3].CacheCreationTokens != 100 {
		t.Fatalf("naturally eligible hit was not reduced after rate exceeded target: %+v", results[3])
	}
}

func TestClaudeMockCacheCountsRequestsWithoutBreakpointsInDenominator(t *testing.T) {
	simulator := newClaudeMockCacheSimulator(nil)
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	req := ClaudeMockCacheClassifyRequest{
		AccountID:      5,
		HourBucket:     base.Unix() / int64(time.Hour/time.Second),
		TargetBasisPts: 5000,
		TotalTokens:    100,
		PrefixTTL:      5 * time.Minute,
		StatsTTL:       2 * time.Hour,
	}

	simulator.classifyMemory(req, base)
	req.Prefixes = []ClaudeMockCachePrefix{{Hash: "prefix", Tokens: 100}}
	simulator.classifyMemory(req, base.Add(time.Minute))
	result := simulator.classifyMemory(req, base.Add(2*time.Minute))
	if result.CacheReadTokens != 100 {
		t.Fatalf("uncached request was omitted from denominator: %+v", result)
	}
}

func TestClaudeMockCacheApplyConservesTokensAndPreservesRealUsage(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	simulator := newClaudeMockCacheSimulator(nil)
	simulator.now = func() time.Time { return base }
	account := &Account{
		ID:       6,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			"mock_cache_enabled":        true,
			"mock_cache_target_percent": 91,
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

func TestClaudeMockCacheTargetIsDeterministicAndBounded(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 17, 30, 0, time.UTC)
	first := claudeMockCacheTargetBasisPoints(7, 91, now)
	second := claudeMockCacheTargetBasisPoints(7, 91, now)
	if first != second {
		t.Fatalf("target is not deterministic: %d != %d", first, second)
	}
	if first < 8500 || first > 9700 {
		t.Fatalf("target %d is outside configured +/-1 hour offset and +/-5 gaussian bound", first)
	}
	for slot := int64(0); slot < 12; slot++ {
		deviation := claudeMockCacheGaussian(7, now.Unix()/int64(time.Hour/time.Second), slot)
		if deviation < -5 || deviation > 5 {
			t.Fatalf("slot %d deviation %f is outside +/-5", slot, deviation)
		}
	}
}

func TestNormalizeAnthropicMockCacheExtra(t *testing.T) {
	normalized, err := normalizeAnthropicMockCacheExtra(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		"mock_cache_enabled": true,
	})
	if err != nil {
		t.Fatalf("normalize enabled config: %v", err)
	}
	if normalized["mock_cache_target_percent"] != 91 {
		t.Fatalf("default target = %#v, want 91", normalized["mock_cache_target_percent"])
	}

	if _, err := normalizeAnthropicMockCacheExtra(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		"mock_cache_target_percent": 100,
	}); err == nil {
		t.Fatal("out-of-range target was accepted")
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
}
