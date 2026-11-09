package sandbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// autofsHomePath matches a path under /home/, the directory macOS serves from the
// auto_home automount map, whether it starts a literal or follows an
// environment assignment ("HOME=") or a list separator ("PATH=a:"). Resolve and
// the environment floor canonicalize and stat such paths on the REAL
// filesystem, so a simulated host whose home sits under /home/ makes each one
// ask automountd, and through it opendirectoryd, for that user: about 12ms per
// lookup, and tens of thousands of lookups per package run. The mount point
// itself ("/home") is cheap and stays usable as a fixture.
var autofsHomePath = regexp.MustCompile(`(^|[=:])/home/[^/:\s]`)

// TestSandboxFixturesKeepOffTheHomeAutomount fails on any string literal in this
// package that names a path under /home/. Simulated hosts put their homes under
// /fixture-home/, which exists on no host and is served by no automounter.
func TestSandboxFixturesKeepOffTheHomeAutomount(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if autofsHomePath.MatchString(value) {
				t.Errorf("%s: fixture path %q is under the macOS /home automount", fset.Position(lit.Pos()), value)
			}
			return true
		})
	}
}
