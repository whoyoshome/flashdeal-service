//go:build e2e

package e2e_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

var (
	redisAddr   = envOr("E2E_REDIS_ADDR", "localhost:6379")
	eventStream = envOr("E2E_EVENT_STREAM", "flashdeal:events")
)

// TestConfirmPublishesOrderConfirmed follows an order from the HTTP
// confirmation to the Redis Stream, through the outbox relay.
func TestConfirmPublishesOrderConfirmed(t *testing.T) {
	t.Parallel()
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })

	deal := createDeal(t, 1, 1, true)
	user := unique("buyer")
	res := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(t, user, unique("key")))
	require.Equal(t, http.StatusCreated, res.Status, res.RawBody)
	order := call(t, http.MethodPost, "/v1/reservations/"+res.Body["id"].(string)+"/confirm", "",
		map[string]string{"Authorization": bearer(t, user, auth.RoleUser)})
	require.Equal(t, http.StatusOK, order.Status, order.RawBody)
	orderID := order.Body["id"].(string)

	var event map[string]any
	require.Eventually(t, func() bool {
		entries, err := rdb.XRevRangeN(t.Context(), eventStream, "+", "-", 1000).Result()
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.Values["aggregate_id"] == orderID {
				event = e.Values
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "the relay publishes within OUTBOX_EVERY")

	require.Equal(t, "order.confirmed", event["type"])
	require.NotEmpty(t, event["event_id"])
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(event["payload"].(string)), &payload))
	require.Equal(t, orderID, payload["order_id"])
	require.Equal(t, user, payload["user_id"])
	require.InDelta(t, 4999, payload["total_cents"], 0)
}
