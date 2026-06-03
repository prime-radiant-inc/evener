package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"primeradiant.com/serf/agent/schema"
)

// SessionSnapshot holds the serializable state of a Session for persistence and resume.
//
// Deprecated: Use schema.SessionMeta + transcript JSONL for persistence.
// SessionSnapshot is retained for backward compatibility with external tools
// (transcript_tools.go, serfeval) that read snapshot files directly.
type SessionSnapshot struct {
	ID        string                 `json:"id"`         // session identifier
	ProfileID string                 `json:"profile_id"` // ID of the provider profile in use
	Model     string                 `json:"model"`      // model name the session is driving
	Config    schema.ConfigSnapshot  `json:"config"`     // the session's configuration
	EnvInfo   schema.EnvironmentInfo `json:"env_info"`   // captured environment description
	History   []schema.Turn          `json:"history"`    // full conversation transcript
	CreatedAt time.Time              `json:"created_at"` // when the session was first created
	UpdatedAt time.Time              `json:"updated_at"` // last time the snapshot was written
	TurnCount int                    `json:"turn_count"` // number of user-input turns processed
	// LastInputTokens is the prompt-token count reported by the provider on the
	// most recent LLM call; used to display context-window pressure on resume.
	LastInputTokens int `json:"last_input_tokens,omitempty"`
}

// SaveSession writes a snapshot to <dir>/sessions/<id>.json using atomic rename.
//
// Deprecated: Use schema.SaveSessionMeta for lightweight persistence. SaveSession
// is retained for backward compatibility with external tools.
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
