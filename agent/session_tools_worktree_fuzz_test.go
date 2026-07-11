//go:build serffuzz

package agent

import (
	"fmt"
	"os"
	"testing"
)

// FuzzWorktreeSessionProgram replays bounded worktree lifecycle programs
// through a real Session and registered manage_worktree tool. The harness
// supplies a scripted Git runner, so every create/switch/exit/list/remove/prune
// command stays offline while the production orchestration, sidecars, lock
// rules, and environment restore behavior remain real.
func FuzzWorktreeSessionProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 255},
		{1, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5},
		{10, 11, 0, 10, 11, 4, 9},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		scriptedWorktreeReplay(t, program)
	})
}

func scriptedWorktreeReplay(t *testing.T, program []byte) {
	t.Helper()
	h := newScriptedWorktreeSession(t)

	// A fixed baseline reaches the successful lifecycle choreography on every
	// replay. The byte program below then perturbs the same live state with
	// bounded valid and invalid operations, so corpus minimization cannot erase
	// the important lock/restore behavior.
	alpha := scriptedCreate(t, h, "alpha")
	h.requireCurrent(t, alpha, true)
	h.requireOwnLock(t, alpha)
	beta := scriptedCreate(t, h, "beta")
	h.requireCurrent(t, beta, true)
	h.requireUnlocked(t, alpha)
	h.requireOwnLock(t, beta)
	if _, err := h.exec(map[string]any{"operation": "switch", "name": "alpha"}); err != nil {
		t.Fatalf("baseline switch alpha: %v", err)
	}
	h.requireCurrent(t, alpha, true)
	h.requireOwnLock(t, alpha)
	h.requireUnlocked(t, beta)
	h.exitToRoot(t)
	h.assertInvariants(t)

	// Invalid names must fail before any Git call. This is an important safety
	// boundary: arbitrary model text cannot reach the process argv layer.
	callsBefore := len(h.git.calls)
	if _, err := h.exec(map[string]any{"operation": "create", "name": "bad name"}); err == nil {
		t.Fatal("invalid worktree name reached create success")
	}
	if got := len(h.git.calls); got != callsBefore {
		t.Fatalf("invalid name invoked scripted Git %d times, want %d", got, callsBefore)
	}
	h.assertInvariants(t)

	// A failed atomic add must remove its sidecar in the same call and leave the
	// session rooted where it started.
	failedName := "failed-add"
	h.git.failNextAdd = true
	if _, err := h.exec(map[string]any{"operation": "create", "name": failedName}); err == nil {
		t.Fatal("injected git worktree add failure succeeded")
	}
	if h.git.entry(h.managedPath(failedName)) != nil {
		t.Fatal("failed add left a registered scripted worktree")
	}
	h.requireNoSidecar(t, failedName)
	h.assertInvariants(t)
	h.requireListMatches(t)

	for step, raw := range program {
		if step == 24 {
			break
		}
		scriptedWorktreeStep(t, h, step, raw)
		h.assertInvariants(t)
	}

	// Exercise remove's full clean, merge-gated, branch-delete path on an
	// inactive lane, then prune every remaining unlocked lane. Both operations
	// execute against the same real sidecar and Session state as normal tools.
	h.exitToRoot(t)
	cleanupName := scriptedFreshName(h, "cleanup")
	cleanupPath := scriptedCreate(t, h, cleanupName)
	h.exitToRoot(t)
	removed, err := h.exec(map[string]any{
		"operation":     "remove",
		"name":          cleanupName,
		"delete_branch": true,
	})
	if err != nil {
		t.Fatalf("remove clean lane: %v", err)
	}
	if removed["branch_deleted"] != true {
		t.Fatalf("remove did not delete unchanged branch: %#v", removed)
	}
	if h.git.entry(cleanupPath) != nil {
		t.Fatalf("remove left scripted entry %q", cleanupPath)
	}
	if _, err := os.Stat(cleanupPath); !os.IsNotExist(err) {
		t.Fatalf("remove left worktree path %q: %v", cleanupPath, err)
	}
	h.requireNoSidecar(t, cleanupName)
	h.assertInvariants(t)

	if _, err := h.exec(map[string]any{"operation": "prune"}); err != nil {
		t.Fatalf("prune unlocked scripted lanes: %v", err)
	}
	h.requireAtRoot(t)
	h.assertInvariants(t)
	h.requireListMatches(t)
}

func scriptedCreate(t *testing.T, h *scriptedWorktreeSession, name string) string {
	t.Helper()
	out, err := h.exec(map[string]any{"operation": "create", "name": name})
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	path, ok := out["path"].(string)
	if !ok || path == "" {
		t.Fatalf("create %q path = %#v", name, out["path"])
	}
	if out["branch"] != name {
		t.Fatalf("create %q branch = %#v", name, out["branch"])
	}
	return path
}

