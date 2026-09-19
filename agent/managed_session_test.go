package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

type managedFixture struct {
	afterExecute  func()
	prepareChange func(*ManagedRequest)
	readOnly      bool
	result        *ManagedResult

	mu                                   sync.Mutex
	sessions                             []ManagedSession
	requests                             []ManagedRequest
	prepareErr, executeErr, authorizeErr error
	closed                               int
}

func (f *managedFixture) Catalog() ([]ManagedTool, error) {
	return []ManagedTool{{Definition: llm.ToolDefinition{Name: "managed_write", Description: "Managed test operation", Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "number"}, "label": map[string]any{"type": "string"}}, "required": []string{"n"}, "additionalProperties": false}}, Operation: "write", ReadOnly: f.readOnly, Validate: strictManagedFixtureInput}}, nil
}
func strictManagedFixtureInput(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("object required")
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("duplicate input field")
		}
		seen[name] = true
		var value any
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		switch name {
		case "n":
			if _, ok := value.(json.Number); !ok {
				return errors.New("number required")
			}
		case "label":
			if _, ok := value.(string); !ok {
				return errors.New("string required")
			}
		default:
			return errors.New("unknown input field")
		}
	}
	if _, err = decoder.Token(); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing input")
	}
	if !seen["n"] {
		return errors.New("n required")
	}
	return nil
}

