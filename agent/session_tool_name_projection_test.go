package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const (
	invalidToolNameDisplay = "invalid tool name"
	invalidToolNameWire    = tool.InvalidToolNameWire
)

type toolNameProjectionObservation struct {
	resultName             string
	startNames             []string
	deltaNames             []string
	endNames               []string
	streamStartNames       []string
	streamEndNames         []string
	hookPrompts            []string
	liveAssistantCallNames []string
	liveToolResultNames    []string
	transcript             string
}

func observeUnknownToolNameProjection(t *testing.T, name string) toolNameProjectionObservation {
	t.Helper()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	sess := newSession(t,
		withDir(workspace),
		withConfig(SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)

	var hookMu sync.Mutex
	var hookPrompts []string
	hookClient := llm.NewClient()
	hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
		hookMu.Lock()
		hookPrompts = append(hookPrompts, req.Messages[0].Text())
		hookMu.Unlock()
		return llm.Response{Message: llm.Assistant(`{}`)}
	}})
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	// Route on the exact raw identity. Hook payloads must still receive only the
	// display projection, proving matching and presentation are independent.
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: name, Type: "prompt", Prompt: "pre:$TOOL_NAME"})
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{Matcher: name, Type: "prompt", Prompt: "post:$TOOL_NAME"})
	sess.hookRunner = runner

	eventsDone := make(chan []events.SessionEvent, 1)
	go func() {
		var emitted []events.SessionEvent
		for ev := range sess.Events() {
			emitted = append(emitted, ev)
		}
		eventsDone <- emitted
	}()

	call := llm.ToolCallData{ID: "unknown-name", Name: name, Arguments: []byte(`{}`)}
	assistantCall := call
	if err := sess.appendAssistantTurn(llm.Response{Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &assistantCall,
		}},
	}}, ModelAttemptMetadata{}); err != nil {
		t.Fatalf("persist assistant tool call: %v", err)
	}
	res := sess.execTool(context.Background(), call, "")
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{res}); err != nil {
		t.Fatalf("persist tool result: %v", err)
	}

	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	path := sess.TranscriptPath()
	sess.Close()
	emitted := <-eventsDone

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript %q: %v", path, err)
	}

	obs := toolNameProjectionObservation{resultName: res.ToolName, transcript: string(body)}
	for _, ev := range emitted {
		switch data := ev.Data.(type) {
		case events.ToolCallStartData:
			obs.startNames = append(obs.startNames, data.ToolName)
		case events.ToolCallOutputDeltaData:
			obs.deltaNames = append(obs.deltaNames, data.ToolName)
		case events.ToolCallEndData:
			obs.endNames = append(obs.endNames, data.ToolName)
		}
		if stream := ev.ToStreamEvent(); stream != nil && stream.ToolCall != nil {
			switch stream.Type {
			case llm.StreamEventToolCallStart:
				obs.streamStartNames = append(obs.streamStartNames, stream.ToolCall.Name)
			case llm.StreamEventToolCallEnd:
				obs.streamEndNames = append(obs.streamEndNames, stream.ToolCall.Name)
			}
		}
	}
	for _, turn := range history {
		for _, part := range turn.Message.Content {
			if part.ToolCall != nil {
				obs.liveAssistantCallNames = append(obs.liveAssistantCallNames, part.ToolCall.Name)
			}
			if part.ToolResult != nil {
				obs.liveToolResultNames = append(obs.liveToolResultNames, part.ToolResult.Name)
			}
		}
	}
	hookMu.Lock()
	obs.hookPrompts = append([]string(nil), hookPrompts...)
	hookMu.Unlock()
	return obs
}

