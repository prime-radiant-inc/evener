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
	"primeradiant.com/serf/identifier"
)

var marshalSessionMeta = json.Marshal

// sessionMetaFS is the filesystem session-meta persistence uses when a caller
// does not supply one. afero.OsFs delegates straight to os, so the exported
// non-FS entry points behave exactly as direct os calls; tests and fuzzers pass
// their own afero.Fs to the WithFS variants instead of swapping this out, which
// keeps them safe to run in parallel.
var sessionMetaFS = afero.NewOsFs()

// ErrInvalidSessionID reports a session ID that session-meta persistence
// refuses, because it is not safe to use as a filename component.
var ErrInvalidSessionID = errors.New("invalid session id")

// sessionIDMaxLen bounds an ID so that the longest name derived from it,
// <id>.meta.json.tmp, stays well inside the 255-byte filename limit.
const sessionIDMaxLen = 128

// validateSessionID accepts a session ID only when it is safe to use directly as
// a filename component: 1 to 128 bytes, each an ASCII letter, an ASCII digit,
// '-' or '_'. Everything else is refused, which is what makes the ID safe to
// join into a path and safe to key the write lock on:
//
//   - No '/', '\' or ':', so an ID cannot name a file in another directory.
//   - No '.', so no ID is "." or ".." or carries a traversal segment, and none
//     can impersonate the ".meta.json" or ".tmp" suffixes the layout uses.
//   - No spaces, control bytes or NUL, so an ID cannot be an unprintable
//     near-duplicate of another.
//   - ASCII only, so case folding is exactly ASCII case folding. Two distinct
//     IDs can then only alias onto one file through case, which
//     sessionMetaWriteLock's strings.ToLower already accounts for — whereas
//     Unicode folding and NFC/NFD normalization vary by filesystem and are
//     beyond what any in-process canonicalization can predict.
//
// The rule is deliberately wider than identifier.ValidateSessionID's minted
// base62 shape: this is a safety boundary, not an authenticity check, and test
// fixtures across the repo legitimately persist terse IDs like "WORKER".
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session id is empty: %w", ErrInvalidSessionID)
	}
	if len(id) > sessionIDMaxLen {
		return fmt.Errorf("session id is %d bytes, limit is %d: %w", len(id), sessionIDMaxLen, ErrInvalidSessionID)
	}
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return fmt.Errorf("session id %q has a disallowed byte %q at offset %d: %w", id, string(rune(c)), i, ErrInvalidSessionID)
		}
	}
	return nil
}

// sessionMetaWriteStripes is the number of locks session-meta writes are spread
// across. A fixed array rather than a per-session-ID map so a long-running
// daemon's lock table cannot grow with the number of sessions it has ever
// written; two session IDs that happen to share a stripe merely serialize.
const sessionMetaWriteStripes = 256

var sessionMetaWriteMus [sessionMetaWriteStripes]sync.Mutex

// sessionMetaWriteLock returns the lock guarding meta writes for one session.
//
// A session-meta write is a read-modify-write of that session's persisted
// ObservedBy set (load, union, store) followed by a non-atomic temp-file write
// and rename — and every path involved, target and temp alike, is derived from
// the session ID. So the exclusion the lock owes is per session: two writers for
// one ID would otherwise lose an observer to a stale read, or have one's rename
// fail on a temp file the other already renamed away. Writers for different IDs
// touch disjoint files and share only the sessions directory, whose MkdirAll is
// safe to race.
//
// The shard key is the session ID, deliberately not the resolved target path:
// two callers can spell the same state directory differently and must still
// land on the same lock. The ID is canonicalized first, because a lock may only
// ever be coarser than the file it guards and two unequal IDs can still name
// one file — a traversal segment collapses when the path is joined, and a
// case-insensitive filesystem folds case. Nothing validates meta.ID on the way
// in, so this side does not assume it is well-formed.
func sessionMetaWriteLock(sessionID string) *sync.Mutex {
	key := strings.ToLower(filepath.Clean(sessionID))
	// FNV-1a, inlined rather than via hash/fnv's interface-returning
	// constructor, which would escape to the heap on every write.
	const (
		fnvOffset32 = 2166136261
		fnvPrime32  = 16777619
	)
	hash := uint32(fnvOffset32)
	for i := 0; i < len(key); i++ {
		hash ^= uint32(key[i])
		hash *= fnvPrime32
	}
	return &sessionMetaWriteMus[hash%sessionMetaWriteStripes]
}

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
	return SaveSessionMetaWithFS(sessionMetaFS, dir, meta)
}

// SaveSessionMetaWithFS writes a SessionMeta through fs using the same atomic
// temp-file and rename sequence as SaveSessionMeta: mkdir plus a temp write and
// rename, all through the injected filesystem. Tests and fuzzers inject an
// in-memory or sandboxed filesystem to exercise persistence without touching
// real disk.
func SaveSessionMetaWithFS(fs afero.Fs, dir string, meta SessionMeta) error {
	if err := validateSessionID(meta.ID); err != nil {
		return err
	}
	lock := sessionMetaWriteLock(meta.ID)
	lock.Lock()
	defer lock.Unlock()
	return saveSessionMetaLocked(fs, dir, meta)
}

// AppendSessionObservedBy records a deduplicated observer UI relationship
// without changing other persisted session metadata.
func AppendSessionObservedBy(dir, workerSessionID, observerSessionID string) error {
	return appendSessionObservedByWithFS(sessionMetaFS, dir, workerSessionID, observerSessionID)
}

func appendSessionObservedByWithFS(fs afero.Fs, dir, workerSessionID, observerSessionID string) error {
	if err := validateSessionID(workerSessionID); err != nil {
		return err
	}
	lock := sessionMetaWriteLock(workerSessionID)
	lock.Lock()
	defer lock.Unlock()
	meta, err := loadSessionMetaFS(fs, dir, workerSessionID)
	if err != nil {
		return err
	}
	meta.ObservedBy = stableUnion(meta.ObservedBy, []string{observerSessionID})
	return saveSessionMetaLocked(fs, dir, meta)
}

// LoadSessionMeta reads a SessionMeta from <dir>/sessions/<id>.meta.json.
func LoadSessionMeta(dir, id string) (SessionMeta, error) {
	return LoadSessionMetaWithFS(sessionMetaFS, dir, id)
}

// LoadSessionMetaWithFS reads a SessionMeta through fs using the same path and
// decoding behavior as LoadSessionMeta.
func LoadSessionMetaWithFS(fs afero.Fs, dir, id string) (SessionMeta, error) {
	return loadSessionMetaFS(fs, dir, id)
}

// ListSessionMetas returns all valid session metas sorted by UpdatedAt descending.
// Scans for .meta.json files. Corrupt files are silently skipped.
func ListSessionMetas(dir string) ([]SessionMeta, error) {
	return listSessionMetasFS(sessionMetaFS, dir)
}

// saveSessionMetaLocked performs the write with the session's write lock already
// held. Callers must not re-enter any of the session-meta write entry points
// from within it: re-entering for the same session self-deadlocks, and now that
// the lock is striped, re-entering for a different session self-deadlocks only
// on a stripe collision — a hang that would be rare enough to be untraceable.
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
	if err := validateSessionID(id); err != nil {
		return SessionMeta{}, err
	}
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
