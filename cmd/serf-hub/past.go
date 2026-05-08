package main

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/serf/agent"
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

	mu      sync.RWMutex
	all     []PastEntry  // sorted by UpdatedAt desc
	byID    map[string]PastEntry
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
	sort.Slice(all, func(a, b int) bool {
		return all[a].Meta.UpdatedAt.After(all[b].Meta.UpdatedAt)
	})
	i.mu.Lock()
	i.all = all
	i.byID = byID
	i.mu.Unlock()
	return nil
}

// All returns the full index sorted by UpdatedAt desc (most recent first).
func (i *PastIndex) All() []PastEntry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]PastEntry, len(i.all))
	copy(out, i.all)
	return out
}

// Search returns the limit results starting at offset whose
// OriginalTask, ID, or WorkingDir contains q (case-insensitive). Empty q
// returns all entries.
func (i *PastIndex) Search(q string, limit, offset int) []PastEntry {
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

func matches(e PastEntry, lowerQ string) bool {
	if strings.Contains(strings.ToLower(e.Meta.OriginalTask), lowerQ) {
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

// Find returns the entry for a given session_id.
func (i *PastIndex) Find(sessionID string) (PastEntry, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.byID[sessionID]
	return e, ok
}
