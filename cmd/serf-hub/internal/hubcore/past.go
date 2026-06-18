package hubcore

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/schema"
)

// PastEntry is one indexed past session.
type PastEntry struct {
	ID       string
	Meta     schema.SessionMeta
	StateDir string // the project's state-dir root (parent of `sessions/`)
}

// PastIndex globs `<projectsRoot>/<sha>/sessions/*.meta.json` style paths
// (state_glob points at the projects-root pattern with a trailing wildcard).
// All metas are kept in memory; rebuild is lazy or scheduled.
type PastIndex struct {
	stateGlob string
	dbPath    string

	mu   sync.RWMutex
	all  []PastEntry // sorted by the Hub session ordering contract
	byID map[string]PastEntry
	fts  bool
	// observers maps a worker session id to the session ids of observer
	// subagents granted read on its work, folded from every local session's
	// durable jobs.jsonl watch-read-grants during Rebuild. This is the historical
	// on-disk source for observer auto-open — it does not depend on the forward
	// SessionMeta.ObservedBy stamp (empty on existing data). Like the metas, it is
	// rebuilt once per cycle, so it is at most one rebuild interval stale.
	observers map[string][]string
}

// NewPastIndex returns a PastIndex configured to glob projectGlob.
//
// projectGlob is a shell-style glob like
// "/Users/jesse/.local/state/serf/projects/*"
// — each match is treated as a state-dir root containing a `sessions/`
// subdirectory of meta files.
func NewPastIndex(projectGlob string) *PastIndex {
	return &PastIndex{
		stateGlob: projectGlob,
		byID:      make(map[string]PastEntry),
	}
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

// Rebuild scans every project under stateGlob and reloads the index.
func (i *PastIndex) Rebuild() error {
	if i.stateGlob == "" {
		return nil
	}
	matches, err := filepath.Glob(i.stateGlob)
	if err != nil {
		return err
	}
	var all []PastEntry
	byID := make(map[string]PastEntry)
	observers := make(map[string][]string)
	for _, project := range matches {
		metas, err := schema.ListSessionMetas(project)
		if err != nil {
			continue
		}
		for _, m := range metas {
			pe := PastEntry{ID: m.ID, Meta: m, StateDir: project}
			all = append(all, pe)
			byID[m.ID] = pe
		}
		foldProjectObserverGrants(observers, project)
	}
	sort.SliceStable(all, func(a, b int) bool {
		return sessionMetaLess(all[a].Meta, all[b].Meta)
	})
	dedupObserverLists(observers)
	i.mu.Lock()
	i.all = all
	i.byID = byID
	i.observers = observers
	i.fts = false
	i.mu.Unlock()
	if i.dbPath != "" {
		if err := i.rebuildFTS(all); err == nil {
			i.mu.Lock()
			i.fts = true
			i.mu.Unlock()
		}
	}
	return nil
}

// All returns the full index sorted by the Hub session ordering contract.
func (i *PastIndex) All() []PastEntry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]PastEntry, len(i.all))
	copy(out, i.all)
	return out
}

// Search returns the limit results starting at offset whose indexed text
// matches q. SQLite FTS contributes token-prefix matches when available; the
// in-memory scan preserves substring matches.
func (i *PastIndex) Search(q string, limit, offset int) []PastEntry {
	if strings.TrimSpace(q) != "" {
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
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
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

func (i *PastIndex) rebuildFTS(entries []PastEntry) error {
	dbDir := filepath.Dir(i.dbPath)
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", i.dbPath)
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
	tx, err := db.Begin() //nolint:noctx
	if err != nil {
		return err
	}
	// best-effort rollback; the Commit/Close path below owns the real error
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM past_sessions_fts`); err != nil { //nolint:noctx
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO past_sessions_fts(id, name, original_prompt, working_dir, state_dir, sort_rank) VALUES (?, ?, ?, ?, ?, ?)`) //nolint:noctx
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for rank, entry := range entries {
		if _, err := stmt.Exec(entry.ID, entry.Meta.Name, entry.Meta.OriginalPrompt, entry.Meta.EnvInfo.WorkingDir, entry.StateDir, rank); err != nil { //nolint:noctx
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	closed = true
	return chmodSQLiteIndexFiles(i.dbPath)
}

func chmodSQLiteIndexFiles(dbPath string) error {
	for _, path := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
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
	db, err := sql.Open("sqlite", i.dbPath)
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
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
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

// ObserversOf returns the session ids of observer subagents granted read on the
// given worker session, reconstructed from durable jobs.jsonl watch-read-grants
// at the last Rebuild. The result is at most one rebuild interval stale. It is
// the historical, on-disk observer source that does not rely on the forward
// SessionMeta.ObservedBy stamp. Caller must not mutate the returned slice.
func (i *PastIndex) ObserversOf(workerSessionID string) []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.observers[workerSessionID]
}

// foldProjectObserverGrants folds every local session's worker→observers grants
// under a project into the accumulating index. It scans the project's sessions/
// dir for per-session jobstore directories (each holding a jobs.jsonl) rather
// than keying off the meta list: the WATCHING session that minted a grant need
// not itself appear in the past index, but its log is the durable source of the
// link. Best-effort — a session with no log or an unreadable log contributes
// nothing rather than failing the rebuild. Lists may carry duplicates across
// sessions until dedupObserverLists.
func foldProjectObserverGrants(into map[string][]string, project string) {
	entries, err := os.ReadDir(filepath.Join(project, "sessions"))
	if err != nil {
		return // no sessions dir (or unreadable): nothing to fold
	}
	for _, e := range entries {
		// A session's jobstore is a directory <id>/ holding jobs.jsonl; the
		// session .meta.json files in the same sessions/ dir are skipped here.
		if !e.IsDir() {
			continue
		}
		perWorker, err := agent.LoadSessionObserverGrants(project, e.Name())
		if err != nil {
			continue
		}
		for worker, obs := range perWorker {
			into[worker] = append(into[worker], obs...)
		}
	}
}

// dedupObserverLists collapses duplicate observer ids per worker (the same
// (worker, observer) pair can be granted in more than one watching session's
// log) and sorts each list for a stable order.
func dedupObserverLists(observers map[string][]string) {
	for worker, obs := range observers {
		seen := make(map[string]bool, len(obs))
		deduped := obs[:0]
		for _, id := range obs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			deduped = append(deduped, id)
		}
		sort.Strings(deduped)
		observers[worker] = deduped
	}
}

// Find returns the entry for a given session_id.
func (i *PastIndex) Find(sessionID string) (PastEntry, bool) {
	if e, ok := i.findCached(sessionID); ok {
		return e, true
	}
	if sessionID == "" || i.stateGlob == "" {
		return PastEntry{}, false
	}
	if err := i.Rebuild(); err != nil {
		return PastEntry{}, false
	}
	return i.findCached(sessionID)
}

func (i *PastIndex) findCached(sessionID string) (PastEntry, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.byID[sessionID]
	return e, ok
}
