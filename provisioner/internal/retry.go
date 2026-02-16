package internal

import (
	"context"
	"time"
)

// Retry calls fn up to maxAttempts times with exponential backoff
// (100ms, 200ms, 400ms, 800ms, ...). Returns the last error if all attempts fail.
func Retry(maxAttempts int, fn func() error) error {
	var err error
	for i := 0; i < maxAttempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(100*(1<<i)) * time.Millisecond)
		}
	}
	return err
}

// RetryResult is like Retry but for functions that return a value.
func RetryResult[T any](maxAttempts int, fn func() (T, error)) (T, error) {
	var result T
	var err error
	for i := 0; i < maxAttempts; i++ {
		if result, err = fn(); err == nil {
			return result, nil
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(100*(1<<i)) * time.Millisecond)
		}
	}
	return result, err
}

// RetryWithContext is like Retry but respects context cancellation.
// Returns ctx.Err() if the context is cancelled before all attempts are exhausted.
func RetryWithContext(ctx context.Context, maxAttempts int, fn func() error) error {
	var err error
	for i := 0; i < maxAttempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < maxAttempts-1 {
			select {
			case <-time.After(time.Duration(100*(1<<i)) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return err
}

// RetryResultWithContext is like RetryResult but respects context cancellation.
// Returns ctx.Err() if the context is cancelled before all attempts are exhausted.
func RetryResultWithContext[T any](ctx context.Context, maxAttempts int, fn func() (T, error)) (T, error) {
	var result T
	var err error
	for i := 0; i < maxAttempts; i++ {
		if result, err = fn(); err == nil {
			return result, nil
		}
		if i < maxAttempts-1 {
			select {
			case <-time.After(time.Duration(100*(1<<i)) * time.Millisecond):
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
	}
	return result, err
}
