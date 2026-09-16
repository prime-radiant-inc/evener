package agent

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestSessionTestsWithAPerTestConfigHomeSkipTheLaunchSnapshot scans this
// package's test sources (agent/*_test.go, non-recursive) for a function that
// points the config home at a directory of its own — a Setenv of HOME or
// XDG_CONFIG_HOME, in its own body or in a test helper it calls — and then
// constructs a session (NewSession or a RestoreSessionFromMeta* entry point,
// directly or through test helpers) without skipping the launch-time
// environment snapshot.
//
// The snapshot forks `go env` through the session's execution environment
// (probeCapabilities). The go command records telemetry under
// os.UserConfigDir()/go/telemetry — $XDG_CONFIG_HOME on Linux,
// $HOME/Library/Application Support on macOS — and the first time it runs in a
// config home with no upload token it daemonizes a sidecar (Setsid, never
// waited) that keeps creating files there after `go env` has exited. The
// config home such a test hands out is a t.TempDir, so the sidecar races the
// TempDir RemoveAll cleanup: its MkdirAll re-creates a subdirectory the
// cleanup has already removed, the parent rmdir fails, and a test that passed
// reports "TempDir RemoveAll cleanup: unlinkat …: directory not empty"
// (#1527). Nothing can join that sidecar — it is its own session, outside the
// process table the exec environment reaps — so the one correct move is not
// to fork at all, which is what testConfig.skipGitSnapshot exists for: a test
// whose contract sits below the environment-snapshot layer must not fork for
// it.
//
// A function clears the audit when every session it constructs is gated by
// the value the constructor actually receives: a SessionConfig or
// RestoreSessionConfig whose testOnly.skipGitSnapshot is true at the call —
// written in the literal, or assigned to the variable by the last write before
// the call at the top level of the enclosing body (a write under a condition
// does not count, and a later false un-gates) — or, for a helper such as
// newSession, a withoutGitSnapshot() option or a gated config among the call's
// arguments, or a helper that gates every session it constructs itself.
func TestSessionTestsWithAPerTestConfigHomeSkipTheLaunchSnapshot(t *testing.T) {
	findings, err := configHomeForkAuditFindings(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("test(s) isolate the config home per test and construct a session whose launch snapshot forks `go env` into it:\n%s\n\n"+
			"Fix: pass testOnly: testConfig{skipGitSnapshot: true} in the SessionConfig the constructor receives (or "+
			"withoutGitSnapshot() to newSession) so the snapshot never forks go and go's "+
			"telemetry sidecar never outlives the test's TempDir.",
			strings.Join(findings, "\n"))
	}
}

// configHomeEnvVars are the variables os.UserConfigDir derives the config home
// from on the platforms CI runs: XDG_CONFIG_HOME on Linux, HOME on macOS.
var configHomeEnvVars = map[string]bool{"HOME": true, "XDG_CONFIG_HOME": true}

// sessionConstructors map each package entry point that takes the launch
// snapshot to the index of the argument carrying its testOnly gate; -1 marks
// an entry point that cannot be gated.
var sessionConstructors = map[string]int{
	"NewSession":                       3,
	"RestoreSessionFromMeta":           -1,
	"RestoreSessionFromMetaWithConfig": 4,
}

// snapshotGateOption is the newSession fixture option that sets the gate.
const snapshotGateOption = "withoutGitSnapshot"

// auditFunc is one function or method declared in the test files, with the
// bare names of everything its body calls.
type auditFunc struct {
	decl  *ast.FuncDecl
	calls map[string]bool
}

