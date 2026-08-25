package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

// ---------------------------------------------------------------------------
// isDelegateID
// ---------------------------------------------------------------------------

func TestIsDelegateID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"dlg_abc123", true},
		{"dlg_", true},
		{"dlg_034BxDnVFQ8wWCICfOqgk5", true},
		{"job_abc", false},
		{"abc123", false},
		{"", false},
		{"DLG_abc", false}, // case sensitive
		{" dlg_abc", false},
	}
	for _, tc := range tests {
		if got := isDelegateID(tc.id); got != tc.want {
			t.Errorf("isDelegateID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// laneWorktreePresent
// ---------------------------------------------------------------------------

func TestLaneWorktreePresent(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /somewhere"), 0644); err != nil {
			t.Fatalf("write .git: %v", err)
		}
		if !laneWorktreePresent(dir) {
			t.Fatalf("expected lane to be present")
		}
	})
	t.Run("absent", func(t *testing.T) {
		dir := t.TempDir()
		if laneWorktreePresent(dir) {
			t.Fatalf("expected lane to be absent")
		}
	})
	t.Run("nonexistent dir", func(t *testing.T) {
		if laneWorktreePresent("/nonexistent/path/that/does/not/exist") {
			t.Fatalf("expected false for nonexistent path")
		}
	})
}

// ---------------------------------------------------------------------------
// laneAheadCount
// ---------------------------------------------------------------------------

func TestLaneAheadCount(t *testing.T) {
	t.Run("valid count", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			if args[0] == "-C" && args[2] == "rev-list" && args[3] == "--count" {
				return "5\n", nil
			}
			return "", errors.New("unexpected args")
		}
		n, ok := laneAheadCount(worktree.GitRunner(run), "/path", "baseSHA")
		if !ok || n != 5 {
			t.Fatalf("n=%d ok=%v", n, ok)
		}
	})
	t.Run("zero count", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "0\n", nil
		}
		n, ok := laneAheadCount(worktree.GitRunner(run), "/path", "baseSHA")
		if !ok || n != 0 {
			t.Fatalf("n=%d ok=%v", n, ok)
		}
	})
	t.Run("git error", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "", errors.New("git failed")
		}
		n, ok := laneAheadCount(worktree.GitRunner(run), "/path", "baseSHA")
		if ok || n != 0 {
			t.Fatalf("n=%d ok=%v, want ok=false", n, ok)
		}
	})
	t.Run("non-numeric output", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "not-a-number\n", nil
		}
		n, ok := laneAheadCount(worktree.GitRunner(run), "/path", "baseSHA")
		if ok || n != 0 {
			t.Fatalf("n=%d ok=%v, want ok=false", n, ok)
		}
	})
}

// ---------------------------------------------------------------------------
// disposeAlreadyDisposedGone
// ---------------------------------------------------------------------------

func TestDisposeAlreadyDisposedGone(t *testing.T) {
	t.Run("not-exist error", func(t *testing.T) {
		result := disposeAlreadyDisposedGone("dlg_1", "/lane/path", os.ErrNotExist)
		if result.DelegateID != "dlg_1" {
			t.Fatalf("delegateID = %q", result.DelegateID)
		}
		if result.LanePath != "/lane/path" {
			t.Fatalf("lanePath = %q", result.LanePath)
		}
		if result.Branch != "dlg_1" {
			t.Fatalf("branch = %q", result.Branch)
		}
		if !result.AlreadyDisposed {
			t.Fatalf("expected alreadyDisposed=true")
		}
		if !strings.Contains(result.Message, "already gone") {
			t.Fatalf("message = %q", result.Message)
		}
	})
	t.Run("other error", func(t *testing.T) {
		result := disposeAlreadyDisposedGone("dlg_2", "/path", errors.New("permission denied"))
		if !result.AlreadyDisposed {
			t.Fatalf("expected alreadyDisposed=true")
		}
		if !strings.Contains(result.Message, "permission denied") {
			t.Fatalf("expected error message in: %q", result.Message)
		}
		if !strings.Contains(result.Message, "could not be read") {
			t.Fatalf("expected 'could not be read' in: %q", result.Message)
		}
	})
}

// ---------------------------------------------------------------------------
// metaDirForLane
// ---------------------------------------------------------------------------

func TestMetaDirForLane(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		result := metaDirForLane("/path/to/lane")
		if result != filepath.Join("/path/to", ".meta") { //nolint:gocritic // test needs absolute path
			t.Fatalf("metaDir = %q", result)
		}
	})
	t.Run("relative path", func(t *testing.T) {
		result := metaDirForLane("lane")
		expected := filepath.Join(".", ".meta")
		if result != expected {
			t.Fatalf("metaDir = %q, want %q", result, expected)
		}
	})
}

// ---------------------------------------------------------------------------
// WorktreeDisposeResult struct
// ---------------------------------------------------------------------------

