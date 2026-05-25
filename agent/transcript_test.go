package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
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
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "transcript.jsonl")

	header := TranscriptHeader{
		SessionID:  "sess-001",
		CreatedAt:  time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
		ProfileID:  "anthropic-default",
		Model:      "claude-opus-4-6",
		WorkingDir: "/tmp/test",
	}

	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	lines := readTranscriptLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (header), got %d", len(lines))
	}

	var got TranscriptHeader
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if got.Kind != "header" {
		t.Errorf("header kind = %q, want %q", got.Kind, "header")
	}
	if got.FormatVersion != 1 {
		t.Errorf("format_version = %d, want 1", got.FormatVersion)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID: "sess-002",
		CreatedAt: time.Now().UTC(),
		ProfileID: "openai-default",
		Model:     "gpt-5",
	}

	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	turn1 := NewTurn(TurnUserInput, llm.User("Hello"))
	turn2 := NewTurn(TurnAssistant, llm.Assistant("Hi there"))

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
	var entry0 TranscriptEntry
	if err := json.Unmarshal([]byte(lines[1]), &entry0); err != nil {
		t.Fatalf("unmarshal entry 0: %v", err)
	}
	if entry0.Kind != "entry" {
		t.Errorf("entry0 kind = %q, want %q", entry0.Kind, "entry")
	}
	if entry0.Seq != 0 {
		t.Errorf("entry0 seq = %d, want 0", entry0.Seq)
	}
	if entry0.Turn.Kind != TurnUserInput {
		t.Errorf("entry0 turn kind = %q, want %q", entry0.Turn.Kind, TurnUserInput)
	}
	if entry0.Turn.Message.Text() != "Hello" {
		t.Errorf("entry0 turn text = %q, want %q", entry0.Turn.Message.Text(), "Hello")
	}

	// Verify entry at line 3 (index 2)
	var entry1 TranscriptEntry
	if err := json.Unmarshal([]byte(lines[2]), &entry1); err != nil {
		t.Fatalf("unmarshal entry 1: %v", err)
	}
	if entry1.Kind != "entry" {
		t.Errorf("entry1 kind = %q, want %q", entry1.Kind, "entry")
	}
	if entry1.Seq != 1 {
		t.Errorf("entry1 seq = %d, want 1", entry1.Seq)
	}
	if entry1.Turn.Kind != TurnAssistant {
		t.Errorf("entry1 turn kind = %q, want %q", entry1.Turn.Kind, TurnAssistant)
	}
	if entry1.Turn.Message.Text() != "Hi there" {
		t.Errorf("entry1 turn text = %q, want %q", entry1.Turn.Message.Text(), "Hi there")
	}
}

func TestTranscriptWriter_SeqMonotonicallyIncreasing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-003",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	for i := 0; i < 10; i++ {
		turn := NewTurn(TurnAssistant, llm.Assistant("msg"))
		if err := w.Append(turn); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 11 { // header + 10 entries
		t.Fatalf("expected 11 lines, got %d", len(lines))
	}

	for i := 1; i <= 10; i++ {
		var entry TranscriptEntry
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-004",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("before close"))); err != nil {
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
	var w *TranscriptWriter

	// Append on nil should not panic and should return nil.
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("test"))); err != nil {
		t.Errorf("nil Append returned error: %v", err)
	}

	// Close on nil should not panic and should return nil.
	if err := w.Close(); err != nil {
		t.Errorf("nil Close returned error: %v", err)
	}
}

func TestTranscriptWriter_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-005",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	const numGoroutines = 10
	const turnsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < turnsPerGoroutine; j++ {
				turn := NewTurn(TurnAssistant, llm.Assistant("concurrent"))
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
		var entry TranscriptEntry
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-006",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	// Text content
	w.Append(NewTurn(TurnUserInput, llm.User("Hello world")))

	// Tool call content
	w.Append(NewTurn(TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Let me check that."},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call-1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"/tmp/foo"}`),
			}},
		},
	}))

	// Tool result content
	w.Append(NewTurn(TurnToolResults, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call-1",
				Name:       "read_file",
				Content:    "file contents here",
			}},
		},
	}))

	// Thinking content
	w.Append(NewTurn(TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				Text: "Let me think about this...",
			}},
			{Kind: llm.ContentText, Text: "Here's what I found."},
		},
	}))

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

	// The header must parse as TranscriptHeader.
	var hdr TranscriptHeader
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil {
		t.Fatalf("header does not parse: %v", err)
	}
	if hdr.Kind != "header" {
		t.Errorf("header kind = %q, want %q", hdr.Kind, "header")
	}

	// Each entry must parse as TranscriptEntry with incrementing seq.
	for i := 1; i < len(lines); i++ {
		var entry TranscriptEntry
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
	dir := t.TempDir()
	path := filepath.Join(dir, "large.transcript.jsonl")

	hdr := TranscriptHeader{SessionID: "test-large"}
	tw, err := NewTranscriptWriter(path, hdr)
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
	turn := NewTurn(TurnToolResults, msg)

	if err := tw.Append(turn); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	// Read back and verify
	_, entries, _, err := ReadTranscript(path)
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

// --- ReadTranscript tests ---

func TestReadTranscript_ReturnsHeaderAndEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID:  "sess-read-001",
		CreatedAt:  time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC),
		ProfileID:  "anthropic-default",
		Model:      "claude-opus-4-6",
		WorkingDir: "/tmp/test",
	}

	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	turns := []Turn{
		NewTurn(TurnUserInput, llm.User("Hello")),
		NewTurn(TurnAssistant, llm.Assistant("Hi there")),
		NewTurn(TurnToolResults, llm.ToolResult("call-1", "result", false)),
		NewTurn(TurnAssistant, llm.Assistant("Done")),
		NewTurn(TurnUserInput, llm.User("Thanks")),
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	gotHeader, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID: "sess-partial",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
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

	gotHeader, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")

	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, _, err := ReadTranscript(path)
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

func TestReadTranscript_HeaderOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID: "sess-header-only",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	w.Close()

	gotHeader, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}

	if gotHeader.SessionID != "sess-header-only" {
		t.Errorf("header session_id = %q, want %q", gotHeader.SessionID, "sess-header-only")
	}

	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// --- OpenTranscriptWriter tests ---

