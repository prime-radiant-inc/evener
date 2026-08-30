package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

// stopParkAdapter is the model seam for the daemon-level Stop test. Its first
// call blocks until its context is cancelled, which is how a turn is held
// mid-model-round for a Stop to land on; every call announces itself on
// modelCalls first, so a turn the daemon should never have started is
// observable rather than merely slow.
type stopParkAdapter struct {
	modelCalls chan string
	mu         sync.Mutex
	calls      int
}

func newStopParkAdapter() *stopParkAdapter {
	// Buffered: an unexpected call must be RECORDED, not blocked. A blocked
	// send would hold the very turn the test is trying to catch.
	return &stopParkAdapter{modelCalls: make(chan string, 8)}
}

func (a *stopParkAdapter) Name() string { return "openai" }

func (a *stopParkAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if response, ok := scriptedSessionNamerResponse(a.Name(), req); ok {
		return response, nil
	}
	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()
	last := ""
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1].Text()
	}
	a.modelCalls <- last
	if n == 1 {
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Provider: "openai", Model: req.Model, Message: llm.Assistant("done")}, nil
}

func (a *stopParkAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// stopParkDaemon is a real serve loop, a real server, and a real AppWire client
// over a websocket: the whole daemon seam the accept-time queued-input wake runs
// through.
type stopParkDaemon struct {
	adapter *stopParkAdapter
	client  *appwire.Client
	ctx     context.Context
	ref     string
}

func startStopParkDaemon(t *testing.T) *stopParkDaemon {
	t.Helper()
	adapter := newStopParkAdapter()
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, func() error { return nil }, nil
	}
	deps.newSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 0}
		return agent.NewSession(client, profile, env, cfg)
	}
	var observedServer *server.Server
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		observedServer = server.NewServer(cfg)
		return observedServer
	}
	deps.bridge = func(_ serveServer, session *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		session.ConsumeEventsLossless(func(ev events.SessionEvent) {
			server.BridgeEvent(observedServer, ev, observer)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		cancel()
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "stop-park-test", Version: "test"},
	}); err != nil {
		client.Close()
		cancel()
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() {
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
	return &stopParkDaemon{
		adapter: adapter,
		client:  client,
		ctx:     ctx,
		ref:     appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String(),
	}
}

func (d *stopParkDaemon) awaitModelCall(t *testing.T, label string) string {
	t.Helper()
	select {
	case text := <-d.adapter.modelCalls:
		return text
	case <-d.ctx.Done():
		t.Fatalf("%s: %v", label, d.ctx.Err())
		return ""
	}
}

func (d *stopParkDaemon) queueDepth(t *testing.T) int {
	t.Helper()
	response, err := d.client.ThreadRead(d.ctx, appwire.ThreadReadParams{Ref: d.ref})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	return response.Thread.Evener.Queue.Depth
}

// stopParkObservationWindow bounds the "nothing auto-started" check. The wake it
// has to outlast was already delivered to the serve loop before the Stop RPC
// returned, so the loop acts on it within microseconds of the turn settling;
// this is three orders of magnitude past that, and it is only ever spent when
// the test is about to pass.
const stopParkObservationWindow = 2 * time.Second

// TestRunServeStopParksTheQueuedMessageUntilTheUserActs is wms7's ruling at the
// daemon seam. The session-level half of it (a Stop leaves the queue head
// parked instead of draining it into a follow-on turn) is pinned in
// agent/session_stop_and_queued_work_test.go. This is the other half: the
// daemon parked a queued-input wake when the message was ACCEPTED, and that
// wake outlives the Stop -- so the serve loop ran the very message the user
// stopped as soon as the interrupted turn settled.
func TestRunServeStopParksTheQueuedMessageUntilTheUserActs(t *testing.T) {
	daemon := startStopParkDaemon(t)

	if _, err := daemon.client.TurnStart(daemon.ctx, appwire.TurnStartParams{
		ClientMutationID:   "turn-one",
		ExpectedInstanceID: strings.TrimPrefix(daemon.ref, "local:"),
		Ref:                daemon.ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "first message"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if first := daemon.awaitModelCall(t, "first model call"); !strings.Contains(first, "first message") {
		t.Fatalf("first model call carried %q, want the started message", first)
	}

	// The collision: a second message queued while turn one is mid-model-call.
	// Accepting it is what arms the daemon's queued-input wake.
	if err := daemon.client.TurnQueue(daemon.ctx, appwire.TurnQueueParams{
		ClientMutationID:   "queued-behind-stop",
		ExpectedInstanceID: strings.TrimPrefix(daemon.ref, "local:"),
		Ref:                daemon.ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "run me later"}},
	}); err != nil {
		t.Fatalf("TurnQueue: %v", err)
	}

	if err := daemon.client.TurnInterrupt(daemon.ctx, appwire.TurnInterruptParams{
		ClientMutationID:   "stop-mid-turn",
		ExpectedInstanceID: strings.TrimPrefix(daemon.ref, "local:"),
		Ref:                daemon.ref,
	}); err != nil {
		t.Fatalf("TurnInterrupt: %v", err)
	}

	// The Stop RPC returns only once the interrupted turn has settled, so the
	// serve loop has already had its chance to act on the parked wake by the
	// time this window opens: it bounds the tail of a race that has mostly been
	// run, not the whole of one.
	select {
	case text := <-daemon.adapter.modelCalls:
		t.Fatalf("the daemon ran %q after the Stop; the message the user stopped must stay parked until the user acts", text)
	case <-time.After(stopParkObservationWindow):
	}
	if depth := daemon.queueDepth(t); depth != 1 {
		t.Fatalf("queue depth after the stop = %d, want the message still parked", depth)
	}

	// The park ends when the user acts. Queueing another message is one of the
	// ordinary ways to say "carry on": the parked head runs first, FIFO.
	if err := daemon.client.TurnQueue(daemon.ctx, appwire.TurnQueueParams{
		ClientMutationID:   "queued-after-stop",
		ExpectedInstanceID: strings.TrimPrefix(daemon.ref, "local:"),
		Ref:                daemon.ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "and me too"}},
	}); err != nil {
		t.Fatalf("TurnQueue after the stop: %v", err)
	}
	if next := daemon.awaitModelCall(t, "the parked message after the user acted"); !strings.Contains(next, "run me later") {
		t.Fatalf("the turn the user unparked carried %q, want the message parked by the Stop", next)
	}
}
