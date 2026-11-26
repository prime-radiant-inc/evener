package transcript

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// freshEntryBytes encodes one entry the way the writer built its line before
// its buffer was reused: a new buffer per append, encoded straight out. It is
// the reference the writer's reused buffer is compared against.
func freshEntryBytes(t *testing.T, seq int, turn schema.Turn) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(Entry{Kind: "entry", Seq: seq, Turn: turn, MachineryFlagged: true}); err != nil {
		t.Fatalf("encode reference entry: %v", err)
	}
	return buf.Bytes()
}

// TestAppendWritesTheFreshBufferBytes proves the writer's reused encode buffer
// leaves the on-disk bytes exactly as a fresh buffer per append did. Each
// Record reports the turn it encoded, placement included, so every line on
// disk is compared byte for byte with a fresh buffer's encoding of that same
// turn. The appends run through PlaceSession, the placement a session's own
// writes take. The second and later appends reuse a buffer an earlier one
// grew, so a stale capacity, a leftover byte, or a shifted line boundary fails
// here rather than in a long-lived transcript reader.
func TestAppendWritesTheFreshBufferBytes(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterWithFS(fs, path, sharedFileHeader)
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SyncInterval = time.Hour
	w.BeginExecution("turn_bytes", false)

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("second reply, onto a reused buffer")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "a tool result body long enough to grow the buffer again", false)),
	}
	var recorded []Record
	for _, turn := range turns {
		rec, err := w.Record(turn, RecordOptions{Door: DoorBuffered, Place: PlaceSession})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		if !rec.Recorded {
			t.Fatalf("Record reported not recorded for %q", turn.Kind)
		}
		recorded = append(recorded, rec)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var wrote [][]byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var boundary struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &boundary); err != nil {
			t.Fatalf("decode line kind: %v", err)
		}
		if boundary.Kind == "entry" {
			wrote = append(wrote, line)
		}
	}
	if len(wrote) != len(recorded) {
		t.Fatalf("transcript holds %d entry lines, want the %d recorded", len(wrote), len(recorded))
	}
	for i, rec := range recorded {
		if want := freshEntryBytes(t, rec.Seq, rec.Turn); !bytes.Equal(wrote[i], want) {
			t.Fatalf("entry %d on disk = %q, want the fresh-buffer encoding %q", i, wrote[i], want)
		}
	}
}

// TestAppendReusesTheEncodeBuffer pins the reuse itself, without counting
// allocations: an append of a small turn onto a writer that already encoded a
// larger one keeps the larger capacity. A fresh buffer per append would leave
// only what the small turn needed.
func TestAppendReusesTheEncodeBuffer(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterWithFS(fs, path, sharedFileHeader)
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SyncInterval = time.Hour

	at := time.Unix(0, 0).UTC()
	big := schema.Turn{Kind: schema.TurnToolResults, Message: llm.ToolResult("call-1", strings.Repeat("x", 64<<10), false), Timestamp: at}
	if _, err := w.Record(big, RecordOptions{Door: DoorBuffered}); err != nil {
		t.Fatalf("Record big: %v", err)
	}
	grown := w.entryBuf.Cap()
	if grown < 64<<10 {
		t.Fatalf("the buffer holds %d bytes after a %d-byte turn", grown, 64<<10)
	}
	small := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("small"), Timestamp: at}
	if _, err := w.Record(small, RecordOptions{Door: DoorBuffered}); err != nil {
		t.Fatalf("Record small again: %v", err)
	}
	if got := w.entryBuf.Cap(); got != grown {
		t.Fatalf("a small append left the buffer at %d bytes, want the %d it already had: the buffer must be reused, not reallocated", got, grown)
	}
}

// TestAppendBatchWritesTheFreshBufferBytes proves the reused buffer does not
// blur a batch's entry boundaries: a batch encodes several entries into one
// buffer, so each line's start is where the previous line ended. Every line on
// disk is compared with a fresh buffer's encoding of that turn alone, which
// fails if the start offset or the per-entry length is taken from the wrong
// place.
func TestAppendBatchWritesTheFreshBufferBytes(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterWithFS(fs, path, sharedFileHeader)
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SyncInterval = time.Hour

	at := time.Unix(0, 0).UTC()
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("one"), Timestamp: at},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("two, a longer line than the first"), Timestamp: at},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call-1", "three", false), Timestamp: at},
	}
	if _, _, err := w.appendBatch(turns, PlaceVerbatim, DoorDurable); err != nil {
		t.Fatalf("appendBatch: %v", err)
	}
	after := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("four"), Timestamp: at}
	records, _, err := w.appendBatch([]schema.Turn{after}, PlaceVerbatim, DoorDurable)
	if err != nil {
		t.Fatalf("appendBatch (second): %v", err)
	}
	first := firstRecord(records).Seq
	if first != len(turns) {
		t.Fatalf("second batch firstSeq = %d, want %d", first, len(turns))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var wrote [][]byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var boundary struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &boundary); err != nil {
			t.Fatalf("decode line kind: %v", err)
		}
		if boundary.Kind == "entry" {
			wrote = append(wrote, line)
		}
	}
	all := make([]schema.Turn, 0, len(turns)+1)
	all = append(all, turns...)
	all = append(all, after)
	if len(wrote) != len(all) {
		t.Fatalf("transcript holds %d entry lines, want the %d written", len(wrote), len(all))
	}
	for i, turn := range all {
		if want := freshEntryBytes(t, i, turn); !bytes.Equal(wrote[i], want) {
			t.Fatalf("entry %d on disk = %q, want the fresh-buffer encoding %q", i, wrote[i], want)
		}
	}
	// Each entry's length is what advances the tail's recorded length, so a
	// boundary taken from the wrong offset would leave the writer reporting a
	// prefix that is not the file's.
	if got, want := w.RecordedLength(), int64(len(data)); got != want {
		t.Fatalf("recorded length %d, file %d: a batch entry's length was measured from the wrong offset", got, want)
	}
}

// TestAppendDropsAnEntryBufferPastTheRetentionCap pins that a batch whose
// buffer grew past maxRetainedEntryBuf is not kept: the writer holds no more
// than the cap between appends, so one oversized tool result cannot pin its
// memory for the session's life.
func TestAppendDropsAnEntryBufferPastTheRetentionCap(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterWithFS(fs, path, sharedFileHeader)
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SyncInterval = time.Hour

	oversized := schema.Turn{
		Kind:      schema.TurnToolResults,
		Message:   llm.ToolResult("call-1", strings.Repeat("x", maxRetainedEntryBuf), false),
		Timestamp: time.Unix(0, 0).UTC(),
	}
	if _, err := w.Record(oversized, RecordOptions{Door: DoorBuffered}); err != nil {
		t.Fatalf("Record oversized: %v", err)
	}
	if got := w.entryBuf.Cap(); got >= maxRetainedEntryBuf {
		t.Fatalf("the writer retained a %d-byte buffer after a %d-byte append, want it dropped", got, maxRetainedEntryBuf)
	}
}
