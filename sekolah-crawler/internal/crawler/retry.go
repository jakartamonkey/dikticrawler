package crawler

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"time"

	"sekolah-crawler/internal/api"
)

// withRetry retries fn with exponential backoff + jitter. A *api.ClientError
// (non-429 4xx) is treated as permanent — e.g. a malformed sekolah_id will
// never succeed no matter how many times it's retried. Everything else
// (network errors, 5xx, 429) is retried up to maxAttempts times, honoring
// Retry-After on 429 when present.
func withRetry(ctx context.Context, maxAttempts int, fn func() error) error {
	var lastErr error
	backoff := 500 * time.Millisecond

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		var ce *api.ClientError
		if errors.As(err, &ce) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == maxAttempts {
			break
		}

		wait := backoff + time.Duration(rand.Int63n(int64(backoff)/2+1))
		var rl *api.RateLimitedError
		if errors.As(err, &rl) && rl.RetryAfter != "" {
			if secs, perr := strconv.Atoi(rl.RetryAfter); perr == nil {
				wait = time.Duration(secs) * time.Second
			}
		}

		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
	return lastErr
}
