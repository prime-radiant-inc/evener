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
// XDG_CONFIG_HOME — and then constructs a session (NewSession or a
// RestoreSessionFromMeta* entry point, directly or through test helpers)
// without skipping the launch-time environment snapshot.
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
// A function clears the audit when its body names the gate — a
// `skipGitSnapshot: true` in its SessionConfig literal, or the newSession
// fixture's withoutGitSnapshot() option.
func TestSessionTestsWithAPerTestConfigHomeSkipTheLaunchSnapshot(t *testing.T) {
	findings, err := configHomeForkAuditFindings(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("test(s) isolate the config home per test and construct a session whose launch snapshot forks `go env` into it:\n%s\n\n"+
			"Fix: pass testOnly: testConfig{skipGitSnapshot: true} in the SessionConfig (or "+
			"withoutGitSnapshot() to newSession) so the snapshot never forks go and go's "+
			"telemetry sidecar never outlives the test's TempDir.",
			strings.Join(findings, "\n"))
	}
}

// configHomeEnvVars are the variables os.UserConfigDir derives the config home
// from on the platforms CI runs: XDG_CONFIG_HOME on Linux, HOME on macOS.
var configHomeEnvVars = map[string]bool{"HOME": true, "XDG_CONFIG_HOME": true}

// sessionConstructors are the package entry points that take the launch
// snapshot.
var sessionConstructors = map[string]bool{
	"NewSession":                       true,
	"RestoreSessionFromMeta":           true,
	"RestoreSessionFromMetaWithConfig": true,
}

// launchSnapshotGates are the identifiers a test names to skip the snapshot.
var launchSnapshotGates = map[string]bool{"skipGitSnapshot": true, "withoutGitSnapshot": true}

// configHomeForkAuditFindings reports every function in dir's test files that
// sets a config-home variable, reaches a session constructor, and names no
// gate, as "file:line name" lines in sorted order.
func configHomeForkAuditFindings(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var decls []*ast.FuncDecl
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
				decls = append(decls, fn)
			}
		}
	}

	// Reach is a fixed point over the test files' own functions: a function
	// reaches a constructor when it calls one by name, or calls a test-file
	// function that does. Methods are keyed by bare name alongside functions;
	// a callee name that is not a test-file declaration is ignored.
	calls := map[*ast.FuncDecl]map[string]bool{}
	for _, fn := range decls {
		calls[fn] = calledNames(fn.Body)
	}
	reaches := map[string]bool{}
	for name := range sessionConstructors {
		reaches[name] = true
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range decls {
			if reaches[fn.Name.Name] {
				continue
			}
			for callee := range calls[fn] {
				if reaches[callee] {
					reaches[fn.Name.Name] = true
					changed = true
					break
				}
			}
		}
	}

	var findings []string
	for _, fn := range decls {
		if !reaches[fn.Name.Name] || !setsConfigHome(fn.Body) || namesLaunchSnapshotGate(fn.Body) {
			continue
		}
		pos := fset.Position(fn.Pos())
		findings = append(findings, fmt.Sprintf("%s:%d %s", filepath.Base(pos.Filename), pos.Line, fn.Name.Name))
	}
	sort.Strings(findings)
	return findings, nil
}

// calledNames returns the bare identifiers body calls directly — f(...) — plus
// the selector names of method and package calls — x.f(...).
func calledNames(body *ast.BlockStmt) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			names[fun.Name] = true
		case *ast.SelectorExpr:
			names[fun.Sel.Name] = true
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

// namesLaunchSnapshotGate reports whether body mentions one of the gate
// identifiers anywhere.
func namesLaunchSnapshotGate(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && launchSnapshotGates[id.Name] {
			found = true
		}
		return !found
	})
	return found
}
