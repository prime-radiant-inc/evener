package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const invalidToolNameDisplay = "invalid tool name"

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
	fields := map[string][]string{
		"ExecResult.ToolName":             {obs.resultName},
		"TOOL_CALL_START.ToolName":        obs.startNames,
		"TOOL_CALL_OUTPUT_DELTA.ToolName": obs.deltaNames,
		"TOOL_CALL_END.ToolName":          obs.endNames,
		"stream TOOL_CALL_START name":     obs.streamStartNames,
		"stream TOOL_CALL_END name":       obs.streamEndNames,
		"live assistant tool-call name":   obs.liveAssistantCallNames,
		"live tool-result name":           obs.liveToolResultNames,
	}
	for label, values := range fields {
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
	if !strings.Contains(obs.transcript, `"name":"`+want+`"`) {
		t.Errorf("persisted assistant/tool-result payloads do not contain projected name %q", want)
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
	if result.Name != invalidToolNameDisplay {
		t.Errorf("synthetic result name = %q, want %q", result.Name, invalidToolNameDisplay)
	}
	content, ok := result.Content.(string)
	if !ok {
		t.Fatalf("synthetic result content type = %T, want string", result.Content)
	}
	if strings.Contains(content, secret) {
		t.Fatal("synthetic result content leaked unreadable provider name")
	}
}
