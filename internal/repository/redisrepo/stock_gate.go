package redisrepo

import (
	"context"
	_ "embed" // Lua scripts are compiled into the binary
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var (
	_ usecase.StockGate  = (*StockGate)(nil)
	_ usecase.GateRepair = (*StockGate)(nil)
)

var (
	//go:embed scripts/reserve.lua
	reserveLua string
	//go:embed scripts/release.lua
	releaseLua string
	//go:embed scripts/restore.lua
	restoreLua string
)

// Script results with a business meaning (see the scripts' headers).
const (
	resultSoldOut   = -1
	resultNotLoaded = -2
	resultUserLimit = -3
)

// keyTTLMargin keeps a deal's keys alive a while after it ends, so late
// cancellations and expiries can still give units back.
const keyTTLMargin = time.Hour

// StockGate is the Redis-backed stock gate. Only as many requests as there
// are units ever reach PostgreSQL; every other one is rejected here, in
// memory, in a single round trip. PostgreSQL constraints stay the source of
// truth if the two ever diverge.
type StockGate struct {
	rdb     *redis.Client
	reserve *redis.Script
	release *redis.Script
	restore *redis.Script
}

// NewStockGate returns a gate using rdb. Scripts run via EVALSHA, falling
// back to EVAL the first time a server has not cached them.
func NewStockGate(rdb *redis.Client) *StockGate {
	return &StockGate{
		rdb:     rdb,
		reserve: redis.NewScript(reserveLua),
		release: redis.NewScript(releaseLua),
		restore: redis.NewScript(restoreLua),
	}
}

// stockKey and buyersKey share the {id} hash tag so both land in the same
// slot under Redis Cluster, which multi-key scripts require.
func stockKey(dealID int64) string  { return fmt.Sprintf("deal:{%d}:stock", dealID) }
func buyersKey(dealID int64) string { return fmt.Sprintf("deal:{%d}:buyers", dealID) }

// Load opens the deal's stock with the units PostgreSQL reports available.
// SET NX makes it idempotent: activating again never resets a live counter
// that reservations have already moved.
func (g *StockGate) Load(ctx context.Context, d domain.Deal) error {
	err := g.rdb.SetArgs(ctx, stockKey(d.ID()), d.Available(), redis.SetArgs{
		Mode:     "NX",
		ExpireAt: d.EndsAt().Add(keyTTLMargin),
	}).Err()
	if err != nil && !errors.Is(err, redis.Nil) { // redis.Nil: already loaded
		return unavailable("load stock", err)
	}
	return nil
}

// TryReserve takes qty units for userID atomically, enforcing the cumulative
// per-user limit across all of the user's reservations.
func (g *StockGate) TryReserve(ctx context.Context, dealID int64, userID string, qty, perUserLimit int) error {
	res, err := g.reserve.Run(ctx, g.rdb, []string{stockKey(dealID), buyersKey(dealID)}, userID, qty, perUserLimit).Int64()
	if err != nil {
		return unavailable("reserve script", err)
	}
	switch res {
	case resultSoldOut:
		return domain.ErrSoldOut
	case resultNotLoaded:
		return domain.ErrDealNotActive
	case resultUserLimit:
		return domain.ErrUserLimitReached
	}
	return nil
}

// Release gives back units taken by TryReserve. A deal whose keys are gone
// has nothing to give back to, which is not an error.
func (g *StockGate) Release(ctx context.Context, dealID int64, userID string, qty int) error {
	if err := g.release.Run(ctx, g.rdb, []string{stockKey(dealID), buyersKey(dealID)}, userID, qty).Err(); err != nil {
		return unavailable("release script", err)
	}
	return nil
}

// Stock returns the units the gate has left for dealID, or loaded false when
// the deal's stock is not in Redis.
func (g *StockGate) Stock(ctx context.Context, dealID int64) (int, bool, error) {
	stock, err := g.rdb.Get(ctx, stockKey(dealID)).Int()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, unavailable("read stock", err)
	}
	return stock, true, nil
}

// Restore replaces the deal's stock and buyers with want if the stock still
// equals expected. The buyers are rewritten from scratch, so entries left at
// zero by releases disappear.
func (g *StockGate) Restore(ctx context.Context, dealID int64, expected int, want usecase.GateState) (bool, error) {
	args := make([]any, 0, 2+2*len(want.Held))
	args = append(args, expected, want.Available)
	for _, user := range slices.Sorted(maps.Keys(want.Held)) {
		args = append(args, user, want.Held[user])
	}
	applied, err := g.restore.Run(ctx, g.rdb, []string{stockKey(dealID), buyersKey(dealID)}, args...).Int()
	if err != nil {
		return false, unavailable("restore script", err)
	}
	return applied == 1, nil
}

// unavailable reports a Redis failure as ErrStockUnavailable so the service
// fails closed (503) instead of selling without its gate. The driver error is
// kept as text only, so no go-redis type escapes this package.
func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %s", domain.ErrStockUnavailable, op, err.Error())
}