func assertProjectedToolNames(t *testing.T, obs toolNameProjectionObservation, want, secret string) {
	t.Helper()
	displayFields := map[string][]string{
		"ExecResult.ToolName":             {obs.resultName},
		"TOOL_CALL_START.ToolName":        obs.startNames,
		"TOOL_CALL_OUTPUT_DELTA.ToolName": obs.deltaNames,
		"TOOL_CALL_END.ToolName":          obs.endNames,
		"stream TOOL_CALL_START name":     obs.streamStartNames,
		"stream TOOL_CALL_END name":       obs.streamEndNames,
	}
	for label, values := range displayFields {
		if len(values) == 0 {
			t.Errorf("%s was not observed", label)
			continue
		}
		for _, got := range values {
			if got != want {
				t.Errorf("%s is not the expected external projection: bytes=%d contains_secret=%t want=%q", label, len(got), secret != "" && strings.Contains(got, secret), want)
			}
		}
	}
	wireName := want
	if want == invalidToolNameDisplay {
		wireName = invalidToolNameWire
	}
	for label, values := range map[string][]string{
		"live assistant tool-call name": obs.liveAssistantCallNames,
		"live tool-result name":         obs.liveToolResultNames,
	} {
		if len(values) == 0 {
			t.Errorf("%s was not observed", label)
			continue
		}
		for _, got := range values {
			if got != wireName {
				t.Errorf("%s is not the expected wire projection: bytes=%d contains_secret=%t want=%q", label, len(got), secret != "" && strings.Contains(got, secret), wireName)
			}
			if err := llm.ValidateToolName(got); err != nil {
				t.Errorf("%s = %q is invalid on the provider wire: %v", label, got, err)
			}
		}
	}
	if len(obs.hookPrompts) != 2 {
		t.Errorf("tool hooks invoked %d times, want pre and post routing preserved", len(obs.hookPrompts))
	}
	for _, prompt := range obs.hookPrompts {
		if !strings.HasSuffix(prompt, want) {
			t.Errorf("hook input tool_name is not the expected external projection: bytes=%d contains_secret=%t want suffix %q", len(prompt), secret != "" && strings.Contains(prompt, secret), want)
		}
	}
	if secret != "" && strings.Contains(obs.transcript, secret) {
		t.Errorf("persisted assistant/tool-result payloads leak unreadable name: transcript_bytes=%d", len(obs.transcript))
	}
	if !strings.Contains(obs.transcript, `"name":"`+wireName+`"`) {
		t.Errorf("persisted assistant/tool-result payloads do not contain wire-safe name %q", wireName)
	}
}

// This is an end-to-end projection contract. The provider-supplied name remains
// untouched for Registry lookup and hook matching, but every result/event/hook
// payload that crosses the Session boundary must use the bounded display name.
func TestSessionUnknownToolNameExternalProjectionIsPrivateAndBounded(t *testing.T) {
	const secret = "SESSION_PRIVATE_INVALID_TOOL_NAME"
	name := strings.Repeat(secret, 300)
	obs := observeUnknownToolNameProjection(t, name)
	assertProjectedToolNames(t, obs, invalidToolNameDisplay, secret)
}

// Sanitization is deliberately conditional: a readable unknown name is useful
// to the model/operator and must survive every projection unchanged.
func TestSessionReadableUnknownToolNameExternalProjectionIsPreserved(t *testing.T) {
	const name = "readable_unknown_tool"
	obs := observeUnknownToolNameProjection(t, name)
	assertProjectedToolNames(t, obs, name, "")
}

func TestCanonicalIncomingToolNameOnlyMapsReadableAliases(t *testing.T) {
	sess := newSession(t)
	t.Cleanup(sess.Close)

	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "exec_command", want: "shell"},
		{name: " exec_command ", want: " exec_command "},
		{name: "readable_unknown_tool", want: "readable_unknown_tool"},
	} {
		if got := sess.canonicalIncomingToolName(tc.name); got != tc.want {
			t.Errorf("canonicalIncomingToolName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSessionMalformedProviderAliasDoesNotDispatchCanonicalTool(t *testing.T) {
	const malformedAlias = " exec_command "
	sess := newSession(t, withSteps(
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "malformed-alias",
				Name:      malformedAlias,
				Arguments: []byte(`{}`),
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response { return finalResponse("done") },
	), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))

	called := false
	if err := sess.RegisterTool("shell", "test replacement", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		called = true
		return "unexpected", nil
	}); err != nil {
		t.Fatalf("replace shell tool: %v", err)
	}
	if _, err := sess.ProcessInput(context.Background(), "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if called {
		t.Fatal("malformed provider alias dispatched the canonical shell tool")
	}

	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	var callNames, resultNames []string
	for _, turn := range history {
		for _, part := range turn.Message.Content {
			if part.ToolCall != nil && part.ToolCall.ID == "malformed-alias" {
				callNames = append(callNames, part.ToolCall.Name)
			}
			if part.ToolResult != nil && part.ToolResult.ToolCallID == "malformed-alias" {
				resultNames = append(resultNames, part.ToolResult.Name)
			}
		}
	}
	if len(callNames) != 1 || callNames[0] != invalidToolNameWire {
		t.Errorf("provider assistant call names = %q, want [%q]", callNames, invalidToolNameWire)
	}
	if len(resultNames) != 1 || resultNames[0] != invalidToolNameWire {
		t.Errorf("provider tool result names = %q, want [%q]", resultNames, invalidToolNameWire)
	}
}

