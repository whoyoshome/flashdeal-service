package redisrepo

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.DealReader = (*DealCache)(nil)

// loadTimeout bounds one shared cache load. The load is detached from the
// caller that happened to start it, because other callers wait on it too.
const loadTimeout = 2 * time.Second

// cachedDeal is the JSON snapshot of a deal stored under deal:{id}:cache.
type cachedDeal struct {
	ID           int64     `json:"id"`
	ProductID    string    `json:"product_id"`
	Title        string    `json:"title"`
	PriceCents   int64     `json:"price_cents"`
	Currency     string    `json:"currency"`
	TotalStock   int       `json:"total_stock"`
	Reserved     int       `json:"reserved"`
	Sold         int       `json:"sold"`
	PerUserLimit int       `json:"per_user_limit"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DealCache serves deal reads from a short-lived Redis snapshot in front of
// the database. When a deal opens, thousands of identical reads arrive at
// once; singleflight collapses those of one process into a single load, and
// the shared snapshot spares PostgreSQL across replicas. The snapshot may be
// up to ttl old, which is fine for every reader of it: reservations only
// check the window and limits, never the stock counters.
type DealCache struct {
	rdb   *redis.Client
	src   usecase.DealReader
	ttl   time.Duration
	group singleflight.Group
}

// NewDealCache returns a cache over src that keeps snapshots for ttl.
func NewDealCache(rdb *redis.Client, src usecase.DealReader, ttl time.Duration) *DealCache {
	return &DealCache{rdb: rdb, src: src, ttl: ttl}
}

func cacheKey(dealID int64) string { return fmt.Sprintf("deal:{%d}:cache", dealID) }

// GetByID returns the deal from the cache, loading it from the source on a
// miss. Concurrent calls for the same deal share one load.
func (c *DealCache) GetByID(ctx context.Context, id int64) (domain.Deal, error) {
	v, err, _ := c.group.Do(strconv.FormatInt(id, 10), func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		return c.load(loadCtx, id)
	})
	if err != nil {
		return domain.Deal{}, err
	}
	return v.(domain.Deal), nil
}

func (c *DealCache) load(ctx context.Context, id int64) (domain.Deal, error) {
	if d, ok := c.read(ctx, id); ok {
		return d, nil
	}
	d, err := c.src.GetByID(ctx, id)
	if err != nil {
		return domain.Deal{}, err
	}
	c.write(ctx, d)
	return d, nil
}

// read returns the cached snapshot. Any failure (miss, Redis down, an entry
// written by an older version) counts as a miss: the cache only ever speeds
// reads up, so it must never be the reason one fails.
func (c *DealCache) read(ctx context.Context, id int64) (domain.Deal, bool) {
	raw, err := c.rdb.Get(ctx, cacheKey(id)).Bytes()
	if err != nil {
		return domain.Deal{}, false
	}
	var s cachedDeal
	if err := json.Unmarshal(raw, &s); err != nil {
		return domain.Deal{}, false
	}
	price, err := domain.NewMoney(s.PriceCents, s.Currency)
	if err != nil {
		return domain.Deal{}, false
	}
	d, err := domain.RestoreDeal(domain.DealState{
		ID: s.ID, ProductID: s.ProductID, Title: s.Title, Price: price,
		TotalStock: s.TotalStock, Reserved: s.Reserved, Sold: s.Sold, PerUserLimit: s.PerUserLimit,
		StartsAt: s.StartsAt, EndsAt: s.EndsAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	})
	if err != nil {
		return domain.Deal{}, false
	}
	return d, true
}

// write stores the snapshot. A failure only means the next read loads from
// the source again, so it is deliberately not reported.
func (c *DealCache) write(ctx context.Context, d domain.Deal) {
	raw, err := json.Marshal(cachedDeal{
		ID: d.ID(), ProductID: d.ProductID(), Title: d.Title(),
		PriceCents: d.Price().Amount(), Currency: d.Price().Currency(),
		TotalStock: d.TotalStock(), Reserved: d.Reserved(), Sold: d.Sold(), PerUserLimit: d.PerUserLimit(),
		StartsAt: d.StartsAt(), EndsAt: d.EndsAt(), CreatedAt: d.CreatedAt(), UpdatedAt: d.UpdatedAt(),
	})
	if err != nil {
		return
	}
	_ = c.rdb.Set(ctx, cacheKey(d.ID()), raw, c.ttl).Err() // best effort, see the doc comment
}
