package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPricingRemoteSyncDisabledUsesBundledPricesInsteadOfStaleCache(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "must not fetch", 500) }))
	defer server.Close()
	dir := t.TempDir()
	fallback := filepath.Join(dir, "bundled.json")
	override := filepath.Join(dir, "override.json")
	require.NoError(t, os.WriteFile(fallback, []byte(`{"gpt-5.6-sol":{"input_cost_per_token":0.000004,"output_cost_per_token":0.00002}}`), 0644))
	require.NoError(t, os.WriteFile(override, []byte(`{"gpt-5.6-sol":{"output_cost_per_token":0.000021}}`), 0644))
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir: dir, FallbackFile: fallback, OverrideFile: override, RemoteURL: server.URL, HashURL: server.URL + "/hash",
	}}, nil)
	defer svc.Stop()
	require.NoError(t, os.WriteFile(svc.getPricingFilePath(), []byte(`{"gpt-5.6-sol":{"input_cost_per_token":0.000005,"output_cost_per_token":0.00003}}`), 0644))
	require.NoError(t, svc.Initialize())
	require.InDelta(t, 4e-6, svc.GetModelPricing("gpt-5.6-sol").InputCostPerToken, 1e-12)
	require.InDelta(t, 21e-6, svc.GetModelPricing("gpt-5.6-sol").OutputCostPerToken, 1e-12)
	require.NoError(t, svc.syncWithRemote())
	require.NoError(t, svc.ForceUpdate())
	require.NoError(t, svc.downloadPricingData())
	require.Zero(t, requests.Load())
	// Local changes remain reloadable even with an old downloaded cache present.
	require.NoError(t, os.WriteFile(fallback, []byte(`{"gpt-5.6-sol":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000015}}`), 0644))
	svc.reloadIfCustomFilesChanged()
	require.InDelta(t, 3e-6, svc.GetModelPricing("gpt-5.6-sol").InputCostPerToken, 1e-12)
	require.InDelta(t, 21e-6, svc.GetModelPricing("gpt-5.6-sol").OutputCostPerToken, 1e-12)
	require.Zero(t, requests.Load())
}

func TestPricingRemoteSyncRequiresExplicitEnableAndURL(t *testing.T) {
	svc := &PricingService{cfg: &config.Config{}}
	svc.cfg.Pricing.RemoteURL = "https://example.com/prices.json"
	require.False(t, svc.remotePricingSyncEnabled())
	svc.cfg.Pricing.RemoteSyncEnabled = true
	require.True(t, svc.remotePricingSyncEnabled())
	svc.cfg.Pricing.RemoteURL = " \t "
	require.False(t, svc.remotePricingSyncEnabled())
}
