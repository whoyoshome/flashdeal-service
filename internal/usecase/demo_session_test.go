package usecase_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func TestStartDemo(t *testing.T) {
	t.Parallel()

	t.Run("creates an active deal with demo stock and a buyer id", func(t *testing.T) {
		t.Parallel()
		deals := newFakeDeals()
		gate := &fakeGate{}
		clock := &fakeClock{now: t0}
		ids := &fakeIDs{}

		session, err := usecase.NewStartDemo(deals, gate, clock, ids).Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, "demo-00000000-0000-7000-8000-000000000001", session.UserID)
		require.Equal(t, usecase.DemoStock, session.Deal.TotalStock())
		require.Equal(t, 1, session.Deal.PerUserLimit())
		require.Equal(t, []int64{session.Deal.ID()}, gate.loaded)
		require.True(t, session.Deal.StartsAt().Before(t0) || session.Deal.StartsAt().Equal(t0.Add(-time.Minute)))
		require.Equal(t, t0.Add(usecase.DemoSessionTTL), session.Deal.EndsAt())
	})

	t.Run("returns a gate error after storing the deal", func(t *testing.T) {
		t.Parallel()
		deals := newFakeDeals()
		gate := &fakeGate{loadErr: errBoom}

		_, err := usecase.NewStartDemo(deals, gate, &fakeClock{now: t0}, &fakeIDs{}).Execute(t.Context())

		require.ErrorIs(t, err, errBoom)
		require.Len(t, deals.byID, 1)
	})
}

func TestDemoSessionTTL(t *testing.T) {
	t.Parallel()
	require.GreaterOrEqual(t, usecase.DemoSessionTTL, 10*time.Minute)
}
