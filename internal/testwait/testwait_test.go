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

// TestGiveUpReportsBeforeTheTestDeadline pins the bound: the wait gives up
// before the test binary's own timeout would kill it, so the failure names
// the condition instead of arriving as a goroutine dump.
func TestGiveUpReportsBeforeTheTestDeadline(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, remaining := range []time.Duration{10 * time.Minute, 30 * time.Second, 100 * time.Millisecond} {
		deadline := now.Add(remaining)
		got := giveUpAt(&recorder{deadline: deadline}, now)
		if !got.Before(deadline) {
			t.Errorf("with %s left, the wait gives up at %s, not before the deadline", remaining, got.Sub(now))
		}
		if !got.After(now) {
			t.Errorf("with %s left, the wait gives up at %s, leaving no time to wait", remaining, got.Sub(now))
		}
	}
}

func TestGiveUpWithoutATestDeadlineUsesTheCeiling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	if got := giveUpAt(noDeadline{}, now); !got.Equal(now.Add(noDeadlineCeiling)) {
		t.Fatalf("with no test deadline the wait gives up after %s, want the %s ceiling", got.Sub(now), noDeadlineCeiling)
	}
}
