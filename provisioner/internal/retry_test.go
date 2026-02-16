package internal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryWithContext_Success(t *testing.T) {
	attempts := 0
	err := RetryWithContext(context.Background(), 3, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryWithContext_ExhaustsAttempts(t *testing.T) {
	attempts := 0
	err := RetryWithContext(context.Background(), 3, func() error {
		attempts++
		return errors.New("always fails")
	})
	if err == nil || err.Error() != "always fails" {
		t.Fatalf("expected 'always fails' error, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryWithContext_CancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := RetryWithContext(ctx, 10, func() error {
		attempts++
		return errors.New("fail")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if attempts >= 10 {
		t.Fatalf("expected fewer than 10 attempts due to cancellation, got %d", attempts)
	}
}

func TestRetryResultWithContext_Success(t *testing.T) {
	attempts := 0
	result, err := RetryResultWithContext(context.Background(), 3, func() (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("not yet")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected 'ok', got %q", result)
	}
}

func TestRetryResultWithContext_ExhaustsAttempts(t *testing.T) {
	result, err := RetryResultWithContext(context.Background(), 2, func() (int, error) {
		return -1, errors.New("always fails")
	})
	if err == nil || err.Error() != "always fails" {
		t.Fatalf("expected 'always fails' error, got %v", err)
	}
	if result != -1 {
		t.Fatalf("expected -1, got %d", result)
	}
}

func TestRetryResultWithContext_CancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := RetryResultWithContext(ctx, 10, func() (string, error) {
		attempts++
		return "", errors.New("fail")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if attempts >= 10 {
		t.Fatalf("expected fewer than 10 attempts due to cancellation, got %d", attempts)
	}
}
