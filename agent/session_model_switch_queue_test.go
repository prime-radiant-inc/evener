package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
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
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "first turn done") }, func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") })
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
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("mid-turn SetModel: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
	})

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
	adapter := &gatedListingAdapter{name: "openai"}
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
	pickListingCalled := make(chan struct{}, 1)
	adapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			pickListingCalled <- struct{}{}
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
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			go func() { pickDone <- s.SetModel("gpt-5.4") }()
			// Do not end the first turn until the pick has reached its
			// preflight listing: the pick is then guaranteed to have taken
			// the queue path while this turn was still running, not the
			// at-rest path if its goroutine happened to start late.
			<-pickListingCalled
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "t2_tasks", Name: "task_list", Arguments: []byte(`{}`), Type: "function"})
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "second turn done") },
	})
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
	adapter := &gatedListingAdapter{name: "openai"}
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
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The queued pick: its preflight fails open, so the pending switch
			// carries no listing and the tail must re-fetch.
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("mid-turn SetModel: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
	})

	// The newer pick arrives from its own goroutine while the main goroutine
	// is blocked inside the idle tail's listing fetch; the same goroutine
	// releases the fetch once the newer pick has committed, so the older
	// application then resumes against the newer choice.
	go func() {
		<-tailFetchCalled
		// The queued pick's application is parked inside its boundary listing
		// fetch - popped from the queue, not yet committed, nothing newer
		// committed - so the read sample must keep naming the validating pick
		// itself: the thread/model/set hook's synchronous refresh can run
		// exactly here when a pick races the end of its turn.
		model, instance := s.ReportedModelRef()
		if model != "gpt-5.4" || instance != "openai" {
			t.Errorf("advertised pair while the pick's application validates = %s/%s, want the accepted pick gpt-5.4/openai", instance, model)
		}
		if err := s.SetModel("gpt-4.1-mini"); err != nil {
			t.Errorf("newer SetModel: %v", err)
		}
		// The newer pick has committed and published while the queued pick's
		// application is still parked mid-validation. The read sample must
		// name the committed pick, not the validating pick it superseded:
		// model-set's synchronous refresh runs exactly here, and reporting
		// the parked pick would overwrite the committed choice in thread/read
		// for as long as the superseded application takes to drop.
		model, instance = s.ReportedModelRef()
		if model != "gpt-4.1-mini" || instance != "openai" {
			t.Errorf("advertised pair while the superseded pick validates = %s/%s, want the committed newer pick gpt-4.1-mini/openai", instance, model)
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

// TestConcurrentMidTurnPicksLastAcceptedChoiceWins pins the queue's store
// under concurrent mid-turn picks: a slower preflight from an earlier pick
// must not overwrite the newer pick's queued choice when it finishes late.
func TestConcurrentMidTurnPicksLastAcceptedChoiceWins(t *testing.T) {
	dir := t.TempDir()
	adapter := &gatedListingAdapter{name: "openai"}
	releaseSlowListing := make(chan struct{})
	aListingCalled := make(chan struct{}, 1)
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	adapter.models = []func() ([]registry.Model, error){
		// The earlier pick's preflight: parked while the turn runs.
		func() ([]registry.Model, error) {
			aListingCalled <- struct{}{}
			<-releaseSlowListing
			return nil, errors.New("slow listing unavailable")
		},
		// The newer pick's preflight: fails fast and queues.
		func() ([]registry.Model, error) { return nil, errors.New("fast listing unavailable") },
	}
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			go func() { aDone <- s.SetModel("gpt-5.4") }()
			// The second pick only enters its preflight once the first one
			// has claimed the parked listing, so the two listings route
			// deterministically.
			go func() {
				<-aListingCalled
				bDone <- s.SetModel("gpt-4.1-mini")
			}()
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	})
	var evMu sync.Mutex
	evs := []events.SessionEvent{}
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		evMu.Lock()
		evs = append(evs, ev)
		evMu.Unlock()
	}, func() { close(drained) })
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	queueOneMutation(t, s, "cm-last-accepted", "the queued message")
	// The boundary hook is the rendezvous: it fires after the first turn's
	// boundary and before the loop applies the pending switch, so it can
	// order the two picks' stores and release the parked one deterministically.
	// One-shot: it also fires at the drained turn's own communicate boundary,
	// where nothing is left to wait for.
	hookOnce := make(chan struct{}, 1)
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterCommunicateBoundary = func(*Session) {
			select {
			case hookOnce <- struct{}{}:
				<-bDone
				close(releaseSlowListing)
				if err := <-aDone; err != nil {
					t.Errorf("earlier pick: %v", err)
				}
			default:
			}
		}
	})

	if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
	}
	if requests[1].Model != "gpt-4.1-mini" {
		t.Fatalf("the queued message ran on model %q; the newer pick's accepted choice must survive the earlier pick's late store (%q)", requests[1].Model, "gpt-4.1-mini")
	}
	s.Close()
	<-drained
	// The earlier pick's preflight finished after the newer pick queued and
	// announced, so its late arrival must not move the visible choice back to
	// itself: the queue selects the newer pick, and the announcement the
	// composer last saw must still name it.
	evMu.Lock()
	var lastAnnounce events.NextModelChangedData
	for _, ev := range evs {
		if d, ok := ev.Data.(events.NextModelChangedData); ok {
			lastAnnounce = d
		}
	}
	evMu.Unlock()
	if lastAnnounce.Model != "gpt-4.1-mini" {
		t.Fatalf("the last acceptance announcement named %q; the earlier pick's late arrival must not announce itself over the newer pick the queue selects (%q)", lastAnnounce.Model, "gpt-4.1-mini")
	}
}