func TestSessionCustomToolCannotShadowInvalidHistoryProjection(t *testing.T) {
	sess := newSession(t)
	defer sess.Close()

	called := false
	err := sess.RegisterTool(invalidToolNameWire, "must not shadow invalid history", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		called = true
		return "unexpected", nil
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("RegisterTool error = %v, want actionable reserved-name rejection", err)
	}
	if registered := sess.reg.Get(invalidToolNameWire); registered != nil {
		t.Fatalf("reserved history placeholder was registered: %#v", registered)
	}
	for _, definition := range sess.reg.Definitions() {
		if definition.Name == invalidToolNameWire {
			t.Fatal("reserved history placeholder appeared in provider tool definitions")
		}
	}

	result := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "projected-history",
		Name:      invalidToolNameWire,
		Arguments: []byte(`{}`),
	}, "")
	if !result.IsError || called {
		t.Fatalf("projected invalid-history call reached custom executor: result=%#v called=%t", result, called)
	}
}

func TestSyntheticToolResultsProjectUnreadableToolName(t *testing.T) {
	const secret = "SYNTHETIC_RESULT_PRIVATE_TOOL_NAME"
	turn := syntheticToolResultsTurn([]llm.ToolCallData{{
		ID:   "interrupted",
		Name: strings.Repeat(secret, 300),
	}})
	if len(turn.Message.Content) != 1 || turn.Message.Content[0].ToolResult == nil {
		t.Fatalf("synthetic turn = %#v, want one tool result", turn)
	}
	result := turn.Message.Content[0].ToolResult
	if result.Name != invalidToolNameWire {
		t.Errorf("synthetic result name = %q, want %q", result.Name, invalidToolNameWire)
	}
	if err := llm.ValidateToolName(result.Name); err != nil {
		t.Errorf("synthetic result name is invalid on the provider wire: %v", err)
	}
	content, ok := result.Content.(string)
	if !ok {
		t.Fatalf("synthetic result content type = %T, want string", result.Content)
	}
	if strings.Contains(content, secret) {
		t.Fatal("synthetic result content leaked unreadable provider name")
	}
}

