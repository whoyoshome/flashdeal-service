package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.OrderRepository = (*OrderRepo)(nil)

// One statement inserts the order and its outbox event: they commit or roll
// back together even outside a transaction, and a duplicate order fails the
// whole statement before any event is recorded.
const insertOrder = `WITH o AS (
  INSERT INTO orders (id, reservation_id, deal_id, user_id, quantity, total_cents, currency, created_at)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
  RETURNING id, created_at
)
INSERT INTO outbox (event_type, aggregate_id, payload, occurred_at)
SELECT $9, o.id::text, $10::jsonb, o.created_at FROM o`

// OrderRepo persists orders.
type OrderRepo struct {
	pool *pgxpool.Pool
}

// NewOrderRepo returns an OrderRepo backed by pool.
func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo {
	return &OrderRepo{pool: pool}
}

// Create inserts o and records its order.confirmed event in the outbox; a
// second order for the same reservation returns domain.ErrAlreadyExists.
func (r *OrderRepo) Create(ctx context.Context, o domain.Order) error {
	payload, err := orderConfirmedEvent(o)
	if err != nil {
		return fmt.Errorf("encode order %s event: %w", o.ID(), err)
	}
	_, err = db(ctx, r.pool).Exec(ctx, insertOrder,
		o.ID(), o.ReservationID(), o.DealID(), o.UserID(), o.Quantity(),
		o.Total().Amount(), o.Total().Currency(), o.CreatedAt(),
		usecase.EventOrderConfirmed, payload,
	)
	if err != nil {
		if de := translate(err); de != nil {
			return de
		}
		return fmt.Errorf("insert order %s: %w", o.ID(), err)
	}
	return nil
}
