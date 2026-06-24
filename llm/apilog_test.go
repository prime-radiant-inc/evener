package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRawHTTPLogEnabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{" yes ", true},
		{"on", true},
		{"0", false},
		{"false", false},
		{"off", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			if got := rawHTTPLogEnabled(tc.value); got != tc.want {
				t.Fatalf("rawHTTPLogEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestBuildAPILogRequest_IncludesContinuationMetadata(t *testing.T) {
	req := Request{
		Model:       "gpt-5.2",
		Provider:    "openai",
		Messages:    []Message{User("hi")},
		HistoryMode: HistoryModeResponsesDelta,
		Continuation: &ContinuationMetadata{
			AttemptIndex:            1,
			PreviousResponseIDHash:  "cont-handle-v1:response_id:abc",
			ConversationIDHash:      "cont-handle-v1:conversation_id:def",
			AnchorTurnIndex:         3,
			DeltaTurnCount:          1,
			DeltaTurnKinds:          []string{"TOOL_RESULTS"},
			EndpointFamily:          string(ResponsesEndpointFamilyOpenAIPublic),
			RequestFingerprint:      "cont-req-v1:abc",
			ContextMarker:           "cont-ctx-v1",
			StoragePolicyLabel:      "public-openai-store",
			StorageScopeFingerprint: "cont-scope-v1:abc",
			ChatFallbackHistoryLen:  7,
		},
	}
	got := BuildAPILogRequest(req)
	if got.HistoryMode != HistoryModeResponsesDelta {
		t.Fatalf("HistoryMode = %q", got.HistoryMode)
	}
	if got.AttemptIndex != 1 ||
		got.PreviousResponseIDHash != "cont-handle-v1:response_id:abc" ||
		got.ConversationIDHash != "cont-handle-v1:conversation_id:def" ||
		got.AnchorTurnIndex != 3 ||
		got.DeltaTurnCount != 1 ||
		got.ChatFallbackHistoryLen != 7 {
		t.Fatalf("continuation counters/handles not copied: %+v", got)
	}
	if got.EndpointFamily != string(ResponsesEndpointFamilyOpenAIPublic) ||
		got.RequestFingerprint != "cont-req-v1:abc" ||
		got.ContextMarker != "cont-ctx-v1" ||
		got.StoragePolicyLabel != "public-openai-store" ||
		got.StorageScopeFingerprint != "cont-scope-v1:abc" {
		t.Fatalf("continuation metadata not copied: %+v", got)
	}
	if len(got.DeltaTurnKinds) != 1 || got.DeltaTurnKinds[0] != "TOOL_RESULTS" {
		t.Fatalf("DeltaTurnKinds = %#v", got.DeltaTurnKinds)
	}
}

func TestAPILogEntry_AttemptFieldsRoundTrip(t *testing.T) {
	finalCount := 1
	entry := APILogEntry{
		SessionID:         "sess",
		Round:             2,
		AttemptIndex:      1,
		AttemptCount:      1,
		FinalAttemptCount: &finalCount,
		HistoryMode:       HistoryModeFullHistory,
		Request: APILogRequest{
			Model:       "gpt-5.2",
			Provider:    "openai",
			HistoryMode: HistoryModeFullHistory,
		},
		Response: &APILogResponse{
			ID:     "resp_raw_local",
			IDHash: "cont-handle-v1:response_id:abc",
			Model:  "gpt-5.2",
		},
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got APILogEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AttemptIndex != 1 || got.AttemptCount != 1 || got.HistoryMode != HistoryModeFullHistory {
		t.Fatalf("attempt fields = %+v", got)
	}
	if got.FinalAttemptCount == nil || *got.FinalAttemptCount != 1 {
		t.Fatalf("FinalAttemptCount = %v", got.FinalAttemptCount)
	}
	if got.Response == nil || got.Response.IDHash != "cont-handle-v1:response_id:abc" {
		t.Fatalf("response = %+v", got.Response)
	}
}

func TestAPILoggerWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	// Fake adapter that returns a canned response.
	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			ID:       "resp-123",
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop, Raw: "stop"},
			Usage:    Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
			Raw:      map[string]any{"output": []any{"item1"}},
			Message: Message{
				Role:    RoleAssistant,
				Content: []ContentPart{{Kind: ContentText, Text: "Hello world"}},
			},
		}, nil
	}

	wrapped := logger.WrapComplete(fakeAdapter)

	ctx := WithAPILogContext(context.Background(), "sess-abc", 5)
	strict := true
	req := Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi"), Assistant("hello")},
		Tools: []ToolDefinition{
			{
				Name:        "shell",
				Description: "run shell commands",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
					"required": []any{"command"},
				},
				Strict: &strict,
			},
			{Name: "read_file", Description: "read a file"},
		},
	}

	resp, err := wrapped(ctx, req)
	if err != nil {
		t.Fatalf("wrapped call failed: %v", err)
	}
	if resp.ID != "resp-123" {
		t.Errorf("response not passed through: got ID %q", resp.ID)
	}

	logger.Close()

	// Read back the JSONL file and verify.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("no lines in log file")
	}

	var entry APILogEntry
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}

	// Context values.
	if entry.SessionID != "sess-abc" {
		t.Errorf("session_id = %q, want %q", entry.SessionID, "sess-abc")
	}
	if entry.Round != 5 {
		t.Errorf("round = %d, want 5", entry.Round)
	}

	// Request metadata.
	if entry.Request.Model != "test-model" {
		t.Errorf("request.model = %q, want %q", entry.Request.Model, "test-model")
	}
	if entry.Request.Provider != "test" {
		t.Errorf("request.provider = %q, want %q", entry.Request.Provider, "test")
	}
	if entry.Request.MessageCount != 2 {
		t.Errorf("request.message_count = %d, want 2", entry.Request.MessageCount)
	}
	if entry.Request.ToolCount != 2 {
		t.Errorf("request.tool_count = %d, want 2", entry.Request.ToolCount)
	}
	if len(entry.Request.ToolNames) != 2 || entry.Request.ToolNames[0] != "shell" {
		t.Errorf("request.tool_names = %v, want [shell read_file]", entry.Request.ToolNames)
	}
	if len(entry.Request.Tools) != 2 || entry.Request.Tools[0].Name != "shell" {
		t.Fatalf("request.tools = %+v, want full tool definitions", entry.Request.Tools)
	}
	if entry.Request.Tools[0].Description != "run shell commands" {
		t.Errorf("request.tools[0].description = %q", entry.Request.Tools[0].Description)
	}
	if entry.Request.Tools[0].Parameters["type"] != "object" {
		t.Errorf("request.tools[0].parameters = %+v", entry.Request.Tools[0].Parameters)
	}
	if entry.Request.Tools[0].Strict == nil || !*entry.Request.Tools[0].Strict {
		t.Errorf("request.tools[0].strict = %v, want true", entry.Request.Tools[0].Strict)
	}

	// Response.
	if entry.Response == nil {
		t.Fatal("response is nil")
	}
	if entry.Response.ID != "resp-123" {
		t.Errorf("response.id = %q, want %q", entry.Response.ID, "resp-123")
	}
	if entry.Response.FinishReason != "stop" {
		t.Errorf("response.finish_reason = %q, want %q", entry.Response.FinishReason, "stop")
	}
	if entry.Response.TextLength != 11 {
		t.Errorf("response.text_length = %d, want 11", entry.Response.TextLength)
	}
	if entry.Response.ToolCallCount != 0 {
		t.Errorf("response.tool_call_count = %d, want 0", entry.Response.ToolCallCount)
	}
	if entry.Response.Usage.InputTokens != 100 {
		t.Errorf("response.usage.input_tokens = %d, want 100", entry.Response.Usage.InputTokens)
	}
	if entry.Response.Raw == nil {
		t.Error("response.raw is nil, want the raw API response")
	}

	// Timing.
	if entry.Timestamp == "" {
		t.Error("timestamp is empty")
	}
	if entry.LatencyMs < 0 {
		t.Errorf("latency_ms = %d, want >= 0", entry.LatencyMs)
	}
	if entry.Error != "" {
		t.Errorf("error = %q, want empty", entry.Error)
	}
}

func TestAPILoggerLogsErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{}, errors.New("rate limited")
	}

	wrapped := logger.WrapComplete(fakeAdapter)
	_, err = wrapped(context.Background(), Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	logger.Close()

	data, _ := os.ReadFile(path)
	var entry APILogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Error == "" {
		t.Error("error field is empty, want error message")
	}
	if entry.Response != nil {
		t.Error("response should be nil on error")
	}
}

func TestAPILoggerNoContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop},
		}, nil
	}

	// Call without WithAPILogContext — should still log, just without session/round.
	wrapped := logger.WrapComplete(fakeAdapter)
	_, err = wrapped(context.Background(), Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logger.Close()

	data, _ := os.ReadFile(path)
	var entry APILogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.SessionID != "" {
		t.Errorf("session_id = %q, want empty", entry.SessionID)
	}
	if entry.Round != 0 {
		t.Errorf("round = %d, want 0", entry.Round)
	}
}

func TestAPILoggerWrapStreamCallsNext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	called := false
	fakeStream := func(ctx context.Context, req Request) (Stream, error) {
		called = true
		return nil, nil
	}

	wrapped := logger.WrapStream(fakeStream)
	_, _ = wrapped(context.Background(), Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi")},
	})
	if !called {
		t.Error("stream func was not called (passthrough failed)")
	}
}

