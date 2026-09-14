//go:build browserguard

package hub

import (
	"testing"
	"time"
)

// The tail is the only thing the guard's failure hands a reader on CI, so the
// shape it takes is worth pinning: dropping the last line, or counting a
// trailing newline as a line, would quietly cost the one line that matters.
func TestSkillGuardDriverTail(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		contents string
		lines    int
		want     string
		wantKept int
	}{
		{name: "empty log", contents: "", lines: 3, want: "", wantKept: 0},
		{name: "only newlines", contents: "\n\n", lines: 3, want: "", wantKept: 0},
		{name: "shorter than the limit", contents: "a\nb\n", lines: 3, want: "a\nb", wantKept: 2},
		{name: "exactly the limit", contents: "a\nb\nc\n", lines: 3, want: "a\nb\nc", wantKept: 3},
		{name: "longer than the limit keeps the END", contents: "a\nb\nc\nd\n", lines: 3, want: "b\nc\nd", wantKept: 3},
		{name: "no trailing newline", contents: "a\nb", lines: 3, want: "a\nb", wantKept: 2},
		{name: "blank lines inside are kept", contents: "a\n\nc\n", lines: 3, want: "a\n\nc", wantKept: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, kept := skillGuardDriverTail([]byte(testCase.contents), testCase.lines)
			if got != testCase.want {
				t.Errorf("tail = %q, want %q", got, testCase.want)
			}
			if kept != testCase.wantKept {
				t.Errorf("kept = %d, want %d", kept, testCase.wantKept)
			}
		})
	}
}

// The wait exists so the tail reads a finished log. Its three outcomes are
// worth pinning because two of them are silent: a driver that never started
// and one that has already been collected must not cost the failing test a
// pause, and a driver still shutting down must not be waited on forever.
func TestSkillGuardAwaitDriver(t *testing.T) {
	t.Run("a driver that has returned is not waited on", func(t *testing.T) {
		finished := make(chan struct{})
		close(finished)
		start := time.Now()
		if !skillGuardAwaitDriver(t, finished, time.Minute) {
			t.Fatal("a closed channel must report the driver as finished")
		}
		if waited := time.Since(start); waited > 5*time.Second {
			t.Fatalf("waited %s on an already-finished driver", waited)
		}
	})

	t.Run("a driver that never started is not waited on", func(t *testing.T) {
		start := time.Now()
		if !skillGuardAwaitDriver(t, nil, time.Minute) {
			t.Fatal("a nil channel must report the driver as finished")
		}
		if waited := time.Since(start); waited > 5*time.Second {
			t.Fatalf("waited %s on a driver that never started", waited)
		}
	})

	t.Run("a driver still running is waited on, but only to the bound", func(t *testing.T) {
		if skillGuardAwaitDriver(t, make(chan struct{}), 10*time.Millisecond) {
			t.Fatal("an open channel must report the driver as still running")
		}
	})

	t.Run("a driver that returns during the wait is picked up", func(t *testing.T) {
		finished := make(chan struct{})
		go func() {
			time.Sleep(5 * time.Millisecond)
			close(finished)
		}()
		if !skillGuardAwaitDriver(t, finished, time.Minute) {
			t.Fatal("a channel closed during the wait must report the driver as finished")
		}
	})
}
