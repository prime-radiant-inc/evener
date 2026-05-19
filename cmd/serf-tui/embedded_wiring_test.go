package main

// Wire-level tests for the embedded daemon's queue + drainAsSteer +
// queueWithImages plumbing (kata vxsk). Mirror the wiring exercised by
// cmd/serf/serve.go so the TUI's embedded-server mode (no auto-start
// hub) supports the same mutating actions the standalone serf serve
// hub does.
//
// These tests build a minimal *embeddedServer shell against a real
// *agent.Session backed by a no-op LLM adapter, then exercise the
// public HTTP + appwire surface. They intentionally avoid startEmbedded
// so they can run without API credentials.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/server"
)

// embeddedPNGSig is a tiny PNG header used as opaque image bytes.
var embeddedPNGSig = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// newWiredEmbeddedForTest builds an *embeddedServer with a real session
// backed by a noop LLM adapter, then runs the production wireSession
// path. The returned httptest.Server fronts the embedded server's mux
// so tests can hit /status, /queue, /drain-as-steer over real HTTP.
func newWiredEmbeddedForTest(t *testing.T) (*embeddedServer, *httptest.Server, *agent.Session) {
	t.Helper()
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&embeddedNoopAdapter{name: "openai"})
	profile := agent.NewOpenAIProfile("gpt-test")
	env := agent.NewLocalExecutionEnvironment(dir)
	sess, err := agent.NewSession(c, profile, env, agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())

	e := &embeddedServer{
		srv:        srv,
		sess:       sess,
		client:     c,
		profile:    profile,
		env:        env,
		sessionCfg: agent.SessionConfig{StateDir: dir},
	}
	e.wireSession(sess)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		sess.Close()
	})
	return e, ts, sess
}

// TestEmbeddedDaemon_QueueCapability asserts that once a turn is in
// flight, the embedded daemon's /status response advertises
// capabilities.queue=true. RED until wireSession wires SetQueueFunc
// on the embedded server (kata vxsk).
func TestEmbeddedDaemon_QueueCapability(t *testing.T) {
	e, ts, _ := newWiredEmbeddedForTest(t)

	// Sanity: queue capability is false while idle.
	if cap := getQueueCapability(t, ts.URL); cap {
		t.Fatalf("capabilities.queue=true while idle; want false")
	}

	// Force the in-flight-turn state and re-check. With queueFunc nil
	// the projection stays false; with queueFunc set we expect true.
	e.srv.SetProcessing(true)

	if cap := getQueueCapability(t, ts.URL); !cap {
		t.Fatalf("capabilities.queue=false while processing; want true (embedded daemon must call SetQueueFunc in wireSession)")
	}
}

