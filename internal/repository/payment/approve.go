// Package payment holds payment-gateway adapters. The production stub
// always approves; a real provider replaces AlwaysApprove behind the same
// usecase.PaymentGateway port (ADR 0014).
package payment

import (
	"context"
	"sync"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.PaymentGateway = (*AlwaysApprove)(nil)

// AlwaysApprove is the payment gateway used until a real provider is wired.
// Charge succeeds for every request and is idempotent on ReservationID
// within the process.
type AlwaysApprove struct {
	mu   sync.Mutex
	done map[string]struct{}
}

// NewAlwaysApprove returns an AlwaysApprove gateway.
func NewAlwaysApprove() *AlwaysApprove {
	return &AlwaysApprove{done: make(map[string]struct{})}
}

// Charge records the reservation as paid. A second call with the same
// ReservationID is a no-op.
func (g *AlwaysApprove) Charge(_ context.Context, in usecase.PaymentCharge) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done[in.ReservationID] = struct{}{}
	return nil
}
