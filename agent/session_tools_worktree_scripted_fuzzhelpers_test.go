//go:build serffuzz

package agent

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"primeradiant.com/serf/agent/internal/worktree"
)

func (h *scriptedWorktreeSession) managedPath(name string) string {
	return filepath.Join(h.s.currentStateDir(), "worktrees", worktree.ProjectID(h.root), filepath.FromSlash(name))
}

func (h *scriptedWorktreeSession) metaDir() string {
	return metaDirForProject(filepath.Dir(h.managedPath("placeholder")))
}

func (h *scriptedWorktreeSession) addExternal(name string) string {
	h.t.Helper()
	path := filepath.Join(h.root, "external-"+name)
	if h.git.entry(path) != nil {
		return path
	}
	if _, exists := h.git.branches[name]; exists {
		h.t.Fatalf("scripted external branch %q already exists", name)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		h.t.Fatalf("create external worktree %q: %v", path, err)
	}
	gitDir := filepath.Join(h.root, ".git", "worktrees", "external-"+name)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		h.t.Fatalf("create external git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		h.t.Fatalf("write external .git pointer: %v", err)
	}
	h.git.branches[name] = "base-sha"
	h.git.entries[filepath.Clean(path)] = &scriptedWorktreeEntry{
		path: filepath.Clean(path), branch: name, head: "base-sha", managed: false,
	}
	return filepath.Clean(path)
}

func (h *scriptedWorktreeSession) hasRestoreEnv() bool {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return h.s.worktreeRestoreEnv != nil
}

func (h *scriptedWorktreeSession) currentState() (path string, managed bool) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return h.s.worktreeCurrentPath, h.s.worktreeCurrentManaged
}

func (h *scriptedWorktreeSession) exitToRoot(t *testing.T) {
	t.Helper()
	if !h.hasRestoreEnv() {
		h.requireAtRoot(t)
		return
	}
	if _, err := h.exec(map[string]any{"operation": "exit"}); err != nil {
		t.Fatalf("exit to root: %v", err)
	}
	h.requireAtRoot(t)
}

func (h *scriptedWorktreeSession) managedEntries() []*scriptedWorktreeEntry {
	entries := make([]*scriptedWorktreeEntry, 0, len(h.git.entries))
	for _, entry := range h.git.entries {
		if entry.managed {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries
}

func (h *scriptedWorktreeSession) inactiveManagedEntry() *scriptedWorktreeEntry {
	current, _ := h.currentState()
	for _, entry := range h.managedEntries() {
		if entry.path != current {
			return entry
		}
	}
	return nil
}

func (h *scriptedWorktreeSession) managedEntryByName(name string) *scriptedWorktreeEntry {
	for _, entry := range h.managedEntries() {
		if entry.branch == name {
			return entry
		}
	}
	return nil
}

func (h *scriptedWorktreeSession) requireNoSidecar(t *testing.T, name string) {
	t.Helper()
	if _, err := worktree.ReadSidecar(h.metaDir(), name); !os.IsNotExist(err) {
		t.Fatalf("sidecar %q survived an aborted create: %v", name, err)
	}
}

func (h *scriptedWorktreeSession) assertInvariants(t *testing.T) {
	t.Helper()
	wd := h.s.currentEnv().WorkingDirectory()
	current, managed := h.currentState()
	restore := h.hasRestoreEnv()
	if current == "" {
		if restore {
			t.Fatal("empty current worktree retained a restore environment")
		}
		if wd != h.root {
			t.Fatalf("empty worktree state has working directory %q, want root %q", wd, h.root)
		}
	} else {
		if wd != current {
			t.Fatalf("working directory = %q, current worktree = %q", wd, current)
		}
		if !restore {
			t.Fatalf("current worktree %q has no restore environment", current)
		}
		entry := h.git.entry(current)
		if entry == nil {
			t.Fatalf("current worktree %q is absent from scripted Git", current)
		}
		if entry.managed != managed {
			t.Fatalf("current worktree managed=%v, scripted Git says %v", managed, entry.managed)
		}
		if managed {
			h.requireOwnLock(t, current)
		}
	}

	for _, entry := range h.managedEntries() {
		sc, err := worktree.ReadSidecar(h.metaDir(), entry.branch)
		if err != nil {
			t.Fatalf("managed worktree %q has no readable sidecar: %v", entry.branch, err)
		}
		if sc.Name != entry.branch || sc.Branch != entry.branch || sc.BaseSHA != entry.head {
			t.Fatalf("sidecar for %q = %+v, want branch/base %q/%q", entry.branch, sc, entry.branch, entry.head)
		}
	}
}

func (h *scriptedWorktreeSession) requireListMatches(t *testing.T) {
	t.Helper()
	out, err := h.exec(map[string]any{"operation": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries, ok := out["entries"].([]map[string]any)
	if !ok {
		t.Fatalf("list entries = %T, want []map[string]any", out["entries"])
	}
	want := h.managedEntries()
	if len(entries) != len(want) {
		t.Fatalf("list returned %d entries, want %d", len(entries), len(want))
	}
	for i, entry := range want {
		if entries[i]["name"] != entry.branch || entries[i]["path"] != entry.path {
			t.Fatalf("list entry %d = %#v, want branch/path %q/%q", i, entries[i], entry.branch, entry.path)
		}
		if entries[i]["locked"] != (entry.lockReason != "") || entries[i]["lock_reason"] != entry.lockReason {
			t.Fatalf("list lock entry %q = (%v, %q), want (%v, %q)", entry.branch, entries[i]["locked"], entries[i]["lock_reason"], entry.lockReason != "", entry.lockReason)
		}
	}
}
