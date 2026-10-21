package main

import (
	"maps"
	"path/filepath"
	"slices"
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
	Removed   []string // deleted files that no surviving package can observe
	GraphOnly []string // selected packages whose module test-changed does not run
	// OutOfScope counts Go files that belong to a module test-changed does not
	// run (fuzz). They are a real change and a legitimate skip, so they are not
	// part of GoChanged and must not trip the loud-failure guard.
	OutOfScope int
	GoChanged  int
}

// Module manifests change what every package in their module can build against,
// so they select the whole module rather than the directory's one package.
var moduleManifests = map[string]bool{
	"go.mod":  true,
	"go.sum":  true,
	"go.work": true,
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
	// countGo records one changed Go file. A file in a module test-changed does
	// not run (fuzz) is a legitimate skip rather than a broken selection, so the
	// two counts are kept together and cannot drift apart.
	countGo := func(module string) {
		if g.runnableModule(module) {
			res.GoChanged++
			return
		}
		res.OutOfScope++
	}
	// propagating maps a changed package to a reason when its code or assets
	// changed, so its importers are affected. testOnly maps a package to its
	// reason when only its tests changed; those never spread.
	propagating := map[string]string{}
	testOnly := map[string]string{}
	// packageOnly maps a package to its reason for a change that can only be
	// observed by that package's own tests, such as a non-Go asset it reads.
	// Those never spread either.
	packageOnly := map[string]string{}
	// wholeModules lists modules whose manifest changed. Every package in one
	// is selected: a dependency change can break any of them, and a module
	// root that holds no Go package would otherwise be dropped entirely.
	wholeModules := map[string]string{}
	for _, rel := range changed {
		rel = filepath.ToSlash(rel)
		goFile := strings.HasSuffix(rel, ".go")
		module, ok := g.moduleFor(rel)
		// A module loaded only for its import edges (fuzz) is attributed like any
		// other: its packages are filtered out of the run when the selection is
		// built, but its importers under GO_MODULES are exactly what a change
		// there breaks, so they must come through the closure below.
		if !ok {
			if goFile {
				// Outside every module, so no module is out of scope here: this
				// is a real Go change nothing claimed, and the guard should see it.
				res.GoChanged++
			}
			res.Outside = append(res.Outside, rel)
			continue
		}
		dir := filepath.Join(g.root, filepath.FromSlash(filepath.Dir(rel)))

		// Only a manifest at a module's own root speaks for that module. A
		// go.mod deeper down belongs to a nested fixture module, which is
		// outside GO_MODULES and reported as such below.
		atModuleRoot := filepath.Clean(dir) == filepath.Clean(g.moduleDir(module))
		if moduleManifests[filepath.Base(rel)] && atModuleRoot {
			if goFile {
				countGo(module)
			}
			// go.work spans the whole workspace, so it reaches every module;
			// so does a go.mod, because go.work mode shares one MVS build list:
			// bumping a requirement in agent/go.mod changes the version every
			// root-module package compiles against, whether or not it imports
			// agent. go.sum is a checksum list for its own module and stays
			// scoped to that module.
			if filepath.Base(rel) == "go.work" {
				for _, m := range moduleRoots {
					wholeModules[m] = joinReason(wholeModules[m], "workspace manifest "+rel)
				}
			} else if filepath.Base(rel) == "go.mod" {
				for _, m := range moduleRoots {
					wholeModules[m] = joinReason(wholeModules[m], "shared build list from "+rel)
				}
			} else {
				wholeModules[module] = joinReason(wholeModules[module], "module manifest "+rel)
			}
			continue
		}

		ip, found := g.packageAt(dir)
		if !found {
			if nested, mod := nestedModule(g.moduleDir(module), dir); nested {
				res.Outside = append(res.Outside, rel+" (nested module "+mod+")")
				continue
			}
			// A deleted or renamed-away package leaves no package behind, but
			// its former importers still name it. Deriving the import path
			// from the path keeps those importers in the run; without this a
			// deletion printed PASS while the dependent build was broken.
			// A Go file can name a package that no longer exists. Treat it as a
			// removal only when some surviving package still imports that path:
			// otherwise it is a fixture or generated file living outside any
			// package (testdata), which its nearest package owns.
			if ip, derived := g.importPathAt(module, dir); derived && goFile && g.importedAnywhere(ip) {
				countGo(module)
				propagating[ip] = joinReason(propagating[ip], "removed file "+rel)
				continue
			}
			// A non-Go file below a package directory can still be embedded by
			// that package (prompt templates, registry data, dist output).
			if !goFile {
				if owner, embedded := g.embeddedAssetOwner(module, filepath.Join(g.root, filepath.FromSlash(rel))); embedded {
					propagating[owner] = joinReason(propagating[owner], "embedded asset "+rel)
					continue
				}
			}
			// A path that no longer exists is a deletion. Checked before any
			// ancestor attribution: a repo root that holds a test-only package
			// would otherwise claim every deleted file and drag in everything
			// that imports the root module. Removing a dead package with no
			// surviving importers is a complete change, so it is reported and
			// does not fail the run.
			//
			// Testdata is exempt: a deleted fixture is still read by its
			// package's tests, so it must reach the attribution below rather
			// than be dropped as unobservable. Treating it as removed reported
			// success while the owning package's tests failed.
			//
			// Non-Go files are exempt too. Only a Go file implies a package that
			// stopped existing; a deleted doc or Makefile fragment is read by
			// the repo-root audit tests, so it must reach the repository-file
			// branch below instead of being dropped as unobservable.
			if goFile && !isTestdataPath(rel) && !pathExists(filepath.Join(g.root, filepath.FromSlash(rel))) {
				res.Removed = append(res.Removed, rel)
				continue
			}
			// A Go file under testdata/ is a fixture a nearby package compiles
			// or drives, not a package itself: `go list ./...` skips it. Blame
			// its nearest package so the real consumer runs, instead of failing
			// the run for a legitimate fixture edit. It stays package-local: a
			// testdata file is not compiled into the package, so no importer can
			// observe it.
			if goFile && isTestdataPath(rel) {
				if owner, ok := g.nearestPackageAncestor(module, dir); ok {
					countGo(module)
					packageOnly[owner] = joinReason(packageOnly[owner], "testdata fixture "+rel)
					continue
				}
			}
			// A non-Go fixture under testdata/ (a golden, a table, a script) is
			// read by the nearest package's tests. It cannot be embedded, and it
			// changes nothing an importer can observe, so it stays package-local
			// instead of spreading.
			if !goFile && isTestdataPath(rel) {
				if owner, ok := g.nearestPackageAncestor(module, dir); ok {
					packageOnly[owner] = joinReason(packageOnly[owner], "testdata fixture "+rel)
					continue
				}
			}
			// The repo-root package's audit tests read repository files directly:
			// it globs make/*.mk, and checks docs/developing-evener/testing.md
			// and the workflow files. A non-Go change to one of those is
			// invisible to `go list` as a package, but it can fail those tests,
			// so it selects the root package only — no spread to importers.
			if !goFile {
				// A file inside a package's directory tree can still be read by
				// that package's tests by relative path (docs/examples/**
				// fixtures). Attribute it there first: the nearest package is
				// the one whose tests would notice, and package-only means no
				// spread. The root package is then an addition, not a
				// replacement, because the root audit tests also read deep
				// repository files.
				if owner, ok := g.nearestPackageAncestor(module, dir); ok {
					packageOnly[owner] = joinReason(packageOnly[owner], "repository file "+rel)
				}
				if owner, ok := g.packageAt(g.root); ok {
					packageOnly[owner] = joinReason(packageOnly[owner], "repository file "+rel)
					continue
				}
			}
			if goFile {
				countGo(module)
			}
			res.NoPackage = append(res.NoPackage, rel)
			continue
		}
		if goFile {
			countGo(module)
		}
		if strings.HasSuffix(rel, "_test.go") {
			testOnly[ip] = joinReason(testOnly[ip], "test-only change "+rel)
			continue
		}
		if !goFile {
			// An asset in the package's own directory (embedded seatbelt
			// profiles, an embedded installer) is compiled into the package, so
			// importers observe it exactly as they observe Go source. Anything
			// else in the directory is a test-only asset and stays local.
			if embedsFile(dir, filepath.Join(g.root, filepath.FromSlash(rel))) {
				propagating[ip] = joinReason(propagating[ip], "embedded asset "+rel)
				rootPackageReadsFile(g, packageOnly, ip, rel)
				continue
			}
			packageOnly[ip] = joinReason(packageOnly[ip], "changed file "+rel)
			rootPackageReadsFile(g, packageOnly, ip, rel)
			continue
		}
		propagating[ip] = joinReason(propagating[ip], "changed file "+rel)
	}

	reasonFor := map[string]string{}
	maps.Copy(reasonFor, testOnly)
	for ip, r := range packageOnly {
		if _, ok := reasonFor[ip]; !ok {
			reasonFor[ip] = r
		}
	}
	for module, reason := range wholeModules {
		for ip, info := range g.pkgs {
			if info.ModuleRoot != module {
				continue
			}
			propagating[ip] = joinReason(propagating[ip], reason)
		}
	}
	// The closure below only adds importers; every seed, including those the
	// manifest expansion just added, takes its own reason here.
	maps.Copy(reasonFor, propagating)
	// `go list -deps` is transitive, so one membership test is the whole
	// closure. testOnly seeds are deliberately absent from this loop.
	seeds := make([]string, 0, len(propagating))
	for seed := range propagating {
		seeds = append(seeds, seed)
	}
	sort.Strings(seeds)
	for ip := range g.pkgs {
		if _, seeded := propagating[ip]; seeded {
			continue
		}
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
		if !g.runnableModule(info.ModuleRoot) {
			// The module is loaded for its import edges only (fuzz), so its own
			// tests are out of scope by design. Say so instead of dropping it:
			// otherwise a fuzz-only diff selects nothing with GoChanged > 0 and
			// exits 2, which reads as a broken change.
			res.GraphOnly = append(res.GraphOnly, ip+" (module "+info.ModuleRoot+", not run by test-changed)")
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

// rootPackageReadsFile adds the repo-root package whenever a non-Go file is
// attributed to some other package. The root audit tests read repository files
// by path even when those files live inside another package's directory tree
// (test/scenarios/*.md, docs/examples/**), so attributing only to the nearest
// package would skip them and let a broken scenario card report PASS.
func rootPackageReadsFile(g *graph, packageOnly map[string]string, owner, rel string) {
	rootPkg, ok := g.packageAt(g.root)
	if !ok || rootPkg == owner {
		return
	}
	packageOnly[rootPkg] = joinReason(packageOnly[rootPkg], "repository file "+rel)
}

func joinReason(prev, add string) string {
	if prev == "" {
		return add
	}
	return prev + ", " + add
}

// isTestdataPath reports whether a repo-relative path sits under a testdata
// directory, the go command's own convention for files that belong to tests
// rather than to a package.
func isTestdataPath(rel string) bool {
	return slices.Contains(strings.Split(rel, "/"), "testdata")
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
