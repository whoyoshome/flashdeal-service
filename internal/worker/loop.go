package worker

import (
	"context"
	"time"
)

// Tracer starts a span around one pass of a worker, so database and Redis
// calls made by the pass appear under it. platform/observability implements
// it.
type Tracer interface {
	StartSpan(ctx context.Context, name string) (context.Context, func(error))
}

// runEvery runs pass every interval, each run inside its own span named
// span, until ctx is cancelled, then returns nil: stopping is the normal end
// of a worker, not a failure. A slow pass delays the next one instead of
// overlapping with it, because the ticker drops ticks nobody is waiting for.
// pass reports its own errors; the one it returns only marks its span.
func runEvery(ctx context.Context, interval time.Duration, tracer Tracer, span string, pass func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			passCtx, end := tracer.StartSpan(ctx, span)
			end(pass(passCtx))
		}
	}
}
