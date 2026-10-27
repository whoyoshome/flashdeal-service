package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

const (
	testPostgresURL = "postgres://localhost:5432/db?sslmode=disable"
	//nolint:gosec // G101: test-only HMAC key, not a production credential.
	testJWTSecret = "JWT_SECRET=test-secret-at-least-32-bytes-long!!"
)

func baseEnv(extra ...string) []string {
	return append([]string{"POSTGRES_URL=" + testPostgresURL, testJWTSecret}, extra...)
}

func TestLoad(t *testing.T) {
	t.Parallel()

	t.Run("applies defaults when only required variables are set", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load(baseEnv())

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
		require.InDelta(t, 50.0, cfg.RateLimitRPS, 0)
		require.Equal(t, 100, cfg.RateLimitBurst)
		require.Equal(t, 32, cfg.StockShards)
		require.Equal(t, config.Outbox{
			Every: time.Second, BatchSize: 100, Stream: "flashdeal:events", StreamMaxLen: 100000,
		}, cfg.Outbox)
		require.Equal(t, testPostgresURL, cfg.Postgres.URL)
		require.EqualValues(t, 20, cfg.Postgres.MaxConns)
		require.EqualValues(t, 2, cfg.Postgres.MinConns)
		require.Equal(t, 5*time.Second, cfg.Postgres.StatementTimeout)
		require.Equal(t, "localhost:6379", cfg.Redis.Addr)
		require.Equal(t, 50, cfg.Redis.PoolSize)
		require.Equal(t, 500*time.Millisecond, cfg.Redis.DialTimeout)
		require.Equal(t, 500*time.Millisecond, cfg.Redis.ReadTimeout)
		require.Empty(t, cfg.Tracing.OTLPEndpoint, "tracing is off unless configured")
		require.InDelta(t, 1.0, cfg.Tracing.SampleRatio, 0)
		require.Equal(t, "test-secret-at-least-32-bytes-long!!", cfg.Auth.Secret)
		require.Equal(t, "flashdeal", cfg.Auth.Issuer)
	})

	t.Run("reads overrides from the environment", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load(baseEnv(
			"HTTP_ADDR=:9090",
			"LOG_LEVEL=debug",
			"SHUTDOWN_TIMEOUT=30s",
			"POSTGRES_MAX_CONNS=50",
			"REDIS_ADDR=redis:6379",
			"REDIS_READ_TIMEOUT=1s",
			"JWT_ISSUER=flashdeal-ci",
		))

		require.NoError(t, err)
		require.Equal(t, ":9090", cfg.HTTPAddr)
		require.Equal(t, slog.LevelDebug, cfg.LogLevel)
		require.Equal(t, 30*time.Second, cfg.ShutdownTimeout)
		require.EqualValues(t, 50, cfg.Postgres.MaxConns)
		require.Equal(t, "redis:6379", cfg.Redis.Addr)
		require.Equal(t, time.Second, cfg.Redis.ReadTimeout)
		require.Equal(t, "flashdeal-ci", cfg.Auth.Issuer)
	})

	t.Run("falls back to the default when a variable is set but empty", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load(baseEnv("HTTP_ADDR=", "REDIS_ADDR="))

		require.NoError(t, err)
		require.Equal(t, ":8080", cfg.HTTPAddr)
		require.Equal(t, "localhost:6379", cfg.Redis.Addr)
	})

	t.Run("returns EmptyVarError when POSTGRES_URL is set but empty", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load([]string{"POSTGRES_URL=", testJWTSecret})

		var empty env.EmptyVarError
		require.ErrorAs(t, err, &empty)
		require.Equal(t, "POSTGRES_URL", empty.Key)
	})

	t.Run("returns VarIsNotSetError when POSTGRES_URL is missing", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load([]string{testJWTSecret})

		var notSet env.VarIsNotSetError
		require.ErrorAs(t, err, &notSet)
		require.Equal(t, "POSTGRES_URL", notSet.Key)
	})

	t.Run("rejects a missing JWT_SECRET when JWKS is not configured", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load([]string{"POSTGRES_URL=" + testPostgresURL})

		require.ErrorIs(t, err, config.ErrInvalid)
	})

	t.Run("allows an empty JWT_SECRET when JWKS URL is set", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.Load([]string{
			"POSTGRES_URL=" + testPostgresURL,
			"JWT_JWKS_URL=https://idp.example/.well-known/jwks.json",
		})

		require.NoError(t, err)
		require.Equal(t, "https://idp.example/.well-known/jwks.json", cfg.Auth.JWKSURL)
		require.Empty(t, cfg.Auth.Secret)
	})

	t.Run("returns a parse error when LOG_LEVEL is unknown", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load(baseEnv("LOG_LEVEL=loud"))

		var parseErr env.ParseError
		require.ErrorAs(t, err, &parseErr)
	})
}

func TestLoadMigrate(t *testing.T) {
	t.Parallel()

	t.Run("needs only POSTGRES_URL, not the API's secrets", func(t *testing.T) {
		t.Parallel()

		cfg, err := config.LoadMigrate([]string{"POSTGRES_URL=" + testPostgresURL})

		require.NoError(t, err)
		require.Equal(t, testPostgresURL, cfg.Postgres.URL)
		require.Equal(t, slog.LevelInfo, cfg.LogLevel)
		require.Equal(t, 5*time.Second, cfg.Postgres.ConnectTimeout)
		require.Equal(t, 5*time.Second, cfg.Postgres.StatementTimeout)
	})

	t.Run("returns VarIsNotSetError when POSTGRES_URL is missing", func(t *testing.T) {
		t.Parallel()

		_, err := config.LoadMigrate(nil)

		var notSet env.VarIsNotSetError
		require.ErrorAs(t, err, &notSet)
		require.Equal(t, "POSTGRES_URL", notSet.Key)
	})

	t.Run("rejects a non-positive statement timeout", func(t *testing.T) {
		t.Parallel()

		_, err := config.LoadMigrate([]string{"POSTGRES_URL=" + testPostgresURL, "POSTGRES_STATEMENT_TIMEOUT=0s"})

		require.ErrorIs(t, err, config.ErrInvalid)
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
		{name: "rejects a short JWT secret", env: []string{"JWT_SECRET=too-short-to-be-safe"}},
		{name: "rejects a negative rate limit", env: []string{"RATE_LIMIT_RPS=-1"}},
		{name: "rejects zero stock shards", env: []string{"STOCK_SHARDS=0"}},
		{name: "rejects more stock shards than the maximum", env: []string{"STOCK_SHARDS=257"}},
		{name: "rejects a non-positive outbox interval", env: []string{"OUTBOX_EVERY=0s"}},
		{name: "rejects a non-positive outbox batch size", env: []string{"OUTBOX_BATCH_SIZE=0"}},
		{name: "rejects a non-positive outbox stream length", env: []string{"OUTBOX_STREAM_MAXLEN=0"}},
		{name: "rejects a non-positive burst when rate limiting is on", env: []string{"RATE_LIMIT_RPS=10", "RATE_LIMIT_BURST=0"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(baseEnv(tc.env...))

			require.ErrorIs(t, err, config.ErrInvalid)
		})
	}
}