// TestEmbeddedDaemon_DrainAsSteerCapability asserts that POST
// /drain-as-steer reports the drain function as wired. Without
// SetDrainAsSteerFunc the endpoint returns 503; once wired with the
// queue empty it returns 409 ("queue is empty").
func TestEmbeddedDaemon_DrainAsSteerCapability(t *testing.T) {
	e, ts, _ := newWiredEmbeddedForTest(t)
	e.srv.SetProcessing(true)

	resp, err := http.Post(ts.URL+"/drain-as-steer", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /drain-as-steer: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("drain-as-steer returned 503 %q; embedded daemon must call SetDrainAsSteerFunc in wireSession", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("drain-as-steer status=%d body=%q; want 409 queue-is-empty", resp.StatusCode, string(body))
	}
}

func TestEmbeddedDaemon_InputLoopWiresInterruptForLiveTurn(t *testing.T) {
	dir := t.TempDir()
	adapter := &embeddedBlockingAdapter{name: "openai", started: make(chan struct{}), done: make(chan error, 1)}
	c := llm.NewClient()
	c.Register(adapter)
	profile := agent.NewOpenAIProfile("gpt-test")
	env := agent.NewLocalExecutionEnvironment(dir)
	sess, err := agent.NewSession(c, profile, env, agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	e := &embeddedServer{
		srv:        srv,
		sess:       sess,
		client:     c,
		profile:    profile,
		env:        env,
		sessionCfg: agent.SessionConfig{StateDir: dir},
	}
	e.wireSession(sess)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	defer sess.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.inputLoop(ctx)

	resp, err := http.Post(ts.URL+"/input", "application/json", strings.NewReader(`{"text":"block"}`))
	if err != nil {
		t.Fatalf("POST /input: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("input status=%d", resp.StatusCode)
	}

	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not start")
	}

	resp, err = http.Post(ts.URL+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /interrupt: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("interrupt status=%d", resp.StatusCode)
	}

	select {
	case err := <-adapter.done:
		if err == nil {
			t.Fatal("adapter completed without cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not observe turn cancellation")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err = http.Post(ts.URL+"/interrupt", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /interrupt after turn: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interrupt after turn status=%d, want eventual 503", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEmbeddedDaemon_InterruptedQueueDrainStaysProcessingAndInterruptible(t *testing.T) {
	dir := t.TempDir()
	adapter := &embeddedMultiBlockingAdapter{
		name:    "openai",
		started: make(chan struct{}, 2),
		done:    make(chan error, 2),
	}
	c := llm.NewClient()
	c.Register(adapter)
	profile := agent.NewOpenAIProfile("gpt-test")
	env := agent.NewLocalExecutionEnvironment(dir)
	sess, err := agent.NewSession(c, profile, env, agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	e := &embeddedServer{
		srv:        srv,
		sess:       sess,
		client:     c,
		profile:    profile,
		env:        env,
		sessionCfg: agent.SessionConfig{StateDir: dir},
	}
	e.wireSession(sess)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	defer sess.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.inputLoop(ctx)

	resp, err := http.Post(ts.URL+"/input", "application/json", strings.NewReader(`{"text":"block"}`))
	if err != nil {
		t.Fatalf("POST /input: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("input status=%d", resp.StatusCode)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not start")
	}
	if err := sess.Enqueue(context.Background(), "queued drain"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	resp, err = http.Post(ts.URL+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatalf("POST first interrupt: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first interrupt status=%d", resp.StatusCode)
	}
	select {
	case err := <-adapter.done:
		if err == nil {
			t.Fatal("first turn completed without cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first turn was not canceled")
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("queued drained turn did not start")
	}

	resp, err = http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	var status server.StatusInfo
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		resp.Body.Close()
		t.Fatalf("decode status: %v", err)
	}
	resp.Body.Close()
	if status.State != "active" || status.Capabilities.Send {
		t.Fatalf("status during drained turn = %+v, want processing with send disabled", status)
	}

	resp, err = http.Post(ts.URL+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatalf("POST second interrupt: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second interrupt status=%d", resp.StatusCode)
	}
	select {
	case err := <-adapter.done:
		if err == nil {
			t.Fatal("drained turn completed without cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drained turn was not canceled by second interrupt")
	}
}

// TestEmbeddedDaemon_QueueWithImagesRoundTrip drives an appwire
// turn/queue request carrying an image attachment, then a
// turn/drainAsSteer, and verifies the underlying agent.Session's
// steering queue ended up holding the image bytes. RED until
// wireSession wires SetQueueWithImagesFunc, SetDrainAsSteerFunc, and
// SetQueueDepthFunc on the embedded server.
func TestEmbeddedDaemon_QueueWithImagesRoundTrip(t *testing.T) {
	e, _, sess := newWiredEmbeddedForTest(t)
	e.srv.SetProcessing(true)

	conn := e.srv.AppServer().NewConnection("test")
	ctx := context.Background()

	if r := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{})); r.Kind() != appwire.MessageResponse {
		raw, _ := json.Marshal(r)
		t.Fatalf("initialize: %s", raw)
	}
	if r := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref: "local:" + sess.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "drain me"}, {
			Type:      "image",
			MediaType: "image/png",
			Data:      embeddedPNGSig,
			Name:      "d.png",
		}},
	})); r.Kind() != appwire.MessageResponse {
		raw, _ := json.Marshal(r)
		t.Fatalf("turn/queue: %s (embedded daemon must call SetQueueWithImagesFunc in wireSession)", raw)
	}
	if r := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref: "local:" + sess.ID(),
	})); r.Kind() != appwire.MessageResponse {
		raw, _ := json.Marshal(r)
		t.Fatalf("turn/drainAsSteer: %s (embedded daemon must call SetDrainAsSteerFunc + SetQueueDepthFunc in wireSession)", raw)
	}

	steering := sess.SteeringQueueSnapshot()
	if len(steering) != 1 {
		t.Fatalf("steering queue: got %d, want 1", len(steering))
	}
	if !strings.Contains(steering[0].Text, "drain me") {
		t.Errorf("steering text=%q, want contains %q", steering[0].Text, "drain me")
	}
	if len(steering[0].Images) != 1 || !bytes.Equal(steering[0].Images[0].Data, embeddedPNGSig) {
		t.Errorf("steering image not preserved: %+v", steering[0].Images)
	}
}

// getQueueCapability reads /status from baseURL and returns the
// capabilities.queue boolean.
func getQueueCapability(t *testing.T, baseURL string) bool {
	t.Helper()
	resp, err := http.Get(baseURL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	var status struct {
		Capabilities struct {
			Queue bool `json:"queue"`
		} `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	return status.Capabilities.Queue
}

// embeddedNoopAdapter is a minimal llm.ProviderAdapter shim used to
// build sessions in these wiring tests without API credentials. The
// session never executes a turn here, so Complete is unused.
type embeddedNoopAdapter struct{ name string }

func (a *embeddedNoopAdapter) Name() string { return a.name }
func (a *embeddedNoopAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("noop")}, nil
}
func (a *embeddedNoopAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type embeddedBlockingAdapter struct {
	name    string
	started chan struct{}
	done    chan error
}

func (a *embeddedBlockingAdapter) Name() string { return a.name }
func (a *embeddedBlockingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	close(a.started)
	<-ctx.Done()
	err := ctx.Err()
	a.done <- err
	return llm.Response{Provider: a.name, Model: req.Model}, err
}
func (a *embeddedBlockingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type embeddedMultiBlockingAdapter struct {
	name    string
	started chan struct{}
	done    chan error
}

func (a *embeddedMultiBlockingAdapter) Name() string { return a.name }
func (a *embeddedMultiBlockingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.started <- struct{}{}
	<-ctx.Done()
	err := ctx.Err()
	a.done <- err
	return llm.Response{Provider: a.name, Model: req.Model}, err
}
func (a *embeddedMultiBlockingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
