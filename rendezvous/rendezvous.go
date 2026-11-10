// Package rendezvous defines the on-disk protocol that lets the evener-hub
// orchestrator discover live evener serve daemons on the local host.
//
// Each daemon writes a small JSON file at <dir>/<pid>.json on startup and
// removes it on graceful shutdown. The hub watches the directory.
package rendezvous

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/execsupport/fsatomic"
)

// Entry describes one live evener serve daemon.
type Entry struct {
	PID          int       `json:"pid"`
	Address      string    `json:"address"`
	Protocol     string    `json:"protocol,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	SourceID     string    `json:"source_id,omitempty"`
	ThreadID     string    `json:"thread_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	WorkspaceRef string    `json:"workspace_ref,omitempty"`
	InstanceID   string    `json:"instance_id,omitempty"`
	WorkingDir   string    `json:"working_dir,omitempty"`
	StateDir     string    `json:"state_dir,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Model        string    `json:"model,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	HubToken     string    `json:"hub_token,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	SpawnedBy    string    `json:"spawned_by,omitempty"`
}

// DefaultDir returns the canonical rendezvous directory,
// <state root>/run (see userdirs.StateRoot). It returns
// userdirs.ErrNoStateHome when no state root can be resolved.
func DefaultDir() (string, error) {
	root, err := userdirs.DefaultStateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "run"), nil
}

// Write creates dir if necessary and writes <dir>/<pid>.json atomically.
// Returns the absolute path that was written.
func Write(dir string, entry Entry) (string, error) {
	var path string
	err := withOwnershipLock(dir, entry.PID, func() error {
		var err error
		path, err = write(dir, entry)
		return err
	})
	return path, err
}

func write(dir string, entry Entry) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create rendezvous dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure rendezvous dir: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}
	target := filepath.Join(dir, fmt.Sprintf("%d.json", entry.PID))
	// The entry is runtime state the daemon republishes on every start, so a
	// directory sync that fails after the rename only costs crash durability
	// of a file nobody needs after a crash; treating it as a failed write
	// would leave a published entry the caller believes is absent.
	if err := fsatomic.Write(target, data, 0o600); err != nil && !errors.Is(err, fsatomic.ErrPublished) {
		return "", fmt.Errorf("write entry: %w", err)
	}
	return target, nil
}

// Remove deletes <dir>/<pid>.json. A missing file is not an error.
func Remove(dir string, pid int) error {
	return withOwnershipLock(dir, pid, func() error {
		return removeFS(afero.NewOsFs(), dir, pid)
	})
}

// removeFS is Remove against an injected afero.Fs.
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

// ListStrict requires every PID-named rendezvous entry to be readable and
// valid. Ownership checks must not interpret a skipped claim as a stopped daemon.
func ListStrict(dir string) ([]Entry, error) {
	return listFSMode(afero.NewOsFs(), dir, true)
}

// listFS is List against an injected afero.Fs.
func listFS(fs afero.Fs, dir string) ([]Entry, error) {
	return listFSMode(fs, dir, false)
}

func listFSMode(fs afero.Fs, dir string, strict bool) ([]Entry, error) {
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if os.IsNotExist(err) && !strict {
			return nil, nil
		}
		return nil, fmt.Errorf("read rendezvous dir: %w", err)
	}
	var out []Entry
	for _, de := range entries {
		if (!strict && de.IsDir()) || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(de.Name(), ".json")
		pid, err := strconv.Atoi(base)
		if err != nil {
			continue
		}
		data, err := afero.ReadFile(fs, filepath.Join(dir, de.Name()))
		if err != nil {
			if strict {
				return nil, fmt.Errorf("read rendezvous %s: %w", de.Name(), err)
			}
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			if strict {
				return nil, fmt.Errorf("decode rendezvous %s: %w", de.Name(), err)
			}
			continue
		}
		if strict && e.PID != pid {
			return nil, fmt.Errorf("rendezvous %s has invalid process identity", de.Name())
		}
		out = append(out, e)
	}
	return out, nil
}
