// evener-serialtestcheck fails when a top-level test runs serially for no
// reason. A test is serial unless it calls t.Parallel(), and in a package
// with thousands of tests the serial ones run one at a time: the agent
// package once ran 3,800 tests that way, three quarters of its -race lane,
// though only a few hundred touched anything another test could see.
//
// A serial test is fine when it reaches process-wide state, directly or
// through any function in its package it calls:
//
//   - the environment or working directory (t.Setenv, t.Chdir, os.Setenv,
//     os.Unsetenv, os.Chdir, os.Clearenv);
//   - the default logger (slog.SetDefault, log.SetOutput, ...);
//   - a global test setter (a Set... or Observe... function whose name
//     contains ForTest);
//   - a write to, or through, a package-level variable (v = x, v[k] = x,
//     v.f = x, v++), or a one-value atomic Store, Swap or CompareAndSwap on
//     one - unless the variable is a reviewed synchronized cache
//     (synchronizedCaches);
//
// or when its doc comment says why it is not parallel ("Not parallel: ...").
// A test that calls another test, or is called by one, is left alone: a
// t.Parallel in both would run twice on one T.
//
// The rules lean serial: an unreviewed package write, in production or test
// code, counts as shared state, so the check never asks for t.Parallel on a
// test that reaches a new global seam. Calls are matched by name, without type
// information, so a method call reaches every package function or method of
// that name (t.Run reaches any Run). That also only widens what counts as
// shared: the check is a floor that catches self-contained tests, not a
// proof that every serial test it passes needs to be serial.
//
// Run via `make lint-serial-tests` (or `evener-dev serialtestcheck [dir ...]`,
// default agent). Exits 1 with one line per finding, 2 on a parse error.
package serialtestcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// defaultDirs are the packages checked when no directory is given.
var defaultDirs = []string{"agent"}

// synchronizedCaches are package variables that are written only as memo
// caches under their own synchronization, so tests share them safely. Each
// was read and confirmed; add one only after reading every write to it.
var synchronizedCaches = map[string]bool{
	"testRegistryCache": true, // agent: testRegistryWith, under testRegistryMu
	"feedOffsets":       true, // agent: feedJob, under feedOffsetsMu
	"wtBaseRepoPath":    true, // agent: worktreeBaseRepo, in wtBaseRepoOnce
	"wtBaseRepoHead":    true, // agent: worktreeBaseRepo, in wtBaseRepoOnce
	"errWtBaseRepo":     true, // agent: worktreeBaseRepo, in wtBaseRepoOnce
	"intgMCPServerDir":  true, // agent: intg_buildMCPServer, in intgMCPServerOnce
	"intgMCPServerPath": true, // agent: intg_buildMCPServer, in intgMCPServerOnce
}

// Run is the evener-dev entry point.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr)
}

func run(dirs []string, stdout, stderr io.Writer) int {
	if len(dirs) == 0 {
		dirs = defaultDirs
	}
	var all []finding
	for _, dir := range dirs {
		findings, err := check(dir, synchronizedCaches)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "evener-serialtestcheck:", err)
			return 2
		}
		all = append(all, findings...)
	}
	for _, f := range all {
		_, _ = fmt.Fprintf(stdout, "%s:%d: %s runs serially but reaches no process-wide state: add t.Parallel(), or give the reason in its doc comment (\"Not parallel: ...\")\n", f.file, f.line, f.test)
	}
	if len(all) != 0 {
		_, _ = fmt.Fprintf(stderr, "evener-serialtestcheck: %d needlessly serial test(s)\n", len(all))
		return 1
	}
	return 0
}

type finding struct {
	file string
	line int
	test string
}

// function is what the check records about one function or method body.
type function struct {
	shared   bool            // reaches process-wide state itself
	parallel bool            // calls .Parallel() itself
	calls    map[string]bool // package functions and method names it calls
}

type test struct {
	name   string
	pos    token.Position
	reason bool // its doc comment says why it is serial
}

