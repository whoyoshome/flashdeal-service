package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var (
	_ usecase.DealRepository = (*DealRepo)(nil)
	_ usecase.DealStock      = (*DealRepo)(nil)
	_ usecase.StockLedger    = (*DealRepo)(nil)
)

const (
	// One statement creates the deal and its stock shards, so a deal never
	// exists without them. No shard holds fewer units than per_user_limit,
	// so any valid request fits in one shard of an untouched deal; the first
	// total_stock % shards shards take one unit more than the rest.
	insertDeal = `WITH d AS (
  INSERT INTO deals (product_id, title, price_cents, currency, total_stock, per_user_limit, starts_at, ends_at)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
  RETURNING id, product_id, title, price_cents, currency, total_stock, per_user_limit, starts_at, ends_at, created_at, updated_at
), n AS (
  SELECT GREATEST(1, LEAST($9::int, d.total_stock / d.per_user_limit)) AS shards FROM d
), s AS (
  INSERT INTO deal_stock_shards (deal_id, shard, capacity)
  SELECT d.id, g, d.total_stock / n.shards + CASE WHEN g < d.total_stock % n.shards THEN 1 ELSE 0 END
  FROM d, n, generate_series(0, n.shards - 1) AS g
)
SELECT id, product_id, title, price_cents, currency, total_stock, 0, 0,
  per_user_limit, starts_at, ends_at, created_at, updated_at
FROM d`

	selectDealByID = selectDeals + ` WHERE d.id = $1`

	selectActiveDeals = selectDeals + `
WHERE d.starts_at <= $1 AND d.ends_at > $1 AND d.id > $2
ORDER BY d.id
LIMIT $3`

	// The units move on the shard the reservation took them from. The WHERE
	// clause is evaluated after the row lock, so concurrent confirmations
	// can never push reserved below zero.
	commitReserved = `UPDATE deal_stock_shards AS s
SET reserved = s.reserved - r.quantity, sold = s.sold + r.quantity
FROM reservations AS r
WHERE r.id = $1 AND s.deal_id = r.deal_id AND s.shard = r.stock_shard AND s.reserved >= r.quantity`

	// Ordered by deal and shard so concurrent releases, which may span the
	// same shards, always lock them in the same order and cannot deadlock.
	selectReleaseGroups = `SELECT deal_id, stock_shard, sum(quantity)::int, count(*)::int
FROM reservations
WHERE id = ANY($1::uuid[])
GROUP BY deal_id, stock_shard
ORDER BY deal_id, stock_shard`

	releaseShard = `UPDATE deal_stock_shards SET reserved = reserved - $3
WHERE deal_id = $1 AND shard = $2 AND reserved >= $3`

	// One statement, so the counters and the holdings come from the same
	// snapshot. A deal nobody holds units of still returns one row, with a
	// NULL user.
	selectGateState = `SELECT d.total_stock - s.reserved - s.sold, h.user_id, h.held
FROM deals AS d
CROSS JOIN LATERAL (
  SELECT COALESCE(sum(reserved), 0)::int AS reserved, COALESCE(sum(sold), 0)::int AS sold
  FROM deal_stock_shards WHERE deal_id = d.id
) AS s
LEFT JOIN LATERAL (
  SELECT user_id, sum(quantity)::int AS held FROM reservations
  WHERE deal_id = d.id AND status IN ('pending', 'confirmed')
  GROUP BY user_id
) AS h ON true
WHERE d.id = $1`
)

// DealRepo persists deals and moves their stock counters.
type DealRepo struct {
	pool   *pgxpool.Pool
	shards int
}

// NewDealRepo returns a DealRepo backed by pool. New deals split their stock
// over up to shards rows (at least one, and never so many that a shard holds
// less than the per-user limit); see ADR 0011.
func NewDealRepo(pool *pgxpool.Pool, shards int) *DealRepo {
	return &DealRepo{pool: pool, shards: max(shards, 1)}
}

// Create inserts d with its stock shards and returns it as stored, with its
// ID and timestamps.
func (r *DealRepo) Create(ctx context.Context, d domain.Deal) (domain.Deal, error) {
	var row dealRow
	err := db(ctx, r.pool).QueryRow(ctx, insertDeal,
		d.ProductID(), d.Title(), d.Price().Amount(), d.Price().Currency(),
		d.TotalStock(), d.PerUserLimit(), d.StartsAt(), d.EndsAt(), r.shards,
	).Scan(row.scanTargets()...)
	if err != nil {
		if de := translate(err); de != nil {
			return domain.Deal{}, de
		}
		return domain.Deal{}, fmt.Errorf("insert deal: %w", err)
	}
	return row.toDomain()
}

