package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

// readTranscriptLines reads all non-empty lines from a JSONL transcript file.
func readTranscriptLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript file: %v", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			t.Fatal("unexpected blank line in transcript")
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript file: %v", err)
	}
	return lines
}

func TestTranscriptJSONLMaxLineCoversMaxImagePayload(t *testing.T) {
	t.Parallel()
	const (
		maxImages     = 8
		maxImageBytes = 8 * 1024 * 1024
		jsonHeadroom  = 1024 * 1024
	)
	encodedImageBytes := ((maxImageBytes + 2) / 3) * 4
	encodedPayloadBytes := maxImages*encodedImageBytes + jsonHeadroom
	if transcriptJSONLMaxLineBytes < encodedPayloadBytes {
		t.Fatalf("transcriptJSONLMaxLineBytes=%d, want at least %d", transcriptJSONLMaxLineBytes, encodedPayloadBytes)
	}
}

func TestTranscriptWriter_CreatesFileAndWritesHeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "transcript.jsonl")

	header := transcript.Header{
		SessionID:  "sess-001",
		CreatedAt:  time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
		ProfileID:  "anthropic-default",
		Model:      "claude-opus-4-6",
		WorkingDir: "/tmp/test",
	}

	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	defer w.Close()

	lines := readTranscriptLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (header), got %d", len(lines))
	}

	var got transcript.Header
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if got.Kind != "header" {
		t.Errorf("header kind = %q, want %q", got.Kind, "header")
	}
	if got.FormatVersion != transcript.FormatVersion {
		t.Errorf("format_version = %d, want %d", got.FormatVersion, transcript.FormatVersion)
	}
	if got.SessionID != "sess-001" {
		t.Errorf("session_id = %q, want %q", got.SessionID, "sess-001")
	}
	if got.ProfileID != "anthropic-default" {
		t.Errorf("profile_id = %q, want %q", got.ProfileID, "anthropic-default")
	}
	if got.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", got.Model, "claude-opus-4-6")
	}
	if got.WorkingDir != "/tmp/test" {
		t.Errorf("working_dir = %q, want %q", got.WorkingDir, "/tmp/test")
	}
}

func TestTranscriptWriter_AppendWritesEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID: "sess-002",
		CreatedAt: time.Now().UTC(),
		ProfileID: "openai-default",
		Model:     "gpt-5",
	}

	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	defer w.Close()

	turn1 := schema.NewTurn(schema.TurnUserInput, llm.User("Hello"))
	turn2 := schema.NewTurn(schema.TurnAssistant, llm.Assistant("Hi there"))

	if err := w.Append(turn1); err != nil {
		t.Fatalf("Append turn1: %v", err)
	}
	if err := w.Append(turn2); err != nil {
		t.Fatalf("Append turn2: %v", err)
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 3 { // header + 2 entries
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	// Verify entry at line 2 (index 1)
	var entry0 transcript.Entry
	if err := json.Unmarshal([]byte(lines[1]), &entry0); err != nil {
		t.Fatalf("unmarshal entry 0: %v", err)
	}
	if entry0.Kind != "entry" {
		t.Errorf("entry0 kind = %q, want %q", entry0.Kind, "entry")
	}
	if entry0.Seq != 0 {
		t.Errorf("entry0 seq = %d, want 0", entry0.Seq)
	}
	if entry0.Turn.Kind != schema.TurnUserInput {
		t.Errorf("entry0 turn kind = %q, want %q", entry0.Turn.Kind, schema.TurnUserInput)
	}
	if entry0.Turn.Message.Text() != "Hello" {
		t.Errorf("entry0 turn text = %q, want %q", entry0.Turn.Message.Text(), "Hello")
	}

	// Verify entry at line 3 (index 2)
	var entry1 transcript.Entry
	if err := json.Unmarshal([]byte(lines[2]), &entry1); err != nil {
		t.Fatalf("unmarshal entry 1: %v", err)
	}
	if entry1.Kind != "entry" {
		t.Errorf("entry1 kind = %q, want %q", entry1.Kind, "entry")
	}
	if entry1.Seq != 1 {
		t.Errorf("entry1 seq = %d, want 1", entry1.Seq)
	}
	if entry1.Turn.Kind != schema.TurnAssistant {
		t.Errorf("entry1 turn kind = %q, want %q", entry1.Turn.Kind, schema.TurnAssistant)
	}
	if entry1.Turn.Message.Text() != "Hi there" {
		t.Errorf("entry1 turn text = %q, want %q", entry1.Turn.Message.Text(), "Hi there")
	}
}

func TestTranscriptWriter_SeqMonotonicallyIncreasing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-003",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	defer w.Close()

	for i := 0; i < 10; i++ {
		turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("msg"))
		if err := w.Append(turn); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 11 { // header + 10 entries
		t.Fatalf("expected 11 lines, got %d", len(lines))
	}

	for i := 1; i <= 10; i++ {
		var entry transcript.Entry
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			t.Fatalf("unmarshal entry %d: %v", i-1, err)
		}
		expectedSeq := i - 1
		if entry.Seq != expectedSeq {
			t.Errorf("entry %d seq = %d, want %d", i-1, entry.Seq, expectedSeq)
		}
	}
}

func TestTranscriptWriter_CloseClosesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-004",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("before close"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// First close should succeed.
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second close should not panic and should not error.
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// File should still be readable after close.
	lines := readTranscriptLines(t, path)
	if len(lines) != 2 { // header + 1 entry
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
}

func TestTranscriptWriter_NilWriterSafe(t *testing.T) {
	t.Parallel()
	var w *transcript.Writer

	// Append on nil should not panic and should return nil.
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("test"))); err != nil {
		t.Errorf("nil Append returned error: %v", err)
	}

	// Close on nil should not panic and should return nil.
	if err := w.Close(); err != nil {
		t.Errorf("nil Close returned error: %v", err)
	}
}

func TestTranscriptWriter_ConcurrentAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-005",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	defer w.Close()

	// Batch fsync so 100 concurrent appends don't each pay durability cost.
	// Close() flushes; read-back goes through the page cache while the writer
	// is open, so the line-count/JSON/seq assertions (mutex-serialized
	// write+seq, not fsync durability) are unaffected.
	w.SyncInterval = time.Hour

	const numGoroutines = 10
	const turnsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < turnsPerGoroutine; j++ {
				turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("concurrent"))
				if err := w.Append(turn); err != nil {
					t.Errorf("goroutine %d append %d: %v", id, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	lines := readTranscriptLines(t, path)
	expectedTotal := 1 + numGoroutines*turnsPerGoroutine // header + 100
	if len(lines) != expectedTotal {
		t.Fatalf("expected %d lines, got %d", expectedTotal, len(lines))
	}

	// Verify every line is valid JSON.
	for i, line := range lines {
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}

	// Verify seq uniqueness
	seqs := map[int]bool{}
	for _, line := range lines[1:] { // skip header
		var entry transcript.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshal entry: %v", err)
		}
		if seqs[entry.Seq] {
			t.Errorf("duplicate seq %d", entry.Seq)
		}
		seqs[entry.Seq] = true
	}
	if len(seqs) != 100 {
		t.Errorf("expected 100 unique seqs, got %d", len(seqs))
	}
}

