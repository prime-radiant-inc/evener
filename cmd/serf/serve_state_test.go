package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/server"
)

type closedStreamAdapter struct {
	mu            sync.Mutex
	streamCalls   int
	completeCalls int
}

func (a *closedStreamAdapter) Name() string { return "openai" }

func (a *closedStreamAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
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

// TestRunServe_StreamErrorPublishesIdleStatus proves the real serve input loop
// publishes owning Session state after an exhausted streaming failure. The
// wrapper observes the production true -> false -> SetState boundary while
// forwarding every state mutation to the real server used by both HTTP and
// AppWire projections.
func TestRunServe_StreamErrorPublishesIdleStatus(t *testing.T) {
	adapter := &closedStreamAdapter{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
		client := llm.NewClient()
		client.Register(adapter)
		cfg := providercfg.Config{
			Default: "openai",
			Instances: []providercfg.InstanceConfig{
				{Name: "openai", Type: "openai"},
			},
		}
		return client, cfg, true, func() error { return nil }, nil
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
	deps.bridge = func(_ serveServer, session *agent.Session, observer func(events.SessionEvent)) {
		server.BridgeWithObserver(observedServer.Server, session.Events(), observer)
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
		ClientMutationID: "stream-error-turn",
		Ref:              ref,
		Input:            []appwire.InputItem{{Type: "text", Text: "trigger a closed stream"}},
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

	statusResp, err := http.Get("http://" + entry.Address + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer statusResp.Body.Close()
	var status server.StatusInfo
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	if status.State != string(agent.SessionIdle) || !status.Capabilities.Send || status.Capabilities.Queue || status.Capabilities.Interrupt {
		t.Fatalf("/status = state %q, capabilities %+v; want idle, send enabled, queue and interrupt disabled", status.State, status.Capabilities)
	}

	thread, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if thread.Thread.Status.Type != appwire.ThreadStatusIdle || !thread.Thread.Serf.Capabilities.Send || thread.Thread.Serf.Capabilities.Queue {
		t.Fatalf("thread/read = status %q, capabilities %+v; want idle, send enabled, queue disabled", thread.Thread.Status.Type, thread.Thread.Serf.Capabilities)
	}

	shutdownResp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	shutdownResp.Body.Close()
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
// must hold its /status shadow write for exactly the (kind, hasPendingAsk) pairs
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
		{"watch delivery with a pending ask is held", agent.EntryWatchDelivery, true, true},
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
