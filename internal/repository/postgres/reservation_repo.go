package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.ReservationRepository = (*ReservationRepo)(nil)

const (
	// ON CONFLICT keeps the transaction usable on a retried idempotency key,
	// so the stored reservation can be read back in the same transaction.
	insertReservation = `INSERT INTO reservations (id, deal_id, user_id, quantity, status, idempotency_key, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT ON CONSTRAINT reservations_idempotent DO NOTHING`

	selectReservationByKey = `SELECT ` + reservationColumns + ` FROM reservations
WHERE user_id = $1 AND idempotency_key = $2`

	// Conditional increment: the WHERE clause is re-evaluated after the row
	// lock, so concurrent reservations cannot exceed total_stock. The
	// deals_stock_not_exceeded CHECK is the last line of defense.
	reserveStock = `UPDATE deals SET reserved = reserved + $2, updated_at = now()
WHERE id = $1 AND reserved + sold + $2 <= total_stock`

	selectReservationForUpdate = `SELECT ` + reservationColumns + ` FROM reservations
WHERE id = $1
FOR UPDATE`

	// Only a reservation still pending in storage may change status, which
	// guards against a transition decided on a stale read.
	updateReservationStatus = `UPDATE reservations SET status = $2, updated_at = $3
WHERE id = $1 AND status = 'pending'`
)

// ReservationRepo persists reservations and reserves their stock.
type ReservationRepo struct {
	pool *pgxpool.Pool
}

// NewReservationRepo returns a ReservationRepo backed by pool.
func NewReservationRepo(pool *pgxpool.Pool) *ReservationRepo {
	return &ReservationRepo{pool: pool}
}

// Create inserts res and reserves its units on the deal in one transaction,
// joining the caller's transaction when there is one. A retry with the same
// idempotency key and the same request returns the stored reservation with
// created false and reserves nothing; the same key for a different request
// returns domain.ErrIdempotencyKeyReused.
func (r *ReservationRepo) Create(ctx context.Context, res domain.Reservation) (domain.Reservation, bool, error) {
	var (
		stored  domain.Reservation
		created bool
	)
	err := withinTx(ctx, r.pool, func(ctx context.Context) error {
		q := db(ctx, r.pool)
		tag, err := q.Exec(ctx, insertReservation,
			res.ID(), res.DealID(), res.UserID(), res.Quantity(), string(res.Status()),
			res.IdempotencyKey(), res.ExpiresAt(), res.CreatedAt(), res.UpdatedAt(),
		)
		if err != nil {
			if de := translate(err); de != nil {
				return de
			}
			return fmt.Errorf("insert reservation: %w", err)
		}

		if tag.RowsAffected() == 0 {
			existing, err := r.byIdempotencyKey(ctx, q, res.UserID(), res.IdempotencyKey())
			if err != nil {
				return err
			}
			if existing.DealID() != res.DealID() || existing.Quantity() != res.Quantity() {
				return domain.ErrIdempotencyKeyReused
			}
			stored = existing
			return nil
		}

		tag, err = q.Exec(ctx, reserveStock, res.DealID(), res.Quantity())
		if err != nil {
			if de := translate(err); de != nil {
				return de
			}
			return fmt.Errorf("reserve stock on deal %d: %w", res.DealID(), err)
		}
		if tag.RowsAffected() == 0 {
			// The insert proved the deal exists (FK), so this is lack of stock.
			return domain.ErrSoldOut
		}
		stored, created = res, true
		return nil
	})
	if err != nil {
		return domain.Reservation{}, false, err
	}
	return stored, created, nil
}

func (r *ReservationRepo) byIdempotencyKey(ctx context.Context, q querier, userID, key string) (domain.Reservation, error) {
	var row reservationRow
	if err := q.QueryRow(ctx, selectReservationByKey, userID, key).Scan(row.scanTargets()...); err != nil {
		return domain.Reservation{}, fmt.Errorf("query reservation by idempotency key: %w", err)
	}
	return row.toDomain()
}

// GetForUpdate loads the reservation and locks its row until the
// surrounding transaction ends.
func (r *ReservationRepo) GetForUpdate(ctx context.Context, id string) (domain.Reservation, error) {
	var row reservationRow
	err := db(ctx, r.pool).QueryRow(ctx, selectReservationForUpdate, id).Scan(row.scanTargets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	if err != nil {
		if de := translate(err); de != nil {
			return domain.Reservation{}, de
		}
		return domain.Reservation{}, fmt.Errorf("query reservation %s: %w", id, err)
	}
	return row.toDomain()
}

// UpdateStatus persists res's current status if the stored row is still
// pending.
func (r *ReservationRepo) UpdateStatus(ctx context.Context, res domain.Reservation) error {
	tag, err := db(ctx, r.pool).Exec(ctx, updateReservationStatus, res.ID(), string(res.Status()), res.UpdatedAt())
	if err != nil {
		if de := translate(err); de != nil {
			return de
		}
		return fmt.Errorf("update reservation %s: %w", res.ID(), err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrReservationNotPending
	}
	return nil
}
