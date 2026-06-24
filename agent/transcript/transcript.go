// Package transcript defines the on-disk JSONL transcript format and the
// append-only writer that records an agent session's turns and API calls.
// Readers live with their consumers; this package owns the write side and the
// shared line schema (Header, Entry, APICall).
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

const transcriptJSONLMaxLineBytes = 128 << 20

// Header is the first line of a transcript JSONL file.
type Header struct {
	Kind          string `json:"kind"`           // Always "header"
	FormatVersion int    `json:"format_version"` // Currently 1
	SessionID     string `json:"session_id"`     // ID of the session this transcript records
	// ParentSessionID and ParentToolCallID are set only for spawned subagent
	// transcripts: the parent session and the tool call that spawned this run.
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Task             string    `json:"task,omitempty"`          // task description for a spawned subagent
	CreatedAt        time.Time `json:"created_at"`              // when the session was created
	ProfileID        string    `json:"profile_id"`              // provider profile ID at creation
	Model            string    `json:"model"`                   // model name at creation
	WorkingDir       string    `json:"working_dir,omitempty"`   // the agent's working directory
	Depth            int       `json:"depth,omitempty"`         // subagent nesting depth (0 for root)
	BuildVersion     string    `json:"build_version,omitempty"` // serf build version that wrote the file
	SystemPrompt     string    `json:"system_prompt,omitempty"` // initial system prompt
	// AgentTasks is the full task list the agent started with (from the
	// agent's YAML frontmatter for root sessions, or from the parent's
	// task_list parameter for spawned subagents). Captured at session
	// creation so the transcript is self-describing even for runs that
	// never call task_list(action="view") or fail before all STEERING
	// messages are emitted.
	AgentTasks []task.Task `json:"agent_tasks,omitempty"`
}

// Entry is a single turn in the transcript JSONL file.
type Entry struct {
	Kind string      `json:"kind"` // Always "entry"
	Seq  int         `json:"seq"`  // monotonically increasing line sequence number
	Turn schema.Turn `json:"turn"` // the recorded conversation turn
}

