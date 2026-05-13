package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"primeradiant.com/serf/agent"

	_ "modernc.org/sqlite"
)

// PastEntry is one indexed past session.
type PastEntry struct {
	ID       string
	Meta     agent.SessionMeta
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
	for _, project := range matches {
		metas, err := agent.ListSessionMetas(project)
		if err != nil {
			continue
		}
		for _, m := range metas {
			pe := PastEntry{ID: m.ID, Meta: m, StateDir: project}
			all = append(all, pe)
			byID[m.ID] = pe
		}
	}
	sort.SliceStable(all, func(a, b int) bool {
		return sessionMetaLess(all[a].Meta, all[b].Meta)
	})
	i.mu.Lock()
	i.all = all
	i.byID = byID
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

// Search returns the limit results starting at offset whose
// OriginalPrompt, ID, or WorkingDir contains q (case-insensitive). Empty q
// returns all entries.
func (i *PastIndex) Search(q string, limit, offset int) []PastEntry {
	if strings.TrimSpace(q) != "" {
		if out, ok := i.searchFTS(q, limit, offset); ok {
			if len(out) > 0 {
				return out
			}
		}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()

	q = strings.ToLower(strings.TrimSpace(q))
	out := []PastEntry{}
	for _, e := range i.all {
		if q == "" || matches(e, q) {
			out = append(out, e)
		}
	}
	if offset >= len(out) {
		return nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end]
}

func (i *PastIndex) rebuildFTS(entries []PastEntry) error {
	if err := os.MkdirAll(filepath.Dir(i.dbPath), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", i.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS past_sessions_fts USING fts5(
id,
original_prompt,
working_dir,
state_dir UNINDEXED,
sort_rank UNINDEXED
)`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM past_sessions_fts`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO past_sessions_fts(id, original_prompt, working_dir, state_dir, sort_rank) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for rank, entry := range entries {
		if _, err := stmt.Exec(entry.ID, entry.Meta.OriginalPrompt, entry.Meta.EnvInfo.WorkingDir, entry.StateDir, rank); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (i *PastIndex) searchFTS(q string, limit, offset int) ([]PastEntry, bool) {
	query := ftsQuery(q)
	if query == "" || limit <= 0 {
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
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM past_sessions_fts WHERE past_sessions_fts MATCH ? ORDER BY CAST(sort_rank AS INTEGER) ASC LIMIT ? OFFSET ?`, query, limit, offset)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
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
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
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
func (i *PastIndex) AllMetas() []agent.SessionMeta {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]agent.SessionMeta, 0, len(i.all))
	for _, e := range i.all {
		out = append(out, e.Meta)
	}
	return out
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
