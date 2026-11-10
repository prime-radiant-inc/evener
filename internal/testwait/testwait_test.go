package testwait

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// recorder stands in for the test the helper reports to, so a wait that never
// succeeds can fail without failing this test. Fatalf ends the calling
// goroutine the way testing.T's does.
type recorder struct {
	testing.TB
	deadline time.Time
	failure  string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failure = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Deadline() (time.Time, bool) { return r.deadline, true }

// noDeadline is a test that carries no deadline, as under `go test -timeout 0`.
type noDeadline struct{ testing.TB }

// runToEnd runs wait on its own goroutine, since a failing wait ends it.
func runToEnd(wait func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait()
	}()
	<-done
}

func TestUntilReturnsOnceTheConditionHolds(t *testing.T) {
	t.Parallel()
	calls := 0
	Until(t, "the third check", func() bool {
		calls++
		return calls == 3
	})
	if calls != 3 {
		t.Fatalf("condition checked %d times, want 3: the wait must stop the moment it holds", calls)
	}
}

func TestUntilFailsNamingWhatItWaitedFor(t *testing.T) {
	t.Parallel()
	const what = "sentinel-condition-7f3a"
	r := &recorder{deadline: time.Now().Add(100 * time.Millisecond)}
	runToEnd(func() { Until(r, what, func() bool { return false }) })
	if r.failure == "" {
		t.Fatal("a condition that never holds did not fail the test")
	}
	if !strings.Contains(r.failure, what) {
		t.Fatalf("failure %q does not carry the caller's description %q", r.failure, what)
	}
}

func TestUntilSeenFailsReportingTheLastObservedState(t *testing.T) {
	t.Parallel()
	const what, seen = "sentinel-condition-91c2", "sentinel-state-44be"
	r := &recorder{deadline: time.Now().Add(100 * time.Millisecond)}
	runToEnd(func() { UntilSeen(r, what, func() (bool, string) { return false, seen }) })
	if r.failure == "" {
		t.Fatal("a condition that never holds did not fail the test")
	}
	for _, want := range []string{what, seen} {
		if !strings.Contains(r.failure, want) {
			t.Fatalf("failure %q does not carry %q", r.failure, want)
		}
	}
}

// TestGiveUpStopsAtTheCeiling pins the ordinary bound: with plenty of test
// time left, or none set, a wait that never succeeds fails after waitCeiling,
// not at the far-off binary deadline.
func TestGiveUpStopsAtTheCeiling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for name, tb := range map[string]testing.TB{
		"a distant deadline": &recorder{deadline: now.Add(10 * time.Minute)},
		"no deadline":        noDeadline{},
	} {
		if got := giveUpAt(tb, now); !got.Equal(now.Add(waitCeiling)) {
			t.Errorf("with %s the wait gives up after %s, want the %s ceiling", name, got.Sub(now), waitCeiling)
		}
	}
}

// TestGiveUpReportsBeforeTheTestDeadline pins the near-deadline bound: when
// the binary's own timeout comes before the ceiling, the wait gives up first,
// so the failure names the condition instead of arriving as a goroutine dump.
func TestGiveUpReportsBeforeTheTestDeadline(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, remaining := range []time.Duration{waitCeiling, 8 * time.Second, 100 * time.Millisecond} {
		deadline := now.Add(remaining)
		got := giveUpAt(&recorder{deadline: deadline}, now)
		if !got.Before(deadline) {
			t.Errorf("with %s left, the wait gives up at %s, not before the deadline", remaining, got.Sub(now))
		}
		if remaining >= 2*deadlineMargin && got.After(deadline.Add(-deadlineMargin)) {
			t.Errorf("with %s left, the wait gives up %s before the deadline, inside the %s margin",
				remaining, deadline.Sub(got), deadlineMargin)
		}
		if !got.After(now) {
			t.Errorf("with %s left, the wait gives up at %s, leaving no time to wait", remaining, got.Sub(now))
		}
	}
}
