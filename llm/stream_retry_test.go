package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func noSleepRetry(context.Context, time.Duration) error { return nil }

func fastPolicy(maxRetries int) RetryPolicy {
	return RetryPolicy{MaxRetries: maxRetries, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, BackoffMultiplier: 2.0}
}

func TestRetryStream_SucceedsFirstAttempt(t *testing.T) {
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (bool, error) {
		calls++
		return false, nil
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestRetryStream_RetriesRetryableThenSucceeds(t *testing.T) {
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (bool, error) {
		calls++
		if calls < 3 {
			return false, NewStreamError("openai", "truncated", nil)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
}

func TestRetryStream_ExhaustsBudget(t *testing.T) {
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(4), Sleep: noSleepRetry}, func(context.Context) (bool, error) {
		calls++
		return false, NewStreamError("openai", "truncated", nil)
	})
	if err == nil {
		t.Fatal("expected error after exhausting budget")
	}
	if calls != 5 {
		t.Fatalf("calls=%d, want 5 (1 initial + 4 retries)", calls)
	}
}

func TestRetryStream_DoesNotRetryPermanent(t *testing.T) {
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (bool, error) {
		calls++
		return false, ErrorFromHTTPStatus("openai", 403, "forbidden", nil, nil)
	})
	if err == nil {
		t.Fatal("expected permanent error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1 (no retry on permanent)", calls)
	}
}

func TestRetryStream_PartialOutputBlocksRetryByDefault(t *testing.T) {
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (bool, error) {
		calls++
		return true, NewStreamError("openai", "truncated", nil)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1 (partial output blocks retry by default)", calls)
	}
}

func TestRetryStream_RetryAfterPartialResetsAndRetries(t *testing.T) {
	calls := 0
	resets := 0
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:            fastPolicy(5),
		Sleep:             noSleepRetry,
		RetryAfterPartial: true,
		OnReset:           func() { resets++ },
	}, func(context.Context) (bool, error) {
		calls++
		if calls < 3 {
			return true, NewStreamError("openai", "truncated", nil)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
	if resets != 2 {
		t.Fatalf("resets=%d, want 2 (one before each retry after partial output)", resets)
	}
}

func TestRetryStream_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RetryStream(ctx, RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (bool, error) {
		calls++
		cancel()
		return false, NewStreamError("openai", "truncated", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}
