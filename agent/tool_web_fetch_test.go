package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
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

	// Sample dates before and after the SUT call to tolerate a midnight boundary crossing.
	dateBefore := time.Now().UTC().Format("2006-01-02")
	p := webFetchCachePath("https://example.com/docs")
	dateAfter := time.Now().UTC().Format("2006-01-02")

	// Should contain a valid UTC date (either side of any midnight boundary).
	if !strings.Contains(p, dateBefore) && !strings.Contains(p, dateAfter) {
		t.Fatalf("cache path %q missing today's date (before=%q, after=%q)", p, dateBefore, dateAfter)
	}

	// Should be an absolute path under $XDG_CACHE_HOME/evener/web_cache/date/hash.
	wantKey := webFetchCacheKey("https://example.com/docs")
	// Use whichever date the SUT actually embedded in the path.
	usedDate := dateBefore
	if strings.Contains(p, dateAfter) {
		usedDate = dateAfter
	}
	want := filepath.Join(xdg, "evener", "web_cache", usedDate, wantKey)
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

	// Sample date before the fetch so we can tolerate a midnight boundary crossing.
	dateBefore := time.Now().UTC().Format("2006-01-02")
	out, err := sess.ProcessInput(context.Background(), "Fetch the test page", nil)
	dateAfter := time.Now().UTC().Format("2006-01-02")
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
	sysText := sysTextSb171.String()
	userText := userTextSb171.String()
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
	// Try both dates to tolerate a midnight boundary crossing during the fetch.
	cacheKey := webFetchCacheKey(srv.URL)
	fetchDir := filepath.Join(cacheHome, "evener", "web_cache", dateBefore, cacheKey)
	if _, err := os.Stat(filepath.Join(fetchDir, "raw.html")); os.IsNotExist(err) {
		fetchDir = filepath.Join(cacheHome, "evener", "web_cache", dateAfter, cacheKey)
	}

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

	// Sample date before the fetch so we can tolerate a midnight boundary crossing.
	dateBefore := time.Now().UTC().Format("2006-01-02")
	out, err := sess.ProcessInput(context.Background(), "Check the JSON API", nil)
	dateAfter := time.Now().UTC().Format("2006-01-02")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("unexpected output: %s", out)
	}

	// Verify raw.json was written (no rendered.md for JSON).
	// Try both dates to tolerate a midnight boundary crossing during the fetch.
	cacheKey := webFetchCacheKey(srv.URL)
	fetchDir := filepath.Join(cacheHome, "evener", "web_cache", dateBefore, cacheKey)
	if _, err := os.Stat(filepath.Join(fetchDir, "raw.json")); os.IsNotExist(err) {
		fetchDir = filepath.Join(cacheHome, "evener", "web_cache", dateAfter, cacheKey)
	}

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

func TestWebFetchTool_HTTP404RetriesWithAlternateUserAgent(t *testing.T) {
	var requestCount int
	var userAgents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		userAgents = append(userAgents, r.Header.Get("User-Agent"))
		if requestCount == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, "doc-site wall")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><body><h1>Recovered page</h1></body></html>`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("recovered answer")}
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

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "wf1",
		Name:      "web_fetch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"url": %q, "question": "test"}`, srv.URL)),
	})
	if res.IsError {
		t.Fatalf("web_fetch returned error after alternate-UA recovery: %s", res.Output)
	}
	if !strings.Contains(res.Output, "recovered answer") {
		t.Fatalf("output missing recovered answer: %s", res.Output)
	}
	if requestCount != 2 {
		t.Fatalf("HTTP request count = %d, want one retry after 404", requestCount)
	}
	if len(userAgents) != 2 || userAgents[0] != "evener/1.0" {
		t.Fatalf("User-Agent sequence = %q, want initial evener/1.0 then alternate", userAgents)
	}
	if !strings.HasPrefix(userAgents[1], "Mozilla/5.0") {
		t.Fatalf("alternate User-Agent = %q, want browser-shaped User-Agent", userAgents[1])
	}
	rawPaths, err := filepath.Glob(filepath.Join(cacheHome, "evener", "web_cache", "*", "*", "raw.html"))
	if err != nil {
		t.Fatalf("glob recovered cache: %v", err)
	}
	if len(rawPaths) != 1 {
		t.Fatalf("recovered raw cache paths = %v, want one successful-response cache", rawPaths)
	}
	raw, err := os.ReadFile(rawPaths[0])
	if err != nil {
		t.Fatalf("read recovered raw cache: %v", err)
	}
	if !strings.Contains(string(raw), "Recovered page") {
		t.Fatalf("raw cache did not contain alternate-UA response: %q", raw)
	}
}

type webFetchTrackingBody struct {
	io.ReadCloser
	closed bool
}

func (b *webFetchTrackingBody) Close() error {
	b.closed = true
	return b.ReadCloser.Close()
}

type webFetchTrackingTransport struct {
	base                       http.RoundTripper
	requests                   []*http.Request
	bodies                     []*webFetchTrackingBody
	firstBodyClosedBeforeRetry bool
}

func (t *webFetchTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.requests) == 1 && len(t.bodies) == 1 {
		t.firstBodyClosedBeforeRetry = t.bodies[0].closed
	}
	t.requests = append(t.requests, req)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body := &webFetchTrackingBody{ReadCloser: resp.Body}
	t.bodies = append(t.bodies, body)
	resp.Body = body
	return resp, nil
}

