package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/clock"
)

func TestSystemNow(t *testing.T) {
	t.Parallel()

	t.Run("returns UTC at the microsecond precision PostgreSQL stores", func(t *testing.T) {
		t.Parallel()

		now := clock.System{}.Now()

		require.Equal(t, time.UTC, now.Location())
		require.Zero(t, now.Nanosecond()%int(time.Microsecond))
		require.WithinDuration(t, time.Now(), now, time.Second)
	})
}
