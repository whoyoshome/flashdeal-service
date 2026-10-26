package usecase_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// dealWithCounters returns a deal active around t0 with 10 units, of which
// reserved are held and sold are sold.
func dealWithCounters(t *testing.T, id int64, reserved, sold int) domain.Deal {
	t.Helper()
	d, err := domain.RestoreDeal(domain.DealState{
		ID: id, ProductID: "sku", Title: "Deal", Price: usd(t, 4999),
		TotalStock: 10, Reserved: reserved, Sold: sold, PerUserLimit: 2,
		StartsAt: t0.Add(-time.Hour), EndsAt: t0.Add(time.Hour),
		CreatedAt: t0.Add(-2 * time.Hour), UpdatedAt: t0.Add(-2 * time.Hour),
	})
	require.NoError(t, err)
	return d
}

type reconcileDeps struct {
	deals  *fakeDeals
	ledger *fakeLedger
	gate   *fakeRepair
}

// newReconcileDeps sets up deal 7 with 3 units reserved by alice and 2 sold
// to bob, so PostgreSQL says 5 units are available.
func newReconcileDeps(t *testing.T, gateStock int) *reconcileDeps {
	t.Helper()
	return &reconcileDeps{
		deals: newFakeDeals(dealWithCounters(t, 7, 3, 2)),
		ledger: &fakeLedger{states: map[int64]usecase.GateState{
			7: {Available: 5, Held: map[string]int{"alice": 3, "bob": 2}},
		}},
		gate: &fakeRepair{stock: map[int64]int{7: gateStock}},
	}
}

func (d *reconcileDeps) reconcile() *usecase.Reconcile {
	return usecase.NewReconcile(d.deals, d.ledger, d.gate, &fakeClock{now: t0})
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	t.Run("leaves a deal whose gate matches PostgreSQL alone", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 5)
		uc := deps.reconcile()

		for range 3 {
			res, err := uc.Execute(t.Context())
			require.NoError(t, err)
			require.Equal(t, usecase.ReconcileResult{Checked: 1}, res)
		}
		require.Empty(t, deps.gate.restores)
		require.Zero(t, deps.ledger.calls)
	})

	t.Run("repairs drift that stays the same across two passes", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 3) // a release of 2 units was lost
		uc := deps.reconcile()

		first, err := uc.Execute(t.Context())
		require.NoError(t, err)
		require.Equal(t, usecase.ReconcileResult{Checked: 1, Drifting: 1}, first)
		require.Empty(t, deps.gate.restores, "one sighting may be an operation in flight")

		second, err := uc.Execute(t.Context())
		require.NoError(t, err)
		require.Equal(t, []usecase.StockRepair{{DealID: 7, From: 3, To: 5}}, second.Repaired)
		require.Equal(t, []restoreCall{{
			dealID:   7,
			expected: 3,
			want:     usecase.GateState{Available: 5, Held: map[string]int{"alice": 3, "bob": 2}},
		}}, deps.gate.restores)

		third, err := uc.Execute(t.Context())
		require.NoError(t, err)
		require.Equal(t, usecase.ReconcileResult{Checked: 1}, third, "a repaired deal is in sync")
	})

	t.Run("waits while the gate keeps moving", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 4)
		uc := deps.reconcile()

		_, err := uc.Execute(t.Context())
		require.NoError(t, err)
		deps.gate.stock[7] = 3 // a buyer was admitted between passes
		_, err = uc.Execute(t.Context())
		require.NoError(t, err)
		require.Empty(t, deps.gate.restores)

		res, err := uc.Execute(t.Context())
		require.NoError(t, err)
		require.Len(t, res.Repaired, 1, "the same drift on two passes in a row is repaired")
	})

	t.Run("waits while PostgreSQL keeps moving", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 2)
		uc := deps.reconcile()

		_, err := uc.Execute(t.Context())
		require.NoError(t, err)
		deps.deals.byID[7] = dealWithCounters(t, 7, 4, 2) // a reservation committed
		deps.ledger.states[7] = usecase.GateState{Available: 4, Held: map[string]int{"alice": 4, "bob": 2}}
		_, err = uc.Execute(t.Context())
		require.NoError(t, err)

		require.Empty(t, deps.gate.restores)
	})

	t.Run("does not repair when PostgreSQL moved after the pass listed the deal", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 3)
		uc := deps.reconcile()
		deps.ledger.states[7] = usecase.GateState{Available: 6, Held: map[string]int{"alice": 2, "bob": 2}}

		for range 2 {
			res, err := uc.Execute(t.Context())
			require.NoError(t, err)
			require.Empty(t, res.Repaired)
		}
		require.Empty(t, deps.gate.restores, "a cancellation committing now will release the gate itself")
	})

	t.Run("gives up without error when a buyer takes a unit before the repair", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 3)
		deps.gate.takeBeforeRestore = true
		uc := deps.reconcile()

		_, err := uc.Execute(t.Context())
		require.NoError(t, err)
		res, err := uc.Execute(t.Context())

		require.NoError(t, err)
		require.Empty(t, res.Repaired)
		require.Len(t, deps.gate.restores, 1)
		require.Equal(t, 2, deps.gate.stock[7], "the buyer's unit is never overwritten")
	})

	t.Run("ignores deals whose stock is not in the gate", func(t *testing.T) {
		t.Parallel()
		deps := newReconcileDeps(t, 5)
		delete(deps.gate.stock, 7)
		uc := deps.reconcile()

		for range 2 {
			res, err := uc.Execute(t.Context())
			require.NoError(t, err)
			require.Equal(t, usecase.ReconcileResult{}, res)
		}
		require.Empty(t, deps.gate.restores, "only activation opens a deal")
	})

	t.Run("checks every active deal, page by page", func(t *testing.T) {
		t.Parallel()
		var deals []domain.Deal
		stock := map[int64]int{}
		for id := int64(1); id <= 150; id++ {
			deals = append(deals, dealWithCounters(t, id, 0, 0))
			stock[id] = 10
		}
		deps := &reconcileDeps{deals: newFakeDeals(deals...), ledger: &fakeLedger{}, gate: &fakeRepair{stock: stock}}

		res, err := deps.reconcile().Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, 150, res.Checked)
		require.Len(t, deps.deals.listArgs, 2)
		require.Equal(t, int64(100), deps.deals.listArgs[1].afterID)
		require.Equal(t, t0, deps.deals.listArgs[0].now)
	})

	failures := []struct {
		name   string
		mutate func(*reconcileDeps)
	}{
		{name: "returns the error when active deals cannot be listed", mutate: func(d *reconcileDeps) { d.deals.listErr = errBoom }},
		{name: "returns the error when the gate cannot be read", mutate: func(d *reconcileDeps) { d.gate.stockErr = errBoom }},
		{name: "returns the error when the gate state cannot be read", mutate: func(d *reconcileDeps) { d.ledger.err = errBoom }},
		{name: "returns the error when the gate cannot be restored", mutate: func(d *reconcileDeps) { d.gate.restoreErr = errBoom }},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := newReconcileDeps(t, 3)
			uc := deps.reconcile()
			_, err := uc.Execute(t.Context()) // first sighting
			require.NoError(t, err)
			tc.mutate(deps)

			_, err = uc.Execute(t.Context())

			require.ErrorIs(t, err, errBoom)
		})
	}
}