// TestLateOlderPickAppliesWhenTheNewerPickFailsAtTheBoundary pins the queue's
// ordering under the interleaving roborev found on #6683: pick A (older) has a
// slow preflight, pick B (newer) stores first, then A's preflight finishes. An
// implementation that lets the newer store stop the older pick from queueing
// reproduces #6621 exactly - if B then fails the boundary's deferred history
// check, neither pick applies.
//
// The queue must instead keep both and let the pop choose. B's target cannot
// carry the content the turn adds before the boundary, so B is rejected there
// and A, still queued, must be what the next turn runs on.
func TestLateOlderPickAppliesWhenTheNewerPickFailsAtTheBoundary(t *testing.T) {
	dir := t.TempDir()
	openaiAdapter := &gatedListingAdapter{name: "openai"}
	glmAdapter := &gatedListingAdapter{name: "glm"}
	s := newSession(t,
		withAdapter(openaiAdapter),
		withAdapter(glmAdapter),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
	// B's target speaks the Chat protocol (unrepresentable for a non-user
	// document); A's target is OpenAI Responses and can carry it. That
	// asymmetry is what makes B fail at the boundary while A survives.
	doc := documentTurn()
	doc.Message.Role = llm.RoleAssistant
	if len(unrepresentableHistoryKinds([]schema.Turn{doc}, registry.ProtocolOpenAIChat)) == 0 {
		t.Fatal("pre-condition: a non-user document must be unrepresentable for B's target")
	}
	if got := len(unrepresentableHistoryKinds([]schema.Turn{doc}, NewOpenAIProfile("gpt-5.4").Protocol())); got != 0 {
		t.Fatalf("pre-condition: A's target must carry the document, got %d offending kinds", got)
	}

	releaseA := make(chan struct{})
	aListingCalled := make(chan struct{}, 1)
	openaiAdapter.models = []func() ([]registry.Model, error){
		// A's preflight: parked until B has stored, so A arrives last.
		func() ([]registry.Model, error) {
			aListingCalled <- struct{}{}
			<-releaseA
			return []registry.Model{{ID: "gpt-5.2"}, {ID: "gpt-5.4"}}, nil
		},
	}
	glmAdapter.models = []func() ([]registry.Model, error){
		// B's preflight: resolves and queues immediately.
		func() ([]registry.Model, error) { return []registry.Model{{ID: "glm-test"}}, nil },
	}

	aDone, bDone := make(chan error, 1), make(chan error, 1)
	openaiAdapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			go func() { aDone <- s.SetModel("gpt-5.4") }()
			// B enters its own preflight only once A has claimed its parked
			// listing, so the two route deterministically.
			go func() {
				<-aListingCalled
				bDone <- s.SetModel("glm/glm-test")
			}()
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	})
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-late-older-pick", "the queued message")
	// The boundary hook is the rendezvous: B has stored by now, so releasing
	// A here makes A the late arrival. The content B cannot carry is added
	// before A finishes, so it is settled history when the boundary runs.
	hookOnce := make(chan struct{}, 1)
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterCommunicateBoundary = func(*Session) {
			select {
			case hookOnce <- struct{}{}:
				if err := <-bDone; err != nil {
					t.Errorf("newer pick: %v", err)
				}
				s.appendTurn(schema.TurnAssistant, doc.Message)
				close(releaseA)
				if err := <-aDone; err != nil {
					t.Errorf("older pick: %v", err)
				}
			default:
			}
		}
	})

	if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := openaiAdapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
	}
	if requests[1].Model != "gpt-5.4" {
		t.Fatalf("the queued message ran on model %q; the newer pick fails its deferred history check, so the older still-queued pick must apply (%q)", requests[1].Model, "gpt-5.4")
	}
	if got := s.Profile().Model(); got != "gpt-5.4" {
		t.Fatalf("selected model = %q; the older pick must be selected once the newer one is rejected (%q)", got, "gpt-5.4")
	}
}