func scriptedFreshName(h *scriptedWorktreeSession, prefix string) string {
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s-%d", prefix, i)
		if _, exists := h.git.branches[name]; !exists {
			return name
		}
	}
}

func scriptedWorktreeStep(t *testing.T, h *scriptedWorktreeSession, step int, raw byte) {
	t.Helper()
	names := []string{"alpha", "beta", "gamma", "nested/delta", "epsilon"}
	name := names[int(raw)%len(names)]

	switch raw % 12 {
	case 0:
		_, exists := h.git.branches[name]
		out, err := h.exec(map[string]any{"operation": "create", "name": name})
		if exists {
			if err == nil {
				t.Fatalf("duplicate create %q succeeded: %#v", name, out)
			}
			return
		}
		if err != nil {
			t.Fatalf("fresh create %q: %v", name, err)
		}
		if out["branch"] != name {
			t.Fatalf("fresh create %q branch = %#v", name, out["branch"])
		}
	case 1:
		entry := h.managedEntryByName(name)
		before := h.s.currentEnv().WorkingDirectory()
		out, err := h.exec(map[string]any{"operation": "switch", "name": name})
		if entry == nil {
			if err == nil {
				t.Fatalf("switch to unknown %q succeeded: %#v", name, out)
			}
			if got := h.s.currentEnv().WorkingDirectory(); got != before {
				t.Fatalf("unknown switch changed cwd to %q, want %q", got, before)
			}
			return
		}
		if err != nil {
			t.Fatalf("switch to known %q: %v", name, err)
		}
	case 2:
		wasInside := h.hasRestoreEnv()
		_, err := h.exec(map[string]any{"operation": "exit"})
		if wasInside && err != nil {
			t.Fatalf("exit from entered worktree: %v", err)
		}
		if !wasInside && err == nil {
			t.Fatal("exit from root succeeded")
		}
	case 3:
		external := h.addExternal("outside")
		if _, err := h.exec(map[string]any{"operation": "switch", "path": external}); err != nil {
			t.Fatalf("switch external worktree: %v", err)
		}
	case 4:
		h.requireListMatches(t)
	case 5:
		entry := h.inactiveManagedEntry()
		if entry == nil {
			return
		}
		foreign := "serf:foreign-" + fmt.Sprint(step)
		h.setLock(entry.path, foreign)
		before := h.s.currentEnv().WorkingDirectory()
		if _, err := h.exec(map[string]any{"operation": "switch", "name": entry.branch}); err == nil {
			t.Fatalf("foreign-lock switch to %q succeeded", entry.branch)
		}
		if got := h.s.currentEnv().WorkingDirectory(); got != before {
			t.Fatalf("foreign-lock refusal changed cwd to %q, want %q", got, before)
		}
		h.requireForeignLock(t, entry.path, foreign)
		h.setLock(entry.path, "")
	case 6:
		callsBefore := len(h.git.calls)
		if _, err := h.exec(map[string]any{"operation": "create", "name": "not valid"}); err == nil {
			t.Fatal("invalid create succeeded")
		}
		if got := len(h.git.calls); got != callsBefore {
			t.Fatalf("invalid create invoked scripted Git %d times, want %d", got, callsBefore)
		}
	case 7:
		failedName := scriptedFreshName(h, fmt.Sprintf("fault-%d", step))
		h.git.failNextAdd = true
		if _, err := h.exec(map[string]any{"operation": "create", "name": failedName}); err == nil {
			t.Fatalf("injected add failure for %q succeeded", failedName)
		}
		h.requireNoSidecar(t, failedName)
	case 8:
		entry := h.inactiveManagedEntry()
		if entry == nil {
			return
		}
		if _, err := h.exec(map[string]any{
			"operation":     "remove",
			"name":          entry.branch,
			"delete_branch": true,
		}); err != nil {
			t.Fatalf("remove inactive %q: %v", entry.branch, err)
		}
	case 9:
		h.exitToRoot(t)
		if _, err := h.exec(map[string]any{"operation": "prune"}); err != nil {
			t.Fatalf("prune: %v", err)
		}
	case 10:
		path, managed := h.currentState()
		if !managed {
			return
		}
		entry := h.git.entry(path)
		if entry == nil {
			t.Fatalf("current managed entry %q missing", path)
		}
		out, err := h.exec(map[string]any{"operation": "switch", "name": entry.branch})
		if err != nil || out["status"] != "unchanged" {
			t.Fatalf("switch current %q = (%#v, %v), want unchanged", entry.branch, out, err)
		}
	case 11:
		path, managed := h.currentState()
		if !managed {
			return
		}
		entry := h.git.entry(path)
		if entry == nil {
			t.Fatalf("current managed entry %q missing", path)
		}
		if _, err := h.exec(map[string]any{
			"operation":     "remove",
			"name":          entry.branch,
			"delete_branch": true,
		}); err != nil {
			t.Fatalf("remove current %q: %v", entry.branch, err)
		}
	}
}
