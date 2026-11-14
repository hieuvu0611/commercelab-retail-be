package db

import (
	"context"
	"time"
)

const (
	// startupAttempts is how many times a store is tried before a service gives up on it.
	startupAttempts = 5
	// startupRetryDelay is the pause between two attempts.
	startupRetryDelay = 1 * time.Second
)

// retryStartup runs connect until it succeeds, the attempts are used up or ctx is done.
// A store that reports healthy can still answer its first request slowly on a throttled container,
// so a single 2-second try at start-up is too strict.
func retryStartup(ctx context.Context, attempts int, delay time.Duration, connect func() error) error {
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = connect(); err == nil {
			return nil
		}
		if attempt == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}
