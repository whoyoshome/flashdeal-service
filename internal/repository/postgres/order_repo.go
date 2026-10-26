package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.OrderRepository = (*OrderRepo)(nil)

const insertOrder = `INSERT INTO orders (id, reservation_id, deal_id, user_id, quantity, total_cents, currency, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

// OrderRepo persists orders.
type OrderRepo struct {
	pool *pgxpool.Pool
}

// NewOrderRepo returns an OrderRepo backed by pool.
func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo {
	return &OrderRepo{pool: pool}
}

// Create inserts o; a second order for the same reservation returns
// domain.ErrAlreadyExists.
func (r *OrderRepo) Create(ctx context.Context, o domain.Order) error {
	_, err := db(ctx, r.pool).Exec(ctx, insertOrder,
		o.ID(), o.ReservationID(), o.DealID(), o.UserID(), o.Quantity(),
		o.Total().Amount(), o.Total().Currency(), o.CreatedAt(),
	)
	if err != nil {
		if de := translate(err); de != nil {
			return de
		}
		return fmt.Errorf("insert order %s: %w", o.ID(), err)
	}
	return nil
}
