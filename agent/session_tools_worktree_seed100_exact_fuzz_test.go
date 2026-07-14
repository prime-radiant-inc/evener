//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
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
		if got, err := h.s.worktreeRootFor(h.s.currentEnv(), "", h.root); err != nil || got == "" {
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

	t.Run("injected boundaries", func(t *testing.T) {
		t.Run("control policy", func(t *testing.T) {
			h, _ := newWorktreeFaultSession(t)
			installWorktreeSeams(t, h.s, worktreeTestSeams{useControlPolicy: func(*execenv.LocalExecutionEnvironment, string) error {
				return errors.New("scripted control policy failure")
			}})
			if _, err := h.s.worktreeControlEnv(h.root); err == nil {
				t.Fatal("control environment accepted policy failure")
			}
			h.s.rollbackFreshDelegateWorktree("delegate", filepath.Join(h.root, "delegate"))
		})

		t.Run("sidecar write", func(t *testing.T) {
			h, _ := newWorktreeFaultSession(t)
			installWorktreeSeams(t, h.s, worktreeTestSeams{writeSidecar: func(string, string, worktree.Sidecar) error {
				return errors.New("scripted sidecar write failure")
			}})
			if _, err := h.exec(map[string]any{"operation": "create", "name": "write-failure"}); err == nil {
				t.Fatal("create accepted sidecar write failure")
			}
		})

		t.Run("enter create and external", func(t *testing.T) {
			for _, external := range []bool{false, true} {
				h, _ := newWorktreeFaultSession(t)
				var path string
				if external {
					path = h.addExternal("enter-failure")
				}
				installWorktreeSeams(t, h.s, worktreeTestSeams{enterWorktree: func(string, bool) error {
					return errors.New("scripted enter failure")
				}})
				var err error
				if external {
					_, err = h.s.worktreeSwitchByPath(context.Background(), path)
				} else {
					_, err = h.s.worktreeCreate(context.Background(), "enter-create", "")
				}
				if err == nil {
					t.Fatal("operation accepted enter failure")
				}
			}
		})

		t.Run("remove sidecar disposition", func(t *testing.T) {
			for _, deleteBranch := range []bool{false, true} {
				h, _ := newWorktreeFaultSession(t)
				name := map[bool]string{false: "update-failure", true: "delete-failure"}[deleteBranch]
				scriptedCreate(t, h, name)
				h.exitToRoot(t)
				seams := worktreeTestSeams{}
				if deleteBranch {
					seams.deleteSidecar = func(string, string) error { return errors.New("scripted delete failure") }
				} else {
					seams.updateSidecar = func(string, string, func(*worktree.Sidecar)) error { return errors.New("scripted update failure") }
				}
				installWorktreeSeams(t, h.s, seams)
				if _, err := h.s.worktreeRemove(context.Background(), name, false, false, deleteBranch); err == nil {
					t.Fatal("remove accepted sidecar disposition failure")
				}
			}
		})

		t.Run("prune sidecar disposition", func(t *testing.T) {
			t.Run("registered", func(t *testing.T) {
				h, _ := newWorktreeFaultSession(t)
				scriptedCreate(t, h, "prune-registered")
				h.exitToRoot(t)
				installWorktreeSeams(t, h.s, failingDeleteWorktreeSeam())
				if _, err := h.s.worktreePrune(context.Background()); err == nil {
					t.Fatal("prune accepted registered sidecar delete failure")
				}
			})
			for _, mode := range []string{"stale", "adopted", "branch"} {
				t.Run(mode, func(t *testing.T) {
					h, _ := newWorktreeFaultSession(t)
					name := "prune-" + mode
					switch mode {
					case "stale":
						worktreeFaultWriteSidecar(t, h, name, "base-sha", false, "", true)
					case "adopted":
						h.git.branches[name] = "new-tip"
						worktreeFaultWriteSidecar(t, h, name, "base-sha", true, "removed-tip", true)
					case "branch":
						h.git.branches[name] = "base-sha"
						worktreeFaultWriteSidecar(t, h, name, "base-sha", false, "", true)
					}
					installWorktreeSeams(t, h.s, failingDeleteWorktreeSeam())
					if _, err := h.s.worktreePrune(context.Background()); err == nil {
						t.Fatal("prune accepted reconciled sidecar delete failure")
					}
				})
			}
		})
	})
}

func installWorktreeSeams(t *testing.T, s *Session, seams worktreeTestSeams) {
	t.Helper()
	worktreeSeams.Store(s, seams)
	t.Cleanup(func() { worktreeSeams.Delete(s) })
}

func failingDeleteWorktreeSeam() worktreeTestSeams {
	return worktreeTestSeams{deleteSidecar: func(string, string) error {
		return errors.New("scripted sidecar delete failure")
	}}
}
