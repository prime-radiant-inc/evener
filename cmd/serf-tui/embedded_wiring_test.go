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
		Ref:  "local:" + sess.ID(),
		Text: "drain me",
		Items: []appwire.InputItem{{
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
