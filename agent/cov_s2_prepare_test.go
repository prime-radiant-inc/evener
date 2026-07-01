package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_PrepareModelRequest_RepairsOrphan seeds history with an assistant
// tool call that has no following tool result, then drives prepareModelRequest
// and asserts the orphan is repaired (a synthetic tool-result turn is spliced in)
// and the recovery warning is emitted.
func TestS2Cov_PrepareModelRequest_RepairsOrphan(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	var mu sync.Mutex
	var warnings []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == events.EventWarning {
				if w, ok := ev.Data.(events.WarningData); ok {
					mu.Lock()
					warnings = append(warnings, w.Message)
					mu.Unlock()
				}
			}
		}
	}()

	orphanAsst := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "running a tool"},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{}`)}},
	}}
	sess.mu.Lock()
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("do it")),
		schema.NewTurn(schema.TurnAssistant, orphanAsst),
		schema.NewTurn(schema.TurnUserInput, llm.User("actually never mind")),
	}
	sess.mu.Unlock()

	var rt events.RoundTimings
	sess.prepareModelRequest(context.Background(), 0, &rt)

	sess.mu.Lock()
	var foundSynthetic bool
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnToolResults {
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "c1" {
					foundSynthetic = true
				}
			}
		}
	}
	sess.mu.Unlock()
	if !foundSynthetic {
		t.Fatal("no synthetic tool-result turn spliced in for orphaned call c1")
	}

	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	var sawRecovery bool
	for _, w := range warnings {
		if strings.Contains(w, "Recovered") && strings.Contains(w, "interrupted tool call") {
			sawRecovery = true
		}
	}
	if !sawRecovery {
		t.Fatalf("no recovery warning emitted; warnings = %v", warnings)
	}
}
