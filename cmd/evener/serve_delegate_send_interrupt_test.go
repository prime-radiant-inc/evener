package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
	"primeradiant.com/evener/server"
)

const (
	waiterInterruptRootPrompt = "ROOT-WAITER-INTERRUPT"
	waiterInterruptChildTask  = "CHILD-WAITER-INTERRUPT"
)

var waiterInterruptDelegateID = regexp.MustCompile(`dlg_[A-Za-z0-9_-]+`)

type waiterInterruptAdapter struct {
	initialTerminal    chan struct{}
	childSecondEntered chan struct{}
	childMayFinish     chan struct{}
	afterSendEntered   chan struct{}

	mu           sync.Mutex
	rootCalls    int
	childCalls   int
	afterSendOne sync.Once
}

func newWaiterInterruptAdapter() *waiterInterruptAdapter {
	return &waiterInterruptAdapter{
		initialTerminal:    make(chan struct{}),
		childSecondEntered: make(chan struct{}),
		childMayFinish:     make(chan struct{}),
		afterSendEntered:   make(chan struct{}),
	}
}

func (a *waiterInterruptAdapter) Name() string { return "openai" }

func (a *waiterInterruptAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	text := requestFullText(req)
	if strings.Contains(text, waiterInterruptChildTask) && !strings.Contains(text, waiterInterruptRootPrompt) {
		a.mu.Lock()
		a.childCalls++
		childCall := a.childCalls
		a.mu.Unlock()
		if childCall == 1 {
			return scriptedCommunicate("initial child run finished"), nil
		}
		if childCall == 2 {
			close(a.childSecondEntered)
			select {
			case <-a.childMayFinish:
				return scriptedCommunicate("child finished inline"), nil
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return scriptedCommunicate("unexpected child continuation"), nil
	}

	a.mu.Lock()
	a.rootCalls++
	call := a.rootCalls
	a.mu.Unlock()
	switch call {
	case 1:
		return scriptedDelegateCall("create_waiter_target", waiterInterruptChildTask), nil
	case 2:
		select {
		case <-a.initialTerminal:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		// Finish one ordinary tool round so the initial background delivery is
		// flushed and its wake bit cannot mask the positive-wait delivery wake.
		return scriptedToolCalls(scriptedForegroundShellCall("flush_initial_delivery", "printf flushed")), nil
	case 3:
		delegateID := waiterInterruptDelegateID.FindString(text)
		if delegateID == "" {
			return llm.Response{}, errors.New("delegate creation result carried no delegate id")
		}
		args, _ := json.Marshal(map[string]any{
			"to":          delegateID,
			"message":     "finish and report inline",
			"max_wait_ms": 60_000,
		})
		return scriptedToolCalls(llm.ToolCallData{
			ID:        "positive_wait_send",
			Name:      "delegate_send",
			Arguments: args,
			Type:      "function",
		}), nil
	default:
		a.afterSendOne.Do(func() { close(a.afterSendEntered) })
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
}

func (a *waiterInterruptAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type waiterInterruptServer struct {
	*server.Server
	notification           chan struct{}
	armed                  atomic.Bool
	processingBarrierArmed atomic.Bool
	processingFalseEntered chan struct{}
	releaseProcessingFalse chan struct{}
	processingBarrierOnce  sync.Once
	processingReleaseOnce  sync.Once
	once                   sync.Once
}

func (s *waiterInterruptServer) SetProcessing(processing bool) {
	s.Server.SetProcessing(processing)
	if !processing && s.processingBarrierArmed.Load() {
		s.processingBarrierOnce.Do(func() { close(s.processingFalseEntered) })
		<-s.releaseProcessingFalse
	}
}

func (s *waiterInterruptServer) armProcessingBarrier() {
	s.processingBarrierArmed.Store(true)
}

func (s *waiterInterruptServer) releaseProcessingBarrier() {
	s.processingReleaseOnce.Do(func() { close(s.releaseProcessingFalse) })
}

func (s *waiterInterruptServer) SubmitNotification() {
	if s.armed.Load() {
		s.once.Do(func() { close(s.notification) })
	}
	s.Server.SubmitNotification()
}

type waiterInterruptDaemon struct {
	adapter             *waiterInterruptAdapter
	client              *appwire.Client
	ctx                 context.Context
	ref                 string
	terminalClaimed     chan struct{}
	positiveWaitSeen    chan struct{}
	turnEnded           chan struct{}
	sessionEndEntered   chan struct{}
	releaseSessionEnd   chan struct{}
	sessionEndProjected chan struct{}
	releaseOnce         sync.Once
	server              *waiterInterruptServer
	stateDir            string
	sessionID           string
	interruptResponseID *atomic.Value
	interruptResponse   chan struct{}
}

func startWaiterInterruptDaemon(t *testing.T) *waiterInterruptDaemon {
	t.Helper()
	adapter := newWaiterInterruptAdapter()
	positiveWaitSeen := make(chan struct{})
	terminalClaimed := make(chan struct{})
	turnEnded := make(chan struct{})
	sessionEndEntered := make(chan struct{})
	releaseSessionEnd := make(chan struct{})
	sessionEndProjected := make(chan struct{})
	var turnEndedOnce sync.Once
	var sessionEndEnteredOnce sync.Once
	var sessionEndProjectedOnce sync.Once
	var positiveWaitOnce sync.Once
	var initialTerminalOnce sync.Once
	var terminalOnce sync.Once
	var terminalMu sync.Mutex
	initialTerminalSeen := false
	secondGenerationRunning := false

	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
		client := llm.NewClient()
		client.Register(adapter)
		cfg := providercfg.Config{
			Default:   "openai",
			Instances: []providercfg.InstanceConfig{{Name: "openai", Type: "openai"}},
		}
		return client, cfg, true, func() error { return nil }, nil
	}
	var liveSession *agent.Session
	deps.newSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 0}
		sess, err := agent.NewSession(client, profile, env, cfg)
		if err == nil {
			liveSession = sess
		}
		return sess, err
	}
	var observedServer *server.Server
	var waiterServer *waiterInterruptServer
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		observedServer = server.NewServer(cfg)
		waiterServer = &waiterInterruptServer{
			Server:                 observedServer,
			notification:           make(chan struct{}),
			processingFalseEntered: make(chan struct{}),
			releaseProcessingFalse: make(chan struct{}),
		}
		return waiterServer
	}
	deps.bridge = func(_ serveServer, session *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		session.ConsumeEventsLossless(func(event events.SessionEvent) {
			if event.Kind == events.EventSessionEnd {
				if data, ok := event.Data.(events.SessionEndData); ok && data.Interrupted {
					sessionEndEnteredOnce.Do(func() { close(sessionEndEntered) })
					<-releaseSessionEnd
				}
			}
			server.BridgeEvent(observedServer, event, observer)
			if event.Kind == events.EventSessionEnd {
				if data, ok := event.Data.(events.SessionEndData); ok && data.Interrupted {
					sessionEndProjectedOnce.Do(func() { close(sessionEndProjected) })
				}
			}
			switch event.Kind {
			case events.EventToolCallStart:
				if data, ok := event.Data.(events.ToolCallStartData); ok && data.ToolName == "delegate_send" {
					positiveWaitOnce.Do(func() { close(positiveWaitSeen) })
				}
			case events.EventDelegateUpdated:
				if data, ok := event.Data.(events.DelegateUpdatedData); ok {
					terminalMu.Lock()
					switch {
					case !initialTerminalSeen && data.Terminal && data.Lifecycle == "idle":
						initialTerminalSeen = true
						initialTerminalOnce.Do(func() { close(adapter.initialTerminal) })
					case initialTerminalSeen && !data.Terminal && data.Lifecycle == "running":
						secondGenerationRunning = true
					case secondGenerationRunning && data.Terminal && data.Lifecycle == "idle":
						terminalOnce.Do(func() { close(terminalClaimed) })
					}
					terminalMu.Unlock()
				}
			case events.EventTurnEnded:
				turnEndedOnce.Do(func() { close(turnEnded) })
			}
		}, onDrained)
	}
	deps.subscriberCount = func(_ serveServer, id string) int {
		return observedServer.AppServer().SubscriberCount(id)
	}

	runDir := t.TempDir()
	stateDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		runErr := runServeWithDeps([]string{
			"--model", "openai/gpt-test",
			"--addr", "127.0.0.1:0",
			"--dir", t.TempDir(),
			"--state-dir", stateDir,
			"--run-dir", runDir,
		}, deps)
		done <- runErr
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		cancel()
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	var interruptResponseID atomic.Value
	interruptResponseCompleted := make(chan struct{})
	var interruptResponseOnce sync.Once
	client.SetOrderedFrameHandler(func(message appwire.Message, err error) {
		if err != nil {
			return
		}
		id, _ := interruptResponseID.Load().(string)
		if id != "" && message.IDString() == id {
			interruptResponseOnce.Do(func() { close(interruptResponseCompleted) })
		}
	})
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "waiter-interrupt-test", Version: "test"},
	}); err != nil {
		client.Close()
		cancel()
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() {
		client.Close()
		if liveSession != nil {
			liveSession.Close()
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		shutdownReq, requestErr := http.NewRequestWithContext(shutdownCtx, http.MethodPost, "http://"+entry.Address+"/shutdown", nil)
		var shutdownResp *http.Response
		var shutdownErr error
		if requestErr == nil {
			shutdownResp, shutdownErr = http.DefaultClient.Do(shutdownReq)
		} else {
			shutdownErr = requestErr
		}
		if shutdownErr == nil {
			shutdownResp.Body.Close()
		}
		shutdownCancel()
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Errorf("runServeWithDeps: %v", runErr)
			}
		case <-ctx.Done():
			t.Errorf("runServeWithDeps did not exit: %v", ctx.Err())
		}
		cancel()
	})
	return &waiterInterruptDaemon{
		adapter:             adapter,
		client:              client,
		ctx:                 ctx,
		ref:                 appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String(),
		terminalClaimed:     terminalClaimed,
		positiveWaitSeen:    positiveWaitSeen,
		turnEnded:           turnEnded,
		sessionEndEntered:   sessionEndEntered,
		releaseSessionEnd:   releaseSessionEnd,
		sessionEndProjected: sessionEndProjected,
		server:              waiterServer,
		stateDir:            stateDir,
		sessionID:           entry.SessionID,
		interruptResponseID: &interruptResponseID,
		interruptResponse:   interruptResponseCompleted,
	}
}

