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
	_ usecase.ReservationRepository = (*ReservationRepo)(nil)
	_ usecase.ExpiryQueue           = (*ReservationRepo)(nil)
)

const (
	// A reservation is a short hold, not a sale: the transaction that creates it
	// commits without waiting for its WAL to reach disk. The stock shard stays
	// locked from the stock UPDATE until COMMIT, so waiting for the flush
	// serialized every sale on that shard behind one fsync.
	// If PostgreSQL itself crashes, holds committed in the last few hundred
	// milliseconds can be lost: the reservation and its reserved units vanish
	// together, so nothing is oversold, and the reconciler re-aligns the gate.
	// Confirmations and orders keep synchronous commits. See ADR 0006.
	asyncCommit = `SET LOCAL synchronous_commit = off`

	// ON CONFLICT keeps the transaction usable on a retried idempotency key,
	// so the stored reservation can be read back in the same transaction.
	insertReservation = `INSERT INTO reservations (id, deal_id, user_id, quantity, status, idempotency_key, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT ON CONSTRAINT reservations_idempotent DO NOTHING`

	// The common case in one round trip and one implicit transaction: take
	// the units from a free shard and insert the reservation that records
	// it (ADR 0013). The shard stays locked only while the statement runs,
	// with no network round trip before COMMIT. set_config(..., true) is the
	// asyncCommit of this transaction and ends with it. Zero rows means no
	// free shard had room, and a reservations_idempotent violation means a
	// retry: Create then takes the transactional path, which tells those
	// cases apart. The casts type parameters that INSERT ... SELECT would
	// otherwise read as text.
	reserveInOneStatement = `WITH async AS (
  SELECT set_config('synchronous_commit', 'off', true)
), picked AS (
  UPDATE deal_stock_shards AS s SET reserved = s.reserved + $4::int
  WHERE (s.deal_id, s.shard) = (
    SELECT deal_id, shard FROM deal_stock_shards
    WHERE deal_id = $2::bigint AND capacity - reserved - sold >= $4::int
    ORDER BY shard
    LIMIT 1
    FOR NO KEY UPDATE SKIP LOCKED
  ) AND s.capacity - s.reserved - s.sold >= $4::int
  RETURNING s.shard
)
INSERT INTO reservations (id, deal_id, user_id, quantity, status, idempotency_key, expires_at, created_at, updated_at, stock_shard)
SELECT $1::uuid, $2::bigint, $3::text, $4::int, $5::text, $6::text, $7::timestamptz, $8::timestamptz, $9::timestamptz, picked.shard
FROM picked, async`

	selectReservationByKey = `SELECT ` + reservationColumns + ` FROM reservations
WHERE user_id = $1 AND idempotency_key = $2`

	// Takes the units from one stock shard with room and records which one
	// on the reservation (ADR 0011). SKIP LOCKED passes over shards another
	// reservation holds, so concurrent reservations of a deal spread over its
	// shards instead of queuing on one row lock. The outer WHERE re-checks
	// the room after the lock, and the deal_stock_shards_not_exceeded CHECK
	// is the last line of defense. Zero rows means no free shard had room.
	reserveOnFreeShard = `WITH picked AS (
  UPDATE deal_stock_shards AS s SET reserved = s.reserved + $2
  WHERE (s.deal_id, s.shard) = (
    SELECT deal_id, shard FROM deal_stock_shards
    WHERE deal_id = $1 AND capacity - reserved - sold >= $2
    ORDER BY shard
    LIMIT 1
    FOR NO KEY UPDATE SKIP LOCKED
  ) AND s.capacity - s.reserved - s.sold >= $2
  RETURNING s.shard
)
UPDATE reservations SET stock_shard = picked.shard FROM picked WHERE id = $3`

	// The same, waiting for a locked shard instead of skipping it: used when
	// every shard with room was held by another reservation.
	reserveOnAnyShard = `WITH picked AS (
  UPDATE deal_stock_shards AS s SET reserved = s.reserved + $2
  WHERE (s.deal_id, s.shard) = (
    SELECT deal_id, shard FROM deal_stock_shards
    WHERE deal_id = $1 AND capacity - reserved - sold >= $2
    ORDER BY shard
    LIMIT 1
    FOR NO KEY UPDATE
  ) AND s.capacity - s.reserved - s.sold >= $2
  RETURNING s.shard
)
UPDATE reservations SET stock_shard = picked.shard FROM picked WHERE id = $3`

	// A row locked by FOR NO KEY UPDATE stays locked until the transaction
	// ends even when the re-check then rejects it, so a failed attempt can
	// leave locks on shards it did not use. Every attempt runs after this
	// savepoint, and each retry first rolls back to it, so a reservation
	// never waits for a shard while holding another: otherwise two
	// reservations could each hold the shard the other waits for.
	savepointShard = `SAVEPOINT reserve_shard`
	rollbackShard  = `ROLLBACK TO SAVEPOINT reserve_shard`

	// Only decides whether another attempt can succeed; the units are still
	// taken by the conditional UPDATE above.
	selectShardWithRoom = `SELECT EXISTS (
  SELECT 1 FROM deal_stock_shards WHERE deal_id = $1 AND capacity - reserved - sold >= $2
)`

	selectReservationByID = `SELECT ` + reservationColumns + ` FROM reservations
WHERE id = $1`

	selectReservationForUpdate = `SELECT ` + reservationColumns + ` FROM reservations
WHERE id = $1
FOR UPDATE`

	// Only a reservation still pending in storage may change status, which
	// guards against a transition decided on a stale read.
	updateReservationStatus = `UPDATE reservations SET status = $2, updated_at = $3
WHERE id = $1 AND status = 'pending'`

	// SKIP LOCKED lets workers on every replica take disjoint batches: rows
	// another transaction holds are passed over instead of waited on. The
	// cutoff is a parameter, not now(), so the caller's clock decides what
	// is due, matching domain.Reservation.IsExpired (expires_at <= now).
	selectDueReservations = `SELECT ` + reservationColumns + ` FROM reservations
WHERE status = 'pending' AND expires_at <= $1
ORDER BY expires_at
LIMIT $2
FOR UPDATE SKIP LOCKED`

	// One statement for the whole batch; the status and time come from the
	// domain transitions, and only rows still pending may change.
	updateReservationStatuses = `UPDATE reservations AS r SET status = u.status, updated_at = u.updated_at
FROM unnest($1::uuid[], $2::text[], $3::timestamptz[]) AS u(id, status, updated_at)
WHERE r.id = u.id AND r.status = 'pending'`
)