func TestTranscriptWriter_ValidJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-006",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	defer w.Close()

	// Text content
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("Hello world"))); err != nil {
		t.Fatalf("Append text: %v", err)
	}

	// Tool call content
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Let me check that."},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call-1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"/tmp/foo"}`),
			}},
		},
	})); err != nil {
		t.Fatalf("Append tool call: %v", err)
	}

	// Tool result content
	if err := w.Append(schema.NewTurn(schema.TurnToolResults, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call-1",
				Name:       "read_file",
				Content:    "file contents here",
			}},
		},
	})); err != nil {
		t.Fatalf("Append tool result: %v", err)
	}

	// Thinking content
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				Text: "Let me think about this...",
			}},
			{Kind: llm.ContentText, Text: "Here's what I found."},
		},
	})); err != nil {
		t.Fatalf("Append thinking: %v", err)
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 5 { // header + 4 entries
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// Every line must independently parse as valid JSON.
	for i, line := range lines {
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nline: %s", i, err, line)
		}
	}

	// The header must parse as transcript.Header.
	var hdr transcript.Header
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil {
		t.Fatalf("header does not parse: %v", err)
	}
	if hdr.Kind != "header" {
		t.Errorf("header kind = %q, want %q", hdr.Kind, "header")
	}

	// Each entry must parse as transcript.Entry with incrementing seq.
	for i := 1; i < len(lines); i++ {
		var entry transcript.Entry
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			t.Fatalf("entry %d does not parse: %v", i-1, err)
		}
		if entry.Kind != "entry" {
			t.Errorf("entry %d kind = %q, want %q", i-1, entry.Kind, "entry")
		}
		if entry.Seq != i-1 {
			t.Errorf("entry %d seq = %d, want %d", i-1, entry.Seq, i-1)
		}
	}
}

func TestTranscriptWriter_LargeEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "large.transcript.jsonl")

	hdr := transcript.Header{SessionID: "test-large"}
	tw, err := transcript.NewWriter(path, hdr)
	if err != nil {
		t.Fatal(err)
	}

	// Create a 1MB tool result
	bigContent := strings.Repeat("x", 1024*1024)
	msg := llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_big",
				Content:    bigContent,
			},
		}},
	}
	turn := schema.NewTurn(schema.TurnToolResults, msg)

	if err := tw.Append(turn); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	// Read back and verify
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	// Verify the content survived the round-trip
	got, ok := entries[0].Turn.Message.Content[0].ToolResult.Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", entries[0].Turn.Message.Content[0].ToolResult.Content)
	}
	if got != bigContent {
		t.Errorf("content length mismatch: got %d bytes, want %d", len(got), len(bigContent))
	}
}

// --- readTranscript tests ---

func TestReadTranscript_ReturnsHeaderAndEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID:  "sess-read-001",
		CreatedAt:  time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
		ProfileID:  "anthropic-default",
		Model:      "claude-opus-4-6",
		WorkingDir: "/tmp/test",
	}

	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Hello")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Hi there")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "result", false)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Done")),
		schema.NewTurn(schema.TurnUserInput, llm.User("Thanks")),
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	gotHeader, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if gotHeader.SessionID != "sess-read-001" {
		t.Errorf("header session_id = %q, want %q", gotHeader.SessionID, "sess-read-001")
	}
	if gotHeader.ProfileID != "anthropic-default" {
		t.Errorf("header profile_id = %q, want %q", gotHeader.ProfileID, "anthropic-default")
	}
	if gotHeader.Model != "claude-opus-4-6" {
		t.Errorf("header model = %q, want %q", gotHeader.Model, "claude-opus-4-6")
	}

	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	for i, entry := range entries {
		if entry.Seq != i {
			t.Errorf("entry %d seq = %d, want %d", i, entry.Seq, i)
		}
		if entry.Turn.Kind != turns[i].Kind {
			t.Errorf("entry %d turn kind = %q, want %q", i, entry.Turn.Kind, turns[i].Kind)
		}
	}
}

func TestReadTranscript_PartialLastLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID: "sess-partial",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	w.Close()

	// Append a partial JSON line (no closing brace, no trailing newline) to simulate crash.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.WriteString(`{"kind":"entry","seq":3,"turn":{"kind":"ASSISTANT"`)
	f.Close()

	gotHeader, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if gotHeader.SessionID != "sess-partial" {
		t.Errorf("header session_id = %q, want %q", gotHeader.SessionID, "sess-partial")
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (partial line skipped), got %d", len(entries))
	}

	for i, entry := range entries {
		if entry.Seq != i {
			t.Errorf("entry %d seq = %d, want %d", i, entry.Seq, i)
		}
	}
}

func TestReadTranscript_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")

	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, _, err := readTranscript(path)
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

func TestReadTranscript_HeaderOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID: "sess-header-only",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	w.Close()

	gotHeader, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if gotHeader.SessionID != "sess-header-only" {
		t.Errorf("header session_id = %q, want %q", gotHeader.SessionID, "sess-header-only")
	}

	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// --- transcript.OpenWriter tests ---

func TestOpenTranscriptWriter_AppendsToExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID: "sess-open-001",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	// Write header + 5 entries, then close.
	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	w.Close()

	// Reopen for appending.
	w2, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatalf("transcript.OpenWriter: %v", err)
	}
	defer w2.Close()

	// Append 3 more turns.
	for i := 0; i < 3; i++ {
		if err := w2.Append(schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("input %d", i)))); err != nil {
			t.Fatalf("Append (resumed) %d: %v", i, err)
		}
	}

	// Read back and verify.
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if len(entries) != 8 {
		t.Fatalf("expected 8 entries, got %d", len(entries))
	}

	for i, entry := range entries {
		if entry.Seq != i {
			t.Errorf("entry %d seq = %d, want %d", i, entry.Seq, i)
		}
	}
}

func TestOpenTranscriptWriter_TruncatesPartialLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID: "sess-open-002",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	// Write header + 3 entries, then close.
	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	w.Close()

	// Manually append a partial JSON line (no newline) to simulate crash.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.WriteString(`{"kind":"ent`)
	f.Close()

	// Reopen — partial line should be truncated.
	w2, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatalf("transcript.OpenWriter: %v", err)
	}
	defer w2.Close()

	// Append 1 more turn.
	if err := w2.Append(schema.NewTurn(schema.TurnUserInput, llm.User("after crash"))); err != nil {
		t.Fatalf("Append (after crash): %v", err)
	}

	// Read back and verify.
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries (3 original + 1 new), got %d", len(entries))
	}

	for i, entry := range entries {
		if entry.Seq != i {
			t.Errorf("entry %d seq = %d, want %d", i, entry.Seq, i)
		}
	}
}

