package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
)

// pkgEntry is the subset of `go list -json` output this tool reads. ImportPath
// is the raw field, so a test variant keeps its " [pkg.test]" suffix.
type pkgEntry struct {
	ImportPath string
	ForTest    string
	DepOnly    bool
	Dir        string
	Name       string
	Deps       []string
	Module     *struct {
		Path string
		Dir  string
	}
}

// pkgInfo is one real (buildable, non-test-variant) package in the workspace.
type pkgInfo struct {
	ModuleRoot string // GO_MODULES entry, "." for the root module
	Dir        string // absolute
}

// graph is the workspace's test-inclusive import graph: every real package the
// modules list, plus the set of packages each one's tests depend on.
type graph struct {
	root        string
	moduleRoots []string
	moduleDirs  map[string]string          // module root -> absolute dir
	modulePaths map[string]string          // module root -> import path
	runnable    map[string]bool            // module root -> GO_MODULES owns it
	pkgs        map[string]*pkgInfo        // import path -> package
	byDir       map[string]string          // absolute dir -> import path
	deps        map[string]map[string]bool // import path -> test-inclusive deps
}

// loadGraph runs `go list -e -deps -test -json ./...` once per module, in
// parallel, and folds the results into one graph. The dependency edges come
// only from go list's own output, so a new cross-module import is picked up
// without touching this tool.
func loadGraph(root string, moduleRoots []string) (*graph, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// `go list` reports fully resolved directories, so a checkout reached
	// through a symlink (/tmp on macOS) would never match a path built from the
	// unresolved root. Resolve once here and every directory comparison holds.
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	g := &graph{
		root:        absRoot,
		moduleRoots: moduleRoots,
		moduleDirs:  make(map[string]string, len(moduleRoots)),
		modulePaths: make(map[string]string, len(moduleRoots)),
		runnable:    make(map[string]bool, len(moduleRoots)),
		pkgs:        make(map[string]*pkgInfo),
		byDir:       make(map[string]string),
		deps:        make(map[string]map[string]bool),
	}
	for _, m := range moduleRoots {
		g.moduleDirs[m] = filepath.Join(absRoot, filepath.FromSlash(m))
		g.runnable[m] = true
	}
	// GO_MODULES names the modules the gates run, but go.work can hold more.
	// `fuzz` is one: `make test` skips it on purpose, yet root packages' tests
	// import it, so a change there really does break them. Load those modules'
	// packages for their import edges, and let selection reach the dependent
	// packages that GO_MODULES does run.
	graphOnly := workspaceModules(absRoot, moduleRoots)
	for _, m := range graphOnly {
		g.moduleDirs[m] = filepath.Join(absRoot, filepath.FromSlash(m))
	}
	loadModules := append(append([]string{}, moduleRoots...), graphOnly...)

	type result struct {
		module  string
		entries []pkgEntry
		err     error
	}
	results := make([]result, len(loadModules))
	var wg sync.WaitGroup
	for i, m := range loadModules {
		wg.Go(func() {
			entries, err := listModule(g.moduleDirs[m])
			results[i] = result{module: m, entries: entries, err: err}
		})
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("module %s: %w", r.module, r.err)
		}
		g.addModule(r.module, r.entries)
	}
	return g, nil
}