func TestAPILoggerWrapStreamWritesRawLogOnFinish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	rawPath := filepath.Join(dir, "api-raw.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	if err := logger.EnableRawLogging(rawPath); err != nil {
		t.Fatalf("EnableRawLogging: %v", err)
	}

	wrapped := logger.WrapStream(func(ctx context.Context, req Request) (Stream, error) {
		_ = ctx
		st := NewChanStream(nil)
		go func() {
			defer st.CloseSend()
			resp := Response{
				ID:              "stream-123",
				Model:           req.Model,
				Provider:        req.Provider,
				Message:         Assistant("stream ok"),
				Finish:          FinishReason{Reason: FinishReasonStop},
				Usage:           Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
				Raw:             map[string]any{"endpoint_url": "https://example.test/v1/chat/completions"},
				RawRequestBody:  `{"input":"hi"}`,
				RawResponseBody: "data: done\n\n",
			}
			st.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &resp.Finish, Usage: &resp.Usage, Response: &resp})
		}()
		return st, nil
	})

	st, err := wrapped(WithAPILogContext(context.Background(), "sess-stream", 9), Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range st.Events() {
	}
	if err := st.Close(); err != nil {
		t.Fatalf("stream Close: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("logger Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	var entry APILogEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("unmarshal api.jsonl: %v\n%s", err, string(data))
	}
	if entry.SessionID != "sess-stream" || entry.Round != 9 {
		t.Fatalf("api context = %q/%d, want sess-stream/9", entry.SessionID, entry.Round)
	}
	if entry.Response == nil || entry.Response.EndpointURL != "https://example.test/v1/chat/completions" {
		t.Fatalf("api response = %+v, want endpoint URL", entry.Response)
	}

	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read api-raw.jsonl: %v", err)
	}
	var rawEntry APIRawLogEntry
	if err := json.Unmarshal(bytes.TrimSpace(rawData), &rawEntry); err != nil {
		t.Fatalf("unmarshal api-raw.jsonl: %v\n%s", err, string(rawData))
	}
	if rawEntry.Mode != "stream" {
		t.Fatalf("raw mode = %q, want stream", rawEntry.Mode)
	}
	if rawEntry.RequestBody != `{"input":"hi"}` || rawEntry.ResponseBody != "data: done\n\n" {
		t.Fatalf("raw bodies = %q / %q", rawEntry.RequestBody, rawEntry.ResponseBody)
	}
}

func TestAPILoggerReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop},
		}, nil
	}

	wrapped := logger.WrapComplete(fakeAdapter)
	effort := "high"
	_, err = wrapped(context.Background(), Request{
		Model:           "test-model",
		Provider:        "test",
		Messages:        []Message{User("hi")},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logger.Close()

	data, _ := os.ReadFile(path)
	var entry APILogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Request.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want %q", entry.Request.ReasoningEffort, "high")
	}
}

// --- Periodic sync tests ---

// TestBuildLogResponse_EndpointURL covers the promotion of Raw["endpoint_url"]
// to APILogResponse.EndpointURL. Adapters stash the dialed URL in Raw so QA
// can disambiguate, e.g., OpenAI API-key (/v1/responses) vs ChatGPT OAuth
// (/backend-api/codex/responses) calls from the transcript alone.
func TestBuildLogResponse_EndpointURL(t *testing.T) {
	cases := []struct {
		name     string
		raw      map[string]any
		expected string
	}{
		{
			name:     "string value is promoted",
			raw:      map[string]any{"endpoint_url": "https://example.com/api"},
			expected: "https://example.com/api",
		},
		{
			name:     "missing key yields empty",
			raw:      map[string]any{"other": 1},
			expected: "",
		},
		{
			name:     "non-string value is ignored",
			raw:      map[string]any{"endpoint_url": 42},
			expected: "",
		},
		{
			name:     "nil raw yields empty",
			raw:      nil,
			expected: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := Response{
				ID:     "r1",
				Model:  "m",
				Raw:    tc.raw,
				Finish: FinishReason{Reason: FinishReasonStop},
			}
			lr := buildLogResponse(resp)
			if lr.EndpointURL != tc.expected {
				t.Fatalf("EndpointURL = %q, want %q", lr.EndpointURL, tc.expected)
			}
		})
	}
}

// TestAPILogger_EndpointURL_RoundTrip ensures the field appears in the
// serialized JSONL entry exactly when Raw["endpoint_url"] was populated.
func TestAPILogger_EndpointURL_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:  "m",
			Finish: FinishReason{Reason: FinishReasonStop},
			Raw:    map[string]any{"endpoint_url": "https://api.example.com/v1/responses"},
		}, nil
	}
	wrapped := logger.WrapComplete(fakeAdapter)
	if _, err := wrapped(context.Background(), Request{Model: "m", Provider: "p", Messages: []Message{User("hi")}}); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	logger.Close()

	data, _ := os.ReadFile(path)
	var entry APILogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Response == nil {
		t.Fatal("response is nil")
	}
	if entry.Response.EndpointURL != "https://api.example.com/v1/responses" {
		t.Errorf("EndpointURL = %q, want %q", entry.Response.EndpointURL, "https://api.example.com/v1/responses")
	}
	// And confirm omitempty: a separate entry without the key should not include the field.
	if !bytes.Contains(data, []byte(`"endpoint_url":"https://api.example.com/v1/responses"`)) {
		t.Errorf("serialized entry missing endpoint_url field: %s", string(data))
	}
}

