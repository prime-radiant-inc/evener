package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/serf/llm"
)

// TranscriptHeader is the first line of a transcript JSONL file.
type TranscriptHeader struct {
	Kind             string    `json:"kind"`           // Always "header"
	FormatVersion    int       `json:"format_version"` // Currently 1
	SessionID        string    `json:"session_id"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Task             string    `json:"task,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	ProfileID        string    `json:"profile_id"`
	Model            string    `json:"model"`
	WorkingDir       string    `json:"working_dir,omitempty"`
	Depth            int       `json:"depth,omitempty"`
	BuildVersion     string    `json:"build_version,omitempty"`
	SystemPrompt     string    `json:"system_prompt,omitempty"`
	// AgentTasks is the full task list the agent started with (from the
	// agent's YAML frontmatter for root sessions, or from the parent's
	// task_list parameter for spawned subagents). Captured at session
	// creation so the transcript is self-describing even for runs that
	// never call task_list(action="view") or fail before all STEERING
	// messages are emitted.
	AgentTasks []Task `json:"agent_tasks,omitempty"`
}

// TranscriptEntry is a single turn in the transcript JSONL file.
type TranscriptEntry struct {
	Kind string `json:"kind"` // Always "entry"
	Seq  int    `json:"seq"`
	Turn Turn   `json:"turn"`
}

// TranscriptAPICall records an LLM API call in the transcript JSONL file.
type TranscriptAPICall struct {
	Kind         string              `json:"kind"` // Always "api_call"
	Seq          int                 `json:"seq"`
	Round        int                 `json:"round"`
	Timestamp    string              `json:"ts"`
	LatencyMs    int64               `json:"latency_ms"`
	SystemPrompt string              `json:"system_prompt"`
	Request      llm.APILogRequest   `json:"request"`
	Response     *llm.APILogResponse `json:"response,omitempty"`
	Error        string              `json:"error,omitempty"`
	Source       string              `json:"source,omitempty"`
	Title        string              `json:"title,omitempty"`
	Hint         string              `json:"hint,omitempty"`
}

// TranscriptWriter appends turns to an immutable JSONL transcript file.
type TranscriptWriter struct {
	file      *os.File
	mu        sync.Mutex
	seq       int
	closeOnce sync.Once
	closed    atomic.Bool

	// SyncInterval controls how often Append calls fsync.
	// If 0, every Append fsyncs (backward-compatible default for tests).
	// If >0, Append only fsyncs when this duration has elapsed since the last sync.
	SyncInterval time.Duration

	dirty    bool
	lastSync time.Time
}

// NewTranscriptWriter creates a transcript file at path, writes the header as the first line,
// and returns a writer that keeps the file handle open for subsequent Append calls.
func NewTranscriptWriter(path string, header TranscriptHeader) (*TranscriptWriter, error) {
	header.Kind = "header"
	header.FormatVersion = 1

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create transcript file: %w", err)
	}

	data, err := json.Marshal(header)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("marshal transcript header: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return nil, fmt.Errorf("write transcript header: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("sync transcript header: %w", err)
	}

	return &TranscriptWriter{file: f, lastSync: time.Now()}, nil
}

// Append writes a turn as a TranscriptEntry to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
func (w *TranscriptWriter) Append(turn Turn) error {
	if w == nil || w.closed.Load() {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Re-check after acquiring lock: Close may have raced between the
	// fast-path check above and the lock acquisition.
	if w.closed.Load() {
		return nil
	}

	entry := TranscriptEntry{
		Kind: "entry",
		Seq:  w.seq,
		Turn: turn,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal transcript entry: %w", err)
	}

	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write transcript entry: %w", err)
	}

	w.dirty = true
	if w.SyncInterval == 0 || time.Since(w.lastSync) >= w.SyncInterval {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("sync transcript entry: %w", err)
		}
		w.lastSync = time.Now()
		w.dirty = false
	}

	w.seq++
	return nil
}

// AppendAPICall writes an API call record to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
// Shares the seq counter with Append so entries and api_calls are interleaved in order.
func (w *TranscriptWriter) AppendAPICall(call TranscriptAPICall) error {
	if w == nil || w.closed.Load() {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Re-check after acquiring lock: Close may have raced between the
	// fast-path check above and the lock acquisition.
	if w.closed.Load() {
		return nil
	}

	call.Kind = "api_call"
	call.Seq = w.seq

	data, err := json.Marshal(call)
	if err != nil {
		return fmt.Errorf("marshal transcript api_call: %w", err)
	}

	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write transcript api_call: %w", err)
	}

	w.dirty = true
	if w.SyncInterval == 0 || time.Since(w.lastSync) >= w.SyncInterval {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("sync transcript api_call: %w", err)
		}
		w.lastSync = time.Now()
		w.dirty = false
	}

	w.seq++
	return nil
}

// Close syncs and closes the underlying file. Idempotent: safe to call multiple times.
// No-op if the receiver is nil.
func (w *TranscriptWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}

	// Acquire mu so any in-flight Append finishes before we close.
	w.mu.Lock()
	w.closed.Store(true)
	w.mu.Unlock()

	var closeErr error
	w.closeOnce.Do(func() {
		// Flush any dirty writes before closing.
		if w.dirty {
			if err := w.file.Sync(); err != nil {
				closeErr = fmt.Errorf("sync transcript on close: %w", err)
			}
			w.dirty = false
		}
		if err := w.file.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("close transcript file: %w", err)
		}
	})
	return closeErr
}

