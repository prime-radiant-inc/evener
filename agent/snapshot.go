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
//
// Deprecated: Use SessionMeta + transcript JSONL for persistence. SessionSnapshot
// is retained for backward compatibility with external tools (transcript_tools.go,
// serfeval) that read snapshot files directly.
type SessionSnapshot struct {
	ID              string          `json:"id"`
	ProfileID       string          `json:"profile_id"`
	Model           string          `json:"model"`
	Config          SessionConfig   `json:"config"`
	EnvInfo         EnvironmentInfo `json:"env_info"`
	History         []Turn          `json:"history"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	TurnCount       int             `json:"turn_count"`
	LastInputTokens int             `json:"last_input_tokens,omitempty"`
}

// SessionMeta holds session metadata without the full conversation history.
// The history is always recovered from the transcript JSONL file.
type SessionMeta struct {
	ID              string          `json:"id"`
	ProfileID       string          `json:"profile_id"`
	Model           string          `json:"model"`
	Config          SessionConfig   `json:"config"`
	EnvInfo         EnvironmentInfo `json:"env_info"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	TurnCount       int             `json:"turn_count"`
	LastInputTokens int             `json:"last_input_tokens,omitempty"`
	OriginalTask    string          `json:"original_task,omitempty"`
	// ParentSessionID, DivergenceTurn, and ForkLabel are non-empty on sessions
	// that branched from another via the fork operation. ParentSessionID names
	// the original session (the one whose transcript prefix this session shares);
	// DivergenceTurn is the turn index immediately after the shared prefix
	// (the first turn unique to this branch). ForkLabel, if set, is the
	// user-supplied display name for the original branch.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	DivergenceTurn  int    `json:"divergence_turn,omitempty"`
	ForkLabel       string `json:"fork_label,omitempty"`
	// IsSubagent is true on sessions spawned via spawn_agent.
	// NOTE: The agent's spawn-subagent code does not yet set this field;
	// wiring the write path is a follow-up task.
	IsSubagent bool `json:"is_subagent,omitempty"`
}

const sessionsSubdir = "sessions"

// SaveSessionMeta writes a SessionMeta to <dir>/sessions/<id>.meta.json using
// atomic rename and compact JSON (no indentation).
func SaveSessionMeta(dir string, meta SessionMeta) error {
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}

	target := filepath.Join(sessDir, meta.ID+".meta.json")
	tmp := target + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp to target: %w", err)
	}
	return nil
}

// LoadSessionMeta reads a SessionMeta from <dir>/sessions/<id>.meta.json.
func LoadSessionMeta(dir, id string) (SessionMeta, error) {
	path := filepath.Join(dir, sessionsSubdir, id+".meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("read session meta %s: %w", id, err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, fmt.Errorf("unmarshal session meta %s: %w", id, err)
	}
	return meta, nil
}

// ListSessionMetas returns all valid session metas sorted by UpdatedAt descending.
// Scans for .meta.json files. Corrupt files are silently skipped.
func ListSessionMetas(dir string) ([]SessionMeta, error) {
	sessDir := filepath.Join(dir, sessionsSubdir)
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var metas []SessionMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".meta.json")
		meta, err := LoadSessionMeta(dir, id)
		if err != nil {
			continue // skip corrupt files
		}
		metas = append(metas, meta)
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})

	return metas, nil
}

// SaveSession writes a snapshot to <dir>/sessions/<id>.json using atomic rename.
//
// Deprecated: Use SaveSessionMeta for lightweight persistence. SaveSession is
// retained for backward compatibility with external tools.
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
		_ = os.Remove(tmp)
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