// listModule decodes the JSON stream `go list -e -deps -test` writes. -e keeps
// a package with a build error in the listing so one broken package cannot
// erase the rest of the graph.
func listModule(dir string) ([]pkgEntry, error) {
	cmd, cancel := command(context.Background(), "go", "list", "-e", "-deps", "-test", "-json", "./...")
	defer cancel()
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var entries []pkgEntry
	for {
		var e pkgEntry
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decoding go list output in %s: %w", dir, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// addModule records one module's packages and their test-inclusive dependency
// sets. The package's own test binary (`pkg.test`), its internal test variant
// (`pkg [pkg.test]`) and its external test package (`pkg_test [pkg.test]`) are
// all folded into the package's dependency set, so an import that only a test
// of a dependent package adds still creates an affected edge.
func (g *graph) addModule(module string, entries []pkgEntry) {
	byDir := map[string][]pkgEntry{}
	for _, e := range entries {
		if e.Dir == "" {
			continue
		}
		byDir[e.Dir] = append(byDir[e.Dir], e)
	}
	for dir, group := range byDir {
		var self *pkgEntry
		for i := range group {
			e := group[i]
			if e.DepOnly || e.ForTest != "" || strings.Contains(e.ImportPath, " [") {
				continue
			}
			if strings.HasSuffix(e.ImportPath, ".test") && e.Name == "main" {
				continue
			}
			self = &group[i]
			break
		}
		if self == nil {
			continue
		}
		g.pkgs[self.ImportPath] = &pkgInfo{ModuleRoot: module, Dir: dir}
		g.byDir[dir] = self.ImportPath
		// go list already reports the module each package belongs to. Using
		// it keeps a deleted package's import path derivable without
		// inferring a module path from a package path.
		if self.Module != nil && self.Module.Path != "" {
			g.modulePaths[module] = self.Module.Path
		}
		// The module's own import path lets a deleted package, which is no
		// longer in the listing, still be named so its importers are found.
		if _, known := g.modulePaths[module]; !known {
			relDir, relErr := filepath.Rel(g.moduleDirs[module], dir)
			if relErr == nil {
				relSlash := filepath.ToSlash(relDir)
				if relSlash == "." {
					g.modulePaths[module] = self.ImportPath
				} else {
					g.modulePaths[module] = strings.TrimSuffix(self.ImportPath, "/"+relSlash)
				}
			}
		}
		deps := map[string]bool{}
		for _, e := range group {
			belongs := e.ImportPath == self.ImportPath ||
				e.ForTest == self.ImportPath ||
				e.ImportPath == self.ImportPath+".test"
			if !belongs {
				continue
			}
			for _, d := range e.Deps {
				deps[bareImportPath(d)] = true
			}
		}
		delete(deps, self.ImportPath)
		g.deps[self.ImportPath] = deps
	}
}

// bareImportPath drops the test-variant annotation go list appends to a
// package name, as in `pkg [pkg.test]`. Without this, a dependency edge reached
// only through an external test package is stored under a name no real import
// path ever equals, so the closure can never match a change to that package.
func bareImportPath(dep string) string {
	if bare, _, ok := strings.Cut(dep, " ["); ok {
		return bare
	}
	if trimmed, ok := strings.CutSuffix(dep, ".test"); ok {
		return trimmed
	}
	return dep
}

// moduleFor maps a repo-relative path to its longest GO_MODULES root. The
// root module (".") matches everything no other module claims.
func (g *graph) moduleFor(rel string) (string, bool) {
	best := ""
	for _, m := range g.allModules() {
		if m == "." {
			if best == "" {
				best = m
			}
			continue
		}
		if rel == m || strings.HasPrefix(rel, m+"/") {
			if len(m) > len(best) {
				best = m
			}
		}
	}
	return best, best != ""
}

// allModules is every module this graph loaded: the GO_MODULES roots plus any
// module pulled in only for its import edges. A graph built by hand in a test
// carries only moduleRoots, so both sources are read.
func (g *graph) allModules() []string {
	seen := make(map[string]bool, len(g.moduleRoots)+len(g.moduleDirs))
	out := make([]string, 0, len(g.moduleRoots)+len(g.moduleDirs))
	for _, m := range g.moduleRoots {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for m := range g.moduleDirs {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// moduleDir returns the absolute directory a module's tests run in.
func (g *graph) moduleDir(module string) string { return g.moduleDirs[module] }

// runnableModule reports whether GO_MODULES owns the module, as opposed to it
// being loaded only for its import edges.
func (g *graph) runnableModule(module string) bool { return g.runnable[module] }

// packageAt maps an absolute directory to the real package there.
func (g *graph) packageAt(dir string) (string, bool) {
	ip, ok := g.byDir[filepath.Clean(dir)]
	return ip, ok
}

// importPathAt derives the import path a directory would have if it held a
// package, without requiring one to exist. Deletion and rename probes need
// this: `go list` no longer reports the directory, but its former importers
// still name the package that used to live there.
func (g *graph) importPathAt(module, dir string) (string, bool) {
	base, ok := g.modulePaths[module]
	if !ok {
		return "", false
	}
	rel, err := filepath.Rel(g.moduleDirs[module], filepath.Clean(dir))
	if err != nil {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)
	if relSlash == "." {
		return base, true
	}
	if strings.HasPrefix(relSlash, "..") {
		return "", false
	}
	return base + "/" + relSlash, true
}

// workspaceModules returns the go.work `use` entries that GO_MODULES does not
// already list, as repo-relative paths. It parses with x/mod's modfile, the same
// reader cmd/evener-fuzzregistry uses, so quoting and trailing comments are
// handled by the toolchain's own grammar instead of a second implementation of
// it. A workspace that will not parse yields no extra modules: that only makes
// selection more conservative, never wrong.
func workspaceModules(root string, known []string) []string {
	workPath := filepath.Join(root, "go.work")
	src, err := os.ReadFile(workPath)
	if err != nil {
		return nil
	}
	work, err := modfile.ParseWork(workPath, src, nil)
	if err != nil {
		return nil
	}
	var extra []string
	for _, use := range work.Use {
		line := strings.TrimPrefix(filepath.ToSlash(use.Path), "./")
		if line == "" || containsModule(known, line) {
			continue
		}
		extra = append(extra, line)
	}
	return extra
}

func containsModule(modules []string, m string) bool {
	return slices.Contains(modules, m)
}

// importedAnywhere reports whether any package in the graph depends on
// importPath. It separates a package that was removed (its importers remain and
// now fail to build) from a Go file that simply lives outside every package.
func (g *graph) importedAnywhere(importPath string) bool {
	for _, deps := range g.deps {
		if deps[importPath] {
			return true
		}
	}
	return false
}

// nearestPackageAncestor returns the closest package directory above dir,
// stopping at the module root. It attributes a file that lives outside any
// package — a testdata fixture, an asset in a subdirectory — to the package
// that owns the directory tree it sits in.
//
// The repo-root package is deliberately not an answer: root audits read
// repository-wide state and cost about 43 seconds, which test-changed does not
// pay. A walk that would reach the root reports no owner, so the caller can
// report the file as audit-only instead.
func (g *graph) nearestPackageAncestor(module, dir string) (string, bool) {
	moduleDir := filepath.Clean(g.moduleDirs[module])
	start := filepath.Clean(dir)
	// A file directly in the module root has no package ancestor inside this
	// module; starting at its parent would attribute it to the parent module's
	// root package, which is a different module's concern.
	if start == moduleDir {
		return "", false
	}
	for d := filepath.Dir(start); ; d = filepath.Dir(d) {
		if owner, found := g.packageAt(d); found {
			if rootPkg, ok := g.packageAt(g.root); ok && owner == rootPkg {
				return "", false
			}
			return owner, true
		}
		if filepath.Clean(d) == moduleDir {
			return "", false
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
	}
}

// embeddedAssetOwner returns the package that embeds the changed file, if any.
// A changed asset in a package subdirectory (a prompt template directory, a
// registry data file, frontend dist output) never appears in `go list` as a
// package, so without this a change to an embedded asset looks like it affects
// nothing while the embedding package's tests fail. Attribution requires a
// real //go:embed directive matching the file, so an ordinary docs or testdata
// change is still reported as outside rather than over-selecting its module's
// root package.
func (g *graph) embeddedAssetOwner(module, absFile string) (string, bool) {
	for d := filepath.Dir(filepath.Clean(absFile)); ; d = filepath.Dir(d) {
		if owner, found := g.packageAt(d); found {
			if embedsFile(d, absFile) {
				return owner, true
			}
		}
		if filepath.Clean(d) == filepath.Clean(g.moduleDirs[module]) {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
	}
	return "", false
}

// embedsFile reports whether any Go source in pkgDir has a //go:embed
// directive whose patterns cover absFile. Patterns are relative to the package
// directory, may be quoted, and may name a directory (which covers everything
// beneath it), matching the go command's own rules.
func embedsFile(pkgDir, absFile string) bool {
	rel, err := filepath.Rel(pkgDir, absFile)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			continue
		}
		for _, pattern := range embedPatterns(string(src)) {
			if embedPatternCovers(pattern, rel) {
				return true
			}
		}
	}
	return false
}

// embedPatterns extracts the patterns from every //go:embed directive in src.
// The directive sits in a comment directly above the declaration it applies
// to; the patterns are the space-separated words after the directive, with
// optional quoting.
func embedPatterns(src string) []string {
	var patterns []string
	for line := range strings.SplitSeq(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//go:embed ") && trimmed != "//go:embed" {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "//go:embed")
		patterns = append(patterns, splitEmbedPatterns(rest)...)
	}
	return patterns
}

// splitEmbedPatterns splits a directive's argument list on whitespace that is
// outside a quoted string, so the legal pattern `"x y.txt"` stays one pattern.
// strings.Fields would split it in two and attribute a file named with a space
// to nothing. Quoting is stripped; both quote forms the go command accepts are
// handled here rather than re-using go/build, whose ImportDir applies build
// constraints and would miss a directive in a file the host does not compile.
func splitEmbedPatterns(rest string) []string {
	var (
		out     []string
		current strings.Builder
		quote   rune
	)
	flush := func() {
		if current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
		}
	}
	for _, r := range rest {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '"' || r == '`':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// embedPatternCovers reports whether one //go:embed pattern selects rel, a
// slash-separated path relative to the package directory. The "all:" prefix
// drops the leading-dot exclusion, which does not change whether a named file
// matches; a pattern that names a directory covers every path beneath it.
func embedPatternCovers(pattern, rel string) bool {
	pattern = strings.TrimPrefix(pattern, "all:")
	if pattern == "" {
		return false
	}
	if ok, err := path.Match(pattern, rel); err == nil && ok {
		return true
	}
	if strings.HasPrefix(rel, pattern+"/") {
		return true
	}
	// The go command also embeds a directory's whole subtree when a glob
	// matches the directory itself: `assets/*` covers assets/icons/a.png when
	// assets/icons is a directory. Test each leading directory prefix.
	for i := 0; i < len(rel); i++ {
		if rel[i] != '/' {
			continue
		}
		if ok, err := path.Match(pattern, rel[:i]); err == nil && ok {
			return true
		}
	}
	return false
}
