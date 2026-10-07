package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPricingRemoteSyncDefaultOffAndExplicitOptIn(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("PRICING_REMOTE_SYNC_ENABLED", "false")
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Pricing.RemoteSyncEnabled)
	t.Setenv("PRICING_REMOTE_SYNC_ENABLED", "true")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.Pricing.RemoteSyncEnabled)
}