// OpenTranscriptWriter opens an existing transcript file for appending.
// Reads the file once to count valid entries and determine the next seq number.
// Truncates any partial last line for crash recovery. Uses a single file handle
// for the entire read-truncate-append sequence to avoid TOCTOU races.
func OpenTranscriptWriter(path string) (*TranscriptWriter, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open transcript for resume: %w", err)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read transcript for resume: %w", err)
	}

	// Truncate any trailing partial line: if the file doesn't end with '\n',
	// find the last newline and truncate there.
	validLen := int64(len(data))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL < 0 {
			f.Close()
			return nil, fmt.Errorf("transcript has no complete lines")
		}
		validLen = int64(lastNL + 1)
		data = data[:validLen]
		if err := f.Truncate(validLen); err != nil {
			f.Close()
			return nil, fmt.Errorf("truncate partial line: %w", err)
		}
	}

	// Parse entries to find the max seq number.
	maxSeq := -1
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue // skip header
		}
		var entry TranscriptEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && (entry.Kind == "entry" || entry.Kind == "api_call") {
			if entry.Seq > maxSeq {
				maxSeq = entry.Seq
			}
		}
	}

	if err := scanner.Err(); err != nil {
		f.Close()
		return nil, fmt.Errorf("scanning transcript entries: %w", err)
	}

	// Use max(seq)+1 so resumed writes never collide with existing entries,
	// even if earlier entries were lost to crash recovery.
	nextSeq := 0
	if maxSeq >= 0 {
		nextSeq = maxSeq + 1
	}

	// Seek to end for subsequent appends.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek to end of transcript: %w", err)
	}

	return &TranscriptWriter{file: f, seq: nextSeq, lastSync: time.Now()}, nil
}

// ReadTranscript reads a transcript JSONL file, returning the header, all valid entries,
// and the count of skipped (corrupt/partial) lines. Callers can use the skipped count
// to decide whether to warn about data loss from crash recovery.
func ReadTranscript(path string) (TranscriptHeader, []TranscriptEntry, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptHeader{}, nil, 0, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// First line must be the header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return TranscriptHeader{}, nil, 0, fmt.Errorf("reading transcript header: %w", err)
		}
		return TranscriptHeader{}, nil, 0, fmt.Errorf("transcript file is empty: no header")
	}

	var header TranscriptHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return TranscriptHeader{}, nil, 0, fmt.Errorf("parsing transcript header: %w", err)
	}

	// Remaining lines are entries. Skip non-entry lines (e.g. api_call) and
	// any that fail to parse.
	var entries []TranscriptEntry
	skipped := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry TranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip corrupt/partial lines (crash recovery).
			skipped++
			continue
		}
		if entry.Kind != "entry" {
			continue // skip non-entry lines (e.g. api_call)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return TranscriptHeader{}, nil, 0, fmt.Errorf("reading transcript: %w", err)
	}

	return header, entries, skipped, nil
}

// TranscriptData holds all parsed content from a transcript JSONL file.
type TranscriptData struct {
	Header   TranscriptHeader
	Entries  []TranscriptEntry
	APICalls []TranscriptAPICall
	Skipped  int
}

// ReadTranscriptFull reads a transcript JSONL file, returning all line types:
// header, entries, and API calls. Corrupt/partial lines are counted in Skipped.
func ReadTranscriptFull(path string) (TranscriptData, error) {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptData{}, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// First line must be the header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return TranscriptData{}, fmt.Errorf("reading transcript header: %w", err)
		}
		return TranscriptData{}, fmt.Errorf("transcript file is empty: no header")
	}

	var data TranscriptData
	if err := json.Unmarshal(scanner.Bytes(), &data.Header); err != nil {
		return TranscriptData{}, fmt.Errorf("parsing transcript header: %w", err)
	}

	// Remaining lines are entries or api_calls. Dispatch by "kind" field.
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Peek at the kind field to decide which struct to unmarshal into.
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			data.Skipped++
			continue
		}

		switch peek.Kind {
		case "entry":
			var entry TranscriptEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				data.Skipped++
				continue
			}
			data.Entries = append(data.Entries, entry)
		case "api_call":
			var call TranscriptAPICall
			if err := json.Unmarshal(line, &call); err != nil {
				data.Skipped++
				continue
			}
			data.APICalls = append(data.APICalls, call)
		default:
			data.Skipped++
		}
	}
	if err := scanner.Err(); err != nil {
		return TranscriptData{}, fmt.Errorf("reading transcript: %w", err)
	}

	return data, nil
}

// ResumeHistory extracts the history needed for session resume from transcript entries.
// If a compaction turn (CHECKPOINT or SUMMARY) exists, returns [last compaction turn, ...subsequent turns].
// Otherwise returns all turns.
func ResumeHistory(entries []TranscriptEntry) []Turn {
	// Scan backward for the last compaction turn.
	compactionIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		kind := entries[i].Turn.Kind
		if kind == TurnCheckpoint || kind == TurnSummary {
			compactionIdx = i
			break
		}
	}

	if compactionIdx < 0 {
		// No compaction: return all turns.
		turns := make([]Turn, len(entries))
		for i, e := range entries {
			turns[i] = e.Turn
		}
		return turns
	}

	// Return compaction turn + everything after it.
	result := make([]Turn, 0, len(entries)-compactionIdx)
	for i := compactionIdx; i < len(entries); i++ {
		result = append(result, entries[i].Turn)
	}
	return result
}
