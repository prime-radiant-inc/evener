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
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
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

func TestWebFetchCachePath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)

	p := webFetchCachePath("https://example.com/docs")

	// Should contain today's date.
	today := time.Now().UTC().Format("2006-01-02")
	if !strings.Contains(p, today) {
		t.Fatalf("cache path %q missing today's date %q", p, today)
	}

	// Should be an absolute path under $XDG_CACHE_HOME/serf/web_cache/date/hash.
	wantKey := webFetchCacheKey("https://example.com/docs")
	want := filepath.Join(xdg, "serf", "web_cache", today, wantKey)
	if p != want {
		t.Fatalf("cache path:\n  got  %q\n  want %q", p, want)
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

func TestExtFromContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want string
	}{
		{"text/html; charset=utf-8", ".html"},
		{"text/HTML", ".html"},
		{"application/json", ".json"},
		{"text/plain", ".txt"},
		{"text/xml", ".xml"},
		{"application/xml", ".xml"},
		{"application/octet-stream", ".bin"},
		{"", ".bin"},
	}
	for _, tc := range cases {
		got := extFromContentType(tc.ct)
		if got != tc.want {
			t.Errorf("extFromContentType(%q) = %q, want %q", tc.ct, got, tc.want)
		}
	}
}

func toolCallResponse(calls ...llm.ToolCallData) llm.Response {
	parts := make([]llm.ContentPart, len(calls))
	for i, c := range calls {
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
		_, _ = fmt.Fprint(w, `<html><body><h1>Test Page</h1><p>This is a test document about Go programming.</p></body></html>`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

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
				return finalResponse("This page is about Go programming.")
			},
			// Step 2: the main agent receives the web_fetch result and responds.
			func(req llm.Request) llm.Response {
				return finalResponse("The page is about Go programming.")
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	out, err := sess.ProcessInput(context.Background(), "Fetch the test page", nil)
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
	var sysTextSb171 strings.Builder
	var userTextSb171 strings.Builder
	for _, m := range cheapReq.Messages {
		if m.Role == "system" {
			sysTextSb171.WriteString(m.Text())
		}
		if m.Role == "user" {
			userTextSb171.WriteString(m.Text())
		}
	}
	sysText += sysTextSb171.String()
	userText += userTextSb171.String()
	if !strings.Contains(sysText, "web content") {
		t.Fatalf("cheap model system prompt missing expected text: %s", sysText)
	}
	if !strings.Contains(userText, "What is this page about?") {
		t.Fatalf("cheap model user message missing question: %s", userText)
	}
	if !strings.Contains(userText, "Test Page") {
		t.Fatalf("cheap model user message missing page content: %s", userText)
	}

	// Verify cache files use date-bucketed directory structure under XDG cache.
	today := time.Now().UTC().Format("2006-01-02")
	cacheKey := webFetchCacheKey(srv.URL)
	fetchDir := filepath.Join(cacheHome, "serf", "web_cache", today, cacheKey)

	rawPath := filepath.Join(fetchDir, "raw.html")
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		t.Fatalf("raw.html not found at %s", rawPath)
	}

	mdPath := filepath.Join(fetchDir, "rendered.md")
	if _, err := os.Stat(mdPath); os.IsNotExist(err) {
		t.Fatalf("rendered.md not found at %s", mdPath)
	}

	// Verify rendered.md contains converted content, not raw HTML.
	mdContent, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("reading rendered.md: %v", err)
	}
	if strings.Contains(string(mdContent), "<h1>") {
		t.Fatalf("rendered.md contains raw HTML tags")
	}
	if !strings.Contains(string(mdContent), "Test Page") {
		t.Fatalf("rendered.md missing page content")
	}
}

func TestWebFetchTool_ResultContainsFilePaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body><h1>Paths Test</h1></body></html>`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "wf1",
					Name:      "web_fetch",
					Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "test"}`, srv.URL)),
				})
			},
			func(req llm.Request) llm.Response {
				return finalResponse("answer")
			},
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Execute the web_fetch tool directly via the registry so we can inspect the output.
	ctx := context.Background()
	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "wf1",
		Name:      "web_fetch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "test"}`, srv.URL)),
	})
	if res.IsError {
		t.Fatalf("web_fetch returned error: %s", res.Output)
	}

	// The tool output should contain file paths and the answer.
	if !strings.Contains(res.Output, "raw_file") {
		t.Fatalf("output missing raw_file key: %s", res.Output)
	}
	if !strings.Contains(res.Output, "markdown_file") {
		t.Fatalf("output missing markdown_file key: %s", res.Output)
	}
	if !strings.Contains(res.Output, "rendered.md") {
		t.Fatalf("output missing rendered.md path: %s", res.Output)
	}
	if !strings.Contains(res.Output, "raw.html") {
		t.Fatalf("output missing raw.html path: %s", res.Output)
	}
	if !strings.Contains(res.Output, "answer") {
		t.Fatalf("output missing answer key: %s", res.Output)
	}
}

func TestWebFetchTool_JSONContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status": "ok", "data": [1, 2, 3]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

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
				return finalResponse("The status is ok.")
			},
			func(req llm.Request) llm.Response {
				return finalResponse("Status is ok.")
			},
		},
	}

	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	out, err := sess.ProcessInput(context.Background(), "Check the JSON API", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("unexpected output: %s", out)
	}

	// Verify raw.json was written (no rendered.md for JSON).
	today := time.Now().UTC().Format("2006-01-02")
	cacheKey := webFetchCacheKey(srv.URL)
	fetchDir := filepath.Join(cacheHome, "serf", "web_cache", today, cacheKey)

	rawPath := filepath.Join(fetchDir, "raw.json")
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		t.Fatalf("raw.json not found at %s", rawPath)
	}

	mdPath := filepath.Join(fetchDir, "rendered.md")
	if _, err := os.Stat(mdPath); !os.IsNotExist(err) {
		t.Fatalf("rendered.md should not exist for JSON content, but found at %s", mdPath)
	}
}

func TestWebFetchTool_InvalidURL(t *testing.T) {
	dir := t.TempDir()

	fa := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Execute web_fetch directly with an invalid scheme.
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "wf1",
		Name:      "web_fetch",
		Arguments: json.RawMessage(`{"url": "ftp://example.com", "question": "test"}`),
	})
	if !res.IsError {
		t.Fatalf("expected error for ftp:// URL, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "unsupported URL scheme") {
		t.Fatalf("error should mention unsupported scheme: %s", res.Output)
	}
}

func TestWebFetchTool_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	fa := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "wf1",
		Name:      "web_fetch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "test"}`, srv.URL)),
	})
	if !res.IsError {
		t.Fatalf("expected error for 404, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "404") {
		t.Fatalf("error should mention 404: %s", res.Output)
	}
}
