// Package sessionlog provides a structured, append-only log of session actions.
// It is the shared substrate beneath the session-namer (which records advisory
// entries) and the session-log family of context strategies (which build
// compaction checkpoints from logged actions). It depends only on the standard
// library so both consumers can share it without a package cycle.
package sessionlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/afero"
)

// SessionLogEntry is a structured summary of one action.
type SessionLogEntry struct {
	Kind         string   `json:"kind,omitempty"`          // optional category tag (e.g. "advisory")
	Turn         int      `json:"turn"`                    // turn index the action occurred on
	Action       string   `json:"action"`                  // tool name or "assistant"
	Summary      string   `json:"summary"`                 // one-line description of the action
	Outcome      string   `json:"outcome"`                 // "success" or "failure"
	FilesTouched []string `json:"files_touched,omitempty"` // paths created or modified
	Failures     []string `json:"failures,omitempty"`      // failure messages, when Outcome is "failure"
}

// SessionLog manages a structured, append-only log of session actions.
type SessionLog struct {
	path    string
	fs      afero.Fs
	marshal func(any) ([]byte, error)
	mu      sync.RWMutex
	entries []SessionLogEntry
	// tornTail records that the file ended in an unterminated final line when
	// it was loaded — the residue of an interrupted append. The bytes are left
	// on disk (malformed lines are tolerated), but the next append must first
	// terminate that line so the new record is not concatenated onto it and
	// silently lost by the following load.
	tornTail bool
}

// NewSessionLog creates a new SessionLog that persists to the given path.
// If the file exists, loads existing entries. Returns an error if an
// existing log file cannot be read.
func NewSessionLog(path string) (*SessionLog, error) {
	return newSessionLogFS(path, afero.NewOsFs())
}

// newSessionLogFS is the construction seam beneath NewSessionLog: it builds a
// SessionLog over an injected afero.Fs. Production passes afero.NewOsFs(), whose
// methods delegate directly to os, so behavior is byte-identical to using os
// calls; tests and fuzzers inject an in-memory or sandboxed filesystem so
// persistence can be exercised without touching real disk.
func newSessionLogFS(path string, fs afero.Fs) (*SessionLog, error) {
	log := &SessionLog{
		path:    path,
		fs:      fs,
		marshal: json.Marshal,
		entries: []SessionLogEntry{},
	}

	if _, err := fs.Stat(path); err == nil {
		if loadErr := log.loadFromDisk(); loadErr != nil {
			return nil, fmt.Errorf("load session log: %w", loadErr)
		}
	}

	return log, nil
}

// loadFromDisk reads entries from the log file. Malformed lines are skipped to
// tolerate partial writes; an unterminated final line (a torn write) is
// recorded so the next append can separate itself from it.
func (l *SessionLog) loadFromDisk() error {
	f, err := l.fs.Open(l.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is immaterial

	tail := &tailReader{r: f}
	scanner := bufio.NewScanner(tail)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry SessionLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Malformed lines are skipped to tolerate partial writes
			// (e.g., crash mid-append). The scanner.Err() check below
			// catches true I/O errors.
			continue
		}
		l.entries = append(l.entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// A file that does not end in a newline ends in an unterminated final
	// line: an interrupted append whose bytes the scanner skipped as
	// malformed. Record it so the next append terminates that line first,
	// rather than concatenating onto it and losing the new record on reload.
	l.tornTail = tail.n > 0 && tail.last != '\n'
	return nil
}

// tailReader records the last byte it read, so loadFromDisk can tell whether
// the log ended in an unterminated line without a second pass over the file.
type tailReader struct {
	r    io.Reader
	last byte
	n    int
}

func (t *tailReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.last = p[n-1]
		t.n += n
	}
	return n, err
}

// Append appends an entry to the in-memory list and persists to disk.
func (l *SessionLog) Append(entry SessionLogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Append to in-memory list
	l.entries = append(l.entries, entry)

	// Persist to disk (append-only)
	return l.appendToDisk(entry)
}

// appendToDisk writes a single entry to the log file.
func (l *SessionLog) appendToDisk(entry SessionLogEntry) error {
	if err := l.fs.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	f, err := l.fs.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // best-effort observability log; the write error below is what matters

	data, err := l.marshal(entry)
	if err != nil {
		return err
	}

	record := make([]byte, 0, len(data)+2)
	if l.tornTail {
		// Terminate the torn tail so the new record starts on its own line.
		// The torn bytes stay on disk as a malformed line, which load tolerates
		// — consistent with the existing skip-malformed behavior.
		record = append(record, '\n')
	}
	record = append(record, data...)
	record = append(record, '\n')
	if _, err := f.Write(record); err != nil {
		return err
	}
	l.tornTail = false
	return nil
}

// Entries returns a copy of all entries.
func (l *SessionLog) Entries() []SessionLogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Return a copy to prevent external modification
	result := make([]SessionLogEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

// EntriesRange returns entries in [start, end) range with bounds clamping.
func (l *SessionLog) EntriesRange(start, end int) []SessionLogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Clamp to valid bounds
	if start < 0 {
		start = 0
	}
	if end > len(l.entries) {
		end = len(l.entries)
	}
	if start >= end {
		return []SessionLogEntry{}
	}

	// Return a copy
	result := make([]SessionLogEntry, end-start)
	copy(result, l.entries[start:end])
	return result
}

// String returns a human-readable rendering of the log for injection into
// context, excluding advisory entries.
func (l *SessionLog) String() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.entries) == 0 {
		return ""
	}

	var sb strings.Builder
	wrote := false
	for _, entry := range l.entries {
		if entry.Kind == "advisory" {
			continue
		}
		if wrote {
			sb.WriteString("\n")
		}
		// Format: "Turn 47 [edit_file] success: Modified auth middleware..."
		fmt.Fprintf(&sb, "Turn %d [%s] %s: %s",
			entry.Turn,
			entry.Action,
			entry.Outcome,
			entry.Summary)
		wrote = true
	}

	return sb.String()
}

// Len returns the number of entries.
func (l *SessionLog) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}
