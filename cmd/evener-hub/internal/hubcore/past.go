package hubcore

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// PastEntry is one indexed past session.
type PastEntry struct {
	ID       string
	Meta     schema.SessionMeta
	StateDir string // the project's state-dir root (parent of `sessions/`)
}

// PastIndex globs `<projectsRoot>/<project-id>/sessions/*.meta.json` style paths
// (state_glob points at the projects-root pattern with a trailing wildcard).
// All metas are kept in memory; rebuild is lazy or scheduled.
type PastIndex struct {
	stateGlob string
	dbPath    string
	openDB    func(string, string) (*sql.DB, error)

	// fs mediates the directory-scaffolding, chmod, and sessions-dir scan
	// filesystem ops. It defaults to afero.NewOsFs() (identical to direct os
	// calls). NOTE: the SQLite FTS index (sql.Open on dbPath) opens the file on
	// the real OS filesystem directly and does NOT route through fs — injecting
	// a non-OS filesystem redirects only the surrounding dir/chmod/readdir ops,
	// not the database itself.
	fs afero.Fs

	mu   sync.RWMutex
	all  []PastEntry // sorted by the Hub session ordering contract
	byID map[string]PastEntry
	fts  bool
	// gen is a monotonic generation bumped under mu by every index-mutating
	// swap (Rebuild, UpdateMeta, foldOne, SeedForTest). A publisher captures the
	// generation of the snapshot it built and abandons its (FTS + fingerprint)
	// write once a newer mutation has superseded it, so an older snapshot can
	// never clobber a fresher one's mirror or lock the mirror into stale data.
	gen uint64
	// rebuildGen is bumped only by Rebuild's swap. Find captures it before
	// probing so foldOne does not re-insert a probe when a Rebuild completed in
	// the meantime and did not find the session — the deletion won, and folding
	// the stale probe would resurrect it.
	rebuildGen uint64
	// idGen is a per-session mutation generation, bumped under mu by every
	// operation that changes one id's indexed row: a fold or update that inserts
	// or replaces it, and an eviction — including a confirmed-absence eviction of
	// an id that is not currently indexed, which must still fence that id's
	// in-flight probe. Find captures an id's generation before probing, and evict
	// and foldOne each act only while it is unchanged, so a fold of a session
	// cannot be clobbered by a miss-eviction of that same session and, because it
	// is keyed by id, an eviction of an unrelated session cannot invalidate a
	// fold of another. Whole-index replacement is covered separately by
	// rebuildGen.
	idGen map[string]uint64
	// probePins counts in-flight Find probes per id. It lets unpinProbe drop an
	// id's generation entry only once no probe can still compare against it,
	// keeping idGen bounded by the live index plus active probes rather than
	// growing without bound on every confirmed miss for a nonexistent id.
	probePins map[string]int

	// ftsMu serializes every write to the SQLite FTS mirror so an incremental
	// publish's delta is applied against exactly the snapshot the previous
	// publish left (tracked in published). Serializing here keeps concurrent
	// writers from interleaving table writes; the generation check, not the lock
	// order, decides which snapshot wins (see publishFTS).
	ftsMu sync.Mutex
	// published is the snapshot the FTS mirror currently reflects, or nil when
	// the mirror is absent, unknown, or stale. Guarded by ftsMu; only touched
	// inside publishFTS.
	published []PastEntry
	// ftsOwner is this PastIndex instance's random identity and ftsSeq counts
	// its successful FTS writes; lastSeq is the seq of the most recent one.
	// Together (owner, seq) is the baseline token stored in the dedicated
	// past_sessions_fts_state row: a delta accepts the table only when it
	// carries exactly (ftsOwner, lastSeq), so a restored or foreign index.db —
	// even one with the same row count, or a backup from an earlier run of this
	// binary or another instance, whose owner differs — is detected as stale,
	// while a same-instance restore at the exact last write is identical
	// content and harmless. The token lives in its own table, not the shared
	// DB header: index.db is also written by the archive/favorite/pin stores
	// and the fleet-view migration reserves PRAGMA user_version for schema
	// versioning (see sqlite_dsn.go). Guarded by ftsMu.
	ftsOwner int64
	ftsSeq   int64
	lastSeq  int64

	// skipped maps every path the last Rebuild refused to index to the reason
	// it was refused, so the next Rebuild can report only what is newly
	// unindexable (see reportSkips).
	skipped map[string]string

	// onChange, when set via SetOnChange, is fired by Rebuild only when the
	// indexed content's fingerprint actually changes.
	onChange func()
	// fingerprint is the content hash from the most recent Rebuild (see
	// contentFingerprint), used to gate onChange against no-op rebuilds.
	fingerprint uint64
	// afterFindProbe, when non-nil, runs in Find after a miss's probe and before
	// foldOne folds the row. Instance-scoped test seam for interleaving a
	// concurrent writer that indexes a newer row first; nil in production.
	afterFindProbe func()
	// afterFindCacheMiss, when non-nil, runs in Find after its top-level
	// findCached miss and before the first probe. Instance-scoped test seam for
	// interleaving a Rebuild swap in that window; nil in production.
	afterFindCacheMiss func()
}

// NewPastIndex returns a PastIndex configured to glob projectGlob.
//
// projectGlob is a shell-style glob like
// "/Users/jesse/.local/state/evener/projects/*"
// — each match is treated as a state-dir root containing a `sessions/`
// subdirectory of meta files.
func NewPastIndex(projectGlob string) *PastIndex {
	return &PastIndex{
		stateGlob: projectGlob,
		fs:        afero.NewOsFs(),
		openDB:    sql.Open,
		byID:      make(map[string]PastEntry),
		idGen:     make(map[string]uint64),
		probePins: make(map[string]int),
		ftsOwner:  pastIndexOwner(),
	}
}

// pastIndexOwner returns a process-random identity for the FTS baseline token
// (see PastIndex.ftsOwner). A collision only costs a redundant full rebuild.
func pastIndexOwner() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return int64(binary.LittleEndian.Uint64(b[:]))
	}
	return time.Now().UnixNano()
}

// SetFs overrides the index's filesystem for the dir-scaffolding, chmod, and
// sessions-dir scan ops (see the fs field note about the SQLite boundary).
// Returns the index for call chaining.
func (i *PastIndex) SetFs(fs afero.Fs) *PastIndex {
	i.fs = fs
	return i
}

// NewPastIndexWithDB returns a PastIndex that mirrors rebuilt metadata into a
// SQLite FTS index. Search falls back to the in-memory index if SQLite is not
// available or the FTS index cannot satisfy the query.
func NewPastIndexWithDB(projectGlob, dbPath string) *PastIndex {
	idx := NewPastIndex(projectGlob)
	idx.dbPath = dbPath
	return idx
}

// StateGlob returns the project glob the index scans.
func (i *PastIndex) StateGlob() string {
	return i.stateGlob
}