func TestExpandHistoryProjectsLegacyUnreadableToolNamesForProviderWire(t *testing.T) {
	const secret = "LEGACY_REPLAY_PRIVATE_TOOL_NAME"
	rawName := strings.Repeat(secret, 300)
	callNames := []string{rawName, invalidToolNameDisplay, "readable_unknown_tool"}
	wantNames := []string{invalidToolNameWire, invalidToolNameWire, "readable_unknown_tool"}

	callParts := make([]llm.ContentPart, 0, len(callNames))
	resultParts := make([]llm.ContentPart, 0, len(callNames))
	for i, name := range callNames {
		id := string(rune('a' + i))
		callParts = append(callParts, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        id,
				Name:      name,
				Arguments: []byte(`{}`),
			},
		})
		resultParts = append(resultParts, llm.ContentPart{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: id,
				Name:       name,
				Content:    "result",
			},
		})
	}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: callParts}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: resultParts}),
	}

	messages := expandHistory(history, replayScope{})
	if len(messages) != 1+len(callNames) {
		t.Fatalf("expanded messages = %d, want %d", len(messages), 1+len(callNames))
	}
	for i, part := range messages[0].Content {
		if part.ToolCall == nil {
			t.Fatalf("assistant part %d has no tool call", i)
		}
		if got := part.ToolCall.Name; got != wantNames[i] {
			t.Errorf("assistant call %d wire name = %q, want %q", i, got, wantNames[i])
		}
		if err := llm.ValidateToolName(part.ToolCall.Name); err != nil {
			t.Errorf("assistant call %d wire name is invalid: %v", i, err)
		}
		result := messages[i+1].Content[0].ToolResult
		if result == nil {
			t.Fatalf("result message %d has no tool result", i)
		}
		if got := result.Name; got != wantNames[i] {
			t.Errorf("tool result %d wire name = %q, want %q", i, got, wantNames[i])
		}
		if err := llm.ValidateToolName(result.Name); err != nil {
			t.Errorf("tool result %d wire name is invalid: %v", i, err)
		}
	}
	if history[0].Message.Content[0].ToolCall.Name != rawName || history[0].Message.Content[1].ToolCall.Name != invalidToolNameDisplay {
		t.Fatal("provider replay projection mutated stored assistant history")
	}
	if history[1].Message.Content[0].ToolResult.Name != rawName || history[1].Message.Content[1].ToolResult.Name != invalidToolNameDisplay {
		t.Fatal("provider replay projection mutated stored tool-result history")
	}
}

func TestProviderHistoryMessagePreservesEmptyToolResultNameForAdapterRecovery(t *testing.T) {
	message := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "recover-name",
			Content:    "result",
		},
	}}}

	projected := providerHistoryMessage(message)
	if got := projected.Content[0].ToolResult.Name; got != "" {
		t.Errorf("projected empty tool-result name = %q, want empty sentinel for adapter recovery", got)
	}
	if got := message.Content[0].ToolResult.Name; got != "" {
		t.Errorf("projection mutated source tool-result name to %q", got)
	}
}

