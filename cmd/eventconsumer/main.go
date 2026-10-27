// Command eventconsumer is a sample consumer of the flashdeal:events Redis
// Stream. It joins a consumer group, drops duplicates by event_id and logs
// each new order.confirmed. A real fulfilment service replaces the logging
// handler behind the same worker.EventHandler interface (ADR 0012).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/platform/logger"
	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
	"github.com/whoyoshome/flashdeal-service/internal/worker"
)

type consumerConfig struct {
	LogLevel slog.Level    `env:"LOG_LEVEL" envDefault:"info"`
	Redis    config.Redis  `envPrefix:"REDIS_"`
	Stream   string        `env:"OUTBOX_STREAM" envDefault:"flashdeal:events"`
	Group    string        `env:"EVENTS_GROUP" envDefault:"flashdeal-demo"`
	Consumer string        `env:"EVENTS_CONSUMER" envDefault:"demo-1"`
	Batch    int64         `env:"EVENTS_BATCH" envDefault:"100"`
	Block    time.Duration `env:"EVENTS_BLOCK" envDefault:"2s"`
	SeenTTL  time.Duration `env:"EVENTS_SEEN_TTL" envDefault:"168h"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "eventconsumer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg consumerConfig
	if err := env.Parse(&cfg); err != nil {
		return err
	}
	if cfg.Batch < 1 {
		return errors.New("EVENTS_BATCH must be at least 1")
	}
	// XREADGROUP blocks up to Block; the socket read timeout must outlast it.
	if cfg.Redis.ReadTimeout > 0 && cfg.Redis.ReadTimeout <= cfg.Block {
		cfg.Redis.ReadTimeout = cfg.Block + time.Second
	}

	log := logger.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb, err := redisrepo.NewClient(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	reader := redisrepo.NewEventConsumer(rdb, cfg.Stream, cfg.Group, cfg.Consumer, cfg.SeenTTL)
	return worker.NewEventConsumer(
		reader, worker.NewLoggingHandler(log), cfg.Batch, cfg.Block,
		nil, nopTracer{}, log,
	).Run(ctx)
}

type nopTracer struct{}

func (nopTracer) StartSpan(ctx context.Context, _ string) (context.Context, func(error)) {
	return ctx, func(error) {}
}