// SetOnChange registers a callback fired by Rebuild/UpdateMeta only when the
// indexed content actually changes (a Find-miss rebuild with no delta does not
// fire — round-3 G5). Nil disables the hook.
//
// Ordering hazard: call this after an initial Rebuild, not before. An
// unseeded index's first-ever Rebuild always looks like a delta — even zero
// entries fingerprint to a non-zero constant, never equal to the zero-value
// initial fingerprint — so wiring the hook first fires a spurious "change"
// on nothing (runMain always seeds via the startup Rebuild before wiring).
func (i *PastIndex) SetOnChange(fn func()) { i.onChange = fn }

// contentFingerprint hashes the consumer-visible fields of the sorted entries so
// a publish can detect a genuine content delta without a deep compare. It
// deliberately covers ForkLabel and ObservedBy: a fork tag or an observer append
// re-saves the meta without moving UpdatedAt, and a fold that adopts such a row
// must still fire onChange so the Hub bumps/invalidates navigation.
func contentFingerprint(all []PastEntry) uint64 {
	h := fnv.New64a()
	for _, e := range all {
		_, _ = h.Write([]byte(e.ID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.Meta.Name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.Meta.ForkLabel))
		_, _ = h.Write([]byte{0})
		for _, observer := range e.Meta.ObservedBy {
			_, _ = h.Write([]byte(observer))
			_, _ = h.Write([]byte{0})
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(e.Meta.UpdatedAt.UnixNano()))
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

// Rebuild scans every project under stateGlob and reloads the index. The
// bool return reports whether the reload's content actually differed from
// what was already indexed AND a registered onChange callback fired for it
// (false on a no-op reload, a glob error, or when SetOnChange was never
// called) — so a caller relying on that hook to signal the change elsewhere
// (e.g. a broadcast) can tell whether that already happened, or whether it
// needs to compensate.
//
// The disk scan runs unlocked, so a concurrent fold or rename can leave the
// scan's view stale before it takes the lock to swap. Rebuild captures the
// generation before scanning and, when a mutation moved it, rescans instead of
// swapping — otherwise a slow Rebuild would overwrite a newer in-memory
// snapshot with stale disk state and mark that stale FTS snapshot healthy
// (#724). Bounded so a busy tick cannot spin; after the bound it leaves the
// newer snapshot in place and the next tick re-scans.
func (i *PastIndex) Rebuild() (bool, error) {
	if i.stateGlob == "" {
		return false, nil
	}
	for range pastRebuildAttempts {
		i.mu.RLock()
		startGen := i.gen
		i.mu.RUnlock()

		all, byID, skipped, err := i.scanAll()
		if err != nil {
			return false, err
		}

		if pastBeforeRebuildSwap != nil {
			pastBeforeRebuildSwap()
		}

		i.mu.Lock()
		if i.gen != startGen {
			// A newer mutation superseded this scan; rescan rather than clobber.
			i.mu.Unlock()
			continue
		}
		i.all = all
		i.byID = byID
		// Drop generation fences for ids this scan no longer indexes so idGen stays
		// bounded by the live index rather than by every id ever seen. An in-flight
		// probe for a pruned id is already invalidated by the rebuildGen bump below.
		for id := range i.idGen {
			if _, ok := byID[id]; !ok {
				delete(i.idGen, id)
			}
		}
		i.gen++
		i.rebuildGen++
		gen := i.gen
		i.mu.Unlock()
		// Report skips only for the scan that actually replaced the index; a
		// superseded attempt's stale view must not move the skip baseline or
		// emit a diagnostic for a scan that was discarded.
		i.reportSkips(skipped)
		return i.publishAndSignal(all, gen), nil
	}
	// Contended on every attempt: keep the newer in-memory snapshot rather than
	// overwrite it with a scan already known to be stale.
	return false, nil
}

// pastRebuildAttempts bounds how many times Rebuild rescans when a mutation
// supersedes its unlocked scan (see Rebuild).
const pastRebuildAttempts = 3

// pastBeforeRebuildSwap, when non-nil, runs after Rebuild's unlocked disk scan
// and before it takes the lock to swap. It is a deterministic test seam for
// interleaving a mutation with a scan that predates it; nil in production.
var pastBeforeRebuildSwap func()

// scanAll performs Rebuild's unlocked disk scan: it globs stateGlob, reads each
// project's session metas, and returns the entries sorted per the Hub ordering
// contract, the by-id map, and the skip reasons for reportSkips.
func (i *PastIndex) scanAll() ([]PastEntry, map[string]PastEntry, map[string]string, error) {
	matches, err := filepath.Glob(i.stateGlob)
	if err != nil {
		return nil, nil, nil, err
	}
	byID := make(map[string]PastEntry)
	skipped := make(map[string]string)
	for _, project := range matches {
		if err := identifier.ValidateProjectID(filepath.Base(project)); err != nil {
			skipped[project] = badProjectID(err)
			continue
		}
		metas, err := schema.ListSessionMetas(project)
		if err != nil {
			skipped[project] = "list session metas: " + err.Error()
			continue
		}
		indexed := make(map[string]bool, len(metas))
		for _, m := range metas {
			if err := identifier.ValidateSessionID(m.ID); err != nil {
				skipped[sessionMetaPath(project, m.ID)] = badSessionID(err)
				continue
			}
			indexed[m.ID] = true
			pe := PastEntry{ID: m.ID, Meta: m, StateDir: project}
			byID[m.ID] = pe
		}
		reportUnlistedMetas(i.fs, project, indexed, skipped)
	}
	// i.all derives from byID (one row per id, the LAST project's row for a
	// session present in several projects) rather than accumulating per
	// project: appending as the loop scanned would keep the FIRST project's
	// row while byID keeps the LAST's, leaving the replace-one-row writers
	// below (UpdateMeta, foldOne) and the next Rebuild fighting over the
	// duplicate forever, firing onChange every tick on unchanged disk.
	all := make([]PastEntry, 0, len(byID))
	for _, pe := range byID {
		all = append(all, pe)
	}
	sort.SliceStable(all, func(a, b int) bool {
		return sessionMetaLess(all[a].Meta, all[b].Meta)
	})
	return all, byID, skipped, nil
}

// maxReportedSkips bounds how many individual unindexable paths one Rebuild
// names, so a projects root full of pre-identifier directories reports a sample
// and a count rather than hundreds of lines.
const maxReportedSkips = 5

// metaFileSuffix is the on-disk filename suffix schema writes a session meta
// under, mirrored here so a skip report can name the files the lister dropped.
const metaFileSuffix = ".meta.json"

// The identifier validators report the rule an id broke ("invalid UUID
// payload") but not the shape to write instead, which is the fact whoever
// seeded the entry actually needs. A skip report carries both.
const (
	sessionIDShape = "want a 22-character base62 UUIDv7 payload"
	projectIDShape = "want <readable>-<10 base62>"
)

// badSessionID renders the reason a session id was rejected.
func badSessionID(err error) string {
	return "invalid session id (" + sessionIDShape + "): " + err.Error()
}

// badProjectID renders the reason a project directory name was rejected.
func badProjectID(err error) string {
	return "invalid project id (" + projectIDShape + "): " + err.Error()
}

// sessionMetaPath renders the on-disk location a session's meta was read from,
// for naming it in a skip report.
func sessionMetaPath(project, id string) string {
	return filepath.Join(project, "sessions", id+".meta.json")
}

// reportUnlistedMetas records every <id>.meta.json under a project that
// ListSessionMetas did not return — an id that fails validation, an id that
// disagrees with its filename, or a file it could not decode.
//
// Those drops happen a layer below this index, inside schema.ListSessionMetas,
// and they are the actual reason a hand-seeded session with a plausible-looking
// id never appears: the index's own session-id guard can only reject a meta the
// lister already refused to hand it. Re-deriving the difference here is what
// puts the seeded path and its rejection in front of whoever seeded it.
func reportUnlistedMetas(fs afero.Fs, project string, indexed map[string]bool, skipped map[string]string) {
	sessionsDir := filepath.Join(project, "sessions")
	entries, err := afero.ReadDir(fs, sessionsDir)
	if err != nil {
		return // no sessions dir (or unreadable): nothing to compare against
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), metaFileSuffix) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), metaFileSuffix)
		if indexed[id] {
			continue
		}
		reason := "unreadable session meta, or its id disagrees with its filename"
		if err := identifier.ValidateSessionID(id); err != nil {
			reason = badSessionID(err)
		}
		skipped[filepath.Join(sessionsDir, e.Name())] = reason
	}
}