func (f *managedFixture) Bind(s ManagedSession) (ManagedBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, s)
	return f, nil
}
func (f *managedFixture) Prepare(_ context.Context, c ManagedCall) (ManagedRequest, error) {
	r := ManagedRequest{ToolName: c.ToolName, ToolCallID: c.ToolCallID, Identity: ManagedIdentity{BindingID: "logical-binding", ServiceID: "backend", RealmID: "realm", PrincipalID: "principal", NamespaceID: "namespace"}, Operation: c.Operation, InvocationID: c.InvocationID, Arguments: append(json.RawMessage(nil), c.Arguments...)}
	if f.prepareChange != nil {
		f.prepareChange(&r)
	}
	return r, f.prepareErr
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
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}), withSteps([]func(llm.Request) llm.Response{managedCallStep, managedCallStep, managedDoneStep}...))
	if len(f.requests) != 0 || len(f.sessions) != 1 || f.sessions[0].SessionID != s.ID() {
		t.Fatalf("construction acquired or misbound: %+v", f)
	}
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[0].InvocationID == f.requests[1].InvocationID || string(f.requests[0].Arguments) != `{ "n":9007199254740993 }` {
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
	if len(f.requests) != 2 || f.requests[1].InvocationID != original.InvocationID || string(f.requests[1].Arguments) != string(original.Arguments) {
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
	if len(f.requests) != 2 || f.requests[0].InvocationID != f.requests[1].InvocationID {
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
	s.managedTools["managed_write"] = ManagedTool{Operation: "write", ReadOnly: f.readOnly, Validate: func(raw json.RawMessage) error {
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

func TestManagedRestoreDenialPreservesPendingWithoutDispatch(t *testing.T) {
	for _, paired := range []bool{false, true} {
		t.Run(fmt.Sprint("paired=", paired), func(t *testing.T) {
			f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
			response := managedCallStep(llm.Request{})
			if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "denied-round"}); err != nil {
				t.Fatal(err)
			}
			results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
			if err != nil {
				t.Fatal(err)
			}
			if paired {
				if err := s.persistToolResults(t.Context(), response.ToolCalls(), results); err != nil {
					t.Fatal(err)
				}
			}
			f.authorizeErr = ErrManagedAuthorityDenied
			f.executeErr = nil
			restored, err := restoreManagedFixture(t, s, f)
			if paired {
				if err != nil || restored == nil || len(restored.managedJournal.pending()) != 1 {
					t.Fatalf("paired denial: %v", err)
				}
			} else {
				var pending *ManagedRecoveryPendingError
				if restored != nil || !errors.As(err, &pending) || !pending.Denied {
					t.Fatalf("unpaired denial: %v", err)
				}
			}
			if len(f.requests) != 1 {
				t.Fatal("denied invocation replayed")
			}
		})
	}
}
func TestManagedChildUsesCurrentParentCatalogAndCleansItsBinding(t *testing.T) {
	f := &managedFixture{}
	parent := newSession(t, withConfig(SessionConfig{ManagedRuntime: f, MaxSubagentDepth: 2, NoProjectPrompts: true}), withoutGitSnapshot())
	prepared, err := parent.prepareSubagentRun(t.Context(), "inspect", "", "", 1, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePreparedTreeSlot(prepared)
	child := prepared.sub.sess
	if child.cfg.ManagedRuntime != f || child.cfg.managedParent != parent.managedBinding || child.cfg.spawn.parentSessionID != parent.id {
		t.Fatal("child lost live binding identity")
	}
	child.Close()
	parent.reg.Remove("managed_write")
	prepared2, err := parent.prepareSubagentRun(t.Context(), "inspect", "", "", 1, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePreparedTreeSlot(prepared2)
	defer prepared2.sub.sess.Close()
	if prepared2.sub.sess.reg.Get("managed_write") != nil {
		t.Fatal("child widened current parent")
	}
}
func TestManagedFrozenConfigurationUsesLiveProvider(t *testing.T) {
	f := &managedFixture{}
	cfg := subagentConfigFromFrozenDescriptor(SessionConfig{}.toSnapshot(), SessionConfig{ManagedRuntime: f})
	s := newSession(t, withConfig(cfg))
	if s.managedBinding != f || len(f.sessions) != 1 {
		t.Fatal("frozen child omitted live provider")
	}
}
func TestManagedInitializationFailureClosesBinding(t *testing.T) {
	f := &managedFixture{}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{ManagedRuntime: f, ContextStrategy: "missing-strategy"})
	if s != nil {
		s.Close()
	}
	if err == nil || s != nil || f.closed != 1 {
		t.Fatalf("failed initialization: returnedSession=%t err=%v closed=%d", s != nil, err, f.closed)
	}
}

func TestManagedPendingOccurrenceSurvivesActualFold(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s := newScriptedSummaryCompactSession(t, "managed-fold-cheap", func(llm.Request) llm.Response {
		once.Do(func() { close(entered); <-proceed })
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{ManagedRuntime: f, StateDir: t.TempDir(), ForceRealIO: true, NoProjectPrompts: true}))
	seedNumberedSessionHistory(t, s, 12)
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	compactDone := make(chan error, 1)
	go func() { compactDone <- s.Compact(t.Context()) }()
	<-entered
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "fold-round"}); err != nil {
		t.Fatal(err)
	}
	results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.persistToolResults(t.Context(), response.ToolCalls(), results); err != nil {
		t.Fatal(err)
	}
	pending := s.managedJournal.pending()[0]
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	close(proceed)
	if err = <-compactDone; err != nil {
		t.Fatal(err)
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	wrongLocator := pending
	wrongLocator.AssistantSeq = -1
	if managedAnchorPresent(full.Entries, wrongLocator) {
		t.Fatal("ignored original assistant sequence integrity locator")
	}
	copied := false
	for _, entry := range full.Entries {
		if entry.Turn.AttemptGroupID == "fold-round" && entry.Seq != pending.AssistantSeq {
			copied = true
		}
	}
	if !copied {
		t.Fatal("actual fold did not copy assistant occurrence to a new sequence")
	}
	f.executeErr = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[1].InvocationID != pending.Request.InvocationID || len(restored.managedJournal.pending()) != 0 {
		t.Fatal("fold lost original invocation")
	}
}

func TestManagedUnpairedHistoryPreventsCompactionModelCall(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	var cheapCalls int
	s := newScriptedSummaryCompactSession(t, "managed-blocked-cheap", func(llm.Request) llm.Response { cheapCalls++; return llm.Response{Message: llm.Assistant("summary")} }, withConfig(SessionConfig{ManagedRuntime: f, StateDir: t.TempDir(), ForceRealIO: true, NoProjectPrompts: true}))
	seedNumberedSessionHistory(t, s, 12)
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "unpaired-round"}); err != nil {
		t.Fatal(err)
	}
	s.execTool(s.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	if err := s.Compact(t.Context()); !errors.Is(err, ErrManagedRecoveryPending) {
		t.Fatalf("unpaired compact=%v", err)
	}
	if cheapCalls != 0 {
		t.Fatal("model called on unpaired history")
	}
}

func TestManagedRestoreReducedCatalogDoesNotAcquire(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	parent := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, MaxSubagentDepth: 2}), withoutGitSnapshot())
	prepared, prepareErr := parent.prepareSubagentRun(t.Context(), "inspect", "", "", 1, "", "", nil, nil)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	defer releasePreparedTreeSlot(prepared)
	s := prepared.sub.sess
	defer s.Close()
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "reduced-round"}); err != nil {
		t.Fatal(err)
	}
	s.execTool(s.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
	if err := s.autoSaveMeta(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(s.client, s.currentProfile(), execenv.NewLocalExecutionEnvironment(s.currentEnv().WorkingDirectory()), meta, RestoreSessionConfig{StateDir: s.stateDir, ManagedRuntime: f, ForceRealIO: true, spawn: spawnConfig{parentSessionID: parent.id, toolNameCeiling: []string{"communicate"}}, managedParent: parent.managedBinding, managedParentTools: parent.reg.Names()})
	if restored != nil {
		restored.Close()
		t.Fatal("returned unpaired runnable session")
	}
	if !errors.Is(err, ErrManagedAuthorityDenied) || len(f.requests) != 1 || len(f.sessions) != 2 {
		t.Fatalf("reduced restore: %v calls=%d bindings=%d", err, len(f.requests), len(f.sessions))
	}
}
func TestManagedRecoveryBypassesRepeatedFailureBreaker(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}), withSteps([]func(llm.Request) llm.Response{managedCallStep, managedCallStep, managedDoneStep}...))
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	if len(s.managedJournal.pending()) != 2 {
		t.Fatal("pending calls lost")
	}
	f.executeErr = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 4 || len(restored.managedJournal.pending()) != 0 {
		t.Fatalf("recovery suppressed by breaker: calls=%d", len(f.requests))
	}
}

