package cilanes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// workspace is the Go side of the repository: the go.work modules and, for
// each package, the packages and paths it depends on.
type workspace struct {
	// moduleDirs are the go.work module directories, "." for the root,
	// longest first so the first prefix match is the innermost module.
	moduleDirs []string
	// foreignModuleDirs hold a go.mod that go.work does not use: fixtures,
	// or tools outside the workspace.
	foreignModuleDirs map[string]bool
	packages          map[string]*goPackage
}

// goPackage is one package directory's dependencies.
type goPackage struct {
	dir string
	// dependsOn holds the package directories it imports, and every path
	// its source names: a test that builds ./cmd/evener depends on that
	// command as surely as an import would.
	dependsOn map[string]bool
	// dependsOnTrees are directories named with a trailing /..., which
	// cover every package below them.
	dependsOnTrees []string
	// names are the repository paths its source names, read by the tree
	// rule: a Go test that reads a frontend file by path runs when that
	// file changes.
	names map[string]bool
}

// parsedFile is what one Go file contributes before import paths resolve.
type parsedFile struct {
	dir     string
	imports []string
	names   []string
}

// loadWorkspace reads go.work, the go.mod of each module it uses, and every
// tracked Go file that is a workspace package's own source. tracked lists
// the repository's files relative to root, slash-separated.
func loadWorkspace(root string, tracked []string) (*workspace, error) {
	workData, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		return nil, err
	}
	work, err := modfile.ParseWork("go.work", workData, nil)
	if err != nil {
		return nil, err
	}
	ws := &workspace{foreignModuleDirs: map[string]bool{}, packages: map[string]*goPackage{}}
	modulePaths := map[string]string{}
	for _, use := range work.Use {
		dir := path.Clean(filepath.ToSlash(use.Path))
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "go.mod"))
		if err != nil {
			return nil, err
		}
		modulePaths[dir] = modfile.ModulePath(data)
		ws.moduleDirs = append(ws.moduleDirs, dir)
	}
	slices.SortFunc(ws.moduleDirs, func(a, b string) int { return len(b) - len(a) })
	for _, file := range tracked {
		if path.Base(file) == "go.mod" {
			if dir := path.Dir(file); !slices.Contains(ws.moduleDirs, dir) {
				ws.foreignModuleDirs[dir] = true
			}
		}
	}

	var files []parsedFile
	fset := token.NewFileSet()
	for _, file := range tracked {
		if _, ok := ws.packageSource(file); !ok {
			continue
		}
		parsed, err := parseGoFile(fset, root, file)
		if err != nil {
			return nil, err
		}
		files = append(files, parsed)
		if ws.packages[parsed.dir] == nil {
			ws.packages[parsed.dir] = &goPackage{dir: parsed.dir, dependsOn: map[string]bool{}, names: map[string]bool{}}
		}
	}

	importDirs := map[string]string{}
	for dir := range ws.packages {
		moduleDir := ws.moduleOf(dir)
		importPath := modulePaths[moduleDir]
		if dir != moduleDir {
			importPath += "/" + strings.TrimPrefix(dir, moduleDir+"/")
		}
		importDirs[importPath] = dir
	}
	for _, file := range files {
		pkg := ws.packages[file.dir]
		for _, imp := range file.imports {
			if dir, ok := importDirs[imp]; ok {
				pkg.dependsOn[dir] = true
			}
		}
		for _, name := range file.names {
			if dir, ok := importDirs[name]; ok {
				pkg.dependsOn[dir] = true
				continue
			}
			for _, candidate := range pathCandidates(name, file.dir, ws.moduleOf(file.dir)) {
				pkg.names[candidate] = true
				if tree, ok := strings.CutSuffix(candidate, "/..."); ok {
					pkg.dependsOnTrees = append(pkg.dependsOnTrees, tree)
					continue
				}
				pkg.dependsOn[candidate] = true
				if strings.HasSuffix(candidate, ".go") {
					pkg.dependsOn[path.Dir(candidate)] = true
				}
			}
		}
	}
	return ws, nil
}

// moduleOf is the innermost workspace module holding dir, or "" when no
// workspace module does.
func (ws *workspace) moduleOf(dir string) string {
	for _, m := range ws.moduleDirs {
		if atOrUnder(dir, m) {
			return m
		}
	}
	return ""
}

// packageSource reports the package directory of a Go file that is a
// workspace package's own source, as the go command sees it: inside a
// workspace module, with no go.mod of another module, testdata directory,
// or directory the go command ignores (a leading "_" or ".") between the
// module and the file. Other Go files are data or fixtures.
func (ws *workspace) packageSource(file string) (string, bool) {
	if !strings.HasSuffix(file, ".go") {
		return "", false
	}
	dir := path.Dir(file)
	moduleDir := ws.moduleOf(dir)
	if moduleDir == "" {
		return "", false
	}
	for d := dir; d != moduleDir; d = path.Dir(d) {
		base := path.Base(d)
		if ws.foreignModuleDirs[d] || base == "testdata" || strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".") {
			return "", false
		}
	}
	return dir, true
}

