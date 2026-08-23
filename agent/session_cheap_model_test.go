package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestCheapModelRefusalIsLearnedOnceForTheWholeSession is the wiring proof for
// the cheap-model fallback: web_fetch and the context manager share one caller,
// so a refusal learned by one is respected by the other. The policy itself is
// tested in agent/internal/cheapmodel.
func TestCheapModelRefusalIsLearnedOnceForTheWholeSession(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><body><h1>Cheap Model Page</h1></body></html>`)
	}))
	t.Cleanup(page.Close)

	// The served model answers with a session-log entry, which satisfies both
	// consumers: web_fetch echoes the text, forkSummarize parses it.
	entry, err := json.Marshal(map[string]any{
		"action": "web_fetch", "summary": "Fetched a page.", "outcome": "success",
	})
	if err != nil {
		t.Fatalf("marshal session log entry: %v", err)
	}
	// A Codex backend on a ChatGPT account: it serves its own model and refuses
	// gpt-4.1-nano with an HTTP 400 (evener#313).
	fa := &agenttest.ModelTrackingAdapter{Provider: "openai"}
	fa.Respond = func(req llm.Request) (llm.Response, error) {
		if req.Model != "test-model" {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400,
				fmt.Sprintf("The '%s' model is not supported when using Codex with a ChatGPT account", req.Model), nil, nil)
		}
		return llm.Response{Message: llm.Assistant(string(entry))}, nil
	}

	c := llm.NewClient()
	c.Register(fa)
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		ContextStrategy: "session-log",
		StateDir:        t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "wf1",
		Name:      "web_fetch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "What is this page about?"}`, page.URL)),
	})
	if res.IsError {
		t.Fatalf("web_fetch failed after the cheap model was refused: %s", res.Output)
	}

	// The context manager's summarizer is a different call site in a different
	// package; it must inherit what web_fetch already paid to learn.
	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "wf1", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"x"}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("wf1", "web_fetch", "fetched", false)},
	}
	if err := sess.strategy.AfterAction(context.Background(), history, sess.client); err != nil {
		t.Fatalf("AfterAction: %v", err)
	}

	want := []string{"gpt-4.1-nano", "test-model", "test-model"}
	if got := fa.Models(); !slices.Equal(got, want) {
		t.Fatalf("models addressed = %v, want %v", got, want)
	}
}
