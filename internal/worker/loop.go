package worker

import (
	"context"
	"time"
)

// runEvery calls fn every interval until ctx is cancelled, then returns
// nil: stopping is the normal end of a worker, not a failure. A slow fn
// delays the next call instead of overlapping with it, because the ticker
// drops ticks nobody is waiting for.
func runEvery(ctx context.Context, interval time.Duration, fn func(context.Context)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			fn(ctx)
		}
	}
}