// reserveAttempts bounds how many times Create looks for a shard with room
// while other reservations keep taking the shards it found.
const reserveAttempts = 3

// ReservationRepo persists reservations and reserves their stock.
type ReservationRepo struct {
	pool *pgxpool.Pool
}

// NewReservationRepo returns a ReservationRepo backed by pool.
func NewReservationRepo(pool *pgxpool.Pool) *ReservationRepo {
	return &ReservationRepo{pool: pool}
}

// Create inserts res and reserves its units on the deal in one transaction,
// joining the caller's transaction when there is one. That transaction
// commits asynchronously (see asyncCommit). A retry with the same
// idempotency key and the same request returns the stored reservation with
// created false and reserves nothing; the same key for a different request
// returns domain.ErrIdempotencyKeyReused.
func (r *ReservationRepo) Create(ctx context.Context, res domain.Reservation) (domain.Reservation, bool, error) {
	// Inside a caller's transaction a failed statement would abort it, so
	// only a standalone Create tries the single statement first.
	if _, inTx := txFrom(ctx); !inTx {
		done, err := r.createInOneStatement(ctx, res)
		if err != nil {
			return domain.Reservation{}, false, err
		}
		if done {
			return res, true, nil
		}
	}
	return r.createInTx(ctx, res)
}

// createInOneStatement runs reserveInOneStatement and reports whether it
// stored res. False with a nil error leaves the outcome to createInTx.
func (r *ReservationRepo) createInOneStatement(ctx context.Context, res domain.Reservation) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// The statement commits as it ends, so like COMMIT in withinTx it must
	// not be cancelled midway: a sale could land while the caller hears it
	// failed and gives its units back to the gate.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, reserveInOneStatement,
		res.ID(), res.DealID(), res.UserID(), res.Quantity(), string(res.Status()),
		res.IdempotencyKey(), res.ExpiresAt(), res.CreatedAt(), res.UpdatedAt(),
	)
	if violates(err, constraintIdempotentKey) {
		return false, nil
	}
	if err != nil {
		if de := translate(err); de != nil {
			return false, de
		}
		return false, fmt.Errorf("reserve stock on deal %d: %w", res.DealID(), err)
	}
	return tag.RowsAffected() == 1, nil
}

// createInTx is Create as a transaction of several statements: it covers
// what the single statement leaves undecided, at five round trips.
func (r *ReservationRepo) createInTx(ctx context.Context, res domain.Reservation) (domain.Reservation, bool, error) {
	var (
		stored  domain.Reservation
		created bool
	)
	err := withinTx(ctx, r.pool, func(ctx context.Context) error {
		q := db(ctx, r.pool)
		// Issued first, before any stock shard is locked, so the extra round
		// trip does not lengthen the lock.
		if _, err := q.Exec(ctx, asyncCommit); err != nil {
			return fmt.Errorf("set async commit: %w", err)
		}
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

		if err := reserveOnShard(ctx, q, res); err != nil {
			return err
		}
		stored, created = res, true
		return nil
	})
	if err != nil {
		return domain.Reservation{}, false, err
	}
	return stored, created, nil
}

