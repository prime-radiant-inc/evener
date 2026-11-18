package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
)

// countNotificationHookRuns installs a real Notification hook on s and returns
// the number of dispatches that reached the runner's lifecycle callback.
//
// The callback is the PRODUCTION wiring (publishHookLifecycle), not a counter
// bolted onto the runner: a test that swapped the runner without it would pass
// on a drain that never dispatched a hook at all, which is the shape this pair
// of tests exists to rule out. HOOK_START is the fact "the dispatch began".
func countNotificationHookRuns(t *testing.T, s *Session) func() int {
	t.Helper()
	var starts atomic.Int32
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s\n' '{}'`,
		Timeout: 30,
	})
	runner.SetEventCallbackContext(func(ctx context.Context, kind events.EventKind, data events.EventData) {
		if kind == events.EventHookStart {
			starts.Add(1)
		}
		s.publishHookLifecycle(ctx, kind, data)
	})
	s.hookRunner = runner
	// Room for a warning plus one hook's lifecycle events, so a send never parks
	// for lack of a slot: what this pair asserts is whether a dispatch HAPPENED,
	// not where it parked.
	s.events = make(chan events.SessionEvent, 8)
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })
	return func() int { return int(starts.Load()) }
}

// A close warning that reaches the stream with its Notification hook DECLINED is
// not delivered: the hook is the configured report of the warning, and a
// consumer that resumes inside the cascade still has no sign the dispatch was
// skipped.
//
// The cutoff is what declines it. A stop that times out after the publication
// cutoff while the cascade still has time left publishes the incomplete-close
// warning successfully -- the consumer is responsive, there is a slot -- and then
// the dispatch arms a hook on a context that has already expired, so the hook
// returns without running. The warning is already marked delivered, so the retry
// that runs after the worktree removals never looks at it again.
//
// The fixture pins both halves: the hook does NOT run at the first publication,
// and it DOES run on the retry, exactly once.
func TestDeclinedCloseDrainHookIsDispatchedAfterCleanup(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	hookStarts := countNotificationHookRuns(t, s)

	// The stop's own publication cutoff, already spent: this is what a stop that
	// ran out of time leaves behind while the cascade still has budget.
	stopCtx, cancelStop := context.WithCancel(t.Context())
	closeJoined := closeDrainBound(stopCtx)
	cancelStop()

	s.emitCloseDrainWarnings(closeJoined, []events.WarningData{
		{Message: "delegate tree close incomplete: stop timed out"},
	})

	// The warning got out -- that is what makes the loss silent rather than loud.
	if got := len(s.events); got != 1 {
		t.Fatalf("the stream holds %d event(s), want 1: the warning must reach a responsive consumer even "+
			"when the stop's own cutoff has expired", got)
	}
	if got := hookStarts(); got != 0 {
		t.Fatalf("the Notification hook started %d time(s) on the spent cutoff, want 0: this fixture cannot "+
			"show a deferred dispatch if the first attempt already dispatched", got)
	}

	// The post-cleanup retry, on the budget the removals did not need.
	retryCtx, cancelRetry := context.WithCancel(t.Context())
	defer cancelRetry()
	s.retryCloseDrainWarnings(retryCtx)

	if got := hookStarts(); got != 1 {
		t.Fatalf("the Notification hook started %d time(s) across the close, want exactly 1: a dispatch the "+
			"cutoff declined is the configured report of a warning that WAS published, and dropping it is how "+
			"a close loses the report it was supposed to make", got)
	}
	// At-most-once: a second retry finds nothing, so the hook never repeats.
	s.retryCloseDrainWarnings(retryCtx)
	if got := hookStarts(); got != 1 {
		t.Fatalf("the Notification hook started %d time(s) after a second retry, want still 1: a deferred "+
			"dispatch must be delivered exactly once", got)
	}
}

// A dispatch that already ran must never run again. Without this, the fix above
// could be "always defer", which would double every close warning's hook.
func TestPublishedCloseDrainHookIsNotDispatchedTwice(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	hookStarts := countNotificationHookRuns(t, s)

	// A LIVE bound: the dispatch runs, so nothing may be deferred.
	live, cancelLive := context.WithCancel(t.Context())
	defer cancelLive()
	s.closeCtxMu.Lock()
	s.closeCtx = live
	s.closeCtxMu.Unlock()
	t.Cleanup(func() {
		s.closeCtxMu.Lock()
		s.closeCtx = nil
		s.closeCtxMu.Unlock()
	})

	s.emitCloseDrainWarnings(closeDrainBound(live), []events.WarningData{
		{Message: "delegate tree close incomplete: stop timed out"},
	})
	if got := hookStarts(); got != 1 {
		t.Fatalf("the live publication started the Notification hook %d time(s), want 1: the fixture never "+
			"reached the dispatch its retry must not repeat", got)
	}

	retryCtx, cancelRetry := context.WithCancel(t.Context())
	defer cancelRetry()
	s.retryCloseDrainWarnings(retryCtx)

	if got := hookStarts(); got != 1 {
		t.Fatalf("the Notification hook started %d time(s), want exactly 1: a dispatch that already ran must "+
			"not be repeated by the post-cleanup retry", got)
	}
}
