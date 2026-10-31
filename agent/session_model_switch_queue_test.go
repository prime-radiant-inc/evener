package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The queue's application point is the input loop, not the processing
// boundary: a pick that lands after a turn's boundary but before the next
// turn starts must still apply to that next turn, and a queued switch must
// not apply until the turn it interrupts has finished settling its own
// deferred work (attention finalizers included). These tests pin the two
// orderings and the queue's enumeration budget.

// TestModelPickBetweenTurnsAppliesToTheNextTurn pins the window between a
// turn's committed boundary and the next drained turn: the boundary has
// already applied whatever was queued when it fired, so a pick landing after
// it (here, from the boundary hook the serve loop's post-communicate window
// sits in) queues again and must apply to the very next turn, not the one
// after it.
func TestModelPickBetweenTurnsAppliesToTheNextTurn(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "first turn done") },
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	}}
	dir := t.TempDir()
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-pick-between-turns", "the message queued behind the first turn")

	var pickOnce = make(chan struct{}, 1)
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterCommunicateBoundary = func(sess *Session) {
			select {
			case pickOnce <- struct{}{}:
				if err := sess.SetModel("gpt-5.4"); err != nil {
					t.Errorf("SetModel between turns: %v", err)
				}
			default:
			}
		}
	})

	if _, err := s.ProcessInput(context.Background(), "the first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
	}
	if requests[1].Model != "gpt-5.4" {
		t.Fatalf("the queued message ran on model %q; a pick made between turns must apply to the next turn (%q)", requests[1].Model, "gpt-5.4")
	}
}

// pickFailingStreamAdapter queues a model pick at the first stream attempt,
// then fails the stream permanently the way a dead credential does.
type pickFailingStreamAdapter struct {
	pick func()
}

func (a *pickFailingStreamAdapter) Name() string { return "openai" }

func (a *pickFailingStreamAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, llm.ErrStreamUnsupported
}

func (a *pickFailingStreamAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	if pick := a.pick; pick != nil {
		a.pick = nil
		pick()
	}
	return nil, llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil)
}

func (a *pickFailingStreamAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	return nil, errors.New("no listing in this test")
}

// TestQueuedModelSwitchAppliesAfterTheFailedTurnSettlesAttention pins the
// failure ordering: a switch queued during a turn that then fails
// permanently must apply AFTER that turn's deferred attention settlement. A
// committed switch re-arms attention a permanent failure deferred
// (resumeRootAttentionAfterModelSwitch); applied before the settlement
// instead, the finalizer's pause lands last and the session sits paused
// despite the fresh, healthy model.
func TestQueuedModelSwitchAppliesAfterTheFailedTurnSettlesAttention(t *testing.T) {
	dir := t.TempDir()
	adapter := &pickFailingStreamAdapter{}
	c := llm.NewClient()
	c.Register(adapter)
	policy := llm.RetryPolicy{MaxRetries: 0}
	s := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	}))
	adapter.pick = func() {
		if err := s.SetModel("gpt-5.4"); err != nil {
			t.Errorf("mid-turn SetModel: %v", err)
		}
	}
	serveSession(t, s)

	armOneRootAttention(t, s, "dlg_pick401", "delegate:dlg_pick401/delivery/1")
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the 401 notification turn unexpectedly succeeded")
	}

	wake, retryActive, pending := attentionRailState(s)
	if !wake || retryActive || pending != 1 {
		t.Fatalf("after the queued switch to a healthy model: wake=%t retry=%t pending=%d, want the deferred attention re-armed and the permanent failure's paced retry left off (true, false, 1)", wake, retryActive, pending)
	}
}

// TestQueuedMidTurnSwitchEnumeratesTheLiveListingOnce pins the queue's
// enumeration budget: the pick-time membership preflight is the one live
// model listing a queued switch pays for. The application at the input loop
// must reuse that listing rather than enumerating the provider again.
func TestQueuedMidTurnSwitchEnumeratesTheLiveListingOnce(t *testing.T) {
	dir := t.TempDir()
	listModels := attemptRecordingLiveModels("openai", "gpt-5.2", "gpt-5.4")
	var listings atomic.Int64
	adapter := &fakeAdapter{
		name: "openai",
		liveModels: func(ctx context.Context) ([]registry.Model, error) {
			listings.Add(1)
			return listModels(ctx)
		},
	}
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("mid-turn SetModel: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
	}

	baseline := listings.Load()
	if _, err := s.ProcessInput(context.Background(), "run one turn and pick mid-turn", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := listings.Load() - baseline; got != 1 {
		t.Fatalf("live model enumerations for the queued switch = %d, want exactly one (the pick-time preflight; the application must reuse it)", got)
	}
}