func TestManagedCatalogKeepsExactModelSchema(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{ManagedRuntime: f}))
	for _, definition := range s.ToolDefinitions() {
		if definition.Name == "managed_write" {
			properties := definition.Parameters["properties"].(map[string]any)
			if _, exists := properties["intent"]; exists {
				t.Fatal("managed catalog gained generic intent input")
			}
			return
		}
	}
	t.Fatal("managed catalog missing")
}

func TestManagedPreToolUseHookValidatesFinalRawWithoutRepair(t *testing.T) {
	for _, tc := range []struct {
		name, arguments, hook string
		wantCalls             int
	}{
		{"unrelated-update", `{"n":9007199254740993,"label":"old"}`, `{"hookSpecificOutput":{"updatedInput":{"label":"new"}}}`, 1},
		{"changed-field-invalid", `{"n":1}`, `{"hookSpecificOutput":{"updatedInput":{"n":"1"}}}`, 0},
		{"denied", `{"n":1}`, `{"hookSpecificOutput":{"permissionDecision":"deny"}}`, 0},
		{"duplicate-cannot-repair", `{"n":1,"n":2}`, `{"hookSpecificOutput":{"updatedInput":{"n":3}}}`, 0},
		{"malformed-cannot-repair", `{"n":1`, `{"hookSpecificOutput":{"updatedInput":{"n":3}}}`, 0},
		{"host-field-cannot-repair", `{"n":1,"mutationId":"forged"}`, `{"hookSpecificOutput":{"updatedInput":{"n":3}}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &managedFixture{}
			s := newSession(t, withConfig(SessionConfig{ManagedRuntime: f, StateDir: t.TempDir(), ForceRealIO: true}))
			hookClient := llm.NewClient()
			hookClient.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(tc.hook)} }}})
			runner := hooks.NewRunner(hookClient, "gpt-5.2")
			runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "managed_write", Type: "prompt", Prompt: "fixture"})
			s.hookRunner = runner
			response := managedCallStep(llm.Request{})
			response.Message.Content[0].ToolCall.Arguments = json.RawMessage(tc.arguments)
			// Invalid JSON cannot itself be encoded as a transcript anchor. Direct
			// prepare/registry rejection still runs before any durable admission.
			if json.Valid([]byte(tc.arguments)) {
				if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "hook-round"}); err != nil {
					t.Fatal(err)
				}
			}
			result := s.execTool(s.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
			if len(f.requests) != tc.wantCalls {
				t.Fatalf("backend calls=%d want=%d result=%+v", len(f.requests), tc.wantCalls, result)
			}
			if tc.wantCalls == 0 {
				if !result.IsError || len(s.managedJournal.pending()) != 0 {
					t.Fatal("rejected hook input acquired identity")
				}
			} else {
				var values map[string]json.RawMessage
				if err := json.Unmarshal(f.requests[0].Arguments, &values); err != nil {
					t.Fatal(err)
				}
				if string(values["n"]) != "9007199254740993" || string(values["label"]) != `"new"` {
					t.Fatalf("hook changed untouched number: %s", f.requests[0].Arguments)
				}
			}
		})
	}
}

func TestManagedBareChildRestoreCannotBecomeRootBinding(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	parent := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, MaxSubagentDepth: 2}), withoutGitSnapshot())
	prepared, err := parent.prepareSubagentRun(t.Context(), "inspect", "", "", 1, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePreparedTreeSlot(prepared)
	child := prepared.sub.sess
	defer child.Close()
	response := managedCallStep(llm.Request{})
	if err = child.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "bare-child-round"}); err != nil {
		t.Fatal(err)
	}
	child.execTool(child.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
	if err = child.autoSaveMeta(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	meta, err := schema.LoadSessionMeta(child.stateDir, child.id)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(child.client, child.currentProfile(), execenv.NewLocalExecutionEnvironment(child.currentEnv().WorkingDirectory()), meta, RestoreSessionConfig{StateDir: child.stateDir, ManagedRuntime: f, ForceRealIO: true})
	if restored != nil {
		restored.Close()
		t.Fatal("bare child became runnable root")
	}
	if !errors.Is(err, ErrManagedAuthorityDenied) || len(f.sessions) != 2 || len(f.requests) != 1 {
		t.Fatalf("bare child restore: %v bindings=%d requests=%d", err, len(f.sessions), len(f.requests))
	}
}

func TestManagedImmutableCallContextAndReadIdentity(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprintf("durable=%t", durable), func(t *testing.T) {
			f := &managedFixture{readOnly: true}
			cfg := SessionConfig{ManagedRuntime: f}
			if durable {
				cfg.StateDir = t.TempDir()
				cfg.ForceRealIO = true
			}
			s := newSession(t, withConfig(cfg), withSteps(managedCallStep, managedDoneStep))
			if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
				t.Fatal(err)
			}
			if len(f.requests) != 1 {
				t.Fatalf("requests=%+v", f.requests)
			}
			r := f.requests[0]
			if r.ToolName != "managed_write" || r.ToolCallID != "same-provider-call" || (r.InvocationID != "") != durable || string(r.Arguments) != `{ "n":9007199254740993 }` {
				t.Fatalf("adapter context=%+v", r)
			}
		})
	}
}
func TestManagedPrepareCannotChangeCallContext(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, field := range []string{"name", "call", "operation", "invocation", "service"} {
			t.Run(fmt.Sprintf("durable=%t/%s", durable, field), func(t *testing.T) {
				f := &managedFixture{readOnly: true, prepareChange: func(r *ManagedRequest) {
					switch field {
					case "name":
						r.ToolName = "other"
					case "call":
						r.ToolCallID = "other"
					case "operation":
						r.Operation = "other"
					case "invocation":
						r.InvocationID = "invented"
					case "service":
						r.Identity.ServiceID = ""
					}
				}}
				cfg := SessionConfig{ManagedRuntime: f}
				if durable {
					cfg.StateDir = t.TempDir()
					cfg.ForceRealIO = true
				}
				s := newSession(t, withConfig(cfg), withSteps(managedCallStep, managedDoneStep))
				if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
					t.Fatal(err)
				}
				if len(f.requests) != 0 {
					t.Fatalf("changed context dispatched: %+v", f.requests)
				}
			})
		}
	}
}

func TestManagedRecoveryPairsBeforeInterveningInput(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("lost acknowledgment")}
	summaryCalls := 0
	s := newScriptedSummaryCompactSession(t, "managed-recovered-fold", func(llm.Request) llm.Response {
		summaryCalls++
		return llm.Response{Message: llm.Assistant("summary of older turns")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, NoProjectPrompts: true}))
	for range 12 {
		s.appendTurn(schema.TurnUserInput, llm.User("older conversation"))
	}
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "interrupted-round"}); err != nil {
		t.Fatal(err)
	}
	s.execTool(s.managedCallContext(t.Context(), 0), response.ToolCalls()[0], "")
	s.appendTurn(schema.TurnUserInput, llm.User("queued input"))
	f.executeErr = nil
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	assertPair := func(session *Session) {
		t.Helper()
		session.repairOrphanedToolResults(t.Context(), "test next input")
		messages := expandHistory(session.history, replayScope{})
		assistants := 0
		for i, m := range messages {
			if len(assistantToolCalls(m)) > 0 {
				assistants++
				if i+1 >= len(messages) || messages[i+1].Role != llm.RoleTool || messages[i+1].Content[0].ToolResult.Content != "model-sentinel" {
					t.Fatalf("unpaired provider history: %+v", messages)
				}
			}
		}
		count := 0
		for _, m := range messages {
			for _, p := range m.Content {
				if p.ToolResult != nil && p.ToolResult.ToolCallID == "same-provider-call" {
					count++
				}
			}
		}
		if count != 1 || assistants != 1 {
			for _, turn := range session.history {
				t.Logf("retained turn kind=%s attempt=%s text=%q", turn.Kind, turn.AttemptGroupID, turn.Message.Text())
			}
			t.Fatalf("orphan or duplicate managed history: assistants=%d results=%d", assistants, count)
		}
		assistantIndex, userIndex, resultIndex := -1, -1, -1
		for index, turn := range session.history {
			if turn.Kind == schema.TurnAssistant && turn.AttemptGroupID == "interrupted-round" {
				assistantIndex = index
			}
			if turn.Kind == schema.TurnUserInput && turn.Message.Text() == "queued input" {
				userIndex = index
			}
			for _, part := range turn.Message.Content {
				if part.ToolResult != nil && part.ToolResult.ManagedInvocationID != "" {
					resultIndex = index
				}
			}
		}
		if assistantIndex < 0 || userIndex <= assistantIndex || resultIndex <= userIndex {
			t.Fatalf("raw chronology changed: assistant=%d user=%d result=%d", assistantIndex, userIndex, resultIndex)
		}
		if len(session.managedJournal.pending()) != 0 || len(f.requests) != 2 {
			t.Fatal("settled occurrence retained or executed again")
		}
	}
	assertPair(restored)
	// Choose a preserved-tail boundary at the intervening accepted input.
	// Recovery has already evicted the journal; only occurrence metadata can
	// keep the exact assistant and recovered result together during the fold.
	userIndex := indexOfTurnText(restored.history, "queued input")
	restored.contextMgr.PreserveRecentTurns = len(restored.history) - userIndex
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	if err := restored.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if summaryCalls == 0 || restored.history[0].Kind != schema.TurnSummary {
		t.Fatal("did not reach an actual summarized fold")
	}
	assertPair(restored)
	reopened, err := restoreManagedFixture(t, restored, f)
	if err != nil {
		t.Fatal(err)
	}
	assertPair(reopened)
}
