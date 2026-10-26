package usecase_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func validCreateDealInput() usecase.CreateDealInput {
	return usecase.CreateDealInput{
		ProductID: "sku-9", Title: "Keyboard", PriceCents: 12900, Currency: "USD",
		TotalStock: 500, PerUserLimit: 1, StartsAt: t0, EndsAt: t0.Add(2 * time.Hour),
	}
}

func TestManageDealsCreate(t *testing.T) {
	t.Parallel()

	t.Run("stores a valid deal and returns it with its id", func(t *testing.T) {
		t.Parallel()
		deals := newFakeDeals()
		uc := usecase.NewManageDeals(deals, &fakeGate{}, &fakeClock{now: t0})

		d, err := uc.Create(t.Context(), validCreateDealInput())

		require.NoError(t, err)
		require.Positive(t, d.ID())
		require.Equal(t, usd(t, 12900), d.Price())
		require.Equal(t, 500, d.TotalStock())
	})

	tests := []struct {
		name   string
		mutate func(*usecase.CreateDealInput)
	}{
		{name: "rejects an invalid currency", mutate: func(in *usecase.CreateDealInput) { in.Currency = "us" }},
		{name: "rejects a negative price", mutate: func(in *usecase.CreateDealInput) { in.PriceCents = -1 }},
		{name: "rejects zero stock", mutate: func(in *usecase.CreateDealInput) { in.TotalStock = 0 }},
		{name: "rejects a window that ends before it starts", mutate: func(in *usecase.CreateDealInput) { in.EndsAt = in.StartsAt.Add(-time.Minute) }},
	}
	for _, tc := range tests {
		t.Run(tc.name+" without storing it", func(t *testing.T) {
			t.Parallel()
			deals := newFakeDeals()
			uc := usecase.NewManageDeals(deals, &fakeGate{}, &fakeClock{now: t0})
			in := validCreateDealInput()
			tc.mutate(&in)

			_, err := uc.Create(t.Context(), in)

			de, ok := domain.AsDomain(err)
			require.True(t, ok)
			require.Equal(t, domain.CodeValidation, de.Code)
			require.Empty(t, deals.byID)
		})
	}

	t.Run("returns storage errors", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("connection reset")
		deals := newFakeDeals()
		deals.createErr = boom
		uc := usecase.NewManageDeals(deals, &fakeGate{}, &fakeClock{now: t0})

		_, err := uc.Create(t.Context(), validCreateDealInput())

		require.ErrorIs(t, err, boom)
	})
}

func TestManageDealsActivate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		now        time.Time
		dealID     int64
		gateErr    error
		wantErr    error
		wantLoaded bool
	}{
		{name: "loads an active deal into the stock gate", now: t0, dealID: 7, wantLoaded: true},
		{name: "loads a deal that has not started yet", now: t0.Add(-3 * time.Hour), dealID: 7, wantLoaded: true},
		{name: "returns ErrDealNotActive for a deal whose window closed", now: t0.Add(time.Hour), dealID: 7, wantErr: domain.ErrDealNotActive},
		{name: "returns ErrDealNotFound for an unknown deal", now: t0, dealID: 404, wantErr: domain.ErrDealNotFound},
		{name: "returns ErrStockUnavailable when the gate is down", now: t0, dealID: 7, gateErr: domain.ErrStockUnavailable, wantErr: domain.ErrStockUnavailable, wantLoaded: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate := &fakeGate{loadErr: tc.gateErr}
			uc := usecase.NewManageDeals(newFakeDeals(activeDeal(t)), gate, &fakeClock{now: tc.now})

			d, err := uc.Activate(t.Context(), tc.dealID)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.dealID, d.ID())
			}
			if tc.wantLoaded {
				require.Equal(t, []int64{tc.dealID}, gate.loaded)
			} else {
				require.Empty(t, gate.loaded)
			}
		})
	}
}

func TestDealQueries(t *testing.T) {
	t.Parallel()

	t.Run("returns a deal by id", func(t *testing.T) {
		t.Parallel()
		uc := newDealQueries(newFakeDeals(activeDeal(t)), &fakeClock{now: t0})

		d, err := uc.Get(t.Context(), 7)

		require.NoError(t, err)
		require.Equal(t, int64(7), d.ID())
	})

	t.Run("returns ErrDealNotFound for an unknown id", func(t *testing.T) {
		t.Parallel()
		uc := newDealQueries(newFakeDeals(), &fakeClock{now: t0})

		_, err := uc.Get(t.Context(), 404)

		require.ErrorIs(t, err, domain.ErrDealNotFound)
	})

	limits := []struct {
		name      string
		requested int
		want      int
	}{
		{name: "uses the default page size when no limit is given", requested: 0, want: usecase.DefaultPageSize},
		{name: "uses the default page size for a negative limit", requested: -5, want: usecase.DefaultPageSize},
		{name: "keeps a limit within bounds", requested: 50, want: 50},
		{name: "caps the limit at the maximum page size", requested: 10_000, want: usecase.MaxPageSize},
	}
	for _, tc := range limits {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deals := newFakeDeals()
			uc := newDealQueries(deals, &fakeClock{now: t0})

			_, err := uc.ListActive(t.Context(), 0, tc.requested)

			require.NoError(t, err)
			require.Equal(t, tc.want, deals.listArgs[0].limit)
			require.Equal(t, t0, deals.listArgs[0].now)
		})
	}

	t.Run("returns a next cursor only when the page is full", func(t *testing.T) {
		t.Parallel()
		deals := newFakeDeals()
		for range 3 {
			_, err := deals.Create(t.Context(), activeDeal(t))
			require.NoError(t, err)
		}
		uc := newDealQueries(deals, &fakeClock{now: t0})

		first, err := uc.ListActive(t.Context(), 0, 2)
		require.NoError(t, err)
		require.Len(t, first.Deals, 2)
		require.Equal(t, first.Deals[1].ID(), first.NextCursor)

		last, err := uc.ListActive(t.Context(), first.NextCursor, 2)
		require.NoError(t, err)
		require.Len(t, last.Deals, 1)
		require.Zero(t, last.NextCursor)
	})

	t.Run("rejects a negative cursor", func(t *testing.T) {
		t.Parallel()
		uc := newDealQueries(newFakeDeals(), &fakeClock{now: t0})

		_, err := uc.ListActive(t.Context(), -1, 10)

		de, ok := domain.AsDomain(err)
		require.True(t, ok)
		require.Equal(t, domain.CodeValidation, de.Code)
	})
}

// newDealQueries uses the same fake for single reads and listing.
func newDealQueries(deals *fakeDeals, clock *fakeClock) *usecase.DealQueries {
	return usecase.NewDealQueries(deals, deals, clock)
}
