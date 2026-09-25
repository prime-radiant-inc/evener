package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/identifier"
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

// windowsReservedSessionIDs are the DOS device names Windows resolves no matter
// what extension follows them: opening "NUL.meta.json" reaches the NUL device,
// not a file. evener builds for Windows (agent/internal/installid/
// lock_windows.go), and a state directory written on one platform can be read
// on another, so these are refused everywhere rather than under a GOOS guard.
// Dots are already banned, so the whole ID is the basename to compare.
var windowsReservedSessionIDs = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidateSessionID accepts a session ID only when it is safe to use directly as
// a filename component: 1 to 128 bytes, each an ASCII letter, an ASCII digit,
// '-' or '_'. Everything else is refused, which is what makes the ID safe to
// join into a path and safe to key the write lock on:
//
//   - No '/', '\' or ':', so an ID cannot name a file in another directory.
//   - No '.', so no ID is "." or ".." or carries a traversal segment, and none
//     can impersonate the ".meta.json" or ".tmp" suffixes the layout uses.
//   - No spaces, control bytes or NUL, so an ID cannot be an unprintable
//     near-duplicate of another.
//   - No Windows reserved device name, which no extension makes safe.
//   - ASCII only, so case folding is exactly ASCII case folding. Two distinct
//     IDs can then only alias onto one file through case, which
//     sessionMetaWriteLock's strings.ToLower already accounts for — whereas
//     Unicode folding and NFC/NFD normalization vary by filesystem and are
//     beyond what any in-process canonicalization can predict.
//
// The rule is deliberately wider than identifier.ValidateSessionID's minted
// base62 shape: this is a safety boundary, not an authenticity check, and test
// fixtures across the repo legitimately persist terse IDs like "WORKER".
//
// A write is checked transitively anyway, since saveSessionMetaLocked loads the
// previous meta first and that load validates. Checking at each entry point
// buys the stronger property: a refused ID takes no write lock and creates no
// directory, so it leaves the filesystem untouched.
func ValidateSessionID(id string) error {
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
			return fmt.Errorf("session id %q has a disallowed byte %#x at offset %d: %w", id, c, i, ErrInvalidSessionID)
		}
	}
	if windowsReservedSessionIDs[strings.ToUpper(id)] {
		return fmt.Errorf("session id %q is a reserved device name: %w", id, ErrInvalidSessionID)
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
// land on the same lock. It is lowercased first, because a lock may only ever be
// coarser than the file it guards, and ValidateSessionID admits "ABC" and "abc"
// as distinct IDs that name one file on a case-insensitive filesystem. It needs
// no path canonicalization: every caller validates first, so no ID reaching here
// can carry a separator or a "." segment for filepath.Clean to collapse.
func sessionMetaWriteLock(sessionID string) *sync.Mutex {
	key := strings.ToLower(sessionID)
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
	CheapModel  string          `json:"cheap_model,omitempty"`
	VisionModel string          `json:"vision_model,omitempty"`
	Config      ConfigSnapshot  `json:"config"`     // the session's configuration
	EnvInfo     EnvironmentInfo `json:"env_info"`   // captured environment description
	CreatedAt   time.Time       `json:"created_at"` // when the session was first created
	UpdatedAt   time.Time       `json:"updated_at"` // last time the meta was written
	// Revision is a monotonic per-session counter bumped on every save, so two
	// revisions that share a timestamp (a fork tag or an ObservedBy append
	// re-saves without advancing UpdatedAt) can still be ordered. Zero on metas
	// written before it existed.
	Revision                 uint64 `json:"revision,omitempty"`
	TurnCount                int    `json:"turn_count"` // number of model responses processed
	AcceptedInputTurns       int    `json:"accepted_input_turns,omitempty"`
	TurnBudgetWarningEmitted bool   `json:"turn_budget_warning_emitted,omitempty"`
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
	// runs (set via EVENER_SESSION_ORIGIN), empty for normal sessions. The hub
	// classifies an all-"test" project into the "Test runs" group.
	Origin string `json:"origin,omitempty"`
	// Goal holds the persisted goal state so the objective survives daemon
	// restart and evener resume. It is nil when no goal is active or has been set.
	Goal *GoalSnapshot `json:"goal,omitempty"`
	// PinnedNote is the agent's self-compaction note_to_self, persisted so it
	// survives daemon restart and evener resume (mirrors Goal).
	PinnedNote string `json:"pinned_note,omitempty"`
	// Skills contains future-only activation metadata, never ordinary bodies.
	Skills *SkillLifecycleSnapshot `json:"skills,omitempty"`
	// HumanNote is a projection of the canonical client-mutation snapshot.
	// It is not imported as human-note authority when restoring a session.
	HumanNote string `json:"human_note,omitempty"`
	// AgentNote is the agent's one-paragraph session whiteboard, persisted like
	// HumanNote. Empty means unset.
	AgentNote string `json:"agent_note,omitempty"`
	// SessionURLs is the agent-curated session URL list, persisted so it
	// survives daemon restart and evener resume. Empty/nil means no links.
	SessionURLs []SessionURL `json:"session_urls,omitempty"`
	// EnvContext is the environment-context tracker state (last emitted
	// snapshot), persisted so resume stays silent when nothing changed.
	EnvContext *envctx.State `json:"env_context,omitempty"`
	// ReasoningEffortEscalated records the sticky loop-detection escalation
	// separately from Config so a resumed lower-effort task cannot undo it.
	ReasoningEffortEscalated bool `json:"reasoning_effort_escalated,omitempty"`
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
	// WorktreeManaged is true when WorktreePath is a evener-managed worktree
	// (entered via create, or switch by name/managed path) — the idempotent
	// occupancy-lock rule applies to it on resume re-entry. False for a
	// non-managed worktree entered by path, which carries no evener lock.
	WorktreeManaged bool `json:"worktree_managed,omitempty"`
	// WorktreeRestoreRoot is the root of the env saved the first time the
	// session entered WorktreePath (native worktree tools spec §7
	// "env-restore model"). On resume, a foreign lock or a worktree that no
	// longer exists lands the session here instead, with a notice.
	WorktreeRestoreRoot string `json:"worktree_restore_root,omitempty"`
	// CumulativeUsage carries the session's running self-only token totals so
	// they survive restart/resume.
	CumulativeUsage CumulativeUsage `json:"cumulative_usage,omitzero"`
	// WorkMillis is the accumulated wall-clock work time (sum of every turn's
	// duration, interrupted and failed included), persisted so the total
	// survives restart/resume.
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
// nil pointers map to 0.
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

// SessionURL is one entry in a session's shared-notes URL list.
type SessionURL struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Label   string `json:"label,omitempty"`
	AddedBy string `json:"added_by,omitempty"`
	AddedAt int64  `json:"added_at,omitempty"`
}

// SessionDisplayName returns the best available human-readable title for a
// session: the generated name if set, otherwise the original prompt, falling
// back to the session ID.
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

// SessionMetaTombstoneSuffix names the marker TombstoneSessionMeta leaves beside
// a removed session so a writer that acquires the meta lock afterwards refuses
// to recreate it. It is deliberately not a *.meta.json suffix, so the directory
// scanners ignore it.
const SessionMetaTombstoneSuffix = ".meta.json.deleted"

// ErrSessionDeleted reports a write attempted against a session that has been
// deleted (tombstoned). The write is dropped, not retried: the session no longer
// exists, so the metadata change is intentionally lost. Callers own the policy
// for surfacing it — the hub warns on an autosave and maps it to an internal
// error on a rename — but none may retry the save, because a retry is refused
// again by the same marker.
var ErrSessionDeleted = errors.New("session meta deleted")

// TombstoneSessionMeta writes a session's deletion marker while holding the same
// in-process and cross-process locks every writer takes. A writer that acquires
// the lock afterwards observes the tombstone and refuses to recreate the meta, so
// an in-flight out-of-process autosave cannot resurrect a session the hub is
// deleting. It does not remove the meta itself: the caller's artifact sweep owns
// that, so a failed sweep still leaves the metadata for a resume. The tombstone
// must be left in place by the caller once the metadata is durably removed;
// UntombstoneSessionMeta reverses it when the sweep fails first.
//
// When the sessions dir is already gone there is nothing to fence, so the
// tombstone is skipped rather than creating the directory: a marker write must
// never resurrect a deleted project's state dir, which the PastIndex projects/*
// glob would then surface as a live project. A daemon that recreates the dir
// after the deletion is handled by the durable deletion record, not this marker.
func TombstoneSessionMeta(dir, id string) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}
	lock := sessionMetaWriteLock(id)
	lock.Lock()
	defer lock.Unlock()
	err := withExistingSessionsDirLock(sessionMetaFS, dir, id, func() error {
		tombstone := filepath.Join(dir, sessionsSubdir, id+SessionMetaTombstoneSuffix)
		if err := afero.WriteFile(sessionMetaFS, tombstone, nil, 0o600); err != nil {
			if os.IsNotExist(err) {
				return errSessionsDirAbsent
			}
			return fmt.Errorf("write session tombstone: %w", err)
		}
		return nil
	})
	if errors.Is(err, errSessionsDirAbsent) {
		return nil
	}
	return err
}

