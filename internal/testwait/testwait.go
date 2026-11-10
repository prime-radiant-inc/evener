// Package testwait waits for a condition in a test that cannot run under
// testing/synctest because it drives a real subprocess, socket or filesystem
// watcher. Anything a synctest bubble can hold belongs in one instead: there
// the fake clock makes the wait instant and exact.
//
// The wait takes no timeout. A per-call timeout is a guess about how slow the
// host is, and every guess re-flakes as the suite grows; the test binary's own
// deadline is the only bound, and the helper gives up just before it so the
// failure names the condition instead of arriving as a goroutine dump.
package testwait

import (
	"testing"
	"time"
)

const (
	// deadlineMargin is how long before the test binary's deadline the wait
	// gives up, so its failure reports before `go test` kills the binary.
	deadlineMargin = 5 * time.Second

	// noDeadlineCeiling bounds a wait in a test run with no deadline
	// (`go test -timeout 0`, or a compiled test binary run without
	// -test.timeout). Nothing else would end such a hang, and a ceiling far
	// above any healthy wait keeps a loaded host from tripping it.
	noDeadlineCeiling = 2 * time.Minute

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

// giveUpAt is when a wait starting at now stops polling: deadlineMargin before
// the test's deadline, or halfway there when less than twice the margin is
// left, and noDeadlineCeiling after now when the test has no deadline.
func giveUpAt(t testing.TB, now time.Time) time.Time {
	if withDeadline, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, ok := withDeadline.Deadline(); ok {
			remaining := deadline.Sub(now)
			return now.Add(remaining - min(deadlineMargin, remaining/2))
		}
	}
	return now.Add(noDeadlineCeiling)
}