func TestOpenTranscriptWriter_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID: "sess-open-001",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	// Write header + 5 entries, then close.
	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	w.Close()

	// Reopen for appending.
	w2, err := OpenTranscriptWriter(path)
	if err != nil {
		t.Fatalf("OpenTranscriptWriter: %v", err)
	}
	defer w2.Close()

	// Append 3 more turns.
	for i := 0; i < 3; i++ {
		if err := w2.Append(NewTurn(TurnUserInput, llm.User(fmt.Sprintf("input %d", i)))); err != nil {
			t.Fatalf("Append (resumed) %d: %v", i, err)
		}
	}

	// Read back and verify.
	_, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID: "sess-open-002",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	// Write header + 3 entries, then close.
	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
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
	w2, err := OpenTranscriptWriter(path)
	if err != nil {
		t.Fatalf("OpenTranscriptWriter: %v", err)
	}
	defer w2.Close()

	// Append 1 more turn.
	if err := w2.Append(NewTurn(TurnUserInput, llm.User("after crash"))); err != nil {
		t.Fatalf("Append (after crash): %v", err)
	}

	// Read back and verify.
	_, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	header := TranscriptHeader{
		SessionID: "sess-open-003",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	}

	// Write header only, no entries.
	w, err := NewTranscriptWriter(path, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	w.Close()

	// Reopen.
	w2, err := OpenTranscriptWriter(path)
	if err != nil {
		t.Fatalf("OpenTranscriptWriter: %v", err)
	}
	defer w2.Close()

	// Append one turn.
	if err := w2.Append(NewTurn(TurnUserInput, llm.User("first after header"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read back and verify.
	_, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	entries := []TranscriptEntry{
		{Kind: "entry", Seq: 0, Turn: NewTurn(TurnUserInput, llm.User("Hello"))},
		{Kind: "entry", Seq: 1, Turn: NewTurn(TurnAssistant, llm.Assistant("Hi"))},
		{Kind: "entry", Seq: 2, Turn: NewTurn(TurnToolResults, llm.ToolResult("call-1", "ok", false))},
		{Kind: "entry", Seq: 3, Turn: NewTurn(TurnAssistant, llm.Assistant("Done"))},
		{Kind: "entry", Seq: 4, Turn: NewTurn(TurnUserInput, llm.User("Thanks"))},
	}

	history := ResumeHistory(entries)

	if len(history) != 5 {
		t.Fatalf("expected 5 turns, got %d", len(history))
	}

	expectedKinds := []TurnKind{TurnUserInput, TurnAssistant, TurnToolResults, TurnAssistant, TurnUserInput}
	for i, turn := range history {
		if turn.Kind != expectedKinds[i] {
			t.Errorf("turn %d kind = %q, want %q", i, turn.Kind, expectedKinds[i])
		}
	}
}

func TestResumeHistoryFromTranscript_WithCheckpoint(t *testing.T) {
	entries := make([]TranscriptEntry, 10)
	for i := 0; i < 10; i++ {
		kind := TurnAssistant
		msg := llm.Assistant(fmt.Sprintf("msg %d", i))
		switch i {
		case 0, 4, 9:
			kind = TurnUserInput
			msg = llm.User(fmt.Sprintf("input %d", i))
		case 2, 5, 8:
			kind = TurnToolResults
			msg = llm.ToolResult(fmt.Sprintf("call-%d", i), "ok", false)
		case 6:
			kind = TurnCheckpoint
			msg = llm.User("checkpoint summary")
		}
		entries[i] = TranscriptEntry{Kind: "entry", Seq: i, Turn: NewTurn(kind, msg)}
	}

	history := ResumeHistory(entries)

	// Should return: checkpoint (index 6), plus entries 7, 8, 9 = 4 turns total
	if len(history) != 4 {
		t.Fatalf("expected 4 turns (checkpoint + 3 after), got %d", len(history))
	}

	if history[0].Kind != TurnCheckpoint {
		t.Errorf("first turn kind = %q, want %q", history[0].Kind, TurnCheckpoint)
	}
	if history[0].Message.Text() != "checkpoint summary" {
		t.Errorf("first turn text = %q, want %q", history[0].Message.Text(), "checkpoint summary")
	}

	// Entries 7, 8, 9 follow the checkpoint
	expectedAfter := []TurnKind{TurnAssistant, TurnToolResults, TurnUserInput}
	for i, want := range expectedAfter {
		if history[i+1].Kind != want {
			t.Errorf("turn %d kind = %q, want %q", i+1, history[i+1].Kind, want)
		}
	}
}

func TestResumeHistoryFromTranscript_WithSummary(t *testing.T) {
	entries := make([]TranscriptEntry, 10)
	for i := 0; i < 10; i++ {
		kind := TurnAssistant
		msg := llm.Assistant(fmt.Sprintf("msg %d", i))
		switch i {
		case 0, 4, 9:
			kind = TurnUserInput
			msg = llm.User(fmt.Sprintf("input %d", i))
		case 2, 5, 8:
			kind = TurnToolResults
			msg = llm.ToolResult(fmt.Sprintf("call-%d", i), "ok", false)
		case 6:
			kind = TurnSummary
			msg = llm.User("LLM summary of conversation")
		}
		entries[i] = TranscriptEntry{Kind: "entry", Seq: i, Turn: NewTurn(kind, msg)}
	}

	history := ResumeHistory(entries)

	// Should return: summary (index 6), plus entries 7, 8, 9 = 4 turns total
	if len(history) != 4 {
		t.Fatalf("expected 4 turns (summary + 3 after), got %d", len(history))
	}

	if history[0].Kind != TurnSummary {
		t.Errorf("first turn kind = %q, want %q", history[0].Kind, TurnSummary)
	}
	if history[0].Message.Text() != "LLM summary of conversation" {
		t.Errorf("first turn text = %q, want %q", history[0].Message.Text(), "LLM summary of conversation")
	}

	// Entries 7, 8, 9 follow the summary
	expectedAfter := []TurnKind{TurnAssistant, TurnToolResults, TurnUserInput}
	for i, want := range expectedAfter {
		if history[i+1].Kind != want {
			t.Errorf("turn %d kind = %q, want %q", i+1, history[i+1].Kind, want)
		}
	}
}

// --- Session-integration tests ---

func TestSession_TranscriptCreatedOnNewSession(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
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
	header, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	if header.FormatVersion != 1 {
		t.Errorf("header format_version: got %d want 1", header.FormatVersion)
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
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
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
	header, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if header.SessionID != sess.ID() {
		t.Errorf("header session_id mismatch")
	}

	// Expect at least a user input turn and an assistant turn.
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 transcript entries, got %d", len(entries))
	}

	// First entry should be user input.
	if entries[0].Turn.Kind != TurnUserInput {
		t.Errorf("first entry kind: got %q want %q", entries[0].Turn.Kind, TurnUserInput)
	}
	if entries[0].Turn.Message.Text() != "hello" {
		t.Errorf("first entry text: got %q want %q", entries[0].Turn.Message.Text(), "hello")
	}

	// Second entry should be the assistant's communicate tool call.
	if entries[1].Turn.Kind != TurnAssistant {
		t.Errorf("second entry kind: got %q want %q", entries[1].Turn.Kind, TurnAssistant)
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

	// Sequence numbers should be monotonically increasing (may have gaps due
	// to interleaved api_call lines that share the seq counter).
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq <= entries[i-1].Seq {
			t.Errorf("entry[%d].Seq = %d not greater than entry[%d].Seq = %d",
				i, entries[i].Seq, i-1, entries[i-1].Seq)
		}
	}
}

func TestSession_ContextDiagnosticsRecordedOnAPICall(t *testing.T) {
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	data, err := ReadTranscriptFull(tpath)
	if err != nil {
		t.Fatalf("ReadTranscriptFull: %v", err)
	}
	if len(data.APICalls) != 1 {
		t.Fatalf("expected 1 api_call, got %d", len(data.APICalls))
	}

	call := data.APICalls[0]
	if call.ContextHistoryTurns != 1 {
		t.Errorf("context_history_turns = %d, want 1", call.ContextHistoryTurns)
	}
	if call.SystemPromptBytes != len(call.SystemPrompt) {
		t.Errorf("system_prompt_bytes = %d, want %d", call.SystemPromptBytes, len(call.SystemPrompt))
	}
	if call.SystemPromptBytes == 0 {
		t.Fatal("system_prompt_bytes = 0, want non-zero")
	}
}

func TestSession_TranscriptClosedOnSessionClose(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
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
	_, _, _, err = ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("transcript not readable after Close: %v", err)
	}
}

func TestSubagent_TranscriptHasParentLinkage(t *testing.T) {
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		StateDir:        stateDir,
		ParentSessionID: "parent-session-123",
		SubagentTask:    "implement auth middleware",
		Depth:           2,
	}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(t.TempDir()), cfg)
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
	hdr, _, _, err := ReadTranscript(files[0])
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
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Root session: no parent fields set.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
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
	hdr, _, _, err := ReadTranscript(files[0])
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
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		Depth: 3,
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
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", bigContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Use a very small context window to force compaction.
	profile := &baseProfile{
		id:            "openai",
		model:         "gpt-5.2",
		contextWindow: 500,
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defCommunicate(),
		},
	}

	sess, err := NewSession(c, profile, env, SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events in background to prevent blocking.
	var events []SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			events = append(events, ev)
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
	for _, e := range events {
		if e.Kind == EventContextCompaction {
			foundCompaction = true
		}
	}
	if !foundCompaction {
		t.Fatal("expected CONTEXT_COMPACTION event with small context window")
	}

	// --- Read the transcript ---
	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	hdr, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}

	// --- Verify header ---
	if hdr.Kind != "header" {
		t.Errorf("header kind = %q, want %q", hdr.Kind, "header")
	}
	if hdr.FormatVersion != 1 {
		t.Errorf("FormatVersion = %d, want 1", hdr.FormatVersion)
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
	// Seq numbers may have gaps due to interleaved api_call lines.
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
	kinds := map[TurnKind]int{}
	for _, e := range entries {
		kinds[e.Turn.Kind]++
	}

	// Must have user input, assistant, and tool results turns.
	if kinds[TurnUserInput] == 0 {
		t.Error("no USER_INPUT turns in transcript")
	}
	if kinds[TurnAssistant] == 0 {
		t.Error("no ASSISTANT turns in transcript")
	}
	if kinds[TurnToolResults] == 0 {
		t.Error("no TOOL_RESULTS turns in transcript")
	}

	// Must have at least one compaction turn (checkpoint or summary).
	if kinds[TurnCheckpoint]+kinds[TurnSummary] == 0 {
		t.Errorf("no compaction turns (CHECKPOINT or SUMMARY) in transcript; kinds: %v", kinds)
	}

	// --- Verify compaction turn is sequenced correctly ---
	// The compaction turn should appear after the turns that preceded it, not at the very start.
	var firstCompactionSeq int = -1
	for _, e := range entries {
		if e.Turn.Kind == TurnCheckpoint || e.Turn.Kind == TurnSummary {
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
		if e.Turn.Kind == TurnToolResults {
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

func TestCheckpoint_UsesTurnCheckpointKind(t *testing.T) {
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"login.go"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | package main\n", false)},
		{Kind: TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"login.go","old_string":"old","new_string":"new"}`)},
		{Kind: TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 2, nil, "communicate")

	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[0].Kind != TurnCheckpoint {
		t.Fatalf("checkpoint turn kind = %q, want %q", result[0].Kind, TurnCheckpoint)
	}
	// The text content should still have [CONTEXT CHECKPOINT] header.
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("checkpoint missing header: %q", text)
	}
}

func TestSummarizeWithLLM_UsesTurnSummaryKind(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("Summary: fixed auth bug")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewContextManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	if len(result) < 1 {
		t.Fatalf("expected at least 1 turn, got %d", len(result))
	}
	if result[0].Kind != TurnSummary {
		t.Fatalf("summary turn kind = %q, want %q", result[0].Kind, TurnSummary)
	}
	// The text content should still have [CONTEXT SUMMARY] header.
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT SUMMARY]") {
		t.Fatalf("summary missing header: %q", text)
	}
}

func TestMaybeCompact_CallsOnCompactionTurn(t *testing.T) {
	// Use a tiny context window to force checkpoint (L3).
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 500}
	cm := NewContextManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Use assistant text (not tool results) so observation masking can't reduce pressure.
	// Need >80% of 500 = 400 tokens.
	history := []Turn{{Kind: TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for EstimateTokens(history) < 425 {
		history = append(history,
			Turn{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 50))},
		)
	}
	history = append(history,
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		Turn{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var callbackTurns []Turn
	cm.OnCompactionTurn = func(turn Turn) {
		callbackTurns = append(callbackTurns, turn)
	}

	emitFn := func(kind EventKind, data any) {}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	if len(callbackTurns) == 0 {
		t.Fatal("expected OnCompactionTurn callback to be called")
	}
	if callbackTurns[0].Kind != TurnCheckpoint {
		t.Fatalf("callback turn kind = %q, want %q", callbackTurns[0].Kind, TurnCheckpoint)
	}
	if !strings.Contains(callbackTurns[0].Message.Text(), "[CONTEXT CHECKPOINT]") {
		t.Fatalf("callback turn missing checkpoint text: %q", callbackTurns[0].Message.Text())
	}
}

// --- Sub-agent transcript persistence ---

func TestSubagent_TranscriptPersistsAfterCloseAgent(t *testing.T) {
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Sub-agent's response (consumed by the spawned child session).
			func(req llm.Request) llm.Response {
				return finalResponse("sub-agent done")
			},
		},
	})

	parentSess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain parent events so Close doesn't block.
	go func() {
		for range parentSess.Events() {
		}
	}()

	// Spawn a sub-agent via the tool registry (matches existing test patterns).
	// Inject the tool call ID via context so spawnAgent can record it in the transcript header.
	spawnCtx := context.WithValue(context.Background(), ctxToolCallID, "call_spawn_1")
	spawnRes := parentSess.reg.ExecuteCall(spawnCtx, parentSess.env, llm.ToolCallData{
		ID:        "call_spawn_1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"implement auth middleware"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn_agent output: %v (out=%q)", err, spawnRes.Output)
	}
	agentID := fmt.Sprint(spawned["agent_id"])
	if agentID == "" {
		t.Fatalf("missing agent_id in spawn output: %v", spawned)
	}

	// Wait for the sub-agent to complete.
	waitRes := parentSess.reg.ExecuteCall(context.Background(), parentSess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	// Close the sub-agent (this calls sub.sess.Close(), closing the transcript).
	closeRes := parentSess.reg.ExecuteCall(context.Background(), parentSess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "close_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if closeRes.IsError {
		t.Fatalf("close_agent error: %s", closeRes.Output)
	}

	// Close the parent session.
	parentSess.Close()

	// Both sessions should have transcript files in the shared stateDir.
	files, _ := filepath.Glob(filepath.Join(stateDir, sessionsSubdir, "*.transcript.jsonl"))
	if len(files) != 2 {
		t.Fatalf("expected 2 transcript files, got %d", len(files))
	}

	// Identify parent and sub-agent transcripts by checking ParentSessionID.
	var parentHeader, subHeader TranscriptHeader
	var subEntries []TranscriptEntry
	foundParent, foundSub := false, false
	for _, f := range files {
		hdr, entries, _, err := ReadTranscript(f)
		if err != nil {
			t.Fatalf("ReadTranscript(%s): %v", f, err)
		}
		if hdr.ParentSessionID == "" {
			parentHeader = hdr
			foundParent = true
		} else {
			subHeader = hdr
			subEntries = entries
			foundSub = true
		}
	}
	if !foundParent {
		t.Fatal("no parent transcript found (expected one with empty ParentSessionID)")
	}
	if !foundSub {
		t.Fatal("no sub-agent transcript found (expected one with non-empty ParentSessionID)")
	}

	// The parent transcript's SessionID should match the parent session's ID.
	if parentHeader.SessionID != parentSess.ID() {
		t.Errorf("parent SessionID = %q, want %q", parentHeader.SessionID, parentSess.ID())
	}

	// The sub-agent's ParentSessionID should point to the parent's actual ID.
	if subHeader.ParentSessionID != parentSess.ID() {
		t.Errorf("sub-agent ParentSessionID = %q, want %q", subHeader.ParentSessionID, parentSess.ID())
	}

	// The sub-agent's Task should match what was passed to spawn_agent.
	if subHeader.Task != "implement auth middleware" {
		t.Errorf("sub-agent Task = %q, want %q", subHeader.Task, "implement auth middleware")
	}

	// The sub-agent's Depth should be 1 (parent is depth 0).
	if subHeader.Depth != 1 {
		t.Errorf("sub-agent Depth = %d, want 1", subHeader.Depth)
	}

	// The sub-agent's ParentToolCallID should match the tool call that spawned it.
	if subHeader.ParentToolCallID != "call_spawn_1" {
		t.Errorf("sub-agent ParentToolCallID = %q, want %q", subHeader.ParentToolCallID, "call_spawn_1")
	}

	// The sub-agent transcript should contain at least a user input and assistant response.
	if len(subEntries) < 2 {
		t.Fatalf("expected at least 2 sub-agent transcript entries, got %d", len(subEntries))
	}
	if subEntries[0].Turn.Kind != TurnUserInput {
		t.Errorf("sub-agent entry 0 kind = %q, want %q", subEntries[0].Turn.Kind, TurnUserInput)
	}
	// The sub-agent's first input should be the task.
	if subEntries[0].Turn.Message.Text() != "implement auth middleware" {
		t.Errorf("sub-agent entry 0 text = %q, want %q", subEntries[0].Turn.Message.Text(), "implement auth middleware")
	}
}

func TestSession_TranscriptWriteFailureEmitsWarning(t *testing.T) {
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("first")
			},
			func(req llm.Request) llm.Response {
				return finalResponse("second")
			},
		},
	})

	env := NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{StateDir: stateDir}
	sess, err := NewSession(c, &baseProfile{
		id:            "openai",
		model:         "test",
		contextWindow: 100000,
	}, env, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Force-close the transcript file to cause future writes to fail.
	sess.transcript.file.Close()

	// Collect events.
	var warnings []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == EventWarning {
				if wd, ok := ev.Data.(WarningData); ok {
					warnings = append(warnings, wd.Message)
				}
			}
		}
	}()

	// Process input: should succeed despite transcript failures.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput failed: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}

	sess.Close()
	<-done

	// Should have at least one warning about transcript write failure.
	hasTranscriptWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "transcript") {
			hasTranscriptWarning = true
			break
		}
	}
	if !hasTranscriptWarning {
		t.Errorf("expected transcript write warning, got warnings: %v", warnings)
	}
}

// --- Fix 1: seq increment after successful write ---

func TestTranscriptWriter_SeqNotIncrementedOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-seq-fix",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	// Write one successful entry (seq 0).
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("first"))); err != nil {
		t.Fatalf("Append 0: %v", err)
	}

	// Close the underlying file to force the next write to fail.
	w.mu.Lock()
	w.file.Close()
	w.mu.Unlock()

	// This Append should fail (file is closed).
	err = w.Append(NewTurn(TurnAssistant, llm.Assistant("should fail")))
	if err == nil {
		t.Fatal("expected error from Append on closed file")
	}

	// Reopen the file so future writes succeed.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	w.mu.Lock()
	w.file = f
	w.mu.Unlock()

	// The next successful write should use seq 1 (not seq 2, which would
	// indicate the failed write incremented seq).
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("after failure"))); err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}

	// Read back and check seq numbers.
	lines := readTranscriptLines(t, path)
	// header + 2 successful entries
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	var entry0, entry1 TranscriptEntry
	json.Unmarshal([]byte(lines[1]), &entry0)
	json.Unmarshal([]byte(lines[2]), &entry1)

	if entry0.Seq != 0 {
		t.Errorf("entry0 seq = %d, want 0", entry0.Seq)
	}
	if entry1.Seq != 1 {
		t.Errorf("entry1 seq = %d, want 1 (no gap from failed write)", entry1.Seq)
	}
}

// --- Fix 2: ReadTranscript returns corrupt line count ---

func TestReadTranscript_ReturnsCorruptLineCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write a valid transcript with 3 entries.
	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-corrupt",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	for i := 0; i < 3; i++ {
		w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i))))
	}
	w.Close()

	// Insert two corrupt lines into the middle.
	data, _ := os.ReadFile(path)
	lines := bytes.Split(data, []byte("\n"))
	// lines: header, entry0, entry1, entry2, "" (trailing)
	// Insert corrupt lines between entry1 and entry2.
	var rebuilt [][]byte
	rebuilt = append(rebuilt, lines[0]) // header
	rebuilt = append(rebuilt, lines[1]) // entry0
	rebuilt = append(rebuilt, []byte(`{not valid json`))
	rebuilt = append(rebuilt, lines[2]) // entry1
	rebuilt = append(rebuilt, []byte(`also corrupt`))
	rebuilt = append(rebuilt, lines[3]) // entry2
	os.WriteFile(path, bytes.Join(rebuilt, []byte("\n")), 0o644)
	// Append final newline.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString("\n")
	f.Close()

	_, entries, skipped, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 valid entries, got %d", len(entries))
	}
	if skipped != 2 {
		t.Errorf("expected 2 skipped lines, got %d", skipped)
	}
}

func TestReadTranscript_ZeroCorruptLinesOnCleanFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-clean",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	w.Append(NewTurn(TurnAssistant, llm.Assistant("msg")))
	w.Close()

	_, entries, skipped, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped lines, got %d", skipped)
	}
}

// --- Fix 3: OpenTranscriptWriter single file handle ---

func TestOpenTranscriptWriter_SingleFileHandle(t *testing.T) {
	// This test verifies that OpenTranscriptWriter uses a single file handle
	// for read-truncate-append, not separate open calls. We do this by
	// verifying that a partial-line truncation + append works correctly
	// even in a single operation. If there were separate open calls, we
	// couldn't observe a difference directly, but we verify the behavior
	// is correct (truncation + append with correct seq).
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write header + 2 entries.
	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-handle",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	w.Append(NewTurn(TurnAssistant, llm.Assistant("msg 0")))
	w.Append(NewTurn(TurnAssistant, llm.Assistant("msg 1")))
	w.Close()

	// Append a partial line to simulate a crash.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"kind":"entry","seq":2`)
	f.Close()

	// Open for resume: should truncate partial line and continue at seq 2.
	w2, err := OpenTranscriptWriter(path)
	if err != nil {
		t.Fatalf("OpenTranscriptWriter: %v", err)
	}
	if err := w2.Append(NewTurn(TurnAssistant, llm.Assistant("msg 2"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w2.Close()

	// Read back: should have header + 3 clean entries, no partial line.
	_, entries, skipped, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	dir := t.TempDir()
	stateDir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	header, _, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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

func TestTranscriptWriter_PeriodicSync_SkipsSyncWithinInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-sync-001",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	// Set a long sync interval so rapid writes don't trigger sync.
	w.SyncInterval = 1 * time.Hour

	// Write several entries rapidly.
	for i := 0; i < 5; i++ {
		if err := w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// The dirty flag should be true (writes happened but no sync since header).
	w.mu.Lock()
	dirty := w.dirty
	w.mu.Unlock()
	if !dirty {
		t.Error("expected dirty=true after writes within sync interval")
	}

	// Close should flush all data.
	w.Close()

	// All data should be readable after Close.
	_, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
}

func TestTranscriptWriter_PeriodicSync_SyncsAfterIntervalExpires(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-sync-002",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	// Set a very short sync interval.
	w.SyncInterval = 1 * time.Millisecond

	// Write first entry.
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("first"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Wait for interval to expire.
	time.Sleep(5 * time.Millisecond)

	// Next write should trigger a sync because the interval has elapsed.
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("second"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// After the sync, dirty should be false.
	w.mu.Lock()
	dirty := w.dirty
	w.mu.Unlock()
	if dirty {
		t.Error("expected dirty=false after sync interval elapsed and write occurred")
	}
}

func TestTranscriptWriter_PeriodicSync_ZeroIntervalSyncsEveryWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-sync-003",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	// Zero interval = sync every write (backward compat).
	w.SyncInterval = 0

	for i := 0; i < 3; i++ {
		if err := w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		// After each write with zero interval, dirty should be false.
		w.mu.Lock()
		dirty := w.dirty
		w.mu.Unlock()
		if dirty {
			t.Errorf("Append %d: expected dirty=false with zero SyncInterval", i)
		}
	}
}

func TestTranscriptWriter_PeriodicSync_CloseFlushesDirtyWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-sync-004",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	// Long interval so no auto-sync happens.
	w.SyncInterval = 1 * time.Hour

	// Write entries without syncing.
	for i := 0; i < 10; i++ {
		if err := w.Append(NewTurn(TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// Close must flush.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// All entries must be readable.
	_, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-sync-005",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
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
				turn := NewTurn(TurnAssistant, llm.Assistant("concurrent"))
				if err := w.Append(turn); err != nil {
					t.Errorf("goroutine %d append %d: %v", id, j, err)
				}
			}
		}(i)
	}

	wg.Wait()
	w.Close()

	_, entries, _, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}

	expectedTotal := numGoroutines * turnsPerGoroutine
	if len(entries) != expectedTotal {
		t.Fatalf("expected %d entries, got %d", expectedTotal, len(entries))
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

func TestOpenTranscriptWriter_SetsLastSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Create a transcript.
	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-sync-006",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	w.Append(NewTurn(TurnAssistant, llm.Assistant("msg 0")))
	w.Close()

	// Reopen for resume.
	before := time.Now()
	w2, err := OpenTranscriptWriter(path)
	if err != nil {
		t.Fatalf("OpenTranscriptWriter: %v", err)
	}
	defer w2.Close()

	// lastSync should be set to approximately now (not zero).
	w2.mu.Lock()
	ls := w2.lastSync
	w2.mu.Unlock()
	if ls.Before(before) {
		t.Errorf("lastSync = %v, expected >= %v", ls, before)
	}
}

// --- TranscriptAPICall tests ---

func TestTranscriptWriter_AppendAPICallWritesValidLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-api-001",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	call := TranscriptAPICall{
		Round:               1,
		Timestamp:           "2026-03-25T12:00:00Z",
		LatencyMs:           1500,
		SystemPrompt:        "You are a helpful assistant.",
		ContextHistoryTurns: 3,
		SystemPromptBytes:   len("You are a helpful assistant."),
		Request: llm.APILogRequest{
			Model:        "gpt-5.2",
			Provider:     "openai",
			MessageCount: 3,
			ToolCount:    2,
			ToolNames:    []string{"read_file", "write_file"},
		},
		Response: &llm.APILogResponse{
			ID:            "resp-123",
			Model:         "gpt-5.2",
			FinishReason:  "stop",
			TextLength:    42,
			ToolCallCount: 0,
		},
	}

	if err := w.AppendAPICall(call); err != nil {
		t.Fatalf("AppendAPICall: %v", err)
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 2 { // header + 1 api_call
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var got TranscriptAPICall
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("unmarshal api_call: %v", err)
	}
	if got.Kind != "api_call" {
		t.Errorf("kind = %q, want %q", got.Kind, "api_call")
	}
	if got.Seq != 0 {
		t.Errorf("seq = %d, want 0", got.Seq)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1", got.Round)
	}
	if got.LatencyMs != 1500 {
		t.Errorf("latency_ms = %d, want 1500", got.LatencyMs)
	}
	if got.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("system_prompt = %q, want %q", got.SystemPrompt, "You are a helpful assistant.")
	}
	if got.ContextHistoryTurns != 3 {
		t.Errorf("context_history_turns = %d, want 3", got.ContextHistoryTurns)
	}
	if got.SystemPromptBytes != len("You are a helpful assistant.") {
		t.Errorf("system_prompt_bytes = %d, want %d", got.SystemPromptBytes, len("You are a helpful assistant."))
	}
	if got.Request.Model != "gpt-5.2" {
		t.Errorf("request.model = %q, want %q", got.Request.Model, "gpt-5.2")
	}
	if got.Request.Provider != "openai" {
		t.Errorf("request.provider = %q, want %q", got.Request.Provider, "openai")
	}
	if got.Request.ToolCount != 2 {
		t.Errorf("request.tool_count = %d, want 2", got.Request.ToolCount)
	}
	if got.Response == nil {
		t.Fatal("response is nil, expected non-nil")
	}
	if got.Response.FinishReason != "stop" {
		t.Errorf("response.finish_reason = %q, want %q", got.Response.FinishReason, "stop")
	}
}

func TestTranscriptWriter_AppendAPICallWithError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-api-err",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	call := TranscriptAPICall{
		Round:     2,
		Timestamp: "2026-03-25T12:01:00Z",
		LatencyMs: 500,
		Request: llm.APILogRequest{
			Model:    "gpt-5.2",
			Provider: "openai",
		},
		Error: "context deadline exceeded",
	}

	if err := w.AppendAPICall(call); err != nil {
		t.Fatalf("AppendAPICall: %v", err)
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var got TranscriptAPICall
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("unmarshal api_call: %v", err)
	}
	if got.Kind != "api_call" {
		t.Errorf("kind = %q, want %q", got.Kind, "api_call")
	}
	if got.Error != "context deadline exceeded" {
		t.Errorf("error = %q, want %q", got.Error, "context deadline exceeded")
	}
	if got.Response != nil {
		t.Errorf("response should be nil for error case, got %+v", got.Response)
	}
}

func TestTranscriptWriter_InterleavedSeqNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-interleave",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	// Interleave: entry, api_call, entry, api_call, entry
	if err := w.Append(NewTurn(TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("Append 0: %v", err)
	}
	if err := w.AppendAPICall(TranscriptAPICall{Round: 1, Request: llm.APILogRequest{Model: "m"}}); err != nil {
		t.Fatalf("AppendAPICall 0: %v", err)
	}
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("hi"))); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if err := w.AppendAPICall(TranscriptAPICall{Round: 2, Request: llm.APILogRequest{Model: "m"}}); err != nil {
		t.Fatalf("AppendAPICall 1: %v", err)
	}
	if err := w.Append(NewTurn(TurnAssistant, llm.Assistant("done"))); err != nil {
		t.Fatalf("Append 2: %v", err)
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 6 { // header + 3 entries + 2 api_calls
		t.Fatalf("expected 6 lines, got %d", len(lines))
	}

	// Parse all non-header lines and verify seq numbers are 0,1,2,3,4.
	type seqLine struct {
		Kind string `json:"kind"`
		Seq  int    `json:"seq"`
	}
	expectedKinds := []string{"entry", "api_call", "entry", "api_call", "entry"}
	for i := 1; i < len(lines); i++ {
		var sl seqLine
		if err := json.Unmarshal([]byte(lines[i]), &sl); err != nil {
			t.Fatalf("unmarshal line %d: %v", i, err)
		}
		wantSeq := i - 1
		if sl.Seq != wantSeq {
			t.Errorf("line %d: seq = %d, want %d", i, sl.Seq, wantSeq)
		}
		if sl.Kind != expectedKinds[i-1] {
			t.Errorf("line %d: kind = %q, want %q", i, sl.Kind, expectedKinds[i-1])
		}
	}
}

func TestTranscriptWriter_NilAppendAPICallSafe(t *testing.T) {
	var w *TranscriptWriter

	// AppendAPICall on nil should not panic and should return nil.
	if err := w.AppendAPICall(TranscriptAPICall{}); err != nil {
		t.Errorf("nil AppendAPICall returned error: %v", err)
	}
}

func TestReadTranscriptFull_ParsesAllLineTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-full-001",
		CreatedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	// Write interleaved entries and api_calls.
	w.Append(NewTurn(TurnUserInput, llm.User("hello")))
	w.AppendAPICall(TranscriptAPICall{
		Round:     1,
		Timestamp: "2026-03-25T12:00:01Z",
		LatencyMs: 100,
		Request:   llm.APILogRequest{Model: "gpt-5.2", Provider: "openai"},
		Response:  &llm.APILogResponse{Model: "gpt-5.2", FinishReason: "stop"},
	})
	w.Append(NewTurn(TurnAssistant, llm.Assistant("hi")))
	w.AppendAPICall(TranscriptAPICall{
		Round:     2,
		Timestamp: "2026-03-25T12:00:02Z",
		LatencyMs: 200,
		Request:   llm.APILogRequest{Model: "gpt-5.2", Provider: "openai"},
		Error:     "rate limit",
	})
	w.Append(NewTurn(TurnAssistant, llm.Assistant("done")))
	w.Close()

	data, err := ReadTranscriptFull(path)
	if err != nil {
		t.Fatalf("ReadTranscriptFull: %v", err)
	}

	// Header
	if data.Header.SessionID != "sess-full-001" {
		t.Errorf("header session_id = %q, want %q", data.Header.SessionID, "sess-full-001")
	}

	// Entries
	if len(data.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(data.Entries))
	}
	if data.Entries[0].Turn.Kind != TurnUserInput {
		t.Errorf("entry 0 turn kind = %q, want %q", data.Entries[0].Turn.Kind, TurnUserInput)
	}
	if data.Entries[1].Turn.Kind != TurnAssistant {
		t.Errorf("entry 1 turn kind = %q, want %q", data.Entries[1].Turn.Kind, TurnAssistant)
	}

	// API Calls
	if len(data.APICalls) != 2 {
		t.Fatalf("expected 2 api_calls, got %d", len(data.APICalls))
	}
	if data.APICalls[0].Round != 1 {
		t.Errorf("api_call 0 round = %d, want 1", data.APICalls[0].Round)
	}
	if data.APICalls[0].Response == nil {
		t.Error("api_call 0 response should not be nil")
	}
	if data.APICalls[1].Error != "rate limit" {
		t.Errorf("api_call 1 error = %q, want %q", data.APICalls[1].Error, "rate limit")
	}
	if data.APICalls[1].Response != nil {
		t.Error("api_call 1 response should be nil for error case")
	}

	// Seq numbers should be interleaved correctly.
	if data.Entries[0].Seq != 0 {
		t.Errorf("entry 0 seq = %d, want 0", data.Entries[0].Seq)
	}
	if data.APICalls[0].Seq != 1 {
		t.Errorf("api_call 0 seq = %d, want 1", data.APICalls[0].Seq)
	}
	if data.Entries[1].Seq != 2 {
		t.Errorf("entry 1 seq = %d, want 2", data.Entries[1].Seq)
	}
	if data.APICalls[1].Seq != 3 {
		t.Errorf("api_call 1 seq = %d, want 3", data.APICalls[1].Seq)
	}
	if data.Entries[2].Seq != 4 {
		t.Errorf("entry 2 seq = %d, want 4", data.Entries[2].Seq)
	}

	if data.Skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", data.Skipped)
	}
}

