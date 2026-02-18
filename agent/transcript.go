package agent

import (
	"encoding/json"
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
