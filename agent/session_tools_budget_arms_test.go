package agent

// Arm-selection tests for the optional execenv budget capabilities. Each
// capability must be asserted on its own: the glob tool's budgeted arm and
// list_dir's budgeted walk are independent, so an environment that implements
// only one must still take that one while the other tool falls back. Merging
// the two into a single multi-method interface would silently drop both
// budgeted paths for such a double, which these tests catch at the tool
// boundary rather than inside execenv.

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
)

// globBudgetOnlyEnv implements execenv.GlobBudgeter and nothing else: its
// ListDirectory is the plain ExecutionEnvironment entry the fallback calls.
type globBudgetOnlyEnv struct {
	agenttest.FakeEnv
	globBudgetCalls int
	legacyListCalls int
}

func (e *globBudgetOnlyEnv) GlobWithBudget(_ context.Context, _, _ string, _ bool, _ *execenv.GlobBudget) ([]string, int, error) {
	e.globBudgetCalls++
	return nil, 0, nil
}

func (e *globBudgetOnlyEnv) ListDirectory(_ string, _ int) ([]execenv.DirEntry, error) {
	e.legacyListCalls++
	return nil, nil
}

// dirListBudgetOnlyEnv implements execenv.DirListBudgeter and nothing else:
// its Glob is the plain ExecutionEnvironment entry the fallback calls.
type dirListBudgetOnlyEnv struct {
	agenttest.FakeEnv
	plainGlobCalls  int
	listBudgetCalls int
}

func (e *dirListBudgetOnlyEnv) ListDirectoryWithBudget(_ context.Context, _ string, _ int, _ *execenv.DirListBudget) ([]execenv.DirEntry, error) {
	e.listBudgetCalls++
	return nil, nil
}

func (e *dirListBudgetOnlyEnv) Glob(_ context.Context, _, _ string, _ ...bool) ([]string, error) {
	e.plainGlobCalls++
	return nil, nil
}

// TestGlobAndListDirBudgetsAreIndependentArms proves each budget capability
// selects its own tool arm: an environment implementing only one of them takes
// the budgeted arm for that tool and the legacy path for the other, in both
// directions.
func TestGlobAndListDirBudgetsAreIndependentArms(t *testing.T) {
	t.Parallel()
	reg := w3sub_shellReg(t, nil)

	// GlobWithBudget only: glob is budgeted, list_dir walks the legacy path.
	globOnly := &globBudgetOnlyEnv{}
	if res := w3sub_call(t, reg, globOnly, "glob", map[string]any{"pattern": "*.go", "path": "."}); res.IsError {
		t.Fatalf("glob with a GlobBudgeter-only environment: %v", res.Output)
	}
	if res := w3sub_call(t, reg, globOnly, "list_dir", map[string]any{"path": ".", "depth": 1}); res.IsError {
		t.Fatalf("list_dir with a GlobBudgeter-only environment: %v", res.Output)
	}
	if globOnly.globBudgetCalls != 1 {
		t.Fatalf("glob called GlobWithBudget %d times, want 1: the budgeted arm must be selected on its own", globOnly.globBudgetCalls)
	}
	if globOnly.legacyListCalls != 1 {
		t.Fatalf("list_dir called the legacy ListDirectory %d times, want 1: it must not require the glob capability", globOnly.legacyListCalls)
	}

	// ListDirectoryWithBudget only: list_dir is budgeted, glob falls back.
	listOnly := &dirListBudgetOnlyEnv{}
	if res := w3sub_call(t, reg, listOnly, "glob", map[string]any{"pattern": "*.go", "path": "."}); res.IsError {
		t.Fatalf("glob with a DirListBudgeter-only environment: %v", res.Output)
	}
	if res := w3sub_call(t, reg, listOnly, "list_dir", map[string]any{"path": ".", "depth": 1}); res.IsError {
		t.Fatalf("list_dir with a DirListBudgeter-only environment: %v", res.Output)
	}
	if listOnly.plainGlobCalls != 1 {
		t.Fatalf("glob called the plain Glob %d times, want 1: it must not require the list capability", listOnly.plainGlobCalls)
	}
	if listOnly.listBudgetCalls != 1 {
		t.Fatalf("list_dir called ListDirectoryWithBudget %d times, want 1: the budgeted walk must be selected on its own", listOnly.listBudgetCalls)
	}
}
