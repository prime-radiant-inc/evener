package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSession_ProcessInputRepairsOrphanedAssistantToolCallsBeforeModelRequest(t *testing.T) {
	dir := t.TempDir()

	var validationErr error
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				validationErr = validateRecoveredToolCallHistory(req.Messages)
				return finalResponse("recovered")
			},
		},
	}
	c := llm.NewClient()
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var dangerousRuns atomic.Int32
	if err := sess.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "dangerous",
			Description: "side-effecting test tool that must not be replayed during history repair",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			dangerousRuns.Add(1)
			return "ran", nil
		},
	}); err != nil {
		t.Fatalf("register dangerous tool: %v", err)
	}

	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("start")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_ok", Name: "shell", Arguments: json.RawMessage(`{"command":"true"}`), Type: "function"}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_missing", Name: "dangerous", Arguments: json.RawMessage(`{}`), Type: "function"}},
			},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_ok", "shell", "already done", false)),
	}

	out, err := sess.ProcessInput(context.Background(), "please continue", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "recovered" {
		t.Fatalf("ProcessInput output = %q, want recovered", out)
	}
	if validationErr != nil {
		t.Fatal(validationErr)
	}
	if got := dangerousRuns.Load(); got != 0 {
		t.Fatalf("orphan repair re-ran dangerous tool %d time(s); missing tool calls must be repaired, not replayed", got)
	}

	missing, ok := findToolResultInHistory(sess.history, "call_missing")
	if !ok {
		t.Fatalf("session history did not persist synthetic result for call_missing: %s", turnKinds(sess.history))
	}
	if missing.Name != "dangerous" {
		t.Fatalf("synthetic result name = %q, want dangerous", missing.Name)
	}
	if !missing.IsError {
		t.Fatalf("synthetic result IsError = false, want true")
	}
	text := fmt.Sprint(missing.Content)
	for _, want := range []string{"unavailable", "interrupted", "not rerun"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Fatalf("synthetic result content %q does not explain %q", text, want)
		}
	}

	userIdx := lastTurnIndex(sess.history, schema.TurnUserInput)
	repairIdx := turnIndexWithToolResult(sess.history, "call_missing")
	if repairIdx < 0 || userIdx < 0 || repairIdx >= userIdx {
		t.Fatalf("repair turn must be persisted before the new user input; repairIdx=%d userIdx=%d history=%s", repairIdx, userIdx, turnKinds(sess.history))
	}
}

func TestResumeHistoryRepairsOrphanedAssistantToolCallsBeforeLaterUserInput(t *testing.T) {
	entries := []TranscriptEntry{
		{Kind: "entry", Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("start"))},
		{Kind: "entry", Seq: 1, Turn: schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_done", Name: "shell", Arguments: json.RawMessage(`{"command":"true"}`), Type: "function"}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_lost", Name: "apply_patch", Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** End Patch"}`), Type: "function"}},
			},
		})},
		{Kind: "entry", Seq: 2, Turn: schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_done", "shell", "ok", false))},
		{Kind: "entry", Seq: 3, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("commit it"))},
	}

	history := ResumeHistory(entries)
	if got, want := len(history), 5; got != want {
		t.Fatalf("len(history) = %d, want %d; history=%s", got, want, turnKinds(history))
	}
	if history[3].Kind != schema.TurnToolResults {
		t.Fatalf("history[3].Kind = %s, want TOOL_RESULTS before later user input; history=%s", history[3].Kind, turnKinds(history))
	}
	if history[4].Kind != schema.TurnUserInput || history[4].Message.Text() != "commit it" {
		t.Fatalf("history[4] = %+v, want original later user input preserved after repair", history[4])
	}
	lost, ok := findToolResultInHistory(history, "call_lost")
	if !ok {
		t.Fatalf("missing synthetic result for call_lost; history=%s", turnKinds(history))
	}
	if lost.Name != "apply_patch" || !lost.IsError {
		t.Fatalf("synthetic lost result = %+v, want apply_patch error result", lost)
	}
	if doneCount := countToolResultsInHistory(history, "call_done"); doneCount != 1 {
		t.Fatalf("call_done result count = %d, want existing result preserved exactly once", doneCount)
	}
}

func validateRecoveredToolCallHistory(messages []llm.Message) error {
	pending := map[string]string{}
	results := map[string]int{}

	for i, msg := range messages {
		if len(pending) > 0 && msg.Role != llm.RoleTool {
			return fmt.Errorf("message %d role %s arrived while assistant tool calls were unanswered: %v", i, msg.Role, pending)
		}
		switch msg.Role {
		case llm.RoleAssistant:
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
					pending[part.ToolCall.ID] = part.ToolCall.Name
				}
			}
		case llm.RoleTool:
			for _, part := range msg.Content {
				if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
					continue
				}
				id := part.ToolResult.ToolCallID
				results[id]++
				delete(pending, id)
			}
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("request ended with unanswered assistant tool calls: %v", pending)
	}
	if results["call_ok"] != 1 {
		return fmt.Errorf("call_ok result count = %d, want 1", results["call_ok"])
	}
	if results["call_missing"] != 1 {
		return fmt.Errorf("call_missing synthetic result count = %d, want 1", results["call_missing"])
	}
	return nil
}

func countToolResultsInHistory(history []schema.Turn, callID string) int {
	var count int
	for _, turn := range history {
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				count++
			}
		}
	}
	return count
}

func findToolResultInHistory(history []schema.Turn, callID string) (*llm.ToolResultData, bool) {
	for i := range history {
		for j := range history[i].Message.Content {
			part := history[i].Message.Content[j]
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return part.ToolResult, true
			}
		}
	}
	return nil, false
}

func turnIndexWithToolResult(history []schema.Turn, callID string) int {
	for i := range history {
		for _, part := range history[i].Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return i
			}
		}
	}
	return -1
}

func lastTurnIndex(history []schema.Turn, kind schema.TurnKind) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind == kind {
			return i
		}
	}
	return -1
}

func turnKinds(history []schema.Turn) string {
	kinds := make([]string, len(history))
	for i, turn := range history {
		kinds[i] = string(turn.Kind)
	}
	return strings.Join(kinds, ",")
}