// GetByID returns domain.ErrDealNotFound when no deal has that ID.
func (r *DealRepo) GetByID(ctx context.Context, id int64) (domain.Deal, error) {
	var row dealRow
	err := db(ctx, r.pool).QueryRow(ctx, selectDealByID, id).Scan(row.scanTargets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deal{}, domain.ErrDealNotFound
	}
	if err != nil {
		return domain.Deal{}, fmt.Errorf("query deal %d: %w", id, err)
	}
	return row.toDomain()
}

// ListActive returns up to limit deals active at now with an ID above
// afterID, in ID order, for keyset pagination.
func (r *DealRepo) ListActive(ctx context.Context, now time.Time, afterID int64, limit int) ([]domain.Deal, error) {
	rows, err := db(ctx, r.pool).Query(ctx, selectActiveDeals, now, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query active deals: %w", err)
	}
	defer rows.Close()

	deals := make([]domain.Deal, 0, limit)
	for rows.Next() {
		var row dealRow
		if err := rows.Scan(row.scanTargets()...); err != nil {
			return nil, fmt.Errorf("scan deal: %w", err)
		}
		d, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		deals = append(deals, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active deals: %w", err)
	}
	return deals, nil
}

// CommitReserved moves res's units from reserved to sold on the shard they
// were reserved on.
func (r *DealRepo) CommitReserved(ctx context.Context, res domain.Reservation) error {
	tag, err := db(ctx, r.pool).Exec(ctx, commitReserved, res.ID())
	if err != nil {
		if de := translate(err); de != nil {
			return de
		}
		return fmt.Errorf("commit reserved for reservation %s: %w", res.ID(), err)
	}
	if tag.RowsAffected() == 0 {
		// The reservation or its shard is gone, or the shard holds fewer
		// units than the reservation claims: counters and reservations
		// disagree.
		return fmt.Errorf("%w: commit reserved for reservation %s on deal %d: shard missing or short",
			domain.ErrCorruptState, res.ID(), res.DealID())
	}
	return nil
}

// ReleaseReserved returns the units of every reservation in rs to the
// shards they were reserved on. Shards are updated in a fixed order so
// concurrent callers cannot deadlock.
func (r *DealRepo) ReleaseReserved(ctx context.Context, rs []domain.Reservation) error {
	if len(rs) == 0 {
		return nil
	}
	ids := make([]string, len(rs))
	for i, res := range rs {
		ids[i] = res.ID()
	}
	groups, err := r.releaseGroups(ctx, ids)
	if err != nil {
		return err
	}
	for _, g := range groups {
		tag, err := db(ctx, r.pool).Exec(ctx, releaseShard, g.dealID, g.shard, g.units)
		if err != nil {
			if de := translate(err); de != nil {
				return de
			}
			return fmt.Errorf("release %d units on deal %d shard %d: %w", g.units, g.dealID, g.shard, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: release %d units on deal %d shard %d: shard missing or short",
				domain.ErrCorruptState, g.units, g.dealID, g.shard)
		}
	}
	return nil
}

type releaseGroup struct {
	dealID int64
	shard  int
	units  int
}

func (r *DealRepo) releaseGroups(ctx context.Context, ids []string) ([]releaseGroup, error) {
	rows, err := db(ctx, r.pool).Query(ctx, selectReleaseGroups, ids)
	if err != nil {
		if de := translate(err); de != nil {
			return nil, de
		}
		return nil, fmt.Errorf("query release groups: %w", err)
	}
	defer rows.Close()

	var (
		groups []releaseGroup
		found  int
	)
	for rows.Next() {
		var (
			g     releaseGroup
			count int
		)
		if err := rows.Scan(&g.dealID, &g.shard, &g.units, &count); err != nil {
			return nil, fmt.Errorf("scan release group: %w", err)
		}
		found += count
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release groups: %w", err)
	}
	if found != len(ids) {
		return nil, fmt.Errorf("%w: release found %d of %d reservations", domain.ErrCorruptState, found, len(ids))
	}
	return groups, nil
}

// GateState returns the units the deal has available and those each buyer
// holds through pending or confirmed reservations.
func (r *DealRepo) GateState(ctx context.Context, dealID int64) (usecase.GateState, error) {
	rows, err := db(ctx, r.pool).Query(ctx, selectGateState, dealID)
	if err != nil {
		return usecase.GateState{}, fmt.Errorf("query gate state of deal %d: %w", dealID, err)
	}
	defer rows.Close()

	state := usecase.GateState{Held: map[string]int{}}
	found := false
	for rows.Next() {
		var (
			user *string
			held *int
		)
		if err := rows.Scan(&state.Available, &user, &held); err != nil {
			return usecase.GateState{}, fmt.Errorf("scan gate state: %w", err)
		}
		found = true
		if user != nil && held != nil {
			state.Held[*user] = *held
		}
	}
	if err := rows.Err(); err != nil {
		return usecase.GateState{}, fmt.Errorf("iterate gate state of deal %d: %w", dealID, err)
	}
	if !found {
		return usecase.GateState{}, domain.ErrDealNotFound
	}
	return state, nil
}
