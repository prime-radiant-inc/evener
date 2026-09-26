package agent

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestSessionTestsWithAPerTestConfigHomeSkipTheLaunchSnapshot scans this
// package's test sources for a function that isolates the config home and
// constructs a session without skipping the launch-time environment snapshot.
//
// The snapshot forks `go env` (probeCapabilities). The go command records
// telemetry under os.UserConfigDir()/go/telemetry — $XDG_CONFIG_HOME on
// Linux, $HOME/Library/Application Support on macOS — and the first time it
// runs in a config home with no upload token it daemonizes a sidecar (Setsid,
// never waited) that keeps creating files there after `go env` has exited.
// When that config home is a t.TempDir the sidecar races the TempDir RemoveAll
// cleanup and a test that passed fails with "unlinkat …: directory not empty".
// Nothing can join the sidecar, so such a test must not fork at all (#1527).
func TestSessionTestsWithAPerTestConfigHomeSkipTheLaunchSnapshot(t *testing.T) {
	t.Parallel()
	findings, err := configHomeForkAuditFindings(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("test(s) set HOME or XDG_CONFIG_HOME (directly or through a helper) and construct a session whose launch snapshot forks `go env` into it:\n%s\n\n"+
			"Fix: write the gate in the literal the constructor receives — SessionConfig{testOnly: testConfig{skipGitSnapshot: true}} "+
			"(RestoreSessionConfig likewise) — or pass withoutGitSnapshot() to newSession. A config held in a variable is "+
			"refused here even when it is gated, so a later write cannot re-enable the snapshot unseen.",
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
// construction it reaches ungated, as "file:line name" lines in sorted order.
func configHomeForkAuditFindings(dir string) ([]string, error) {
	fset, files, err := parseAgentTestFiles(dir)
	if err != nil {
		return nil, err
	}
	var funcs []auditFunc
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs = append(funcs, auditFunc{decl: fn, calls: calledNames(fn.Body)})
			}
		}
	}

	// Three fixed points over the test files' own functions, keyed by bare
	// name (a method and a function sharing a name are not distinguished; none
	// do today): reaches a constructor, isolates the config home, and gates
	// every construction it reaches.
	reaches := map[string]bool{}
	for name := range sessionConstructors {
		reaches[name] = true
	}
	closeOver(reaches, funcs, callsAny)
	isolates := map[string]bool{}
	closeOver(isolates, funcs, func(f auditFunc, set map[string]bool) bool {
		return setsConfigHome(f.decl.Body) || callsAny(f, set)
	})
	gated := map[string]bool{}
	closeOver(gated, funcs, func(f auditFunc, set map[string]bool) bool {
		return reaches[f.decl.Name.Name] && allConstructionsGated(f.decl.Body, reaches, set)
	})

	var findings []string
	for _, f := range funcs {
		if !isolates[f.decl.Name.Name] || !callsAny(f, reaches) || allConstructionsGated(f.decl.Body, reaches, gated) {
			continue
		}
		pos := fset.Position(f.decl.Pos())
		findings = append(findings, fmt.Sprintf("%s:%d %s", filepath.Base(pos.Filename), pos.Line, f.decl.Name.Name))
	}
	sort.Strings(findings)
	return findings, nil
}

// closeOver grows set until no function outside it satisfies has, which sees
// the set as grown so far.
func closeOver(set map[string]bool, funcs []auditFunc, has func(auditFunc, map[string]bool) bool) {
	for changed := true; changed; {
		changed = false
		for _, f := range funcs {
			if name := f.decl.Name.Name; !set[name] && has(f, set) {
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
		if call, ok := n.(*ast.CallExpr); ok {
			if name := calleeName(call.Fun); reaches[name] && !callGated(call, name, gated) {
				all = false
			}
		}
		return all
	})
	return all
}

// callGated decides one constructor-reaching call. A constructor is gated only
// by a literal in its config position; a helper by a withoutGitSnapshot() call
// or a gated literal anywhere in its arguments, or by gating every
// construction it reaches itself.
func callGated(call *ast.CallExpr, name string, gated map[string]bool) bool {
	if idx, ok := sessionConstructors[name]; ok {
		if idx < 0 || len(call.Args) <= idx {
			return false
		}
		lit, ok := call.Args[idx].(*ast.CompositeLit)
		return ok && literalGate(lit)
	}
	if gated[name] {
		return true
	}
	carries := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.CallExpr:
				carries = carries || calleeName(e.Fun) == snapshotGateOption
			case *ast.CompositeLit:
				carries = carries || literalGate(e)
			}
			return !carries
		})
	}
	return carries
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
			inner, ok := kv.Value.(*ast.CompositeLit)
			return ok && literalGate(inner)
		case "skipGitSnapshot":
			id, ok := kv.Value.(*ast.Ident)
			return ok && id.Name == "true"
		}
	}
	return false
}
