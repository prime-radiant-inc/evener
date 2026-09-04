package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

type closedStreamAdapter struct {
	mu            sync.Mutex
	streamCalls   int
	completeCalls int
}

func (a *closedStreamAdapter) Name() string { return "openai" }

func (a *closedStreamAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if response, ok := scriptedSessionNamerResponse(a.Name(), req); ok {
		return response, nil
	}
	a.mu.Lock()
	a.completeCalls++
	a.mu.Unlock()
	return llm.Response{}, nil
}

func (a *closedStreamAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	a.mu.Lock()
	a.streamCalls++
	a.mu.Unlock()
	stream := llm.NewChanStream(nil)
	stream.CloseSend()
	return stream, nil
}

func (a *closedStreamAdapter) calls() (stream, complete int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.streamCalls, a.completeCalls
}

type idlePublicationServer struct {
	*server.Server

	mu             sync.Mutex
	sawProcessing  bool
	sawNotProcess  bool
	idlePublished  chan struct{}
	publishedState string
	publishOnce    sync.Once
}

func newIdlePublicationServer(cfg server.ServerConfig) *idlePublicationServer {
	return &idlePublicationServer{
		Server:        server.NewServer(cfg),
		idlePublished: make(chan struct{}),
	}
}

func (s *idlePublicationServer) SetProcessing(processing bool) {
	s.Server.SetProcessing(processing)
	s.observeProcessing(processing)
}

func (s *idlePublicationServer) SetProcessingTurn(turnID string) {
	s.Server.SetProcessingTurn(turnID)
	s.observeProcessing(true)
}

func (s *idlePublicationServer) observeProcessing(processing bool) {
	s.mu.Lock()
	if processing {
		s.sawProcessing = true
	} else if s.sawProcessing {
		s.sawNotProcess = true
	}
	s.mu.Unlock()
}

func (s *idlePublicationServer) SetState(state string) {
	s.Server.SetState(state)
	s.mu.Lock()
	postTurn := s.sawProcessing && s.sawNotProcess
	if postTurn {
		s.publishedState = state
	}
	s.mu.Unlock()
	if postTurn {
		s.publishOnce.Do(func() { close(s.idlePublished) })
	}
}

func (s *idlePublicationServer) postTurnState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishedState
}

type sessionControlIdentityServer struct {
	*server.Server

	mu                   sync.Mutex
	processing           bool
	processingStarted    chan struct{}
	releaseProcessing    chan struct{}
	processingFinished   chan struct{}
	userInputProjected   chan struct{}
	interruptEntered     chan struct{}
	processingStartOnce  sync.Once
	processingFinishOnce sync.Once
	userInputOnce        sync.Once
	interruptOnce        sync.Once
	releaseOnce          sync.Once
}

func newSessionControlIdentityServer(cfg server.ServerConfig) *sessionControlIdentityServer {
	return &sessionControlIdentityServer{
		Server:             server.NewServer(cfg),
		processingStarted:  make(chan struct{}),
		releaseProcessing:  make(chan struct{}),
		processingFinished: make(chan struct{}),
		userInputProjected: make(chan struct{}),
		interruptEntered:   make(chan struct{}),
	}
}

func (s *sessionControlIdentityServer) SetState(state string) {
	s.Server.SetState(state)
	if state != string(agent.SessionProcessing) {
		return
	}
	s.mu.Lock()
	s.processing = true
	s.mu.Unlock()
	s.processingStartOnce.Do(func() { close(s.processingStarted) })
	<-s.releaseProcessing
}

func (s *sessionControlIdentityServer) SetProcessing(processing bool) {
	s.Server.SetProcessing(processing)
	if processing {
		return
	}
	s.mu.Lock()
	finishing := s.processing
	s.processing = false
	s.mu.Unlock()
	if finishing {
		s.processingFinishOnce.Do(func() { close(s.processingFinished) })
	}
}

func (s *sessionControlIdentityServer) SetRetrySafeTurnFunctions(functions server.RetrySafeTurnFunctions) {
	interrupt := functions.Interrupt
	functions.Interrupt = func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		s.interruptOnce.Do(func() { close(s.interruptEntered) })
		return interrupt(ctx, params)
	}
	s.Server.SetRetrySafeTurnFunctions(functions)
}

func (s *sessionControlIdentityServer) release() {
	s.releaseOnce.Do(func() { close(s.releaseProcessing) })
}

type sessionControlLifecycle struct {
	server *sessionControlIdentityServer
	client *appwire.Client
	ctx    context.Context
	ref    string
}