func (d *waiterInterruptDaemon) releaseInterruptedSessionEnd() {
	d.releaseOnce.Do(func() { close(d.releaseSessionEnd) })
}

type waiterInterruptMutationSnapshot struct {
	QueueHeld      bool            `json:"queue_held"`
	SteeringHeld   bool            `json:"steering_held"`
	InterruptFence json.RawMessage `json:"interrupt_fence"`
	Journal        map[string]struct {
		OperationState string `json:"operation_state"`
		ExecutionState string `json:"execution_state"`
	} `json:"journal"`
}

func (d *waiterInterruptDaemon) mutationSnapshot(t *testing.T) waiterInterruptMutationSnapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.stateDir, "mutations", d.sessionID+".json"))
	if err != nil {
		t.Fatalf("read client mutation snapshot: %v", err)
	}
	var snapshot waiterInterruptMutationSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode client mutation snapshot: %v", err)
	}
	return snapshot
}

func awaitWaiterInterruptSignal(ctx context.Context, t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", label, ctx.Err())
	}
}

func waiterInterruptTurnStatuses(turns []appwire.Turn) []string {
	statuses := make([]string, 0, len(turns))
	for _, turn := range turns {
		statuses = append(statuses, turn.ID+"="+turn.Status)
	}
	return statuses
}

