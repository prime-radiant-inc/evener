package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func FuzzRetryStreamCore(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		transient := NewStreamError("stub", "retry", nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := RetryStream(ctx, RetryStreamOptions{}, func(context.Context) (bool, error) { return false, nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-cancel: %v", err)
		}

		calls := 0
		if err := RetryStream(context.Background(), RetryStreamOptions{Policy: RetryPolicy{MaxRetries: -1}}, func(context.Context) (bool, error) {
			calls++
			return false, nil
		}); err != nil || calls != 1 {
			t.Fatalf("negative budget: calls=%d err=%v", calls, err)
		}

		ctx, cancel = context.WithCancel(context.Background())
		if err := RetryStream(ctx, RetryStreamOptions{Policy: RetryPolicy{MaxRetries: 1}}, func(context.Context) (bool, error) {
			cancel()
			return false, transient
		}); !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt cancel: %v", err)
		}

		retried, reset := 0, 0
		policy := RetryPolicy{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Second, OnRetry: func(error, int, time.Duration) { retried++ }}
		calls = 0
		err := RetryStream(context.Background(), RetryStreamOptions{
			Policy: policy, Sleep: func(context.Context, time.Duration) error { return nil }, RetryAfterPartial: true,
			OnReset: func() { reset++ },
		}, func(context.Context) (bool, error) {
			calls++
			if calls == 1 {
				return true, transient
			}
			return false, nil
		})
		if err != nil || retried != 1 || reset != 1 {
			t.Fatalf("retry callbacks: %v %d %d", err, retried, reset)
		}

		sleepErr := errors.New("sleep")
		err = RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: func(context.Context, time.Duration) error { return sleepErr }}, func(context.Context) (bool, error) { return false, transient })
		if !errors.Is(err, sleepErr) {
			t.Fatalf("sleep error: %v", err)
		}
		err = RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: noSleep}, func(context.Context) (bool, error) { return true, transient })
		if err == nil {
			t.Fatal("partial output must block retry")
		}
		err = RetryStream(context.Background(), RetryStreamOptions{Policy: RetryPolicy{MaxRetries: 0}}, func(context.Context) (bool, error) { return false, transient })
		if err == nil {
			t.Fatal("exhausted retry must return error")
		}

		retryAfter := 2 * time.Second
		limited := ErrorFromHTTPStatus("stub", 429, "slow", nil, &retryAfter)
		err = RetryStream(context.Background(), RetryStreamOptions{Policy: RetryPolicy{MaxRetries: 1, MaxDelay: time.Second}, Sleep: noSleep}, func(context.Context) (bool, error) { return false, limited })
		if err == nil {
			t.Fatal("server delay above max must stop retries")
		}
	})
}
