package usecase

import (
	"context"
	"fmt"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// reconcilePageSize bounds how many active deals one query lists.
const reconcilePageSize = 100

// StockRepair records one deal whose gate was corrected.
type StockRepair struct {
	DealID int64
	// From is the stock the gate held; To is what PostgreSQL says it must
	// hold.
	From, To int
}

// ReconcileResult summarizes one reconciliation pass.
type ReconcileResult struct {
	// Checked counts active deals whose stock is in the gate.
	Checked int
	// Drifting counts those whose gate disagreed with PostgreSQL.
	Drifting int
	// Repaired lists the deals whose gate was corrected.
	Repaired []StockRepair
}

// observation is what a pass saw for a deal whose gate disagreed with
// PostgreSQL.
type observation struct {
	gate, reserved, sold int
}

// Reconcile corrects the stock gate from PostgreSQL, the source of truth,
// when a release was lost (Redis down after a commit, a crash between
// commit and release). It is what turns "the reconciler will fix it" in
// the other use cases into a guarantee.
//
// The gate and PostgreSQL legitimately disagree while an operation is
// half-way, for example after the gate admitted a buyer and before the
// reservation commits. Correcting then would fight that operation, so a
// deal is repaired only when the same disagreement, with neither store
// changing, is seen on two consecutive passes. Passes run further apart
// than any operation can take, so a disagreement that stood still for a
// whole pass is drift, not work in progress. The repair itself is a
// compare-and-set: it applies only if the gate still holds what was seen.
// See docs/adr/0005-stock-gate-reconciliation.md.
//
// Reconcile remembers the previous pass, so one instance must not run
// passes concurrently; each replica's worker owns its own.
type Reconcile struct {
	deals  DealRepository
	ledger StockLedger
	gate   GateRepair
	clock  Clock
	seen   map[int64]observation
}

// NewReconcile wires the reconciliation use case.
func NewReconcile(deals DealRepository, ledger StockLedger, gate GateRepair, clock Clock) *Reconcile {
	return &Reconcile{deals: deals, ledger: ledger, gate: gate, clock: clock, seen: map[int64]observation{}}
}

// Execute runs one pass over every active deal. If it fails part-way, the
// previous pass's observations are kept, so the next pass compares against
// an even older state, which is only more conservative.
func (uc *Reconcile) Execute(ctx context.Context) (ReconcileResult, error) {
	now := uc.clock.Now()
	var res ReconcileResult
	seen := map[int64]observation{}
	for afterID := int64(0); ; {
		deals, err := uc.deals.ListActive(ctx, now, afterID, reconcilePageSize)
		if err != nil {
			return res, fmt.Errorf("list active deals: %w", err)
		}
		for _, d := range deals {
			if err := uc.check(ctx, d, seen, &res); err != nil {
				return res, err
			}
		}
		if len(deals) < reconcilePageSize {
			break
		}
		afterID = deals[len(deals)-1].ID()
	}
	uc.seen = seen
	return res, nil
}

func (uc *Reconcile) check(ctx context.Context, d domain.Deal, seen map[int64]observation, res *ReconcileResult) error {
	stock, loaded, err := uc.gate.Stock(ctx, d.ID())
	if err != nil {
		return fmt.Errorf("read gate stock of deal %d: %w", d.ID(), err)
	}
	if !loaded {
		// Activation, not reconciliation, decides when a deal opens.
		return nil
	}
	res.Checked++
	if stock == d.Available() {
		return nil
	}
	res.Drifting++

	obs := observation{gate: stock, reserved: d.Reserved(), sold: d.Sold()}
	if prev, ok := uc.seen[d.ID()]; !ok || prev != obs {
		// First sighting, or something moved since the last pass: look again.
		seen[d.ID()] = obs
		return nil
	}

	want, err := uc.ledger.GateState(ctx, d.ID())
	if err != nil {
		return fmt.Errorf("read gate state of deal %d: %w", d.ID(), err)
	}
	if want.Available != d.Available() {
		// PostgreSQL moved after this pass listed the deal: an operation is
		// in flight, and its own gate update may still be on the way.
		return nil
	}
	applied, err := uc.gate.Restore(ctx, d.ID(), stock, want)
	if err != nil {
		return fmt.Errorf("restore gate of deal %d: %w", d.ID(), err)
	}
	if applied {
		res.Repaired = append(res.Repaired, StockRepair{DealID: d.ID(), From: stock, To: want.Available})
	}
	return nil
}