// reportSkips names the entries this Rebuild refused to index that the previous
// Rebuild did not, and records the full set as the baseline for the next one.
//
// Skipping is the right behavior — a stray directory must not fail the scan —
// but a silent skip puts the symptom nowhere near the cause: the session simply
// does not exist as far as every reader is concerned, and the only way back to
// the id encoding is to read this file. Reporting just the newly-unindexable
// keeps a steady-state rebuild tick quiet while still pinning a freshly seeded
// session or project to the exact path and validation that rejected it.
func (i *PastIndex) reportSkips(skipped map[string]string) {
	i.mu.Lock()
	previous := i.skipped
	i.skipped = skipped
	i.mu.Unlock()

	fresh := make([]string, 0, len(skipped))
	for path := range skipped {
		if _, seen := previous[path]; !seen {
			fresh = append(fresh, path)
		}
	}
	if len(fresh) == 0 {
		return
	}
	sort.Strings(fresh)
	named := fresh
	if len(named) > maxReportedSkips {
		named = named[:maxReportedSkips]
	}
	for _, path := range named {
		fmt.Fprintf(os.Stderr, "[hub] past index: skipped %s: %s\n", path, skipped[path])
	}
	if len(fresh) > len(named) {
		fmt.Fprintf(os.Stderr, "[hub] past index: skipped %d more unindexable entries\n", len(fresh)-len(named))
	}
}

// UpdateMeta targets one existing entry: it replaces the meta, re-inserts the
// entry at its new sorted position (the title is a sort-key component), and
// publishes the change to the FTS mirror incrementally — only the renamed row
// is rewritten, while every other row keeps its stored sort_rank (search
// re-sorts the FTS ∪ memory union from the in-memory index, so a shifted row's
// stale rank is not observable). That keeps the rename response off the full
// disk Rebuild it cannot afford (round-2 A5/B1, round-3 H3). StateDir is
// preserved (rename never moves the files). No-op when the id is not already
// indexed.
//
// The reordered index is built into a freshly-allocated slice and only then
// swapped into i.all under the lock — mirroring Rebuild's own
// allocate-then-swap discipline — rather than mutated in place. Rebuild
// publishes i.all and then, after releasing the lock, keeps reading that same
// slice's backing array (for rebuildFTS/contentFingerprint). An in-place
// append/copy here would write into a backing array a concurrent, unlocked
// Rebuild may still be reading, which is a data race.
//
// The bool return reports whether the edit actually differed from what was
// already indexed AND a registered onChange callback fired for it (false on
// the untracked-id no-op, a no-op edit, or when SetOnChange was never
// called) — see Rebuild's return for why a caller needs this.
func (i *PastIndex) UpdateMeta(id string, meta schema.SessionMeta) bool {
	i.mu.Lock()
	old, ok := i.byID[id]
	if !ok {
		i.mu.Unlock()
		return false
	}
	if metaNewer(old.Meta, meta) {
		// The indexed row is strictly newer than this update — a concurrent
		// Rebuild, fold, or refresh advanced it — so keep it rather than let a
		// stale rename/refresh overwrite newer metadata.
		i.mu.Unlock()
		return false
	}
	pe := PastEntry{ID: id, Meta: meta, StateDir: old.StateDir}
	i.byID[id] = pe
	fresh := make([]PastEntry, 0, len(i.all))
	for _, e := range i.all {
		if e.ID != id {
			fresh = append(fresh, e)
		}
	}
	fresh = insertSorted(fresh, pe)
	i.all = fresh
	all := append([]PastEntry(nil), i.all...)
	i.idGen[id]++
	i.gen++
	gen := i.gen
	i.mu.Unlock()
	return i.publishAndSignal(all, gen)
}

// insertSorted returns a new slice with pe inserted at its sorted position
// (per sessionMetaLess). Callers own the lock; the input slice must not be
// aliased by anything a concurrent reader still holds (allocate-then-swap,
// see UpdateMeta's doc).
func insertSorted(entries []PastEntry, pe PastEntry) []PastEntry {
	pos := sort.Search(len(entries), func(k int) bool { return !sessionMetaLess(entries[k].Meta, pe.Meta) })
	entries = append(entries, PastEntry{})
	copy(entries[pos+1:], entries[pos:])
	entries[pos] = pe
	return entries
}

// publishAndSignal mirrors a freshly-swapped index snapshot into the FTS
// index and the content fingerprint, firing onChange when the content
// actually changed. all must be an immutable snapshot of i.all taken under
// the lock (publishFTS and contentFingerprint run unlocked), and gen the
// generation that snapshot belongs to (see PastIndex.gen). The bool is
// Rebuild/UpdateMeta's contract: whether content changed AND a registered
// onChange fired for it.
//
// A newer mutation (a fold or update landing after this snapshot was taken)
// bumps i.gen. This publisher then abandons its tail entirely: the newer
// publisher owns both the FTS mirror and the fingerprint, so writing this stale
// snapshot would regress the fingerprint (a spurious onChange on unchanged
// disk) and could lock stale rows into the mirror — the fold-vs-Rebuild
// stale-publish race (#724).
func (i *PastIndex) publishAndSignal(all []PastEntry, gen uint64) bool {
	if i.dbPath != "" {
		i.publishFTS(all, gen)
	}
	fp := contentFingerprint(all)
	i.mu.Lock()
	if i.gen != gen {
		i.mu.Unlock()
		return false
	}
	changed := fp != i.fingerprint
	i.fingerprint = fp
	i.mu.Unlock()
	if changed && i.onChange != nil {
		i.onChange()
	}
	return changed && i.onChange != nil
}