// APICall records an LLM API call in the transcript JSONL file.
type APICall struct {
	Kind                   string              `json:"kind"`  // Always "api_call"
	Seq                    int                 `json:"seq"`   // line sequence number in the transcript
	Round                  int                 `json:"round"` // tool-call round within the turn
	AttemptIndex           int                 `json:"attempt_index,omitempty"`
	AttemptCount           int                 `json:"attempt_count,omitempty"`
	FinalAttemptCount      *int                `json:"final_attempt_count,omitempty"`
	HistoryMode            llm.HistoryMode     `json:"history_mode,omitempty"`
	PreviousResponseIDHash string              `json:"previous_response_id_hash,omitempty"`
	ConversationIDHash     string              `json:"conversation_id_hash,omitempty"`
	Timestamp              string              `json:"ts"`                              // RFC3339 time the round started
	LatencyMs              int64               `json:"latency_ms"`                      // LLM call latency in milliseconds
	SystemPrompt           string              `json:"system_prompt"`                   // system prompt sent on this call
	ContextHistoryTurns    int                 `json:"context_history_turns,omitempty"` // number of history turns in the request
	SystemPromptBytes      int                 `json:"system_prompt_bytes,omitempty"`   // byte length of SystemPrompt
	Request                llm.APILogRequest   `json:"request"`                         // sanitized request log
	Response               *llm.APILogResponse `json:"response,omitempty"`              // sanitized response log; nil on error
	Error                  string              `json:"error,omitempty"`                 // error message when the call failed
	// Source, Title, and Hint are the diagnostic classification of Error
	// (provider/model/etc.), populated only on failed calls.
	Source string `json:"source,omitempty"`
	Title  string `json:"title,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Writer appends turns to an immutable JSONL transcript file.
type Writer struct {
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

// NewWriter creates a transcript file at path, writes the header as the first line,
// and returns a writer that keeps the file handle open for subsequent Append calls.
func NewWriter(path string, header Header) (*Writer, error) {
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
		_ = f.Close() // cleanup on error path; the marshal error is what matters
		return nil, fmt.Errorf("marshal transcript header: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close() // cleanup on error path; the write error is what matters
		return nil, fmt.Errorf("write transcript header: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close() // cleanup on error path; the sync error is what matters
		return nil, fmt.Errorf("sync transcript header: %w", err)
	}

	return &Writer{file: f, lastSync: time.Now()}, nil
}

// Append writes a turn as an Entry to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
func (w *Writer) Append(turn schema.Turn) error {
	return w.append(turn, false)
}

// AppendDurable writes a turn and fsyncs it before returning.
// Safe for concurrent use. No-op if the receiver is nil.
func (w *Writer) AppendDurable(turn schema.Turn) error {
	return w.append(turn, true)
}

func (w *Writer) append(turn schema.Turn, forceSync bool) error {
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

	entry := Entry{
		Kind: "entry",
		Seq:  w.seq,
		Turn: turn,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal transcript entry: %w", err)
	}

	var startOffset int64
	if forceSync {
		var err error
		startOffset, err = w.file.Seek(0, io.SeekEnd)
		if err != nil {
			return fmt.Errorf("seek transcript append start: %w", err)
		}
	}

	previousDirty := w.dirty
	if err := w.writeLineLocked(append(data, '\n')); err != nil {
		if forceSync {
			return w.appendFailureLocked("write transcript entry", err, startOffset)
		}
		return fmt.Errorf("write transcript entry: %w", err)
	}

	w.dirty = true
	if forceSync || w.SyncInterval == 0 || time.Since(w.lastSync) >= w.SyncInterval {
		if err := w.file.Sync(); err != nil {
			if forceSync {
				if rollbackErr := w.rollbackAppendLocked(startOffset); rollbackErr != nil {
					return fmt.Errorf("sync transcript entry: %w; rollback failed: %w", err, rollbackErr)
				}
				w.dirty = previousDirty
				return fmt.Errorf("sync transcript entry: %w", err)
			}
			return fmt.Errorf("sync transcript entry: %w", err)
		}
		w.lastSync = time.Now()
		w.dirty = false
	}

	w.seq++
	return nil
}

func (w *Writer) writeLineLocked(line []byte) error {
	for len(line) > 0 {
		n, err := w.file.Write(line)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		line = line[n:]
	}
	return nil
}

func (w *Writer) appendFailureLocked(operation string, err error, startOffset int64) error {
	if rollbackErr := w.rollbackAppendLocked(startOffset); rollbackErr != nil {
		return fmt.Errorf("%s: %w; rollback failed: %w", operation, err, rollbackErr)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (w *Writer) rollbackAppendLocked(startOffset int64) error {
	truncateErr := w.file.Truncate(startOffset)
	_, seekErr := w.file.Seek(0, io.SeekEnd)
	if truncateErr != nil && seekErr != nil {
		return fmt.Errorf("truncate to %d: %w; seek eof: %w", startOffset, truncateErr, seekErr)
	}
	if truncateErr != nil {
		return fmt.Errorf("truncate to %d: %w", startOffset, truncateErr)
	}
	if seekErr != nil {
		return fmt.Errorf("seek eof: %w", seekErr)
	}
	if syncErr := w.file.Sync(); syncErr != nil {
		return fmt.Errorf("sync rollback truncate: %w", syncErr)
	}
	return nil
}

// AppendAPICall writes an API call record to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
// Shares the seq counter with Append so entries and api_calls are interleaved in order.
func (w *Writer) AppendAPICall(call APICall) error {
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
func (w *Writer) Close() error {
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

// OpenWriter opens an existing transcript file for appending.
// Reads the file once to count valid entries and determine the next seq number.
// Truncates any partial last line for crash recovery. Uses a single file handle
// for the entire read-truncate-append sequence to avoid TOCTOU races.
func OpenWriter(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open transcript for resume: %w", err)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		_ = f.Close() // cleanup on error path; the read error is what matters
		return nil, fmt.Errorf("read transcript for resume: %w", err)
	}

	// Truncate any trailing partial line: if the file doesn't end with '\n',
	// find the last newline and truncate there.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL < 0 {
			_ = f.Close() // cleanup on error path; the validation error is what matters
			return nil, errors.New("transcript has no complete lines")
		}
		validLen := int64(lastNL + 1)
		data = data[:validLen]
		if err := f.Truncate(validLen); err != nil {
			_ = f.Close() // cleanup on error path; the truncate error is what matters
			return nil, fmt.Errorf("truncate partial line: %w", err)
		}
	}

	// Parse entries to find the max seq number.
	maxSeq := -1
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue // skip header
		}
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && (entry.Kind == "entry" || entry.Kind == "api_call") {
			if entry.Seq > maxSeq {
				maxSeq = entry.Seq
			}
		}
	}

	if err := scanner.Err(); err != nil {
		_ = f.Close() // cleanup on error path; the scan error is what matters
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
		_ = f.Close() // cleanup on error path; the seek error is what matters
		return nil, fmt.Errorf("seek to end of transcript: %w", err)
	}

	return &Writer{file: f, seq: nextSeq, lastSync: time.Now()}, nil
}
