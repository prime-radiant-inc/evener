package agent

import (
	"bufio"
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
	_, entries, err := ReadTranscript(path)
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

	gotHeader, entries, err := ReadTranscript(path)
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

	gotHeader, entries, err := ReadTranscript(path)
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

	_, _, err := ReadTranscript(path)
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

	gotHeader, entries, err := ReadTranscript(path)
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
	_, entries, err := ReadTranscript(path)
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
	_, entries, err := ReadTranscript(path)
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
	_, entries, err := ReadTranscript(path)
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
	header, entries, err := ReadTranscript(tpath)
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
				return llm.Response{Message: llm.Assistant("hello back")}
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
	out, err := sess.ProcessInput(ctx, "hello")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "hello back" {
		t.Fatalf("unexpected output: %q", out)
	}
	sess.Close()

	// Read the transcript and verify entries were recorded.
	tpath := filepath.Join(stateDir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	header, entries, err := ReadTranscript(tpath)
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

	// Second entry should be assistant response.
	if entries[1].Turn.Kind != TurnAssistant {
		t.Errorf("second entry kind: got %q want %q", entries[1].Turn.Kind, TurnAssistant)
	}
	if entries[1].Turn.Message.Text() != "hello back" {
		t.Errorf("second entry text: got %q want %q", entries[1].Turn.Message.Text(), "hello back")
	}

	// Sequence numbers should be monotonically increasing.
	for i := 0; i < len(entries); i++ {
		if entries[i].Seq != i {
			t.Errorf("entry[%d].Seq = %d, want %d", i, entries[i].Seq, i)
		}
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
	_, _, err = ReadTranscript(tpath)
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
	go func() { for range sess.Events() {} }()

	// Read the transcript and verify parent linkage fields.
	files, _ := filepath.Glob(filepath.Join(stateDir, sessionsSubdir, "*.transcript.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 transcript, got %d", len(files))
	}
	hdr, _, err := ReadTranscript(files[0])
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

	go func() { for range sess.Events() {} }()

	files, _ := filepath.Glob(filepath.Join(stateDir, sessionsSubdir, "*.transcript.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 transcript, got %d", len(files))
	}
	hdr, _, err := ReadTranscript(files[0])
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
	go func() { for range sess.Events() {} }()

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
				return llm.Response{Message: llm.Assistant("finished reading")}
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
				return llm.Response{Message: llm.Assistant("done with second read")}
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
		basePrompt:    "You are a test agent.",
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
	out1, err := sess.ProcessInput(ctx, "read the big file")
	if err != nil {
		t.Fatalf("ProcessInput 1: %v", err)
	}
	if out1 == "" {
		t.Fatal("ProcessInput 1 returned empty")
	}

	// Second input: reads again, should trigger compaction.
	out2, err := sess.ProcessInput(ctx, "read it again")
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
	hdr, entries, err := ReadTranscript(tpath)
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
	for i, e := range entries {
		if e.Seq != i {
			t.Errorf("entry %d: seq = %d, want %d", i, e.Seq, i)
		}
		if e.Kind != "entry" {
			t.Errorf("entry %d: kind = %q, want %q", i, e.Kind, "entry")
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

	result := checkpoint(history, 2)

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
				return llm.Response{Message: llm.Assistant("Summary: fixed auth bug")}
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
				return llm.Response{Message: llm.Assistant("sub-agent done")}
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
	go func() { for range parentSess.Events() {} }()

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
		hdr, entries, err := ReadTranscript(f)
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
				return llm.Response{Message: llm.Assistant("first")}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("second")}
			},
		},
	})

	env := NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{StateDir: stateDir}
	sess, err := NewSession(c, &baseProfile{
		id:            "openai",
		model:         "test",
		contextWindow: 100000,
		basePrompt:    "test",
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
	out, err := sess.ProcessInput(ctx, "hello")
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