func startSessionControlLifecycle(t *testing.T) *sessionControlLifecycle {
	t.Helper()
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client := llm.NewClient()
		client.Register(&closedStreamAdapter{})
		return client, func() error { return nil }, nil
	}
	deps.newSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 0}
		return agent.NewSession(client, profile, env, cfg)
	}
	var observedServer *sessionControlIdentityServer
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		observedServer = newSessionControlIdentityServer(cfg)
		return observedServer
	}
	deps.bridge = func(_ serveServer, session *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		session.ConsumeEventsLossless(func(ev events.SessionEvent) {
			server.BridgeEvent(observedServer.Server, ev, observer)
			if ev.Kind == events.EventUserInput {
				observedServer.userInputOnce.Do(func() { close(observedServer.userInputProjected) })
			}
		}, onDrained)
	}
	deps.subscriberCount = func(_ serveServer, id string) int {
		return observedServer.AppServer().SubscriberCount(id)
	}

	runDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- runServeWithDeps([]string{
			"--model", "openai/gpt-test",
			"--addr", "127.0.0.1:0",
			"--dir", t.TempDir(),
			"--state-dir", t.TempDir(),
			"--run-dir", runDir,
		}, deps)
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		cancel()
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "session-control-test", Version: "test"},
	}); err != nil {
		client.Close()
		cancel()
		t.Fatalf("Initialize: %v", err)
	}

	lifecycle := &sessionControlLifecycle{
		server: observedServer,
		client: client,
		ctx:    ctx,
		ref:    appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String(),
	}
	t.Cleanup(func() {
		observedServer.release()
		client.Close()
		if err := shutdownServeTestDaemon(ctx, entry.Address, entry.SessionID); err != nil {
			return
		}
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
	return lifecycle
}

func awaitSessionControlLifecycle(t *testing.T, lifecycle *sessionControlLifecycle, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-lifecycle.ctx.Done():
		t.Fatalf("%s: %v", label, lifecycle.ctx.Err())
	}
}

func startHeldClientMutationTurn(t *testing.T, lifecycle *sessionControlLifecycle, mutationID, text string) appwire.TurnStartResponse {
	t.Helper()
	response, err := lifecycle.client.TurnStart(lifecycle.ctx, appwire.TurnStartParams{
		ClientMutationID:   mutationID,
		ExpectedInstanceID: strings.TrimPrefix(lifecycle.ref, "local:"),
		Ref:                lifecycle.ref,
		Input:              []appwire.InputItem{{Type: "text", Text: text}},
	})
	if err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	awaitSessionControlLifecycle(t, lifecycle, lifecycle.server.processingStarted, "processing start")
	return response
}

func readSessionControlThread(t *testing.T, lifecycle *sessionControlLifecycle, includeTurns bool) appwire.Thread {
	t.Helper()
	response, err := lifecycle.client.ThreadRead(lifecycle.ctx, appwire.ThreadReadParams{
		Ref:          lifecycle.ref,
		IncludeTurns: includeTurns,
	})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	return response.Thread
}

