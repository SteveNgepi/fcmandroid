// Package pacer hands out start slots at a fixed rate to many goroutines.
package pacer

import (
	"context"
	"sync"
	"time"
)

// Pacer gives out slots no faster than one per interval. Every goroutine that
// shares a Pacer shares the rate, so it holds for the whole pool.
type Pacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time // earliest start of the next slot
}

// New returns a Pacer that hands out one slot per interval.
func New(interval time.Duration) *Pacer { return &Pacer{interval: interval} }

// Wait blocks until a slot is free or ctx ends.
func (p *Pacer) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	at := p.next
	if now := time.Now(); at.Before(now) {
		at = now
	}
	p.next = at.Add(p.interval)
	p.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pause moves the slots nobody has reserved yet to until. A goroutine already
// parked in Wait keeps its slot, so Pause does not delay a queue that was
// reserved ahead of time.
func (p *Pacer) Pause(until time.Time) {
	p.mu.Lock()
	if until.After(p.next) {
		p.next = until
	}
	p.mu.Unlock()
}
