// Package config loads and validates the service configuration from the
// environment, once, at startup.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
)

// reconcileMargin is how much longer than a request a reconciliation pass
// must take; see Config.ReconcileEvery.
const reconcileMargin = 5 * time.Second

// ErrInvalid is returned when the configuration parses but breaks a rule
// the service needs to run safely (for example, a non-positive timeout).
var ErrInvalid = errors.New("invalid config")

// Config is the full service configuration. It is built once in cmd/api and
// passed down as values; no other package reads the environment.
type Config struct {
	HTTPAddr         string        `env:"HTTP_ADDR"         envDefault:":8080"`
	AdminAddr        string        `env:"ADMIN_ADDR"        envDefault:":6060"` // pprof: never expose publicly
	LogLevel         slog.Level    `env:"LOG_LEVEL"         envDefault:"info"`
	ShutdownTimeout  time.Duration `env:"SHUTDOWN_TIMEOUT"  envDefault:"15s"`
	ReadinessTimeout time.Duration `env:"READINESS_TIMEOUT" envDefault:"2s"`
	// RequestTimeout bounds each /v1 request; keep it below the server's
	// 10s write timeout so the handler, not the connection, times out first.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"5s"`
	// ReservationTTL is how long a reservation holds its units.
	ReservationTTL time.Duration `env:"RESERVATION_TTL" envDefault:"5m"`
	// IdempotencyTTL is how long a reservation response is replayed for a
	// retried Idempotency-Key.
	IdempotencyTTL time.Duration `env:"IDEMPOTENCY_TTL" envDefault:"24h"`
	// DealCacheTTL is how long a deal snapshot is served from Redis (1-5s by
	// design): long enough to absorb an opening burst, short enough that the
	// approximate stock shown to clients stays close to reality.
	DealCacheTTL time.Duration `env:"DEAL_CACHE_TTL" envDefault:"2s"`
	// ExpireEvery is how often each replica looks for reservations whose TTL
	// elapsed; units of an abandoned reservation return at most this late.
	ExpireEvery time.Duration `env:"EXPIRE_EVERY" envDefault:"5s"`
	// ExpireBatchSize bounds how many reservations one expiry transaction
	// locks, keeping row locks on the deals short.
	ExpireBatchSize int `env:"EXPIRE_BATCH_SIZE" envDefault:"100"`
	// ReconcileEvery is how often each replica compares the Redis gate with
	// PostgreSQL. A drift is repaired only after it stood still for a whole
	// interval, which proves nothing was in flight only if the interval
	// outlasts the longest operation: see validate.
	ReconcileEvery time.Duration `env:"RECONCILE_EVERY" envDefault:"30s"`
	// StockSampleEvery is how often each replica publishes the stock left per
	// active deal (stock_remaining). Match it to the Prometheus scrape interval.
	StockSampleEvery time.Duration `env:"STOCK_SAMPLE_EVERY" envDefault:"5s"`

	Postgres Postgres `envPrefix:"POSTGRES_"`
	Redis    Redis    `envPrefix:"REDIS_"`
}

// Postgres configures the pgx connection pool.
type Postgres struct {
	// URL holds credentials: never log it.
	URL              string        `env:"URL,required,notEmpty"`
	MaxConns         int32         `env:"MAX_CONNS"         envDefault:"20"`
	MinConns         int32         `env:"MIN_CONNS"         envDefault:"2"`
	ConnectTimeout   time.Duration `env:"CONNECT_TIMEOUT"   envDefault:"5s"`
	StatementTimeout time.Duration `env:"STATEMENT_TIMEOUT" envDefault:"5s"`
}

// Redis configures the go-redis client.
type Redis struct {
	Addr string `env:"ADDR" envDefault:"localhost:6379"`
	// Password is a secret: never log it.
	Password     string        `env:"PASSWORD"`
	DB           int           `env:"DB"            envDefault:"0"`
	PoolSize     int           `env:"POOL_SIZE"     envDefault:"50"`
	DialTimeout  time.Duration `env:"DIAL_TIMEOUT"  envDefault:"2s"`
	ReadTimeout  time.Duration `env:"READ_TIMEOUT"  envDefault:"500ms"`
	WriteTimeout time.Duration `env:"WRITE_TIMEOUT" envDefault:"500ms"`
}

// Load parses configuration from environ (KEY=value pairs, as returned by
// os.Environ) and validates it. Taking the environment as an argument keeps
// tests hermetic and confines environment access to cmd/api.
func Load(environ []string) (Config, error) {
	cfg, err := env.ParseAsWithOptions[Config](env.Options{Environment: env.ToMap(environ)})
	if err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate checks rules that parsing alone cannot express. Empty strings need
// no check: env falls back to envDefault when a variable is set but empty.
func (c Config) validate() error {
	errs := []error{
		positive("SHUTDOWN_TIMEOUT", c.ShutdownTimeout),
		positive("READINESS_TIMEOUT", c.ReadinessTimeout),
		positive("REQUEST_TIMEOUT", c.RequestTimeout),
		positive("RESERVATION_TTL", c.ReservationTTL),
		positive("IDEMPOTENCY_TTL", c.IdempotencyTTL),
		positive("DEAL_CACHE_TTL", c.DealCacheTTL),
		positive("EXPIRE_EVERY", c.ExpireEvery),
		positive("RECONCILE_EVERY", c.ReconcileEvery),
		positive("STOCK_SAMPLE_EVERY", c.StockSampleEvery),
		positive("POSTGRES_CONNECT_TIMEOUT", c.Postgres.ConnectTimeout),
		positive("POSTGRES_STATEMENT_TIMEOUT", c.Postgres.StatementTimeout),
		positive("REDIS_DIAL_TIMEOUT", c.Redis.DialTimeout),
		positive("REDIS_READ_TIMEOUT", c.Redis.ReadTimeout),
		positive("REDIS_WRITE_TIMEOUT", c.Redis.WriteTimeout),
	}
	if c.Postgres.MaxConns <= 0 {
		errs = append(errs, errors.New("POSTGRES_MAX_CONNS must be positive"))
	}
	if c.Postgres.MinConns < 0 || c.Postgres.MinConns > c.Postgres.MaxConns {
		errs = append(errs, errors.New("POSTGRES_MIN_CONNS must be between 0 and POSTGRES_MAX_CONNS"))
	}
	if c.ExpireBatchSize <= 0 {
		errs = append(errs, errors.New("EXPIRE_BATCH_SIZE must be positive"))
	}
	// The longest operation that can leave Redis and PostgreSQL disagreeing is
	// a reservation: REQUEST_TIMEOUT, then a detached COMMIT (2s) and a gate
	// compensation (2s). The extra second is margin.
	if c.ReconcileEvery <= c.RequestTimeout+reconcileMargin {
		errs = append(errs, fmt.Errorf("RECONCILE_EVERY must exceed REQUEST_TIMEOUT by more than %s", reconcileMargin))
	}
	if c.Redis.PoolSize <= 0 {
		errs = append(errs, errors.New("REDIS_POOL_SIZE must be positive"))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func positive(name string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}