func TestOpenTranscriptWriter_HeaderOnlyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := transcript.Header{
		SessionID: "sess-open-003",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	// Write header only, no entries.
	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	w.Close()

	// Reopen.
	w2, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatalf("transcript.OpenWriter: %v", err)
	}
	defer w2.Close()

	// Append one turn.
	if err := w2.Append(schema.NewTurn(schema.TurnUserInput, llm.User("first after header"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read back and verify.
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	if entries[0].Seq != 0 {
		t.Errorf("entry 0 seq = %d, want 0", entries[0].Seq)
	}
	if entries[0].Turn.Message.Text() != "first after header" {
		t.Errorf("entry 0 text = %q, want %q", entries[0].Turn.Message.Text(), "first after header")
	}
}

// --- ResumeHistory tests ---

func TestResumeHistoryFromTranscript_NoCompaction(t *testing.T) {
	t.Parallel()
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("Hello"))},
		{Kind: "entry", Seq: 1, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("Hi"))},
		{Kind: "entry", Seq: 2, Turn: schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "ok", false))},
		{Kind: "entry", Seq: 3, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("Done"))},
		{Kind: "entry", Seq: 4, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("Thanks"))},
	}

	history := ResumeHistory(entries)

	if len(history) != 5 {
		t.Fatalf("expected 5 turns, got %d", len(history))
	}

	expectedKinds := []schema.TurnKind{schema.TurnUserInput, schema.TurnAssistant, schema.TurnToolResults, schema.TurnAssistant, schema.TurnUserInput}
	for i, turn := range history {
		if turn.Kind != expectedKinds[i] {
			t.Errorf("turn %d kind = %q, want %q", i, turn.Kind, expectedKinds[i])
		}
	}
}

func TestResumeHistoryFromTranscript_WithCheckpoint(t *testing.T) {
	t.Parallel()
	entries := make([]transcript.Entry, 10)
	for i := 0; i < 10; i++ {
		kind := schema.TurnAssistant
		msg := llm.Assistant(fmt.Sprintf("msg %d", i))
		switch i {
		case 0, 4, 9:
			kind = schema.TurnUserInput
			msg = llm.User(fmt.Sprintf("input %d", i))
		case 2, 5, 8:
			kind = schema.TurnToolResults
			msg = llm.ToolResult(fmt.Sprintf("call-%d", i), "ok", false)
		case 6:
			kind = schema.TurnCheckpoint
			msg = llm.User("checkpoint summary")
		}
		entries[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: schema.NewTurn(kind, msg)}
	}

	history := ResumeHistory(entries)

	// Should return: checkpoint (index 6), plus entries 7, 8, 9 = 4 turns total
	if len(history) != 4 {
		t.Fatalf("expected 4 turns (checkpoint + 3 after), got %d", len(history))
	}

	if history[0].Kind != schema.TurnCheckpoint {
		t.Errorf("first turn kind = %q, want %q", history[0].Kind, schema.TurnCheckpoint)
	}
	if history[0].Message.Text() != "checkpoint summary" {
		t.Errorf("first turn text = %q, want %q", history[0].Message.Text(), "checkpoint summary")
	}

	// Entries 7, 8, 9 follow the checkpoint
	expectedAfter := []schema.TurnKind{schema.TurnAssistant, schema.TurnToolResults, schema.TurnUserInput}
	for i, want := range expectedAfter {
		if history[i+1].Kind != want {
			t.Errorf("turn %d kind = %q, want %q", i+1, history[i+1].Kind, want)
		}
	}
}

func TestResumeHistoryFromTranscript_WithSummary(t *testing.T) {
	t.Parallel()
	entries := make([]transcript.Entry, 10)
	for i := 0; i < 10; i++ {
		kind := schema.TurnAssistant
		msg := llm.Assistant(fmt.Sprintf("msg %d", i))
		switch i {
		case 0, 4, 9:
			kind = schema.TurnUserInput
			msg = llm.User(fmt.Sprintf("input %d", i))
		case 2, 5, 8:
			kind = schema.TurnToolResults
			msg = llm.ToolResult(fmt.Sprintf("call-%d", i), "ok", false)
		case 6:
			kind = schema.TurnSummary
			msg = llm.User("LLM summary of conversation")
		}
		entries[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: schema.NewTurn(kind, msg)}
	}

	history := ResumeHistory(entries)

	// Should return: summary (index 6), plus entries 7, 8, 9 = 4 turns total
	if len(history) != 4 {
		t.Fatalf("expected 4 turns (summary + 3 after), got %d", len(history))
	}

	if history[0].Kind != schema.TurnSummary {
		t.Errorf("first turn kind = %q, want %q", history[0].Kind, schema.TurnSummary)
	}
	if history[0].Message.Text() != "LLM summary of conversation" {
		t.Errorf("first turn text = %q, want %q", history[0].Message.Text(), "LLM summary of conversation")
	}

	// Entries 7, 8, 9 follow the summary
	expectedAfter := []schema.TurnKind{schema.TurnAssistant, schema.TurnToolResults, schema.TurnUserInput}
	for i, want := range expectedAfter {
		if history[i+1].Kind != want {
			t.Errorf("turn %d kind = %q, want %q", i+1, history[i+1].Kind, want)
		}
	}
}

// --- Session-integration tests ---

func TestSession_TranscriptCreatedOnNewSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Verify the transcript file exists at the expected path.
	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	if _, err := os.Stat(tpath); os.IsNotExist(err) {
		t.Fatalf("transcript file not created at %s", tpath)
	}

	// Read it back and verify the header.
	header, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if header.SessionID != sess.ID() {
		t.Errorf("header session_id: got %q want %q", header.SessionID, sess.ID())
	}
	if header.ProfileID != "openai" {
		t.Errorf("header profile_id: got %q want %q", header.ProfileID, "openai")
	}
	if header.Model != "gpt-5.2" {
		t.Errorf("header model: got %q want %q", header.Model, "gpt-5.2")
	}
	if header.FormatVersion != transcript.FormatVersion {
		t.Errorf("header format_version: got %d want %d", header.FormatVersion, transcript.FormatVersion)
	}
	if header.Kind != "header" {
		t.Errorf("header kind: got %q want %q", header.Kind, "header")
	}
	if header.WorkingDir == "" {
		t.Error("header working_dir should not be empty")
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries in fresh transcript, got %d", len(entries))
	}
}

func TestSession_NoTranscriptWithoutStateDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// The transcript field should be nil.
	if sess.transcript != nil {
		t.Fatal("expected nil transcript when StateDir is empty")
	}
}

func TestSession_TranscriptRecordsTurns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("hello back")
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "hello back" {
		t.Fatalf("unexpected output: %q", out)
	}
	sess.Close()

	// Read the transcript and verify entries were recorded.
	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	header, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if header.SessionID != sess.ID() {
		t.Errorf("header session_id mismatch")
	}

	// Expect at least a user input turn and an assistant turn.
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 transcript entries, got %d", len(entries))
	}

	// First entry should be user input.
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Errorf("first entry kind: got %q want %q", entries[0].Turn.Kind, schema.TurnUserInput)
	}
	if entries[0].Turn.Message.Text() != "hello" {
		t.Errorf("first entry text: got %q want %q", entries[0].Turn.Message.Text(), "hello")
	}

	// Second entry should be the assistant's communicate tool call.
	if entries[1].Turn.Kind != schema.TurnAssistant {
		t.Errorf("second entry kind: got %q want %q", entries[1].Turn.Kind, schema.TurnAssistant)
	}
	var communicateCalls int
	for _, part := range entries[1].Turn.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.Name == "communicate" {
			communicateCalls++
		}
	}
	if communicateCalls != 1 {
		t.Errorf("second entry should record communicate tool call, got %+v", entries[1].Turn.Message.Content)
	}

	// Sequence numbers should be monotonically increasing.
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq <= entries[i-1].Seq {
			t.Errorf("entry[%d].Seq = %d not greater than entry[%d].Seq = %d",
				i, entries[i].Seq, i-1, entries[i-1].Seq)
		}
	}
}

func TestSession_TranscriptClosedOnSessionClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if sess.transcript == nil {
		t.Fatal("expected non-nil transcript")
	}

	sess.Close()

	// After Close, the transcript file should be readable (properly flushed).
	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, _, _, err = readTranscript(tpath)
	if err != nil {
		t.Fatalf("transcript not readable after Close: %v", err)
	}
}

func TestSubagent_TranscriptHasParentLinkage(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		StateDir: stateDir,
		spawn: spawnConfig{
			parentSessionID: "parent-session-123",
			subagentTask:    "implement auth middleware",
			depth:           2,
		},
	}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Drain the event channel so Close doesn't block.
	go func() {
		for range sess.Events() {
		}
	}()

	// Read the transcript and verify parent linkage fields.
	files, _ := filepath.Glob(filepath.Join(stateDir, sessionsSubdir, "*.transcript.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 transcript, got %d", len(files))
	}
	hdr, _, _, err := readTranscript(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if hdr.ParentSessionID != "parent-session-123" {
		t.Errorf("ParentSessionID = %q, want %q", hdr.ParentSessionID, "parent-session-123")
	}
	if hdr.Task != "implement auth middleware" {
		t.Errorf("Task = %q, want %q", hdr.Task, "implement auth middleware")
	}
	if hdr.Depth != 2 {
		t.Errorf("Depth = %d, want 2", hdr.Depth)
	}
}

func TestRootSession_TranscriptHasEmptyParentFields(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Root session: no parent fields set.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	go func() {
		for range sess.Events() {
		}
	}()

	files, _ := filepath.Glob(filepath.Join(stateDir, sessionsSubdir, "*.transcript.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 transcript, got %d", len(files))
	}
	hdr, _, _, err := readTranscript(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if hdr.ParentSessionID != "" {
		t.Errorf("ParentSessionID = %q, want empty", hdr.ParentSessionID)
	}
	if hdr.Task != "" {
		t.Errorf("Task = %q, want empty", hdr.Task)
	}
	if hdr.Depth != 0 {
		t.Errorf("Depth = %d, want 0", hdr.Depth)
	}
}

func TestSubagent_DepthSetFromConfig(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		spawn: spawnConfig{depth: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	if sess.depth != 3 {
		t.Errorf("sess.depth = %d, want 3", sess.depth)
	}
}

// --- Full lifecycle integration test ---

func TestSession_TranscriptFullLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()

	// Adapter: first call returns a read_file tool call, second call returns "done".
	// This pattern repeats for each ProcessInput.
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 1, input 1: read a big file.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			// Round 1, after tool result: finish.
			func(req llm.Request) llm.Response {
				return finalResponse("finished reading")
			},
			// Round 2, input 2: read again.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c2",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			// Round 2, after tool result: finish.
			func(req llm.Request) llm.Response {
				return finalResponse("done with second read")
			},
			// Some context-management paths can trigger one more completion round
			// after compaction before returning the final answer.
			func(req llm.Request) llm.Response {
				return finalResponse("done with second read")
			},
		},
	})

	// Write a big file that will fill a small context window.
	bigContent := strings.Repeat("line of content\n", 200)
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Use a very small context window to force compaction.
	profile := WithContextWindow(NewOpenAIProfile("gpt-5.2"), 500)

	sess, err := NewSession(c, profile, env, SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// This lifecycle test scripts exact model steps and crosses the compaction
	// threshold; mute the default-on note elicitation so it doesn't steal a step.
	muteNoteElicitation(sess)

	// Drain events in background to prevent blocking.
	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First input: reads big file, fills context.
	out1, err := sess.ProcessInput(ctx, "read the big file", nil)
	if err != nil {
		t.Fatalf("ProcessInput 1: %v", err)
	}
	if out1 == "" {
		t.Fatal("ProcessInput 1 returned empty")
	}

	// Second input: reads again, should trigger compaction.
	out2, err := sess.ProcessInput(ctx, "read it again", nil)
	if err != nil {
		t.Fatalf("ProcessInput 2: %v", err)
	}
	if out2 == "" {
		t.Fatal("ProcessInput 2 returned empty")
	}

	sess.Close()
	<-done

	// --- Verify compaction occurred ---
	foundCompaction := false
	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			foundCompaction = true
		}
	}
	if !foundCompaction {
		t.Fatal("expected CONTEXT_COMPACTION event with small context window")
	}

	// --- Read the transcript ---
	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	hdr, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	// --- Verify header ---
	if hdr.Kind != "header" {
		t.Errorf("header kind = %q, want %q", hdr.Kind, "header")
	}
	if hdr.FormatVersion != transcript.FormatVersion {
		t.Errorf("FormatVersion = %d, want %d", hdr.FormatVersion, transcript.FormatVersion)
	}
	if hdr.SessionID != sess.ID() {
		t.Errorf("SessionID = %q, want %q", hdr.SessionID, sess.ID())
	}
	if hdr.ProfileID != "openai" {
		t.Errorf("ProfileID = %q, want %q", hdr.ProfileID, "openai")
	}
	if hdr.Model != "gpt-5.2" {
		t.Errorf("Model = %q, want %q", hdr.Model, "gpt-5.2")
	}

	// --- Verify seq numbers are monotonically increasing ---
	for i, e := range entries {
		if e.Kind != "entry" {
			t.Errorf("entry %d: kind = %q, want %q", i, e.Kind, "entry")
		}
		if i > 0 && e.Seq <= entries[i-1].Seq {
			t.Errorf("entry %d: seq = %d not greater than entry %d: seq = %d",
				i, e.Seq, i-1, entries[i-1].Seq)
		}
	}

	// --- Collect turn kinds ---
	kinds := map[schema.TurnKind]int{}
	for _, e := range entries {
		kinds[e.Turn.Kind]++
	}

	// Must have user input, assistant, and tool results turns.
	if kinds[schema.TurnUserInput] == 0 {
		t.Error("no USER_INPUT turns in transcript")
	}
	if kinds[schema.TurnAssistant] == 0 {
		t.Error("no ASSISTANT turns in transcript")
	}
	if kinds[schema.TurnToolResults] == 0 {
		t.Error("no TOOL_RESULTS turns in transcript")
	}

	// Must have at least one compaction turn (checkpoint or summary).
	if kinds[schema.TurnCheckpoint]+kinds[schema.TurnSummary] == 0 {
		t.Errorf("no compaction turns (CHECKPOINT or SUMMARY) in transcript; kinds: %v", kinds)
	}

	// --- Verify compaction turn is sequenced correctly ---
	// The compaction turn should appear after the turns that preceded it, not at the very start.
	var firstCompactionSeq = -1
	for _, e := range entries {
		if e.Turn.Kind == schema.TurnCheckpoint || e.Turn.Kind == schema.TurnSummary {
			firstCompactionSeq = e.Seq
			break
		}
	}
	if firstCompactionSeq < 1 {
		t.Errorf("compaction turn at seq %d; expected after at least one regular turn", firstCompactionSeq)
	}

	// --- Verify original tool output is preserved in transcript (not masked) ---
	// Observation masking is ephemeral (modifies in-memory history); the transcript
	// should contain the full original content from before masking.
	for _, e := range entries {
		if e.Turn.Kind == schema.TurnToolResults {
			text := toolResultContent(e.Turn)
			// The original tool output should contain the actual file content,
			// not a masked summary like "[read_file: big.txt, N lines]".
			if strings.HasPrefix(text, "[read_file:") {
				t.Errorf("tool result at seq %d appears masked in transcript: %q", e.Seq, text[:min(len(text), 60)])
			}
			break // check just the first tool result
		}
	}

	// --- Verify total entry count is reasonable ---
	// Two ProcessInput calls, each with: user_input, assistant (tool call), tool_results, assistant (text)
	// = 8 regular turns + at least 1 compaction turn = 9+
	if len(entries) < 5 {
		t.Errorf("expected at least 5 transcript entries, got %d", len(entries))
	}
}