func TestRunServeRetrySafeTurnPublishesControllableStableIdentity(t *testing.T) {
	t.Run("steer and incorporate", func(t *testing.T) {
		lifecycle := startSessionControlLifecycle(t)
		lifecycle.server.RecordAppEvent(events.SessionEvent{
			Kind:      events.EventUserInput,
			SessionID: strings.TrimPrefix(lifecycle.ref, "local:"),
			Data:      events.UserInputData{Text: "stale projected turn"},
		})
		staleProjectedTurnID := readSessionControlThread(t, lifecycle, false).Evener.ActiveTurnID
		if staleProjectedTurnID == "" {
			t.Fatal("failed to prime a stale projected active turn")
		}
		start := startHeldClientMutationTurn(t, lifecycle, "stable-start", "pending user input")
		thread := readSessionControlThread(t, lifecycle, false)
		activeTurnID := thread.Evener.ActiveTurnID
		if activeTurnID == "" {
			t.Fatal("thread/read published no active turn while processing")
		}
		if err := lifecycle.client.TurnSteer(lifecycle.ctx, appwire.TurnSteerParams{
			ClientMutationID:   "stable-steer",
			ExpectedInstanceID: strings.TrimPrefix(lifecycle.ref, "local:"),
			Ref:                lifecycle.ref,
			Input:              []appwire.InputItem{{Type: "text", Text: "steer accepted"}},
		}); err != nil {
			t.Fatalf("TurnSteer with published active ID %q: %v", activeTurnID, err)
		}
		if activeTurnID != start.Turn.ID {
			t.Fatalf("published active turn = %q, durable start turn = %q", activeTurnID, start.Turn.ID)
		}

		lifecycle.server.RecordAppEvent(events.SessionEvent{
			Kind:      events.EventWarning,
			SessionID: strings.TrimPrefix(lifecycle.ref, "local:"),
			Data:      events.WarningData{Message: "pre-input projection"},
		})
		if afterWarning := readSessionControlThread(t, lifecycle, false).Evener.ActiveTurnID; afterWarning != start.Turn.ID {
			t.Fatalf("active turn after intervening projection = %q, want %q", afterWarning, start.Turn.ID)
		}

		lifecycle.server.release()
		awaitSessionControlLifecycle(t, lifecycle, lifecycle.server.userInputProjected, "user input projection")
		awaitSessionControlLifecycle(t, lifecycle, lifecycle.server.processingFinished, "processing finish")
		projected := readSessionControlThread(t, lifecycle, true)
		for _, turn := range projected.Turns {
			for _, item := range turn.Items {
				if item.Type == "userMessage" && item.ClientMutationID == "stable-start" && item.Text == "pending user input" {
					return
				}
			}
		}
		t.Fatalf("projected turns do not contain incorporated pending input: %#v", projected.Turns)
	})

	t.Run("stop", func(t *testing.T) {
		lifecycle := startSessionControlLifecycle(t)
		start := startHeldClientMutationTurn(t, lifecycle, "stop-start", "stop this turn")
		activeTurnID := readSessionControlThread(t, lifecycle, false).Evener.ActiveTurnID
		if activeTurnID == "" {
			t.Fatal("thread/read published no active turn while processing")
		}
		stopDone := make(chan error, 1)
		go func() {
			stopDone <- lifecycle.client.TurnInterrupt(lifecycle.ctx, appwire.TurnInterruptParams{
				ClientMutationID:   "stable-stop",
				ExpectedInstanceID: strings.TrimPrefix(lifecycle.ref, "local:"),
				Ref:                lifecycle.ref,
			})
		}()
		awaitSessionControlLifecycle(t, lifecycle, lifecycle.server.interruptEntered, "interrupt handler entry")
		lifecycle.server.release()
		select {
		case err := <-stopDone:
			if err != nil {
				t.Fatalf("TurnInterrupt with published active ID %q: %v", activeTurnID, err)
			}
		case <-lifecycle.ctx.Done():
			t.Fatalf("TurnInterrupt: %v", lifecycle.ctx.Err())
		}
		if activeTurnID != start.Turn.ID {
			t.Fatalf("published active turn = %q, durable start turn = %q", activeTurnID, start.Turn.ID)
		}
		awaitSessionControlLifecycle(t, lifecycle, lifecycle.server.processingFinished, "processing finish")
		if status := readSessionControlThread(t, lifecycle, false).Status.Type; status != appwire.ThreadStatusIdle {
			t.Fatalf("thread status after Stop = %q, want idle", status)
		}
		if activeTurnID := readSessionControlThread(t, lifecycle, false).Evener.ActiveTurnID; activeTurnID != "" {
			t.Fatalf("active turn after Stop = %q, want empty", activeTurnID)
		}
	})
}

