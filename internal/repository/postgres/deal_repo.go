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
)

const (
	insertDeal = `INSERT INTO deals (product_id, title, price_cents, currency, total_stock, per_user_limit, starts_at, ends_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING ` + dealColumns

	selectDealByID = `SELECT ` + dealColumns + ` FROM deals WHERE id = $1`

	selectActiveDeals = `SELECT ` + dealColumns + ` FROM deals
WHERE starts_at <= $1 AND ends_at > $1 AND id > $2
ORDER BY id
LIMIT $3`

	// The WHERE clause makes the move atomic: it is evaluated after the row
	// lock, so concurrent confirmations can never push reserved below zero.
	commitReserved = `UPDATE deals SET reserved = reserved - $2, sold = sold + $2, updated_at = now()
WHERE id = $1 AND reserved >= $2`

	releaseReserved = `UPDATE deals SET reserved = reserved - $2, updated_at = now()
WHERE id = $1 AND reserved >= $2`
)

// DealRepo persists deals and moves their stock counters.
type DealRepo struct {
	pool *pgxpool.Pool
}

// NewDealRepo returns a DealRepo backed by pool.
func NewDealRepo(pool *pgxpool.Pool) *DealRepo {
	return &DealRepo{pool: pool}
}

// Create inserts d and returns it as stored, with its ID and timestamps.
func (r *DealRepo) Create(ctx context.Context, d domain.Deal) (domain.Deal, error) {
	var row dealRow
	err := db(ctx, r.pool).QueryRow(ctx, insertDeal,
		d.ProductID(), d.Title(), d.Price().Amount(), d.Price().Currency(),
		d.TotalStock(), d.PerUserLimit(), d.StartsAt(), d.EndsAt(),
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

// CommitReserved moves qty units from reserved to sold.
func (r *DealRepo) CommitReserved(ctx context.Context, dealID int64, qty int) error {
	return r.moveStock(ctx, commitReserved, "commit reserved", dealID, qty)
}

// ReleaseReserved returns qty reserved units to the deal.
func (r *DealRepo) ReleaseReserved(ctx context.Context, dealID int64, qty int) error {
	return r.moveStock(ctx, releaseReserved, "release reserved", dealID, qty)
}

func (r *DealRepo) moveStock(ctx context.Context, sql, op string, dealID int64, qty int) error {
	tag, err := db(ctx, r.pool).Exec(ctx, sql, dealID, qty)
	if err != nil {
		if de := translate(err); de != nil {
			return de
		}
		return fmt.Errorf("%s on deal %d: %w", op, dealID, err)
	}
	if tag.RowsAffected() == 0 {
		// Either the deal is gone or fewer units are reserved than a
		// reservation claims: the counters and reservations disagree.
		return fmt.Errorf("%w: %s on deal %d: fewer than %d units reserved or deal missing",
			domain.ErrCorruptState, op, dealID, qty)
	}
	return nil
}