// UntombstoneSessionMeta removes a session's deletion marker under the same
// in-process and cross-process locks TombstoneSessionMeta takes. A caller rolls
// the marker back when a deletion fails before the metadata is durably removed:
// while the meta still exists the session is still resumable and must stay
// writable, whereas leaving the marker would make every later save — autosave,
// rename, observer append — fail with ErrSessionDeleted for a session that was
// never actually deleted. Removing an absent marker is a no-op.
func UntombstoneSessionMeta(dir, id string) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}
	lock := sessionMetaWriteLock(id)
	lock.Lock()
	defer lock.Unlock()
	err := withExistingSessionsDirLock(sessionMetaFS, dir, id, func() error {
		tombstone := filepath.Join(dir, sessionsSubdir, id+SessionMetaTombstoneSuffix)
		if err := sessionMetaFS.Remove(tombstone); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session tombstone: %w", err)
		}
		return nil
	})
	if errors.Is(err, errSessionsDirAbsent) {
		return nil
	}
	return err
}

// SessionsDirListable reports whether dir's sessions subdirectory can be
// listed — the same gate ListSessionMetas applies before reading any metas
// (a missing directory counts as listable: the list is simply empty). A
// caller deciding whether a session EXISTS under dir via a targeted read
// (rather than the full list) uses this to skip the same projects the list
// path skips, so the two paths cannot disagree about which projects hold
// sessions.
//
// The check opens the directory rather than listing it: opening fails with
// the same permission error a full listing would fail on, at O(1) instead
// of O(entries). A caller only needs the boolean; the one observable
// divergence — a readable regular file named "sessions", which opens fine
// but lists as ENOTDIR — yields "no session here" on both paths anyway.
func SessionsDirListable(fs afero.Fs, dir string) bool {
	f, err := fs.Open(filepath.Join(dir, sessionsSubdir))
	if err == nil {
		_ = f.Close()
		return true
	}
	return os.IsNotExist(err)
}

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
	if err := ValidateSessionID(meta.ID); err != nil {
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
	if err := ValidateSessionID(workerSessionID); err != nil {
		return err
	}
	lock := sessionMetaWriteLock(workerSessionID)
	lock.Lock()
	defer lock.Unlock()
	// The load must happen under the cross-process lock too: loading first and
	// saving later would write this process's stale copy of every other field
	// over a concurrent writer's newer one.
	return withSessionMetaCrossProcessLock(fs, dir, workerSessionID, func() error {
		meta, err := loadSessionMetaFS(fs, dir, workerSessionID)
		if err != nil {
			return err
		}
		// Only workerSessionID is validated: it names the file being written, while
		// observerSessionID is persisted as data and never joined into a path here.
		meta.ObservedBy = stableUnion(meta.ObservedBy, []string{observerSessionID})
		return writeSessionMetaLocked(fs, dir, meta)
	})
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
	return withSessionMetaCrossProcessLock(fs, dir, meta.ID, func() error {
		return writeSessionMetaLocked(fs, dir, meta)
	})
}

