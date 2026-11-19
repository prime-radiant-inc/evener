package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
)

// Recording a close-dispatched hook's completion must not be able to dispatch
// that same hook again.
//
// The completion is recorded because a Notification hook just ran and a
// transcript line must show it. If that write fails, the failure is an
// EventWarning -- and the ordinary warning path fires the configured Notification
// hook. So a writer that keeps refusing would dispatch the hook again on the very
// send that failed, once per completion, spending the close budget one dispatch
// at a time. The repair routes the failure through the diagnostic sink, which
// publishes without dispatching.
//
// Both writer failures a close can meet are covered, because they reach different
// branches of the writer: a Sync failure leaves a whole line that stays a record
// and is retained as debt, while a PARTIAL write poisons the writer so every
// later append is refused. A repair that only dulled the warning on one of them
// would leave the dispatch loop reachable through the other.
//
// The assertion is the DISPATCH COUNT in a file, not a message. The premise --
// that the write really failed and really reached the stream -- is asserted
// first, so this cannot pass by recording nothing.
func TestBoundedHookCompletionRecordingFailureDoesNotRedispatchTheHook(t *testing.T) {
	tests := []struct {
		name string
		// arm installs the writer failure and reports whether it is the partial
		// write that poisons the writer.
		arm func(t *testing.T, s *Session) (poisoned bool)
	}{
		{
			name: "sync failure retains a whole line",
			arm: func(t *testing.T, s *Session) bool {
				t.Helper()
				fs := attachEnvironmentFailureFS(t, s)
				fs.mu.Lock()
				fs.failure = errors.New("injected transcript write failure")
				fs.rollbackFailure = errors.New("injected transcript rollback failure")
				fs.mu.Unlock()
				return false
			},
		},
		{
			name: "partial write poisons the writer",
			arm: func(t *testing.T, s *Session) bool {
				t.Helper()
				attachEnvironmentPoisoningWrite(t, s,
					errors.New("injected partial transcript write"),
					errors.New("injected transcript rollback failure"))
				return true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
			s := newQueuePersistTestSession(t, t.TempDir())
			t.Cleanup(func() { s.Close() })

			runner := hooks.NewRunner(nil, "test-model")
			runner.Add(plugin.HookNotification, plugin.RegisteredHook{
				Matcher: "*",
				Type:    "command",
				Command: `printf 'hit\n' >> ` + shellquote.Literal(dispatchLog),
				Timeout: 30,
			})
			s.hookRunner = runner

			wantPoisoned := test.arm(t, s)

			s.emitHookCompletedBounded(withPublicationBound(t.Context(), t.Context()), events.HookEndData{
				Event: "Notification", HookType: "command", Matcher: "*", ExitCode: 0,
			})

			// The premise: the injected failure left the writer in the state this
			// case is about, so the warning below came from the branch under test.
			if got := s.attachedTranscript().Poisoned(); got != wantPoisoned {
				t.Fatalf("the injected failure left the transcript writer poisoned=%t, want %t: the case "+
					"did not reach the branch it names", got, wantPoisoned)
			}

			data, readErr := os.ReadFile(dispatchLog)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatalf("read hook dispatch log: %v", readErr)
			}
			if got := strings.Count(string(data), "hit"); got != 0 {
				t.Fatalf("a bounded hook completion's recording failure dispatched the Notification hook "+
					"%d time(s), want 0: the failure is an EventWarning, and the ordinary warning path "+
					"re-enters the dispatcher whose own send just failed", got)
			}
			if !warningOnStream(s) {
				t.Fatal("the failed recording produced no warning event: the failure did not reach the " +
					"stream at all, so this test proves nothing about which route it took")
			}
		})
	}
}

// warningOnStream reports whether a warning event reached the session's channel,
// which is the premise this test needs before it can conclude anything about the
// route that warning took.
func warningOnStream(s *Session) bool {
	for len(s.events) > 0 {
		if ev := <-s.events; ev.Kind == events.EventWarning {
			return true
		}
	}
	return false
}
