package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionSnapshot holds the serializable state of a Session for persistence and resume.
type SessionSnapshot struct {
	ID        string         `json:"id"`
	ProfileID string         `json:"profile_id"`
	Model     string         `json:"model"`
	Config    SessionConfig  `json:"config"`
	EnvInfo   EnvironmentInfo `json:"env_info"`
	History   []Turn         `json:"history"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	TurnCount int            `json:"turn_count"`
}

const sessionsSubdir = "sessions"

// SaveSession writes a snapshot to <dir>/sessions/<id>.json using atomic rename.
func SaveSession(dir string, snap SessionSnapshot) error {
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	target := filepath.Join(sessDir, snap.ID+".json")
	tmp := target + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp to target: %w", err)
	}
	return nil
}

// LoadSession reads a snapshot from <dir>/sessions/<id>.json.
func LoadSession(dir, id string) (SessionSnapshot, error) {
	path := filepath.Join(dir, sessionsSubdir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("read session %s: %w", id, err)
	}
	var snap SessionSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return SessionSnapshot{}, fmt.Errorf("unmarshal session %s: %w", id, err)
	}
	return snap, nil
}

// ListSessions returns all valid snapshots sorted by UpdatedAt descending (most recent first).
// Corrupt files are silently skipped.
func ListSessions(dir string) ([]SessionSnapshot, error) {
	sessDir := filepath.Join(dir, sessionsSubdir)
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var snaps []SessionSnapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		snap, err := LoadSession(dir, id)
		if err != nil {
			continue // skip corrupt files
		}
		snaps = append(snaps, snap)
	}

	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].UpdatedAt.After(snaps[j].UpdatedAt)
	})

	return snaps, nil
}
