package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

func TestNewDeal(t *testing.T) {
	t.Parallel()

	t.Run("builds an unsaved deal with the spec values", func(t *testing.T) {
		t.Parallel()
		spec := validDealSpec(t)

		d, err := domain.NewDeal(spec)

		require.NoError(t, err)
		require.Zero(t, d.ID())
		require.Equal(t, spec.ProductID, d.ProductID())
		require.Equal(t, spec.Title, d.Title())
		require.Equal(t, spec.Price, d.Price())
		require.Equal(t, spec.TotalStock, d.TotalStock())
		require.Equal(t, spec.PerUserLimit, d.PerUserLimit())
		require.Zero(t, d.Reserved())
		require.Zero(t, d.Sold())
		require.Equal(t, spec.TotalStock, d.Available())
		require.Equal(t, spec.StartsAt, d.StartsAt())
		require.Equal(t, spec.EndsAt, d.EndsAt())
	})

	t.Run("stores the window in UTC", func(t *testing.T) {
		t.Parallel()
		bogota := time.FixedZone("COT", -5*60*60)
		spec := validDealSpec(t)
		spec.StartsAt = t0.In(bogota)
		spec.EndsAt = t0.Add(time.Hour).In(bogota)

		d, err := domain.NewDeal(spec)

		require.NoError(t, err)
		require.Equal(t, time.UTC, d.StartsAt().Location())
		require.Equal(t, time.UTC, d.EndsAt().Location())
		require.True(t, d.StartsAt().Equal(t0))
	})

	zeroUSD := usd(t, 0)
	tests := []struct {
		name   string
		mutate func(*domain.DealSpec)
	}{
		{name: "rejects a missing product id", mutate: func(s *domain.DealSpec) { s.ProductID = "" }},
		{name: "rejects a missing title", mutate: func(s *domain.DealSpec) { s.Title = "" }},
		{name: "rejects a zero price", mutate: func(s *domain.DealSpec) { s.Price = zeroUSD }},
		{name: "rejects a price without a currency", mutate: func(s *domain.DealSpec) { s.Price = domain.Money{} }},
		{name: "rejects zero stock", mutate: func(s *domain.DealSpec) { s.TotalStock = 0 }},
		{name: "rejects negative stock", mutate: func(s *domain.DealSpec) { s.TotalStock = -5 }},
		{name: "rejects a zero per-user limit", mutate: func(s *domain.DealSpec) { s.PerUserLimit = 0 }},
		{name: "rejects a window that ends when it starts", mutate: func(s *domain.DealSpec) { s.EndsAt = s.StartsAt }},
		{name: "rejects a window that ends before it starts", mutate: func(s *domain.DealSpec) { s.EndsAt = s.StartsAt.Add(-time.Second) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := validDealSpec(t)
			tc.mutate(&spec)

			_, err := domain.NewDeal(spec)

			requireCode(t, err, domain.CodeValidation)
		})
	}
}

func TestRestoreDeal(t *testing.T) {
	t.Parallel()

	t.Run("rebuilds every persisted field", func(t *testing.T) {
		t.Parallel()
		s := validDealState(t)

		d, err := domain.RestoreDeal(s)

		require.NoError(t, err)
		require.Equal(t, s.ID, d.ID())
		require.Equal(t, s.Reserved, d.Reserved())
		require.Equal(t, s.Sold, d.Sold())
		require.Equal(t, s.TotalStock-s.Reserved-s.Sold, d.Available())
		require.Equal(t, s.CreatedAt, d.CreatedAt())
		require.Equal(t, s.UpdatedAt, d.UpdatedAt())
	})

	t.Run("accepts a deal whose stock is fully reserved or sold", func(t *testing.T) {
		t.Parallel()
		s := validDealState(t)
		s.Reserved, s.Sold = 400, 600

		d, err := domain.RestoreDeal(s)

		require.NoError(t, err)
		require.Zero(t, d.Available())
	})

	tests := []struct {
		name   string
		mutate func(*domain.DealState)
	}{
		{name: "rejects a non-positive id", mutate: func(s *domain.DealState) { s.ID = 0 }},
		{name: "rejects negative reserved units", mutate: func(s *domain.DealState) { s.Reserved = -1 }},
		{name: "rejects negative sold units", mutate: func(s *domain.DealState) { s.Sold = -1 }},
		{name: "rejects reserved plus sold above total stock", mutate: func(s *domain.DealState) { s.Reserved, s.Sold = 600, 401 }},
		{name: "rejects an invalid window", mutate: func(s *domain.DealState) { s.EndsAt = s.StartsAt }},
	}
	for _, tc := range tests {
		t.Run(tc.name+" as corrupt state, not a client error", func(t *testing.T) {
			t.Parallel()
			s := validDealState(t)
			tc.mutate(&s)

			_, err := domain.RestoreDeal(s)

			require.ErrorIs(t, err, domain.ErrCorruptState)
			_, isDomain := domain.AsDomain(err)
			require.False(t, isDomain, "corrupt data must not surface as a client-facing code")
		})
	}
}

func TestDealIsActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "is inactive before the window opens", now: t0.Add(-time.Nanosecond), want: false},
		{name: "is active exactly when the window opens", now: t0, want: true},
		{name: "is active inside the window", now: t0.Add(30 * time.Minute), want: true},
		{name: "is active just before the window closes", now: t0.Add(time.Hour - time.Nanosecond), want: true},
		{name: "is inactive exactly when the window closes", now: t0.Add(time.Hour), want: false},
		{name: "is inactive after the window closes", now: t0.Add(2 * time.Hour), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := domain.NewDeal(validDealSpec(t))
			require.NoError(t, err)

			require.Equal(t, tc.want, d.IsActive(tc.now))
		})
	}
}

func TestDealCanReserve(t *testing.T) {
	t.Parallel()

	inWindow := t0.Add(time.Minute)
	tests := []struct {
		name     string
		now      time.Time
		qty      int
		wantErr  error
		wantCode domain.Code
	}{
		{name: "allows a quantity within the per-user limit", now: inWindow, qty: 1},
		{name: "allows exactly the per-user limit", now: inWindow, qty: 2},
		{name: "returns a validation error for zero quantity", now: inWindow, qty: 0, wantCode: domain.CodeValidation},
		{name: "returns a validation error for negative quantity", now: inWindow, qty: -1, wantCode: domain.CodeValidation},
		{name: "returns ErrDealNotActive before the window opens", now: t0.Add(-time.Minute), qty: 1, wantErr: domain.ErrDealNotActive},
		{name: "returns ErrDealNotActive after the window closes", now: t0.Add(time.Hour), qty: 1, wantErr: domain.ErrDealNotActive},
		{name: "returns ErrUserLimitReached above the per-user limit", now: inWindow, qty: 3, wantErr: domain.ErrUserLimitReached},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := domain.NewDeal(validDealSpec(t))
			require.NoError(t, err)

			err = d.CanReserve(tc.now, tc.qty)

			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.wantCode != "":
				requireCode(t, err, tc.wantCode)
			default:
				require.NoError(t, err)
			}
		})
	}

	t.Run("ignores the snapshot stock so a stale cache cannot reject as sold out", func(t *testing.T) {
		t.Parallel()
		s := validDealState(t)
		s.Reserved, s.Sold = 500, 500 // snapshot says nothing is left
		d, err := domain.RestoreDeal(s)
		require.NoError(t, err)

		require.NoError(t, d.CanReserve(inWindow, 1))
	})
}
