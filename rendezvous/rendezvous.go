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

	"github.com/spf13/afero"
)

// Entry describes one live serf serve daemon.
type Entry struct {
	PID        int       `json:"pid"`
	Address    string    `json:"address"`
	Protocol   string    `json:"protocol,omitempty"`
	Endpoint   string    `json:"endpoint,omitempty"`
	SourceID   string    `json:"source_id,omitempty"`
	ThreadID   string    `json:"thread_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	WorkingDir string    `json:"working_dir,omitempty"`
	StateDir   string    `json:"state_dir,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Model      string    `json:"model,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	HubToken   string    `json:"hub_token,omitempty"`
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
	return writeFS(afero.NewOsFs(), dir, entry)
}

// writeFS is Write against an injected afero.Fs. Production passes
// afero.NewOsFs(), whose methods delegate directly to the os package, so the
// on-disk behavior is byte-identical; tests and fuzzers inject an in-memory or
// sandboxed filesystem.
func writeFS(fs afero.Fs, dir string, entry Entry) (string, error) {
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create rendezvous dir: %w", err)
	}
	if err := fs.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure rendezvous dir: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}
	target := filepath.Join(dir, fmt.Sprintf("%d.json", entry.PID))
	tmp := target + ".tmp"
	if err := afero.WriteFile(fs, tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write tmp: %w", err)
	}
	if err := fs.Chmod(tmp, 0o600); err != nil {
		_ = fs.Remove(tmp)
		return "", fmt.Errorf("secure tmp: %w", err)
	}
	if err := fs.Rename(tmp, target); err != nil {
		_ = fs.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return target, nil
}

// Remove deletes <dir>/<pid>.json. A missing file is not an error.
func Remove(dir string, pid int) error {
	return removeFS(afero.NewOsFs(), dir, pid)
}

// removeFS is Remove against an injected afero.Fs (see writeFS).
func removeFS(fs afero.Fs, dir string, pid int) error {
	target := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := fs.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rendezvous file: %w", err)
	}
	return nil
}

// List returns every parseable Entry in dir. Corrupt files are skipped.
// A missing directory returns (nil, nil).
func List(dir string) ([]Entry, error) {
	return listFS(afero.NewOsFs(), dir)
}

// listFS is List against an injected afero.Fs (see writeFS).
func listFS(fs afero.Fs, dir string) ([]Entry, error) {
	entries, err := afero.ReadDir(fs, dir)
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
		data, err := afero.ReadFile(fs, filepath.Join(dir, de.Name()))
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
