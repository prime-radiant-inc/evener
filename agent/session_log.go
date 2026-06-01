package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	mu      sync.RWMutex
	entries []SessionLogEntry
}

// NewSessionLog creates a new SessionLog that persists to the given path.
// If the file exists, loads existing entries. Returns an error if an
// existing log file cannot be read.
func NewSessionLog(path string) (*SessionLog, error) {
	log := &SessionLog{
		path:    path,
		entries: []SessionLogEntry{},
	}

	if _, err := os.Stat(path); err == nil {
		if loadErr := log.loadFromDisk(); loadErr != nil {
			return nil, fmt.Errorf("load session log: %w", loadErr)
		}
	}

	return log, nil
}

// loadFromDisk reads entries from the log file.
func (l *SessionLog) loadFromDisk() error {
	f, err := os.Open(l.path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
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

	return scanner.Err()
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
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = f.Write(append(data, '\n'))
	return err
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
		sb.WriteString(fmt.Sprintf("Turn %d [%s] %s: %s",
			entry.Turn,
			entry.Action,
			entry.Outcome,
			entry.Summary))
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
