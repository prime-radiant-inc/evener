//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
)

func FuzzWorktreeSeed100ExactProgram(f *testing.F) {
	f.Add([]byte{100})
	f.Fuzz(func(t *testing.T, _ []byte) {
		worktreeSeed100ExactProgram(t)
	})
}

func worktreeSeed100ExactProgram(t *testing.T) {
	t.Helper()

	t.Run("derived runtime root", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		if got := h.s.worktreeRootFor(h.s.currentEnv(), "", h.root); got == "" {
			t.Fatal("empty derived worktree root")
		}
	})

	t.Run("non repository guards", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		if err := os.RemoveAll(filepath.Join(h.root, ".git")); err != nil {
			t.Fatal(err)
		}
		active := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
		if _, err := h.s.worktreeCreateCore(context.Background(), active, "lane", "", 0, "marker", "create", nil); err == nil {
			t.Fatal("create outside repository succeeded")
		}
		if _, err := h.s.worktreeSwitchByName(context.Background(), "lane"); err == nil {
			t.Fatal("name switch outside repository succeeded")
		}
		if _, err := h.s.worktreeSwitchByPath(context.Background(), h.root); err == nil {
			t.Fatal("path switch outside repository succeeded")
		}
		if _, err := h.s.worktreeRemove(context.Background(), "lane", false, false, false); err == nil {
			t.Fatal("remove outside repository succeeded")
		}
		h.s.rollbackFreshDelegateWorktree("delegate", filepath.Join(h.root, "delegate"))
	})

	t.Run("managed switch by path", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		path := scriptedCreate(t, h, "managed-path")
		h.exitToRoot(t)
		if _, err := h.s.worktreeSwitchByPath(context.Background(), path); err != nil {
			t.Fatalf("managed path switch: %v", err)
		}
	})

	t.Run("path porcelain failure", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		external := h.addExternal("porcelain-failure")
		faults.listErr = errors.New("scripted list failure")
		if _, err := h.s.worktreeSwitchByPath(context.Background(), external); err == nil {
			t.Fatal("path switch accepted failed porcelain query")
		}
	})

	t.Run("exit control failure", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		path := scriptedCreate(t, h, "exit-control")
		faults.listErr = errors.New("scripted exit inspection failure")
		if _, err := h.s.worktreeExit(context.Background()); err == nil {
			t.Fatal("exit accepted failed lock inspection")
		}
		h.requireCurrent(t, path, true)
	})

	t.Run("restore relock failures", func(t *testing.T) {
		for _, removeCurrent := range []bool{false, true} {
			t.Run(map[bool]string{false: "exit", true: "remove"}[removeCurrent], func(t *testing.T) {
				h, faults := newWorktreeFaultSession(t)
				alpha := scriptedCreate(t, h, "restore-alpha")
				beta := scriptedCreate(t, h, "restore-beta")
				if _, err := h.exec(map[string]any{"operation": "switch", "name": "restore-alpha"}); err != nil {
					t.Fatal(err)
				}
				restore := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
				h.s.mu.Lock()
				h.s.worktreeRestoreEnv = restore
				h.s.mu.Unlock()
				if _, err := h.exec(map[string]any{"operation": "switch", "name": "restore-beta"}); err != nil {
					t.Fatal(err)
				}
				faults.lockErr[alpha] = errors.New("scripted restore lock failure")
				var err error
				if removeCurrent {
					_, err = h.s.worktreeRemove(context.Background(), "restore-beta", false, false, false)
				} else {
					_, err = h.s.worktreeExit(context.Background())
				}
				if err == nil {
					t.Fatal("restore accepted relock failure")
				}
				_ = beta
			})
		}
	})

	t.Run("remove invalid name", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		if _, err := h.s.worktreeRemove(context.Background(), "bad name", false, false, false); err == nil {
			t.Fatal("remove accepted invalid name")
		}
	})

	t.Run("sandboxed control failures", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		active := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
		active.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite}
		if _, err := h.s.worktreeCreateCore(context.Background(), active, "sandbox-create", "", 0, "marker", "create", nil); err == nil {
			t.Fatal("create accepted unavailable control sandbox")
		}
		h.s.mu.Lock()
		h.s.worktreeRestoreEnv = active
		h.s.worktreeCurrentPath = filepath.Join(h.root, "lane")
		h.s.mu.Unlock()
		if _, err := h.s.worktreeExit(context.Background()); err == nil {
			t.Fatal("exit accepted unavailable control sandbox")
		}
		h.s.rollbackFreshDelegateWorktree("delegate", filepath.Join(h.root, "delegate"))
	})
}