// reserveOnShard takes res's units from one of the deal's stock shards. The
// insert before it proved the deal exists (FK), so finding no shard with
// room is lack of stock. Units split over shards that each lack room for
// the whole quantity also count as sold out (ADR 0011).
func reserveOnShard(ctx context.Context, q querier, res domain.Reservation) error {
	reset := savepointShard
	for range reserveAttempts {
		for _, sql := range []string{reserveOnFreeShard, reserveOnAnyShard} {
			ok, err := tryShard(ctx, q, reset, sql, res)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			reset = rollbackShard
		}
		var room bool
		if err := q.QueryRow(ctx, selectShardWithRoom, res.DealID(), res.Quantity()).Scan(&room); err != nil {
			return fmt.Errorf("check stock of deal %d: %w", res.DealID(), err)
		}
		if !room {
			return domain.ErrSoldOut
		}
	}
	return domain.ErrSoldOut
}

// tryShard runs reset and one reserve statement in a single round trip and
// reports whether the statement took the units.
func tryShard(ctx context.Context, q querier, reset, sql string, res domain.Reservation) (bool, error) {
	b := &pgx.Batch{}
	b.Queue(reset)
	b.Queue(sql, res.DealID(), res.Quantity(), res.ID())
	br := q.SendBatch(ctx, b)
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return false, fmt.Errorf("reset stock shard savepoint: %w", err)
	}
	tag, err := br.Exec()
	if closeErr := br.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		if de := translate(err); de != nil {
			return false, de
		}
		return false, fmt.Errorf("reserve stock on deal %d: %w", res.DealID(), err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *ReservationRepo) byIdempotencyKey(ctx context.Context, q querier, userID, key string) (domain.Reservation, error) {
	var row reservationRow
	if err := q.QueryRow(ctx, selectReservationByKey, userID, key).Scan(row.scanTargets()...); err != nil {
		return domain.Reservation{}, fmt.Errorf("query reservation by idempotency key: %w", err)
	}
	return row.toDomain()
}

// Get loads the reservation without locking it.
func (r *ReservationRepo) Get(ctx context.Context, id string) (domain.Reservation, error) {
	return r.load(ctx, selectReservationByID, id)
}

// GetForUpdate loads the reservation and locks its row until the
// surrounding transaction ends.
func (r *ReservationRepo) GetForUpdate(ctx context.Context, id string) (domain.Reservation, error) {
	return r.load(ctx, selectReservationForUpdate, id)
}

func (r *ReservationRepo) load(ctx context.Context, sql, id string) (domain.Reservation, error) {
	var row reservationRow
	err := db(ctx, r.pool).QueryRow(ctx, sql, id).Scan(row.scanTargets()...)
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

// LockDue returns up to limit pending reservations due at now, oldest
// first, locking them until the surrounding transaction ends. Call it inside
// TxManager.WithinTx: outside a transaction the locks end with the query.
func (r *ReservationRepo) LockDue(ctx context.Context, now time.Time, limit int) ([]domain.Reservation, error) {
	rows, err := db(ctx, r.pool).Query(ctx, selectDueReservations, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query due reservations: %w", err)
	}
	defer rows.Close()

	due := make([]domain.Reservation, 0, limit)
	for rows.Next() {
		var row reservationRow
		if err := rows.Scan(row.scanTargets()...); err != nil {
			return nil, fmt.Errorf("scan reservation: %w", err)
		}
		res, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		due = append(due, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due reservations: %w", err)
	}
	return due, nil
}

// MarkExpired persists the status of every reservation in rs in one
// statement. If any stored row already left pending, nothing is applied to
// it and domain.ErrReservationNotPending is returned, so the caller's
// transaction rolls back as a whole.
func (r *ReservationRepo) MarkExpired(ctx context.Context, rs []domain.Reservation) error {
	if len(rs) == 0 {
		return nil
	}
	ids := make([]string, len(rs))
	statuses := make([]string, len(rs))
	updated := make([]time.Time, len(rs))
	for i, res := range rs {
		ids[i], statuses[i], updated[i] = res.ID(), string(res.Status()), res.UpdatedAt()
	}
	tag, err := db(ctx, r.pool).Exec(ctx, updateReservationStatuses, ids, statuses, updated)
	if err != nil {
		if de := translate(err); de != nil {
			return de
		}
		return fmt.Errorf("update %d reservations: %w", len(rs), err)
	}
	if tag.RowsAffected() != int64(len(rs)) {
		return domain.ErrReservationNotPending
	}
	return nil
}
