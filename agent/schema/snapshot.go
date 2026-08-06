package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/internal/envctx"
	"primeradiant.com/serf/identifier"
)

var marshalSessionMeta = json.Marshal

var sessionMetaWriteMu sync.Mutex

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
	CheapModel               string          `json:"cheap_model,omitempty"`
	Config                   ConfigSnapshot  `json:"config"`     // the session's configuration
	EnvInfo                  EnvironmentInfo `json:"env_info"`   // captured environment description
	CreatedAt                time.Time       `json:"created_at"` // when the session was first created
	UpdatedAt                time.Time       `json:"updated_at"` // last time the meta was written
	TurnCount                int             `json:"turn_count"` // number of model responses processed
	AcceptedInputTurns       int             `json:"accepted_input_turns,omitempty"`
	TurnBudgetWarningEmitted bool            `json:"turn_budget_warning_emitted,omitempty"`
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
	// Origin marks how the session was launched: "test" for agentic-testing
	// runs (set via SERF_SESSION_ORIGIN), empty for normal sessions. The hub
	// classifies an all-"test" project into the "Test runs" group.
	Origin string `json:"origin,omitempty"`
	// Goal holds the persisted goal state so the objective survives daemon
	// restart and serf resume. It is nil when no goal is active or has been set.
	Goal *GoalSnapshot `json:"goal,omitempty"`
	// PinnedNote is the agent's self-compaction note_to_self, persisted so it
	// survives daemon restart and serf resume (mirrors Goal).
	PinnedNote string `json:"pinned_note,omitempty"`
	// EnvContext is the environment-context tracker state (last emitted
	// snapshot), persisted so resume stays silent when nothing changed.
	EnvContext *envctx.State `json:"env_context,omitempty"`
	// ObservedBy records append-only observer UI relationships. It grants no
	// access and lets the hub auto-open an observer beside this worker.
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
	// CumulativeUsage carries the session's running self-only token totals so
	// they survive restart/resume. omitzero: legacy metas without it round-trip
	// unchanged (WS2 working-state-metrics).
	CumulativeUsage CumulativeUsage `json:"cumulative_usage,omitzero"`
	// WorkMillis is the accumulated wall-clock work time (sum of every turn's
	// duration, interrupted and failed included), persisted so the total
	// survives restart/resume. omitzero for legacy round-trip.
	WorkMillis int64 `json:"work_millis,omitzero"`
	// JobTreeRootSessionID identifies the root session whose shared job/activity
	// lifecycle revision this session participates in. For standalone/root
	// sessions it is the session's own ID; descendants persist the inherited root
	// ID so durable activity-tree responses can preserve the same root-scoped
	// revision envelope after exit.
	JobTreeRootSessionID string `json:"job_tree_root_session_id,omitempty"`
	// JobTreeRevision is the latest authoritative root-scoped job/activity tree
	// revision observed by this session's shared lifecycle clock. Persisted so
	// exited activity-tree responses can report the same authoritative revision
	// without inventing a synthetic counter from durable job records.
	JobTreeRevision uint64 `json:"job_tree_revision,omitzero"`
}

// CumulativeUsage is a deliberately lossy snapshot of an llm.Usage kept in
// SessionMeta so per-session token totals survive daemon restart and resume.
// Conversion from llm.Usage drops Raw and the reasoning/cache-write pointers;
// nil pointers map to 0. Tagged omitzero so legacy metas round-trip untouched.
type CumulativeUsage struct {
	InputTokens     int64 `json:"input_tokens,omitzero"`
	OutputTokens    int64 `json:"output_tokens,omitzero"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitzero"`
	TotalTokens     int64 `json:"total_tokens,omitzero"`
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
	return SaveSessionMetaWithFS(afero.NewOsFs(), dir, meta)
}

// SaveSessionMetaWithFS writes a SessionMeta through fs using the same atomic
// temp-file and rename sequence as SaveSessionMeta.
func SaveSessionMetaWithFS(fs afero.Fs, dir string, meta SessionMeta) error {
	sessionMetaWriteMu.Lock()
	defer sessionMetaWriteMu.Unlock()
	return saveSessionMetaLocked(fs, dir, meta)
}

// AppendSessionObservedBy records a deduplicated observer UI relationship
// without changing other persisted session metadata.
func AppendSessionObservedBy(dir, workerSessionID, observerSessionID string) error {
	return appendSessionObservedByWithFS(afero.NewOsFs(), dir, workerSessionID, observerSessionID)
}

func appendSessionObservedByWithFS(fs afero.Fs, dir, workerSessionID, observerSessionID string) error {
	sessionMetaWriteMu.Lock()
	defer sessionMetaWriteMu.Unlock()
	meta, err := loadSessionMetaFS(fs, dir, workerSessionID)
	if err != nil {
		return err
	}
	meta.ObservedBy = stableUnion(meta.ObservedBy, []string{observerSessionID})
	return saveSessionMetaLocked(fs, dir, meta)
}

// LoadSessionMeta reads a SessionMeta from <dir>/sessions/<id>.meta.json.
func LoadSessionMeta(dir, id string) (SessionMeta, error) {
	return LoadSessionMetaWithFS(afero.NewOsFs(), dir, id)
}

// LoadSessionMetaWithFS reads a SessionMeta through fs using the same path and
// decoding behavior as LoadSessionMeta.
func LoadSessionMetaWithFS(fs afero.Fs, dir, id string) (SessionMeta, error) {
	return loadSessionMetaFS(fs, dir, id)
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
	sessionMetaWriteMu.Lock()
	defer sessionMetaWriteMu.Unlock()
	return saveSessionMetaLocked(fs, dir, meta)
}

func saveSessionMetaLocked(fs afero.Fs, dir string, meta SessionMeta) error {
	previous, err := loadSessionMetaFS(fs, dir, meta.ID)
	if err == nil {
		meta.ObservedBy = stableUnion(previous.ObservedBy, meta.ObservedBy)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := fs.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := marshalSessionMeta(meta)
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

func stableUnion(existing, added []string) []string {
	seen := make(map[string]bool, len(existing)+len(added))
	out := make([]string, 0, len(existing)+len(added))
	for _, values := range [][]string{existing, added} {
		for _, value := range values {
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
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
	if meta.ID != id {
		return SessionMeta{}, fmt.Errorf("session meta ID %q does not match requested session ID %q", meta.ID, id)
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
		if identifier.ValidateSessionID(id) != nil {
			continue
		}
		meta, err := loadSessionMetaFS(fs, dir, id)
		if err != nil || identifier.ValidateSessionID(meta.ID) != nil || meta.ID != id {
			continue // skip corrupt files
		}
		metas = append(metas, meta)
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})

	return metas, nil
}
