package domain

import (
	"errors"
	"fmt"
)

// Code is a stable, client-facing identifier for a business error. Clients
// may branch on it, so existing values must never change.
type Code string

// Business error codes. The HTTP layer maps each one to a status code.
const (
	CodeNotFound           Code = "NOT_FOUND"
	CodeValidation         Code = "VALIDATION_ERROR"
	CodeSoldOut            Code = "SOLD_OUT"
	CodeDealNotActive      Code = "DEAL_NOT_ACTIVE"
	CodeUserLimit          Code = "USER_LIMIT_REACHED"
	CodeReservationExpired Code = "RESERVATION_EXPIRED"
	CodeConflict           Code = "CONFLICT"
	CodeUnavailable        Code = "STOCK_UNAVAILABLE"
	CodePaymentDeclined    Code = "PAYMENT_DECLINED"
)

// Error is a business error with a stable, client-facing code.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Business errors shared across layers. Compare them with errors.Is.
var (
	ErrDealNotFound          = &Error{Code: CodeNotFound, Message: "deal not found"}
	ErrSoldOut               = &Error{Code: CodeSoldOut, Message: "deal sold out"}
	ErrDealNotActive         = &Error{Code: CodeDealNotActive, Message: "deal is not active"}
	ErrUserLimitReached      = &Error{Code: CodeUserLimit, Message: "per-user limit reached"}
	ErrReservationExpired    = &Error{Code: CodeReservationExpired, Message: "reservation expired"}
	ErrReservationNotPending = &Error{Code: CodeConflict, Message: "reservation is not pending"}
	ErrReservationNotFound   = &Error{Code: CodeNotFound, Message: "reservation not found"}
	ErrAlreadyExists         = &Error{Code: CodeConflict, Message: "resource already exists"}
	ErrStockUnavailable      = &Error{Code: CodeUnavailable, Message: "stock service unavailable"}
	// ErrPaymentDeclined is returned when the payment gateway refuses the
	// charge. Confirmation never starts, so no order is created.
	ErrPaymentDeclined = &Error{Code: CodePaymentDeclined, Message: "payment declined"}
	// ErrIdempotencyKeyReused means a client sent an idempotency key it had
	// already used for a different request; replaying it would be unsafe.
	ErrIdempotencyKeyReused = &Error{Code: CodeConflict, Message: "idempotency key already used for a different request"}
)

// Internal errors: they signal a bug or corrupt data, never a client mistake,
// so they deliberately carry no Code and surface as 500.
var (
	// ErrReservationNotDue means something tried to expire a reservation
	// before its TTL elapsed.
	ErrReservationNotDue = errors.New("reservation is not due for expiry")
	// ErrCorruptState means persisted data breaks a domain invariant.
	ErrCorruptState = errors.New("corrupt persisted state")
)

// corrupt reports persisted data that breaks an invariant. The cause is kept
// as text, not wrapped, on purpose: a validation *Error in the chain would
// otherwise reach clients as a 400 for what is really a server-side fault.
func corrupt(entity string, id any, cause error) error {
	return fmt.Errorf("%w: %s %v: %s", ErrCorruptState, entity, id, cause.Error())
}

// NewValidation returns a validation error with a client-safe message.
func NewValidation(msg string) *Error { return &Error{Code: CodeValidation, Message: msg} }

// AsDomain finds a *Error anywhere in the wrapped error chain.
func AsDomain(err error) (*Error, bool) {
	var de *Error
	ok := errors.As(err, &de)
	return de, ok
}