// TestBusyRequeueDoesNotCancelAnOlderPickItMightStillFail pins the re-queue's
// acceptance rule: a pick that finds a turn in flight queues for that turn's
// boundary, where the history check runs again against what the turn appended.
// Queuing therefore must not mark the pick accepted, or it cancels an older
// pick that would still have been valid.
func TestBusyRequeueDoesNotCancelAnOlderPickItMightStillFail(t *testing.T) {
	dir := t.TempDir()
	openaiAdapter := &gatedListingAdapter{name: "openai"}
	glmAdapter := &gatedListingAdapter{name: "glm"}
	s := newSession(t,
		withAdapter(openaiAdapter),
		withAdapter(glmAdapter),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
	doc := documentTurn()
	doc.Message.Role = llm.RoleAssistant
	if len(unrepresentableHistoryKinds([]schema.Turn{doc}, registry.ProtocolOpenAIChat)) == 0 {
		t.Fatal("pre-condition: a non-user document must be unrepresentable for B's target")
	}

	releaseB, releaseA := make(chan struct{}), make(chan struct{})
	bListingCalled, aListingCalled := make(chan struct{}, 1), make(chan struct{}, 1)
	glmAdapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			bListingCalled <- struct{}{}
			<-releaseB
			return []registry.Model{{ID: "glm-test"}}, nil
		},
	}
	openaiAdapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			aListingCalled <- struct{}{}
			<-releaseA
			return []registry.Model{{ID: "gpt-5.2"}, {ID: "gpt-5.4"}}, nil
		},
	}

	// Both picks are admitted at rest, before any turn runs, so each takes the
	// at-rest path and reaches the busy re-queue once the turn starts. A is
	// admitted first, so it owns the older issue; B is admitted second and
	// finishes first, which is the interleaving the re-queue rule is about.
	bDone, aDone := make(chan error, 1), make(chan error, 1)
	go func() { aDone <- s.SetModel("gpt-5.4") }()
	<-aListingCalled
	go func() { bDone <- s.SetModel("glm/glm-test") }()
	<-bListingCalled

	openaiAdapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The turn is in flight now: release B, which re-queues, adding
			// history the boundary's check will reject.
			close(releaseB)
			if err := <-bDone; err != nil {
				t.Errorf("newer pick: %v", err)
			}
			s.appendTurn(schema.TurnAssistant, doc.Message)
			// Then release A: it finds a turn in flight and re-queues too.
			close(releaseA)
			if err := <-aDone; err != nil {
				t.Errorf("older pick: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	})
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-busy-requeue", "the queued message")

	if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := openaiAdapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
	}
	if requests[1].Model != "gpt-5.4" {
		t.Fatalf("the queued message ran on model %q; the re-queued newer pick fails its deferred history check, so the older re-queued pick must apply (%q)", requests[1].Model, "gpt-5.4")
	}
	if got := s.Profile().Model(); got != "gpt-5.4" {
		t.Fatalf("selected model = %q; the older pick must be selected once the newer one is rejected (%q)", got, "gpt-5.4")
	}
}

// TestRejectedPickDoesNotCancelAnEarlierQueuedPick pins the watermark's
// acceptance rule: a pick that fails validation (here, an unresolvable ref)
// must not advance it, or the switch queued before it would drop as superseded
// at the boundary.
func TestRejectedPickDoesNotCancelAnEarlierQueuedPick(t *testing.T) {
	dir := t.TempDir()
	listModels := attemptRecordingLiveModels("openai", "gpt-5.2", "gpt-5.4")
	adapter := &fakeAdapter{
		name:       "openai",
		liveModels: func(ctx context.Context) ([]registry.Model, error) { return listModels(ctx) },
	}
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("accepted pick: %v", err)
			}
			if err := s.SetModel("bogus/no-such-model"); err == nil {
				t.Error("the bogus pick was accepted; its validation must fail")
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	})
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-rejected-pick", "the queued message")

	if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
	}
	if requests[1].Model != "gpt-5.4" {
		t.Fatalf("the queued message ran on model %q; a rejected later pick must not cancel the accepted queued switch (%q)", requests[1].Model, "gpt-5.4")
	}
}

// TestSupersededLoopTopApplicationAppliesTheNewerQueuedPick pins the loop's
// re-drain: when the pending application the loop popped is superseded while
// it refetches, the newer pick queued in the meantime must still apply before
// the turn the loop is about to admit.
func TestSupersededLoopTopApplicationAppliesTheNewerQueuedPick(t *testing.T) {
	dir := t.TempDir()
	adapter := &gatedListingAdapter{name: "openai"}
	releaseLoopFetch := make(chan struct{})
	loopFetchCalled := make(chan struct{}, 1)
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	adapter.models = []func() ([]registry.Model, error){
		// The queued pick's preflight fails open, so the loop-top application
		// must refetch.
		func() ([]registry.Model, error) { return nil, errors.New("pick-time listing unavailable") },
		// The loop-top refetch, parked while the newer pick queues.
		func() ([]registry.Model, error) {
			loopFetchCalled <- struct{}{}
			<-releaseLoopFetch
			return nil, errors.New("loop listing unavailable")
		},
		// The newer pick's preflight.
		func() ([]registry.Model, error) { return nil, errors.New("newer pick listing unavailable") },
	}
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("queued pick: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	})
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-loop-superseded", "the queued message")
	// While the loop-top application's refetch is parked, a newer pick queues
	// from its own goroutine and releases the refetch once it has stored.
	go func() {
		<-loopFetchCalled
		if err := s.SetModel("gpt-4.1-mini"); err != nil {
			t.Errorf("newer pick: %v", err)
		}
		close(releaseLoopFetch)
	}()

	if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
	}
	if requests[1].Model != "gpt-4.1-mini" {
		t.Fatalf("the queued message ran on model %q; a newer pick queued while the loop's application refetched must apply before the turn it admits (%q)", requests[1].Model, "gpt-4.1-mini")
	}
}

