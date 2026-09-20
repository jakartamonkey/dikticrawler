// Package ratelimit implements a minimal, dependency-free interval limiter:
// each call to Wait blocks until at least 1/ratePerSec seconds have passed
// since the previous call returned. It caps aggregate request rate across
// any number of concurrent callers regardless of how many workers share it.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func New(ratePerSec float64) *Limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	return &Limiter{interval: time.Duration(float64(time.Second) / ratePerSec)}
}

func (l *Limiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