func TestReadTranscriptFull_SkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-full-corrupt",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	w.Append(NewTurn(TurnUserInput, llm.User("hello")))
	w.Close()

	// Append a corrupt line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.WriteString("{bad json\n")
	f.Close()

	data, err := ReadTranscriptFull(path)
	if err != nil {
		t.Fatalf("ReadTranscriptFull: %v", err)
	}
	if len(data.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(data.Entries))
	}
	if data.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", data.Skipped)
	}
}

func TestReadTranscript_SkipsAPICallLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-compat",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	// Interleave entries and api_calls.
	w.Append(NewTurn(TurnUserInput, llm.User("hello")))
	w.AppendAPICall(TranscriptAPICall{Round: 1, Request: llm.APILogRequest{Model: "m"}})
	w.Append(NewTurn(TurnAssistant, llm.Assistant("hi")))
	w.Close()

	// ReadTranscript should only return entries, silently skipping api_call lines.
	_, entries, skipped, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped (api_call lines should not count as corrupt), got %d", skipped)
	}
	if entries[0].Turn.Kind != TurnUserInput {
		t.Errorf("entry 0 kind = %q, want %q", entries[0].Turn.Kind, TurnUserInput)
	}
	if entries[1].Turn.Kind != TurnAssistant {
		t.Errorf("entry 1 kind = %q, want %q", entries[1].Turn.Kind, TurnAssistant)
	}
}

