package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// quotaStatePathIn returns the coordinator's published state file, whichever
// naming scheme created it, so a test can inspect state without depending on
// the test-only path helper.
func quotaStatePathIn(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read quota dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".lock") || strings.Contains(name, ".tmp-") {
			continue
		}
		return filepath.Join(dir, name)
	}
	return ""
}

func readQuotaState(t *testing.T, dir string) quotaState {
	t.Helper()
	path := quotaStatePathIn(t, dir)
	if path == "" {
		t.Fatal("no quota state file was published")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read quota state %s: %v", path, err)
	}
	var st quotaState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal quota state %s: %v", path, err)
	}
	return st
}

// A provider named "openai.lock" is valid. Its state file must not be the
// guard file "openai" locks, or publishing it replaces the locked inode and
// lets concurrent writers in.
func TestQuotaCoordinator_StatePathDoesNotCollideWithLockPath(t *testing.T) {
	clk := newBudgetClock()
	dir := t.TempDir()
	q := quotaTestCoordinator(dir, clk)
	const provider = "openai"

	seedQuotaCooldown(t, q, provider, 0)

	lockPaths, err := filepath.Glob(filepath.Join(dir, "*.lock"))
	if err != nil || len(lockPaths) != 1 {
		t.Fatalf("lock files for %q = %v (err %v), want exactly one", provider, lockPaths, err)
	}
	lockPath := lockPaths[0]
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat %s before publishing %q state: %v", lockPath, provider+".lock", err)
	}

	seedQuotaCooldown(t, q, provider+".lock", 0)

	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat %s after publishing %q state: %v", lockPath, provider+".lock", err)
	}
	if !os.SameFile(before, after) {
		t.Fatalf("publishing %q state replaced %s, the lock file for %q", provider+".lock", lockPath, provider)
	}
	if _, err := os.Stat(filepath.Join(dir, provider+".json")); err != nil {
		t.Fatalf("state for %q does not live at %s.json: %v", provider, provider, err)
	}
}

// A 60s Retry-After with five seconds left in this caller's budget must publish
// the full 60s cooldown for other sessions while this caller waits only its
// remaining five seconds.
func TestRetry_QuotaCooldownKeepsFullRetryAfter(t *testing.T) {
	clk := newBudgetClock()
	dir := t.TempDir()
	q := quotaTestCoordinator(dir, clk)
	const key = "bedrock"
	start := clk.Now()

	policy := budgetPolicy(clk, 5*time.Second)
	policy.Quota = q
	policy.QuotaKey = key

	retryAfter := 60 * time.Second
	limited := ErrorFromHTTPStatus("bedrock", 429, "rate limited", nil, &retryAfter)
	attempts := 0
	_, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
		attempts++
		return Response{}, limited
	})
	if err == nil {
		t.Fatal("Retry succeeded after a rate limit that outlasted the budget")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no dispatch after the budget is spent)", attempts)
	}
	if got, want := clk.Now().Sub(start), 5*time.Second; got != want {
		t.Fatalf("caller waited %s, want its %s budget", got, want)
	}
	st := readQuotaState(t, dir)
	if got, want := time.Unix(0, st.CooldownUntil).Sub(start), 60*time.Second; got != want {
		t.Fatalf("shared cooldown = %s, want the full %s Retry-After, not the caller's %s budget", got, want, 5*time.Second)
	}
}

