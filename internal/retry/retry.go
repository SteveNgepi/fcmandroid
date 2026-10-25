// Package retry repeats a step that failed for a transient reason.
package retry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	attempts  = 4
	baseDelay = 500 * time.Millisecond
	maxDelay  = 30 * time.Second
)

// Stop, returned as the wait of a failed attempt, ends the retries: the error is final.
const Stop time.Duration = -1

// Do calls fn until it succeeds, asks to Stop, runs out of attempts or ctx ends.
// fn returns the error of the attempt and how long to wait before the next one:
// 0 for the usual backoff, a positive duration for at least that long, Stop for
// a final error. Both waits carry jitter, so workers refused together do not
// wake together. The error of the last attempt is returned unchanged. When ctx
// ends first, cancelling a wait at once, the error wraps both the context's
// error and the last attempt's.
func Do(ctx context.Context, fn func() (wait time.Duration, err error)) error {
	var err error
	for i := range attempts {
		var wait time.Duration
		wait, err = fn()
		if err == nil {
			return nil
		}
		if wait == Stop {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w (last attempt: %w)", ctxErr, err)
		}
		if i == attempts-1 {
			break
		}
		if sleepErr := sleep(ctx, pause(i+1, wait)); sleepErr != nil {
			return fmt.Errorf("%w (last attempt: %w)", sleepErr, err)
		}
	}
	return err
}

// pause is the wait before attempt n+1: the requested wait plus up to 25%, or
// half to full of baseDelay doubled per attempt when none was requested.
func pause(n int, requested time.Duration) time.Duration {
	if requested > 0 {
		return requested + rand.N(requested/4+1)
	}
	step := min(baseDelay<<(n-1), maxDelay)
	return step/2 + rand.N(step/2+1)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
