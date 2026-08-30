package evener_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// This file pins the ForkLock discipline for the npm shim write in
// npmShimEnv (issue #270): the shim that make execs as `npm` must be written
// through writeExecutable, and writeExecutable must actually hold
// syscall.ForkLock across the write.
//
// The underlying race (golang/go#22315) is load-sensitive and not
// deterministically reproducible — it needs a sibling parallel test's fork
// to land inside the write's open-to-close window, leaving the child holding
// the inherited write fd that makes the kernel refuse the exec with ETXTBSY.
// So the invariant is held structurally, the same shape as
// agent/session_emit_lock_guard_test.go, which pins both halves of its
// guard: the call site routes through the guarded helper, AND the helper
// acquires the lock. The ETXTBSY retry in combinedOutputRetryingETXTBSY
// stays regardless: ForkLock is Go-runtime-local and gives no protection for
// grandchild execs (tsc written by `npm ci`, exec'd inside a make subtree).

// TestNpmShimWriteGoesThroughForkLock asserts that npmShimEnv writes its npm
// shim through writeExecutable rather than a raw os.WriteFile, and that
// writeExecutable holds ForkLock across the write it performs.
func TestNpmShimWriteGoesThroughForkLock(t *testing.T) {
	t.Parallel()

	file := parseRootTestFile(t, "install_test.go")
	shimEnv := funcDecl(t, file, "npmShimEnv", "install_test.go")

	calls := writeExecutableCalls(shimEnv)
	if len(calls) == 0 {
		t.Fatal("npmShimEnv no longer calls writeExecutable: the npm shim write has regressed to a raw write that holds no ForkLock, " +
			"and a sibling parallel test's fork can leave a child holding the shim's write fd (golang/go#22315) — " +
			"see writeExecutable's own comment for the mechanism")
	}

	// The guarded write must be the npm shim itself: some call mentions the
	// npm path — the "npm" string or the fakeBin directory, directly or
	// through one level of variable assignment (a benign hoist of the path
	// into a variable must not false-RED the audit).
	guarded := false
	for _, call := range calls {
		if callMentionsNpmPath(shimEnv, call) {
			guarded = true
		}
	}
	if !guarded {
		t.Error("no writeExecutable call in npmShimEnv mentions the npm path: " +
			"the npm shim is the file that needs the ForkLock guard (issue #270)")
	}

	// No raw EXECUTABLE write may bypass the helper. The hazard this audit
	// exists for is the exec'd file: only a write whose mode has an execute
	// bit needs the ForkLock guard, so a non-executable literal mode (a fake
	// .npmrc at 0o644) is fine to write directly — writeExecutable would
	// wrongly make it 0o755. Non-executable writes through the other os
	// write calls (Create/OpenFile/WriteFile) are still named, since those
	// do not carry a readable mode at all; only os.WriteFile's literal mode
	// can prove a write non-executable.
	for _, w := range rawWriteFileCalls(shimEnv) {
		lit, ok := w.Args[len(w.Args)-1].(*ast.BasicLit)
		if !ok {
			t.Errorf("npmShimEnv's os.WriteFile passes a non-literal mode: " +
				"an unreadable mode is refused by this audit — write it as a literal (0o755 for executables, 0o644 for configs)")
			continue
		}
		mode, err := strconv.ParseInt(lit.Value, 0, 32)
		if err != nil || mode&0o111 == 0 {
			continue // non-executable: no ForkLock guard needed
		}
		t.Errorf("npmShimEnv's os.WriteFile uses executable mode %s: "+
			"an executable written raw bypasses the ForkLock guard (golang/go#22315); use writeExecutable", lit.Value)
	}
	if raw := rawWritePathNonWriteFile(shimEnv); raw != "" {
		t.Errorf("npmShimEnv writes via os.%s instead of os.WriteFile or writeExecutable: "+
			"a write call with no readable mode cannot prove the file non-executable, "+
			"and an executable written raw bypasses the ForkLock guard (golang/go#22315)", raw)
	}

	// The guard itself: writeExecutable must hold ForkLock across its write.
	// Routing the call through a helper whose lock was deleted is the
	// silent-failure mode this half exists for. The pairing is checked in
	// the block that encloses the write — a retry loop that locks and
	// unlocks around each attempt is the idiomatic guard for a write that
	// may be retried, and its lock/unlock pair lives inside the loop body
	// beside the write, not before it at function top level.
	writer := funcDecl(t, file, "writeExecutable", "install_test.go")
	writeStmt, enclosing, ok := writeExecutableWriteStmt(writer)
	if !ok {
		t.Fatal("writeExecutable no longer performs an os.WriteFile: " +
			"the helper this audit routes the npm shim through has lost its write — " +
			"update the audit alongside whatever replaced it")
	}
	if !holdsForkLockAcross(writer, writeStmt, enclosing) {
		t.Error("writeExecutable no longer holds syscall.ForkLock across its os.WriteFile: " +
			"routing the npm shim write through the helper is defense-in-depth only if the lock is actually there — " +
			"a sibling parallel test's fork can leave a child holding the shim's write fd (golang/go#22315)")
	}
}