// TestRunServe_StreamErrorPublishesIdleStatus proves the real serve input loop
// publishes owning Session state after an exhausted streaming failure. The
// wrapper observes the production true -> false -> SetState boundary while
// forwarding every state mutation to the real AppWire server projection.
func TestRunServe_StreamErrorPublishesIdleStatus(t *testing.T) {
	adapter := &closedStreamAdapter{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, func() error { return nil }, nil
	}
	deps.newSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 0}
		return agent.NewSession(client, profile, env, cfg)
	}
	var observedServer *idlePublicationServer
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		observedServer = newIdlePublicationServer(cfg)
		return observedServer
	}
	// The dep must return once attached and drain on its own goroutine; see
	// serveDeps.bridge.
	deps.bridge = func(_ serveServer, session *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		go func() {
			defer onDrained()
			server.BridgeWithObserver(observedServer.Server, session.Events(), observer)
		}()
	}
	deps.subscriberCount = func(_ serveServer, id string) int {
		return observedServer.AppServer().SubscriberCount(id)
	}

	runDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- runServeWithDeps([]string{
			"--model", "openai/gpt-test",
			"--addr", "127.0.0.1:0",
			"--dir", t.TempDir(),
			"--state-dir", t.TempDir(),
			"--run-dir", runDir,
		}, deps)
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-state-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "stream-error-turn",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "trigger a closed stream"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	select {
	case <-observedServer.idlePublished:
	case <-ctx.Done():
		t.Fatalf("post-turn state publication: %v", ctx.Err())
	}

	if got := observedServer.postTurnState(); got != string(agent.SessionIdle) {
		t.Fatalf("published post-turn state = %q, want %q", got, agent.SessionIdle)
	}
	if got := observedServer.GetStatus().State; got != string(agent.SessionIdle) {
		t.Fatalf("stored server state = %q, want %q", got, agent.SessionIdle)
	}
	streamCalls, completeCalls := adapter.calls()
	if streamCalls != 1 || completeCalls != 0 {
		t.Fatalf("adapter calls = Stream %d, Complete %d; want Stream 1, Complete 0", streamCalls, completeCalls)
	}

	thread, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if thread.Thread.Status.Type != appwire.ThreadStatusIdle || !thread.Thread.Evener.Capabilities.Send || thread.Thread.Evener.Capabilities.Queue || thread.Thread.Evener.Capabilities.Interrupt {
		t.Fatalf("thread/read = status %q, capabilities %+v; want idle, send enabled, queue and interrupt disabled", thread.Thread.Status.Type, thread.Thread.Evener.Capabilities)
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServeWithDeps: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServeWithDeps did not exit after shutdown")
	}
}

// TestHoldServeStateForAwaitingWake proves holdServeStateForAwaitingWake mirrors
// the session-level entry gate's refusal predicate (agent/session_lifecycle.go's
// `len(s.askPending) > 0 && kind != EntryUserInput`, spec §5.3): the input loop
// must hold its status shadow write for exactly the (kind, hasPendingAsk) pairs
// where ProcessInputKind will refuse before any state transition, and flip as
// before everywhere else. Keyed on hasPendingAsk rather than raw SessionState
// (attention-status-model v5 reconciliation): a session generally awaiting with
// no pending ask must NOT be held — async wakes re-arm by design there — only a
// genuine pending question is a stronger stop than the wake.
func TestHoldServeStateForAwaitingWake(t *testing.T) {
	cases := []struct {
		name          string
		kind          agent.EntryKind
		hasPendingAsk bool
		want          bool
	}{
		{"notification wake with a pending ask is held", agent.EntryNotification, true, true},
		{"continuation wake with a pending ask is held", agent.EntryContinuation, true, true},
		{"delegate attention with a pending ask is held", agent.EntryDelegateAttention, true, true},
		{"user input with a pending ask is not held (resolves the question)", agent.EntryUserInput, true, false},
		{"notification wake with no pending ask is not held (general awaiting re-arms freely)", agent.EntryNotification, false, false},
		{"user input with no pending ask is not held", agent.EntryUserInput, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdServeStateForAwaitingWake(tc.kind, tc.hasPendingAsk); got != tc.want {
				t.Errorf("holdServeStateForAwaitingWake(%v, %v) = %v, want %v", tc.kind, tc.hasPendingAsk, got, tc.want)
			}
		})
	}
}

// errClearProbe is the injected failure the thread/clear tests drive their fallible
// steps with. It is deliberately not a sentinel any production path inspects:
// the contract under test is "any failure leaves the old identity intact",
// not "this particular error does".
var errClearProbe = errors.New("clear probe failure")

// oldClearInputMarker is written into the OLD thread's snapshot before thread/clear
// runs, so a replacement that leaks the previous authority into the new thread
// is visible in thread/read rather than merely inferable.
const oldClearInputMarker = "OLD-THREAD-INPUT"

// clearTestState records what thread/clear did, in order, across the real daemon
// server and the injected serveDeps. Every step it records is a production
// call site; nothing here stands in for Evener internals.
type clearTestState struct {
	mu       sync.Mutex
	steps    []string
	sessions []*agent.Session

	// failPrepareFrom makes the Nth prepareAppIdentity call (1-based) and every
	// later one fail. The daemon prepares once at startup, so a clear-time
	// failure is call 2.
	failPrepareFrom int
	prepareCalls    int
	failRendezvous  bool

	srv *clearIdentityServer
}

func (s *clearTestState) record(step string) {
	s.mu.Lock()
	s.steps = append(s.steps, step)
	s.mu.Unlock()
}

func (s *clearTestState) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steps...)
}

func (s *clearTestState) addSession(sess *agent.Session) {
	s.mu.Lock()
	s.sessions = append(s.sessions, sess)
	s.mu.Unlock()
}

func (s *clearTestState) session(i int) *agent.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.sessions) {
		return nil
	}
	return s.sessions[i]
}

