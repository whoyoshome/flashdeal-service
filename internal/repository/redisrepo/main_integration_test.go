//go:build integration

package redisrepo_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
)

// testRedis is shared by every test in the package. Tests isolate themselves
// by using their own deal IDs, so they can run in parallel.
var testRedis *redis.Client

func TestMain(m *testing.M) {
	code, err := runIntegration(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration setup: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runIntegration(m *testing.M) (int, error) {
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, "redis:7.4-alpine",
		testcontainers.WithExposedPorts("6379/tcp"),
		// The log line comes before the published port accepts connections, and
		// the client below dials once (it never retries), so wait for both.
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForLog("Ready to accept connections"),
			wait.ForListeningPort("6379/tcp"),
		)),
	)
	if err != nil {
		return 0, fmt.Errorf("start redis: %w", err)
	}
	defer func() { _ = ctr.Terminate(context.Background()) }()

	addr, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		return 0, fmt.Errorf("redis endpoint: %w", err)
	}
	testRedis, err = redisrepo.NewClient(ctx, redisConfig(addr))
	if err != nil {
		return 0, fmt.Errorf("open redis client: %w", err)
	}
	defer func() { _ = testRedis.Close() }()

	return m.Run(), nil
}

func redisConfig(addr string) config.Redis {
	return config.Redis{
		Addr:         addr,
		PoolSize:     100,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
}

var dealSeq atomic.Int64

// newDeal returns an active deal with a unique ID, stock units available
// and the given per-user limit.
func newDeal(t *testing.T, stock, limit int) domain.Deal {
	t.Helper()
	price, err := domain.NewMoney(4999, "USD")
	require.NoError(t, err)
	now := time.Now().UTC()
	d, err := domain.RestoreDeal(domain.DealState{
		ID: 1_000_000 + dealSeq.Add(1), ProductID: "sku", Title: "Gate test", Price: price,
		TotalStock: stock, PerUserLimit: limit,
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	return d
}

func stockLeft(t *testing.T, dealID int64) int {
	t.Helper()
	n, err := testRedis.Get(t.Context(), fmt.Sprintf("deal:{%d}:stock", dealID)).Int()
	require.NoError(t, err)
	return n
}