func TestRunServeInterruptSettlesClaimedPositiveWaitDelegateSend(t *testing.T) {
	daemon := startWaiterInterruptDaemon(t)
	defer daemon.releaseInterruptedSessionEnd()
	started, err := daemon.client.TurnStart(daemon.ctx, appwire.TurnStartParams{
		ClientMutationID: "waiter-interrupt-turn",
		Ref:              daemon.ref,
		Input:            []appwire.InputItem{{Type: "text", Text: waiterInterruptRootPrompt}},
	})
	if err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	awaitWaiterInterruptSignal(daemon.ctx, t, daemon.positiveWaitSeen, "positive-wait delegate_send start")
	awaitWaiterInterruptSignal(daemon.ctx, t, daemon.adapter.childSecondEntered, "positive-wait delegate generation")
	daemon.server.armed.Store(true)
	close(daemon.adapter.childMayFinish)
	awaitWaiterInterruptSignal(daemon.ctx, t, daemon.terminalClaimed, "terminal delegate waiter claim")
	select {
	case <-daemon.server.notification:
		// The unfixed path has accepted and deferred the waiter-bearing plan.
	case <-daemon.adapter.afterSendEntered:
		// The fixed path resolved it inline and advanced the same root turn.
	case <-daemon.ctx.Done():
		t.Fatalf("waiting for delivery acceptance: %v", daemon.ctx.Err())
	}

	// The RPC itself awaits runnerDone; this deadline is only a deadlock tripwire,
	// not the completion mechanism.
	interruptCtx, cancelInterrupt := context.WithTimeout(daemon.ctx, 3*time.Second)
	defer cancelInterrupt()
	interrupt := appwire.TurnInterruptParams{ClientMutationID: "stop-claimed-waiter", Ref: daemon.ref}
	daemon.server.armProcessingBarrier()
	defer daemon.server.releaseProcessingBarrier()
	interruptResult := make(chan error, 1)
	go func() {
		requestCtx := appwire.WithRequestIDObserver(interruptCtx, func(id appwire.ID) {
			daemon.interruptResponseID.Store(id.String())
		})
		interruptResult <- daemon.client.TurnInterrupt(requestCtx, interrupt)
	}()
	awaitWaiterInterruptSignal(daemon.ctx, t, daemon.server.processingFalseEntered, "runner SetProcessing(false) barrier")
	select {
	case <-daemon.interruptResponse:
		t.Fatal("TurnInterrupt response completed before runnerDone barrier release")
	default:
	}
	daemon.server.releaseProcessingBarrier()
	select {
	case <-daemon.interruptResponse:
	case <-interruptCtx.Done():
		t.Fatalf("TurnInterrupt response did not complete after runnerDone barrier release: %v", interruptCtx.Err())
	}
	select {
	case err := <-interruptResult:
		if err != nil {
			t.Fatalf("TurnInterrupt did not settle the claimed inline waiter and runner: %v", err)
		}
	case <-interruptCtx.Done():
		t.Fatalf("TurnInterrupt did not return after runnerDone barrier release: %v", interruptCtx.Err())
	}
	stopped := daemon.mutationSnapshot(t)
	startRecord, startPresent := stopped.Journal["waiter-interrupt-turn"]
	interruptRecord, interruptPresent := stopped.Journal[interrupt.ClientMutationID]
	if len(stopped.InterruptFence) != 0 || !startPresent || !interruptPresent ||
		startRecord.OperationState != "terminal" || startRecord.ExecutionState != "interrupted" ||
		interruptRecord.OperationState != "terminal" || interruptRecord.ExecutionState != "interrupted" {
		t.Fatalf("interrupt was not durably terminal immediately after TurnInterrupt: fence=%q start_present=%t start=%#v interrupt_present=%t interrupt=%#v", stopped.InterruptFence, startPresent, startRecord, interruptPresent, interruptRecord)
	}
	awaitWaiterInterruptSignal(daemon.ctx, t, daemon.sessionEndEntered, "interrupted SESSION_END bridge entry")
	select {
	case <-daemon.turnEnded:
	default:
		t.Fatal("TurnInterrupt returned before the interrupted runner emitted TURN_ENDED")
	}
	turns, err := daemon.client.ThreadTurnsList(daemon.ctx, appwire.ThreadTurnsListParams{Ref: daemon.ref})
	if err != nil {
		t.Fatalf("ThreadTurnsList: %v", err)
	}
	terminal := false
	for _, turn := range turns.Data {
		if turn.ID == started.Turn.ID {
			terminal = turn.Status == "interrupted"
		}
	}
	if terminal {
		t.Fatalf("turn %q terminalized before its SESSION_END projection was released: %#v", started.Turn.ID, turns.Data)
	}
	stoppedBeforeProjection := daemon.mutationSnapshot(t)
	interruptBeforeProjection := stoppedBeforeProjection.Journal[interrupt.ClientMutationID]
	t.Logf("before SESSION_END projection: turns=%v interrupt=%#v fence=%s", waiterInterruptTurnStatuses(turns.Data), interruptBeforeProjection, stoppedBeforeProjection.InterruptFence)
	if len(stoppedBeforeProjection.InterruptFence) != 0 || interruptBeforeProjection.OperationState != "terminal" ||
		interruptBeforeProjection.ExecutionState != "interrupted" {
		t.Fatalf("interrupt was not durably terminal before projection: %#v", stoppedBeforeProjection)
	}
	daemon.releaseInterruptedSessionEnd()
	awaitWaiterInterruptSignal(daemon.ctx, t, daemon.sessionEndProjected, "interrupted SESSION_END projection")
	turns, err = daemon.client.ThreadTurnsList(daemon.ctx, appwire.ThreadTurnsListParams{Ref: daemon.ref})
	if err != nil {
		t.Fatalf("ThreadTurnsList after SESSION_END projection: %v", err)
	}
	terminal = false
	for _, turn := range turns.Data {
		if turn.ID == started.Turn.ID {
			terminal = turn.Status == "interrupted"
		}
	}
	if !terminal {
		t.Fatalf("turn %q was not terminalized after SESSION_END projection: %#v", started.Turn.ID, turns.Data)
	}
	t.Logf("after SESSION_END projection: turns=%v", waiterInterruptTurnStatuses(turns.Data))
	if err := daemon.client.TurnInterrupt(daemon.ctx, interrupt); err != nil {
		t.Fatalf("replay terminal interrupt: %v", err)
	}

	// The interrupt response is not enough by itself: read the durable mutation
	// snapshot that future client mutations are serialized against. The runner
	// must have terminalized the fence before the RPC returned.
	if !stopped.QueueHeld || !stopped.SteeringHeld {
		t.Fatalf("Stop did not park both input rails before re-engagement: queue=%t steering=%t", stopped.QueueHeld, stopped.SteeringHeld)
	}

	// Queueing is a user re-engagement and clears both durable held gates in the
	// same accepted mutation. Reading the snapshot again proves later client
	// mutations are no longer trapped behind the completed interrupt.
	if err := daemon.client.TurnQueue(daemon.ctx, appwire.TurnQueueParams{
		ClientMutationID: "reengage-after-stop",
		Ref:              daemon.ref,
		Input:            []appwire.InputItem{{Type: "text", Text: "resume after stop"}},
	}); err != nil {
		t.Fatalf("TurnQueue after interrupt: %v", err)
	}
	reengaged := daemon.mutationSnapshot(t)
	if reengaged.QueueHeld || reengaged.SteeringHeld {
		t.Fatalf("re-engagement left a held input rail: queue=%t steering=%t", reengaged.QueueHeld, reengaged.SteeringHeld)
	}
}
