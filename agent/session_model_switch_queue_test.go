package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// gatedListingAdapter wraps fakeAdapter with per-call scripted live-listing
// behavior, so a test can block or fail each enumeration in turn while the
// recorded model requests show what each round actually ran on.
type gatedListingAdapter struct {
	fakeAdapter

	modelsMu  sync.Mutex
	models    []func() ([]registry.Model, error)
	modelsIdx int
}

func (a *gatedListingAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	a.modelsMu.Lock()
	if a.modelsIdx >= len(a.models) {
		a.modelsMu.Unlock()
		return nil, errors.New("gated adapter: no scripted listing")
	}
	// The scripted call runs outside the mutex: a test parks one listing to
	// let another pick enumerate concurrently.
	fn := a.models[a.modelsIdx]
	a.modelsIdx++
	a.modelsMu.Unlock()
	return fn()
}

func (a *gatedListingAdapter) listingCall() int {
	a.modelsMu.Lock()
	defer a.modelsMu.Unlock()
	return a.modelsIdx
}

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

// TestAtRestPickDoesNotSwapUnderATurnThatStartedWhileItValidated pins the
// queue fallback's commit window: the input that owned a mid-turn pick can
// end while the pick's preflight is still fetching, and the fallback then
// validates at rest. A turn that starts during that validation must not have
// the switch swapped under it - the pick queues for that turn's boundary
// instead, and the running turn keeps the model it was admitted with.
func TestAtRestPickDoesNotSwapUnderATurnThatStartedWhileItValidated(t *testing.T) {
	dir := t.TempDir()
	adapter := &gatedListingAdapter{fakeAdapter: fakeAdapter{name: "openai"}}
	releasePickPreflight := make(chan struct{})
	releaseRefetch := make(chan struct{})
	refetchCalled := make(chan struct{}, 1)
	afterTool := make(chan struct{}, 1)
	resumeAfterTool := make(chan struct{})
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	// Armed after construction: NewSession's own startup resolve enumerates
	// the instance once, and that call must fail open on the unscripted
	// adapter rather than consume the pick's scripted gates.
	adapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			<-releasePickPreflight
			return nil, errors.New("pick-time listing unavailable")
		},
		func() ([]registry.Model, error) {
			refetchCalled <- struct{}{}
			<-releaseRefetch
			return nil, errors.New("fallback listing unavailable")
		},
	}
	pickDone := make(chan error, 1)
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			go func() { pickDone <- s.SetModel("gpt-5.4") }()
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "t2_tasks", Name: "task_list", Arguments: []byte(`{}`), Type: "function"})
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "second turn done") },
	}
	if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	// The first input is over. Let the pick's blocked preflight fail open; the
	// fallback re-fetches on listing #2 and blocks there.
	close(releasePickPreflight)
	<-refetchCalled
	// Gate the second input between its tool execution and its second round,
	// so the fallback's decision has always landed before that round's
	// request is built. Installed here, after the first input is done: the
	// checkpoint fires for the communicate tool too, and the first input's
	// own communicate must not park at a gate nothing opens for it.
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.execToolCheckpoint = func(name string) {
			if name == "after_execute" {
				afterTool <- struct{}{}
				<-resumeAfterTool
			}
		}
	})
	// A second input starts while the fallback's re-fetch is blocked...
	secondDone := make(chan error, 1)
	go func() {
		_, err := s.ProcessInput(context.Background(), "second message", nil)
		secondDone <- err
	}()
	// ...and gets through its tool round, parked at the gate.
	<-afterTool
	// Now the fallback resumes: whatever it decides has landed before the
	// second input's next round builds its request.
	close(releaseRefetch)
	if err := <-pickDone; err != nil {
		t.Fatalf("mid-turn pick: %v", err)
	}
	close(resumeAfterTool)
	if err := <-secondDone; err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("model requests = %d, want the first turn and the second input's two rounds (3)", len(requests))
	}
	if requests[2].Model != "gpt-5.2" {
		t.Fatalf("the second input's second round ran on model %q; a turn that started while an at-rest pick was still validating must keep the model it was admitted with (%q)", requests[2].Model, "gpt-5.2")
	}
}

// TestSupersededPendingApplicationDoesNotOverwriteANewerPick pins
// last-pick-wins across the pending application's fetch: a queued switch whose
// carried listing failed re-fetches at the idle tail, and while that fetch is
// in flight a newer at-rest pick can commit. The older application must drop
// when it resumes instead of overwriting the newer choice.
func TestSupersededPendingApplicationDoesNotOverwriteANewerPick(t *testing.T) {
	dir := t.TempDir()
	adapter := &gatedListingAdapter{fakeAdapter: fakeAdapter{name: "openai"}}
	releaseTailFetch := make(chan struct{})
	tailFetchCalled := make(chan struct{}, 1)
	newerPicked := make(chan struct{}, 1)
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	// Armed after construction: NewSession's startup resolve must not consume
	// the queued pick's scripted listing calls.
	adapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) { return nil, errors.New("pick-time listing unavailable") },
		func() ([]registry.Model, error) {
			tailFetchCalled <- struct{}{}
			<-releaseTailFetch
			return nil, errors.New("tail listing unavailable")
		},
		func() ([]registry.Model, error) { return nil, errors.New("newer pick listing unavailable") },
	}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The queued pick: its preflight fails open, so the pending switch
			// carries no listing and the tail must re-fetch.
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("mid-turn SetModel: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
	}

	// The newer pick arrives from its own goroutine while the main goroutine
	// is blocked inside the idle tail's listing fetch; the same goroutine
	// releases the fetch once the newer pick has committed, so the older
	// application then resumes against the newer choice.
	go func() {
		<-tailFetchCalled
		if err := s.SetModel("gpt-4.1-mini"); err != nil {
			t.Errorf("newer SetModel: %v", err)
		}
		newerPicked <- struct{}{}
		close(releaseTailFetch)
	}()

	if _, err := s.ProcessInput(context.Background(), "the only turn", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	<-newerPicked
	// The tail's fetch has failed open by now (ProcessInput returned), so the
	// older application has had its say: the newer pick must have won.
	if got := s.Profile().Model(); got != "gpt-4.1-mini" {
		t.Fatalf("profile model = %q after the queued pick resumed past a newer at-rest pick; the newer choice must win (%q)", got, "gpt-4.1-mini")
	}
}
