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
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{}, nil
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
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls < 3 {
			return AttemptReport{}, NewStreamError("openai", "truncated", nil)
		}
		return AttemptReport{}, nil
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
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(4), Sleep: noSleepRetry}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{}, NewStreamError("openai", "truncated", nil)
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
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{}, ErrorFromHTTPStatus("openai", 403, "forbidden", nil, nil)
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
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{PartialOutput: true}, NewStreamError("openai", "truncated", nil)
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
	}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls < 3 {
			return AttemptReport{PartialOutput: true}, NewStreamError("openai", "truncated", nil)
		}
		return AttemptReport{}, nil
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
	err := RetryStream(ctx, RetryStreamOptions{Policy: fastPolicy(5), Sleep: noSleepRetry}, func(context.Context) (AttemptReport, error) {
		calls++
		cancel()
		return AttemptReport{}, NewStreamError("openai", "truncated", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func attemptScript(t *testing.T, reports []AttemptReport, errs []error) (StreamAttempt, *int) {
	calls := 0
	return func(ctx context.Context) (AttemptReport, error) {
		i := calls
		calls++
		if i >= len(reports) {
			t.Fatalf("unexpected attempt %d", i)
		}
		return reports[i], errs[i]
	}, &calls
}

func TestRetryStream_StreakStopsAtFailFastAfter(t *testing.T) {
	// 4 consume-phase failures -> ProviderUnhealthyError, exactly 4 attempts.
	rep := AttemptReport{Phase: PhaseConsume}
	e := NewStreamError("p", "cut", nil)
	attempt, calls := attemptScript(t,
		[]AttemptReport{rep, rep, rep, rep}, []error{e, e, e, e})
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:        RetryPolicy{MaxRetries: 10, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
		FailFastAfter: 4,
	}, attempt)
	var pu *ProviderUnhealthyError
	if !errors.As(err, &pu) {
		t.Fatalf("want ProviderUnhealthyError, got %v", err)
	}
	if *calls != 4 {
		t.Fatalf("attempts = %d, want 4", *calls)
	}
	if pu.Attempts != 4 || pu.Shape != "stall" {
		t.Fatalf("bad stats: %+v", pu)
	}
}

func TestRetryStream_OpenPhaseTransparent(t *testing.T) {
	// stall,429,stall,429,stall,stall -> trips at the 4th stall (6 attempts total).
	stall := AttemptReport{Phase: PhaseSilentStall}
	open := AttemptReport{Phase: PhaseOpen}
	e := NewStreamError("p", "cut", nil)
	attempt, calls := attemptScript(t,
		[]AttemptReport{stall, open, stall, open, stall, stall},
		[]error{e, e, e, e, e, e})
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:        RetryPolicy{MaxRetries: 10, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
		FailFastAfter: 4,
	}, attempt)
	var pu *ProviderUnhealthyError
	if !errors.As(err, &pu) || *calls != 6 {
		t.Fatalf("calls=%d err=%v", *calls, err)
	}
}

func TestRetryStream_CapDetectionStopsAtTwo(t *testing.T) {
	long := AttemptReport{Phase: PhaseConsume, ContentWindow: 70 * time.Second, SalvagedBytes: 100}
	e := NewStreamError("p", "cut", nil)
	attempt, calls := attemptScript(t, []AttemptReport{long, long}, []error{e, e})
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:        RetryPolicy{MaxRetries: 10, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
		FailFastAfter: 4,
	}, attempt)
	var pu *ProviderUnhealthyError
	if !errors.As(err, &pu) {
		t.Fatalf("want ProviderUnhealthyError, got %v", err)
	}
	if *calls != 2 || pu.Shape != "cap" {
		t.Fatalf("calls=%d shape=%q", *calls, pu.Shape)
	}
}

func TestRetryStream_FastRejectTransparent_AndDisabledWhenZero(t *testing.T) {
	// FailFastAfter=0: 4 consume failures ride the policy budget (MaxRetries=3 -> 4 attempts, last err returned raw).
	rep := AttemptReport{Phase: PhaseConsume}
	e := NewStreamError("p", "cut", nil)
	attempt, calls := attemptScript(t, []AttemptReport{rep, rep, rep, rep}, []error{e, e, e, e})
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy: RetryPolicy{MaxRetries: 3, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
	}, attempt)
	var pu *ProviderUnhealthyError
	if errors.As(err, &pu) {
		t.Fatal("FailFastAfter=0 must not early-stop")
	}
	if *calls != 4 {
		t.Fatalf("calls=%d", *calls)
	}
}

func TestRetryStream_FastRejectTransparent(t *testing.T) {
	// Four fast rejections must never trip the streak (FailFastAfter=4): spec
	// requires PhaseFastReject be transparent like PhaseOpen, so a provider that
	// fails fast with zero content events every time must never produce the
	// "provider stopped responding mid-stream" steering. 8 attempts (double
	// FailFastAfter) proves this isn't a coincidence of the budget running out
	// at the same count as the streak threshold.
	reject := AttemptReport{Phase: PhaseFastReject}
	e := NewStreamError("p", "cut", nil)
	reports := []AttemptReport{reject, reject, reject, reject, reject, reject, reject, reject}
	errs := []error{e, e, e, e, e, e, e, e}
	attempt, calls := attemptScript(t, reports, errs)
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:        RetryPolicy{MaxRetries: 7, BaseDelay: time.Nanosecond, BackoffMultiplier: 1},
		FailFastAfter: 4,
	}, attempt)
	var pu *ProviderUnhealthyError
	if errors.As(err, &pu) {
		t.Fatalf("fast-reject attempts must not trip ProviderUnhealthyError, got %v", err)
	}
	if *calls != 8 {
		t.Fatalf("calls=%d, want 8", *calls)
	}
}
