// Package uuidgen generates identifiers for reservations and orders.
package uuidgen

import (
	"fmt"

	"github.com/google/uuid"
)

// V7 generates UUIDv7 values. They are time-ordered, which keeps B-tree
// inserts on the primary key local instead of scattered across the index.
type V7 struct{}

// New returns a new UUIDv7 in its canonical string form.
func (V7) New() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate uuid v7: %w", err)
	}
	return id.String(), nil
}
