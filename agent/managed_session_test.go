package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

type managedFixture struct {
	afterExecute func()
	result       *ManagedResult

	mu                                   sync.Mutex
	sessions                             []ManagedSession
	requests                             []ManagedRequest
	prepareErr, executeErr, authorizeErr error
	closed                               int
}

func (f *managedFixture) Catalog() ([]ManagedTool, error) {
	return []ManagedTool{{Definition: llm.ToolDefinition{Name: "managed_write", Description: "Managed test operation", Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "number"}}, "required": []string{"n"}, "additionalProperties": false}}, Operation: "write", Validate: func(raw json.RawMessage) error {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
		if len(args) != 1 || args["n"] == nil {
			return errors.New("invalid arguments")
		}
		return nil
	}}}, nil
}
func (f *managedFixture) Bind(s ManagedSession) (ManagedBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, s)
	return f, nil
}
func (f *managedFixture) Prepare(_ context.Context, c ManagedCall) (ManagedRequest, error) {
	return ManagedRequest{Identity: ManagedIdentity{BindingID: "logical-binding", ServiceID: "backend", RealmID: "realm", PrincipalID: "principal", NamespaceID: "namespace"}, Operation: c.Operation, MutationID: c.MutationID, Arguments: append(json.RawMessage(nil), c.Arguments...)}, f.prepareErr
}
func (f *managedFixture) Authorize(context.Context, ManagedRequest) error { return f.authorizeErr }
func (f *managedFixture) Execute(_ context.Context, r ManagedRequest) (ManagedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if f.afterExecute != nil {
		f.afterExecute()
	}
	if f.result != nil {
		return *f.result, f.executeErr
	}
	if f.executeErr != nil {
		return ManagedResult{}, f.executeErr
	}
	return ManagedResult{ModelText: "model-sentinel", Host: &llm.MCPResult{Version: 1, Origin: llm.MCPOrigin{BindingID: r.Identity.BindingID, ServiceID: r.Identity.ServiceID, SessionID: f.sessions[len(f.sessions)-1].SessionID}, StructuredContent: json.RawMessage(`{"private":"host-sentinel"}`)}}, nil
}
func (f *managedFixture) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}
func managedCallStep(llm.Request) llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "same-provider-call", Name: "managed_write", Arguments: json.RawMessage(`{ "n":9007199254740993 }`)}}}}}
}
func TestManagedSessionDistinctOccurrencesAndDurableHostResults(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}), withSteps(managedCallStep, managedCallStep, managedDoneStep))
	if len(f.requests) != 0 || len(f.sessions) != 1 || f.sessions[0].SessionID != s.ID() {
		t.Fatalf("construction acquired or misbound: %+v", f)
	}
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[0].MutationID == f.requests[1].MutationID || string(f.requests[0].Arguments) != `{ "n":9007199254740993 }` {
		t.Fatalf("occurrences=%+v", f.requests)
	}
	if len(s.managedJournal.pending()) != 0 {
		t.Fatal("durable results not settled")
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, e := range full.Entries {
		for _, p := range e.Turn.Message.Content {
			if p.ToolResult != nil && p.ToolResult.Name == "managed_write" {
				r := p.ToolResult
				if r.MCPResult == nil || r.ManagedModelText != "model-sentinel" || r.ManagedInvocationID == "" {
					t.Fatalf("persisted result=%+v", r)
				}
				ids[r.ManagedInvocationID] = true
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("distinct durable occurrences=%v", ids)
	}
	s.Close()
	if f.closed != 1 {
		t.Fatalf("binding closes=%d", f.closed)
	}
}
func TestManagedSessionUnavailableDoesNotBlockOrdinaryTool(t *testing.T) {
	f := &managedFixture{prepareErr: ErrManagedUnavailable}
	s := newSession(t, withConfig(SessionConfig{ManagedRuntime: f}))
	s.RegisterTool("ordinary", "fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) { return "ordinary-sentinel", nil })
	result := s.execTool(t.Context(), llm.ToolCallData{Name: "managed_write", Arguments: json.RawMessage(`{"n":1}`)}, "")
	if !result.IsError || len(f.requests) != 0 {
		t.Fatalf("unavailable dispatch=%+v", result)
	}
	result = s.execTool(t.Context(), llm.ToolCallData{Name: "ordinary", Arguments: json.RawMessage(`{}`)}, "")
	if result.IsError || result.Output != "ordinary-sentinel" {
		t.Fatalf("ordinary result=%+v", result)
	}
}

func managedDoneStep(llm.Request) llm.Response { return finalResponse("done") }

func restoreManagedFixture(t *testing.T, s *Session, f *managedFixture) (*Session, error) {
	t.Helper()
	if err := s.autoSaveMeta(); err != nil {
		t.Fatal(err)
	}
	state, id, dir, client, profile := s.stateDir, s.id, s.currentEnv().WorkingDirectory(), s.client, s.currentProfile()
	s.Close()
	meta, err := schema.LoadSessionMeta(state, id)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: state, ManagedRuntime: f, ForceRealIO: true})
	if restored != nil {
		t.Cleanup(restored.Close)
	}
	return restored, err
}
func TestManagedRestoreUnknownPairUsesLateResolutionWithoutDuplicateToolResult(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}), withSteps(managedCallStep, managedDoneStep))
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	pending := s.managedJournal.pending()
	if len(pending) != 1 || pending[0].Result != nil {
		t.Fatalf("pending=%+v", pending)
	}
	original := f.requests[0]
	f.executeErr = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[1].MutationID != original.MutationID || string(f.requests[1].Arguments) != string(original.Arguments) {
		t.Fatalf("replayed=%+v", f.requests)
	}
	if len(restored.managedJournal.pending()) != 0 {
		t.Fatal("resolved journal retained")
	}
	full, err := readTranscriptFull(restored.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	tools, resolutions := 0, 0
	for _, entry := range full.Entries {
		if r := entry.Turn.ManagedResolution; r != nil {
			resolutions++
			if entry.Turn.SteeringSource != "" || entry.Turn.Kind != schema.TurnSteering || r.InvocationID != pending[0].ID || r.MCPResult == nil {
				t.Fatalf("resolution=%+v", entry)
			}
		}
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.Name == "managed_write" {
				tools++
			}
		}
	}
	if tools != 1 || resolutions != 1 {
		t.Fatalf("tools=%d resolutions=%d", tools, resolutions)
	}
}
func TestManagedRestoreReservesOnlyJournalOwnedOrphan(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	response := managedCallStep(llm.Request{})
	response.Message.Content = append(response.Message.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "ordinary-orphan", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"missing"}`)}})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "actual-round"}); err != nil {
		t.Fatal(err)
	}
	calls := assistantToolCalls(response.Message)
	s.execTool(s.managedCallContext(t.Context(), 0), calls[0], "")
	f.executeErr = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[0].MutationID != f.requests[1].MutationID {
		t.Fatalf("requests=%+v", f.requests)
	}
	managed, ordinary := 0, 0
	for _, turn := range restored.history {
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil {
				switch part.ToolResult.ToolCallID {
				case "same-provider-call":
					managed++
					if part.ToolResult.MCPResult == nil {
						t.Fatal("managed call synthetically repaired")
					}
				case "ordinary-orphan":
					ordinary++
				}
			}
		}
	}
	if managed != 1 || ordinary != 1 {
		t.Fatalf("managed=%d ordinary=%d", managed, ordinary)
	}
}

func TestManagedOriginalProjectionSurvivesOutputLimitAndSettlement(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, ToolOutputLimits: map[string]schema.ToolOutputLimit{"managed_write": {MaxChars: 5}}}), withSteps(managedCallStep, managedDoneStep))
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range full.Entries {
		for _, part := range entry.Turn.Message.Content {
			r := part.ToolResult
			if r != nil && r.Name == "managed_write" {
				found = true
				if r.ManagedModelText != "model-sentinel" || r.Content == r.ManagedModelText || r.MCPResult == nil {
					t.Fatalf("projection=%+v", r)
				}
			}
		}
	}
	if !found || len(s.managedJournal.pending()) != 0 {
		t.Fatal("durable settlement missing")
	}
}
func TestManagedResultOriginRejectsTransportIdentity(t *testing.T) {
	request := ManagedRequest{Identity: ManagedIdentity{BindingID: "logical-binding", ServiceID: "backend"}}
	for _, origin := range []llm.MCPOrigin{{BindingID: "lease", ServiceID: "backend", SessionID: "durable-session"}, {BindingID: "logical-binding", ServiceID: "backend", SessionID: "transport-session"}, {BindingID: "logical-binding", ServiceID: "incarnation", SessionID: "durable-session"}} {
		if err := validateManagedResult(ManagedResult{Host: &llm.MCPResult{Version: 1, Origin: origin}}, request, "durable-session"); err == nil {
			t.Fatalf("accepted foreign origin=%+v", origin)
		}
	}
}

func TestManagedRestoreAuthoredHTMLKeepsExactRequestBytes(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	response := managedCallStep(llm.Request{})
	raw := json.RawMessage("{ \"n\":\"<script>a&b>c</script>\u2028\u2029\", \"count\":1e+03 }")
	// This trusted catalog fixture accepts an arbitrary authored JSON document.
	s.managedTools["managed_write"] = ManagedTool{Operation: "write", Validate: func(raw json.RawMessage) error {
		if !json.Valid(raw) {
			return errors.New("invalid")
		}
		return nil
	}}
	registered := *s.reg.Get("managed_write")
	registered.ValidateRaw = s.managedTools["managed_write"].Validate
	if err := s.reg.Register(registered); err != nil {
		t.Fatal(err)
	}
	response.Message.Content[0].ToolCall.Arguments = raw
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "html-round"}); err != nil {
		t.Fatal(err)
	}
	result := s.execTool(s.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
	if !result.IsError || len(f.requests) != 1 {
		t.Fatalf("dispatch=%+v", result)
	}
	f.executeErr = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || string(f.requests[1].Arguments) != string(raw) || len(restored.managedJournal.pending()) != 0 {
		t.Fatalf("HTML request changed: %+v", f.requests)
	}
}