// clearIdentityServer is the real daemon server with the identity-install calls
// teed into an ordered log. Every method forwards, so the AppWire projection,
// the notifier and thread/read all run against the same Server production uses.
type clearIdentityServer struct {
	*server.Server

	state    *clearTestState
	clear    func(context.Context, appwire.ThreadClearParams) error
	jobs     func(appwire.JobsListParams) (any, error)
	shutdown func()
	// envelopeSource is the one seam the daemon reads live session state
	// through. Capturing it lets this test observe WHICH session the daemon
	// resolves after thread/clear, which is what the meta callback used to prove.
	envelopeSource server.ThreadEnvelopeSource
}

func (s *clearIdentityServer) ReplaceAppIdentity(prepared server.PreparedAppIdentity, activate func()) {
	s.state.record("replace")
	if activate != nil {
		inner := activate
		activate = func() {
			s.state.record("activate")
			inner()
		}
	}
	s.Server.ReplaceAppIdentity(prepared, activate)
}

func (s *clearIdentityServer) SetClearFunc(fn func(context.Context, appwire.ThreadClearParams) error) {
	s.clear = fn
	s.Server.SetClearFunc(fn)
}

func (s *clearIdentityServer) SetJobsFunc(fn func(appwire.JobsListParams) (any, error)) {
	s.jobs = fn
	s.Server.SetJobsFunc(fn)
}

func (s *clearIdentityServer) SetShutdownFunc(fn func()) {
	s.shutdown = fn
	s.Server.SetShutdownFunc(fn)
}

func (s *clearIdentityServer) SetThreadEnvelopeSource(src server.ThreadEnvelopeSource) {
	s.envelopeSource = src
	s.Server.SetThreadEnvelopeSource(src)
}

// newClearServeDeps builds a real serve run whose LLM boundary is scripted and
// whose identity-swap steps are observable. Sessions, execution environments,
// transcripts, the rendezvous file and the AppWire server are all real.
func newClearServeDeps(t *testing.T) (serveDeps, *clearTestState, []string) {
	t.Helper()
	state := &clearTestState{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client := llm.NewClient()
		client.Register(&closedStreamAdapter{})
		return client, func() error { return nil }, nil
	}
	newSession := func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		sess, err := agent.NewSession(client, profile, env, cfg)
		if err == nil {
			state.addSession(sess)
		}
		return sess, err
	}
	deps.newSession = newSession
	deps.newClearSession = newSession
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		state.srv = &clearIdentityServer{Server: server.NewServer(cfg), state: state}
		return state.srv
	}
	deps.bridge = func(_ serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		go func() {
			defer onDrained()
			server.BridgeWithObserver(state.srv.Server, sess.Events(), observer)
		}()
	}
	deps.subscriberCount = func(_ serveServer, id string) int {
		return state.srv.AppServer().SubscriberCount(id)
	}
	prepare := deps.prepareAppIdentity
	deps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		state.mu.Lock()
		state.prepareCalls++
		fail := state.failPrepareFrom > 0 && state.prepareCalls >= state.failPrepareFrom
		state.mu.Unlock()
		state.record("prepare")
		if fail {
			return server.PreparedAppIdentity{}, errClearProbe
		}
		return prepare(sourceID, threadID, ref, transcriptPath)
	}
	updateSessionID := deps.updateSessionID
	deps.updateSessionID = func(reg *rvreg.Registration, id string) error {
		state.record("rendezvous")
		state.mu.Lock()
		fail := state.failRendezvous
		state.mu.Unlock()
		if fail {
			return errClearProbe
		}
		return updateSessionID(reg, id)
	}
	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--run-dir", t.TempDir(),
		"--no-project-prompts",
	}
	return deps, state, args
}

// clearObservation is everything sampled while the serve run is still live.
// runServeWithDeps closes the current session on its way out, so the session
// states in particular have to be read before it returns.
type clearObservation struct {
	steps            []string
	clearErr         error
	oldSessionID     string
	newSessionID     string
	currentSessionID string
	statusSessionID  string
	oldSessionState  agent.SessionState
	newSessionState  agent.SessionState
	oldThreadClosed  int
	threadResync     int
	threadID         string
	leakedOldTurn    bool
}