// configHomeForkAuditFindings reports every function in dir's test files that
// isolates the config home, reaches a session constructor, and leaves any
// session it constructs ungated, as "file:line name" lines in sorted order.
func configHomeForkAuditFindings(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var funcs []auditFunc
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs = append(funcs, auditFunc{decl: fn, calls: calledNames(fn.Body)})
			}
		}
	}

	// Both predicates are fixed points over the test files' own functions,
	// keyed by bare name: a name has the property when any declaration with
	// that name has it directly or calls a name that has it. Methods share the
	// namespace with functions; a callee name that is not a test-file
	// declaration is ignored.
	reaches := map[string]bool{}
	for name := range sessionConstructors {
		reaches[name] = true
	}
	closeOver(reaches, funcs, func(auditFunc) bool { return false })
	isolates := map[string]bool{}
	closeOver(isolates, funcs, func(f auditFunc) bool { return setsConfigHome(f.decl.Body) })

	// A helper gates on its own when every constructor-reaching call in its
	// body is gated; that too is a fixed point, since a helper may gate by
	// calling another gating helper.
	gated := map[string]bool{}
	for changed := true; changed; {
		changed = false
		byName := map[string][]bool{}
		for _, f := range funcs {
			name := f.decl.Name.Name
			byName[name] = append(byName[name], allConstructionsGated(f.decl.Body, reaches, gated))
		}
		for name, verdicts := range byName {
			if gated[name] || !reaches[name] {
				continue
			}
			all := true
			for _, v := range verdicts {
				all = all && v
			}
			if all {
				gated[name] = true
				changed = true
			}
		}
	}

	var findings []string
	for _, f := range funcs {
		isolatesHome := setsConfigHome(f.decl.Body) || callsAny(f, isolates)
		if !isolatesHome || !callsAny(f, reaches) || allConstructionsGated(f.decl.Body, reaches, gated) {
			continue
		}
		pos := fset.Position(f.decl.Pos())
		findings = append(findings, fmt.Sprintf("%s:%d %s", filepath.Base(pos.Filename), pos.Line, f.decl.Name.Name))
	}
	sort.Strings(findings)
	return findings, nil
}

// closeOver grows set until no function outside it has the property directly
// or calls a name inside it.
func closeOver(set map[string]bool, funcs []auditFunc, direct func(auditFunc) bool) {
	for changed := true; changed; {
		changed = false
		for _, f := range funcs {
			name := f.decl.Name.Name
			if !set[name] && (direct(f) || callsAny(f, set)) {
				set[name] = true
				changed = true
			}
		}
	}
}

func callsAny(f auditFunc, set map[string]bool) bool {
	for callee := range f.calls {
		if set[callee] {
			return true
		}
	}
	return false
}

// calledNames returns the bare identifiers body calls directly — f(...) — plus
// the selector names of method and package calls — x.f(...).
func calledNames(body *ast.BlockStmt) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if name := calleeName(call.Fun); name != "" {
				names[name] = true
			}
		}
		return true
	})
	return names
}

// setsConfigHome reports whether body calls Setenv with a config-home variable
// as its first argument.
func setsConfigHome(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return !found
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Setenv" {
			return !found
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if key, err := strconv.Unquote(lit.Value); err == nil && configHomeEnvVars[key] {
				found = true
			}
		}
		return !found
	})
	return found
}

// allConstructionsGated reports whether every call in body that reaches a
// constructor is gated at the call.
func allConstructionsGated(body *ast.BlockStmt, reaches, gated map[string]bool) bool {
	all := true
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return all
		}
		if name := calleeName(call.Fun); reaches[name] && !callGated(call, name, body, gated) {
			all = false
		}
		return all
	})
	return all
}

// callGated decides one constructor-reaching call. A constructor is gated by
// the config value it receives; a helper by a gate among its arguments — the
// withoutGitSnapshot() option, or a config carrying the gate — or by gating
// every session it constructs itself.
func callGated(call *ast.CallExpr, name string, body *ast.BlockStmt, gated map[string]bool) bool {
	if idx, ok := sessionConstructors[name]; ok {
		return idx >= 0 && len(call.Args) > idx && gateOfExpr(call.Args[idx], call.Pos(), body)
	}
	if gated[name] {
		return true
	}
	carries := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.CallExpr:
				if calleeName(e.Fun) == snapshotGateOption {
					carries = true
				}
			case *ast.CompositeLit:
				if literalGate(e) {
					carries = true
				}
			case *ast.Ident:
				if gateOfIdent(e.Name, call.Pos(), body) {
					carries = true
				}
			}
			return !carries
		})
	}
	return carries
}

