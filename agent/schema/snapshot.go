package schema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// SessionMeta holds session metadata without the full conversation history.
// The history is always recovered from the transcript JSONL file.
type SessionMeta struct {
	ID        string `json:"id"`         // session identifier
	ProfileID string `json:"profile_id"` // ID of the provider profile in use
	Model     string `json:"model"`      // model name the session is driving
	// CheapModel is the configured cheap/fast model for side calls, as a
	// WithCheapModel ref ("provider/model" when cross-provider, else bare model).
	// Empty when none is configured. Persisted so the cheap routing survives
	// resume — launch args alone do not carry it across restart.
	CheapModel string          `json:"cheap_model,omitempty"`
	Config     ConfigSnapshot  `json:"config"`     // the session's configuration
	EnvInfo    EnvironmentInfo `json:"env_info"`   // captured environment description
	CreatedAt  time.Time       `json:"created_at"` // when the session was first created
	UpdatedAt  time.Time       `json:"updated_at"` // last time the meta was written
	TurnCount  int             `json:"turn_count"` // number of user-input turns processed
	// LastInputTokens is the prompt-token count from the most recent LLM call,
	// used to display context-window pressure on resume.
	LastInputTokens int `json:"last_input_tokens,omitempty"`
	// Name is the generated human-readable session title, if one has been
	// produced (see SessionDisplayName).
	Name string `json:"name,omitempty"`
	// NameSource records how Name was derived: "prompt" (from the first user
	// input) or "compaction" (from a context-compaction summary).
	NameSource string `json:"name_source,omitempty"`
	// NameUpdatedAt is when Name was last (re)generated.
	NameUpdatedAt time.Time `json:"name_updated_at,omitzero"`
	// OriginalPrompt is the first user input, kept as the fallback display name
	// for sessions written before naming existed.
	OriginalPrompt string `json:"original_prompt,omitempty"`
	// ParentSessionID, DivergenceTurn, and ForkLabel are non-empty on sessions
	// that branched from another via the fork operation. ParentSessionID names
	// the original session (the one whose transcript prefix this session shares);
	// DivergenceTurn is the turn index immediately after the shared prefix
	// (the first turn unique to this branch). ForkLabel, if set, is the
	// user-supplied display name for the original branch.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	DivergenceTurn  int    `json:"divergence_turn,omitempty"`
	ForkLabel       string `json:"fork_label,omitempty"`
	// IsSubagent is true on sessions spawned as a subagent (i.e. the session
	// has a parent spawn). Written by the spawn path at session initialisation.
	IsSubagent bool `json:"is_subagent,omitempty"`
	// Goal holds the persisted goal state so the objective survives daemon
	// restart and serf resume. It is nil when no goal is active or has been set.
	Goal *GoalSnapshot `json:"goal,omitempty"`
	// PinnedNote is the agent's self-compaction note_to_self, persisted so it
	// survives daemon restart and serf resume (mirrors Goal).
	PinnedNote string `json:"pinned_note,omitempty"`
	// ObservedBy is the set of session ids of observer subagents watching this
	// session's work. It is stamped onto the watched worker's meta when a parent
	// session's job_watch mints a read grant for an observer delegate (the only
	// place that knows the observer↔worker pair). The hub reads it to auto-open
	// a live observer's session beside this one. Append-only and deduped, like
	// the watch read grants it mirrors; never revoked.
	ObservedBy []string `json:"observed_by,omitempty"`
	// WorktreePath is the absolute path of the managed or path-entered
	// worktree the session's env is currently rooted in via manage_worktree,
	// empty when the session is at its main/restore root. Both switch modes —
	// managed and non-managed by-path — swap the env, so both must be
	// persisted here (native worktree tools spec §7 "Persistence and
	// resume": "managed or path-entered; both switch modes swap the env, so
	// both must survive resume").
	WorktreePath string `json:"worktree_path,omitempty"`
	// WorktreeManaged is true when WorktreePath is a serf-managed worktree
	// (entered via create, or switch by name/managed path) — the idempotent
	// occupancy-lock rule applies to it on resume re-entry. False for a
	// non-managed worktree entered by path, which carries no serf lock.
	WorktreeManaged bool `json:"worktree_managed,omitempty"`
	// WorktreeRestoreRoot is the root of the env saved the first time the
	// session entered WorktreePath (native worktree tools spec §7
	// "env-restore model"). On resume, a foreign lock or a worktree that no
	// longer exists lands the session here instead, with a notice.
	WorktreeRestoreRoot string `json:"worktree_restore_root,omitempty"`
}

