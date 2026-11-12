package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// quotaTestCoordinator returns a coordinator over dir driven by clk, so every
// test measures the shared cooldown with the same deterministic clock the
// retry policy uses and never waits on real time.
func quotaTestCoordinator(dir string, clk *budgetClock) *QuotaCoordinator {
	q := NewQuotaCoordinator(dir)
	q.now = clk.Now
	return q
}

// seedQuotaCooldown records a cooldown the way another process would: report
// it, then leave the waiter queue.
func seedQuotaCooldown(t *testing.T, q *QuotaCoordinator, key string, cooldown time.Duration) {
	t.Helper()
	const token = "seeded-cooldown"
	if err := q.reportAndEnqueue(context.Background(), QuotaWait{Key: key, Cooldown: cooldown}, token); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}
	if err := q.dropWaiter(context.Background(), key, token); err != nil {
		t.Fatalf("drop seeded waiter: %v", err)
	}
}

// Two sessions on the same host share one cooldown: a 429 recorded by the
// first must hold the second past its own shorter backoff.
func TestQuotaCoordinator_SessionsShareOneCooldown(t *testing.T) {
	clk := newBudgetClock()
	dir := t.TempDir()
	sessionA := quotaTestCoordinator(filepath.Join(dir, "quota"), clk)
	sessionB := quotaTestCoordinator(filepath.Join(dir, "quota"), clk)
	start := clk.Now()

	// A's rejection records a 30s cooldown, then A stops waiting.
	if err := sessionA.reportAndEnqueue(context.Background(), QuotaWait{Key: "bedrock", Lane: QuotaLaneRoot, Cooldown: 30 * time.Second}, "a"); err != nil {
		t.Fatalf("report A: %v", err)
	}
	if err := sessionA.dropWaiter(context.Background(), "bedrock", "a"); err != nil {
		t.Fatalf("drop A: %v", err)
	}

	// B starts one second later with a 1s backoff of its own. It must wait for
	// A's 30s cooldown, not dispatch at its own 2s mark.
	clk.sleep(context.Background(), time.Second) //nolint:errcheck // fake clock advances
	if err := sessionB.Wait(context.Background(), QuotaWait{
		Key:      "bedrock",
		Lane:     QuotaLaneDelegate,
		Cooldown: time.Second,
		Budget:   time.Minute,
	}, clk.sleep); err != nil {
		t.Fatalf("B Wait: %v", err)
	}
	if got, want := clk.Now().Sub(start), 30*time.Second; got != want {
		t.Fatalf("B dispatched %s after A's rejection, want %s (the shared cooldown, not B's 1s)", got, want)
	}
}

// A root waiting at the opening claims it; a delegate that is already waiting
// yields and dispatches only after the root's probe hold.
func TestQuotaCoordinator_RootClaimsOpeningBeforeDelegate(t *testing.T) {
	clk := newBudgetClock()
	q := quotaTestCoordinator(t.TempDir(), clk)
	const key = "bedrock"
	start := clk.Now()

	// The root is already waiting when the cooldown opens.
	if err := q.reportAndEnqueue(context.Background(), QuotaWait{Key: key, Lane: QuotaLaneRoot, Cooldown: 10 * time.Second}, "root"); err != nil {
		t.Fatalf("enqueue root: %v", err)
	}
	// Take the sleep step its Wait loop would take before blocking, so its
	// lease covers the cooldown instead of expiring first.
	if claimed, _, err := q.step(context.Background(), key, "root", QuotaLaneRoot, 0); err != nil {
		t.Fatalf("root step: %v", err)
	} else if claimed {
		t.Fatal("root claimed before the cooldown opened")
	}

	var rootClaimedAt time.Time
	var rootClaimed bool
	delegateSleeps := 0
	sleep := func(ctx context.Context, d time.Duration) error {
		delegateSleeps++
		if delegateSleeps == 2 {
			// The delegate's first wake: the cooldown is open. The staged root
			// claims through the same production step the waiter loop uses.
			claimed, _, err := q.step(ctx, key, "root", QuotaLaneRoot, 0)
			if err != nil {
				return err
			}
			if claimed {
				rootClaimed = true
				rootClaimedAt = clk.Now()
			}
		}
		return clk.sleep(ctx, d)
	}

	// The delegate's own 1s backoff cannot shorten the shared 10s cooldown.
	if err := q.Wait(context.Background(), QuotaWait{
		Key:      key,
		Lane:     QuotaLaneDelegate,
		Cooldown: time.Second,
		Budget:   time.Minute,
	}, sleep); err != nil {
		t.Fatalf("delegate Wait: %v", err)
	}
	if !rootClaimed {
		t.Fatal("the root never claimed the opening")
	}
	if got, want := rootClaimedAt.Sub(start), 10*time.Second; got != want {
		t.Fatalf("root claimed at %s, want %s", got, want)
	}
	if got, want := clk.Now().Sub(start), 11*time.Second; got != want {
		t.Fatalf("delegate dispatched at %s, want %s (after the root's probe hold)", got, want)
	}
}

