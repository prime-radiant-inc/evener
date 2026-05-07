// Package rendezvous defines the on-disk protocol that lets the serf-hub
// orchestrator discover live serf serve daemons on the local host.
//
// Each daemon writes a small JSON file at <dir>/<pid>.json on startup and
// removes it on graceful shutdown. The hub watches the directory.
package rendezvous

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Entry describes one live serf serve daemon.
//
// SessionID is intentionally absent: it can change under POST /clear, so the
// hub fetches the current value from the daemon's /status on demand.
type Entry struct {
	PID        int       `json:"pid"`
	Address    string    `json:"address"`
	WorkingDir string    `json:"working_dir,omitempty"`
	StateDir   string    `json:"state_dir,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Model      string    `json:"model,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	SpawnedBy  string    `json:"spawned_by,omitempty"`
}

// DefaultDir returns the canonical rendezvous directory ($HOME/.serf/run).
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".serf", "run")
}

// Write creates dir if necessary and writes <dir>/<pid>.json atomically.
// Returns the absolute path that was written.
func Write(dir string, entry Entry) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create rendezvous dir: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}
	target := filepath.Join(dir, fmt.Sprintf("%d.json", entry.PID))
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return target, nil
}

// Remove deletes <dir>/<pid>.json. A missing file is not an error.
func Remove(dir string, pid int) error {
	target := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rendezvous file: %w", err)
	}
	return nil
}

// List returns every parseable Entry in dir. Corrupt files are skipped.
// A missing directory returns (nil, nil).
func List(dir string) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read rendezvous dir: %w", err)
	}
	var out []Entry
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(de.Name(), ".json")
		if _, err := strconv.Atoi(base); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
