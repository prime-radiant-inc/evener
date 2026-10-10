package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// childStopAdapter scripts a parent and its stable child on one provider
// without holding a lock across a model call, so the child can sit in its
// call until it is stopped while the parent keeps taking turns. The child
// waits for its context to end; the parent asks for a reply on its first turn
// and answers a delegate report with reportReason.
type childStopAdapter struct {
	childSessionID string
	reportReason   string

	mu      sync.Mutex
	reports []string
}

func (a *childStopAdapter) Name() string { return "openai" }

func (a *childStopAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *childStopAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.SessionID == a.childSessionID {
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	resp := endReasonResponse("which?", "needs_response")
	if last := req.Messages[len(req.Messages)-1].Text(); strings.Contains(last, "<delegate-notification") {
		a.mu.Lock()
		a.reports = append(a.reports, last)
		a.mu.Unlock()
		resp = endReasonResponse("noted", a.reportReason)
	}
	resp.Provider = a.Name()
	resp.Model = req.Model
	return resp, nil
}

func (a *childStopAdapter) reportsRead() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.reports...)
}

// "Needs you" reflects whether the parent's latest turn ended with a request
// for its human partner. A parent that asked while its child worked rests
// idle; stopping the child delivers the stop report as a parent turn, and
// that turn's end reason decides the rest.
func TestParentReadingAChildStopReportRestsByItsOwnEndReason(t *testing.T) {
	t.Parallel()
	for _, reportReason := range []string{"needs_response", "done"} {
		t.Run(reportReason, func(t *testing.T) {
			t.Parallel()
			fixture := newColdStableDelegateFixture(t, "")
			adapter := &childStopAdapter{childSessionID: fixture.childID, reportReason: reportReason}
			fixture.client.Register(adapter)
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
			if reports := adapter.reportsRead(); len(reports) != 1 || !strings.Contains(reports[0], delegateUserStopMessage) {
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
