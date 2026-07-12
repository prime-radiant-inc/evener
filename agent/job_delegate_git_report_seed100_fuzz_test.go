//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/sandbox"
)

// FuzzJobDelegateGitReportSeed100 exercises delegate revival ownership and
// terminal worktree reporting without starting Git or depending on a host repo.
func FuzzJobDelegateGitReportSeed100(f *testing.F) {
	for i := byte(0); i < 22; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		if op%22 < 11 {
			jdgr100Reacquire(t, op%11)
			return
		}
		jdgr100Report(t, op%11)
	})
}

func jdgr100Reacquire(t *testing.T, mode byte) {
	t.Helper()
	if mode == 0 {
		s := newTestSession(t)
		s.mu.Lock()
		s.env = &timeoutEnv{wd: t.TempDir()}
		s.mu.Unlock()
		if err := s.reacquireDelegateWorktreeLock("missing", "dlg"); err == nil || !strings.Contains(err.Error(), "local execution environment") {
			t.Fatalf("non-local revival error = %v", err)
		}
		return
	}

	h, lane := jdgr100Lane(t)
	entry := h.git.entry(lane)
	delegateID := filepath.Base(lane)
	switch mode {
	case 1:
		if err := h.s.reacquireDelegateWorktreeLock(filepath.Join(t.TempDir(), "missing"), delegateID); err == nil || !strings.Contains(err.Error(), "no longer part") {
			t.Fatalf("missing-repo revival error = %v", err)
		}
	case 2:
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(...string) (string, error) { return "", errors.New("lock-state failed") }
		}
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), "lock state could not be verified") {
			t.Fatalf("lock-state revival error = %v", err)
		}
	case 3:
		entry.lockReason = ""
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err != nil {
			t.Fatalf("unlocked revival: %v", err)
		}
		if want := worktree.FormatDelegateMarker(delegateID, h.s.id); entry.lockReason != want {
			t.Fatalf("revival marker = %q, want %q", entry.lockReason, want)
		}
	case 4:
		entry.lockReason = ""
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(args ...string) (string, error) {
				if len(args) > 1 && args[0] == "worktree" && args[1] == "lock" {
					return "", errors.New("lock denied")
				}
				return h.git.run(args...)
			}
		}
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), "failed to re-lock") {
			t.Fatalf("relock error = %v", err)
		}
	case 5:
		entry.lockReason = worktree.FormatDelegateMarker(delegateID, h.s.id)
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err != nil {
			t.Fatalf("own-lock adoption: %v", err)
		}
	case 6:
		entry.lockReason = "serf:another-session"
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), entry.lockReason) {
			t.Fatalf("foreign-lock refusal = %v", err)
		}
	case 7:
		entry.lockReason = "locked"
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), "refusing to revive") {
			t.Fatalf("unknown-lock refusal = %v", err)
		}
	case 8:
		local := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
		local.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite}
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), "cannot re-root") {
			t.Fatalf("sandbox re-root refusal = %v", err)
		}
	case 9:
		old := delegateWorktreeControlPolicy
		delegateWorktreeControlPolicy = func(*execenv.LocalExecutionEnvironment, string) error {
			return errors.New("control policy denied")
		}
		t.Cleanup(func() { delegateWorktreeControlPolicy = old })
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), "control policy denied") {
			t.Fatalf("control-policy refusal = %v", err)
		}
	case 10:
		entry.lockReason = "placeholder"
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(args ...string) (string, error) {
				out, err := h.git.run(args...)
				if scriptedArgs(args, "worktree", "list", "--porcelain") {
					out = strings.Replace(out, "locked placeholder\n", "locked\n", 1)
				}
				return out, err
			}
		}
		if err := h.s.reacquireDelegateWorktreeLock(lane, delegateID); err == nil || !strings.Contains(err.Error(), "an unknown owner") {
			t.Fatalf("reasonless-lock refusal = %v", err)
		}
	}
}

func jdgr100Report(t *testing.T, mode byte) {
	t.Helper()
	if mode == 0 {
		s := newTestSession(t)
		s.mu.Lock()
		s.env = &timeoutEnv{wd: t.TempDir()}
		s.mu.Unlock()
		if got := s.isolatedDelegateWorktreeReport(&jobstore.DelegateRestoreDescriptor{Isolation: "worktree", WorkingDir: t.TempDir()}); got != nil {
			t.Fatalf("non-local report = %#v, want nil", got)
		}
		return
	}

	h, lane := jdgr100Lane(t)
	desc := &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", WorkingDir: lane}
	switch mode {
	case 1:
		desc.WorkingDir = filepath.Join(t.TempDir(), "missing")
	case 2:
		desc.WorkingDir = filepath.Join(filepath.Dir(lane), "no-sidecar")
		if err := jdgr100CloneLaneShape(lane, desc.WorkingDir); err != nil {
			t.Fatal(err)
		}
	case 3, 4, 5:
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(args ...string) (string, error) {
				if mode == 3 && len(args) > 2 && args[2] == "status" {
					return "", errors.New("status failed")
				}
				if mode == 4 && len(args) > 2 && args[2] == "rev-list" {
					return "", errors.New("rev-list failed")
				}
				if mode == 5 && len(args) > 2 && args[2] == "rev-list" {
					return "not-a-count", nil
				}
				return h.git.run(args...)
			}
		}
	case 7:
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(args ...string) (string, error) {
				if len(args) > 2 && args[2] == "status" {
					return " M changed.go\n", nil
				}
				if len(args) > 2 && args[2] == "rev-list" {
					return "3\n", nil
				}
				return h.git.run(args...)
			}
		}
	case 8:
		local := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
		local.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite}
	case 9, 10:
		old := delegateWorktreeControlPolicy
		delegateWorktreeControlPolicy = func(*execenv.LocalExecutionEnvironment, string) error {
			return errors.New("control policy denied")
		}
		t.Cleanup(func() { delegateWorktreeControlPolicy = old })
	}

	got := h.s.isolatedDelegateWorktreeReport(desc)
	if mode <= 5 || mode >= 8 {
		if got != nil {
			t.Fatalf("failure-mode report = %#v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Fatal("valid lane report = nil")
	}
	if got.Path != lane || got.Branch != filepath.Base(lane) {
		t.Fatalf("report identity = %#v", got)
	}
	if mode == 7 && (got.Ahead != 3 || !got.Dirty) {
		t.Fatalf("dirty report = %#v, want ahead=3 dirty", got)
	}
	if mode == 6 && (got.Ahead != 0 || got.Dirty) {
		t.Fatalf("clean report = %#v, want ahead=0 clean", got)
	}
}

func jdgr100Lane(t *testing.T) (*scriptedWorktreeSession, string) {
	t.Helper()
	h := newScriptedWorktreeSession(t)
	out, err := h.exec(map[string]any{"operation": "create", "name": "dlg-report"})
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	return h, out["path"].(string)
}

func jdgr100CloneLaneShape(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(src, ".git"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, ".git"), raw, 0o644)
}
