package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The cases below run against this checkout's real `go list -deps -test`
// output. There are no fake package graphs: a selection that passes here is
// one the tool will make on this tree.
var testModules = []string{".", "agent", "llm", "auth", "envvars", "invariant", "identifier", "execsupport"}

// graphOnce loads the workspace graph for the whole test binary; every case
// reads the same real listing.
var (
	graphOnce   *graph
	errGraph    error
	graphLoaded bool
)

func loadTestGraph(t *testing.T) *graph {
	t.Helper()
	if graphLoaded {
		if errGraph != nil {
			t.Fatalf("loadGraph: %v", errGraph)
		}
		return graphOnce
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	graphOnce, errGraph = loadGraph(root, testModules)
	graphLoaded = true
	if errGraph != nil {
		t.Fatalf("loadGraph: %v", errGraph)
	}
	return graphOnce
}

// repoRoot walks up from the test binary's working directory to the tree that
// holds go.work and the workspace modules.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if pathExists(filepath.Join(dir, "go.work")) && pathExists(filepath.Join(dir, "agent", "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// importsOf reports whether pkg's test-inclusive dependency set names want.
func importsOf(g *graph, pkg, want string) bool {
	return g.deps[pkg][want]
}

func selectedSet(res selectionResult) map[string]string {
	out := make(map[string]string, len(res.Selected))
	for _, s := range res.Selected {
		out[s.ImportPath] = s.Reason
	}
	return out
}

func TestSelection(t *testing.T) {
	g := loadTestGraph(t)

	cases := []struct {
		name    string
		changed []string
		// wantSelected lists import paths that must be selected; wantAbsent
		// lists ones that must not be.
		wantSelected []string
		wantAbsent   []string
		wantOutside  []string
		wantGoFiles  int
	}{
		{
			name:        "changed file selects package and importers",
			changed:     []string{"agent/argrepair/argrepair.go"},
			wantGoFiles: 1,
			wantSelected: []string{
				"primeradiant.com/evener/agent/argrepair",
				"primeradiant.com/evener/agent",
				// Root-module packages that import the agent module's argrepair.
				"primeradiant.com/evener/internal/appoverlay",
				"primeradiant.com/evener/internal/apptranscript",
				"primeradiant.com/evener/internal/transcriptindex",
			},
			wantAbsent: []string{"primeradiant.com/evener/agent/doctor"},
		},
		{
			name:        "test-only file selects only its package",
			changed:     []string{"agent/activitybound/activitybound_test.go"},
			wantGoFiles: 1,
			wantSelected: []string{
				"primeradiant.com/evener/agent/activitybound",
			},
			wantAbsent: []string{
				"primeradiant.com/evener/agent",
				"primeradiant.com/evener/agent/execenv",
			},
		},
		{
			name:        "docs-only selects nothing",
			changed:     []string{"docs/developing-evener/testing.md"},
			wantGoFiles: 0,
		},
		{
			name:        "llm change selects another module's dependents",
			changed:     []string{"llm/adapter_timeout.go"},
			wantGoFiles: 1,
			wantSelected: []string{
				"primeradiant.com/evener/llm",
				// agent module depends on llm.
				"primeradiant.com/evener/agent",
				"primeradiant.com/evener/agent/argrepair",
				// root module depends on llm.
				"primeradiant.com/evener/cmd/evener",
			},
			// No package in auth/envvars/invariant/identifier/execsupport
			// imports llm, so nothing from them may be selected.
			wantAbsent: []string{"primeradiant.com/evener/auth"},
		},
		{
			name:        "root appwire change selects importing cmd packages",
			changed:     []string{"appwire/client.go"},
			wantGoFiles: 1,
			wantSelected: []string{
				"primeradiant.com/evener/appwire",
				"primeradiant.com/evener/cmd/evener",
				"primeradiant.com/evener/cmd/evener-hub",
			},
		},
		{
			name:         "non-go file in a package directory selects that package",
			changed:      []string{"cmd/evener-hub/host_request_methods.txt"},
			wantGoFiles:  0,
			wantSelected: []string{"primeradiant.com/evener/cmd/evener-hub"},
		},
		{
			name:        "file outside every module is reported",
			changed:     []string{"agent/testdata/memory-eval/recall/task/go.mod"},
			wantGoFiles: 0,
			wantOutside: []string{
				"agent/testdata/memory-eval/recall/task/go.mod",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := selectPackages(g, tc.changed, testModules)
			got := selectedSet(res)
			for _, want := range tc.wantSelected {
				if _, ok := got[want]; !ok {
					t.Errorf("want %s selected, got %s", want, sortedKeys(got))
				}
			}
			for _, absent := range tc.wantAbsent {
				if _, ok := got[absent]; ok {
					t.Errorf("want %s absent, got %s", absent, sortedKeys(got))
				}
			}
			for _, want := range tc.wantOutside {
				if !slices.ContainsFunc(res.Outside, func(s string) bool {
					return s == want || strings.HasPrefix(s, want+" ")
				}) {
					t.Errorf("want %s reported outside modules, got %v", want, res.Outside)
				}
			}
			if res.GoChanged != tc.wantGoFiles {
				t.Errorf("GoChanged = %d, want %d for %v", res.GoChanged, tc.wantGoFiles, tc.changed)
			}
		})
	}
}

// TestSelectionReachesEveryImporter checks the closure against go list
// directly: every package in the workspace whose test-inclusive deps name llm
// must be selected by an llm change, and nothing else may be.
func TestSelectionReachesEveryImporter(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"llm/llm.go"}, testModules)
	got := selectedSet(res)

	var want []string
	for ip := range g.pkgs {
		if ip == "primeradiant.com/evener/llm" || importsOf(g, ip, "primeradiant.com/evener/llm") {
			want = append(want, ip)
		}
	}
	sort.Strings(want)
	var have []string
	for ip := range got {
		have = append(have, ip)
	}
	sort.Strings(have)
	if !slices.Equal(want, have) {
		t.Fatalf("llm change selected %d packages, go list's importers are %d\nmissing: %v\nextra: %v",
			len(have), len(want), difference(want, have), difference(have, want))
	}
}

