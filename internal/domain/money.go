package domain

import "math"

// Money is an amount in the currency's minor unit (cents for USD) plus an
// ISO 4217 code. Integer minor units avoid float rounding errors entirely.
type Money struct {
	amount   int64
	currency string
}

// NewMoney validates and builds a Money. Amounts are never negative because
// the service only models prices and order totals.
func NewMoney(amount int64, currency string) (Money, error) {
	if amount < 0 {
		return Money{}, NewValidation("amount must not be negative")
	}
	if !isCurrencyCode(currency) {
		return Money{}, NewValidation("currency must be a three-letter ISO 4217 code")
	}
	return Money{amount: amount, currency: currency}, nil
}

// Amount returns the value in the currency's minor unit.
func (m Money) Amount() int64 { return m.amount }

// Currency returns the ISO 4217 code.
func (m Money) Currency() string { return m.currency }

// Mul returns m multiplied by n, failing instead of silently overflowing.
func (m Money) Mul(n int) (Money, error) {
	if n < 0 {
		return Money{}, NewValidation("multiplier must not be negative")
	}
	if n != 0 && m.amount > math.MaxInt64/int64(n) {
		return Money{}, NewValidation("amount overflows")
	}
	return Money{amount: m.amount * int64(n), currency: m.currency}, nil
}

// isCurrencyCode checks the ISO 4217 shape (three uppercase ASCII letters).
// Checking membership in the official list would need data the domain does
// not own; the shape check catches typos and lowercase input.
func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := range len(s) {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}
