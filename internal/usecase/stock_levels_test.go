package usecase_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func TestStockLevels(t *testing.T) {
	t.Parallel()

	t.Run("reports the gate stock of every active deal that is loaded", func(t *testing.T) {
		t.Parallel()
		deals := newFakeDeals(dealWithCounters(t, 7, 0, 0), dealWithCounters(t, 8, 0, 0), dealWithCounters(t, 9, 0, 0))
		gate := &fakeRepair{stock: map[int64]int{7: 4, 9: 0}} // 8 not activated yet

		levels, err := usecase.NewStockLevels(deals, gate, &fakeClock{now: t0}).Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, map[int64]int{7: 4, 9: 0}, levels)
		require.Equal(t, t0, deals.listArgs[0].now)
	})

	t.Run("returns the error when the gate cannot be read", func(t *testing.T) {
		t.Parallel()
		gate := &fakeRepair{stock: map[int64]int{7: 4}, stockErr: errBoom}

		_, err := usecase.NewStockLevels(newFakeDeals(dealWithCounters(t, 7, 0, 0)), gate, &fakeClock{now: t0}).Execute(t.Context())

		require.ErrorIs(t, err, errBoom)
	})

	t.Run("returns the error when active deals cannot be listed", func(t *testing.T) {
		t.Parallel()
		deals := newFakeDeals()
		deals.listErr = errBoom

		_, err := usecase.NewStockLevels(deals, &fakeRepair{}, &fakeClock{now: t0}).Execute(t.Context())

		require.ErrorIs(t, err, errBoom)
	})
}
