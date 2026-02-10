package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prime-radiant/serf/internal/llm"
)

func TestWebFetchCacheKey(t *testing.T) {
	// Same URL must produce same key.
	k1 := webFetchCacheKey("https://example.com/docs")
	k2 := webFetchCacheKey("https://example.com/docs")
	if k1 != k2 {
		t.Fatalf("same URL produced different keys: %q vs %q", k1, k2)
	}
	// Different URLs must produce different keys.
	k3 := webFetchCacheKey("https://example.com/other")
	if k1 == k3 {
		t.Fatalf("different URLs produced same key: %q", k1)
	}
	// Key should be hex and a reasonable length.
	if len(k1) != 16 {
		t.Fatalf("expected 16-char hex key, got %d chars: %q", len(k1), k1)
	}
}

func TestWebFetchHTMLToMarkdown(t *testing.T) {
	html := `<html><body><h1>Hello</h1><p>World</p><ul><li>one</li><li>two</li></ul></body></html>`
	md, err := htmlToMarkdown(html)
	if err != nil {
		t.Fatalf("htmlToMarkdown: %v", err)
	}
	if !strings.Contains(md, "Hello") {
		t.Fatalf("markdown missing heading text: %s", md)
	}
	if !strings.Contains(md, "World") {
		t.Fatalf("markdown missing paragraph text: %s", md)
	}
	// Should not contain raw HTML tags.
	if strings.Contains(md, "<h1>") || strings.Contains(md, "<p>") {
		t.Fatalf("markdown still contains HTML tags: %s", md)
	}
}

func toolCallResponse(calls ...llm.ToolCallData) llm.Response {
	parts := make([]llm.ContentPart, len(calls))
	for i, c := range calls {
		c := c
		parts[i] = llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &c}
	}
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: parts},
	}
}

func TestWebFetchTool_Integration(t *testing.T) {
	// Serve HTML via httptest.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>Test Page</h1><p>This is a test document about Go programming.</p></body></html>`)
	}))
	defer srv.Close()

	dir := t.TempDir()

	// Set up fake adapter that captures requests and returns a canned answer.
	var capturedReqs []llm.Request
	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Step 0: the main agent calls web_fetch.
			func(req llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "wf1",
					Name:      "web_fetch",
					Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "What is this page about?"}`, srv.URL)),
				})
			},
			// Step 1: the cheap model Complete() call from web_fetch.
			func(req llm.Request) llm.Response {
				capturedReqs = append(capturedReqs, req)
				return llm.Response{Message: llm.Assistant("This page is about Go programming.")}
			},
			// Step 2: the main agent receives the web_fetch result and responds.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("The page is about Go programming.")}
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	out, err := sess.ProcessInput(context.Background(), "Fetch the test page")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "Go programming") {
		t.Fatalf("unexpected output: %s", out)
	}

	// Verify the cheap model call used CheapModel().
	if len(capturedReqs) == 0 {
		t.Fatalf("expected at least one cheap model request")
	}
	cheapReq := capturedReqs[0]
	if cheapReq.Model != "gpt-4.1-nano" {
		t.Fatalf("cheap model request used model %q, want %q", cheapReq.Model, "gpt-4.1-nano")
	}
	// Verify the cheap model request includes the question and content.
	sysText := ""
	userText := ""
	for _, m := range cheapReq.Messages {
		if m.Role == "system" {
			sysText += m.Text()
		}
		if m.Role == "user" {
			userText += m.Text()
		}
	}
	if !strings.Contains(sysText, "web content") {
		t.Fatalf("cheap model system prompt missing expected text: %s", sysText)
	}
	if !strings.Contains(userText, "What is this page about?") {
		t.Fatalf("cheap model user message missing question: %s", userText)
	}
	if !strings.Contains(userText, "Test Page") {
		t.Fatalf("cheap model user message missing page content: %s", userText)
	}

	// Verify cache files were written.
	cacheDir := filepath.Join(dir, ".serf", "web_cache")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("reading cache dir: %v", err)
	}
	var hasRaw, hasMD bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-raw.html") {
			hasRaw = true
		}
		if strings.HasSuffix(e.Name(), ".md") {
			hasMD = true
		}
	}
	if !hasRaw {
		t.Fatalf("no raw HTML file in cache dir: %v", entries)
	}
	if !hasMD {
		t.Fatalf("no markdown file in cache dir: %v", entries)
	}
}

func TestWebFetchTool_JSONContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status": "ok", "data": [1, 2, 3]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()

	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "wf1",
					Name:      "web_fetch",
					Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "What is the status?"}`, srv.URL)),
				})
			},
			// Cheap model call.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("The status is ok.")}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("Status is ok.")}
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	out, err := sess.ProcessInput(context.Background(), "Check the JSON API")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("unexpected output: %s", out)
	}

	// Verify raw file was written (no markdown conversion for JSON).
	cacheDir := filepath.Join(dir, ".serf", "web_cache")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("reading cache dir: %v", err)
	}
	var hasRaw bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-raw.json") {
			hasRaw = true
		}
	}
	if !hasRaw {
		t.Fatalf("no raw JSON file in cache dir: %v", entries)
	}
}

func TestWebFetchTool_InvalidURL(t *testing.T) {
	dir := t.TempDir()

	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "wf1",
					Name:      "web_fetch",
					Arguments: json.RawMessage(`{"url": "ftp://example.com", "question": "test"}`),
				})
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("Got an error.")}
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Should not crash; the tool should return an error for non-http(s) URLs.
	_, err = sess.ProcessInput(context.Background(), "Fetch ftp site")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
}
