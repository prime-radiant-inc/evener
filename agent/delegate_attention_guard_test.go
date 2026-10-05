package agent

import (
	"go/ast"
	"testing"
)

// The attention drive and a delegate send both refuse to start a generation on
// a child that is not quiescent. delegate_send asks subagent.startBlockedLocked
// (subagents.go); driveStableDelegateAttention used to list the same four flags
// inline, so the two busy lists could drift and one path would let a run start
// on a state the other refused. The drive also has attention-only extras
// (closed, fatalRunGated, the committed-send claim) layered on top, and it used
// to copy the drive-guard release that delegate_send already owns as
// releaseDriveGuard.
//
// This is a structural invariant, like the emit-lock guards in
// session_emit_lock_guard_test.go: no behavioral test distinguishes the shared
// predicate from an inline copy that happens to match today, so the drift
// prevention has to be pinned in the source.
func TestDriveStableDelegateAttentionSharesBusyPredicateAndRelease(t *testing.T) {
	t.Parallel()
	files := agentSourceFiles(t)
	attention := methodDecl(t, files, "Session", "driveStableDelegateAttention")

	blocked := assignedExpr(attention, "blocked")
	if blocked == nil {
		t.Fatal("driveStableDelegateAttention no longer assigns a `blocked` guard expression: the busy predicate can no longer be checked")
	}
	used := selectorNames(blocked)
	if !used["startBlockedLocked"] {
		t.Error("driveStableDelegateAttention's busy guard no longer routes through subagent.startBlockedLocked: " +
			"the attention and send busy lists can drift again")
	}
	for _, flag := range []string{"running", "driving", "finalizing", "disposeGated"} {
		if used[flag] {
			t.Errorf("driveStableDelegateAttention's busy guard inlines the shared flag %q instead of deferring to startBlockedLocked", flag)
		}
	}
	for _, extra := range []string{"closed", "fatalRunGated", "childCommittedSendStart"} {
		if !used[extra] {
			t.Errorf("driveStableDelegateAttention's busy guard no longer layers the attention-only %q on top of startBlockedLocked", extra)
		}
	}
	if !callsIdent(attention, "releaseDriveGuard") {
		t.Error("driveStableDelegateAttention no longer releases its drive guard through releaseDriveGuard: " +
			"the two release copies can drift")
	}
}

// assignedExpr returns the single-expression RHS of the top-level `name := expr`
// assignment in fn, or nil when there is none.
func assignedExpr(fn *ast.FuncDecl, name string) ast.Expr {
	var found ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != name {
			return true
		}
		found = assign.Rhs[0]
		return false
	})
	return found
}

// selectorNames returns every selector field name appearing in expr, e.g.
// `sub.closed` contributes "closed".
func selectorNames(expr ast.Expr) map[string]bool {
	names := make(map[string]bool)
	ast.Inspect(expr, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			names[sel.Sel.Name] = true
		}
		return true
	})
	return names
}

// callsIdent reports whether fn calls the named package-level function, i.e. a
// call whose callee is the bare identifier `name` rather than a method selector.
func callsIdent(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}
