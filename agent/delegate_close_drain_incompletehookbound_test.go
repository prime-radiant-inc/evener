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

// The incomplete-close Notification hook must keep its ONE dispatch on a live
// context while every event its dispatch publishes stays on the publication
// cutoff.
//
// The runner publishes HOOK_START BEFORE it invokes the hook command. Binding
// that lifecycle send to the live cascade puts a stalled consumer in charge of
// the whole remaining cascade: the drain waits for the hook, the hook waits for
// HOOK_START, and HOOK_START waits for a slot that never frees -- so the worktree
// removals begin after their budget is gone and collectible lanes stay on disk.
// Binding the hook's own command to the expired cutoff is the opposite error: the
// notification is then declined for exactly the failure it reports.
//
// The fixture is the shape where the two differ observably: an authoritative
// buffer that is FULL (so any unbounded send parks and the hook never runs) and a
// cascade that is LIVE with no deadline at all (so a send bound to it parks
// forever). The cutoff has already expired. Only a dispatch that runs the command
// on the cascade and publishes the lifecycle on the cutoff lets the hook fire.
func TestIncompleteCloseHookRunsOnTheCascadeWhileItsLifecycleTakesTheCutoff(t *testing.T) {
	dispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
	s := &Session{id: "sess_incomplete_bound", events: make(chan events.SessionEvent, 1)}
	s.authoritativeConsumer = true
	s.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf 'hit\n' >> ` + shellquote.Literal(dispatchLog),
		Timeout: 30,
	})
	s.hookRunner = runner
	// The production wiring: the runner's lifecycle publisher is the session's
	// policy, so the route a HOOK_START takes is the one under test.
	s.installRunnerEventCallback(runner)

	// The only slot is taken, and nothing drains it: an unbounded HOOK_START
	// parks here and the hook command is never invoked.
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})

	// The cascade is LIVE, with NO deadline: a send bound to it parks until the
	// test ends, which is after the assertions. t.Context() is cancelled at test
	// end, so nothing before then releases a wrongly-bound send.
	cascade := t.Context()
	// The publication cutoff has ALREADY expired: what a stop that overran its
	// publication half leaves behind.
	expired, cancelExpired := context.WithDeadline(cascade, time.Now().Add(-time.Second))
	// Cancelled HERE, not deferred: the deadline is already in the past, so the
	// cancel releases nothing the assertions depend on.
	cancelExpired()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.emitCloseDrainWarnings(expired, cascade, []events.WarningData{{
			Message: "delegate tree close incomplete",
			Source:  "delegate",
		}}, hookOnAttempt)
	}()

	select {
	case <-done:
	// TRIPWIRE: the dispatch is in-process and both bounds are controlled by this
	// test; this ceiling only fires if a lifecycle send parked, which is the
	// defect it names.
	case <-time.After(10 * time.Second):
		t.Fatal("the incomplete-close dispatch never returned on a full buffer: a send in the hook's " +
			"lifecycle took the live cascade instead of the publication cutoff, so a stalled consumer " +
			"holds the drain and the worktree removals lose their budget")
	}

	// The premise: the cascade really is live, so nothing below is a consequence
	// of it having ended.
	if cascade.Err() != nil {
		t.Fatalf("the cascade context ended during the test (%v): it must stay live, or a dispatch "+
			"declined here would be declined for the wrong reason", cascade.Err())
	}
	if expired.Err() == nil {
		t.Fatal("the publication cutoff was live during the test: an expired cutoff is what makes " +
			"the lifecycle half of this test meaningful")
	}

	data, readErr := os.ReadFile(dispatchLog)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read hook dispatch log: %v", readErr)
	}
	if got := strings.Count(string(data), "hit"); got != 1 {
		t.Fatalf("the incomplete-close hook dispatched %d time(s) with a live cascade and an expired "+
			"publication cutoff, want exactly 1: the hook's own command must run on the cascade, or the "+
			"notification is declined for exactly the failure it reports", got)
	}
}