// runClearAttempt runs one real serve, drives thread/clear through the daemon's own
// clear callback, and samples the result. sample runs after the attempt with the
// serve loop still live, for assertions the teardown would erase.
func runClearAttempt(t *testing.T, deps serveDeps, state *clearTestState, args []string, sample func(*clearObservation)) clearObservation {
	t.Helper()
	var obs clearObservation
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		oldSess := state.session(0)
		obs.oldSessionID = oldSess.ID()
		// Put a turn on the OLD thread so a snapshot that survives replacement
		// shows up as content rather than as an identifier mismatch.
		state.srv.RecordAppEvent(events.SessionEvent{
			Kind:      events.EventUserInput,
			SessionID: oldSess.ID(),
			Data:      events.UserInputData{Text: oldClearInputMarker},
		})

		before := len(state.recorded())
		obs.clearErr = state.srv.clear(context.Background(), appwire.ThreadClearParams{
			Ref:                "local:" + obs.oldSessionID,
			ClientMutationID:   "clear-test",
			ExpectedInstanceID: obs.oldSessionID,
		})
		obs.steps = state.recorded()[before:]

		if state.srv.envelopeSource != nil {
			obs.currentSessionID = state.srv.envelopeSource.SessionMeta().ID
		}
		obs.statusSessionID = state.srv.GetStatus().SessionID
		obs.oldSessionState = oldSess.State()
		if newSess := state.session(1); newSess != nil {
			obs.newSessionID = newSess.ID()
			obs.newSessionState = newSess.State()
		}
		notificationThreadID := obs.oldSessionID
		if obs.newSessionID != "" {
			notificationThreadID = obs.newSessionID
		}
		for _, record := range state.srv.AppNotificationsAfter(0, notificationThreadID) {
			if record.Notification.Method == appwire.NotifyThreadClosed {
				obs.oldThreadClosed++
			}
			if record.Notification.Method == appwire.NotifyEvenerThreadResync {
				obs.threadResync++
			}
		}
		read := clearThreadRead(t, state.srv)
		obs.threadID = read.Thread.ID
		obs.leakedOldTurn = threadCarriesText(read, oldClearInputMarker)
		if sample != nil {
			sample(&obs)
		}
		state.srv.shutdown()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("runServeWithDeps: %v", err)
	}
	return obs
}

func clearThreadRead(t *testing.T, srv *clearIdentityServer) appwire.ThreadReadResponse {
	t.Helper()
	conn := srv.AppServer().NewConnection("clear-identity-test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1),
		appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion},
	))
	msg := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(2),
		appwire.MethodThreadRead,
		appwire.ThreadReadParams{IncludeTurns: true},
	))
	if msg.Response == nil {
		t.Fatalf("thread/read produced no response: %+v", msg)
	}
	out, ok := msg.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read result = %T (%+v)", msg.Response.Result, msg)
	}
	return out
}

func threadCarriesText(read appwire.ThreadReadResponse, text string) bool {
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if strings.Contains(item.Text, text) {
				return true
			}
		}
	}
	return false
}

// assertClearKeptOldIdentity is the shared failure contract: a /clear that
// cannot complete every fallible step must leave the daemon exactly as it was,
// with nothing published about the swap it abandoned.
func assertClearKeptOldIdentity(t *testing.T, obs clearObservation) {
	t.Helper()
	if !errors.Is(obs.clearErr, errClearProbe) {
		t.Fatalf("clear error = %v, want %v", obs.clearErr, errClearProbe)
	}
	if obs.currentSessionID != obs.oldSessionID {
		t.Errorf("current session = %q, want the old session %q", obs.currentSessionID, obs.oldSessionID)
	}
	if obs.statusSessionID != obs.oldSessionID {
		t.Errorf("status session = %q, want the old session %q", obs.statusSessionID, obs.oldSessionID)
	}
	if obs.threadID != obs.oldSessionID {
		t.Errorf("thread/read thread = %q, want the old session %q", obs.threadID, obs.oldSessionID)
	}
	if !obs.leakedOldTurn {
		t.Error("thread/read lost the old thread's turn after an aborted clear")
	}
	if obs.oldThreadClosed != 0 {
		t.Errorf("old thread received %d thread/closed record(s) after an aborted clear, want 0", obs.oldThreadClosed)
	}
	if obs.oldSessionState == agent.SessionClosed {
		t.Error("old session was closed by an aborted clear")
	}
	if obs.newSessionID == "" {
		t.Fatal("clear never built a replacement session")
	}
	if obs.newSessionState != agent.SessionClosed {
		t.Errorf("abandoned replacement session state = %q, want %q (its environment is disposed with it)", obs.newSessionState, agent.SessionClosed)
	}
	for _, step := range obs.steps {
		if step == "replace" || step == "activate" {
			t.Fatalf("aborted clear installed an identity: steps = %v", obs.steps)
		}
	}
}