// withSessionMetaCrossProcessLock runs fn with the session's cross-process meta
// lock held (and the sessions dir ensured). The caller must already hold the
// in-process striped lock. The lock covers fn's whole load/merge/increment/write
// so a caller that reads the current meta before mutating it cannot race a
// writer in another process.
func withSessionMetaCrossProcessLock(fs afero.Fs, dir, id string, fn func() error) error {
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := fs.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	return withExistingSessionsDirLock(fs, dir, id, fn)
}

// errSessionsDirAbsent reports that a marker-only lock path found the sessions
// dir already gone. Callers treat it as "nothing to fence" rather than a
// failure: there is no metadata to protect.
var errSessionsDirAbsent = errors.New("sessions dir absent")

// withExistingSessionsDirLock takes the same in-process and cross-process locks
// as withSessionMetaCrossProcessLock but never creates the sessions dir. The
// tombstone paths use it so deleting an already-removed session cannot recreate
// the deleted project's state directory — which the PastIndex projects/* glob
// would then surface as a live project. It reports errSessionsDirAbsent when
// that dir is missing.
func withExistingSessionsDirLock(fs afero.Fs, dir, id string, fn func() error) error {
	// The Revision increment is a read-modify-write, and the daemon rewrites the
	// same session's meta out of process, so the in-process striped lock alone
	// cannot serialize it. Hold a file lock across the load/increment/rename.
	release, _, err := lockSessionMetaCrossProcess(fs, dir, id)
	if err != nil {
		if os.IsNotExist(err) {
			return errSessionsDirAbsent
		}
		return fmt.Errorf("lock session meta: %w", err)
	}
	defer release()
	return fn()
}

