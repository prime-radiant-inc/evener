package main

import (
	"path/filepath"
	"sort"
	"strings"
)

// selection is one package chosen by the diff, with the reason it is in the
// run. Reason is printed before the module runs, so a surprising selection can
// be traced to a changed file.
type selection struct {
	ModuleRoot string
	ImportPath string
	Dir        string
	Reason     string
}

// selectionResult is what selectPackages decided about a change set: the
// packages to run, the files it skipped and why, and how many Go files the
// diff touched (for the loud empty-selection failure).
type selectionResult struct {
	Selected  []selection
	Outside   []string // changed files outside every module in GO_MODULES
	NoPackage []string // changed files in a directory with no Go package
	GoChanged int
}

// selectPackages maps each changed file to its package and selects that
// package plus every package whose test-inclusive import graph reaches it.
//
// A test-only change (a _test.go file) selects only its own package: a
// dependent's tests never import another package's test files, so no other
// package can observe it. A file in a package directory that is not a Go file
// selects the package, because package-level assets (goldens, tables, config)
// are read by that package's tests.
func selectPackages(g *graph, changed []string, moduleRoots []string) selectionResult {
	var res selectionResult
	// propagating maps a changed package to a reason when its code or assets
	// changed, so its importers are affected. testOnly maps a package to its
	// reason when only its tests changed; those never spread.
	propagating := map[string]string{}
	testOnly := map[string]string{}
	// packageOnly maps a package to its reason for a change that can only be
	// observed by that package's own tests, such as a non-Go asset it reads.
	// Those never spread either.
	packageOnly := map[string]string{}
	for _, rel := range changed {
		rel = filepath.ToSlash(rel)
		goFile := strings.HasSuffix(rel, ".go")
		module, ok := g.moduleFor(rel)
		if !ok {
			if goFile {
				res.GoChanged++
			}
			res.Outside = append(res.Outside, rel)
			continue
		}
		dir := filepath.Join(g.root, filepath.FromSlash(filepath.Dir(rel)))
		ip, found := g.packageAt(dir)
		if !found {
			if nested, mod := nestedModule(g.moduleDir(module), dir); nested {
				res.Outside = append(res.Outside, rel+" (nested module "+mod+")")
				continue
			}
			if goFile {
				res.GoChanged++
			}
			res.NoPackage = append(res.NoPackage, rel)
			continue
		}
		if goFile {
			res.GoChanged++
		}
		if strings.HasSuffix(rel, "_test.go") {
			testOnly[ip] = joinReason(testOnly[ip], "test-only change "+rel)
			continue
		}
		if !goFile {
			packageOnly[ip] = joinReason(packageOnly[ip], "changed file "+rel)
			continue
		}
		propagating[ip] = joinReason(propagating[ip], "changed file "+rel)
	}

	reasonFor := map[string]string{}
	for ip, r := range testOnly {
		reasonFor[ip] = r
	}
	for ip, r := range packageOnly {
		if _, ok := reasonFor[ip]; !ok {
			reasonFor[ip] = r
		}
	}
	for ip, r := range propagating {
		reasonFor[ip] = r
	}
	// `go list -deps` is transitive, so one membership test is the whole
	// closure. testOnly seeds are deliberately absent from this loop.
	seeds := make([]string, 0, len(propagating))
	for seed := range propagating {
		seeds = append(seeds, seed)
	}
	sort.Strings(seeds)
	for ip := range g.pkgs {
		if _, seeded := reasonFor[ip]; seeded {
			continue
		}
		for _, seed := range seeds {
			if g.deps[ip][seed] {
				reasonFor[ip] = "imports " + seed
				break
			}
		}
	}

	for ip, info := range g.pkgs {
		reason, ok := reasonFor[ip]
		if !ok {
			continue
		}
		res.Selected = append(res.Selected, selection{
			ModuleRoot: info.ModuleRoot,
			ImportPath: ip,
			Dir:        info.Dir,
			Reason:     reason,
		})
	}
	order := make(map[string]int, len(moduleRoots))
	for i, m := range moduleRoots {
		order[m] = i
	}
	sort.Slice(res.Selected, func(i, j int) bool {
		a, b := res.Selected[i], res.Selected[j]
		if order[a.ModuleRoot] != order[b.ModuleRoot] {
			return order[a.ModuleRoot] < order[b.ModuleRoot]
		}
		return a.ImportPath < b.ImportPath
	})
	return res
}

func joinReason(prev, add string) string {
	if prev == "" {
		return add
	}
	return prev + ", " + add
}

// nestedModule reports whether dir sits inside a module that is not in
// GO_MODULES, such as a testdata fixture with its own go.mod. Its files are
// outside the workspace even though their path starts with a module root.
func nestedModule(moduleDir, dir string) (bool, string) {
	for d := dir; strings.HasPrefix(d, moduleDir+string(filepath.Separator)); d = filepath.Dir(d) {
		if pathExists(filepath.Join(d, "go.mod")) {
			return true, relOrSelf(moduleDir, d)
		}
	}
	return false, ""
}

func relOrSelf(base, target string) string {
	rel, err := relPath(base, target)
	if err != nil {
		return target
	}
	return rel
}

// orderedModules returns the selected modules in GO_MODULES order, the order
// the tool runs them in.
func orderedModules(sels []selection, moduleRoots []string) []string {
	present := map[string]bool{}
	for _, s := range sels {
		present[s.ModuleRoot] = true
	}
	var out []string
	for _, m := range moduleRoots {
		if present[m] {
			out = append(out, m)
		}
	}
	return out
}

// selectionsFor returns one module's selected packages in import-path order.
func selectionsFor(sels []selection, module string) []selection {
	var out []selection
	for _, s := range sels {
		if s.ModuleRoot == module {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out
}