// GoalSnapshot is the wire form of a goal.Goal persisted inside SessionMeta.
// It captures full fidelity (including madeProgressOnce and timestamps) so a
// restored session can continue exactly where it left off. The "reported" flag
// is intentionally omitted — it is runtime-only and always starts false on load.
type GoalSnapshot struct {
	Objective        string    `json:"objective"`
	Status           string    `json:"status"`
	Iterations       int       `json:"iterations,omitempty"`
	NoProgressStreak int       `json:"no_progress_streak,omitempty"`
	MadeProgressOnce bool      `json:"made_progress_once,omitempty"`
	StopReason       string    `json:"stop_reason,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitzero"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// UnmarshalJSON decodes a SessionMeta from JSON, falling back to the legacy
// "original_task" field for OriginalPrompt when the current "original_prompt"
// field is empty or absent.
func (m *SessionMeta) UnmarshalJSON(data []byte) error {
	type sessionMetaAlias SessionMeta
	var aux struct {
		sessionMetaAlias
		LegacyOriginalPrompt string `json:"original_task,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*m = SessionMeta(aux.sessionMetaAlias)
	if m.OriginalPrompt == "" {
		m.OriginalPrompt = aux.LegacyOriginalPrompt
	}
	return nil
}

// SessionDisplayName returns the best available human-readable title for a
// session. Generated names are preferred, with OriginalPrompt retained as the
// backward-compatible fallback for sessions written before naming existed.
func SessionDisplayName(meta SessionMeta) string {
	if name := strings.TrimSpace(meta.Name); name != "" {
		return name
	}
	if prompt := strings.TrimSpace(meta.OriginalPrompt); prompt != "" {
		return prompt
	}
	return strings.TrimSpace(meta.ID)
}

const sessionsSubdir = "sessions"

// SaveSessionMeta writes a SessionMeta to <dir>/sessions/<id>.meta.json using
// atomic rename and compact JSON (no indentation).
func SaveSessionMeta(dir string, meta SessionMeta) error {
	return saveSessionMetaFS(afero.NewOsFs(), dir, meta)
}

// LoadSessionMeta reads a SessionMeta from <dir>/sessions/<id>.meta.json.
func LoadSessionMeta(dir, id string) (SessionMeta, error) {
	return loadSessionMetaFS(afero.NewOsFs(), dir, id)
}

// ListSessionMetas returns all valid session metas sorted by UpdatedAt descending.
// Scans for .meta.json files. Corrupt files are silently skipped.
func ListSessionMetas(dir string) ([]SessionMeta, error) {
	return listSessionMetasFS(afero.NewOsFs(), dir)
}

// saveSessionMetaFS is the filesystem seam beneath SaveSessionMeta: it performs
// the mkdir + atomic temp+rename write through an injected afero.Fs. Production
// passes afero.NewOsFs(), whose methods delegate directly to os, so behavior is
// byte-identical to using os calls; tests and fuzzers inject an in-memory or
// sandboxed filesystem to exercise persistence without touching real disk.
func saveSessionMetaFS(fs afero.Fs, dir string, meta SessionMeta) error {
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := fs.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}

	target := filepath.Join(sessDir, meta.ID+".meta.json")
	tmp := target + ".tmp"

	if err := afero.WriteFile(fs, tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := fs.Rename(tmp, target); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("rename temp to target: %w", err)
	}
	return nil
}

// loadSessionMetaFS is the filesystem seam beneath LoadSessionMeta.
func loadSessionMetaFS(fs afero.Fs, dir, id string) (SessionMeta, error) {
	path := filepath.Join(dir, sessionsSubdir, id+".meta.json")
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("read session meta %s: %w", id, err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, fmt.Errorf("unmarshal session meta %s: %w", id, err)
	}
	return meta, nil
}

// listSessionMetasFS is the filesystem seam beneath ListSessionMetas.
func listSessionMetasFS(fs afero.Fs, dir string) ([]SessionMeta, error) {
	sessDir := filepath.Join(dir, sessionsSubdir)
	entries, err := afero.ReadDir(fs, sessDir)
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
		meta, err := loadSessionMetaFS(fs, dir, id)
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