// parseGoFile collects a file's imports and the paths it names: each
// string literal, the literal arguments of a Join call joined into one path
// (filepath.Join(root, "cmd", "evener-hub", "frontend") names the frontend,
// while its "frontend" alone names nothing), and each //go:embed pattern.
func parseGoFile(fset *token.FileSet, root, file string) (parsedFile, error) {
	parsed := parsedFile{dir: path.Dir(file)}
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(file)), nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return parsed, err
	}
	for _, spec := range f.Imports {
		if imp, err := strconv.Unquote(spec.Path.Value); err == nil {
			parsed.imports = append(parsed.imports, imp)
		}
	}
	joined := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Join" {
				parsed.names = append(parsed.names, joinedLiteralRuns(n.Args, joined)...)
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING && !joined[n] {
				if s, err := strconv.Unquote(n.Value); err == nil {
					parsed.names = append(parsed.names, s)
				}
			}
		}
		return true
	})
	for _, group := range f.Comments {
		for _, comment := range group.List {
			if patterns, ok := strings.CutPrefix(comment.Text, "//go:embed "); ok {
				for _, pattern := range strings.Fields(patterns) {
					parsed.names = append(parsed.names, embedDir(pattern))
				}
			}
		}
	}
	return parsed, nil
}

// joinedLiteralRuns joins each run of consecutive string literal arguments
// with "/", marking the literals it consumed.
func joinedLiteralRuns(args []ast.Expr, joined map[*ast.BasicLit]bool) []string {
	var runs []string
	var run []string
	flush := func() {
		if len(run) > 0 {
			runs = append(runs, strings.Join(run, "/"))
		}
		run = nil
	}
	for _, arg := range args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			flush()
			continue
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			flush()
			continue
		}
		joined[lit] = true
		run = append(run, s)
	}
	flush()
	return runs
}

// embedDir is the directory part of an embed pattern before its first glob
// character, or the pattern itself when it has none.
func embedDir(pattern string) string {
	pattern = strings.TrimPrefix(pattern, "all:")
	if pattern, err := strconv.Unquote(pattern); err == nil {
		return pattern
	}
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		return path.Dir(pattern[:i])
	}
	return pattern
}

// pathCandidates are the repository paths a string could name when a
// package in pkgDir uses it: relative to the repository root, where root
// package tests and repository-root helpers run; to the module root; and to
// the package directory, where go test runs. A leading "/" is dropped, since
// root+"/frontend" is the common spelling. The repository root itself names
// every path, and "." or ".." in a package's source is almost always
// something else, so a candidate that resolves to the root is dropped.
func pathCandidates(s, pkgDir, moduleDir string) []string {
	s = strings.TrimPrefix(s, "/")
	if s == "" || strings.ContainsAny(s, " \t\n\x00") {
		return nil
	}
	var out []string
	for _, base := range []string{".", moduleDir, pkgDir} {
		candidate := path.Join(base, s)
		if candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") || slices.Contains(out, candidate) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

// dependents returns every package directory that depends, directly or
// through other packages, on one of the seeds, with the seeds themselves.
// Each maps to the seed's value: the changed path that reached it.
func (ws *workspace) dependents(seeds map[string]string) map[string]string {
	importers := map[string][]string{}
	type treeDependent struct{ tree, dir string }
	var treeDependents []treeDependent
	for _, pkg := range ws.packages {
		for dep := range pkg.dependsOn {
			importers[dep] = append(importers[dep], pkg.dir)
		}
		for _, tree := range pkg.dependsOnTrees {
			treeDependents = append(treeDependents, treeDependent{tree, pkg.dir})
		}
	}
	reached := map[string]string{}
	var queue []string
	for dir, why := range seeds {
		reached[dir] = why
		queue = append(queue, dir)
	}
	slices.Sort(queue)
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		next := slices.Clone(importers[dir])
		for _, td := range treeDependents {
			if atOrUnder(dir, td.tree) {
				next = append(next, td.dir)
			}
		}
		slices.Sort(next)
		for _, d := range next {
			if _, ok := reached[d]; !ok {
				reached[d] = reached[dir]
				queue = append(queue, d)
			}
		}
	}
	return reached
}

// readersOf are the packages whose source names file, or a directory
// holding it, inside tree. A name above the tree (the repository root,
// "cmd") is not counted: those are walks whose reach a name cannot show.
func (ws *workspace) readersOf(file, tree string) []string {
	var readers []string
	for _, pkg := range ws.packages {
		for name := range pkg.names {
			if atOrUnder(name, tree) && atOrUnder(file, name) {
				readers = append(readers, pkg.dir)
				break
			}
		}
	}
	slices.Sort(readers)
	return readers
}

// atOrUnder reports whether p is dir or inside it.
func atOrUnder(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}