// RefreshOne re-reads one already-indexed session's on-disk meta and folds it
// into the index via UpdateMeta (the cheap reorder-without-rescan path),
// without waiting for the next full Rebuild. It exists because a session's
// meta.json can be rewritten out-of-process (the daemon's own periodic
// autosave) between Rebuild ticks, leaving AllMetas stale for up to a full
// rebuild interval.
//
// A no-op, not an error, in both edge cases a caller cannot prevent: an id
// RefreshOne has never indexed (mirrors UpdateMeta's own untracked-ID no-op),
// and a meta file that has been removed or renamed out from under an indexed
// id (e.g. racing session cleanup) — LoadSessionMeta's failure is logged and
// swallowed, leaving the existing entry untouched, rather than panicking or
// erroring the caller.
func (i *PastIndex) RefreshOne(id string) {
	i.mu.RLock()
	entry, ok := i.byID[id]
	i.mu.RUnlock()
	if !ok {
		return
	}
	meta, err := schema.LoadSessionMeta(entry.StateDir, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] RefreshOne(%s): load meta: %v\n", id, err)
		return
	}
	i.UpdateMeta(id, meta)
}

// All returns the full index sorted by the Hub session ordering contract.
func (i *PastIndex) All() []PastEntry {
	all, _ := i.snapshot()
	return all
}

// snapshot returns a copy of i.all together with the generation it was taken
// at, under a single lock, so a caller can publish exactly the state it saw.
func (i *PastIndex) snapshot() ([]PastEntry, uint64) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]PastEntry, len(i.all))
	copy(out, i.all)
	return out, i.gen
}

// Search returns the limit results starting at offset whose indexed text
// matches q. SQLite FTS contributes token-prefix matches when available; the
// in-memory scan preserves substring matches.
func (i *PastIndex) Search(q string, limit, offset int) []PastEntry {
	if strings.TrimSpace(q) != "" {
		// Search is FTS's only consumer, so a stale mirror (a fold's or
		// rebuild's rebuildFTS lost its SQLITE_BUSY race, or the db was
		// briefly unwritable) is observable only here — and this is the
		// repair point: re-publish the current snapshot so the FTS path
		// serves every indexed id again. While the mirror stays broken
		// every Search re-attempts the full FTS write; the first success
		// flips i.fts and the repair stops. The fingerprint gate keeps
		// the redundant publish from firing onChange.
		i.mu.RLock()
		ftsStale := i.dbPath != "" && !i.fts
		i.mu.RUnlock()
		if ftsStale {
			all, gen := i.snapshot()
			i.publishAndSignal(all, gen)
		}
		if fts, ok := i.searchFTS(q); ok {
			mem := i.searchMemoryMatches(q)
			return i.mergeSearchResults(fts, mem, limit, offset)
		}
	}
	return i.searchMemory(q, limit, offset)
}

func (i *PastIndex) searchMemory(q string, limit, offset int) []PastEntry {
	return paginatePastEntries(i.searchMemoryMatches(q), limit, offset)
}

func (i *PastIndex) searchMemoryMatches(q string) []PastEntry {
	i.mu.RLock()
	defer i.mu.RUnlock()

	q = strings.ToLower(strings.TrimSpace(q))
	out := []PastEntry{}
	for _, e := range i.all {
		if q == "" || matches(e, q) {
			out = append(out, e)
		}
	}
	return out
}

func paginatePastEntries(entries []PastEntry, limit, offset int) []PastEntry {
	if limit <= 0 || offset >= len(entries) {
		return nil
	}
	end := min(offset+limit, len(entries))
	return entries[offset:end]
}

func (i *PastIndex) mergeSearchResults(a, b []PastEntry, limit, offset int) []PastEntry {
	ids := make(map[string]struct{}, len(a)+len(b))
	for _, entry := range a {
		ids[entry.ID] = struct{}{}
	}
	for _, entry := range b {
		ids[entry.ID] = struct{}{}
	}
	if len(ids) == 0 {
		return nil
	}

	i.mu.RLock()
	out := make([]PastEntry, 0, len(ids))
	for _, entry := range i.all {
		if _, ok := ids[entry.ID]; ok {
			out = append(out, entry)
		}
	}
	i.mu.RUnlock()
	return paginatePastEntries(out, limit, offset)
}

const createPastSessionsFTS = `CREATE VIRTUAL TABLE IF NOT EXISTS past_sessions_fts USING fts5(
id,
name,
original_prompt,
working_dir,
state_dir UNINDEXED,
sort_rank UNINDEXED
)`

const insertPastSessionsFTS = `INSERT INTO past_sessions_fts(id, name, original_prompt, working_dir, state_dir, sort_rank) VALUES (?, ?, ?, ?, ?, ?)`

// createPastSessionsFTSState is the FTS baseline-token table: exactly one row
// carrying the (owner, seq) of the last successful FTS write. It is a dedicated
// table, not PRAGMA user_version, because index.db's header is shared with the
// archive/favorite/pin stores and reserved for schema versioning.
const createPastSessionsFTSState = `CREATE TABLE IF NOT EXISTS past_sessions_fts_state(
owner INTEGER NOT NULL,
seq INTEGER NOT NULL
)`

// publishFTS mirrors a freshly-swapped snapshot into the SQLite FTS index.
//
// It writes only the rows that differ from the last published snapshot (an
// incremental upsert) instead of rewriting the whole table: a single-session
// fold or rename touches O(changed) rows rather than O(index), which is what
// held the per-publish cost at ~275-437ms for a 13.5k-entry index. The delta is
// applied under ftsMu against the tracked published snapshot, so two concurrent
// publishers cannot interleave one snapshot's delta onto another's table — the
// last publish to take the lock still wins, the same last-writer-wins semantics
// the transaction-per-publish whole-table rewrite provided.
//
// The mirror is marked stale (i.fts=false) for the duration of the write, so a
// failed publish stays visible to Search's staleness gate; only a successful
// write flips it back true. When no usable prior snapshot exists (the first
// publish, or a previous failure left the mirror stale) this falls back to a
// full rebuildFTS.
//
// The delta's baseline (i.published) is read under ftsMu, so it is always the
// table's actual current content: every delta transforms between adjacent
// snapshots and the table can never end up a non-snapshot mix.
//
// ftsMu only orders writes; it does not decide which snapshot may win. Before
// writing, and again before calling the mirror healthy, this checks that gen is
// still the newest mutation (i.gen). A publisher whose snapshot a concurrent
// fold or rename superseded abandons its write outright rather than rewriting
// the mirror back to stale content; the newer publisher owns the mirror and
// Search's repair gate. This is the fold-vs-Rebuild stale-publish race (#724).
func (i *PastIndex) publishFTS(all []PastEntry, gen uint64) {
	if pastBeforePublishFTS != nil {
		pastBeforePublishFTS()
	}
	i.ftsMu.Lock()
	defer i.ftsMu.Unlock()

	i.mu.Lock()
	if i.gen != gen {
		// A newer mutation superseded this snapshot before we could write; the
		// newer publisher owns the mirror. Abandon without touching it.
		i.mu.Unlock()
		return
	}
	healthy := i.fts
	i.fts = false
	i.mu.Unlock()

	var err error
	if healthy && i.published != nil {
		err = i.rewriteFTSDelta(i.published, all)
		if errors.Is(err, errFTSBaselineStale) {
			// The mirror no longer holds the snapshot the delta was computed
			// against (its DB file was deleted or replaced); rebuild it whole.
			err = i.rebuildFTS(all)
		}
	} else {
		err = i.rebuildFTS(all)
	}
	if err != nil {
		// The table may now reflect neither snapshot, so drop the baseline and
		// let the next publish (or Search's repair) do a full rewrite.
		i.published = nil
		return
	}
	i.published = all
	i.mu.Lock()
	// Only call the mirror healthy while this snapshot is still the newest: a
	// mutation that landed during the write leaves a newer snapshot unpublished,
	// so flag the mirror stale for Search to repair.
	i.fts = i.gen == gen
	i.mu.Unlock()
}