// --- Compaction turns flow through transcript ---

// --- Sub-agent transcript persistence ---
func TestSession_StateDirSessionsPathConflictFailsJobManager(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(stateDir, sessionsSubdir), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("first")
			},
		},
	})

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{StateDir: stateDir}
	sess, err := NewSession(c, testProfile("openai", "test", 100000), env, cfg)
	if err == nil {
		sess.Close()
		t.Fatal("NewSession succeeded with unusable state sessions path")
	}
	if !strings.Contains(err.Error(), "job manager:") || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("NewSession error = %v, want job manager state-dir failure", err)
	}
}

// --- Fix 2: readTranscript returns corrupt line count ---

func TestReadTranscript_RejectsCorruptInteriorLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write a valid transcript with 3 entries.
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-corrupt",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	for i := 0; i < 3; i++ {
		w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i))))
	}
	w.Close()

	// Insert two corrupt lines into the middle.
	data, _ := os.ReadFile(path)
	lines := bytes.Split(data, []byte("\n"))
	// lines: header, entry0, entry1, entry2, "" (trailing)
	// Insert corrupt lines between entry1 and entry2.
	rebuilt := [][]byte{
		lines[0], // header
		lines[1], // entry0
		[]byte(`{not valid json`),
		lines[2], // entry1
		[]byte(`also corrupt`),
		lines[3], // entry2
	}
	os.WriteFile(path, bytes.Join(rebuilt, []byte("\n")), 0o644)
	// Append final newline.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString("\n")
	f.Close()

	_, entries, skipped, err := readTranscript(path)
	if err == nil || !strings.Contains(err.Error(), "parsing transcript line") {
		t.Fatalf("readTranscript = entries %d skipped %d err %v, want corruption error", len(entries), skipped, err)
	}
}

func TestReadTranscript_ZeroCorruptLinesOnCleanFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-clean",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("msg")))
	w.Close()

	_, entries, skipped, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped lines, got %d", skipped)
	}
}

// --- Fix 3: transcript.OpenWriter single file handle ---

func TestOpenTranscriptWriter_SingleFileHandle(t *testing.T) {
	t.Parallel()
	// This test verifies that transcript.OpenWriter uses a single file handle
	// for read-truncate-append, not separate open calls. We do this by
	// verifying that a partial-line truncation + append works correctly
	// even in a single operation. If there were separate open calls, we
	// couldn't observe a difference directly, but we verify the behavior
	// is correct (truncation + append with correct seq).
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write header + 2 entries.
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-handle",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("msg 0")))
	w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("msg 1")))
	w.Close()

	// Append a partial line to simulate a crash.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"kind":"entry","seq":2`)
	f.Close()

	// Open for resume: should truncate partial line and continue at seq 2.
	w2, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatalf("transcript.OpenWriter: %v", err)
	}
	if err := w2.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("msg 2"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w2.Close()

	// Read back: should have header + 3 clean entries, no partial line.
	_, entries, skipped, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Seq != i {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i)
		}
	}
}

func TestSession_TranscriptHeaderContainsSystemPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	header, _, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	if header.SystemPrompt == "" {
		t.Fatal("expected non-empty system_prompt in transcript header")
	}

	// System prompt should contain identity section content.
	if !strings.Contains(header.SystemPrompt, "## Identity") {
		t.Errorf("system_prompt missing expected content; got (first 200 chars): %s",
			truncStr(header.SystemPrompt, 200))
	}
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Periodic sync tests ---

func TestTranscriptWriter_PeriodicSync_CloseFlushesDirtyWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-sync-004",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	// Long interval so no auto-sync happens.
	w.SyncInterval = 1 * time.Hour

	// Write entries without syncing.
	for i := 0; i < 10; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// Close must flush.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// All entries must be readable.
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if len(entries) != 10 {
		t.Fatalf("expected 10 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Seq != i {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i)
		}
	}
}

func TestTranscriptWriter_PeriodicSync_ConcurrentAppendWithInterval(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess-sync-005",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	defer w.Close()

	w.SyncInterval = 50 * time.Millisecond

	const numGoroutines = 5
	const turnsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < turnsPerGoroutine; j++ {
				turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("concurrent"))
				if err := w.Append(turn); err != nil {
					t.Errorf("goroutine %d append %d: %v", id, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Sleep well past SyncInterval so the next Append fires the timed-sync branch.
	time.Sleep(150 * time.Millisecond)

	// This Append must trigger a periodic sync (time.Since(lastSync) > 50ms).
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("trigger-sync"))); err != nil {
		t.Fatalf("trigger-sync append: %v", err)
	}

	// Read the file WITHOUT calling Close — all entries must already be on disk
	// via the periodic-sync path, not via Close's final flush.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript before Close: %v", err)
	}
	var onDiskLines int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		if scanner.Text() != "" {
			onDiskLines++
		}
	}
	_ = f.Close()
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript before Close: %v", err)
	}
	// header + numGoroutines*turnsPerGoroutine entries + 1 trigger entry
	wantLines := 1 + numGoroutines*turnsPerGoroutine + 1
	if onDiskLines != wantLines {
		t.Errorf("before Close: expected %d lines on disk (periodic sync must have run), got %d", wantLines, onDiskLines)
	}

	// Verify full correctness after Close.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}

	expectedTotal := numGoroutines*turnsPerGoroutine + 1 // +1 for the trigger entry
	if len(entries) != expectedTotal {
		t.Fatalf("expected %d entries after Close, got %d", expectedTotal, len(entries))
	}

	// Verify seq uniqueness.
	seqs := map[int]bool{}
	for _, e := range entries {
		if seqs[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		seqs[e.Seq] = true
	}
}

func TestTranscriptReadersRejectCorruptNonFinalLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "child-session",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("append turn: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	lines := readTranscriptLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("transcript lines = %d, want header and entry", len(lines))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		t.Fatalf("append corrupt line: %v", err)
	}
	if _, err := f.WriteString(lines[1] + "\n"); err != nil {
		t.Fatalf("append valid line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append handle: %v", err)
	}

	if _, err := readStrictChildTranscript(path, "child-session", 0); err == nil || !strings.Contains(err.Error(), "corrupt_child_transcript") {
		t.Fatalf("strict read error = %v, want corrupt_child_transcript", err)
	}
	_, entries, skipped, err := readTranscript(path)
	if err == nil || !strings.Contains(err.Error(), "parsing transcript line") {
		t.Fatalf("readTranscript = entries %d skipped %d err %v, want corruption error", len(entries), skipped, err)
	}
}

func TestStrictChildTranscriptSessionMismatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "other-session",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	if _, err := readStrictChildTranscript(path, "child-session", 0); err == nil || !strings.Contains(err.Error(), "transcript_session_mismatch") {
		t.Fatalf("strict read error = %v, want transcript_session_mismatch", err)
	}
}

func TestStrictChildTranscriptCorruptBodyPrecedesSessionMismatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "other-session",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("append turn: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	lines := readTranscriptLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("transcript lines = %d, want header and entry", len(lines))
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\n{not-json}\n"+lines[1]+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}

	if _, err := readStrictChildTranscript(path, "child-session", 0); err == nil || !strings.Contains(err.Error(), "corrupt_child_transcript") {
		t.Fatalf("strict read error = %v, want corrupt_child_transcript", err)
	}
	_, entries, skipped, err := readTranscript(path)
	if err == nil || !strings.Contains(err.Error(), "parsing transcript line") {
		t.Fatalf("readTranscript = entries %d skipped %d err %v, want corruption error", len(entries), skipped, err)
	}
}

func TestStrictChildTranscriptRejectsMalformedHeaderShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		line string
	}{
		{
			name: "missing kind",
			line: `{"session_id":"child-session","format_version":2}`,
		},
		{
			name: "wrong kind",
			line: `{"kind":"entry","session_id":"child-session","format_version":2}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0o644); err != nil {
				t.Fatalf("write transcript: %v", err)
			}

			if _, err := readStrictChildTranscript(path, "child-session", 0); !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("strict read error = %v, want ErrUnsupportedFormat", err)
			}
			if _, _, _, err := readTranscript(path); !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("readTranscript error = %v, want ErrUnsupportedFormat", err)
			}
		})
	}
}

func TestStrictChildTranscriptRejectsOversizedHeaderLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65)+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	if _, err := readStrictChildTranscript(path, "child-session", 64); err == nil || !strings.Contains(err.Error(), "corrupt_child_transcript") {
		t.Fatalf("strict read error = %v, want corrupt_child_transcript", err)
	}
}

func TestStrictChildTranscriptRejectsOversizedBodyLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "child-session",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if _, err := f.WriteString(strings.Repeat("x", 65) + "\n"); err != nil {
		t.Fatalf("append oversized line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append handle: %v", err)
	}

	if _, err := readStrictChildTranscript(path, "child-session", 64); err == nil || !strings.Contains(err.Error(), "corrupt_child_transcript") {
		t.Fatalf("strict read error = %v, want corrupt_child_transcript", err)
	}
	_, _, _, err = readTranscript(path)
	if err == nil || !strings.Contains(err.Error(), "parsing transcript line") {
		t.Fatalf("readTranscript error = %v, want corruption error", err)
	}
}

func TestReadSessionTranscriptRejectsInvalidSourceCombinationsBeforeOpeningTranscript(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	originalOpen := openTranscriptFile
	openCount := 0
	openTranscriptFile = func(path string) (io.ReadCloser, error) {
		openCount++
		return originalOpen(path)
	}
	t.Cleanup(func() { openTranscriptFile = originalOpen })

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown source", args: map[string]any{"source": "wire"}, want: "invalid_request: source"},
		{name: "API format", args: map[string]any{"source": "api_log", "format": "markdown"}, want: "invalid_request: format"},
		{name: "body without attempt", args: map[string]any{"body": "request"}, want: "invalid_request: body requires attempt_id"},
		{name: "offset without expansion", args: map[string]any{"offset_bytes": float64(1)}, want: "invalid_request: offset_bytes"},
		{name: "max without expansion", args: map[string]any{"max_bytes": float64(1)}, want: "invalid_request: max_bytes"},
		{name: "API expand turn", args: map[string]any{"source": "api_log", "expand_turn": float64(1)}, want: "invalid_request: expand_turn"},
		{name: "transcript attempt", args: map[string]any{"source": "transcript", "attempt_id": "att_explicit"}, want: "invalid_request: attempt_id"},
		{name: "attempt range", args: map[string]any{"attempt_id": "att_explicit", "range": "0-1"}, want: "invalid_request: range"},
		{name: "negative expand turn", args: map[string]any{"expand_turn": float64(-1)}, want: "invalid_request: expand_turn"},
		{name: "negative offset", args: map[string]any{"expand_turn": float64(1), "offset_bytes": float64(-1)}, want: "invalid_request: offset_bytes"},
		{name: "zero max", args: map[string]any{"expand_turn": float64(1), "max_bytes": float64(0)}, want: "invalid_request: max_bytes"},
		{name: "oversized max", args: map[string]any{"expand_turn": float64(1), "max_bytes": float64(maxExpansionBytes + 1)}, want: "invalid_request: max_bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := openCount
			_, err := execReadSessionTranscript(deps, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if openCount != before {
				t.Fatalf("invalid request opened transcript %d times", openCount-before)
			}
		})
	}
}

func testAPIAttemptRecord(groupID string, index int, request, response []byte) apilog.APIAttemptRecord {
	return apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   groupID,
		AttemptIndex:     index,
		Timestamp:        time.Date(2026, 7, 16, 12, 0, index, 0, time.UTC),
		LatencyMS:        int64(index * 10),
		ProviderInstance: "openai-primary",
		RequestModel:     "gpt-test",
		Request: apilog.APIAttemptRequest{
			Method:      "POST",
			Endpoint:    "https://provider.test/v1/responses",
			Body:        apilog.EncodeBody(request),
			Model:       "gpt-test",
			HistoryMode: "messages",
		},
		Response: &apilog.APIAttemptResponse{
			StatusCode: 200,
			Body:       apilog.EncodeBody(response),
			Model:      "gpt-test-result",
		},
		Outcome: apilog.AttemptSuccess,
	}
}

func writeTestAPILog(t *testing.T, bucketDir, sessionID string, records ...apilog.APILogRecord) string {
	t.Helper()
	path := filepath.Join(bucketDir, "sessions", sessionID+".api.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open test API log: %v", err)
	}
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			_ = f.Close()
			t.Fatalf("marshal test API record: %v", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			t.Fatalf("write test API record: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close test API log: %v", err)
	}
	return path
}

func TestReadSessionTranscriptAPILogSourceSummarizesWithoutBodyData(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv2enqVTitaig6F"
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	attempt := testAPIAttemptRecord("ag_summary", 1, []byte(`{"secret":"request-sentinel"}`), []byte{0x00, 0xff, 0x80})
	settlement := apilog.APIAttemptGroupSettlement{
		Kind:              "attempt_group_settlement",
		SchemaVersion:     1,
		AttemptGroupID:    attempt.AttemptGroupID,
		FinalAttemptID:    attempt.AttemptID,
		FinalAttemptCount: 1,
		Outcome:           apilog.AttemptSuccess,
		SettledAt:         attempt.Timestamp.Add(time.Second),
	}
	writeTestAPILog(t, dir, sessionID, attempt, settlement)

	env := decodeReadEnvelope(t, marshalRead(t, &toolDeps{stateDir: dir, sessionID: sessionID}, map[string]any{
		"source": "api_log",
	}))
	if env["source"] != "api_log" || env["credential_values_excluded"] != true {
		t.Fatalf("API-log envelope identity = %#v", env)
	}
	records, ok := env["records"].([]any)
	if !ok || len(records) != 2 {
		t.Fatalf("records = %#v, want two summaries", env["records"])
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"request-sentinel", `"data"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, raw)
		}
	}
	attemptSummary := records[0].(map[string]any)
	for key, want := range map[string]any{
		"kind":             "api_attempt",
		"attempt_id":       attempt.AttemptID,
		"attempt_group_id": attempt.AttemptGroupID,
		"outcome":          string(apilog.AttemptSuccess),
		"settlement_state": "settled",
	} {
		if got := attemptSummary[key]; got != want {
			t.Errorf("attempt summary %s = %#v, want %#v", key, got, want)
		}
	}
	requestBody := attemptSummary["request_body"].(map[string]any)
	if requestBody["encoding"] != "utf8" || requestBody["byte_count"] != float64(attempt.Request.Body.ByteCount) {
		t.Errorf("request body evidence = %#v", requestBody)
	}
	responseBody := attemptSummary["response_body"].(map[string]any)
	if responseBody["encoding"] != "base64" || responseBody["byte_count"] != float64(attempt.Response.Body.ByteCount) {
		t.Errorf("response body evidence = %#v", responseBody)
	}
}