// One opening admits one dispatch: a second waiter cannot claim the same
// cooldown boundary.
func TestQuotaCoordinator_OneDispatchPerOpening(t *testing.T) {
	clk := newBudgetClock()
	q := quotaTestCoordinator(t.TempDir(), clk)
	const key = "bedrock"
	start := clk.Now()

	seedQuotaCooldown(t, q, key, 10*time.Second)

	wait := QuotaWait{Key: key, Cooldown: time.Second, Budget: time.Minute}
	if err := q.Wait(context.Background(), wait, clk.sleep); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	first := clk.Now()
	if got, want := first.Sub(start), 10*time.Second; got != want {
		t.Fatalf("first dispatch at %s, want %s", got, want)
	}

	if err := q.Wait(context.Background(), wait, clk.sleep); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if got, want := clk.Now().Sub(first), quotaProbeHold; got != want {
		t.Fatalf("second dispatch %s after the first, want %s (one probe per opening)", got, want)
	}
}

// A canceled wait gives up without claiming the opening, so the retry loop
// cannot dispatch another attempt after the caller has gone away.
func TestQuotaCoordinator_CanceledWaitDoesNotClaim(t *testing.T) {
	clk := newBudgetClock()
	q := quotaTestCoordinator(t.TempDir(), clk)
	const key = "bedrock"
	seedQuotaCooldown(t, q, key, 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	sleep := func(context.Context, time.Duration) error {
		cancel()
		return nil
	}
	err := q.Wait(ctx, QuotaWait{Key: key, Cooldown: time.Second, Budget: time.Minute}, sleep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}
}

// A retry group sees a cooldown another process recorded before it began.
func TestRetry_RateLimitWaitsForAnotherSessionsCooldown(t *testing.T) {
	clk := newBudgetClock()
	q := quotaTestCoordinator(t.TempDir(), clk)
	const key = "bedrock"
	start := clk.Now()

	seedQuotaCooldown(t, q, key, 30*time.Second)

	policy := budgetPolicy(clk, 30*time.Minute)
	policy.MaxRetries = 0 // the shared cooldown, not the attempt cap, must bound this
	policy.Quota = q
	policy.QuotaKey = key
	policy.QuotaLane = QuotaLaneRoot

	attempts := 0
	resp, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
		attempts++
		if attempts == 1 {
			return Response{}, rateLimit429()
		}
		return Response{Provider: "bedrock", Model: "m", Message: Assistant("ok")}, nil
	})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("resp = %q, want ok", resp.Text())
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := clk.Now().Sub(start); got < 30*time.Second {
		t.Fatalf("second attempt dispatched after %s, want the other session's 30s cooldown", got)
	}
}

// The streaming loop shares the same cooldown as the completion loop.
func TestRetryStream_RateLimitWaitsForAnotherSessionsCooldown(t *testing.T) {
	clk := newBudgetClock()
	q := quotaTestCoordinator(t.TempDir(), clk)
	const key = "bedrock"
	start := clk.Now()

	seedQuotaCooldown(t, q, key, 30*time.Second)

	policy := budgetPolicy(clk, 30*time.Minute)
	policy.MaxRetries = 0
	policy.Quota = q
	policy.QuotaKey = key

	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls == 1 {
			return AttemptReport{Phase: PhaseOpen}, rateLimit429()
		}
		return AttemptReport{}, nil
	})
	if err != nil {
		t.Fatalf("RetryStream: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if got := clk.Now().Sub(start); got < 30*time.Second {
		t.Fatalf("second stream attempt dispatched after %s, want the other session's 30s cooldown", got)
	}
}