func TestSessionToolCallHistoryProjectsUnsafeArgumentsWithoutMutatingSource(t *testing.T) {
	invalidUTF8 := append([]byte(`{"value":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	if utf8.Valid(invalidUTF8) {
		t.Fatal("invalid UTF-8 fixture is valid")
	}
	if !json.Valid(invalidUTF8) {
		t.Fatal("invalid UTF-8 fixture does not exercise the former JSON-only guard")
	}
	oversized := []byte(`{"value":"` + strings.Repeat("x", tool.MaxToolArgumentBytes) + `"}`)
	if len(oversized) <= tool.MaxToolArgumentBytes {
		t.Fatalf("oversized fixture has %d bytes, want more than %d", len(oversized), tool.MaxToolArgumentBytes)
	}
	if !json.Valid(oversized) {
		t.Fatal("oversized fixture is not valid JSON")
	}

	for _, tc := range []struct {
		name      string
		arguments []byte
	}{
		{name: "invalid UTF-8", arguments: invalidUTF8},
		{name: "oversized valid JSON", arguments: oversized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t, withoutGitSnapshot())
			defer sess.Close()

			responseCall := &llm.ToolCallData{
				ID:        "unsafe-persistence",
				Name:      "read_file",
				Arguments: append([]byte(nil), tc.arguments...),
			}
			response := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: responseCall,
			}}}}
			if err := sess.appendAssistantTurn(response, ModelAttemptMetadata{}); err != nil {
				t.Fatalf("append assistant turn: %v", err)
			}
			if !bytes.Equal(responseCall.Arguments, tc.arguments) {
				t.Error("assistant persistence mutated the provider response")
			}
			sess.mu.Lock()
			persistedArguments := append([]byte(nil), sess.history[len(sess.history)-1].Message.Content[0].ToolCall.Arguments...)
			sess.mu.Unlock()
			if !bytes.Equal(persistedArguments, []byte(`{}`)) {
				t.Errorf("persisted assistant arguments have %d bytes, want safe empty object", len(persistedArguments))
			}

			legacyCall := &llm.ToolCallData{
				ID:        "unsafe-replay",
				Name:      "read_file",
				Arguments: append([]byte(nil), tc.arguments...),
			}
			sess.mu.Lock()
			sess.history = []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("replay the legacy call")),
				schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: legacyCall}}}),
				schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID: legacyCall.ID,
					Name:       legacyCall.Name,
					Content:    "rejected",
				}}}}),
			}
			initialRevision := sess.historyRevision
			sess.mu.Unlock()

			var timings events.RoundTimings
			_, _, _, req, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings)
			if err != nil {
				t.Fatalf("prepare provider request: %v", err)
			}
			var replayedArguments []byte
			for _, message := range req.Messages {
				for _, part := range message.Content {
					if part.ToolCall != nil && part.ToolCall.ID == legacyCall.ID {
						replayedArguments = part.ToolCall.Arguments
					}
				}
			}
			if !bytes.Equal(replayedArguments, []byte(`{}`)) {
				t.Errorf("provider-bound arguments have %d bytes, want safe empty object", len(replayedArguments))
			}
			if !bytes.Equal(legacyCall.Arguments, tc.arguments) {
				t.Error("provider replay mutated stored legacy arguments")
			}
			sess.mu.Lock()
			storedArguments := append([]byte(nil), sess.history[1].Message.Content[0].ToolCall.Arguments...)
			storedRevision := sess.historyRevision
			sess.mu.Unlock()
			if !bytes.Equal(storedArguments, tc.arguments) {
				t.Errorf("provider replay replaced durable legacy arguments with %d bytes", len(storedArguments))
			}
			if storedRevision != initialRevision {
				t.Errorf("provider replay advanced history revision from %d to %d without a context change", initialRevision, storedRevision)
			}
		})
	}
}

type retainProjectedTailStrategy struct {
	arguments []byte
}

func (*retainProjectedTailStrategy) Name() string                 { return "retain-projected-tail" }
func (*retainProjectedTailStrategy) Tools() []tool.RegisteredTool { return nil }
func (*retainProjectedTailStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	return nil
}

func (s *retainProjectedTailStrategy) ManageContext(_ context.Context, history *[]schema.Turn, _ int, _ func(events.EventKind, events.EventData)) error {
	s.arguments = append([]byte(nil), (*history)[1].Message.Content[0].ToolCall.Arguments...)
	encoded, err := json.Marshal((*history)[1:])
	if err != nil {
		return err
	}
	var retained []schema.Turn
	if err := json.Unmarshal(encoded, &retained); err != nil {
		return err
	}
	*history = retained
	return nil
}

type unchangedCompactionStrategy struct{}

func (*unchangedCompactionStrategy) Name() string                 { return "unchanged-compaction" }
func (*unchangedCompactionStrategy) Tools() []tool.RegisteredTool { return nil }
func (*unchangedCompactionStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	return nil
}

func (*unchangedCompactionStrategy) ManageContext(_ context.Context, history *[]schema.Turn, _ int, emit func(events.EventKind, events.EventData)) error {
	turns := len(*history)
	emit(events.EventContextCompaction, events.ContextCompactionData{TurnsBefore: turns, TurnsAfter: turns})
	return nil
}

func TestPrepareModelRequestPublishesUnchangedCompactionEffects(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	defer sess.Close()
	sess.strategy = &unchangedCompactionStrategy{}
	sess.mu.Lock()
	sess.history = []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("unchanged"))}
	initialRevision := sess.historyRevision
	sess.mu.Unlock()

	var timings events.RoundTimings
	if _, _, _, _, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.historyRevision != initialRevision+1 {
		t.Fatalf("history revision = %d, want %d after staged compaction", sess.historyRevision, initialRevision+1)
	}
}

func TestRestoreUnchangedProjectedTurnsUsesRetainedTurnOrigin(t *testing.T) {
	unsafeArguments := func(b byte) []byte {
		arguments := append([]byte(`{"value":"`), b)
		return append(arguments, []byte(`"}`)...)
	}
	durable := make([]schema.Turn, 2)
	for i, b := range []byte{0xfe, 0xff} {
		durable[i] = schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        "duplicate-after-projection",
					Name:      "read_file",
					Arguments: unsafeArguments(b),
				},
			}}},
		}
	}
	projected := providerHistoryTurns(durable)
	if !reflect.DeepEqual(projected[0], projected[1]) {
		t.Fatal("fixture turns remain distinguishable after projection")
	}
	snapshots := snapshotProjectedTurns(projected)

	managed := []schema.Turn{projected[1], projected[1]}
	restored := restoreUnchangedProjectedTurns(managed, snapshots, durable)
	got := restored[0].Message.Content[0].ToolCall.Arguments
	if !bytes.Equal(got, durable[1].Message.Content[0].ToolCall.Arguments) {
		t.Fatalf("restored arguments = %v, want retained second origin %v", got, durable[1].Message.Content[0].ToolCall.Arguments)
	}
	if got := restored[1].Message.Content[0].ToolCall.Arguments; !bytes.Equal(got, []byte(`{}`)) {
		t.Fatalf("duplicated projected arguments = %v, want safe projected value after origin was consumed", got)
	}
}

