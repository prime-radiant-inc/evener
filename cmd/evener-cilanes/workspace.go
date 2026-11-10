package cilanes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
	// wholeTreeTesters are packages whose tests read the whole repository:
	// they list its tracked files, or walk from its root. No name says which
	// files they read, so every change reaches them.
	wholeTreeTesters map[string]bool
	// goReadByTypeScript are the Go files TypeScript-tree code names, such
	// as tests that read Go source to pin its constants.
	goReadByTypeScript map[string]bool
}

// goFileLiteral matches a quoted string naming a Go file, optionally as a
// Vite ?raw import.
var goFileLiteral = regexp.MustCompile("[\"'`]([^\"'`\\s]+\\.go)(?:\\?raw)?[\"'`]")

var typeScriptSourceExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".mjs", ".cjs"}

// readGoNamedByTypeScript records the Go files a TypeScript tree's source
// names, resolved from the naming file's directory, from its tree (where
// the tree's tests run), and from the repository root.
func (ws *workspace) readGoNamedByTypeScript(root string, tracked []string) error {
	for _, file := range tracked {
		tree := typeScriptTreeOf(file)
		if tree == "" || !slices.Contains(typeScriptSourceExts, path.Ext(file)) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return err
		}
		for _, m := range goFileLiteral.FindAllSubmatch(data, -1) {
			for _, base := range []string{path.Dir(file), tree, "."} {
				if named := path.Join(base, string(m[1])); !strings.HasPrefix(named, "../") {
					ws.goReadByTypeScript[named] = true
				}
			}
		}
	}
	return nil
}

// goPackage is one package directory's dependencies, split as go test
// splits them: what the package builds from, which its importers build from
// too, and what only its own tests add.
type goPackage struct {
	dir   string
	build dependencies
	test  dependencies
}

// dependencies are the package directories a set of Go files imports and
// every path they name: a test that builds ./cmd/evener depends on that
// command as surely as an import would, and one that reads a frontend file
// by path depends on that file.
type dependencies struct {
	paths map[string]bool
	// trees are directories named with a trailing /..., which cover every
	// package below them.
	trees []string
	// typeScriptNames are the paths that lie inside a TypeScript tree, the
	// only ones the tree rule reads.
	typeScriptNames []string
}

func (d *dependencies) add(p string) {
	if d.paths == nil {
		d.paths = map[string]bool{}
	}
	if !d.paths[p] && typeScriptTreeOf(p) != "" {
		d.typeScriptNames = append(d.typeScriptNames, p)
	}
	d.paths[p] = true
}

// parsedFile is what one Go file contributes before import paths resolve.
type parsedFile struct {
	dir     string
	test    bool
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
	ws := &workspace{
		foreignModuleDirs:  map[string]bool{},
		packages:           map[string]*goPackage{},
		wholeTreeTesters:   map[string]bool{},
		goReadByTypeScript: map[string]bool{},
	}
	if err := ws.readGoNamedByTypeScript(root, tracked); err != nil {
		return nil, err
	}
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
			ws.packages[parsed.dir] = &goPackage{dir: parsed.dir}
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
		deps := &ws.packages[file.dir].build
		if file.test {
			deps = &ws.packages[file.dir].test
		}
		for _, imp := range file.imports {
			if dir, ok := importDirs[imp]; ok {
				deps.add(dir)
			}
		}
		for _, name := range file.names {
			if file.test && readsWholeTree(name, file.dir) {
				ws.wholeTreeTesters[file.dir] = true
			}
			if dir, ok := importDirs[name]; ok {
				deps.add(dir)
				continue
			}
			for _, candidate := range pathCandidates(name, file.dir, ws.moduleOf(file.dir)) {
				if tree, ok := strings.CutSuffix(candidate, "/..."); ok {
					deps.trees = append(deps.trees, tree)
					continue
				}
				deps.add(candidate)
				// A named Go file stands for its package, except at the
				// repository root, where "main.go" resolves from any
				// package's literal and names the root package by accident.
				if dir := path.Dir(candidate); strings.HasSuffix(candidate, ".go") && dir != "." {
					deps.add(dir)
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
	parsed := parsedFile{dir: path.Dir(file), test: strings.HasSuffix(file, "_test.go")}
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
				for pattern := range strings.FieldsSeq(patterns) {
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

// readsWholeTree reports whether a test naming s reads the whole
// repository: s is git's ls-files command, or a chain of ".." that climbs
// from the test's package directory to the repository root. Production code
// that lists files lists the user's repository, so only tests count.
func readsWholeTree(s, pkgDir string) bool {
	if s == "ls-files" {
		return true
	}
	if pkgDir == "." || strings.Trim(strings.ReplaceAll(s, "..", ""), "/") != "" {
		return false
	}
	return path.Join(pkgDir, s) == "."
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

// affected returns every package whose tests a change reaches, mapped to a
// changed path that reaches it. buildSeeds are packages whose build changed;
// a changed build reaches every package that depends on it, through imports
// and named paths. testSeeds are packages whose tests alone changed. A
// package's tests are reached when its build is, when they change, or when
// they depend on a reached package's build.
func (ws *workspace) affected(buildSeeds, testSeeds map[string]string) map[string]string {
	builders := map[string][]string{}
	var treeBuilders []*goPackage
	for _, pkg := range ws.packages {
		for p := range pkg.build.paths {
			builders[p] = append(builders[p], pkg.dir)
		}
		if len(pkg.build.trees) > 0 {
			treeBuilders = append(treeBuilders, pkg)
		}
	}
	built := maps.Clone(buildSeeds)
	queue := slices.Sorted(maps.Keys(buildSeeds))
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		next := slices.Clone(builders[dir])
		for _, pkg := range treeBuilders {
			if pkg.build.coversTree(dir) {
				next = append(next, pkg.dir)
			}
		}
		slices.Sort(next)
		for _, d := range next {
			if _, ok := built[d]; !ok {
				built[d] = built[dir]
				queue = append(queue, d)
			}
		}
	}

	tested := maps.Clone(built)
	for dir, why := range testSeeds {
		setIfAbsent(tested, dir, why)
	}
	builtDirs := slices.Sorted(maps.Keys(built))
	for _, pkg := range ws.packages {
		if _, ok := tested[pkg.dir]; ok {
			continue
		}
		for _, dir := range builtDirs {
			if pkg.test.paths[dir] || pkg.test.coversTree(dir) {
				tested[pkg.dir] = built[dir]
				break
			}
		}
	}
	return tested
}

// coversTree reports whether a /... name covers the package in dir.
func (d dependencies) coversTree(dir string) bool {
	return slices.ContainsFunc(d.trees, func(tree string) bool { return atOrUnder(dir, tree) })
}

// readersOf are the packages whose build, and those whose tests alone, name
// a TypeScript tree's file or a directory inside the tree holding it. A name
// above the tree (the repository root, "cmd") is not counted: those are
// walks whose reach a name cannot show.
func (ws *workspace) readersOf(file string) (build, test []string) {
	reads := func(d dependencies) bool {
		return slices.ContainsFunc(d.typeScriptNames, func(name string) bool { return atOrUnder(file, name) })
	}
	for _, pkg := range ws.packages {
		switch {
		case reads(pkg.build):
			build = append(build, pkg.dir)
		case reads(pkg.test):
			test = append(test, pkg.dir)
		}
	}
	return build, test
}

// atOrUnder reports whether p is dir or inside it.
func atOrUnder(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}