// TestSelectionReachesTransitiveImporters proves the closure is transitive and
// stays inside the test-inclusive graph: a change at the bottom of a chain
// selects both hops.
func TestSelectionReachesTransitiveImporters(t *testing.T) {
	g := loadTestGraph(t)
	// llm <- agent/phrasebook (if present) or another intermediate <- a root
	// package. Find a real two-hop chain rather than assuming one.
	seed := "primeradiant.com/evener/llm"
	mid, leaf := "", ""
	for ip := range g.pkgs {
		if ip == seed || !importsOf(g, ip, seed) {
			continue
		}
		for other := range g.pkgs {
			if other != ip && importsOf(g, other, ip) {
				mid, leaf = ip, other
				break
			}
		}
		if mid != "" {
			break
		}
	}
	if mid == "" || leaf == "" {
		t.Skip("no two-hop importer chain from llm in this checkout")
	}
	res := selectPackages(g, []string{"llm/llm.go"}, testModules)
	got := selectedSet(res)
	for _, want := range []string{seed, mid, leaf} {
		if _, ok := got[want]; !ok {
			t.Errorf("want %s selected for an llm change, got %s", want, sortedKeys(got))
		}
	}
}

func TestOrdering(t *testing.T) {
	sels := []selection{
		{ModuleRoot: "agent", ImportPath: "primeradiant.com/evener/agent/z"},
		{ModuleRoot: ".", ImportPath: "primeradiant.com/evener/b"},
		{ModuleRoot: "agent", ImportPath: "primeradiant.com/evener/agent/a"},
		{ModuleRoot: ".", ImportPath: "primeradiant.com/evener/a"},
	}
	got := orderedModules(sels, testModules)
	want := []string{".", "agent"}
	if !slices.Equal(got, want) {
		t.Fatalf("orderedModules = %v, want %v", got, want)
	}
	rootSels := selectionsFor(sels, ".")
	if rootSels[0].ImportPath != "primeradiant.com/evener/a" || rootSels[1].ImportPath != "primeradiant.com/evener/b" {
		t.Fatalf("selectionsFor order = %v", rootSels)
	}
}

func TestModuleForPicksLongestRoot(t *testing.T) {
	g := &graph{moduleRoots: testModules}
	cases := []struct{ rel, want string }{
		{"llm/llm.go", "llm"},
		{"llm/providers/anthropic/x.go", "llm"},
		{"agent/argrepair/argrepair.go", "agent"},
		{"appwire/client.go", "."},
		{"cmd/evener/main.go", "."},
	}
	for _, c := range cases {
		if got, ok := g.moduleFor(c.rel); !ok || got != c.want {
			t.Errorf("moduleFor(%q) = %q, %v; want %q, true", c.rel, got, ok, c.want)
		}
	}
}

// TestPackageArgIsModuleRelative pins the argument form `go test` receives
// from a module directory.
func TestPackageArgIsModuleRelative(t *testing.T) {
	g := loadTestGraph(t)
	s := selection{ModuleRoot: "agent", ImportPath: "primeradiant.com/evener/agent/argrepair", Dir: filepath.Join(g.moduleDir("agent"), "argrepair")}
	if got := packageArg(g, s); got != "./argrepair" {
		t.Fatalf("packageArg = %q, want ./argrepair", got)
	}
	root := selection{ModuleRoot: ".", ImportPath: "primeradiant.com/evener", Dir: g.moduleDir(".")}
	if got := packageArg(g, root); got != "." {
		t.Fatalf("packageArg(root) = %q, want .", got)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func difference(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	var out []string
	for _, v := range a {
		if !set[v] {
			out = append(out, v)
		}
	}
	return out
}

// TestSelectionEmptyForNonGoDiff pins the counts run() decides between: a
// non-Go diff counts no Go files (so it can exit 0), while a Go diff does. A
// repository file may still select the root package, whose audit tests read it
// directly; what matters here is that it is not counted as an unrun Go change.
func TestSelectionEmptyForNonGoDiff(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"docs/developing-evener/testing.md"}, testModules)
	if res.GoChanged != 0 {
		t.Fatalf("GoChanged = %d for a docs-only change", res.GoChanged)
	}
	if res.OutOfScope != 0 {
		t.Fatalf("OutOfScope = %d for a docs-only change", res.OutOfScope)
	}
	res = selectPackages(g, []string{"agent/argrepair/argrepair.go"}, testModules)
	if res.GoChanged == 0 {
		t.Fatal("GoChanged = 0 for a .go change")
	}
	if len(res.Selected) == 0 {
		t.Fatal("a .go change selected nothing")
	}
}

// TestSelectionReasonsNameTheChangedFile keeps the printed reason traceable to
// the diff.
func TestSelectionReasonsNameTheChangedFile(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"agent/argrepair/argrepair.go"}, testModules)
	for _, s := range res.Selected {
		if s.ImportPath == "primeradiant.com/evener/agent/argrepair" {
			if !strings.Contains(s.Reason, "agent/argrepair/argrepair.go") {
				t.Fatalf("changed package reason %q does not name the file", s.Reason)
			}
			return
		}
	}
	t.Fatalf("changed package not selected: %v", sortedKeys(selectedSet(res)))
}