// writeExecutableCalls returns npmShimEnv's direct writeExecutable calls.
func writeExecutableCalls(fn *ast.FuncDecl) []*ast.CallExpr {
	if fn.Body == nil {
		return nil
	}
	var calls []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "writeExecutable" {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// rawWritePathNonWriteFile returns the modeless os write-call name
// npmShimEnv uses directly — Create, OpenFile, CreateTemp — or "" when it
// uses none. These calls cannot prove the written file non-executable the
// way os.WriteFile's literal mode can, so any direct use is named; the
// fake-bin directory's exec'd content belongs to writeExecutable.
func rawWritePathNonWriteFile(fn *ast.FuncDecl) string {
	for _, name := range []string{"Create", "OpenFile", "CreateTemp"} {
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
				found = true
			}
			return true
		})
		if found {
			return name
		}
	}
	return ""
}

// rawWriteFileCalls returns npmShimEnv's direct os.WriteFile calls, for the
// mode audit that follows the any-write refusal.
func rawWriteFileCalls(fn *ast.FuncDecl) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WriteFile" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// callMentionsNpmPath reports whether the writeExecutable call's path
// argument mentions the npm shim: the "npm" string or the fakeBin
// identifier directly, or through one level of assignment — the variable a
// call like `writeExecutable(t, npmPath, ...)` was hoisted into.
func callMentionsNpmPath(fn *ast.FuncDecl, call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if mentionsStringLiteral(arg, `"npm"`) || mentionsIdentifier(arg, "fakeBin") {
			return true
		}
	}
	for _, arg := range call.Args {
		ident, ok := arg.(*ast.Ident)
		if !ok {
			continue
		}
		if assignmentMentionsNpmPath(fn, ident.Name) {
			return true
		}
	}
	return false
}

// assignmentMentionsNpmPath reports whether fn assigns the named variable
// from an expression mentioning the npm path. Multi-value assignments
// (`v, ok := ...`) pair Lhs and Rhs by index; a mismatched shape simply does
// not match rather than indexing out of range.
func assignmentMentionsNpmPath(fn *ast.FuncDecl, name string) bool {
	mentions := false
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || ident.Name != name {
				continue
			}
			if i >= len(assign.Rhs) {
				continue
			}
			if mentionsStringLiteral(assign.Rhs[i], `"npm"`) || mentionsIdentifier(assign.Rhs[i], "fakeBin") {
				mentions = true
			}
		}
		return true
	})
	return mentions
}

// mentionsStringLiteral reports whether the expression mentions the given
// quoted string literal anywhere.
func mentionsStringLiteral(expr ast.Expr, quoted string) bool {
	mentioned := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == quoted {
			mentioned = true
		}
		return true
	})
	return mentioned
}

// mentionsIdentifier reports whether the expression mentions the named
// identifier anywhere.
func mentionsIdentifier(expr ast.Expr, name string) bool {
	mentioned := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
			mentioned = true
		}
		return true
	})
	return mentioned
}

// writeExecutableWriteStmt returns the statement holding fn's os.WriteFile
// call and the statement list of the block enclosing it — the assignment form
// `if err := os.WriteFile(...); err != nil` nests the call inside an IfStmt,
// and a retry loop nests it inside a ForStmt, so the search walks statements
// and reports the nesting level it found the write at. The enclosing list is
// where the write's lock/unlock pair must appear when the write is nested
// (a loop-scoped pair is the idiomatic guard for a retried write); a nil
// enclosing list means the write sits at fn's own top level.
func writeExecutableWriteStmt(fn *ast.FuncDecl) (write ast.Stmt, enclosing []ast.Stmt, ok bool) {
	if fn.Body == nil {
		return nil, nil, false
	}
	for _, stmt := range fn.Body.List {
		if stmtOSWriteFlat(stmt) != nil {
			// The write sits in this statement itself — the `if err :=
			// os.WriteFile(...); err != nil` form nests the call in the
			// IfStmt's init clause, at function top level.
			return stmt, nil, true
		}
		if inner, list := nestedOSWrite(stmt); inner != nil {
			// The write sits inside a nested block (a retry loop's body);
			// its lock/unlock pairing lives in that block's list.
			return inner, list, true
		}
	}
	return nil, nil, false
}

// stmtOSWriteFlat returns the statement's os.WriteFile call only when the
// call is NOT inside a nested block — a write inside a loop body belongs to
// the loop's scope, not the function's top level.
func stmtOSWriteFlat(stmt ast.Stmt) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if _, isBlock := n.(*ast.BlockStmt); isBlock {
			return false // do not descend into nested blocks
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WriteFile" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
			found = call
			return false
		}
		return true
	})
	return found
}

