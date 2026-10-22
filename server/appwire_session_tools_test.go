package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestSessionToolsRealConnectionBinding(t *testing.T) {
	t.Parallel()
	session := newNotesTestSession(t)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", session.WorkspaceID())
	srv.SetSessionTools(func() *agent.Session { return session })
	client := dialServerAppWire(t, srv)
	binding := appwire.SessionBinding{Ref: "local:" + session.WorkspaceID(), InstanceID: session.ID(), HubID: "hub-test", BindingID: "connection-test"}
	var bound appwire.SessionBinding
	forged := binding
	forged.InstanceID = "another-instance"
	if err := client.Request(context.Background(), appwire.MethodRuntimeSessionBind, forged, &bound); err == nil {
		t.Fatal("forged runtime identity accepted")
	}
	if err := client.Request(context.Background(), appwire.MethodRuntimeSessionBind, binding, &bound); err != nil {
		t.Fatalf("bind discovered live runtime over real connection: %v", err)
	}
	if bound != binding {
		t.Fatalf("binding = %+v, want %+v", bound, binding)
	}
	if err := client.Request(context.Background(), appwire.MethodRuntimeSessionBind, binding, &bound); err == nil {
		t.Fatal("duplicate connection binding accepted")
	}
}

func TestSessionToolsLiveBindingCannotBeDisplaced(t *testing.T) {
	model := &bindingProbeAdapter{}
	modelClient := llm.NewClient()
	modelClient.Register(model)
	dir := t.TempDir()
	session, err := agent.NewSession(modelClient, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", session.WorkspaceID())
	srv.SetSessionTools(func() *agent.Session { return session })
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	binding := appwire.SessionBinding{Ref: "local:" + session.WorkspaceID(), InstanceID: session.ID(), HubID: "hub-a", BindingID: "first"}
	claimed := binding
	claimed.BindingID = "second"
	firstOwner := &bindingProbeOwner{}
	secondOwner := &bindingProbeOwner{}
	dial := func(binding appwire.SessionBinding, owner appwire.SessionHubClient) *appwire.Client {
		t.Helper()
		transport, err := appwire.DialWebSocket(t.Context(), "ws"+httpServer.URL[len("http"):]+"/rpc", httpServer.Client())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = transport.Close() })
		client := appwire.NewClient(transport)
		client.SetSessionTools(binding, owner)
		client.Start(t.Context())
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		return client
	}
	first := dial(binding, firstOwner)
	second := dial(claimed, secondOwner)
	if err := first.BindSessionTools(t.Context()); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if err := second.BindSessionTools(t.Context()); err == nil {
		t.Fatal("second client displaced a live binding")
	} else {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("second bind = %v, want conflict", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := session.ProcessInput(ctx, "use Manager discovery", nil); err != nil {
		t.Fatalf("original binding no longer serves Manager request: %v", err)
	}
	if firstOwner.states.Load() == 0 || firstOwner.invokes.Load() != 1 {
		t.Fatalf("original socket State/Invoke counts = %d/%d, model calls = %d, tool calls = %d", firstOwner.states.Load(), firstOwner.invokes.Load(), model.calls.Load(), model.toolCalls.Load())
	}
	if secondOwner.states.Load() != 0 || secondOwner.invokes.Load() != 0 {
		t.Fatalf("rejected socket served State/Invoke: %d/%d", secondOwner.states.Load(), secondOwner.invokes.Load())
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first socket: %v", err)
	}
	// A closed socket is the legitimate reconnect case. Its old binding must
	// not permanently reserve the hub ID.
	deadline := time.After(5 * time.Second)
	for {
		err := second.BindSessionTools(t.Context())
		if err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("reconnect never replaced closed socket: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type bindingProbeOwner struct {
	states  atomic.Int32
	invokes atomic.Int32
}

func (o *bindingProbeOwner) State(context.Context) (appwire.ManagerView, error) {
	o.states.Add(1)
	return appwire.ManagerView{Setting: appwire.ManagerSetting{Enabled: true, HubID: "hub-a"}}, nil
}
func (o *bindingProbeOwner) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	o.invokes.Add(1)
	return appwire.SessionOperationResponse{Discover: &appwire.SessionDiscovery{}}, nil
}
func (*bindingProbeOwner) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, errors.New("watch not expected")
}

type bindingProbeAdapter struct {
	calls     atomic.Int32
	toolCalls atomic.Int32
}

func (*bindingProbeAdapter) Name() string { return "openai" }
func (*bindingProbeAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *bindingProbeAdapter) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	a.calls.Add(1)
	if len(request.Tools) == 0 {
		return llm.Response{Provider: a.Name(), Model: request.Model, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "test session"}}}}, nil
	}
	name, args := "job_list", json.RawMessage(`{"hub":{"mode":"discovery"}}`)
	if a.toolCalls.Add(1) > 1 {
		name, args = "communicate", json.RawMessage(`{"message":"done","end_turn":true}`)
	}
	call := llm.ToolCallData{ID: name, Name: name, Arguments: args, Type: "function"}
	return llm.Response{Provider: a.Name(), Model: request.Model, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, nil
}