// TestCommittedSwitchPublicationsLandInCommitOrder pins the publication lock's
// span: a queued application that committed its swap publishes before a newer
// at-rest pick that arrives while the older application sits between its swap
// and its publication. The newer pick's application cannot commit inside that
// window, so both publications land - the older first, the newer last - and
// subscribers end on the newer model without the older one ever publishing
// over it.
func TestCommittedSwitchPublicationsLandInCommitOrder(t *testing.T) {
	dir := t.TempDir()
	listModels := attemptRecordingLiveModels("openai", "gpt-5.2", "gpt-5.4", "gpt-4.1-mini")
	adapter := &fakeAdapter{
		name:       "openai",
		liveModels: func(ctx context.Context) ([]registry.Model, error) { return listModels(ctx) },
	}
	s := newSession(t, withAdapter(adapter), withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The queued pick: mid-turn, so it stores and the idle tail
			// applies it.
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("queued pick: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
	})
	// Gate the queued application between its swap and its publication: the
	// newer pick's application then waits on the publication lock until the
	// older application has told its story. One-shot: the newer pick's own
	// publication passes the gate without parking.
	releaseAPublish := make(chan struct{})
	aPublishEntered := make(chan struct{}, 1)
	hookOnce := make(chan struct{}, 1)
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeModelSwitchPublish = func() {
			select {
			case hookOnce <- struct{}{}:
				aPublishEntered <- struct{}{}
				<-releaseAPublish
			default:
			}
		}
	})

	evs, stop := captureEvents(s)
	newerDone := make(chan error, 1)
	go func() {
		<-aPublishEntered
		// Release the older application first: its publication owns the lock,
		// and the newer pick's application waits behind it.
		close(releaseAPublish)
		newerDone <- s.SetModel("gpt-4.1-mini")
	}()

	if _, err := s.ProcessInput(context.Background(), "the only turn", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if err := <-newerDone; err != nil {
		t.Fatalf("newer pick: %v", err)
	}
	stop()

	var order []string
	for _, ev := range *evs {
		if d, ok := ev.Data.(events.ModelChangedData); ok {
			order = append(order, d.NewModel)
		}
	}
	want := []string{"gpt-5.4", "gpt-4.1-mini"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("model-changed publications = %v, want the committed switch's publication to land before the newer pick's (%v)", order, want)
	}
	if got := s.Profile().Model(); got != "gpt-4.1-mini" {
		t.Fatalf("profile model = %q, want the newer pick (%q)", got, "gpt-4.1-mini")
	}
}