// writeSessionMetaLocked loads the current meta, unions ObservedBy, bumps
// Revision, and writes atomically. It assumes the caller holds both locks, so
// the read-modify-write cannot interleave with another writer.
func writeSessionMetaLocked(fs afero.Fs, dir string, meta SessionMeta) error {
	// A deleted session's tombstone is written under this same lock; refuse to
	// recreate the metadata so an in-flight out-of-process autosave cannot
	// resurrect a session the hub just deleted.
	if exists, err := afero.Exists(fs, filepath.Join(dir, sessionsSubdir, meta.ID+SessionMetaTombstoneSuffix)); err != nil {
		return err
	} else if exists {
		return ErrSessionDeleted
	}
	previous, err := loadSessionMetaFS(fs, dir, meta.ID)
	if err == nil {
		meta.ObservedBy = stableUnion(previous.ObservedBy, meta.ObservedBy)
		if previous.Revision == math.MaxUint64 {
			return fmt.Errorf("session meta revision overflow for %s", meta.ID)
		}
		meta.Revision = previous.Revision + 1
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		meta.Revision = 1
	}

	data, err := marshalSessionMeta(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}

	target := filepath.Join(dir, sessionsSubdir, meta.ID+".meta.json")
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

// lstatIfPossible does Lstat when the fs implements afero.Lstater (OsFs returns
// symlink-aware info), and falls back to Stat otherwise. MemMapFs implements
// Lstater but LstatIfPossible does Stat internally (usedLstat=false) since it has
// no symlinks; either way the returned FileInfo is correct for IsRegular checks.
func lstatIfPossible(fs afero.Fs, path string) (os.FileInfo, error) {
	if lstater, ok := fs.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(path)
		return info, err
	}
	return fs.Stat(path)
}