type authorityState struct{ view appwire.ManagerView }

func (s *authorityState) State(context.Context) (appwire.ManagerView, error) { return s.view, nil }
func (*authorityState) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (*authorityState) Watch(ctx context.Context, _ appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	out := make(chan appwire.SessionWatchUpdate)
	go func() { <-ctx.Done(); close(out) }()
	return out, nil
}

func TestSessionToolsAuthoritySelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session := newNotesTestSession(t)
	clients := &runtimeHubClients{clients: map[string]*runtimeHubClient{}}
	states := map[string]*authorityState{}
	for _, hub := range []string{"hub-a", "hub-b"} {
		life, cancel := context.WithCancel(t.Context())
		defer cancel()
		state := &authorityState{view: appwire.ManagerView{Setting: appwire.ManagerSetting{HubID: hub}}}
		states[hub] = state
		clients.clients[hub] = &runtimeHubClient{current: func() *agent.Session { return session }, session: session, binding: appwire.SessionBinding{InstanceID: session.ID()}, client: state, ctx: life, cancel: cancel}
	}
	states["hub-a"].view.Setting.Enabled = true
	states["hub-b"].view.Setting.Enabled = true
	if _, err := clients.State(t.Context()); err == nil {
		t.Fatal("conflicting enabled hubs selected by connection order")
	}
	states["hub-b"].view.Setting.Enabled = false
	view, err := clients.State(t.Context())
	if err != nil || view.Setting.HubID != "hub-a" {
		t.Fatalf("human-enabled authority=%+v err=%v", view, err)
	}
	updates, err := clients.Watch(t.Context(), appwire.SessionWatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	clients.clients["hub-a"].cancel()
	states["hub-b"].view.Setting.Enabled = true
	if _, err := clients.State(t.Context()); err == nil {
		t.Fatal("disconnected authority silently switched hubs")
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("replaced binding delivered observation")
		}
	case <-ctx.Done():
		t.Fatal("replaced binding did not cancel observation")
	}
}

func TestSessionToolsConflictingEnabledHubAfterSelection(t *testing.T) {
	session := newNotesTestSession(t)
	clients := &runtimeHubClients{clients: map[string]*runtimeHubClient{}}
	states := map[string]*authorityState{}
	for _, hub := range []string{"hub-a", "hub-b"} {
		life, cancel := context.WithCancel(t.Context())
		defer cancel()
		state := &authorityState{view: appwire.ManagerView{Setting: appwire.ManagerSetting{HubID: hub}}}
		states[hub] = state
		clients.clients[hub] = &runtimeHubClient{current: func() *agent.Session { return session }, session: session, binding: appwire.SessionBinding{InstanceID: session.ID()}, client: state, ctx: life, cancel: cancel}
	}
	states["hub-a"].view.Setting.Enabled = true
	if view, err := clients.State(t.Context()); err != nil || view.Setting.HubID != "hub-a" {
		t.Fatalf("first selection = %+v, %v", view, err)
	}
	states["hub-b"].view.Setting.Enabled = true
	assertConflict := func(operation string, err error) {
		t.Helper()
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict || wire.Message != "multiple hubs claim Manager authority" {
			t.Fatalf("%s with newly enabled B = %v, want authority conflict", operation, err)
		}
	}
	_, err := clients.State(t.Context())
	assertConflict("State", err)
	_, err = clients.Invoke(t.Context(), appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{}})
	assertConflict("Invoke", err)
}
