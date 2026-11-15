package repoaudit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// tempDirRoots are the test trees that had grown their own spelling of the
// same fixture step, because a test that compares against a path production
// canonicalized needs a temp dir with symlinks resolved (macOS /var is a
// symlink to /private/var, #2497). #4426 swept the command trees; #5936 added
// the agent module, whose packages cannot be left to drift either.
var tempDirRoots = []string{"cmd/evener", "cmd/evener-dev", "cmd/evener-hub", "agent"}

// tempDirResolveNames are the calls that canonicalize a directory. A function
// that both mints a temp dir and resolves it is the helper shape this audit
// forbids outside internal/testfs: the next fix to the rule would otherwise
// land in every copy, which is how #4426's three spellings drifted apart.
var tempDirResolveNames = map[string]bool{
	"EvalSymlinks":    true,
	"CanonicalizeDir": true,
}

// tempDirResolverFunc reports whether fn mints a temp dir and resolves its
// symlinks, and returns something. Returning a value narrows the audit to the
// helper shape: a test that resolves its own temp dir for one assertion is a
// use, not a second spelling of the fixture step (#4426 notes the hub's
// remaining in-package EvalSymlinks sites as related, out-of-scope work).
func tempDirResolverFunc(fn *ast.FuncDecl) bool {
	if fn.Body == nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	mints, resolves := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch {
		case sel.Sel.Name == "TempDir":
			mints = true
		case tempDirResolveNames[sel.Sel.Name]:
			resolves = true
		}
		return true
	})
	return mints && resolves
}

// noTestMintsItsOwnCanonicalTempDir keeps the canonical temp-dir helper
// singular. Every helper in these roots that mints a temp dir and resolves it
// calls internal/testfs.CanonicalTempDir; a local helper of that shape is the
// copy #4426 collapsed, and a second one would drift the moment the rule
// changes. An inline resolve in a test body is a use, not a second helper.
func noTestMintsItsOwnCanonicalTempDir(a *audit) {
	var offenders []string
	for _, root := range tempDirRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", path, parseErr)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !tempDirResolverFunc(fn) {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("%s: %s", path, fn.Name.Name))
			}
			return nil
		})
		if err != nil {
			a.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(offenders)
	for _, offender := range offenders {
		a.Errorf("%s mints a temp dir and resolves its symlinks in one function; "+
			"call internal/testfs.CanonicalTempDir instead, so #2497's rule has one source", offender)
	}
}