func TestReadSessionTranscriptAPILogSourceErrorsAreClear(t *testing.T) {
	dir := newBucket(t)
	sessionID := identifier.MustNewSessionID()
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	if _, err := execReadSessionTranscript(deps, map[string]any{"source": apiLogSource}); err == nil || !strings.Contains(err.Error(), "open API log") {
		t.Fatalf("missing API-log error = %v, want open API log", err)
	}

	attempt := testAPIAttemptRecord("ag_corrupt", 1, []byte(`{"request":true}`), []byte(`{"response":true}`))
	path := writeTestAPILog(t, dir, sessionID, attempt)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open API log for corruption: %v", err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		_ = f.Close()
		t.Fatalf("append corrupt API-log record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupt API log: %v", err)
	}

	if _, err := execReadSessionTranscript(deps, map[string]any{"source": apiLogSource}); err == nil || !strings.Contains(err.Error(), "read API log") {
		t.Fatalf("corrupt API-log error = %v, want read API log", err)
	}
}

func TestReadSessionTranscriptDefaultSourcesNeverOpenAPILog(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv47YP64RR3B9YJ"
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	originalOpen := openAPILogFile
	openCount := 0
	openAPILogFile = func(string) (io.ReadCloser, error) {
		openCount++
		return nil, errors.New("sentinel API log must stay unopened")
	}
	t.Cleanup(func() { openAPILogFile = originalOpen })

	for _, format := range []string{"", formatMarkdown, formatOutline, formatJSONL} {
		args := map[string]any{}
		if format != "" {
			args["format"] = format
		}
		if _, err := execReadSessionTranscript(deps, args); err != nil {
			t.Fatalf("default transcript format %q: %v", format, err)
		}
	}
	if openCount != 0 {
		t.Fatalf("default transcript reads opened API log %d times", openCount)
	}
}

func TestReadSessionTranscriptAPILogSourceBoundsRecordsAndSerializedBytes(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv5aIxgf9yVdd0N"
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	records := make([]apilog.APILogRecord, 0, 105)
	for i := 0; i < 105; i++ {
		attempt := testAPIAttemptRecord(fmt.Sprintf("ag_bounds_%03d", i), 1, nil, nil)
		attempt.ProviderInstance = "p"
		attempt.RequestModel = "m"
		attempt.Request.Method = "P"
		attempt.Request.Endpoint = "e"
		attempt.Response = nil
		attempt.Outcome = apilog.AttemptTransportFail
		records = append(records, attempt)
	}
	writeTestAPILog(t, dir, sessionID, records...)
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	defaultJSON := marshalRead(t, deps, map[string]any{"source": "api_log"})
	defaultEnv := decodeReadEnvelope(t, defaultJSON)
	if got := len(defaultEnv["records"].([]any)); got != defaultAPILogRecords {
		t.Fatalf("default records = %d, want %d", got, defaultAPILogRecords)
	}
	hardLimitJSON := marshalRead(t, deps, map[string]any{"source": "api_log", "range": "start:200"})
	if len(hardLimitJSON) > maxAPILogOutputBytes {
		t.Fatalf("100-record summary = %d bytes, exceeds %d", len(hardLimitJSON), maxAPILogOutputBytes)
	}
	if got := len(decodeReadEnvelope(t, hardLimitJSON)["records"].([]any)); got != maxAPILogRecords {
		t.Fatalf("hard-limited records = %d, want %d", got, maxAPILogRecords)
	}

	for i := range records {
		attempt := records[i].(apilog.APIAttemptRecord)
		attempt.ProviderInstance = strings.Repeat("provider", 300) + fmt.Sprintf("-%03d", i)
		records[i] = attempt
	}
	writeTestAPILog(t, dir, sessionID, records...)

	boundedJSON := marshalRead(t, deps, map[string]any{"source": "api_log", "range": "start:200"})
	if len(boundedJSON) > maxAPILogOutputBytes {
		t.Fatalf("serialized API summary = %d bytes, exceeds %d", len(boundedJSON), maxAPILogOutputBytes)
	}
	boundedEnv := decodeReadEnvelope(t, boundedJSON)
	if got := len(boundedEnv["records"].([]any)); got == 0 || got > maxAPILogRecords {
		t.Fatalf("bounded records = %d, want 1..%d", got, maxAPILogRecords)
	}
	meta := readMetaMap(t, boundedEnv)
	if meta["truncated"] != true || meta["records_total"] != float64(105) {
		t.Fatalf("bounded meta = %#v", meta)
	}
}

func readExpandedAPIBody(t *testing.T, deps *toolDeps, attemptID, body string, maxBytes int) ([]byte, []map[string]any) {
	t.Helper()
	var joined []byte
	var pages []map[string]any
	offset := 0
	for {
		env := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{
			"attempt_id":   attemptID,
			"body":         body,
			"offset_bytes": float64(offset),
			"max_bytes":    float64(maxBytes),
		}))
		pages = append(pages, env)
		if env["source"] != apiLogSource || env["credential_values_excluded"] != true {
			t.Fatalf("API body envelope identity = %#v", env)
		}
		chunk := env["body"].(map[string]any)
		data := chunk["data"].(string)
		switch chunk["encoding"] {
		case "utf8":
			joined = append(joined, []byte(data)...)
		case "base64":
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				t.Fatalf("decode body chunk: %v", err)
			}
			joined = append(joined, decoded...)
		default:
			t.Fatalf("body encoding = %#v", chunk["encoding"])
		}
		continuation, ok := env["continuation"].(map[string]any)
		if !ok {
			break
		}
		if continuation["attempt_id"] != attemptID || continuation["body"] != body {
			t.Fatalf("continuation = %#v", continuation)
		}
		next := int(continuation["offset_bytes"].(float64))
		if next <= offset {
			t.Fatalf("continuation offset = %d after %d", next, offset)
		}
		offset = next
	}
	return joined, pages
}