// TestNewerPickReconcilesToolsAgainstTheProfileItActuallyReplaces pins the
// commit path's transition facts: a pick classifies its switch against the
// profile current when it validated, but commits later, and an older pick's
// application can move the session through another provider while this one
// sat parked in its live-listing preflight. The commit must reconcile the
// provider-specific tools against the profile it actually replaces, so a
// same-provider pick landing past an intervening cross-provider switch still
// re-registers the tools that switch removed.
func TestNewerPickReconcilesToolsAgainstTheProfileItActuallyReplaces(t *testing.T) {
	dir := t.TempDir()
	googleAdapter := &gatedListingAdapter{name: "google"}
	openaiAdapter := &gatedListingAdapter{name: "openai"}
	s := newSession(t,
		withAdapter(googleAdapter),
		withAdapter(openaiAdapter),
		withProfile(newGeminiProfile("gemini-2.5-pro")),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
	if s.reg.Get("web_search") == nil {
		t.Fatal("pre-condition: the session must start with web_search wired for a web-searching google profile")
	}
	// The fixture session carries no cross-instance resolver; production wires
	// cfg.ResolveProfile to the client's registry, so wire the same here and the
	// openai pick resolves as the cross-provider switch it is.
	s.resolveProfile = func(ref string) (*provider.Profile, error) {
		res, err := s.client.Resolve(ref)
		if err != nil {
			return nil, err
		}
		return provider.FromResolved(res, s.client.Registry()), nil
	}
	// Armed after construction: NewSession's startup resolve must not consume
	// the picks' scripted listing calls.
	releaseOlder, releaseNewer := make(chan struct{}), make(chan struct{})
	olderCalled, newerCalled := make(chan struct{}, 1), make(chan struct{}, 1)
	openaiAdapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			olderCalled <- struct{}{}
			<-releaseOlder
			return []registry.Model{{ID: "gpt-5.2"}}, nil
		},
	}
	googleAdapter.models = []func() ([]registry.Model, error){
		func() ([]registry.Model, error) {
			newerCalled <- struct{}{}
			<-releaseNewer
			return []registry.Model{{ID: "gemini-2.5-pro"}}, nil
		},
	}

	// The older pick (openai) is admitted first and parks in its enumeration
	// after classifying its cross-provider switch against the google profile.
	olderDone := make(chan error, 1)
	go func() { olderDone <- s.SetModel("openai/gpt-5.2") }()
	<-olderCalled
	// The newer pick (google) is admitted second: it classifies itself
	// same-provider against the still-google profile, then parks in its own
	// enumeration. Its commit lands after the older pick's, which the
	// acceptance watermark permits: neither application superseded the other.
	newerDone := make(chan error, 1)
	go func() { newerDone <- s.SetModel("gemini-2.5-pro") }()
	<-newerCalled

	// The older pick's enumeration returns and its commit lands while the
	// newer pick is still parked: google -> openai, web_search removed.
	close(releaseOlder)
	if err := <-olderDone; err != nil {
		t.Fatalf("older pick: %v", err)
	}
	if s.reg.Get("web_search") != nil {
		t.Fatal("pre-condition: the intervening openai switch must have removed web_search")
	}

	// The newer pick's enumeration returns and its commit lands on the openai
	// profile: the transition it actually replaces is openai -> google, and
	// the provider-specific tools the intervening switch removed must return.
	close(releaseNewer)
	if err := <-newerDone; err != nil {
		t.Fatalf("newer pick: %v", err)
	}
	if s.reg.Get("web_search") == nil {
		t.Fatal("web_search is missing after a same-provider pick landed past an intervening cross-provider switch; the commit must reconcile provider tools against the profile it actually replaces")
	}
	if got := s.Profile().Model(); got != "gemini-2.5-pro" {
		t.Fatalf("profile model = %q; the newer pick must own the outcome (%q)", got, "gemini-2.5-pro")
	}
}

