package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheClaudeMockCacheLifecycle(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	store, ok := NewGatewayCache(client).(service.ClaudeMockCacheStore)
	require.True(t, ok)

	req := service.ClaudeMockCacheClassifyRequest{
		AccountID: 42,
		PrefixTTL: 5 * time.Minute,
		Prefixes: []service.ClaudeMockCachePrefix{
			{Hash: "stable", Tokens: 60},
			{Hash: "tail", Tokens: 40},
		},
	}

	first, err := store.ClassifyClaudeMockCache(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 100, first.CacheCreationTokens)
	require.Zero(t, first.CacheReadTokens)

	second, err := store.ClassifyClaudeMockCache(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 100, second.CacheReadTokens)
	require.Zero(t, second.CacheCreationTokens)

	req.ForceMiss = true
	forced, err := store.ClassifyClaudeMockCache(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 100, forced.CacheCreationTokens)
	require.Zero(t, forced.CacheReadTokens)

	req.ForceMiss = false
	redisServer.FastForward(4 * time.Minute)
	refreshed, err := store.ClassifyClaudeMockCache(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 100, refreshed.CacheReadTokens)

	redisServer.FastForward(5*time.Minute + time.Second)
	expired, err := store.ClassifyClaudeMockCache(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 100, expired.CacheCreationTokens)
	require.Zero(t, expired.CacheReadTokens)
}
