package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// "Needs you" reflects whether the parent's latest turn ended with a request
// for its human partner. A parent that asked while its child worked rests
// idle; stopping the child delivers the stop report as a parent turn, and
// that turn's end reason decides the rest.
func TestParentReadingAChildStopReportRestsByItsOwnEndReason(t *testing.T) {
	t.Parallel()
	for _, reportReason := range []string{"needs_response", "done"} {
		t.Run(reportReason, func(t *testing.T) {
			t.Parallel()
			fixture := newColdDelegateFixture(t, "")
			// The child sits in its model call until the stop cancels it; the
			// parent asks for a reply, then answers the stop report. Any other
			// call also waits to be cancelled, so an unexpected extra turn
			// fails the test instead of passing with a plain "done".
			// reports needs no lock: the parent's report turn appends to it
			// inside ProcessInputKind, which returns before the test reads it.
			var reports []string
			adapter := newSessionScriptAdapter("openai")
			adapter.otherwise = awaitCancelStep
			fixture.client.Register(adapter.
				script(fixture.meta.ID,
					answerStep(endReasonResponse("which?", "needs_response")),
					func(_ context.Context, req llm.Request) (llm.Response, error) {
						reports = append(reports, req.Messages[len(req.Messages)-1].Text())
						return endReasonResponse("noted", reportReason), nil
					}))
			fake := agenttest.NewFakeClock()
			root := restoreSupervisionRoot(t, fixture, fake)
			evs, mu, done := collectEvents(root)
			// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if started := (delegateRuntime{owner: root}).send(ctx, fixture.delegateID, "work until stopped", 0); started.result.Err != nil {
				t.Fatalf("start the child: %v", started.result.Err)
			}
			// TRIPWIRE: the child reaches its model call in milliseconds.
			waitForCondition(t, 30*time.Second, "the child to be working", root.hasWorkingSubagent)
			if _, err := root.ProcessInput(ctx, "hello", nil); err != nil {
				t.Fatal(err)
			}
			fake.Advance(2 * needsResponseQuietPeriodDefault)
			fake.Drain()
			if got := root.State(); got != SessionIdle {
				t.Fatalf("state while the child works = %q, want idle", got)
			}

			if got, err := root.StopDelegateRun(fixture.delegateID); err != nil || got != appwire.DelegateStopStopping {
				t.Fatalf("StopDelegateRun = %q, %v; want stopping", got, err)
			}
			waitForStableSupervisionRun(t, root, fixture.childID)
			if !root.pendingRootDelegateAttention() {
				t.Fatal("the stopped child's report is not waiting for the parent")
			}
			if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
				t.Fatalf("the parent's wake: %v", err)
			}
			if len(reports) != 1 || !strings.Contains(reports[0], delegateUserStopMessage) {
				t.Fatalf("the parent's report turn read %q, want the child's stop report", reports)
			}

			if reportReason == "needs_response" {
				requireRestsAwaitingAfterFreshQuietPeriod(t, root, fake, evs, mu, done)
				return
			}
			fake.Advance(2 * needsResponseQuietPeriodDefault)
			fake.Drain()
			if got := root.State(); got != SessionIdle {
				t.Fatalf("state after the quiet period = %q, want idle", got)
			}
			if got := settledStatesAfterClose(root, evs, mu, done); len(got) != 0 {
				t.Fatalf("status settled events = %v, want none", got)
			}
		})
	}
}