// Exhausting the run budget during a shared cooldown stays fatal, and it is
// reported as run-budget exhaustion rather than a retry that never ends.
func TestRetry_QuotaCoordinatorDeadlineExhaustionIsFatal(t *testing.T) {
	clk := newBudgetClock()
	q := quotaTestCoordinator(t.TempDir(), clk)
	const key = "bedrock"

	seedQuotaCooldown(t, q, key, 10*time.Minute)

	// The wait is measured on the fake clock, but the run's remaining budget is
	// snapshotted from the real deadline when the group starts. A generous
	// deadline keeps that snapshot positive on a slow machine; the fake clock
	// still consumes it without waiting.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	ctx = WithRunBudget(ctx)

	policy := budgetPolicy(clk, 30*time.Minute)
	policy.RateLimitShutdownReserve = 0
	policy.Quota = q
	policy.QuotaKey = key

	attempts := 0
	_, err := Retry(ctx, policy, clk.sleep, nil, func() (Response, error) {
		attempts++
		return Response{}, rateLimit429()
	})
	if !errors.Is(err, ErrRunBudgetExhausted) {
		t.Fatalf("err = %v, want ErrRunBudgetExhausted once the run budget is spent", err)
	}
	if Kind(err) == KindRateLimit {
		t.Fatal("the raw 429 leaked through instead of the run-budget settlement")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no dispatch after the budget is spent)", attempts)
	}
}

// Non-rate-limit failures never enter the coordinator, and a rate limit
// without a wall budget keeps its attempt-counted behavior.
func TestRetry_QuotaCoordinatorLeavesOtherFailuresAlone(t *testing.T) {
	t.Run("server error stays attempt counted", func(t *testing.T) {
		clk := newBudgetClock()
		q := quotaTestCoordinator(filepath.Join(t.TempDir(), "quota"), clk)
		policy := budgetPolicy(clk, 30*time.Minute)
		policy.MaxRetries = 2
		policy.Quota = q
		policy.QuotaKey = "bedrock"

		attempts := 0
		_, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
			attempts++
			return Response{}, ErrorFromHTTPStatus("bedrock", 503, "unavailable", nil, nil)
		})
		if err == nil {
			t.Fatal("expected the 503")
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3 (1 + MaxRetries)", attempts)
		}
		if _, statErr := os.Stat(q.dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("quota state created for a 503: %v", statErr)
		}
	})

	t.Run("auth failure fails on the first attempt", func(t *testing.T) {
		clk := newBudgetClock()
		q := quotaTestCoordinator(filepath.Join(t.TempDir(), "quota"), clk)
		policy := budgetPolicy(clk, 30*time.Minute)
		policy.Quota = q
		policy.QuotaKey = "bedrock"

		attempts := 0
		_, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
			attempts++
			return Response{}, ErrorFromHTTPStatus("bedrock", 401, "invalid api key", nil, nil)
		})
		if err == nil {
			t.Fatal("expected the 401")
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
		if _, statErr := os.Stat(q.dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("quota state created for a 401: %v", statErr)
		}
	})

	t.Run("rate limit without a wall budget stays attempt counted", func(t *testing.T) {
		clk := newBudgetClock()
		q := quotaTestCoordinator(filepath.Join(t.TempDir(), "quota"), clk)
		policy := budgetPolicy(clk, 0)
		policy.MaxRetries = 1
		policy.Quota = q
		policy.QuotaKey = "bedrock"

		attempts := 0
		_, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
			attempts++
			return Response{}, rateLimit429()
		})
		if err == nil {
			t.Fatal("expected the 429")
		}
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2 (1 + MaxRetries, no wall budget)", attempts)
		}
		if _, statErr := os.Stat(q.dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("quota state created without a wall budget: %v", statErr)
		}
	})
}