// TestACommittedSwitchPublishesEvenWhenANewerQueuedPickLaterFails pins the
// publication contract behind supersession: a newer pick can queue while an
// older switch is committing, and that queued pick can still fail its deferred
// validation at the boundary it was waiting for. Suppressing an already
// committed switch's publication because a newer pick queued would then leave
// the session running a model no marker or event ever named - subscribers
// keep showing the previous model with nothing to correct them. A switch that
// has committed its swap must publish; supersession belongs before the swap,
// never after it.
func TestACommittedSwitchPublishesEvenWhenANewerQueuedPickLaterFails(t *testing.T) {
	dir := t.TempDir()
	openaiAdapter := &fakeAdapter{
		name: "openai",
		liveModels: func(context.Context) ([]registry.Model, error) {
			return []registry.Model{{ID: "gpt-5.2"}, {ID: "gpt-5.4"}}, nil
		},
	}
	glmAdapter := &fakeAdapter{
		name: "glm",
		liveModels: func(context.Context) ([]registry.Model, error) {
			return []registry.Model{{ID: "glm-test"}}, nil
		},
	}
	s := newSession(t,
		withAdapter(openaiAdapter),
		withAdapter(glmAdapter),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
	// Pre-condition: the newer queued pick's target must fail the deferred
	// history check. OpenAIChat carries USER documents as file parts (#5485)
	// but still drops non-user ones, so the seeded history carries an
	// assistant-role document.
	doc := documentTurn()
	doc.Message.Role = llm.RoleAssistant
	if len(unrepresentableHistoryKinds([]schema.Turn{doc}, registry.ProtocolOpenAIChat)) == 0 {
		t.Fatal("pre-condition: a non-user document must be unrepresentable for the queued pick's OpenAIChat target")
	}
	s.appendTurn(schema.TurnAssistant, doc.Message)

	releaseAPublish := make(chan struct{})
	aPublishEntered := make(chan struct{}, 1)
	hookOnce := make(chan struct{}, 1)
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeModelSwitchPublish = func() {
			select {
			case hookOnce <- struct{}{}:
				aPublishEntered <- struct{}{}
				<-releaseAPublish
			default:
			}
		}
	})
	openaiAdapter.setSteps([]func(llm.Request) llm.Response{
		// Turn 1: pick A mid-turn; the pick queues for this turn's boundary.
		func(llm.Request) llm.Response {
			if err := s.SetModel("gpt-5.4"); err != nil {
				t.Errorf("mid-turn pick of A: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
	})
	// Serve the session through this test's own lossless consumer: one drain
	// owns the events, so nothing races the capture for them.
	var evMu sync.Mutex
	evs := []events.SessionEvent{}
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		evMu.Lock()
		evs = append(evs, ev)
		evMu.Unlock()
	}, func() { close(drained) })
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	queueOneMutation(t, s, "cm-committed-publish", "the message queued behind the first turn")

	// The RPC-side goroutine: while A's application sits between its swap and
	// its publication, the newer pick arrives and queues for the next
	// boundary. One-shot hook: A's own publication is the one parked.
	go func() {
		<-aPublishEntered
		if err := s.SetModel("glm/glm-test"); err != nil {
			t.Errorf("queued pick of B: %v", err)
		}
		close(releaseAPublish)
	}()

	// The one input drains turn 1, applies A at the boundary (parked, then
	// released past B's queueing), runs the queued message on A's model, and
	// at its tail pops B - whose deferred history check fails there, so the
	// pick is dropped with a warning, not a switch.
	if _, err := s.ProcessInput(context.Background(), "the first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	s.Close()
	<-drained

	var last events.ModelChangedData
	sawModelChanged := false
	var nextModel []events.NextModelChangedData
	evMu.Lock()
	for _, ev := range evs {
		if d, ok := ev.Data.(events.ModelChangedData); ok {
			last = d
			sawModelChanged = true
		}
		if d, ok := ev.Data.(events.NextModelChangedData); ok {
			nextModel = append(nextModel, d)
		}
	}
	evMu.Unlock()
	if !sawModelChanged || last.NewModel != "gpt-5.4" {
		t.Fatalf("the last thread/model/changed named %q (seen any = %t); the session runs the committed switch's model %q even though the newer queued pick failed its deferred history check, so the committed switch's publication must not be suppressed", last.NewModel, sawModelChanged, "gpt-5.4")
	}
	if got := s.Profile().Model(); got != "gpt-5.4" {
		t.Fatalf("profile model = %q, want the committed switch's model %q; the failed queued pick must not swap", got, "gpt-5.4")
	}
	// The acceptance publications must keep a subscriber's label truthful
	// throughout: A's pick is announced when it queues, B's pick is announced
	// when it queues, A's commit re-asserts B (the commit frame names the model
	// the session runs now, so the newer accepted pick must follow it), and
	// B's deferred rejection must retract its announcement back to the model
	// the session actually runs — the last acceptance names A again, never B,
	// so no surface keeps showing a pick that failed.
	wantNext := []struct{ provider, model string }{
		{"openai", "gpt-5.4"},
		{"glm", "glm-test"},
		{"glm", "glm-test"},
		{"openai", "gpt-5.4"},
	}
	if len(nextModel) != len(wantNext) {
		t.Fatalf("next-model announcements = %+v, want %d: A accepted, B accepted, B re-asserted at A's commit, B's failed deferred check reverting to A", nextModel, len(wantNext))
	}
	for i, want := range wantNext {
		if nextModel[i].Provider != want.provider || nextModel[i].Model != want.model {
			t.Fatalf("next-model announcement %d = %s/%s, want %s/%s", i, nextModel[i].Provider, nextModel[i].Model, want.provider, want.model)
		}
	}
	// Commit-relative order: B's acceptance precedes A's commit publication,
	// and the re-assertion follows it - the commit frame names the model the
	// session runs now, so the re-assertion is what keeps a subscriber's
	// label from resting on the committed model until B's own boundary.
	evMu.Lock()
	bAccept, aCommit, bReassert := -1, -1, -1
	for i, ev := range evs {
		if d, ok := ev.Data.(events.NextModelChangedData); ok && d.Model == "glm-test" {
			if bAccept < 0 {
				bAccept = i
			} else if aCommit >= 0 && bReassert < 0 {
				bReassert = i
			}
		}
		if d, ok := ev.Data.(events.ModelChangedData); ok && d.NewModel == "gpt-5.4" {
			aCommit = i
		}
	}
	evMu.Unlock()
	if bAccept < 0 || aCommit <= bAccept || bReassert <= aCommit {
		t.Fatalf("commit-relative order = B accepted at %d, A committed at %d, B re-asserted at %d; the acceptance must precede the commit that would bury it and the re-assertion must follow that commit", bAccept, aCommit, bReassert)
	}
}

// stagedLadderProtocol is an instance whose live listing names the target's
// effort tiers only from its second listing on: the session's startup capture
// saw the model reason without naming tiers, and the pick's own fetch is what
// carries them. The listing must run through the transport protocol - a
// registered adapter override's rows are never applied to the registry's live
// layer - for the tiers to reach a later resolve. The transport stubs come
// from windowListingProtocol; only the staged ListModels is this test's own.
type stagedLadderProtocol struct {
	windowListingProtocol
	instance string
	mu       sync.Mutex
	calls    int
}

func (p *stagedLadderProtocol) ListModels(_ context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Instance != p.instance {
		return []registry.Model{{ID: "gpt-launch"}}, nil
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call < 2 {
		return []registry.Model{{ID: "gpt-tier", Caps: registry.Caps{Reasoning: new(true)}}}, nil
	}
	return []registry.Model{{ID: "gpt-tier", Caps: registry.Caps{Reasoning: new(true), EffortValues: []string{"low", "high"}}}}, nil
}

// newStagedLadderProtocol returns the id's registered ladder protocol,
// resetting its listing counter for a repeated test run, or registers a new
// one serving instance.
func newStagedLadderProtocol(id, instance string) *stagedLadderProtocol {
	if existing, ok := llm.ProtocolFor(id); ok {
		p, ok := existing.(*stagedLadderProtocol)
		if !ok {
			panic("staged ladder protocol registered with another type")
		}
		p.mu.Lock()
		p.calls = 0
		p.mu.Unlock()
		return p
	}
	p := &stagedLadderProtocol{instance: instance}
	p.id = id
	llm.RegisterProtocol(p)
	return p
}

func (p *stagedLadderProtocol) listingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestQueuedPickAnnouncementCarriesTheLiveListedReasoningLadder pins the
// acceptance against the profile the pick-time enumeration enriched: the
// target's static registry row reasons but names no effort tiers, and the
// instance's live listing is the only place the tiers appear. The acceptance
// must publish the enriched ladder - what the boundary's commit frame already
// carries - or the composer's effort ladder is empty between the pick and its
// boundary.
func TestQueuedPickAnnouncementCarriesTheLiveListedReasoningLadder(t *testing.T) {
	const protocolID = "test-proto-agent-queue-ladder"
	proto := newStagedLadderProtocol(protocolID, "lunaroute")
	dir := t.TempDir()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"home": {
				Base: "openai-compatible", APIKey: "test",
				Transport: registry.Transport{BaseURL: "https://home.example.com/v1"},
				Models:    map[string]registry.Model{"gpt-launch": {}},
			},
			"lunaroute": {
				Base: "openai-compatible", APIKey: "test",
				Transport: registry.Transport{BaseURL: "https://lunaroute.example.com/v1"},
				Protocol:  protocolID,
				Models: map[string]registry.Model{
					// The static row reasons but names no ladder; only the
					// instance's live listing carries the tiers.
					"gpt-tier": {Caps: registry.Caps{Reasoning: new(true)}},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	homeAdapter := &fakeAdapter{
		name:       "home",
		liveModels: func(context.Context) ([]registry.Model, error) { return []registry.Model{{ID: "gpt-launch"}}, nil },
	}
	client.Register(homeAdapter)
	// Every other instance gets a scripted adapter so the session's startup
	// enumeration stays offline; lunaroute keeps none, so its listings run
	// through the staged transport protocol.
	for _, inst := range r.Instances() {
		if inst.Name != "home" && inst.Name != "lunaroute" {
			client.Register(&fakeAdapter{name: inst.Name})
		}
	}
	launch, err := provider.Resolve(r, "home/gpt-launch")
	if err != nil {
		t.Fatalf("launch profile: %v", err)
	}
	s := newSession(t,
		withClient(client),
		withProfile(launch),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
	homeAdapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			if err := s.SetModel("lunaroute/gpt-tier"); err != nil {
				t.Errorf("mid-turn pick: %v", err)
			}
			return agenttest.CommunicateResponse(true, "first turn done")
		},
	})

	var evMu sync.Mutex
	evs := []events.SessionEvent{}
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		evMu.Lock()
		evs = append(evs, ev)
		evMu.Unlock()
	}, func() { close(drained) })
	if _, err := s.ProcessInput(context.Background(), "the only turn", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	s.Close()
	<-drained

	var acceptance *events.NextModelChangedData
	evMu.Lock()
	for _, ev := range evs {
		if d, ok := ev.Data.(events.NextModelChangedData); ok {
			acceptance = &d
		}
	}
	evMu.Unlock()
	if acceptance == nil {
		t.Fatal("no acceptance announcement for the queued pick")
	}
	if acceptance.Provider != "lunaroute" || acceptance.Model != "gpt-tier" {
		t.Fatalf("acceptance = %s/%s, want lunaroute/gpt-tier", acceptance.Provider, acceptance.Model)
	}
	if len(acceptance.ReasoningEffortLevels) != 2 || acceptance.ReasoningEffortLevels[0] != "low" || acceptance.ReasoningEffortLevels[1] != "high" {
		t.Fatalf("acceptance ladder = %v, want the live listing's [low high]", acceptance.ReasoningEffortLevels)
	}
	if got := proto.listingCount(); got < 2 {
		t.Fatalf("lunaroute listed %d times, want the pick's fetch among them", got)
	}
}

// TestRejectedDeferredPickDoesNotCancelAnEarlierValidPick pins the ordering in
// issue #6621: pick A is valid, pick B is later and fails only the boundary's
// deferred history check. B's rejection must not take A's accepted choice with
// it.
//
// The pair is the whole matrix the store and the commit check must agree on:
//   - valid then invalid: A applies even though B was picked later.
//   - invalid then valid: B's rejection leaves nothing queued, so A applies.
//
// The assertions are behavioral: the model each turn actually ran on and the
// session's selected model. B's pick is shown to pass pick-time validation
// (SetModel returns nil for it) while its target cannot carry the settled
// history, so the only place it can fail is the boundary - which is the path
// that used to cancel A.
func TestRejectedDeferredPickDoesNotCancelAnEarlierValidPick(t *testing.T) {
	t.Run("valid then invalid", func(t *testing.T) {
		dir := t.TempDir()
		listModels := attemptRecordingLiveModels("openai", "gpt-5.2", "gpt-5.4")
		openaiAdapter := &fakeAdapter{
			name:       "openai",
			liveModels: func(ctx context.Context) ([]registry.Model, error) { return listModels(ctx) },
		}
		glmAdapter := &fakeAdapter{
			name: "glm",
			liveModels: func(context.Context) ([]registry.Model, error) {
				return []registry.Model{{ID: "glm-test"}}, nil
			},
		}
		s := newSession(t,
			withAdapter(openaiAdapter),
			withAdapter(glmAdapter),
			withDir(dir),
			withConfig(SessionConfig{
				StateDir:         dir,
				MaxSubagentDepth: 1,
				ResolveProfile:   testResolver,
				testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			}))
		// B's target speaks the Chat protocol, which cannot carry a non-user
		// document, while A's target (OpenAI Responses) can. That asymmetry is
		// what makes B fail at the boundary and A survive it.
		doc := documentTurn()
		doc.Message.Role = llm.RoleAssistant
		if len(unrepresentableHistoryKinds([]schema.Turn{doc}, registry.ProtocolOpenAIChat)) == 0 {
			t.Fatal("pre-condition: a non-user document must be unrepresentable for B's target")
		}
		if got := len(unrepresentableHistoryKinds([]schema.Turn{doc}, NewOpenAIProfile("gpt-5.4").Protocol())); got != 0 {
			t.Fatalf("pre-condition: A's target must carry the document, got %d offending kinds", got)
		}

		openaiAdapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				// A is valid: it resolves, passes membership, and queues.
				if err := s.SetModel("gpt-5.4"); err != nil {
					t.Errorf("valid pick: %v", err)
				}
				// The content B cannot carry arrives after A queued, so only
				// B's deferred check sees it.
				s.appendTurn(schema.TurnAssistant, doc.Message)
				// B passes pick-time resolution and membership; only the
				// boundary rejects it.
				if err := s.SetModel("glm/glm-test"); err != nil {
					t.Errorf("B must pass pick-time validation and fail only at the boundary, got: %v", err)
				}
				return agenttest.CommunicateResponse(true, "first turn done")
			},
			func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
		})
		serveSessionWithMutationStore(t, s)
		queueOneMutation(t, s, "cm-valid-then-invalid", "the queued message")

		if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}

		requests := openaiAdapter.Requests()
		if len(requests) != 2 {
			t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
		}
		if requests[1].Model != "gpt-5.4" {
			t.Fatalf("the queued message ran on model %q; the rejected later pick must not cancel the earlier valid pick (%q)", requests[1].Model, "gpt-5.4")
		}
		if got := s.Profile().Model(); got != "gpt-5.4" {
			t.Fatalf("selected model = %q after the later pick's deferred rejection; the earlier valid pick must still be selected (%q)", got, "gpt-5.4")
		}
	})

	t.Run("invalid then valid", func(t *testing.T) {
		dir := t.TempDir()
		listModels := attemptRecordingLiveModels("openai", "gpt-5.2", "gpt-5.4")
		openaiAdapter := &fakeAdapter{
			name:       "openai",
			liveModels: func(ctx context.Context) ([]registry.Model, error) { return listModels(ctx) },
		}
		s := newSession(t,
			withAdapter(openaiAdapter),
			withDir(dir),
			withConfig(SessionConfig{
				StateDir:         dir,
				MaxSubagentDepth: 1,
				testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			}))
		openaiAdapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				if err := s.SetModel("bogus/no-such-model"); err == nil {
					t.Error("the invalid pick was accepted; it must be rejected")
				}
				// The rejection must not change the selection on its own.
				if got := s.Profile().Model(); got != "gpt-5.2" {
					t.Fatalf("the rejected pick changed the selected model to %q; a rejection selects nothing (%q)", got, "gpt-5.2")
				}
				if err := s.SetModel("gpt-5.4"); err != nil {
					t.Errorf("the valid pick after a rejection: %v", err)
				}
				return agenttest.CommunicateResponse(true, "first turn done")
			},
			func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "queued turn done") },
		})
		serveSessionWithMutationStore(t, s)
		queueOneMutation(t, s, "cm-invalid-then-valid", "the queued message")

		if _, err := s.ProcessInput(context.Background(), "first message", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}

		requests := openaiAdapter.Requests()
		if len(requests) != 2 {
			t.Fatalf("model requests = %d, want the first turn and the drained queued message (2)", len(requests))
		}
		if requests[1].Model != "gpt-5.4" {
			t.Fatalf("the queued message ran on model %q; the valid pick after a rejection must apply (%q)", requests[1].Model, "gpt-5.4")
		}
		if got := s.Profile().Model(); got != "gpt-5.4" {
			t.Fatalf("selected model = %q, want the later valid pick (%q)", got, "gpt-5.4")
		}
	})
}