// nestedOSWrite returns the statement holding an os.WriteFile nested inside
// one of stmt's block statements, with that block's statement list — the
// pairing scope for the guard check. The IfStmt-init form is handled by the
// caller's flat branch, so only block-nested writes reach here.
func nestedOSWrite(stmt ast.Stmt) (write ast.Stmt, list []ast.Stmt) {
	var blocks []*ast.BlockStmt
	ast.Inspect(stmt, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStmt); ok {
			blocks = append(blocks, b)
		}
		return true
	})
	// Deepest-first: the write's true enclosing block is the innermost one.
	for _, block := range slices.Backward(blocks) {
		for _, s := range block.List {
			if stmtOSWrite(s) != nil {
				return s, block.List
			}
		}
	}
	return nil, nil
}

// stmtOSWrite returns the statement's os.WriteFile call, descending through
// nested blocks — used inside a block scope where the write's own nesting is
// already accounted for.
func stmtOSWrite(stmt ast.Stmt) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WriteFile" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
			found = call
			return false
		}
		return true
	})
	return found
}

// holdsForkLockAcross reports whether the statements before write in scope
// acquire the ForkLock read lock — plainly or via a deferred release — and
// never release it before the write. scope is the statement list the pairing
// is checked in: fn's top level when the write sits there, or the enclosing
// block's list (a retry loop's body) when the write is nested. The ordering
// is the whole point: a plain RUnlock anywhere before the write leaves the
// write unguarded no matter how many RLocks precede it (the release-before-
// write mutations — RLock/RUnlock/write, a sandwich re-lock after the write,
// and a plain RUnlock chasing a deferred one — must all fail).
func holdsForkLockAcross(fn *ast.FuncDecl, write ast.Stmt, scope []ast.Stmt) bool {
	list := fn.Body.List
	if scope != nil {
		list = scope
	}
	locked := false
	deferGuard := false
	for _, s := range list {
		if s == write {
			// Only the state at the write matters: the lock is held across
			// it from here until a plain RUnlock or function exit.
			return locked || deferGuard
		}
		switch {
		case isForkLockCall(s, "RLock"):
			locked = true
		case isForkLockCall(s, "RUnlock"):
			// A plain release before the write leaves it unguarded —
			// including the deferred-then-plain spelling, whose plain
			// release is this case.
			return false
		case isDeferForkLockCall(s, "RUnlock"):
			// A deferred release holds the lock past the write and the rest
			// of the function — a guard for everything after it.
			deferGuard = true
		default:
			if containsForkLockRelease(s) {
				// A release hidden inside a compound statement (a block, an
				// if, a loop) before the write is the same unguarded write.
				return false
			}
		}
	}
	return false
}

// containsForkLockRelease reports whether any statement nested inside s
// releases the ForkLock read lock. A release the flat walk cannot see is a
// release the ordering check must not wave through.
func containsForkLockRelease(s ast.Stmt) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		if found {
			return false
		}
		expr, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isForkLockSelector(call.Fun, "RUnlock") {
			found = true
			return false
		}
		return true
	})
	return found
}

// isForkLockCall reports whether stmt is `syscall.ForkLock.<method>()`.
func isForkLockCall(stmt ast.Stmt, method string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	return isForkLockSelector(call.Fun, method)
}

// isDeferForkLockCall reports whether stmt is `defer syscall.ForkLock.<method>()`.
func isDeferForkLockCall(stmt ast.Stmt, method string) bool {
	d, ok := stmt.(*ast.DeferStmt)
	if !ok {
		return false
	}
	return isForkLockSelector(d.Call.Fun, method)
}

// isForkLockSelector reports whether expr is `syscall.ForkLock.<method>`.
func isForkLockSelector(expr ast.Expr, method string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	field, ok := sel.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != "ForkLock" {
		return false
	}
	pkg, ok := field.X.(*ast.Ident)
	return ok && pkg.Name == "syscall"
}

// parseRootTestFile parses one of this package's test files by name. The
// audit checks the fixture builder at its source, so parsing the file — not
// compiling it — is the right instrument; a rename fails loudly below rather
// than silently passing.
func parseRootTestFile(t *testing.T, name string) *ast.File {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return file
}

// funcDecl returns the named plain function declaration, failing if absent —
// a renamed fixture builder must fail loudly rather than silently pass.
func funcDecl(t *testing.T, file *ast.File, name, filename string) *ast.FuncDecl {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Recv == nil {
			return fn
		}
	}
	t.Fatalf("no function %s in %s: the ForkLock audit it anchors can no longer be checked", name, filename)
	return nil
}
