package redisrepo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

func TestRedisOptionsFromURL(t *testing.T) {
	t.Parallel()

	//nolint:gosec // G101: fake Upstash-shaped URL for ParseURL coverage only.
	const url = "rediss://default:token@example.upstash.io:6379"
	opt, err := redisOptions(config.Redis{
		URL:          url,
		PoolSize:     10,
		DialTimeout:  time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: time.Second,
	})
	require.NoError(t, err)
	require.Equal(t, "example.upstash.io:6379", opt.Addr)
	require.Equal(t, "token", opt.Password)
	require.NotNil(t, opt.TLSConfig)
	require.Equal(t, 10, opt.PoolSize)
	require.Equal(t, -1, opt.MaxRetries)
}

func TestRedisOptionsRejectsBadURL(t *testing.T) {
	t.Parallel()
	_, err := redisOptions(config.Redis{URL: "://bad"})
	require.Error(t, err)
}
