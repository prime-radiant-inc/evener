package main

import (
	"sort"
	"strings"
	"testing"
)

// These cases pin the three ways the selector used to report a passing run for
// a diff that genuinely broke something: a deleted package, an embedded asset,
// and a module manifest. Each one is written against this checkout's real
// graph, like the rest of the selection tests.

func selectedPaths(res selectionResult) []string {
	out := make([]string, 0, len(res.Selected))
	for _, s := range res.Selected {
		out = append(out, s.ImportPath)
	}
	sort.Strings(out)
	return out
}

func reasonForPath(res selectionResult, importPath string) string {
	for _, s := range res.Selected {
		if s.ImportPath == importPath {
			return s.Reason
		}
	}
	return ""
}

// A deleted package is no longer in `go list`, so it has no directory entry to
// map to. Its former importers still name it, and they are what actually fail
// to build. Selecting nothing here let `go build ./...` fail while
// test-changed printed PASS.
//
// The graph is built by hand because the package has to be absent from it: in
// this checkout the real package exists, and a test that loads the live graph
// could only ever exercise the "directory still there" path.
func TestDeletedPackageSelectsItsImporters(t *testing.T) {
	const (
		moduleRoot = "."
		modulePath = "example.com/mod"
		removed    = "example.com/mod/gone"
		dependent  = "example.com/mod/app"
		unrelated  = "example.com/mod/other"
	)
	root := t.TempDir()
	g := &graph{
		root:        root,
		moduleRoots: []string{moduleRoot},
		moduleDirs:  map[string]string{moduleRoot: root},
		modulePaths: map[string]string{moduleRoot: modulePath},
		runnable:    map[string]bool{moduleRoot: true},
		pkgs: map[string]*pkgInfo{
			dependent: {ImportPath: dependent, ModuleRoot: moduleRoot, Dir: "app"},
			unrelated: {ImportPath: unrelated, ModuleRoot: moduleRoot, Dir: "other"},
		},
		byDir: map[string]string{"app": dependent, "other": unrelated},
		deps: map[string]map[string]bool{
			dependent: {removed: true},
			unrelated: {},
		},
	}

	res := selectPackages(g, []string{"gone/gone.go", "gone/gone_test.go"}, []string{moduleRoot})

	if got := reasonForPath(res, dependent); got == "" {
		t.Errorf("%s imports the removed package and must be selected", dependent)
	}
	if reasonForPath(res, unrelated) != "" {
		t.Errorf("%s does not import the removed package and must not be selected", unrelated)
	}
	for _, p := range selectedPaths(res) {
		if p == removed {
			t.Errorf("selected %q, but that package no longer exists", p)
		}
	}
	if res.GoChanged != 2 {
		t.Errorf("GoChanged = %d, want 2", res.GoChanged)
	}
}

// A file below a package directory that the package embeds is invisible to
// `go list` as a package, and its package's tests read it at compile time.
// Skipping it made a changed bundled agent prompt report "nothing to run"
// while TestBundledPromptBodiesOnlyNameToolsTheirAgentHas failed.
func TestEmbeddedAssetSelectsItsEmbeddingPackage(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"internal/bundled/agents/doctor.md"}, testModules)

	const owner = "primeradiant.com/evener/internal/bundled"
	if got := reasonForPath(res, owner); !strings.Contains(got, "embedded asset") {
		t.Errorf("reason for %s = %q, want it to name the embedded asset", owner, got)
	}
	// The asset is embedded, so importers observe it too: the agent tests read
	// the bundled prompt bodies.
	if reasonForPath(res, "primeradiant.com/evener/agent") == "" {
		t.Error("the agent package imports internal/bundled and must be selected")
	}
}

// A non-Go file that nothing embeds must not drag in its module's packages.
// Otherwise every README or testdata edit runs the whole module again, which
// is the cost this target exists to remove.
func TestOrdinaryNonGoFileSelectsNothing(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"docs/developing-evener/testing.md"}, testModules)
	if len(res.Selected) != 0 {
		t.Errorf("selected %v for an unembedded doc change, want nothing", selectedPaths(res))
	}
}

// go.mod changes what every package in its module can build against, and a
// module root that holds no package at all used to vanish entirely.
func TestModuleManifestSelectsTheWholeModule(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"agent/go.mod"}, testModules)

	var agentPackages int
	for _, info := range g.pkgs {
		if info.ModuleRoot == "agent" {
			agentPackages++
		}
	}
	var selectedAgent int
	for _, s := range res.Selected {
		if s.ModuleRoot == "agent" {
			selectedAgent++
		}
	}
	if selectedAgent != agentPackages {
		t.Errorf("agent/go.mod selected %d of %d agent packages, want all",
			selectedAgent, agentPackages)
	}
}

// A module whose root holds no Go package used to be dropped silently, so its
// go.mod edit exited 0 with "diff touches no Go package".
func TestRootlessModuleManifestIsNotDropped(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"execsupport/go.mod"}, testModules)

	var selectedExecsupport int
	for _, s := range res.Selected {
		if s.ModuleRoot == "execsupport" {
			selectedExecsupport++
		}
	}
	if selectedExecsupport == 0 {
		t.Error("execsupport/go.mod selected no execsupport package")
	}
	// go.mod is not a Go file, so it does not count toward the loud-failure
	// guard; the selection above is what keeps this honest.
}

// The workspace manifest spans every module, so it cannot be attributed to one.
func TestWorkspaceManifestSelectsEveryModule(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"go.work"}, testModules)

	seen := map[string]bool{}
	for _, s := range res.Selected {
		seen[s.ModuleRoot] = true
	}
	for _, m := range testModules {
		if !seen[m] {
			t.Errorf("go.work selected no package in module %q", m)
		}
	}
}

// embedsFile and embedPatternCovers are the attribution mechanism; keep their
// cases next to the tests that depend on them.
func TestEmbedPatternCovers(t *testing.T) {
	cases := []struct {
		pattern string
		rel     string
		want    bool
	}{
		{"agents/*.md", "agents/doctor.md", true},
		{"agents", "agents/doctor.md", true},
		{"all:agents/*.md", "agents/doctor.md", true},
		{"agents/*.md", "skills/x.md", false},
		{"prompts/system.md.tmpl", "prompts/system.md.tmpl", true},
		{"", "anything", false},
	}
	for _, c := range cases {
		if got := embedPatternCovers(c.pattern, c.rel); got != c.want {
			t.Errorf("embedPatternCovers(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}
