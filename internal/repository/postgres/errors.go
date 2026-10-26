package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// SQLSTATE codes this package translates.
const (
	sqlStateUniqueViolation = "23505"
	sqlStateInvalidText     = "22P02"
)

// Constraint names from migrations/. Violations are matched by name, not
// only by SQLSTATE, because one code covers many unrelated constraints.
const (
	constraintStockNotExceeded = "deals_stock_not_exceeded"
	constraintReservationDeal  = "reservations_deal_id_fkey"
)

// translate maps driver errors to domain errors so no pgx type escapes this
// package as a business outcome. It returns nil when err is not a known
// violation, in which case the caller wraps the original error.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch {
	case pgErr.ConstraintName == constraintStockNotExceeded:
		// Last line of defense: the conditional UPDATE should already have
		// refused, but the CHECK guarantees no oversell regardless.
		return domain.ErrSoldOut
	case pgErr.ConstraintName == constraintReservationDeal:
		return domain.ErrDealNotFound
	case pgErr.Code == sqlStateUniqueViolation:
		return domain.ErrAlreadyExists
	case pgErr.Code == sqlStateInvalidText:
		return domain.NewValidation("malformed identifier")
	}
	return nil
}
