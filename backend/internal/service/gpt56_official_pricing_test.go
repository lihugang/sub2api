package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Official API rates checked 2026-10-07: https://developers.openai.com/api/docs/pricing.
func TestGPT56OfficialBillingAcrossSourcesTiersAndBoundary(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	for _, source := range []string{"catalog", "pricing_fallback", "billing_fallback"} {
		var pricing *PricingService
		if source != "billing_fallback" {
			pricing = &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}
			if source == "catalog" {
				pricing.pricingData, err = pricing.parsePricingData(body)
				require.NoError(t, err)
			}
		}
		billing := NewBillingService(&config.Config{}, pricing)
		for _, model := range []struct {
			name                         string
			input, cached, write, output float64
		}{
			{"gpt-5.6", 4, .4, 5, 20}, {"gpt-5.6-sol", 4, .4, 5, 20},
			{"gpt-5.6-terra", 2, .2, 2.5, 12}, {"gpt-5.6-luna", .2, .02, .25, 1.2},
		} {
			for _, tier := range []struct {
				name  string
				scale float64
			}{{"", 1}, {"priority", 2}, {"fast", 2}, {"flex", .5}} {
				for _, input := range []int{100000, 100001} {
					t.Run(source+"/"+model.name+"/"+tier.name+"/"+string(rune(input-100000+'0')), func(t *testing.T) {
						tokens := UsageTokens{InputTokens: input, CacheReadTokens: 72000, CacheCreationTokens: 100000, OutputTokens: 1000}
						cost, err := billing.CalculateCostWithServiceTier("openai/"+model.name, tokens, 1, tier.name)
						require.NoError(t, err)
						inScale, outScale := 1.0, 1.0
						if input > 100000 {
							inScale, outScale = 2, 1.5
						}
						require.Equal(t, input > 100000, cost.LongContextBillingApplied)
						require.InDelta(t, float64(input)*model.input*1e-6*tier.scale*inScale, cost.InputCost, 1e-10)
						require.InDelta(t, 72000*model.cached*1e-6*tier.scale*inScale, cost.CacheReadCost, 1e-10)
						require.InDelta(t, 100000*model.write*1e-6*tier.scale*inScale, cost.CacheCreationCost, 1e-10)
						require.InDelta(t, 1000*model.output*1e-6*tier.scale*outScale, cost.OutputCost, 1e-10)
					})
				}
			}
		}
		for _, model := range []string{"gpt-5.6-cyber", "openai/gpt-5.6-cyber", "gpt-5.6-cyber-20261001"} {
			t.Run(source+"/"+model, func(t *testing.T) {
				cost, err := billing.CalculateCost(model, UsageTokens{InputTokens: 300000, CacheReadTokens: 1000, CacheCreationTokens: 1000, OutputTokens: 1000}, 1)
				require.NoError(t, err)
				require.InDelta(t, 3.75, cost.InputCost, 1e-12)
				require.InDelta(t, .00125, cost.CacheReadCost, 1e-12)
				require.InDelta(t, .015625, cost.CacheCreationCost, 1e-12)
				require.InDelta(t, .075, cost.OutputCost, 1e-12)
				require.False(t, cost.LongContextBillingApplied)
			})
		}
	}
}

func TestGPT56LegacyRemoteCatalogCorrectionAndOverride(t *testing.T) {
	legacy := `{"gpt-5.6-sol":{"litellm_provider":"openai","input_cost_per_token":0.000005,"output_cost_per_token":0.00003,"input_cost_per_token_priority":0.00001,"output_cost_per_token_priority":0.00006,"cache_creation_input_token_cost":0.00000625,"cache_read_input_token_cost":0.0000005,"input_cost_per_token_above_272k_tokens":0.00001,"output_cost_per_token_above_272k_tokens":0.000045,"input_cost_per_token_flex":0.0000025,"output_cost_per_token_batches":0.000015}}`
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(legacy))
	require.NoError(t, err)
	p := data["gpt-5.6-sol"]
	require.InDelta(t, 4e-6, p.InputCostPerToken, 1e-12)
	require.InDelta(t, 20e-6, p.OutputCostPerToken, 1e-12)
	require.InDelta(t, 8e-6, p.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 40e-6, p.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-6, p.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, .4e-6, p.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, p.LongContextInputTokenThreshold)
	require.InDelta(t, 2, p.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, p.LongContextOutputCostMultiplier, 1e-12)
	// A second load is idempotent, and new upstream prices are not rewritten.
	raw := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(legacy), &raw))
	correctLegacyGPT56SolPricing(raw)
	once := string(raw["gpt-5.6-sol"])
	correctLegacyGPT56SolPricing(raw)
	require.Equal(t, once, string(raw["gpt-5.6-sol"]))
	require.Contains(t, once, `"output_cost_per_token_batches":0.00001`)
	for _, override := range []string{`{"gpt-5.6-sol":{"input_cost_per_token":0.000009,"output_cost_per_token":0.00007,"long_context_input_token_threshold":0}}`, `{"gpt-5.6-sol":{"input_cost_per_token":0.000005,"output_cost_per_token":0.00003,"long_context_input_token_threshold":0}}`} {
		svc := newPricingServiceWithOverride(t, override)
		data, err := svc.parsePricingData([]byte(legacy))
		require.NoError(t, err)
		var expected map[string]struct {
			Input  float64 `json:"input_cost_per_token"`
			Output float64 `json:"output_cost_per_token"`
		}
		require.NoError(t, json.Unmarshal([]byte(override), &expected))
		require.InDelta(t, expected["gpt-5.6-sol"].Input, data["gpt-5.6-sol"].InputCostPerToken, 1e-12)
		require.InDelta(t, expected["gpt-5.6-sol"].Output, data["gpt-5.6-sol"].OutputCostPerToken, 1e-12)
		require.Zero(t, data["gpt-5.6-sol"].LongContextInputTokenThreshold)
	}
	newer := `{"gpt-5.6-sol":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000015}}`
	data, err = svc.parsePricingData([]byte(newer))
	require.NoError(t, err)
	require.InDelta(t, 3e-6, data["gpt-5.6-sol"].InputCostPerToken, 1e-12)
}