// loadSessionMetaFS is the filesystem seam beneath LoadSessionMeta.
func loadSessionMetaFS(fs afero.Fs, dir, id string) (SessionMeta, error) {
	if err := ValidateSessionID(id); err != nil {
		return SessionMeta{}, err
	}
	path := filepath.Join(dir, sessionsSubdir, id+".meta.json")
	// Finding 3: validate intermediate path components (e.g. sessions/) for
	// symlinks. A symlinked sessions/ directory pointing outside the state
	// root would surface metadata from an untrusted location. The leaf
	// itself is NOT checked here — it is opened with O_NOFOLLOW by
	// readMetaFile below (finding 6), which atomically refuses a symlink at
	// the final component and closes the Lstat-then-open TOCTOU window the
	// previous leaf-only guard left.
	if err := metaComponentWalk(fs, path, dir); err != nil {
		return SessionMeta{}, fmt.Errorf("read session meta %s: %w", id, err)
	}
	// Finding 6: open through a single no-follow descriptor and read from it,
	// rather than Lstat-then-ReadFile. O_NOFOLLOW refuses a symlink at the
	// leaf atomically (ELOOP); the descriptor is then read directly, so
	// nothing can be swapped between the check and the bytes. Do NOT fstat
	// for regular or use O_NONBLOCK: a FIFO at this path is a deliberate
	// synchronization barrier in retirement tests and must be allowed to
	// block the open. O_NOFOLLOW is harmless for FIFOs and regular files —
	// it only refuses when the final component is a symlink.
	data, err := readMetaFile(fs, path)
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

// metaComponentWalk Lstats each existing intermediate component of path between
// root (exclusive) and the leaf (exclusive), returning an error if any is a
// symlink. The leaf itself is NOT checked here — it is opened with O_NOFOLLOW
// by readMetaFile, which atomically refuses a symlink at the final component.
// Mirrors symlinkErrorDeep from package agent (transcript_lookup.go), scoped
// to intermediates only and using the injected afero.Fs so it works on both
// the real filesystem (Lstat detects symlinks) and in-memory test filesystems
// (Stat, which never reports symlinks).
func metaComponentWalk(fs afero.Fs, path, root string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	for dir != root && dir != "" && dir != string(filepath.Separator) && dir != "." {
		info, _ := lstatIfPossible(fs, dir)
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses a symlink (%q): symlinks are not allowed on the meta read path", path, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

// readMetaFile reads the meta file through a no-follow descriptor when the
// underlying filesystem is the real OS filesystem, closing the leaf TOCTOU
// window (finding 6). The no-follow open must fire for every afero wrapper
// that ultimately delegates to the OS, not just bare *afero.OsFs: ReadOnlyFs
// and BasePathFs over OsFs pass reads through to os.Open (which follows leaf
// symlinks), so matching only *afero.OsFs lets a symlinked .meta.json leaf
// bypass the O_NOFOLLOW guard via any wrapper.
//
// The unwrap is conditional on the wrapped source being OS-backed. Both
// ReadOnlyFs and BasePathFs keep their backing in an unexported "source" Fs
// field with no public accessor, so the source is inspected via reflection
// (type-only: no pointer arithmetic, no unsafe). The pre-fix code unwrapped
// these wrappers unconditionally and called readFileNoFollowOS — the real OS
// filesystem — even when the source was an in-memory filesystem. For
// ReadOnlyFs(MemMapFs) or BasePathFs(MemMapFs) the identity/RealPath path is
// not a real OS path, so the read silently hit the OS filesystem instead of
// the in-memory FS, returning unrelated metadata or a spurious ENOENT.
//
// Routing:
//   - *afero.OsFs and afero.OsFs (value receiver): the real OS filesystem —
//     open the path directly with O_NOFOLLOW.
//   - *afero.ReadOnlyFs and *afero.BasePathFs: inspect the wrapped source.
//     OS-backed (an OsFs holds the source field) — the wrapper delegates to
//     the OS, so the no-follow open must fire to close the leaf-symlink
//     window (finding 1 of round 13): ReadOnlyFs passes paths through
//     unchanged, so readFileNoFollowOS(path) is correct; BasePathFs remaps
//     paths via RealPath, so resolve to the real OS path first. Non-OS-backed
//     (e.g. MemMapFs) or unknown source — fall back to afero.ReadFile, the
//     documented portable read, which reads the in-memory filesystem.
//   - any other afero.Fs (bare MemMapFs, unknown wrappers): afero.ReadFile.
//
// RealPath on BasePathFs can fail on paths outside the base (os.ErrNotExist);
// in that case the OS-backed branch falls back to afero.ReadFile so the caller
// sees the same error the wrapper would produce.
func readMetaFile(fs afero.Fs, path string) ([]byte, error) {
	switch t := fs.(type) {
	case *afero.OsFs, afero.OsFs:
		return readFileNoFollowOS(path)
	case *afero.ReadOnlyFs:
		// ReadOnlyFs passes paths through unchanged. Only route to the
		// no-follow OS open when the wrapped source is the real OS
		// filesystem; otherwise read through the wrapper itself so an
		// in-memory backing (MemMapFs) is read, not the OS filesystem.
		if wrapperSourceIsOS(t) {
			return readFileNoFollowOS(path)
		}
		return afero.ReadFile(fs, path)
	case *afero.BasePathFs:
		// BasePathFs remaps paths via RealPath. Only resolve to a real OS
		// path and no-follow open when the wrapped source is the real OS
		// filesystem; otherwise read through the wrapper itself.
		if wrapperSourceIsOS(t) {
			realPath, err := t.RealPath(path)
			if err != nil {
				// Path outside the base dir or other RealPath failure:
				// fall back to the wrapper's own read so the caller sees
				// the same error it would have gotten.
				return afero.ReadFile(fs, path)
			}
			return readFileNoFollowOS(realPath)
		}
		return afero.ReadFile(fs, path)
	default:
		return afero.ReadFile(fs, path)
	}
}

// wrapperSourceIsOS reports whether the afero wrapper's unexported "source" Fs
// field holds an OsFs (pointer or value receiver). It is used by readMetaFile
// to decide whether a wrapper delegates to the real OS filesystem — in which
// case the no-follow open must fire to close the leaf-symlink window (round 13
// finding 1) — or to an in-memory filesystem, in which case the read must go
// through afero.ReadFile so the in-memory content comes back.
//
// Both ReadOnlyFs and BasePathFs keep their backing in an unexported "source"
// field with no public accessor, so reflection is the only way to inspect it.
// The inspection is type-only (Kind and Type comparisons); it never calls
// Interface() on the unexported field, which would panic, and uses no unsafe.
// An unknown source (nil, or a non-struct wrapper without a "source" field)
// reports false, so the read falls back to the portable afero.ReadFile.
func wrapperSourceIsOS(w afero.Fs) bool {
	v := reflect.ValueOf(w)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	src := v.FieldByName("source")
	if !src.IsValid() || src.Kind() != reflect.Interface {
		return false
	}
	concrete := src.Elem()
	if !concrete.IsValid() {
		return false
	}
	ct := concrete.Type()
	return ct == reflect.TypeOf((*afero.OsFs)(nil)) || ct == reflect.TypeOf(afero.OsFs{})
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
		// Reject non-regular files: a symlinked .meta.json pointing outside the
		// state root would otherwise be loaded via afero.ReadFile which follows
		// the link. afero.ReadDir on OsFs returns Lstat-based FileInfo, so
		// symlinks carry ModeSymlink and IsRegular returns false.
		if !e.Mode().IsRegular() || !strings.HasSuffix(e.Name(), ".meta.json") {
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
