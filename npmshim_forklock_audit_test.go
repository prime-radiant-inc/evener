package evener_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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

	// No raw executable write may bypass the helper — in ANY spelling, since
	// a mode a human cannot read at a glance is exactly the bypass the
	// audit refuses to wave through. os.WriteFile with any non-literal mode
	// (a variable, a fs.FileMode cast, a helper call) fails loudly rather
	// than silently passing, per the refuse-unreadable convention the
	// recursive-delete audits use.
	if raw := rawWritePath(shimEnv); raw != "" {
		t.Errorf("npmShimEnv writes via os.%s instead of writeExecutable: "+
			"any direct write bypasses the ForkLock guard (golang/go#22315); "+
			"route it through writeExecutable", raw)
	}
	for _, w := range rawWriteFileCalls(shimEnv) {
		lit, ok := w.Args[len(w.Args)-1].(*ast.BasicLit)
		if !ok {
			t.Errorf("npmShimEnv's os.WriteFile passes a non-literal mode: " +
				"an unreadable mode is refused by this audit — write it as 0o755 or use writeExecutable")
			continue
		}
		mode, err := strconv.ParseInt(lit.Value, 0, 32)
		if err != nil || mode&0o111 == 0 {
			continue
		}
		t.Errorf("npmShimEnv's os.WriteFile uses executable mode %s: "+
			"an executable written raw bypasses the ForkLock guard (golang/go#22315); use writeExecutable", lit.Value)
	}

	// The guard itself: writeExecutable must hold ForkLock across its write.
	// Routing the call through a helper whose lock was deleted is the
	// silent-failure mode this half exists for.
	writer := funcDecl(t, file, "writeExecutable", "install_test.go")
	writeStmt, ok := writeExecutableWriteStmt(writer)
	if !ok {
		t.Fatal("writeExecutable no longer performs an os.WriteFile: " +
			"the helper this audit routes the npm shim through has lost its write — " +
			"update the audit alongside whatever replaced it")
	}
	if !holdsForkLockAcross(writer, writeStmt) {
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

// rawWritePath returns the os write-call name npmShimEnv uses directly —
// WriteFile, Create, OpenFile, CreateTemp — or "" when it uses none. The
// fake-bin directory's only content is writeExecutable output; ANY direct
// os write there is a bypass worth naming, regardless of mode.
func rawWritePath(fn *ast.FuncDecl) string {
	for _, name := range []string{"WriteFile", "Create", "OpenFile", "CreateTemp"} {
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
// from an expression mentioning the npm path.
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
// call — the assignment form `if err := os.WriteFile(...); err != nil` nests
// the call inside an IfStmt, so the search walks statements rather than
// expecting a bare expression statement.
func writeExecutableWriteStmt(fn *ast.FuncDecl) (ast.Stmt, bool) {
	if fn.Body == nil {
		return nil, false
	}
	var found ast.Stmt
	for _, stmt := range fn.Body.List {
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
				found = stmt
				return false
			}
			return true
		})
		if found != nil {
			return found, true
		}
	}
	return nil, false
}

// holdsForkLockAcross reports whether fn acquires the ForkLock read lock at
// the top level of its body before stmt, and releases it after — either an
// explicit RUnlock after the statement or a deferred one. An acquisition
// after the write, or a release before it, is not a guard.
func holdsForkLockAcross(fn *ast.FuncDecl, stmt ast.Stmt) bool {
	locked := false
	released := false
	for _, s := range fn.Body.List {
		if !locked {
			if isForkLockCall(s, "RLock") {
				locked = true
				continue
			}
			// A statement at lock depth cannot be the write we are auditing
			// unless the lock came first — which the loop above would have
			// caught by now.
			if s == stmt {
				return false
			}
			continue
		}
		if s == stmt {
			continue // the guarded write itself, between lock and unlock
		}
		if isForkLockCall(s, "RUnlock") {
			released = true
			continue
		}
		if isDeferForkLockCall(s, "RUnlock") {
			// A deferred release keeps the lock held past the write and
			// across the function's remainder — a valid guard.
			return true
		}
	}
	return locked && released
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