// TestRunServeClearPreparationFailureKeepsOldIdentity drives the fallible half
// of the swap -- projecting the new session's transcript -- into failure and
// requires the daemon to abandon the replacement rather than publish half of it.
func TestRunServeClearPreparationFailureKeepsOldIdentity(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	state.failPrepareFrom = 2
	obs := runClearAttempt(t, deps, state, args, nil)
	assertClearKeptOldIdentity(t, obs)
	if len(obs.steps) == 0 || obs.steps[0] != "prepare" {
		t.Fatalf("clear steps = %v, want preparation attempted before any shared state moved", obs.steps)
	}
	for _, step := range obs.steps {
		if step == "rendezvous" {
			t.Fatalf("clear updated the rendezvous after preparation failed: steps = %v", obs.steps)
		}
	}
}

// TestRunServeClearRendezvousFailureKeepsOldIdentity fails the last fallible
// step. Everything before it succeeded, so this is the case an activate-first
// sequence has to undo -- and an undo that emits a thread/closed on the way
// through has already told every subscriber the thread ended.
func TestRunServeClearRendezvousFailureKeepsOldIdentity(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	state.failRendezvous = true
	obs := runClearAttempt(t, deps, state, args, nil)
	assertClearKeptOldIdentity(t, obs)
	if len(obs.steps) < 2 || obs.steps[0] != "prepare" || obs.steps[1] != "rendezvous" {
		t.Fatalf("clear steps = %v, want prepare then rendezvous before any install", obs.steps)
	}
}

// TestRunServeClearSuccessClosesOldStreamAndInstallsPreparedIdentity pins the
// successful order: every fallible step first, then one infallible replacement
// that swaps the live session and closes the old thread's stream in the same
// projection commit.
func TestRunServeClearSuccessClosesOldStreamAndInstallsPreparedIdentity(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var lateStatus string
	var lateThreadID string
	var lateLeak bool
	var jobsErr error
	var jobsTree appwire.JobActivityTree
	obs := runClearAttempt(t, deps, state, args, func(obs *clearObservation) {
		// A straggling event from the session thread/clear just replaced must not
		// reach the new authority. This is the real bridge over a channel the
		// test owns, so the ordering is fixed rather than raced.
		stale := make(chan events.SessionEvent, 1)
		stale <- events.SessionEvent{
			Kind:      events.EventSessionEnd,
			SessionID: obs.oldSessionID,
			Data:      events.SessionEndData{Reason: "clear", State: "closed"},
		}
		close(stale)
		server.BridgeWithObserver(state.srv.Server, stale, nil)
		status := state.srv.GetStatus()
		lateStatus = status.State
		read := clearThreadRead(t, state.srv)
		lateThreadID = read.Thread.ID
		lateLeak = threadCarriesText(read, oldClearInputMarker)
		data, err := state.srv.jobs(appwire.JobsListParams{Ref: "local:" + obs.oldSessionID})
		jobsErr = err
		if tree, ok := data.(appwire.JobActivityTree); ok {
			jobsTree = tree
		}
	})

	if obs.clearErr != nil {
		t.Fatalf("clear: %v", obs.clearErr)
	}
	want := []string{"prepare", "rendezvous", "replace", "activate"}
	if !slices.Equal(obs.steps, want) {
		t.Fatalf("clear steps = %v, want %v", obs.steps, want)
	}
	if obs.newSessionID == "" || obs.newSessionID == obs.oldSessionID {
		t.Fatalf("replacement session = %q, old session = %q", obs.newSessionID, obs.oldSessionID)
	}
	if obs.currentSessionID != obs.newSessionID {
		t.Errorf("current session = %q, want the replacement %q", obs.currentSessionID, obs.newSessionID)
	}
	if obs.statusSessionID != obs.newSessionID {
		t.Errorf("status session = %q, want the replacement %q", obs.statusSessionID, obs.newSessionID)
	}
	if obs.threadID != obs.newSessionID {
		t.Errorf("thread/read thread = %q, want the replacement %q", obs.threadID, obs.newSessionID)
	}
	if obs.leakedOldTurn {
		t.Error("thread/read served the old thread's turn out of the replacement snapshot")
	}
	if obs.oldThreadClosed != 0 {
		t.Errorf("replacement received %d thread/closed record(s), want 0", obs.oldThreadClosed)
	}
	if obs.threadResync != 1 {
		t.Errorf("replacement received %d evener/thread/resync record(s), want exactly 1", obs.threadResync)
	}
	if obs.oldSessionState != agent.SessionClosed {
		t.Errorf("old session state = %q, want %q after replacement", obs.oldSessionState, agent.SessionClosed)
	}
	if lateThreadID != obs.newSessionID || lateLeak {
		t.Errorf("a late old-session event changed the snapshot: thread %q, old turn present %v", lateThreadID, lateLeak)
	}
	if lateStatus == string(agent.SessionClosed) {
		t.Error("a late old-session event closed the replacement's status")
	}
	if jobsErr != nil {
		t.Fatalf("jobs for the stable workspace ref after clear: %v", jobsErr)
	}
	if jobsTree.Root.SessionID != obs.newSessionID {
		t.Fatalf("jobs root session = %q, want replacement %q", jobsTree.Root.SessionID, obs.newSessionID)
	}
}