func TestWorktreeDisposeResultStruct(t *testing.T) {
	result := WorktreeDisposeResult{
		DelegateID:      "dlg_1",
		LanePath:        "/path",
		Branch:          "dlg_1",
		AlreadyDisposed: true,
		Message:         "disposed",
	}
	if result.DelegateID != "dlg_1" || result.LanePath != "/path" || result.Branch != "dlg_1" {
		t.Fatalf("struct fields wrong: %+v", result)
	}
	if !result.AlreadyDisposed || result.Message != "disposed" {
		t.Fatalf("struct fields wrong: %+v", result)
	}
}

// ---------------------------------------------------------------------------
// stableWorktreeDisposalReason constant
// ---------------------------------------------------------------------------

func TestStableWorktreeDisposalReason(t *testing.T) {
	if stableWorktreeDisposalReason != "isolation_disposed" {
		t.Fatalf("stableWorktreeDisposalReason = %q", stableWorktreeDisposalReason)
	}
}

// ---------------------------------------------------------------------------
// subtreeWatchesTargeting with nil jobManager and no subagents
// ---------------------------------------------------------------------------

func TestSubtreeWatchesTargetingNoJobManager(t *testing.T) {
	s := &Session{
		subagents: &subagentManager{
			subs: map[string]*subagent{},
		},
	}
	// jobManager is nil, subagents is initialized but empty
	if s.subtreeWatchesTargeting("dlg_test") {
		t.Fatalf("expected false with no jobManager or subagents")
	}
}

// ---------------------------------------------------------------------------
// jobManager.watchesTargeting with no watches
// ---------------------------------------------------------------------------

func TestWatchesTargetingEmpty(t *testing.T) {
	jm := &jobManager{
		watches:       map[watchKey]*watchConfig{},
		terminalFlush: map[*watchConfig]bool{},
	}
	// Call watchesTargeting — it will lock jm.mu and iterate empty maps
	if jm.watchesTargeting("dlg_test") {
		t.Fatalf("expected false with empty watches")
	}
}

// ---------------------------------------------------------------------------
// delegateDisposeControlEnv: requires a local env (tested via the error path)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// laneAheadCount: whitespace trimming
// ---------------------------------------------------------------------------

func TestLaneAheadCountWhitespaceTrimmed(t *testing.T) {
	run := func(args ...string) (string, error) {
		return "  42  \n", nil
	}
	n, ok := laneAheadCount(worktree.GitRunner(run), "/path", "baseSHA")
	if !ok || n != 42 {
		t.Fatalf("n=%d ok=%v, want 42", n, ok)
	}
}

// ---------------------------------------------------------------------------
// disposeAlreadyDisposedGone: verifies Branch is set to the delegate id
// ---------------------------------------------------------------------------

func TestDisposeAlreadyDisposedGoneBranch(t *testing.T) {
	result := disposeAlreadyDisposedGone("dlg_branch_test", "/path", os.ErrNotExist)
	if result.Branch != "dlg_branch_test" {
		t.Fatalf("branch = %q, want 'dlg_branch_test'", result.Branch)
	}
}

// ---------------------------------------------------------------------------
// isDelegateID + worktreeDispose validation (empty id)
// ---------------------------------------------------------------------------

func TestWorktreeDisposeEmptyID(t *testing.T) {
	s := &Session{}
	// This will fail at beginDispose or earlier, depending on session state.
	// We can't easily call worktreeDispose without a valid Session, but
	// the validation for empty id happens before beginDispose.
	_, err := s.worktreeDispose(context.TODO(), "", false, false)
	if err == nil {
		t.Fatalf("expected error for empty id")
	}
	if !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("expected 'id is required' error, got %v", err)
	}
}

func TestWorktreeDisposeNonDelegateID(t *testing.T) {
	s := &Session{}
	_, err := s.worktreeDispose(context.TODO(), "not_a_delegate_id", false, false)
	if err == nil {
		t.Fatalf("expected error for non-delegate id")
	}
	if !strings.Contains(err.Error(), "not a delegate id") {
		t.Fatalf("expected 'not a delegate id' error, got %v", err)
	}
}

func TestWorktreeDisposeWhitespaceID(t *testing.T) {
	s := &Session{}
	// Whitespace-only id should be trimmed to empty
	_, err := s.worktreeDispose(context.TODO(), "   ", false, false)
	if err == nil {
		t.Fatalf("expected error for whitespace id")
	}
	if !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("expected 'id is required' after trim, got %v", err)
	}
}

func TestWorktreeDisposeValidDelegateID(t *testing.T) {
	s := &Session{}
	// A valid delegate id passes the id check but fails at disposeStableDelegateLane
	// since delegateController is nil
	_, err := s.worktreeDispose(context.TODO(), "dlg_abc123", false, false)
	if err == nil {
		t.Fatalf("expected error (session not initialized)")
	}
	// Should fail at delegateController nil check, not at id validation
	if strings.Contains(err.Error(), "id is required") || strings.Contains(err.Error(), "not a delegate id") {
		t.Fatalf("should not fail on id validation for valid delegate id: %v", err)
	}
	if !strings.Contains(err.Error(), "delegate controller is unavailable") {
		t.Fatalf("expected controller unavailable error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// fmt usage verification
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
