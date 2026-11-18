package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
)

// The incomplete-close warning must still dispatch its configured Notification
// hook when the event channel is full.
//
// The warning used to go out through emit, which fires the hook independently of
// whether the channel accepted the event. Routing it through the bounded close
// publisher made the notification a CONSEQUENCE of a free slot: the publisher
// retains an undelivered warning and fires the hook only when the send lands,
// and the close-time retry deliberately never redispatches. A full buffer then
// silently removed a notification the session always sent -- a regression in
// behavior the old path had, not a limit of the new bound.
//
// The count assertion is the other half: a repair that dispatched on retain AND
// on the retry would duplicate an operator notification, which is its own
// defect. Exactly one dispatch across the first attempt and the retry.
func TestIncompleteCloseWarningDispatchesItsHookDespiteAFullBuffer(t *testing.T) {
	s := newPublicationRouteSession(t)
	s.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	// A hook that appends one line per dispatch to a file: the only observation
	// that cannot agree with a wrong policy by accident.
	dispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf 'hit\n' >> ` + shellquote.Literal(dispatchLog),
		Timeout: 30,
	})
	s.hookRunner = runner

	// The best-effort buffer is FULL, which is the condition that dropped the
	// notification: the event cannot land, and the hook must fire anyway.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.warnIncompleteCloseBeforeDisposal(ctx, "delegate tree", context.DeadlineExceeded)
	// The post-removal retry must not be the thing that delivers it, and must not
	// duplicate it if the first attempt did.
	s.retryCloseDrainWarnings(ctx)

	data, readErr := os.ReadFile(dispatchLog)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read hook dispatch log: %v", readErr)
	}
	got := strings.Count(string(data), "hit")
	if got != 1 {
		t.Fatalf("the incomplete-close warning dispatched its Notification hook %d time(s), want exactly 1: "+
			"a full buffer must not drop a notification the session always sent, and the retry must not duplicate it", got)
	}
}