// gateOfExpr resolves the gate a config expression carries at pos: a literal
// by its own fields, a variable by the last top-level write before pos.
func gateOfExpr(expr ast.Expr, pos token.Pos, body *ast.BlockStmt) bool {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return literalGate(e)
	case *ast.Ident:
		return gateOfIdent(e.Name, pos, body)
	case *ast.UnaryExpr:
		return gateOfExpr(e.X, pos, body)
	case *ast.ParenExpr:
		return gateOfExpr(e.X, pos, body)
	}
	return false
}

// literalGate reads a SessionConfig, RestoreSessionConfig or testConfig
// literal: the gate is its testOnly's skipGitSnapshot, or its own
// skipGitSnapshot, and absent means false.
func literalGate(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "testOnly":
			if inner, ok := kv.Value.(*ast.CompositeLit); ok {
				return literalGate(inner)
			}
			return false
		case "skipGitSnapshot":
			return isTrue(kv.Value)
		}
	}
	return false
}

func isTrue(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "true"
}

// gateOfIdent follows the writes to a config variable named name that a
// constructor at pos can rely on: the top-level statements of the function
// body and of every function literal enclosing pos, in source order, taking
// the last one before pos. The forms read are `name := <config>`,
// `name = <config>`, `var name = <config>`, `name.testOnly = <testConfig>` and
// `name.testOnly.skipGitSnapshot = true|false`; a write nested under a
// condition or loop is not one the constructor can rely on and is not read.
func gateOfIdent(name string, pos token.Pos, body *ast.BlockStmt) bool {
	var lastPos token.Pos
	gate := false
	for _, stmt := range scopeStatements(body, pos) {
		if stmt.Pos() >= pos {
			continue
		}
		var lhs []ast.Expr
		var rhs []ast.Expr
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			lhs, rhs = s.Lhs, s.Rhs
		case *ast.DeclStmt:
			gen, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for _, id := range vs.Names {
						lhs = append(lhs, id)
					}
					rhs = append(rhs, vs.Values...)
				}
			}
		default:
			continue
		}
		if len(lhs) != len(rhs) {
			continue
		}
		for i, target := range lhs {
			value, ok := writeTo(name, target, rhs[i], stmt.Pos(), body)
			if ok && stmt.Pos() > lastPos {
				lastPos, gate = stmt.Pos(), value
			}
		}
	}
	return gate
}

// writeTo reads one assignment target/value pair as a write to name's gate.
func writeTo(name string, target, value ast.Expr, pos token.Pos, body *ast.BlockStmt) (bool, bool) {
	switch t := target.(type) {
	case *ast.Ident:
		if t.Name == name {
			return gateOfExpr(value, pos, body), true
		}
	case *ast.SelectorExpr:
		switch path := selectorPath(t); {
		case len(path) == 3 && path[0] == name && path[1] == "testOnly" && path[2] == "skipGitSnapshot":
			return isTrue(value), true
		case len(path) == 2 && path[0] == name && path[1] == "testOnly":
			return gateOfExpr(value, pos, body), true
		}
	}
	return false, false
}

// selectorPath flattens a.b.c into ["a","b","c"]; nil when the chain does not
// bottom out in an identifier.
func selectorPath(sel *ast.SelectorExpr) []string {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return []string{x.Name, sel.Sel.Name}
	case *ast.SelectorExpr:
		if inner := selectorPath(x); inner != nil {
			return append(inner, sel.Sel.Name)
		}
	}
	return nil
}

// scopeStatements returns the top-level statements of body and of every
// function literal in body that encloses pos, in source order.
func scopeStatements(body *ast.BlockStmt, pos token.Pos) []ast.Stmt {
	stmts := append([]ast.Stmt(nil), body.List...)
	ast.Inspect(body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok && lit.Body != nil && lit.Body.Pos() <= pos && pos < lit.Body.End() {
			stmts = append(stmts, lit.Body.List...)
		}
		return true
	})
	sort.Slice(stmts, func(i, j int) bool { return stmts[i].Pos() < stmts[j].Pos() })
	return stmts
}
