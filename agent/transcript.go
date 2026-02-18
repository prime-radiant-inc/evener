package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TranscriptHeader is the first line of a transcript JSONL file.
type TranscriptHeader struct {
	Kind             string    `json:"kind"`              // Always "header"
	FormatVersion    int       `json:"format_version"`    // Currently 1
	SessionID        string    `json:"session_id"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Task             string    `json:"task,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	ProfileID        string    `json:"profile_id"`
	Model            string    `json:"model"`
	WorkingDir       string    `json:"working_dir,omitempty"`
	Depth            int       `json:"depth,omitempty"`
}

// TranscriptEntry is a single turn in the transcript JSONL file.
type TranscriptEntry struct {
	Kind string `json:"kind"` // Always "entry"
	Seq  int    `json:"seq"`
	Turn Turn   `json:"turn"`
}

// TranscriptWriter appends turns to an immutable JSONL transcript file.
type TranscriptWriter struct {
	file     *os.File
	mu       sync.Mutex
	seq      int
	closeOnce sync.Once
}

// NewTranscriptWriter creates a transcript file at path, writes the header as the first line,
// and returns a writer that keeps the file handle open for subsequent Append calls.
func NewTranscriptWriter(path string, header TranscriptHeader) (*TranscriptWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(header)
	if err != nil {
		f.Close()
		return nil, err
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return nil, err
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}

	return &TranscriptWriter{file: f}, nil
}

// Append writes a turn as a TranscriptEntry to the JSONL file.
// Safe for concurrent use. No-op if the receiver is nil.
func (w *TranscriptWriter) Append(turn Turn) error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	entry := TranscriptEntry{
		Kind: "entry",
		Seq:  w.seq,
		Turn: turn,
	}
	w.seq++

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return err
	}

	return w.file.Sync()
}

// Close syncs and closes the underlying file. Idempotent: safe to call multiple times.
// No-op if the receiver is nil.
func (w *TranscriptWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}

	var closeErr error
	w.closeOnce.Do(func() {
		if err := w.file.Sync(); err != nil {
			closeErr = err
		}
		if err := w.file.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

// OpenTranscriptWriter opens an existing transcript file for appending.
// Counts valid entries to determine the next seq number.
// Truncates any partial last line for crash recovery.
func OpenTranscriptWriter(path string) (*TranscriptWriter, error) {
	// Read existing entries to determine seq count.
	_, entries, err := ReadTranscript(path)
	if err != nil {
		return nil, fmt.Errorf("reading transcript for resume: %w", err)
	}

	// Truncate any trailing partial line: if the file doesn't end with '\n',
	// find the last newline and truncate there.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if raw[len(raw)-1] != '\n' {
			// Find last newline and truncate after it.
			lastNL := -1
			for i := len(raw) - 1; i >= 0; i-- {
				if raw[i] == '\n' {
					lastNL = i
					break
				}
			}
			if lastNL >= 0 {
				if err := os.Truncate(path, int64(lastNL+1)); err != nil {
					return nil, fmt.Errorf("truncating partial line: %w", err)
				}
			}
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	return &TranscriptWriter{file: f, seq: len(entries)}, nil
}

// ReadTranscript reads a transcript JSONL file, returning the header and all valid entries.
// Partial or corrupt lines are silently skipped (crash recovery).
func ReadTranscript(path string) (TranscriptHeader, []TranscriptEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptHeader{}, nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// First line must be the header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return TranscriptHeader{}, nil, fmt.Errorf("reading transcript header: %w", err)
		}
		return TranscriptHeader{}, nil, fmt.Errorf("transcript file is empty: no header")
	}

	var header TranscriptHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return TranscriptHeader{}, nil, fmt.Errorf("parsing transcript header: %w", err)
	}

	// Remaining lines are entries. Skip any that fail to parse.
	var entries []TranscriptEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry TranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip corrupt/partial lines (crash recovery).
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return TranscriptHeader{}, nil, fmt.Errorf("reading transcript: %w", err)
	}

	return header, entries, nil
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
