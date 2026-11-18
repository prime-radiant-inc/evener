package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
)

// The incomplete-close Notification hook must get one dispatch while the CASCADE
// is still live, even when the publication cutoff has already expired.
//
// The warning's send is capped at the cascade's shared publication cutoff, and
// a delegate stop that times out after that cutoff but before the cascade
// deadline is exactly the case the warning reports. Dispatching the hook on the
// expired cutoff skipped it there -- so the notification the session always sent
// became a consequence of a deadline that had already passed. The previous close
// path dispatched it while the cascade was still live.
//
// The assertion is the DISPATCH COUNT in a file. The first attempt is the only
// dispatch: the post-removal retry publishes the channel event only, so a second
// dispatch would be a duplicate operator notification.
func TestIncompleteCloseHookDispatchesOnceWhenThePublicationCutoffExpired(t *testing.T) {
	dispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
	s := &Session{id: "sess_incomplete", events: make(chan events.SessionEvent, 8)}
	s.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf 'hit\n' >> ` + shellquote.Literal(dispatchLog),
		Timeout: 30,
	})
	s.hookRunner = runner

	// A cascade that is LIVE, with a publication cutoff that has ALREADY expired:
	// the state a stop that overran its publication half leaves behind.
	cascade := t.Context()
	expired, cancelExpired := context.WithDeadline(cascade, time.Now().Add(-time.Second))
	// Cancelled HERE, not deferred: canceling a child context does not cancel its
	// parent, but a deferred cancel would run before the assertions if a later
	// helper ever derived from this one. The deadline is already in the past, so
	// the cancel releases nothing the assertions depend on.
	cancelExpired()

	s.emitCloseDrainWarnings(expired, cascade, []events.WarningData{{
		Message: "delegate tree close incomplete",
		Source:  "delegate",
	}}, hookOnAttempt)
	// The post-removal retry must not dispatch a second time.
	s.retryCloseDrainWarnings(cascade)

	// The premise: the cascade really is still live, so the dispatch above was not
	// declined for an unrelated reason.
	if cascade.Err() != nil {
		t.Fatalf("the cascade context ended during the test (%v): it must stay live, or a dispatch "+
			"declined here would be declined for the wrong reason", cascade.Err())
	}

	data, readErr := os.ReadFile(dispatchLog)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read hook dispatch log: %v", readErr)
	}
	if got := strings.Count(string(data), "hit"); got != 1 {
		t.Fatalf("the incomplete-close hook dispatched %d time(s) with an expired publication cutoff "+
			"and a live cascade, want exactly 1: a notification the session always sent must not be "+
			"skipped because the send's deadline passed, and the retry must not duplicate it", got)
	}
}
