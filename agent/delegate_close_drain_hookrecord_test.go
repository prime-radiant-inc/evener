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
// at a time and bypassing the publication cutoff even when the lifecycle send
// itself honoured it.
//
// The assertion is the DISPATCH COUNT in a file, not a message. The premise --
// that the write really failed and really reached the stream -- is asserted
// first, so this cannot pass by recording nothing.
func TestBoundedHookCompletionRecordingFailureDoesNotRedispatchTheHook(t *testing.T) {
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

	// A real poisoned writer: the append fails partway and its rollback fails
	// too, so every later record returns ErrWriterPoisoned.
	fs := attachEnvironmentFailureFS(t, s)
	fs.mu.Lock()
	fs.failure = errors.New("injected transcript write failure")
	fs.rollbackFailure = errors.New("injected transcript rollback failure")
	fs.mu.Unlock()

	s.emitHookCompletedBounded(withPublicationBound(t.Context(), t.Context()), events.HookEndData{
		Event: "Notification", HookType: "command", Matcher: "*", ExitCode: 0,
	})

	data, readErr := os.ReadFile(dispatchLog)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read hook dispatch log: %v", readErr)
	}
	if got := strings.Count(string(data), "hit"); got != 0 {
		t.Fatalf("a bounded hook completion's recording failure dispatched the Notification hook %d time(s), "+
			"want 0: the failure is an EventWarning, so the ordinary warning path re-enters the "+
			"dispatcher whose own send just failed", got)
	}
	if !warningOnStream(s) {
		t.Fatal("the failed recording produced no warning event: the failure did not reach the stream at " +
			"all, so this test proves nothing about which route it took")
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