// pastBeforePublishFTS, when non-nil, runs at the top of publishFTS just before
// it takes ftsMu. It is a deterministic test seam for interleaving a newer
// mutation between an older publisher's snapshot capture and its write; nil in
// production.
var pastBeforePublishFTS func()

// ftsRowEqual reports whether two entries write identical content to
// past_sessions_fts. sort_rank is deliberately not compared: a row whose only
// change is its sorted position keeps its stored rank. The mirror's internal
// ORDER BY (searchFTS) is not load-bearing — Search re-sorts the FTS ∪ memory
// union by the in-memory index (mergeSearchResults) — so leaving a shifted row
// untouched is what keeps a front insert or a delete from rewriting the tail.
func ftsRowEqual(a, b PastEntry) bool {
	return a.ID == b.ID &&
		a.Meta.Name == b.Meta.Name &&
		a.Meta.OriginalPrompt == b.Meta.OriginalPrompt &&
		a.Meta.EnvInfo.WorkingDir == b.Meta.EnvInfo.WorkingDir &&
		a.StateDir == b.StateDir
}

// ftsDeleteChunk bounds how many ids a single IN (...) delete names, staying
// well under SQLite's bound-parameter limit while collapsing many removals into
// one table scan.
const ftsDeleteChunk = 500

// errFTSBaselineStale signals that the mirror no longer matches the snapshot the
// delta was computed against: its DB file was deleted or replaced outside the
// index. The delta cannot repair that (it would insert only the changed rows and
// leave the rest missing or foreign), so publishFTS falls back to a full
// rebuildFTS, which is what made the old whole-table rewrite self-healing here.
var errFTSBaselineStale = errors.New("fts mirror does not match the published snapshot")

// rewriteFTSDelta transforms the FTS table from prev to next by touching only
// the rows that differ: removed ids are deleted, and new or content-changed ids
// are (re)inserted at their next-snapshot rank. Rows unchanged in both id and
// mirrored content — including rows that merely shifted rank — are left alone.
//
// It assumes the table currently reflects prev, which publishFTS guarantees by
// serializing on ftsMu, and verifies that inside the transaction two ways: the
// row count must equal len(prev), and the DB must carry the
// past_sessions_fts_state (owner, seq) token writeFTSTx stamped on the last
// successful write. The token is what catches a replaced or restored index.db
// whose row count happens to match but whose rows/ids differ — a count-only
// check would accept it, skip the "unchanged" ids, and leave foreign text in a
// mirror marked healthy. Either mismatch returns errFTSBaselineStale for
// publishFTS to repair with a rewrite.
func (i *PastIndex) rewriteFTSDelta(prev, next []PastEntry) error {
	prevIdx := make(map[string]int, len(prev))
	for k, e := range prev {
		prevIdx[e.ID] = k
	}
	nextIDs := make(map[string]struct{}, len(next))
	var removals []string
	type insert struct {
		entry PastEntry
		rank  int
	}
	var inserts []insert
	for rank, e := range next {
		nextIDs[e.ID] = struct{}{}
		if k, ok := prevIdx[e.ID]; ok {
			if ftsRowEqual(prev[k], e) {
				continue
			}
			removals = append(removals, e.ID) // dropped and re-inserted with its new content
		}
		inserts = append(inserts, insert{entry: e, rank: rank})
	}
	for _, e := range prev {
		if _, ok := nextIDs[e.ID]; !ok {
			removals = append(removals, e.ID)
		}
	}
	return i.writeFTSTx(func(tx *sql.Tx) error {
		var rows int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM past_sessions_fts`).Scan(&rows); err != nil { //nolint:noctx
			return err
		}
		if rows != len(prev) {
			return errFTSBaselineStale
		}
		var owner, seq int64
		if err := tx.QueryRow(`SELECT owner, seq FROM past_sessions_fts_state`).Scan(&owner, &seq); err != nil { //nolint:noctx
			// No state row means a fresh, empty, or foreign DB: treat as stale
			// (a genuine read error will resurface on the rebuild fallback).
			if errors.Is(err, sql.ErrNoRows) {
				return errFTSBaselineStale
			}
			return err
		}
		if owner != i.ftsOwner || seq != i.lastSeq {
			return errFTSBaselineStale
		}
		if len(removals) == 0 && len(inserts) == 0 {
			return nil
		}
		for start := 0; start < len(removals); start += ftsDeleteChunk {
			end := min(start+ftsDeleteChunk, len(removals))
			chunk := removals[start:end]
			args := make([]any, len(chunk))
			for k, id := range chunk {
				args[k] = id
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
			if _, err := tx.Exec(`DELETE FROM past_sessions_fts WHERE id IN (`+placeholders+`)`, args...); err != nil { //nolint:noctx
				return err
			}
		}
		if len(inserts) == 0 {
			return nil
		}
		stmt, err := tx.Prepare(insertPastSessionsFTS) //nolint:noctx
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, in := range inserts {
			e := in.entry
			if _, err := stmt.Exec(e.ID, e.Meta.Name, e.Meta.OriginalPrompt, e.Meta.EnvInfo.WorkingDir, e.StateDir, in.rank); err != nil { //nolint:noctx
				return err
			}
		}
		return nil
	})
}

// writeFTSTx opens the FTS index, ensures its schema, runs fn inside one
// transaction, and commits and closes (then chmods the index files) before
// returning. Both the full rewrite and the incremental delta go through it so
// they share the open/schema/commit/close/chmod discipline.
func (i *PastIndex) writeFTSTx(fn func(*sql.Tx) error) error {
	dbDir := filepath.Dir(i.dbPath)
	if err := i.fs.MkdirAll(dbDir, 0o700); err != nil {
		return err
	}
	// Immediate transaction: the delta reads (row count, baseline token) before
	// its first write, and index.db is also written by the archive/favorite/pin
	// stores. Under the default deferred begin a writer committing in that
	// window invalidates our read snapshot and the write upgrade fails with
	// SQLITE_BUSY_SNAPSHOT (which busy_timeout does not cover); taking the write
	// lock up front avoids it.
	db, err := i.openDB("sqlite", sqliteDSNImmediate(i.dbPath))
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	// These are local, in-process SQLite file operations (modernc.org/sqlite);
	// PastIndex.Rebuild is a context-free API, so the non-Context variants are
	// used deliberately. (noctx)
	if _, err := db.Exec(createPastSessionsFTS); err != nil { //nolint:noctx
		return err
	}
	if _, err := db.Exec(createPastSessionsFTSState); err != nil { //nolint:noctx
		return err
	}
	tx, err := db.Begin() //nolint:noctx
	if err != nil {
		return err
	}
	// best-effort rollback; the Commit/Close path below owns the real error
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	// Stamp this instance's identity token into the state table so the next
	// delta can tell the FTS table is still the one this index wrote: a
	// replaced/restored file, or one written by another instance/run, carries a
	// different owner or an older seq. Bump the seq before the write so a failed
	// commit never leaves lastSeq claiming a token the file does not have.
	i.ftsSeq++
	if _, err := tx.Exec(`DELETE FROM past_sessions_fts_state`); err != nil { //nolint:noctx
		return err
	}
	if _, err := tx.Exec(`INSERT INTO past_sessions_fts_state(owner, seq) VALUES (?, ?)`, i.ftsOwner, i.ftsSeq); err != nil { //nolint:noctx
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	i.lastSeq = i.ftsSeq
	if err := db.Close(); err != nil {
		return err
	}
	closed = true
	return chmodSQLiteIndexFilesFS(i.fs, i.dbPath)
}

func (i *PastIndex) rebuildFTS(entries []PastEntry) error {
	return i.writeFTSTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM past_sessions_fts`); err != nil { //nolint:noctx
			return err
		}
		stmt, err := tx.Prepare(insertPastSessionsFTS) //nolint:noctx
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for rank, entry := range entries {
			if _, err := stmt.Exec(entry.ID, entry.Meta.Name, entry.Meta.OriginalPrompt, entry.Meta.EnvInfo.WorkingDir, entry.StateDir, rank); err != nil { //nolint:noctx
				return err
			}
		}
		return nil
	})
}