// check reports the needlessly serial top-level tests in the package in dir.
// synced names the package variables that are synchronized caches.
func check(dir string, synced map[string]bool) ([]finding, error) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	testFile := map[*ast.File]bool{}
	pkgVars := map[string]bool{}
	topLevel := map[any]bool{}
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
		testFile[file] = strings.HasSuffix(path, "_test.go")
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					topLevel[spec] = true
					for _, name := range spec.(*ast.ValueSpec).Names {
						pkgVars[name.Name] = true
					}
				}
			}
		}
	}
	// The parser resolves names within one file: a local, a parameter or a
	// same-file package variable resolves to its own declaration, and a
	// package variable declared in another file to nothing.
	isShared := func(id *ast.Ident) bool {
		if id == nil || id.Name == "_" || !pkgVars[id.Name] || synced[id.Name] {
			return false
		}
		return id.Obj == nil || topLevel[id.Obj.Decl]
	}

	funcs := map[string]*function{}
	var tests []test
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			f := funcs[name]
			if f == nil {
				f = &function{calls: map[string]bool{}}
				funcs[name] = f
			}
			inspectBody(fd.Body, f, isShared)
			if fd.Recv == nil && testFile[file] && isTestFunc(fd) {
				tests = append(tests, test{name: name, pos: fset.Position(fd.Pos()), reason: statesSerialReason(fd.Doc)})
			}
		}
	}

	shared := closure(funcs, func(f *function) bool { return f.shared })
	parallel := closure(funcs, func(f *function) bool { return f.parallel })
	isTest := map[string]bool{}
	for _, t := range tests {
		isTest[t.name] = true
	}
	wrapped := map[string]bool{}
	for _, t := range tests {
		for callee := range funcs[t.name].calls {
			if callee != t.name && isTest[callee] {
				wrapped[t.name], wrapped[callee] = true, true
			}
		}
	}

	var findings []finding
	for _, t := range tests {
		if parallel[t.name] || shared[t.name] || wrapped[t.name] || t.reason {
			continue
		}
		findings = append(findings, finding{file: t.pos.Filename, line: t.pos.Line, test: t.name})
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].file != findings[j].file {
			return findings[i].file < findings[j].file
		}
		return findings[i].line < findings[j].line
	})
	return findings, nil
}

// inspectBody records what one body calls, whether it calls .Parallel(), and
// whether it reaches process-wide state itself.
func inspectBody(body *ast.BlockStmt, f *function, isShared func(*ast.Ident) bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				break
			}
			for _, lhs := range n.Lhs {
				if isShared(rootIdent(lhs)) {
					f.shared = true
				}
			}
		case *ast.IncDecStmt:
			if isShared(rootIdent(n.X)) {
				f.shared = true
			}
		case *ast.CallExpr:
			switch fun := n.Fun.(type) {
			case *ast.Ident:
				f.calls[fun.Name] = true
				if isGlobalSetter(fun.Name) {
					f.shared = true
				}
			case *ast.SelectorExpr:
				sel := fun.Sel.Name
				f.calls[sel] = true
				switch {
				case sel == "Parallel" && len(n.Args) == 0:
					f.parallel = true
				case sel == "Setenv" || sel == "Chdir" || isGlobalSetter(sel):
					f.shared = true
				}
				if x, ok := fun.X.(*ast.Ident); ok {
					switch x.Name + "." + sel {
					case "os.Setenv", "os.Unsetenv", "os.Chdir", "os.Clearenv",
						"slog.SetDefault", "log.SetOutput", "log.SetFlags", "log.SetPrefix":
						f.shared = true
					}
					// An atomic override of a package value: Store(v), Swap(v),
					// CompareAndSwap(old, new). A keyed map's Store(k, v) is
					// scoped by its key.
					if isShared(x) && (((sel == "Store" || sel == "Swap") && len(n.Args) == 1) || (sel == "CompareAndSwap" && len(n.Args) == 2)) {
						f.shared = true
					}
				}
			}
		}
		return true
	})
}

// closure is the set of functions that have the property themselves or call,
// transitively, a function that does. Methods are matched by name alone,
// which can only add functions to the set.
func closure(funcs map[string]*function, has func(*function) bool) map[string]bool {
	set := map[string]bool{}
	for name, f := range funcs {
		if has(f) {
			set[name] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for name, f := range funcs {
			if set[name] {
				continue
			}
			for callee := range f.calls {
				if set[callee] {
					set[name] = true
					changed = true
					break
				}
			}
		}
	}
	return set
}

// isTestFunc reports whether fd is a top-level Go test: TestXxx(t *testing.T).
func isTestFunc(fd *ast.FuncDecl) bool {
	name := fd.Name.Name
	if !strings.HasPrefix(name, "Test") || name == "TestMain" {
		return false
	}
	if rest := name[len("Test"):]; rest != "" && rest[0] >= 'a' && rest[0] <= 'z' {
		return false
	}
	params := fd.Type.Params.List
	if len(params) != 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

// statesSerialReason reports whether a doc comment says the test is serial on
// purpose.
func statesSerialReason(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	text := strings.ToLower(doc.Text())
	for _, phrase := range []string{"not parallel", "no t.parallel", "must not run in parallel"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// isGlobalSetter reports whether a ...ForTest(s|ing) function sets process
// state (SetX..., ObserveX...) rather than reading it or acting on its
// arguments.
func isGlobalSetter(name string) bool {
	return strings.Contains(name, "ForTest") && (strings.HasPrefix(name, "Set") || strings.HasPrefix(name, "Observe"))
}

// rootIdent is the variable an assignment target writes through: v, v[k],
// v.f, *v and any nesting of them.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return nil
		}
	}
}