func TestReadSessionTranscriptAttemptExpansionPagesExactBodies(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv733WHFsVy66SR"
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	requestBody := []byte("line\n\"quote\"-escaped-tail")
	responseBody := []byte{0x00, 0xff, 0x80, 'A', 'B', 0xfe, '\n'}
	attempt := testAPIAttemptRecord("ag_expand", 1, requestBody, responseBody)
	writeTestAPILog(t, dir, sessionID, attempt)
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	metadata := marshalRead(t, deps, map[string]any{"attempt_id": attempt.AttemptID})
	if strings.Contains(string(metadata), `"data"`) {
		t.Fatalf("attempt metadata leaked body data: %s", metadata)
	}

	gotRequest, requestPages := readExpandedAPIBody(t, deps, attempt.AttemptID, "request", 7)
	if !bytes.Equal(gotRequest, requestBody) {
		t.Fatalf("request expansion = %q, want %q", gotRequest, requestBody)
	}
	if len(requestPages) < 2 || requestPages[0]["body"].(map[string]any)["encoding"] != "utf8" {
		t.Fatalf("request pages = %#v", requestPages)
	}

	gotResponse, responsePages := readExpandedAPIBody(t, deps, attempt.AttemptID, "response", 2)
	if !bytes.Equal(gotResponse, responseBody) {
		t.Fatalf("response expansion = %v, want %v", gotResponse, responseBody)
	}
	if len(responsePages) < 2 || responsePages[0]["body"].(map[string]any)["encoding"] != "base64" {
		t.Fatalf("response pages = %#v", responsePages)
	}
}

func TestReadSessionTranscriptUnsettledGroupStateIsRangeHonest(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv8Vo4rqb3QYZuV"
	writeFindSession(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()}, "semantic turn")
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	attempt := testAPIAttemptRecord("ag_later_settlement", 1, []byte("{}"), []byte("{}"))
	filler := testAPIAttemptRecord("ag_filler", 1, []byte("{}"), []byte("{}"))
	settlement := apilog.APIAttemptGroupSettlement{
		Kind:              "attempt_group_settlement",
		SchemaVersion:     1,
		AttemptGroupID:    attempt.AttemptGroupID,
		FinalAttemptID:    attempt.AttemptID,
		FinalAttemptCount: 1,
		Outcome:           apilog.AttemptSuccess,
		SettledAt:         attempt.Timestamp.Add(time.Second),
	}
	apiPath := writeTestAPILog(t, dir, sessionID, attempt, filler, settlement)

	beforeSettlement := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{"source": "api_log", "range": "0-1"}))
	beforeRecords := beforeSettlement["records"].([]any)
	if got := beforeRecords[0].(map[string]any)["settlement_state"]; got != "unknown_outside_range" {
		t.Fatalf("attempt-only page settlement_state = %v, want unknown_outside_range", got)
	}
	settlementPage := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{"source": "api_log", "range": "2-2"}))
	if got := settlementPage["records"].([]any)[0].(map[string]any)["settlement_state"]; got != "settled" {
		t.Fatalf("settlement page settlement_state = %v, want settled", got)
	}

	orphan := testAPIAttemptRecord("ag_clean_eof", 1, []byte("{}"), []byte("{}"))
	writeTestAPILog(t, dir, sessionID, orphan)
	cleanEOF := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{"source": "api_log", "range": "0-0"}))
	if got := cleanEOF["records"].([]any)[0].(map[string]any)["settlement_state"]; got != "unsettled" {
		t.Fatalf("clean-EOF attempt settlement_state = %v, want unsettled", got)
	}

	f, err := os.OpenFile(apiPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"attempt_group_settlement"`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	partial := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{"source": "api_log", "range": "0-0"}))
	if got := partial["records"].([]any)[0].(map[string]any)["settlement_state"]; got != "unknown_outside_range" {
		t.Fatalf("partial-tail attempt settlement_state = %v, want unknown_outside_range", got)
	}
	if meta := readMetaMap(t, partial); meta["partial_tail"] != true {
		t.Fatalf("partial-tail meta = %#v", meta)
	}
}

func TestReadSessionTranscriptOversizedExpansionIsBytePaged(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv9yYdSRJat13MZ"
	path := transcriptPath(dir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	callID := "call-oversized-expansion"
	assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: callID, Name: "shell", Arguments: json.RawMessage(`{"command":"large"}`)},
	}}}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, assistant)); err != nil {
		t.Fatal(err)
	}
	var largeResult strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&largeResult, "oversized-line-%03d-%s\n", i, strings.Repeat("x", 3000))
	}
	toolResult := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind:       llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{ToolCallID: callID, Name: "shell", Content: largeResult.String()},
	}}}
	if err := w.Append(schema.NewTurn(schema.TurnToolResults, toolResult)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	saveFindMeta(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()})
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}

	first := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{
		"expand_turn": float64(0),
		"max_bytes":   float64(1024),
	}))
	content := first["content"].(string)
	if !strings.Contains(content, "oversized-line-000") || !strings.Contains(content, "oversized-line-199") {
		t.Fatalf("bounded content lacks head/tail evidence: %s", content[:min(len(content), 2000)])
	}
	if strings.Contains(content, "oversized-line-100") {
		t.Fatal("bounded content included elided middle evidence")
	}
	expansion, ok := first["expansion"].(map[string]any)
	if !ok || expansion["bytes_returned"] != float64(1024) || expansion["total_bytes"].(float64) <= 1024 {
		t.Fatalf("first expansion = %#v", first["expansion"])
	}
	continuation, ok := first["continuation"].(map[string]any)
	if !ok || continuation["expand_turn"] != float64(0) || continuation["offset_bytes"] != float64(1024) {
		t.Fatalf("first continuation = %#v", first["continuation"])
	}

	second := decodeReadEnvelope(t, marshalRead(t, deps, map[string]any{
		"expand_turn":  float64(0),
		"offset_bytes": continuation["offset_bytes"],
		"max_bytes":    float64(1024),
	}))
	secondExpansion := second["expansion"].(map[string]any)
	if secondExpansion["offset_bytes"] != float64(1024) || secondExpansion["bytes_returned"].(float64) > 1024 {
		t.Fatalf("second expansion = %#v", secondExpansion)
	}
	if got := len(marshalRead(t, deps, map[string]any{"expand_turn": float64(0), "max_bytes": float64(maxExpansionBytes)})); got > hardCapChars {
		t.Fatalf("maximum transcript expansion envelope = %d bytes, exceeds hard cap %d", got, hardCapChars)
	}
}

func TestReadSessionTranscriptJSONLIsSemanticOnly(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5TxvBRJC3228LTWod"
	path := transcriptPath(dir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    sessionID,
		Model:        "test-model",
		SystemPrompt: "private-system-prompt-sentinel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("semantic-entry"))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	saveFindMeta(t, dir, findMetaSpec{id: sessionID, updated: time.Now().UTC()})

	env := decodeReadEnvelope(t, marshalRead(t, &toolDeps{stateDir: dir, sessionID: sessionID}, map[string]any{"format": "jsonl"}))
	content := env["content"].(string)
	for _, forbidden := range []string{"private-system-prompt-sentinel", `"kind":"api_call"`} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("semantic JSONL leaked %q: %s", forbidden, content)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		var record struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("semantic JSONL line is invalid: %v", err)
		}
		if record.Kind != "header" && record.Kind != "entry" {
			t.Fatalf("semantic JSONL record kind = %q", record.Kind)
		}
	}
	if hint := readMetaMap(t, env)["hint"].(string); !strings.Contains(hint, "semantic") || !strings.Contains(hint, "API") {
		t.Fatalf("JSONL hint = %q", hint)
	}
}
