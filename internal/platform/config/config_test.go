package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

const testPostgresURL = "postgres://localhost:5432/db?sslmode=disable"

func TestLoad(t *testing.T) {
	t.Parallel()

	t.Run("applies defaults when only required variables are set", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load([]string{"POSTGRES_URL=" + testPostgresURL})

		require.NoError(t, err)
		require.Equal(t, ":8080", cfg.HTTPAddr)
		require.Equal(t, ":6060", cfg.AdminAddr)
		require.Equal(t, slog.LevelInfo, cfg.LogLevel)
		require.Equal(t, 15*time.Second, cfg.ShutdownTimeout)
		require.Equal(t, 2*time.Second, cfg.ReadinessTimeout)
		require.Equal(t, 5*time.Second, cfg.RequestTimeout)
		require.Equal(t, 5*time.Minute, cfg.ReservationTTL)
		require.Equal(t, 24*time.Hour, cfg.IdempotencyTTL)
		require.Equal(t, 2*time.Second, cfg.DealCacheTTL)
		require.Equal(t, 5*time.Second, cfg.ExpireEvery)
		require.Equal(t, 100, cfg.ExpireBatchSize)
		require.Equal(t, 30*time.Second, cfg.ReconcileEvery)
		require.Equal(t, 5*time.Second, cfg.StockSampleEvery)
		require.Equal(t, testPostgresURL, cfg.Postgres.URL)
		require.EqualValues(t, 20, cfg.Postgres.MaxConns)
		require.EqualValues(t, 2, cfg.Postgres.MinConns)
		require.Equal(t, 5*time.Second, cfg.Postgres.StatementTimeout)
		require.Equal(t, "localhost:6379", cfg.Redis.Addr)
		require.Equal(t, 50, cfg.Redis.PoolSize)
		require.Equal(t, 500*time.Millisecond, cfg.Redis.ReadTimeout)
		require.Empty(t, cfg.Tracing.OTLPEndpoint, "tracing is off unless configured")
		require.InDelta(t, 1.0, cfg.Tracing.SampleRatio, 0)
	})

	t.Run("reads overrides from the environment", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load([]string{
			"POSTGRES_URL=" + testPostgresURL,
			"HTTP_ADDR=:9090",
			"LOG_LEVEL=debug",
			"SHUTDOWN_TIMEOUT=30s",
			"POSTGRES_MAX_CONNS=50",
			"REDIS_ADDR=redis:6379",
			"REDIS_READ_TIMEOUT=1s",
		})

		require.NoError(t, err)
		require.Equal(t, ":9090", cfg.HTTPAddr)
		require.Equal(t, slog.LevelDebug, cfg.LogLevel)
		require.Equal(t, 30*time.Second, cfg.ShutdownTimeout)
		require.EqualValues(t, 50, cfg.Postgres.MaxConns)
		require.Equal(t, "redis:6379", cfg.Redis.Addr)
		require.Equal(t, time.Second, cfg.Redis.ReadTimeout)
	})

	t.Run("falls back to the default when a variable is set but empty", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load([]string{"POSTGRES_URL=" + testPostgresURL, "HTTP_ADDR=", "REDIS_ADDR="})

		require.NoError(t, err)
		require.Equal(t, ":8080", cfg.HTTPAddr)
		require.Equal(t, "localhost:6379", cfg.Redis.Addr)
	})

	t.Run("returns EmptyVarError when POSTGRES_URL is set but empty", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load([]string{"POSTGRES_URL="})

		var empty env.EmptyVarError
		require.ErrorAs(t, err, &empty)
		require.Equal(t, "POSTGRES_URL", empty.Key)
	})

	t.Run("returns VarIsNotSetError when POSTGRES_URL is missing", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load(nil)

		var notSet env.VarIsNotSetError
		require.ErrorAs(t, err, &notSet)
		require.Equal(t, "POSTGRES_URL", notSet.Key)
	})

	t.Run("returns a parse error when LOG_LEVEL is unknown", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load([]string{"POSTGRES_URL=" + testPostgresURL, "LOG_LEVEL=loud"})

		var parseErr env.ParseError
		require.ErrorAs(t, err, &parseErr)
	})
}

func TestLoadValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
	}{
		{name: "rejects a non-positive shutdown timeout", env: []string{"SHUTDOWN_TIMEOUT=0s"}},
		{name: "rejects a non-positive readiness timeout", env: []string{"READINESS_TIMEOUT=-1s"}},
		{name: "rejects a non-positive request timeout", env: []string{"REQUEST_TIMEOUT=0s"}},
		{name: "rejects a non-positive reservation TTL", env: []string{"RESERVATION_TTL=0s"}},
		{name: "rejects a non-positive idempotency TTL", env: []string{"IDEMPOTENCY_TTL=0s"}},
		{name: "rejects a non-positive deal cache TTL", env: []string{"DEAL_CACHE_TTL=0s"}},
		{name: "rejects a non-positive expiry interval", env: []string{"EXPIRE_EVERY=0s"}},
		{name: "rejects a non-positive expiry batch size", env: []string{"EXPIRE_BATCH_SIZE=0"}},
		{name: "rejects a non-positive reconciliation interval", env: []string{"RECONCILE_EVERY=0s"}},
		{name: "rejects a non-positive stock sample interval", env: []string{"STOCK_SAMPLE_EVERY=0s"}},
		{name: "rejects a negative trace sample ratio", env: []string{"TRACING_SAMPLE_RATIO=-0.1"}},
		{name: "rejects a trace sample ratio above 1", env: []string{"TRACING_SAMPLE_RATIO=1.5"}},
		{
			name: "rejects a reconciliation interval an in-flight reservation could outlast",
			env:  []string{"REQUEST_TIMEOUT=5s", "RECONCILE_EVERY=10s"},
		},
		{name: "rejects a non-positive statement timeout", env: []string{"POSTGRES_STATEMENT_TIMEOUT=0s"}},
		{name: "rejects zero max connections", env: []string{"POSTGRES_MAX_CONNS=0"}},
		{name: "rejects min connections above max connections", env: []string{"POSTGRES_MAX_CONNS=5", "POSTGRES_MIN_CONNS=6"}},
		{name: "rejects negative min connections", env: []string{"POSTGRES_MIN_CONNS=-1"}},
		{name: "rejects a non-positive Redis pool size", env: []string{"REDIS_POOL_SIZE=0"}},
		{name: "rejects a non-positive Redis read timeout", env: []string{"REDIS_READ_TIMEOUT=0s"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(append([]string{"POSTGRES_URL=" + testPostgresURL}, tc.env...))

			require.ErrorIs(t, err, config.ErrInvalid)
		})
	}
}