// The streaming loop publishes the same full provider cooldown as Retry.
func TestRetryStream_QuotaCooldownKeepsFullRetryAfter(t *testing.T) {
	clk := newBudgetClock()
	dir := t.TempDir()
	q := quotaTestCoordinator(dir, clk)
	const key = "bedrock"
	start := clk.Now()

	policy := budgetPolicy(clk, 5*time.Second)
	policy.Quota = q
	policy.QuotaKey = key

	retryAfter := 60 * time.Second
	limited := ErrorFromHTTPStatus("bedrock", 429, "rate limited", nil, &retryAfter)
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{Phase: PhaseOpen}, limited
	})
	if err == nil {
		t.Fatal("RetryStream succeeded after a rate limit that outlasted the budget")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no dispatch after the budget is spent)", calls)
	}
	if got, want := clk.Now().Sub(start), 5*time.Second; got != want {
		t.Fatalf("caller waited %s, want its %s budget", got, want)
	}
	st := readQuotaState(t, dir)
	if got, want := time.Unix(0, st.CooldownUntil).Sub(start), 60*time.Second; got != want {
		t.Fatalf("shared cooldown = %s, want the full %s Retry-After, not the caller's %s budget", got, want, 5*time.Second)
	}
}

// quotaStepClock advances 30 seconds on its second read: the time one state
// write spends behind a suspended lock holder.
type quotaStepClock struct {
	mu     sync.Mutex
	now    time.Time
	calls  int
	sleeps []time.Duration
}

func (c *quotaStepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 2 {
		c.now = c.now.Add(30 * time.Second)
	}
	return c.now
}

func (c *quotaStepClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *quotaStepClock) sleepDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// Coordination time is part of the wait: 30 seconds spent publishing state
// inside a 10-second budget leaves no budget for a sleep or a dispatch.
func TestQuotaCoordinator_CountsCoordinationTimeAgainstBudget(t *testing.T) {
	clk := &quotaStepClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	q := NewQuotaCoordinator(dir)
	q.now = clk.Now

	err := q.Wait(context.Background(), QuotaWait{
		Key:      "bedrock",
		Cooldown: time.Second,
		Budget:   10 * time.Second,
	}, clk.sleep)
	if !errors.Is(err, errQuotaWaitExhausted) {
		t.Fatalf("Wait = %v, want errQuotaWaitExhausted: 30s spent coordinating must consume the 10s budget", err)
	}
	if sleeps := clk.sleepDurations(); len(sleeps) != 0 {
		t.Fatalf("Wait slept %v after the budget was already spent coordinating", sleeps)
	}
}

// If registration succeeds but the first step cannot write state, Wait must
// still wait out the caller's announced backoff, clipped to the budget left
// after the coordination time already spent.
func TestQuotaCoordinator_StateStoreFailureWaitsOutLocalCooldown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
		want   time.Duration
	}{
		{name: "sleeps the unspent cooldown", budget: time.Minute, want: 6 * time.Second},
		{name: "clips to the remaining budget", budget: 8 * time.Second, want: 4 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newBudgetClock()
			dir := t.TempDir()
			q := quotaTestCoordinator(dir, clk)
			sabotaged := false
			var sabotageErr error
			// The state file exists only after registration succeeded. The
			// first clock read after that replaces it with a directory and
			// spends four seconds coordinating, so the first step's read fails.
			q.now = func() time.Time {
				if !sabotaged {
					if path := quotaStatePathIn(t, dir); path != "" {
						sabotaged = true
						sabotageErr = os.Remove(path)
						if sabotageErr == nil {
							sabotageErr = os.Mkdir(path, 0o700)
						}
						clk.now = clk.now.Add(4 * time.Second)
					}
				}
				return clk.Now()
			}

			err := q.Wait(context.Background(), QuotaWait{
				Key:      "bedrock",
				Cooldown: 10 * time.Second,
				Budget:   tc.budget,
			}, clk.sleep)
			if sabotageErr != nil {
				t.Fatalf("sabotage quota state: %v", sabotageErr)
			}
			if err != nil {
				t.Fatalf("Wait = %v, want the best-effort dispatch after waiting out the cooldown", err)
			}
			if sleeps := clk.sleeps; len(sleeps) != 1 || sleeps[0] != tc.want {
				t.Fatalf("sleeps = %v, want one %s (the unspent local cooldown inside the budget)", sleeps, tc.want)
			}
		})
	}
}
