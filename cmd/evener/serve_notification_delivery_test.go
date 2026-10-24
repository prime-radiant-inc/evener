package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
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
	"primeradiant.com/evener/server"
)

type notificationDeliveryProvider struct {
	t                                     *testing.T
	mu                                    sync.Mutex
	rootCalls                             int
	heldQuestion                          bool
	childEntered, childRelease, delivered chan struct{}
	once                                  sync.Once
}

func (*notificationDeliveryProvider) Name() string { return "openai" }
func (*notificationDeliveryProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p *notificationDeliveryProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if response, ok := scriptedSessionNamerResponse(p.Name(), req); ok {
		return response, nil
	}
	p.t.Logf("provider model=%s", req.Model)
	if req.Model == "child-notification-test" {
		close(p.childEntered)
		select {
		case <-p.childRelease:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return scriptedCommunicate("report-payload-cf391"), nil
	}
	p.mu.Lock()
	p.rootCalls++
	n := p.rootCalls
	p.mu.Unlock()
	if n == 1 {
		args, _ := json.Marshal(map[string]any{"prompt": "child-task-f173", "model": "openai/child-notification-test"})
		return scriptedToolCalls(llm.ToolCallData{ID: "create-child-71d", Name: "delegate", Type: "function", Arguments: args}), nil
	}
	if n == 2 && p.heldQuestion {
		return scriptedToolCalls(scriptedAskUserCall("ask-notification-17af", "Report", "May I continue?", serveAskOption{Label: "Yes", Detail: "read the report"}, serveAskOption{Label: "No", Detail: "wait"})), nil
	}
	if strings.Contains(requestDeliveredText(req), "report-payload-cf391") {
		p.once.Do(func() { close(p.delivered) })
	}
	return scriptedCommunicate("result-7e19"), nil
}

type notificationDeliveryServer struct {
	*server.Server
	passFinished, releasePass, wakeSubmitted chan struct{}
	passOnce, wakeOnce                       sync.Once
	processing                               atomic.Bool
}

func (s *notificationDeliveryServer) SetProcessingTurn(id string) {
	s.processing.Store(true)
	s.Server.SetProcessingTurn(id)
}

func (s *notificationDeliveryServer) SetProcessing(v bool) {
	s.Server.SetProcessing(v)
	if v {
		s.processing.Store(true)
	}
	if !v && s.processing.Load() {
		s.passOnce.Do(func() { close(s.passFinished); <-s.releasePass })
	}
}
func (s *notificationDeliveryServer) SubmitNotification() {
	s.Server.SubmitNotification()
	s.wakeOnce.Do(func() { close(s.wakeSubmitted) })
}

func TestServeNotificationDelivery(t *testing.T) {
	t.Run("occupied input slot", func(t *testing.T) { testServeNotificationDelivery(t, false) })
	t.Run("pending question", func(t *testing.T) { testServeNotificationDelivery(t, true) })
}

func testServeNotificationDelivery(t *testing.T, heldQuestion bool) {
	p := &notificationDeliveryProvider{t: t, heldQuestion: heldQuestion, childEntered: make(chan struct{}), childRelease: make(chan struct{}), delivered: make(chan struct{})}
	d := defaultServeDeps()
	d.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
			server.BridgeEvent(s.(*notificationDeliveryServer).Server, ev, observer)
		}, onDrained)
	}
	d.subscriberCount = func(s serveServer, id string) int {
		return s.(*notificationDeliveryServer).AppSubscriberCount(id)
	}
	d.ensureConfigDirs = func() error { return nil }
	d.seedMarketplaces = func(context.Context) error { return nil }
	d.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		c := llm.NewClient()
		c.Register(p)
		return c, func() error { return nil }, nil
	}
	var root *agent.Session
	d.newSession = func(c *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 0}
		s, err := agent.NewSession(c, profile, env, cfg)
		root = s
		return s, err
	}
	ready := make(chan *notificationDeliveryServer, 1)
	d.newServer = func(cfg server.ServerConfig) serveServer {
		s := &notificationDeliveryServer{Server: server.NewServer(cfg), passFinished: make(chan struct{}), releasePass: make(chan struct{}), wakeSubmitted: make(chan struct{})}
		ready <- s
		return s
	}
	runDir, dir, stateDir := t.TempDir(), t.TempDir(), t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- runServeWithDeps([]string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", dir, "--state-dir", stateDir, "--run-dir", runDir}, d)
	}()
	entry := waitForServeTestRendezvous(t, runDir)
	s := <-ready
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "notification-test", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	var childOnce, passOnce sync.Once
	releaseChild := func() { childOnce.Do(func() { close(p.childRelease) }) }
	releasePass := func() { passOnce.Do(func() { close(s.releasePass) }) }
	t.Cleanup(func() {
		releaseChild()
		releasePass()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := shutdownServeTestDaemon(cleanupCtx, entry.Address, entry.SessionID); err != nil {
			t.Error(err)
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-cleanupCtx.Done():
			t.Error(cleanupCtx.Err())
		}
	})
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// Submit real user work through the same server input channel as the daemon.
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{Ref: appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String(), ExpectedInstanceID: entry.SessionID, ClientMutationID: "root-38de", Input: []appwire.InputItem{{Type: "text", Text: "start-root-38de"}}}); err != nil {
		t.Fatal(err)
	}
	await(p.childEntered)
	await(s.passFinished)
	if heldQuestion {
		// The actual asking turn has returned. Accept the report while its
		// question holds execution, then answer through the normal input RPC.
		releaseChild()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			body, err := os.ReadFile(root.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "report-payload-cf391") {
				break
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatal("question hold prevented receiver custody")
			}
		}
		if root.State() != agent.SessionAwaiting {
			t.Fatalf("held receiver state=%s, want awaiting", root.State())
		}
		p.mu.Lock()
		calls := p.rootCalls
		p.mu.Unlock()
		if calls != 2 {
			t.Fatalf("question hold allowed %d root requests, want 2", calls)
		}
		releasePass()
		if _, err := client.TurnStart(ctx, appwire.TurnStartParams{Ref: appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String(), ExpectedInstanceID: entry.SessionID, ClientMutationID: "answer-71af", Input: []appwire.InputItem{{Type: "text", Text: "[answers]\n1. [Report] → \"Yes\""}}}); err != nil {
			t.Fatal(err)
		}
		await(p.delivered)
		return
	}
	// The loop is now held after its input pass, with no Session executor.
	// Fill its single input slot, then accept an actual child's report.
	s.SubmitContinuation("occupant-7de1")
	releaseChild()
	await(s.wakeSubmitted)
	releasePass()
	await(p.delivered)
}
