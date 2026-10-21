package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
}

// pkgInfo is one real (buildable, non-test-variant) package in the workspace.
type pkgInfo struct {
	ImportPath string
	ModuleRoot string // GO_MODULES entry, "." for the root module
	Dir        string // absolute
}

// graph is the workspace's test-inclusive import graph: every real package the
// modules list, plus the set of packages each one's tests depend on.
type graph struct {
	root        string
	moduleRoots []string
	moduleDirs  map[string]string          // module root -> absolute dir
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
	g := &graph{
		root:        absRoot,
		moduleRoots: moduleRoots,
		moduleDirs:  make(map[string]string, len(moduleRoots)),
		pkgs:        make(map[string]*pkgInfo),
		byDir:       make(map[string]string),
		deps:        make(map[string]map[string]bool),
	}
	for _, m := range moduleRoots {
		g.moduleDirs[m] = filepath.Join(absRoot, filepath.FromSlash(m))
	}

	type result struct {
		module  string
		entries []pkgEntry
		err     error
	}
	results := make([]result, len(moduleRoots))
	var wg sync.WaitGroup
	for i, m := range moduleRoots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries, err := listModule(g.moduleDirs[m])
			results[i] = result{module: m, entries: entries, err: err}
		}()
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
	cmd := exec.Command("go", "list", "-e", "-deps", "-test", "-json", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %v: %s", dir, err, strings.TrimSpace(stderr.String()))
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
		g.pkgs[self.ImportPath] = &pkgInfo{ImportPath: self.ImportPath, ModuleRoot: module, Dir: dir}
		g.byDir[dir] = self.ImportPath
		deps := map[string]bool{}
		for _, e := range group {
			belongs := e.ImportPath == self.ImportPath ||
				e.ForTest == self.ImportPath ||
				e.ImportPath == self.ImportPath+".test"
			if !belongs {
				continue
			}
			for _, d := range e.Deps {
				deps[d] = true
			}
		}
		delete(deps, self.ImportPath)
		g.deps[self.ImportPath] = deps
	}
}

// moduleFor maps a repo-relative path to its longest GO_MODULES root. The
// root module (".") matches everything no other module claims.
func (g *graph) moduleFor(rel string) (string, bool) {
	best := ""
	for _, m := range g.moduleRoots {
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

// moduleDir returns the absolute directory a module's tests run in.
func (g *graph) moduleDir(module string) string { return g.moduleDirs[module] }

// packageAt maps an absolute directory to the real package there.
func (g *graph) packageAt(dir string) (string, bool) {
	ip, ok := g.byDir[filepath.Clean(dir)]
	return ip, ok
}