func TestPrepareModelRequestKeepsRawRetainedHistoryAfterProjectedCompaction(t *testing.T) {
	invalidUTF8 := append([]byte(`{"value":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	strategy := &retainProjectedTailStrategy{}
	sess := newSession(t, withoutGitSnapshot())
	defer sess.Close()
	sess.strategy = strategy
	sess.mu.Lock()
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("drop this turn")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "retained-unsafe-call",
				Name:      "read_file",
				Arguments: append([]byte(nil), invalidUTF8...),
			},
		}}}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("retained-unsafe-call", "read_file", "rejected", true)),
	}
	sess.mu.Unlock()

	var timings events.RoundTimings
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings)
	if err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	if !bytes.Equal(strategy.arguments, []byte(`{}`)) {
		t.Fatalf("context strategy arguments = %q, want safe empty object", strategy.arguments)
	}
	var requestArguments []byte
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolCall != nil && part.ToolCall.ID == "retained-unsafe-call" {
				requestArguments = part.ToolCall.Arguments
			}
		}
	}
	if !bytes.Equal(requestArguments, []byte(`{}`)) {
		t.Fatalf("provider request arguments = %q, want safe empty object", requestArguments)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.history) != 2 {
		t.Fatalf("published history has %d turns, want transformed two-turn tail", len(sess.history))
	}
	if got := sess.history[0].Message.Content[0].ToolCall.Arguments; !bytes.Equal(got, invalidUTF8) {
		t.Fatalf("retained durable arguments = %q, want original invalid UTF-8 bytes", got)
	}
}

type mutateProjectedTailStrategy struct{}

func (*mutateProjectedTailStrategy) Name() string                 { return "mutate-projected-tail" }
func (*mutateProjectedTailStrategy) Tools() []tool.RegisteredTool { return nil }
func (*mutateProjectedTailStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	return nil
}

func (*mutateProjectedTailStrategy) ManageContext(_ context.Context, history *[]schema.Turn, _ int, _ func(events.EventKind, events.EventData)) error {
	(*history)[1].Message.Content[0].ToolCall.Arguments = []byte(`{"strategy":"changed"}`)
	*history = (*history)[1:]
	return nil
}

func TestPrepareModelRequestPublishesProjectedInPlaceStrategyChanges(t *testing.T) {
	invalidUTF8 := append([]byte(`{"value":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	sess := newSession(t, withoutGitSnapshot())
	defer sess.Close()
	sess.strategy = &mutateProjectedTailStrategy{}
	sess.mu.Lock()
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("drop this turn")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "changed-projected-call",
				Name:      "read_file",
				Arguments: append([]byte(nil), invalidUTF8...),
			},
		}}}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("changed-projected-call", "read_file", "rejected", true)),
	}
	sess.mu.Unlock()

	var timings events.RoundTimings
	if _, _, _, _, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.history) != 2 {
		t.Fatalf("published history has %d turns, want changed retained call/result pair", len(sess.history))
	}
	if got := sess.history[0].Message.Content[0].ToolCall.Arguments; !bytes.Equal(got, []byte(`{"strategy":"changed"}`)) {
		t.Fatalf("published strategy arguments = %q, want the strategy's in-place change", got)
	}
}