func TestWebFetch_Retries403WithAlternateUserAgentAndClosesBodies(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, "doc-site wall")
			return
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><body><h1>Recovered 403 page</h1></body></html>`)
	}))
	defer srv.Close()

	transport := &webFetchTrackingTransport{base: http.DefaultTransport}
	adapter := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("recovered 403 answer")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("test-model"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	sess.httpClient = &http.Client{Transport: transport}

	got, err := sess.webFetch(context.Background(), srv.URL, "What recovered?")
	if err != nil {
		t.Fatalf("webFetch: %v", err)
	}
	result, ok := got.(map[string]any)
	if !ok || result["answer"] != "recovered 403 answer" {
		t.Fatalf("webFetch result = %#v, want recovered answer", got)
	}
	if requestCount != 2 || len(transport.requests) != 2 {
		t.Fatalf("HTTP requests = %d, transport requests = %d, want exactly one retry", requestCount, len(transport.requests))
	}
	if !transport.firstBodyClosedBeforeRetry {
		t.Fatal("403 response body was not closed before the alternate-UA retry")
	}
	for i, body := range transport.bodies {
		if !body.closed {
			t.Errorf("response body %d was not closed", i)
		}
	}
}

func TestWebFetch_DocWallRetryIsBoundedAndOtherStatusIsNotRetried(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "persistent 404", status: http.StatusNotFound},
		{name: "500 control", status: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requestCount int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, "failure body")
			}))
			t.Cleanup(srv.Close)

			adapter := &agenttest.ModelTrackingAdapter{
				Provider: "openai",
				Respond: func(req llm.Request) (llm.Response, error) {
					return finalResponse("unexpected model call"), nil
				},
			}
			client := llm.NewClient()
			client.Register(adapter)
			sess, err := NewSession(client, NewOpenAIProfile("test-model"),
				execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			t.Cleanup(sess.Close)

			if _, err := sess.webFetch(context.Background(), srv.URL, "question"); err == nil {
				t.Fatalf("webFetch succeeded for HTTP %d", tc.status)
			} else if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.status)) {
				t.Fatalf("webFetch error = %v, want HTTP %d", err, tc.status)
			}
			wantRequests := 1
			if tc.status == http.StatusNotFound {
				wantRequests = 2
			}
			if requestCount != wantRequests {
				t.Fatalf("HTTP request count = %d, want %d", requestCount, wantRequests)
			}
		})
	}
}

func TestWebFetch_RawFallbackWhenBothModelsRefuse(t *testing.T) {
	const wantRawLimit = 20_000
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<html><body><h1>Untrusted source</h1><p>%s</p></body></html>", strings.Repeat("converted-content ", wantRawLimit))
	}))
	t.Cleanup(page.Close)

	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(req llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", http.StatusBadRequest,
			"The provided model identifier is invalid.", nil, nil)
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("test-model"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	got, err := sess.webFetch(context.Background(), page.URL, "What is this source?")
	if err != nil {
		t.Fatalf("webFetch: %v", err)
	}
	result, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("webFetch result = %T, want map[string]any", got)
	}
	if result["fallback"] != "raw" {
		t.Fatalf("fallback = %#v, want raw", result["fallback"])
	}
	if result["url"] != page.URL {
		t.Fatalf("url = %#v, want %q", result["url"], page.URL)
	}
	if result["content_source"] != "converted_markdown" {
		t.Fatalf("content_source = %#v, want converted_markdown", result["content_source"])
	}
	if result["fallback_reason"] != "both_models_refused" {
		t.Fatalf("fallback_reason = %#v, want both_models_refused", result["fallback_reason"])
	}
	if result["content_untrusted"] != true {
		t.Fatalf("content_untrusted = %#v, want true", result["content_untrusted"])
	}
	content, ok := result["content"].(string)
	if !ok {
		t.Fatalf("content = %T, want string", result["content"])
	}
	if got := len([]rune(content)); got != wantRawLimit {
		t.Fatalf("raw content length = %d, want deterministic limit %d", got, wantRawLimit)
	}
	if strings.Contains(content, "<h1>") || !strings.Contains(content, "Untrusted source") {
		t.Fatalf("raw fallback did not preserve converted source content: %q", content[:min(len(content), 80)])
	}
	if result["content_truncated"] != true {
		t.Fatalf("content_truncated = %#v, want true", result["content_truncated"])
	}
	if result["content_limit"] != wantRawLimit {
		t.Fatalf("content_limit = %#v, want %d", result["content_limit"], wantRawLimit)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "test-model"}; !slices.Equal(got, want) {
		t.Fatalf("models addressed = %v, want first and second refusals %v", got, want)
	}
}

func TestWebFetch_NonRefusalModelErrorRemainsError(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "content")
	}))
	t.Cleanup(page.Close)

	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(req llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", http.StatusBadRequest,
			"invalid field: response_format", nil, nil)
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("test-model"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	if _, err := sess.webFetch(context.Background(), page.URL, "question"); err == nil {
		t.Fatal("webFetch succeeded, want non-refusal model error")
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano"}; !slices.Equal(got, want) {
		t.Fatalf("models addressed = %v, want no fallback %v", got, want)
	}
}
