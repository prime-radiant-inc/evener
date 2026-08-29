package evener_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file pins the ForkLock discipline for the npm shim write in
// npmShimEnv (issue #270): the shim that make execs as `npm` must be written
// through writeExecutable, which holds syscall.ForkLock across the write.
//
// The underlying race (golang/go#22315) is load-sensitive and not
// deterministically reproducible — it needs a sibling parallel test's fork
// to land inside the write's open-to-close window, leaving the child holding
// the inherited write fd that makes the kernel refuse the exec with ETXTBSY.
// So the invariant is held structurally, the same shape as
// agent/session_emit_lock_guard_test.go. The ETXTBSY retry in
// combinedOutputRetryingETXTBSY stays regardless: ForkLock is
// Go-runtime-local and gives no protection for grandchild execs (tsc
// written by `npm ci`, exec'd inside a make subtree).

// TestNpmShimWriteGoesThroughForkLock asserts that npmShimEnv writes its npm
// shim through writeExecutable rather than a raw os.WriteFile.
func TestNpmShimWriteGoesThroughForkLock(t *testing.T) {
	t.Parallel()

	file := parseRootTestFile(t, "install_test.go")
	shimEnv := funcDecl(t, file, "npmShimEnv")

	calls := writeExecutableCalls(shimEnv)
	if len(calls) == 0 {
		t.Fatal("npmShimEnv no longer calls writeExecutable: the npm shim write has regressed to a raw write that holds no ForkLock, " +
			"and a sibling parallel test's fork can leave a child holding the shim's write fd (golang/go#22315) — " +
			"see writeExecutable's own comment for the mechanism")
	}

	// The guarded write must be the npm shim itself, not some other file
	// written alongside a still-raw npm write.
	for _, call := range calls {
		if !callMentionsStringLiteral(call, `"npm"`) {
			t.Errorf("npmShimEnv calls writeExecutable without the npm path: " +
				"the npm shim is the file that needs the ForkLock guard (issue #270)")
		}
	}

	// And no raw executable-mode os.WriteFile may remain in its body.
	ast.Inspect(shimEnv, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WriteFile" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
			return true
		}
		if !writesExecutableMode(call) {
			return true
		}
		t.Errorf("npmShimEnv writes an executable with a raw os.WriteFile instead of writeExecutable: " +
			"a raw write holds no ForkLock, so a sibling fork can leave a child holding the shim's write fd (golang/go#22315)")
		return true
	})
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

// callMentionsStringLiteral reports whether the call mentions the given
// quoted string literal anywhere in its arguments.
func callMentionsStringLiteral(call *ast.CallExpr, quoted string) bool {
	mentioned := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == quoted {
				mentioned = true
			}
			return true
		})
	}
	return mentioned
}

// writesExecutableMode reports whether an os.WriteFile call passes an
// executable permission mode (any execute bit set). Go writes octal with an
// 0o prefix, which must be stripped before base-8 parsing — a raw %o scan
// stops at the "o" and reads the literal as 0.
func writesExecutableMode(call *ast.CallExpr) bool {
	if len(call.Args) != 3 {
		return false
	}
	lit, ok := call.Args[2].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return false
	}
	mode, err := strconv.ParseInt(strings.TrimPrefix(lit.Value, "0o"), 8, 32)
	if err != nil {
		return false
	}
	return mode&0o111 != 0
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
func funcDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Recv == nil {
			return fn
		}
	}
	t.Fatalf("no function %s in %s: the ForkLock audit it anchors can no longer be checked", name, file.Name.Name)
	return nil
}