func TestOpenTranscriptWriter_ResumesWithAPICallSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewTranscriptWriter(path, TranscriptHeader{
		SessionID: "sess-resume-api",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	// Write entry (seq 0), api_call (seq 1), entry (seq 2).
	w.Append(NewTurn(TurnUserInput, llm.User("hello")))
	w.AppendAPICall(TranscriptAPICall{Round: 1, Request: llm.APILogRequest{Model: "m"}})
	w.Append(NewTurn(TurnAssistant, llm.Assistant("hi")))
	w.Close()

	// Reopen and append more.
	w2, err := OpenTranscriptWriter(path)
	if err != nil {
		t.Fatalf("OpenTranscriptWriter: %v", err)
	}
	defer w2.Close()

	// Next seq should be 3 (one past the api_call and entries).
	w2.Append(NewTurn(TurnUserInput, llm.User("more")))

	data, err := ReadTranscriptFull(path)
	if err != nil {
		t.Fatalf("ReadTranscriptFull: %v", err)
	}

	if len(data.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(data.Entries))
	}
	if len(data.APICalls) != 1 {
		t.Fatalf("expected 1 api_call, got %d", len(data.APICalls))
	}

	// The resumed entry should have seq 3 (after seq 0, 1, 2).
	if data.Entries[2].Seq != 3 {
		t.Errorf("resumed entry seq = %d, want 3", data.Entries[2].Seq)
	}
}
