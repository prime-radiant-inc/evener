package main

import (
	"bytes"
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
			dependent: {ModuleRoot: moduleRoot, Dir: "app"},
			unrelated: {ModuleRoot: moduleRoot, Dir: "other"},
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

// A repository file that no package embeds or reads is consumed only by the
// repo-root audit suite, which test-changed does not run (it costs ~43s). The
// file is reported as audit-only rather than silently ignored, and no package
// is selected for it.
func TestRepositoryFileIsReportedAsAuditOnly(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"docs/developing-evener/testing.md"}, testModules)

	if len(res.Selected) != 0 {
		t.Errorf("selected %v, want nothing for an audit-only file", selectedPaths(res))
	}
	if len(res.AuditOnly) != 1 || res.AuditOnly[0] != "docs/developing-evener/testing.md" {
		t.Errorf("AuditOnly = %v, want [docs/developing-evener/testing.md]", res.AuditOnly)
	}
}

// A deleted repository file is audit-only too; it must not be counted as a
// broken Go selection, and it must not disappear without a word.
func TestDeletedRepositoryFileIsAuditOnly(t *testing.T) {
	g := loadTestGraph(t)
	// A path that is absent from this checkout: the point is the deleted case.
	res := selectPackages(g, []string{"docs/removed-guide.md"}, testModules)

	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v, want nothing: a deleted doc is not a removed Go package", res.Removed)
	}
	if len(res.AuditOnly) != 1 || res.AuditOnly[0] != "docs/removed-guide.md" {
		t.Errorf("AuditOnly = %v, want the deleted doc", res.AuditOnly)
	}
	if res.GoChanged != 0 {
		t.Errorf("GoChanged = %d, want 0 for a deleted non-Go file", res.GoChanged)
	}
}

// A scenario card lives inside the test/scenarios package's directory, so that
// package's tests run. The repo-root audit suite also reads it, but test-changed
// deliberately leaves root audits to the full gate in CI (they cost ~43s), so
// the root package is not selected. See docs/developing-evener/testing.md.
func TestScenarioCardSelectsOwningPackage(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"test/scenarios/lazy-transcript-loading.md"}, testModules)

	const owner = "primeradiant.com/evener/test/scenarios"
	if reasonForPath(res, owner) == "" {
		t.Errorf("selected %v, want %s", selectedPaths(res), owner)
	}
	if reasonForPath(res, "primeradiant.com/evener") != "" {
		t.Error("root audits are out of scope for test-changed; the root package must not be selected")
	}
}

// A test-only change selects its own package and nothing else. The root audit
// suite would notice it, but root audits belong to the full gate.
func TestTestOnlyChangeSelectsOnlyItsPackage(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"cmd/evener-hub/app_threadread_forward_compat_test.go"}, testModules)

	if reasonForPath(res, "primeradiant.com/evener/cmd/evener-hub") == "" {
		t.Error("the changed package must be selected")
	}
	if got := selectedPaths(res); len(got) != 1 {
		t.Errorf("selected %v, want only the changed package", got)
	}
}

// In go.work mode every module shares one MVS build list, so a go.mod bump in
// any module changes what every module compiles against, not only the module
// that owns the file. go.sum stays scoped to its own module.
func TestModuleManifestSelectsEveryModule(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"agent/go.mod"}, testModules)

	seen := map[string]bool{}
	for _, s := range res.Selected {
		seen[s.ModuleRoot] = true
	}
	for _, m := range testModules {
		if !seen[m] {
			t.Errorf("agent/go.mod selected no package in module %q; the workspace shares one build list", m)
		}
	}
}

// go.sum belongs to its own module's checksums and must not pull in the rest.
func TestGoSumStaysScopedToItsModule(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"agent/go.sum"}, testModules)

	seen := map[string]bool{}
	for _, s := range res.Selected {
		seen[s.ModuleRoot] = true
	}
	// The repo-root package comes along as a repository file, but go.sum must
	// not whole-module-select the other gate modules the way go.mod does.
	for _, m := range testModules {
		if m != "agent" && m != "." && seen[m] {
			t.Errorf("agent/go.sum whole-module selected %q; only go.mod shares the build list", m)
		}
	}
	if !seen["agent"] {
		t.Error("agent/go.sum selected nothing in agent")
	}
}

