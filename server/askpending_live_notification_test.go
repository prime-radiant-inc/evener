package server

// TestAskUserLiveStatusFrameCarriesAskPending pins a production invariant that
// had no end-to-end coverage: when a live ask_user round ends the turn, the
// accompanying thread/status/changed("awaiting") notification carries
// AskPending=true. RoboRev flagged this as missing on #1740 (internal/
// appprojector/appwire_projection.go's threadStatus() helper never sets the
// field), which is true but not the whole picture: the projector is not
// supposed to set it (stampAskPendingOnStatusChange's own doc comment — "the
// projector maps events to notifications and holds no session handle, and
// the flag lives on the envelope"); the SERVER stamps it afterward from the
// live session, at every notification egress. This test drives a real
// agent.Session through a real ask_user round, bridged into a real *Server
// the way cmd/evener/serve.go wires it (ConsumeEventsLossless -> BridgeEvent,
// a ThreadEnvelopeSource whose AskPending() reads the session directly), and
// reads back the actual recorded notification to prove the stamp lands.
import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// oneShotAskUserAdapter answers the first model round that offers ask_user as
// a tool with a single ask_user call, and a bare final response (no tool
// calls) on any other round — including a toolless round the harness makes
// for reasons unrelated to the ask_user round itself (e.g. session naming).
type oneShotAskUserAdapter struct {
	name string

	mu   sync.Mutex
	used bool
}

func (a *oneShotAskUserAdapter) Name() string { return a.name }

func (a *oneShotAskUserAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	offersAskUser := false
	for _, td := range req.Tools {
		if td.Name == "ask_user" {
			offersAskUser = true
			break
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used || !offersAskUser {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	a.used = true
	args, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"header":   "Direction",
				"question": "Which way?",
				"options": []map[string]any{
					{"label": "North", "detail": "up"},
					{"label": "South", "detail": "down"},
				},
			},
		},
	})
	return llm.Response{
		Provider: a.name,
		Model:    req.Model,
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "ask_user",
					Arguments: args,
					Type:      "function",
				},
			}},
		},
	}, nil
}

func (a *oneShotAskUserAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// sessionAskPendingEnvelopeSource is stubThreadEnvelopeSource with exactly one
// facet backed by a real session: AskPending. Every other facet stays at its
// stub zero value, so nothing else in the recorded frame can be mistaken for
// live session state this test did not set up.
type sessionAskPendingEnvelopeSource struct {
	stubThreadEnvelopeSource
	sess *agent.Session
}

func (s *sessionAskPendingEnvelopeSource) AskPending() bool {
	return s.sess.HasPendingAsk()
}

func TestAskUserLiveStatusFrameCarriesAskPending(t *testing.T) {
	dir := t.TempDir()
	adapter := &oneShotAskUserAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := agent.NewSession(c, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	srv := NewServer(ServerConfig{AppReplaySize: 100})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetThreadEnvelopeSource(&sessionAskPendingEnvelopeSource{sess: sess})

	drained := make(chan struct{})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		BridgeEvent(srv, ev, nil)
	}, func() { close(drained) })

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which way should we go?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.HasPendingAsk(); !got {
		t.Fatalf("live HasPendingAsk() = %v, want true (test setup broken)", got)
	}

	// Close drains and closes the events channel once every in-flight emitter
	// has finished; waiting on drained is the deterministic point at which
	// the bridge goroutine has processed everything ProcessInput emitted,
	// including the EventSessionEnd this round's ask boundary settles on.
	sess.Close()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("event bridge never drained")
	}

	var awaiting *appwire.ThreadStatusChangedParams
	for _, s := range statusNotifications(t, srv, sess.ID()) {
		if s.Status.Type == appwire.ThreadStatusAwaiting {
			awaiting = &s
			break
		}
	}
	if awaiting == nil {
		t.Fatal("no thread/status/changed(awaiting) was recorded for the ask_user round ending")
	}
	if awaiting.AskPending == nil {
		t.Fatal("the live thread/status/changed(awaiting) frame carries no AskPending at all")
	}
	if !*awaiting.AskPending {
		t.Fatal("the live thread/status/changed(awaiting) frame says AskPending=false, want true")
	}
}