func TestAPILogger_PeriodicSync_SkipsSyncWithinInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	// Long interval so rapid writes don't trigger sync.
	logger.SyncInterval = 1 * time.Hour

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop},
		}, nil
	}

	wrapped := logger.WrapComplete(fakeAdapter)

	// Make several rapid calls.
	for i := 0; i < 5; i++ {
		_, err := wrapped(context.Background(), Request{
			Model:    "test-model",
			Provider: "test",
			Messages: []Message{User("hi")},
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	// dirty should be true (writes happened but no sync).
	logger.mu.Lock()
	dirty := logger.dirty
	logger.mu.Unlock()
	if !dirty {
		t.Error("expected dirty=true after writes within sync interval")
	}

	// Close should flush.
	logger.Close()

	// All data should be readable.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 5 {
		t.Fatalf("expected 5 lines, got %d", lines)
	}
}

func TestAPILogger_PeriodicSync_ZeroIntervalSyncsEveryWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	// Zero interval = sync every write (backward compat).
	logger.SyncInterval = 0

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop},
		}, nil
	}

	wrapped := logger.WrapComplete(fakeAdapter)

	for i := 0; i < 3; i++ {
		_, err := wrapped(context.Background(), Request{
			Model:    "test-model",
			Provider: "test",
			Messages: []Message{User("hi")},
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		// After each write with zero interval, dirty should be false.
		logger.mu.Lock()
		dirty := logger.dirty
		logger.mu.Unlock()
		if dirty {
			t.Errorf("call %d: expected dirty=false with zero SyncInterval", i)
		}
	}
}

func TestAPILogger_PeriodicSync_CloseFlushesDirtyWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}

	// Long interval so no auto-sync.
	logger.SyncInterval = 1 * time.Hour

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop},
		}, nil
	}

	wrapped := logger.WrapComplete(fakeAdapter)

	for i := 0; i < 5; i++ {
		_, err := wrapped(context.Background(), Request{
			Model:    "test-model",
			Provider: "test",
			Messages: []Message{User("hi")},
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	// Close must flush.
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// All entries must be readable.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 5 {
		t.Fatalf("expected 5 lines, got %d", lines)
	}

	// Each line should parse as valid JSON.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var entry APILogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
}

func TestAPILogger_PeriodicSync_SyncsAfterIntervalExpires(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close()

	// Very short interval.
	logger.SyncInterval = 1 * time.Millisecond

	fakeAdapter := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Model:    "test-model",
			Provider: "test",
			Finish:   FinishReason{Reason: FinishReasonStop},
		}, nil
	}

	wrapped := logger.WrapComplete(fakeAdapter)

	// First call.
	_, _ = wrapped(context.Background(), Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi")},
	})

	// Wait for interval to expire.
	time.Sleep(5 * time.Millisecond)

	// Second call should trigger sync.
	_, _ = wrapped(context.Background(), Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hello")},
	})

	// dirty should be false after sync.
	logger.mu.Lock()
	dirty := logger.dirty
	logger.mu.Unlock()
	if dirty {
		t.Error("expected dirty=false after sync interval elapsed")
	}
}

// TestStampEndpointURL pins the shared write-side helper that every provider
// adapter uses to record the dialed URL. buildLogResponse later promotes
// Raw["endpoint_url"] to a top-level transcript field (see
// TestBuildLogResponse_EndpointURL for the read side).
func TestStampEndpointURL(t *testing.T) {
	t.Run("nil resp is a no-op", func(t *testing.T) {
		// Must not panic.
		StampEndpointURL(nil, "https://example.com/api")
	})

	t.Run("empty endpoint leaves Raw untouched", func(t *testing.T) {
		resp := &Response{}
		StampEndpointURL(resp, "")
		if resp.Raw != nil {
			t.Fatalf("Raw = %v, want nil for empty endpoint", resp.Raw)
		}
	})

	t.Run("nil Raw is initialised and stamped", func(t *testing.T) {
		resp := &Response{}
		StampEndpointURL(resp, "https://example.com/api")
		if got, _ := resp.Raw["endpoint_url"].(string); got != "https://example.com/api" {
			t.Fatalf("Raw[endpoint_url] = %q, want %q", got, "https://example.com/api")
		}
	})

	t.Run("existing Raw entries are preserved", func(t *testing.T) {
		resp := &Response{Raw: map[string]any{"other": 1}}
		StampEndpointURL(resp, "https://example.com/api")
		if got, _ := resp.Raw["endpoint_url"].(string); got != "https://example.com/api" {
			t.Fatalf("Raw[endpoint_url] = %q, want %q", got, "https://example.com/api")
		}
		if resp.Raw["other"] != 1 {
			t.Fatalf("Raw[other] = %v, want 1 (existing entry clobbered)", resp.Raw["other"])
		}
	})
}
