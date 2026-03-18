package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	req := Request{
		Model:    "test-model",
		Provider: "test",
		Messages: []Message{User("hi"), Assistant("hello")},
		Tools: []ToolDefinition{
			{Name: "shell", Description: "run shell commands"},
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
		return Response{}, fmt.Errorf("rate limited")
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

func TestAPILoggerWrapStreamPassthrough(t *testing.T) {
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