func TestPrepareModelRequestProjectsLegacyUnreadableToolNamesBeforeContextManagement(t *testing.T) {
	const secret = "LEGACY_CONTEXT_PRIVATE_TOOL_NAME"
	rawName := strings.Repeat(secret, 300)
	call := &llm.ToolCallData{ID: "legacy-context", Name: rawName, Arguments: []byte(`{}`)}
	result := &llm.ToolResultData{ToolCallID: call.ID, Name: rawName, Content: "result"}

	sess := newSession(t)
	sess.contextMgr.CheckpointThreshold = 0
	sess.contextMgr.PreserveRecentTurns = 0
	sess.mu.Lock()
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("use the legacy result")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: call}}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: result}}}),
	}
	sess.mu.Unlock()

	var contextHistory []schema.Turn
	sess.elicitNoteFn = func(_ context.Context, history []schema.Turn) (string, error) {
		contextHistory = append([]schema.Turn(nil), history...)
		return "", nil
	}

	var timings events.RoundTimings
	if _, _, _, _, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	if len(contextHistory) == 0 {
		t.Fatal("context management did not receive the legacy history")
	}
	for _, turn := range contextHistory {
		for _, part := range turn.Message.Content {
			if part.ToolCall != nil && part.ToolCall.Name != invalidToolNameWire {
				t.Errorf("context tool-call name = %q, want %q", part.ToolCall.Name, invalidToolNameWire)
			}
			if part.ToolResult != nil && part.ToolResult.Name != invalidToolNameWire {
				t.Errorf("context tool-result name = %q, want %q", part.ToolResult.Name, invalidToolNameWire)
			}
		}
	}
	if call.Name != rawName || result.Name != rawName {
		t.Fatal("context projection mutated the source legacy messages")
	}
}

func TestDurableToolResultAppendPathsProjectUnreadableNames(t *testing.T) {
	const secret = "DURABLE_PRIVATE_TOOL_NAME"
	rawName := strings.Repeat(secret, 300)

	for _, test := range []struct {
		name   string
		append func(*Session, llm.Message) error
	}{
		{
			name: "terminal job status",
			append: func(sess *Session, message llm.Message) error {
				return sess.appendTurnWithDurableTranscriptMessage(schema.TurnToolResults, message, message)
			},
		},
		{
			name: "delegate delivery commit",
			append: func(sess *Session, message llm.Message) error {
				return sess.appendToolResultsWithDeliveryCommitsDurably(message, message, []delegateToolCallDeliveryCommit{{toolCallID: "durable"}})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			sess := newSession(t, withConfig(SessionConfig{
				StateDir:         stateDir,
				MaxSubagentDepth: 1,
				NoProjectPrompts: true,
				testOnly: testConfig{
					skipGitSnapshot:     true,
					minimalSystemPrompt: true,
					noSyncJobStore:      true,
				},
			}))
			message := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: "durable",
					Name:       rawName,
					Content:    "result",
				},
			}}}

			if err := test.append(sess, message); err != nil {
				t.Fatalf("append durable result: %v", err)
			}
			sess.mu.Lock()
			gotName := sess.history[len(sess.history)-1].Message.Content[0].ToolResult.Name
			sess.mu.Unlock()
			path := sess.TranscriptPath()
			sess.Close()
			if gotName != invalidToolNameWire {
				t.Errorf("live durable result name = %q, want %q", gotName, invalidToolNameWire)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read durable transcript: %v", err)
			}
			if strings.Contains(string(body), secret) {
				t.Fatal("durable transcript leaked unreadable tool name")
			}
			if count := strings.Count(string(body), `"name":"`+invalidToolNameWire+`"`); count != 1 {
				t.Errorf("durable transcript wire-name occurrences = %d, want 1", count)
			}
			if message.Content[0].ToolResult.Name != rawName {
				t.Fatal("durable append projection mutated its input message")
			}
		})
	}
}
