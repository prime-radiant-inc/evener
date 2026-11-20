// Package testwait waits for a condition in a test that cannot run under
// testing/synctest because it drives a real subprocess, socket or filesystem
// watcher. Anything a synctest bubble can hold belongs in one instead: there
// the fake clock makes the wait instant and exact.
//
// The wait takes no timeout argument. A per-call timeout is a guess about how
// slow the host is, and every guess re-flakes as the suite grows; one shared
// ceiling bounds every wait instead, and the helper gives up early when the
// test binary's own deadline is nearer, so the failure names the condition
// instead of arriving as a goroutine dump.
package testwait

import (
	"testing"
	"time"
)

const (
	// deadlineMargin is how long before the test binary's deadline the wait
	// gives up, so its failure reports before `go test` kills the binary.
	deadlineMargin = 5 * time.Second

	// waitCeiling bounds every wait. It sits about 10x above the 3s per-test
	// ceiling the timing ratchet holds tests to (perTestCeilingSeconds in
	// testing-budget.json), so only a stall far beyond any observed load hits
	// it, and a broken wait fails within 30s rather than holding its lane
	// until the binary's deadline.
	waitCeiling = 30 * time.Second

	// maxPollInterval caps the backoff between checks: short enough that a
	// condition that holds is seen promptly, long enough that a wait lasting
	// minutes does not spin a core.
	maxPollInterval = 20 * time.Millisecond
)

// Until polls cond until it holds. what names the condition, so a hang fails
// as "still waiting for <what>".
func Until(t testing.TB, what string, cond func() bool) {
	t.Helper()
	UntilSeen(t, what, func() (bool, string) { return cond(), "" })
}

// UntilSeen polls check until it reports done. seen describes the state check
// observed, and a failure reports the last one, so a hang reads as "still
// waiting for <what> (last seen: <seen>)".
func UntilSeen(t testing.TB, what string, check func() (done bool, seen string)) {
	t.Helper()
	start := time.Now()
	giveUp := giveUpAt(t, start)
	interval := time.Millisecond
	for {
		done, seen := check()
		if done {
			return
		}
		if !time.Now().Before(giveUp) {
			if seen != "" {
				seen = " (last seen: " + seen + ")"
			}
			t.Fatalf("still waiting for %s after %s%s", what, time.Since(start).Round(time.Millisecond), seen)
		}
		time.Sleep(interval)
		interval = min(2*interval, maxPollInterval)
	}
}

// Budget reports how long a hang guard may wait before it must fail: waitCeiling,
// or less when the test binary's own deadline is nearer, deadlineMargin short of
// it. A guard that waits exactly this long and then fails reports its own
// condition, where a guard that waits past the deadline loses to the goroutine
// dump the binary prints as it dies.
//
// Until and UntilSeen take their give-up point from here, and so does a helper
// that selects on a channel instead of polling. Reading the rule from one place
// is what stops the two kinds of wait from disagreeing about how much time a
// hang guard may spend.
func Budget(t testing.TB) time.Duration {
	return budgetAt(t, time.Now())
}

// BudgetOrCeiling is Budget for a helper whose callers include synctest
// bubbles. Testing forbids reading the binary deadline inside a bubble, and
// that refusal is the only panic this recovers; a bubble gets the ceiling,
// which costs no wall time there because the fake clock advances to the timer
// once every goroutine in the bubble blocks.
func BudgetOrCeiling(t testing.TB) (budget time.Duration) {
	budget = waitCeiling
	defer func() {
		if p := recover(); p != nil && p != "testing: t.Deadline called inside synctest bubble" {
			panic(p)
		}
	}()
	return Budget(t)
}

// budgetAt is Budget as of now, so a caller that anchored its wait to an earlier
// instant measures the remaining deadline from that instant.
func budgetAt(t testing.TB, now time.Time) time.Duration {
	budget := waitCeiling
	if withDeadline, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, ok := withDeadline.Deadline(); ok {
			remaining := deadline.Sub(now)
			budget = min(budget, remaining-min(deadlineMargin, remaining/2))
		}
	}
	return budget
}

// giveUpAt is when a wait starting at now stops polling: Budget after now.
func giveUpAt(t testing.TB, now time.Time) time.Time {
	return now.Add(budgetAt(t, now))
}
