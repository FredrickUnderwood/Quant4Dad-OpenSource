package datasource

import (
	"context"
	"sync"
	"time"

	"github.com/quant4dad/internal/domain"
)

// rateLimiter spaces outgoing requests to stay under a per-minute budget.
//
// Even with sync concurrency set to 1, a fast-responding upstream can exceed a
// per-minute quota. This uses "minimum interval + slot reservation" so that no
// rolling one-minute window ever admits more than perMin requests: each caller
// atomically claims the next time slot, then blocks until that moment.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRateLimiter(perMin int) *rateLimiter {
	if perMin <= 0 {
		return nil // limiting disabled
	}
	return &rateLimiter{interval: time.Minute / time.Duration(perMin)}
}

// Wait blocks until the next available slot, or until ctx is cancelled. A nil
// receiver means limiting is disabled and the call passes straight through.
func (l *rateLimiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	slot := l.next
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	wait := time.Until(slot)
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// rateLimitedClient is a provider-agnostic decorator: it passes through the
// limiter gate before each method that actually hits upstream (ListInstruments
// and FetchBars, one HTTP call each).
type rateLimitedClient struct {
	Client
	lim *rateLimiter
}

func (c *rateLimitedClient) ListInstruments(ctx context.Context) ([]*domain.Instrument, error) {
	if err := c.lim.Wait(ctx); err != nil {
		return nil, err
	}
	return c.Client.ListInstruments(ctx)
}

func (c *rateLimitedClient) FetchBars(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	if err := c.lim.Wait(ctx); err != nil {
		return nil, err
	}
	return c.Client.FetchBars(ctx, code, period, start, end)
}

func (c *rateLimitedClient) FetchInstrumentBars(ctx context.Context, instrument *domain.Instrument, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	if typed, ok := c.Client.(InstrumentBarsClient); ok {
		if err := c.lim.Wait(ctx); err != nil {
			return nil, err
		}
		return typed.FetchInstrumentBars(ctx, instrument, period, start, end)
	}
	return c.FetchBars(ctx, instrument.Code, period, start, end)
}