// chmodSQLiteIndexFiles chmods the SQLite index file and its sidecars on the OS
// filesystem. It is the direct-os entry point; chmodSQLiteIndexFilesFS is the
// filesystem-seam variant that production (rebuildFTS) drives through the
// index's injected fs.
func chmodSQLiteIndexFiles(dbPath string) error {
	return chmodSQLiteIndexFilesFS(afero.NewOsFs(), dbPath)
}

func chmodSQLiteIndexFilesFS(fs afero.Fs, dbPath string) error {
	for _, path := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"} {
		if err := fs.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (i *PastIndex) searchFTS(q string) ([]PastEntry, bool) {
	query := ftsQuery(q)
	if query == "" {
		return nil, false
	}
	i.mu.RLock()
	available := i.fts
	i.mu.RUnlock()
	if !available || i.dbPath == "" {
		return nil, false
	}
	db, err := i.openDB("sqlite", sqliteDSN(i.dbPath))
	if err != nil {
		return nil, false
	}
	defer func() { _ = db.Close() }()
	// local in-process SQLite query; PastIndex.Search is a context-free API. (noctx)
	rows, err := db.Query(`SELECT id FROM past_sessions_fts WHERE past_sessions_fts MATCH ? ORDER BY CAST(sort_rank AS INTEGER) ASC`, query) //nolint:noctx
	if err != nil {
		return nil, false
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]PastEntry, 0, len(ids))
	for _, id := range ids {
		if entry, ok := i.byID[id]; ok {
			out = append(out, entry)
		}
	}
	return out, true
}

func ftsQuery(q string) string {
	tokens := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		parts = append(parts, token+"*")
	}
	return strings.Join(parts, " AND ")
}

func matches(e PastEntry, lowerQ string) bool {
	if strings.Contains(strings.ToLower(e.Meta.Name), lowerQ) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Meta.OriginalPrompt), lowerQ) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Meta.ID), lowerQ) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Meta.EnvInfo.WorkingDir), lowerQ) {
		return true
	}
	return false
}

// SeedForTest replaces the in-memory index with the given metas (StateDir left
// blank). Test-only seam; production always goes through Rebuild.
func (i *PastIndex) SeedForTest(metas []schema.SessionMeta) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.all = i.all[:0]
	i.byID = map[string]PastEntry{}
	i.gen++
	for _, m := range metas {
		pe := PastEntry{ID: m.ID, Meta: m}
		i.all = append(i.all, pe)
		i.byID[m.ID] = pe
	}
	sort.SliceStable(i.all, func(a, b int) bool { return sessionMetaLess(i.all[a].Meta, i.all[b].Meta) })
}

// AllMetas returns the full snapshot of indexed metas. Caller must not mutate.
func (i *PastIndex) AllMetas() []schema.SessionMeta {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]schema.SessionMeta, 0, len(i.all))
	for _, e := range i.all {
		out = append(out, e.Meta)
	}
	return out
}