// A fixture inside a package's directory tree is read by that package's tests
// by relative path, so it must select that package, not only the repo root.
func TestFixtureUnderPackageTreeSelectsThatPackage(t *testing.T) {
	g := loadTestGraph(t)
	res := selectPackages(g, []string{"docs/examples/extensions/commands/summarize-change.md"}, testModules)

	const owner = "primeradiant.com/evener/docs/examples/extensions"
	if reasonForPath(res, owner) == "" {
		t.Errorf("selected %v, want %s for its own fixture", selectedPaths(res), owner)
	}
	// Root audits are out of scope, so the fixture's own package is the whole
	// selection.
	if got := selectedPaths(res); len(got) != 1 {
		t.Errorf("selected %v, want only %s", got, owner)
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
		{"assets/*", "assets/icons/a.png", true},
		{"assets/*", "other/a.png", false},
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
	// An ancestor package exists, as it does at a repo root that holds a
	// test-only package. The removed check must still win: otherwise a deleted
	// file is tagged as a fixture of the ancestor and every package importing
	// that ancestor is selected, which is close to the whole workspace.
	ancestorDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(ancestorDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	g := &graph{
		root:        root,
		moduleRoots: []string{moduleRoot},
		moduleDirs:  map[string]string{moduleRoot: root},
		modulePaths: map[string]string{moduleRoot: modulePath},
		runnable:    map[string]bool{moduleRoot: true},
		pkgs: map[string]*pkgInfo{
			modulePath: {ModuleRoot: moduleRoot, Dir: root},
		},
		byDir: map[string]string{root: modulePath},
		deps:  map[string]map[string]bool{},
	}

	res := selectPackages(g, []string{"sub/dead/dead.go"}, []string{moduleRoot})

	if len(res.Removed) != 1 || res.Removed[0] != "sub/dead/dead.go" {
		t.Errorf("Removed = %v, want [sub/dead/dead.go]", res.Removed)
	}
	if res.GoChanged != 0 {
		t.Errorf("GoChanged = %d, want 0 so the empty-selection guard does not fail the run", res.GoChanged)
	}
	// The root audit package comes along (its audits walk tracked files, and a
	// deletion is a tracked-file change), but nothing else should.
	for _, p := range selectedPaths(res) {
		if p != modulePath {
			t.Errorf("Selected = %v, want only the root package %s", selectedPaths(res), modulePath)
		}
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

// A deleted testdata fixture is still read by its owning package's tests. The
// removed shortcut once swallowed it, so a deleted fixture reported success
// while the owning package's tests failed.
func TestDeletedTestdataFixtureSelectsOwningPackage(t *testing.T) {
	const (
		moduleRoot = "."
		modulePath = "example.com/mod"
	)
	root := t.TempDir()
	ownerDir := filepath.Join(root, "pkg")
	g := &graph{
		root:        root,
		moduleRoots: []string{moduleRoot},
		moduleDirs:  map[string]string{moduleRoot: root},
		modulePaths: map[string]string{moduleRoot: modulePath},
		runnable:    map[string]bool{moduleRoot: true},
		pkgs: map[string]*pkgInfo{
			modulePath + "/pkg": {ModuleRoot: moduleRoot, Dir: ownerDir},
		},
		byDir: map[string]string{ownerDir: modulePath + "/pkg"},
		deps:  map[string]map[string]bool{},
	}

	res := selectPackages(g, []string{"pkg/testdata/golden.json"}, []string{moduleRoot})

	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v, want nothing: a testdata fixture is observable by its package", res.Removed)
	}
	if reasonForPath(res, modulePath+"/pkg") == "" {
		t.Errorf("Selected = %v, want %s selected for its deleted fixture", selectedPaths(res), modulePath+"/pkg")
	}
}

// A change confined to a module test-changed does not run (fuzz) is a
// legitimate skip, not a failed selection: exit 0 with an explanation.
func TestGraphOnlyModuleChangeIsSkippedNotFailed(t *testing.T) {
	const (
		moduleRoot = "."
		modulePath = "example.com/mod"
	)
	root := t.TempDir()
	fuzzDir := filepath.Join(root, "fuzzmod", "x")
	if err := os.MkdirAll(fuzzDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	g := &graph{
		root:        root,
		moduleRoots: []string{moduleRoot},
		moduleDirs:  map[string]string{moduleRoot: root, "fuzzmod": filepath.Join(root, "fuzzmod")},
		modulePaths: map[string]string{moduleRoot: modulePath, "fuzzmod": "example.com/fuzzmod"},
		runnable:    map[string]bool{moduleRoot: true},
		// A real package in the graph-only module is what the per-file path
		// counts; without one the file never reaches the counting branch.
		pkgs: map[string]*pkgInfo{
			"example.com/fuzzmod/x": {ModuleRoot: "fuzzmod", Dir: fuzzDir},
		},
		byDir: map[string]string{fuzzDir: "example.com/fuzzmod/x"},
		deps:  map[string]map[string]bool{},
	}

	res := selectPackages(g, []string{"fuzzmod/x/x.go"}, []string{moduleRoot})

	if len(res.Selected) != 0 {
		t.Errorf("Selected = %v, want nothing: fuzzmod is not a gate module", selectedPaths(res))
	}
	if res.GoChanged != 0 {
		t.Errorf("GoChanged = %d, want 0 so the loud-failure guard does not fire", res.GoChanged)
	}
	if res.OutOfScope != 1 {
		t.Errorf("OutOfScope = %d, want 1", res.OutOfScope)
	}
}

// run() owns the exit status a developer's pre-push check depends on: 0 for a
// diff with no Go work, 2 when Go files changed but nothing was selected, and 1
// when a selected package fails. An earlier version had no test here, so the
// loud-failure guard could regress unnoticed.
func TestRunExitStatus(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  int
	}{
		{
			name:  "unchanged tree",
			setup: func(t *testing.T, root string) {},
			want:  0,
		},
		{
			name: "non-Go change only",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "README.md"), "changed\n")
			},
			want: 0,
		},
		{
			// A new package selects itself, so the loud-failure guard only
			// fires when no package covers the file at all: here the module
			// root holds no Go package and the file sits directly in it.
			name: "Go file with no package",
			setup: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, "pkg")); err != nil {
					t.Fatalf("removing pkg: %v", err)
				}
				writeFile(t, filepath.Join(root, "testdata", "gen", "x.go"), "package gen\n")
			},
			want: 2,
		},
		{
			name: "selected package fails",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "pkg", "x_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"boom\") }\n")
			},
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := newTestModule(t)
			gitIn(t, root, "add", ".")
			gitIn(t, root, "commit", "-q", "-m", "base")
			c.setup(t, root)

			// Capture both streams: a passing suite must not print the failure
			// output of the case that expects one, and that output is itself
			// the evidence the failure was reported.
			var stdout, stderr bytes.Buffer
			if got := run(&stdout, &stderr, root, "HEAD", []string{"."}, false); got != c.want {
				t.Fatalf("run() = %d, want %d\nstdout:\n%s\nstderr:\n%s", got, c.want, stdout.String(), stderr.String())
			}
			if c.want != 0 && !strings.Contains(stderr.String(), "FAIL") {
				t.Errorf("exit %d without a FAIL verdict on stderr; stderr:\n%s", c.want, stderr.String())
			}
		})
	}
}

// newTestModule writes a one-package module under a fresh temp dir.
func newTestModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "user.email", "test@example.com")
	gitIn(t, root, "config", "user.name", "test")
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.27.0\n")
	writeFile(t, filepath.Join(root, "go.work"), "go 1.27.0\n\nuse .\n")
	writeFile(t, filepath.Join(root, "pkg", "x.go"), "package pkg\n")
	writeFile(t, filepath.Join(root, "pkg", "x_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
