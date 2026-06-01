package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestWebSearch_MakesGroundingCall(t *testing.T) {
	dir := t.TempDir()

	// Track the grounding call request.
	var groundingReq llm.Request

	fa := &fakeAdapter{
		name: "google",
		steps: []func(req llm.Request) llm.Response{
			// Step 0: main agent calls web_search tool.
			func(req llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "ws1",
					Name:      "web_search",
					Arguments: json.RawMessage(`{"query": "Go 1.23 release date"}`),
				})
			},
			// Step 1: the grounding call made by webSearch().
			func(req llm.Request) llm.Response {
				groundingReq = req
				return llm.Response{
					Message: llm.Assistant("Go 1.23 was released in August 2024."),
				}
			},
			// Step 2: main agent receives grounding result and responds.
			func(req llm.Request) llm.Response {
				return finalResponse("Go 1.23 was released in August 2024.")
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, newGeminiProfile("gemini-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	out, err := sess.ProcessInput(context.Background(), "When was Go 1.23 released?", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "Go 1.23") {
		t.Fatalf("unexpected output: %s", out)
	}

	// Verify the grounding call has WebSearch: true and NO tools.
	if !groundingReq.WebSearch {
		t.Fatalf("grounding request should have WebSearch: true")
	}
	if len(groundingReq.Tools) != 0 {
		t.Fatalf("grounding request should have no tools, got %d", len(groundingReq.Tools))
	}
	// Verify the grounding call uses the session's model and provider.
	if groundingReq.Model != "gemini-test" {
		t.Fatalf("grounding request model: got %q want %q", groundingReq.Model, "gemini-test")
	}
	if groundingReq.Provider != "google" {
		t.Fatalf("grounding request provider: got %q want %q", groundingReq.Provider, "google")
	}
	// Verify the query was passed as the user message.
	userText := ""
	for _, m := range groundingReq.Messages {
		if m.Role == llm.RoleUser {
			userText += m.Text()
		}
	}
	if !strings.Contains(userText, "Go 1.23 release date") {
		t.Fatalf("grounding request user message should contain the query, got: %s", userText)
	}
}

func TestWebSearch_DirectCall(t *testing.T) {
	dir := t.TempDir()

	fa := &fakeAdapter{
		name: "google",
		steps: []func(req llm.Request) llm.Response{
			// The grounding call.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("Search result for test query."),
				}
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, newGeminiProfile("gemini-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	result, err := sess.webSearch(context.Background(), "test query")
	if err != nil {
		t.Fatalf("webSearch: %v", err)
	}

	resultStr := fmt.Sprint(result)
	if !strings.Contains(resultStr, "Search result for test query") {
		t.Fatalf("unexpected result: %s", resultStr)
	}
}