// RecentModels returns up to limit distinct (provider, model) pairs for the
// model picker's "Recent" group, ordered by global recency — the same
// most-recently-updated-first order the rest of the index uses
// (session_order.go's sessionMetaLess) — not scoped to any one project or
// harness. ProfileID is the provider instance name (mirrors
// appwire.ModelDescriptor.Provider); entries with a blank ProfileID or Model
// are skipped, as are delegate sessions (IsSubagent): a delegate's model is
// inherited or overridden at spawn, never chosen in the picker, so its pair
// is machinery noise rather than a recents signal — the same delegate
// exclusion RecentProjectDirs applies. Deduped on the pair's first (most
// recent) occurrence.
func (i *PastIndex) RecentModels(limit int) []appwire.ModelDescriptor {
	if limit <= 0 {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	seen := make(map[string]bool, limit)
	var out []appwire.ModelDescriptor
	for _, e := range i.all {
		if e.Meta.IsSubagent {
			continue
		}
		provider := strings.TrimSpace(e.Meta.ProfileID)
		model := strings.TrimSpace(e.Meta.Model)
		if provider == "" || model == "" {
			continue
		}
		key := provider + "/" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, appwire.ModelDescriptor{Provider: provider, Model: model})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// RecentProjectDirs returns up to limit distinct project working directories
// for the session creation flows' path dropdown (issue #35), ordered by
// actual recency of use: the index's most-recently-updated-first session
// order (session_order.go's sessionMetaLess — a session's UpdatedAt is its
// last activity moment), not directory mtime. Entries with a blank WorkingDir
// are skipped, as are entries whose WorkingDir no longer exists on disk
// (issue #50) — a deleted project directory would otherwise linger in the
// dropdown until it failed later at spawn/submit validation. Session
// machinery is excluded: an evener-managed worktree lane (WorktreeManaged —
// the delegate isolation and manage_worktree task lanes) has its WorkingDir
// replaced by the project it was entered from (WorktreeRestoreRoot, following
// EffectiveWorkingDir in tree.go, the spec §7 shared mechanism) when that is
// known, and is skipped when it is not; a delegate session (IsSubagent) is
// skipped too, because its dir is either its parent's project — which the
// parent session's own row already surfaces — or an isolation lane. That
// leaves one deliberate divergence from EffectiveWorkingDir: a worktree
// entered by path but not managed (WorktreePath set, WorktreeManaged false)
// keeps its raw path here, since recents answers "which dirs would you spawn
// into next" and that worktree is the user's own checkout, while §7's
// grouping/resume remaps it to its restore root. Deduped on the dir's first
// (most recent) occurrence.
func (i *PastIndex) RecentProjectDirs(limit int) []string {
	if limit <= 0 {
		return nil
	}
	// Collect every distinct candidate dir under the lock, then stat after
	// releasing it: os.Stat on a hung network mount can block indefinitely,
	// and holding even the read lock through that would wedge every index
	// writer. Collection cannot stop at limit dirs because the cap applies
	// after the existence filter — a deleted dir must not consume a slot.
	i.mu.RLock()
	seen := make(map[string]bool, len(i.all))
	candidates := make([]string, 0, len(i.all))
	for _, e := range i.all {
		dir := strings.TrimSpace(e.Meta.EnvInfo.WorkingDir)
		if e.Meta.IsSubagent {
			continue
		}
		if e.Meta.WorktreeManaged {
			dir = strings.TrimSpace(e.Meta.WorktreeRestoreRoot)
		}
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		candidates = append(candidates, dir)
	}
	i.mu.RUnlock()

	var out []string
	for _, dir := range candidates {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		out = append(out, dir)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// pastFindProbeAttempts bounds how many times Find re-probes when a Rebuild
// swaps the index during its probe (see foldOne): after the bound it reports a
// miss rather than a stale probe.
const pastFindProbeAttempts = 3

// Find returns the entry for a given session_id.
func (i *PastIndex) Find(sessionID string) (PastEntry, bool) {
	if identifier.ValidateSessionID(sessionID) != nil {
		return PastEntry{}, false
	}
	if e, ok := i.findCached(sessionID); ok {
		return e, true
	}
	if sessionID == "" || i.stateGlob == "" {
		return PastEntry{}, false
	}
	if i.afterFindCacheMiss != nil {
		i.afterFindCacheMiss()
	}
	// Hold a pin for the whole probe loop so unpinProbe cannot prune this id's
	// generation entry while a value captured below may still be compared.
	i.pinProbe(sessionID)
	defer i.unpinProbe(sessionID)
	for range pastFindProbeAttempts {
		i.mu.RLock()
		probeRebuildGen := i.rebuildGen
		probeIDGen := i.idGen[sessionID]
		i.mu.RUnlock()
		entry, found, determinate := i.probeOne(sessionID)
		if !found {
			if !determinate {
				// An indeterminate miss (corrupt meta, unlistable dir) is not
				// proof of deletion; never evict a valid cached row for it. A
				// concurrent fold or Rebuild may have indexed this id between the
				// top-level cache miss and this probe, so consult the index once
				// more and return a row it now holds: that only reads the cache,
				// it never evicts, so the indeterminate guarantee is untouched.
				if live, ok := i.findCached(sessionID); ok {
					return live, true
				}
				return PastEntry{}, false
			}
			// Evict only if this id's index state still matches what the probe
			// observed; a Rebuild swap, a concurrent fold, or another eviction may
			// have published a row the probe never saw (a session deleted and
			// recreated, or one a racing lookup just indexed), which deleting
			// would drop. Re-probe instead.
			if !i.evict(sessionID, probeRebuildGen, probeIDGen) {
				continue
			}
			return PastEntry{}, false
		}
		if i.afterFindProbe != nil {
			i.afterFindProbe()
		}
		if !i.foldOne(entry, probeRebuildGen, probeIDGen) {
			// A Rebuild swapped in a new index during the probe; its view is newer
			// than ours, so re-probe against it rather than guess deletion vs
			// creation.
			continue
		}
		// foldOne may have kept a strictly newer indexed row — a concurrent Rebuild
		// swapped it in after this Find's cache lookup missed, or another fold won
		// the id — so return what the index actually holds.
		if live, ok := i.findCached(sessionID); ok {
			return live, true
		}
		return PastEntry{}, false
	}
	// Contended on every attempt: report a miss rather than a stale probe.
	return PastEntry{}, false
}

// pinProbe marks an in-flight probe for id so its per-id generation fence cannot
// be pruned while a captured value may still be compared. The count lets
// concurrent probes for the same id coexist; the last one to unpin prunes.
func (i *PastIndex) pinProbe(id string) {
	i.mu.Lock()
	i.probePins[id]++
	i.mu.Unlock()
}

// unpinProbe releases a probe's pin. When it was the last in-flight probe for an
// id that is no longer indexed, the id's generation entry is dropped: with no
// probe left to compare against it the entry can never be observed again, and a
// future probe starts from a fresh generation. The id cannot be re-fenced
// incorrectly after pruning: an entry is dropped only when no probe remains to
// compare against it, and while any probe is in flight the entry survives
// untouched by pruning, so every captured value is compared against the very
// entry it was read from.
func (i *PastIndex) unpinProbe(id string) {
	i.mu.Lock()
	if i.probePins[id] <= 1 {
		delete(i.probePins, id)
		if _, ok := i.byID[id]; !ok {
			delete(i.idGen, id)
		}
	} else {
		i.probePins[id]--
	}
	i.mu.Unlock()
}

// evict removes an id the disk no longer holds, but only while this id's index
// state still matches what the caller's probe observed: a Rebuild swap or any
// other mutation of this id's row (a concurrent fold or update, or a prior
// eviction) between the check and this call means the row may be one the probe
// never saw, so it declines and the caller re-probes. Returns whether it acted.
func (i *PastIndex) evict(id string, expectedRebuildGen, expectedIDGen uint64) bool {
	i.mu.Lock()
	if i.rebuildGen != expectedRebuildGen || i.idGen[id] != expectedIDGen {
		i.mu.Unlock()
		return false
	}
	// Bump unconditionally, even when the row is absent: a cache-miss Find
	// reaches eviction with the row absent from byID, yet its invalidation must
	// still fence in-flight probes for this id.
	i.idGen[id]++
	if _, ok := i.byID[id]; !ok {
		i.mu.Unlock()
		return true
	}
	delete(i.byID, id)
	fresh := make([]PastEntry, 0, len(i.all))
	for _, e := range i.all {
		if e.ID != id {
			fresh = append(fresh, e)
		}
	}
	i.all = fresh
	i.gen++ // supersede any in-flight publisher (FTS + fingerprint)
	gen := i.gen
	all := append([]PastEntry(nil), i.all...)
	i.mu.Unlock()
	i.publishAndSignal(all, gen)
	return true
}

func (i *PastIndex) findCached(sessionID string) (PastEntry, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.byID[sessionID]
	return e, ok
}

// globBaseDir returns the directory portion of pattern before its first
// wildcard, or the pattern's own directory when it has none.
func globBaseDir(pattern string) string {
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		return filepath.Dir(pattern[:i])
	}
	return filepath.Dir(pattern)
}

// probeOne looks for one session's meta across every project the glob
// matches, reading only that session's meta.json per project — the same file
// and decode Rebuild's list path reads — instead of decoding every meta on
// disk. Like Rebuild's byID map, a session present in several projects
// resolves to the LAST project in glob order.
//
// The bool pair distinguishes an authoritative absence (every matched project
// was listable and its meta read as not-exist) from an indeterminate miss (a
// corrupt meta file, an unlistable sessions dir, or an invalid project id was
// skipped) — the same paths Rebuild skips and reports. Find may evict a cached
// row on the former but must not on the latter, so a transient read error cannot
// drop a valid session.
func (i *PastIndex) probeOne(sessionID string) (PastEntry, bool, bool) {
	matches, err := filepath.Glob(i.stateGlob)
	if err != nil {
		return PastEntry{}, false, false
	}
	determinate := true
	// filepath.Glob swallows directory read errors and returns no matches, which
	// would otherwise look like an authoritative "no such session". Verify the
	// glob's base directory is readable so a transiently inaccessible projects
	// root is treated as indeterminate rather than evicting valid cached rows.
	// A base that does not exist is a definite absence, not an indeterminate
	// one: only a non-IsNotExist read error (EACCES, EIO) is indeterminate.
	if base := globBaseDir(i.stateGlob); base != "" {
		if _, err := os.ReadDir(base); err != nil && !os.IsNotExist(err) {
			determinate = false
		}
	}
	var found PastEntry
	for _, project := range matches {
		if identifier.ValidateProjectID(filepath.Base(project)) != nil {
			determinate = false
			continue
		}
		// Rebuild's gate, shared rather than copied: ListSessionMetas skips
		// a project whose sessions dir cannot be listed (a traversable but
		// unlistable dir fails the list while reading a meta by its known
		// path still succeeds — without the gate, Find would fold a session
		// Rebuild keeps dropping, flapping the index every cycle). The
		// OS fs matches the filesystem LoadSessionMeta below reads through;
		// PastIndex.fs is a different seam (FTS scaffolding).
		if !schema.SessionsDirListable(afero.NewOsFs(), project) {
			determinate = false
			continue
		}
		meta, err := schema.LoadSessionMeta(project, sessionID)
		if err != nil {
			// A missing meta is a conclusive "absent here"; any other read or
			// decode error leaves the project's contribution unknown.
			if !errors.Is(err, os.ErrNotExist) {
				determinate = false
			}
			continue
		}
		found = PastEntry{ID: sessionID, Meta: meta, StateDir: project}
	}
	return found, found.ID != "", determinate
}

// foldOne inserts a probed entry into the in-memory index with the same
// allocate-then-swap discipline and publish tail as UpdateMeta (the sorted
// slice Rebuild may still be reading must not be mutated in place); the one
// difference is that it inserts an id the index has never seen, where
// UpdateMeta replaces an existing one.
//
// probeRebuildGen and probeIDGen are the index's rebuildGen and the probed id's
// idGen when the probe read the session. A Rebuild that swapped in a new index
// since that read leaves the probe's view ambiguous (the swap either dropped the
// session or listed its project before the session existed), and a mutation of
// this id since the read — an eviction proving the disk no longer holds it, or a
// concurrent fold/update that already indexed it — means the probe's view is
// superseded; either way foldOne declines (returns false) and the caller
// re-probes the disk. Checking both under this same lock keeps the decision
// atomic with the insert. Returns true when the entry was folded or an
// at-least-as-fresh indexed row was kept.
func (i *PastIndex) foldOne(entry PastEntry, probeRebuildGen, probeIDGen uint64) bool {
	i.mu.Lock()
	if i.rebuildGen != probeRebuildGen || i.idGen[entry.ID] != probeIDGen {
		i.mu.Unlock()
		return false
	}
	old, ok := i.byID[entry.ID]
	if ok && !metaNewer(entry.Meta, old.Meta) {
		// A concurrent Rebuild indexed it first and its row is at least as fresh
		// as the probe's; keep it. The reverse — the scan read v1, an external
		// writer then bumped the meta to v2, and the probe read v2 — must not keep
		// the stale scanned row, so the freshness check falls through to replace.
		i.mu.Unlock()
		return true
	}
	i.byID[entry.ID] = entry
	fresh := make([]PastEntry, 0, len(i.all)+1)
	for _, e := range i.all {
		if e.ID != entry.ID {
			fresh = append(fresh, e)
		}
	}
	fresh = insertSorted(fresh, entry)
	i.all = fresh
	all := append([]PastEntry(nil), i.all...)
	i.idGen[entry.ID]++
	i.gen++
	gen := i.gen
	i.mu.Unlock()
	i.publishAndSignal(all, gen)
	return true
}

// metaNewer reports whether a is a newer revision of the same session than b.
// Revision, bumped on every save by schema.saveSessionMetaLocked, is the
// authoritative order: a fork tag (ForkLabel) or an ObservedBy append re-saves
// without advancing UpdatedAt or NameUpdatedAt, so timestamps alone cannot
// order those. Timestamps remain the fallback for metas written before Revision
// existed, and an undecidable tie returns false so foldOne keeps the indexed row
// rather than clobbering it with a probe whose order cannot be established.
func metaNewer(a, b schema.SessionMeta) bool {
	// Revision orders two revisioned rows. A row written before the field
	// existed has Revision 0 and no revision order, so it must fall back to
	// timestamps rather than lose to every revisioned row.
	if a.Revision != 0 && b.Revision != 0 && a.Revision != b.Revision {
		return a.Revision > b.Revision
	}
	if a.UpdatedAt.After(b.UpdatedAt) {
		return true
	}
	if b.UpdatedAt.After(a.UpdatedAt) {
		return false
	}
	if a.NameUpdatedAt.After(b.NameUpdatedAt) {
		return true
	}
	if b.NameUpdatedAt.After(a.NameUpdatedAt) {
		return false
	}
	// Timestamps tie. A mixed pair (Revision 0 vs nonzero) is a legacy row and
	// its first re-save — the revisioned side is the newer version. Any other
	// tie carries no order, so keep the indexed row rather than clobber it.
	if a.Revision != b.Revision {
		return a.Revision != 0
	}
	return false
}