// TestClearSeedsTheReplacementEnvelopeBeforeItsBridgeRuns pins the seed that
// follows /clear's identity commit.
//
// ReplaceAppIdentity zeroes the envelope in the same commit as the identity, so
// between that commit and the replacement session's first drained event there is
// a window where the daemon knows the new thread's id and nothing else about it.
// serve closes it by re-seeding immediately. Without that call a client reading
// in the window gets a thread with no diagnostics, no reasoning settings and no
// title -- for a session that has all three.
//
// The window is held open by construction rather than raced for: the cleared
// session's bridge is replaced with one that never drains, so the ONLY thing
// that can have populated the envelope is the seed.
func TestClearSeedsTheReplacementEnvelopeBeforeItsBridgeRuns(t *testing.T) {
	deps, state, args := newClearServeDeps(t)

	var bridged int
	var bridgeMu sync.Mutex
	deps.bridge = func(_ serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		bridgeMu.Lock()
		bridged++
		n := bridged
		bridgeMu.Unlock()
		if n > 1 {
			// The replacement session's events are never drained, so nothing
			// after the identity commit can refresh the envelope.
			onDrained()
			return
		}
		go func() {
			defer onDrained()
			server.BridgeWithObserver(state.srv.Server, sess.Events(), observer)
		}()
	}

	var diagnostics bool
	var newID string
	obs := runClearAttempt(t, deps, state, args, func(*clearObservation) {
		newSess := state.session(1)
		if newSess == nil {
			return
		}
		newID = newSess.ID()
		conn := state.srv.AppServer().NewConnection("clear-seed")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(
			appwire.NewIntID(1), appwire.MethodInitialize,
			appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
			appwire.NewIntID(2), appwire.MethodThreadRead,
			appwire.ThreadReadParams{Ref: "local:" + newID}))
		read, ok := resp.Response.Result.(appwire.ThreadReadResponse)
		if !ok {
			return
		}
		diagnostics = read.Thread.Evener.Diagnostics != nil
	})
	if obs.clearErr != nil {
		t.Fatalf("clear: %v", obs.clearErr)
	}
	if newID == "" {
		t.Fatal("clear produced no replacement session; the fixture proves nothing")
	}
	if !diagnostics {
		t.Fatal("thread/read carries no diagnostics for the replacement session: the envelope was zeroed with the identity and never re-seeded")
	}
}

// TestStartupSeedsTheEnvelopeBeforeTheBridgeRuns is the same pin for the other
// seed. The daemon installs its identity before any event has been drained, so
// without an explicit seed the first client to attach -- which is what opening a
// session URL does -- reads a thread with no diagnostics, no reasoning settings
// and no title, for a session that has all three.
//
// Bridging is suppressed entirely, so SESSION_START can never arrive and the
// seed is the only thing that can populate the envelope.
func TestStartupSeedsTheEnvelopeBeforeTheBridgeRuns(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		onDrained()
	}

	var diagnostics bool
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		sess := state.session(0)
		if sess == nil {
			state.srv.shutdown()
			return http.ErrServerClosed
		}
		conn := state.srv.AppServer().NewConnection("startup-seed")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(
			appwire.NewIntID(1), appwire.MethodInitialize,
			appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
			appwire.NewIntID(2), appwire.MethodThreadRead,
			appwire.ThreadReadParams{Ref: "local:" + sess.ID()}))
		read, ok := resp.Response.Result.(appwire.ThreadReadResponse)
		if !ok {
			state.srv.shutdown()
			return http.ErrServerClosed
		}
		diagnostics = read.Thread.Evener.Diagnostics != nil
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !diagnostics {
		t.Fatal("the first thread/read carries no diagnostics: the daemon installed its identity without seeding the envelope")
	}
}
