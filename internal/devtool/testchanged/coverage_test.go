package main

import (
	"os"
	"path/filepath"
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

// workspaceModules must read go.work with the module grammar, not with a line
// scanner: a trailing comment on a `use` line belongs to the comment, not to
// the module path, and a quoted path keeps its spaces.
func TestWorkspaceModulesParsesCommentsAndQuoting(t *testing.T) {
	root := t.TempDir()
	work := "go 1.27.0\n\nuse (\n\t./known // the module we already run\n\t./extra\n\t\"./odd name\"\n)\n"
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte(work), 0o644); err != nil {
		t.Fatalf("writing go.work: %v", err)
	}

	got := workspaceModules(root, []string{".", "known"})

	want := []string{"extra", "odd name"}
	if len(got) != len(want) {
		t.Fatalf("workspaceModules = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("workspaceModules = %v, want %v", got, want)
		}
	}
	for _, m := range got {
		if strings.Contains(m, "//") {
			t.Errorf("workspaceModules = %v, a comment leaked into a module path", got)
		}
	}
}

// A quoted embed pattern may legally contain a space; splitting it with
// strings.Fields turned "x y.txt" into two patterns and attributed a changed
// file of that name to nothing.
func TestSplitEmbedPatternsKeepsQuotedSpaces(t *testing.T) {
	got := splitEmbedPatterns(` assets/*.md "x y.txt" ` + "`back tick.txt`")
	want := []string{"assets/*.md", "x y.txt", "back tick.txt"}
	if len(got) != len(want) {
		t.Fatalf("splitEmbedPatterns = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitEmbedPatterns = %q, want %q", got, want)
		}
	}
}

// Removing a package that nothing imports is a complete change, not a broken
// one. Counting its files toward the loud-empty-selection guard made the gate
// fail on a legitimate dead-code removal, so a path that is gone from the
// worktree is recorded as removed and left out of that count.
func TestDeletedUnimportedFileIsReportedNotFailed(t *testing.T) {
	const (
		moduleRoot = "."
		modulePath = "example.com/mod"
	)
	root := t.TempDir()
	g := &graph{
		root:        root,
		moduleRoots: []string{moduleRoot},
		moduleDirs:  map[string]string{moduleRoot: root},
		modulePaths: map[string]string{moduleRoot: modulePath},
		runnable:    map[string]bool{moduleRoot: true},
		pkgs:        map[string]*pkgInfo{},
		byDir:       map[string]string{},
		deps:        map[string]map[string]bool{},
	}

	res := selectPackages(g, []string{"dead/dead.go"}, []string{moduleRoot})

	if len(res.Removed) != 1 || res.Removed[0] != "dead/dead.go" {
		t.Errorf("Removed = %v, want [dead/dead.go]", res.Removed)
	}
	if res.GoChanged != 0 {
		t.Errorf("GoChanged = %d, want 0 so the empty-selection guard does not fail the run", res.GoChanged)
	}
}

// An embedded asset sitting directly in its package directory is compiled into
// that package, so importers observe it exactly as they observe Go source. It
// used to be treated as a test-only asset and never reached them, which skipped
// the hub tests that compile those bytes in.
func TestEmbeddedAssetInPackageDirReachesImporters(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"internal/remoteinstall/install.sh"}, testModules)

	const owner = "primeradiant.com/evener/internal/remoteinstall"
	if got := reasonForPath(res, owner); !strings.Contains(got, "embedded asset") {
		t.Errorf("reason for %s = %q, want it to name the embedded asset", owner, got)
	}
	const importer = "primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	if reasonForPath(res, importer) == "" {
		t.Errorf("%s imports the embedding package and must be selected", importer)
	}
}
