package cilanes

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixtureFiles is a small workspace shaped like the real one: a root module
// holding the hub, the dev binary and the evener command, a separate agent
// module, a llm module that nothing in the root module imports, and the
// TypeScript trees beside them.
func fixtureFiles() map[string]string {
	return map[string]string{
		"go.work": "go 1.27.0\n\nuse (\n\t.\n\t./agent\n\t./llm\n)\n",
		"go.mod":  "module fixture.test/repo\n\ngo 1.27.0\n",
		"evener_test.go": `package repo_test

import "os"

var _, _ = os.ReadFile("README.md")
`,
		"cmd/evener-hub/hub.go": `package hub

import "fixture.test/repo/agent/session"

var _ = session.Name
`,
		"cmd/evener-hub/webnext.go": `package hub

import "embed"

//go:embed frontend/dist
var dist embed.FS
`,
		"cmd/evener-hub/hub_test.go": `package hub

import (
	"os"
	"os/exec"
	"path/filepath"
)

var _ = exec.Command("go", "build", "-o", "bin", "./cmd/evener/")
var _, _ = os.ReadFile(filepath.Join("frontend", "src", "turn.ts"))
`,
		"cmd/evener/main.go": `package main

import (
	_ "fixture.test/repo/cmd/evener-hub"
	"fixture.test/repo/cmd/evener/internal/cli"
)

func main() { cli.Run() }
`,
		// Like internal/devtool/shardrun: production code naming the
		// command it builds, which agent tests import.
		"internal/prebuilt/prebuilt.go": "package prebuilt\n\nconst Target = \"./cmd/evener\"\n",
		"agent/leaf/leaf_test.go": `package leaf

import "fixture.test/repo/internal/prebuilt"

var _ = prebuilt.Target
`,
		"agent/session/session_test.go":  "package session\n",
		"cmd/evener/internal/cli/cli.go": "package cli\n\nfunc Run() {}\n",
		"cmd/evener-dev/bin/main.go": `package main

import "fixture.test/repo/agent/tool"

func main() { tool.Use() }
`,
		"cmd/evener-doctor/doctor.go":         "package doctor\n",
		"cmd/evener-hub/testdata/fixture.go":  "package fixture\n",
		"cmd/evener-hub/testdata/fixture.txt": "data\n",
		"agent/go.mod":                        "module fixture.test/repo/agent\n\ngo 1.27.0\n",
		"agent/session/session.go":            "package session\n\nconst Name = \"s\"\n",
		"agent/tool/tool.go":                  "package tool\n\nfunc Use() {}\n",
		"agent/leaf/leaf.go":                  "package leaf\n\nimport \"strings\"\n\nvar _ = strings.Split(\"a.b\", \".\")\nvar _ = \"..\"\nvar _ = \"main.go\"\n",
		"agent/testdata/task/go.mod":          "module task\n",
		"agent/testdata/task/task.go":         "package task\n",
		"llm/go.mod":                          "module fixture.test/repo/llm\n\ngo 1.27.0\n",
		"llm/llm.go":                          "package llm\n",
		"llm/llm_test.go": `package llm

import (
	"os"
	"path/filepath"
)

var _, _ = os.ReadFile(filepath.Join("..", "mobile-native", "app.json"))
`,
		"tools/standalone/go.mod":                         "module standalone\n",
		"tools/standalone/main.go":                        "package main\n",
		"cmd/evener-hub/frontend/src/turn.ts":             "export {};\n",
		"cmd/evener-hub/frontend/src/other.ts":            "export {};\n",
		"cmd/evener-hub/frontend/dist/PLACEHOLDER":        "\n",
		"appwire-client/typescript/src/index.ts":          "export {};\n",
		"mobile-native/app.json":                          "{}\n",
		"mobile-native/src/app.tsx":                       "export {};\n",
		"docs/developing-evener/testing.md":               "# Testing\n",
		"scripts/gate/run.go":                             "package main\n",
		"scripts/gate/run.sh":                             "#!/bin/sh\n",
		"make/testing.mk":                                 "test:\n",
		"Makefile":                                        "include make/*.mk\n",
		".github/workflows/ci.yml":                        "name: CI\n",
		".tool-versions":                                  "golang 1.27.0\n",
		"go.sum":                                          "",
		"cmd/evener-hub/frontend/scripts/guard/run.mjs":   "export {};\n",
		"cmd/evener-hub/frontend/src/dev/copyonly/tsv.go": "package copyonly\n",
	}
}

// writeFixture writes the files under a temporary root and returns the root
// and the sorted file list, standing in for git ls-files.
func writeFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, slices.Sorted(maps.Keys(files))
}

func planFixture(t *testing.T, changed ...string) plan {
	t.Helper()
	return planFixtureWith(t, nil, changed...)
}

// planFixtureWith plans a change against the fixture plus extra files.
func planFixtureWith(t *testing.T, extra map[string]string, changed ...string) plan {
	t.Helper()
	files := fixtureFiles()
	maps.Copy(files, extra)
	root, tracked := writeFixture(t, files)
	ws, err := loadWorkspace(root, tracked)
	if err != nil {
		t.Fatal(err)
	}
	return planLanes(ws, changed)
}

func allLaneIDs() []string {
	var ids []string
	for _, l := range catalog {
		ids = append(ids, l.id())
	}
	return ids
}

// requireRuns checks the plan runs exactly the lanes named and skips the rest.
func requireRuns(t *testing.T, p plan, want ...string) {
	t.Helper()
	var got []string
	for _, id := range allLaneIDs() {
		if p.runs(id) {
			got = append(got, id)
		}
	}
	slices.Sort(got)
	want = slices.Compact(slices.Sorted(slices.Values(want)))
	if !slices.Equal(got, want) {
		t.Fatalf("lanes run = %q\nwant %q", got, want)
	}
}

func TestUnknownPathRunsEveryLane(t *testing.T) {
	for _, changed := range []string{
		"docs/developing-evener/testing.md",
		"README.md",
		"cmd/evener-hub/testdata/fixture.txt",
		"testing-budget.json",
	} {
		t.Run(changed, func(t *testing.T) {
			requireRuns(t, planFixture(t, changed), allLaneIDs()...)
		})
	}
}

func TestToolchainAndBuildFilesRunEveryLane(t *testing.T) {
	for _, changed := range []string{
		"go.mod",
		"go.sum",
		"go.work",
		"go.work.sum",
		"agent/go.mod",
		"agent/go.sum",
		".tool-versions",
		"Makefile",
		"make/testing.mk",
		".github/workflows/ci.yml",
		".github/actions/setup-toolchain/action.yml",
		"scripts/gate/run.sh",
		"scripts/gate/run.go",
	} {
		t.Run(changed, func(t *testing.T) {
			requireRuns(t, planFixture(t, changed), allLaneIDs()...)
		})
	}
}

// Go source that is not a workspace package's own source is read as data or
// built as a fixture, so nothing proves which lane it reaches.
func TestGoSourceOutsideWorkspacePackagesRunsEveryLane(t *testing.T) {
	for _, changed := range []string{
		"cmd/evener-hub/testdata/fixture.go",
		"agent/testdata/task/task.go",
		"tools/standalone/main.go",
		"cmd/evener-hub/_scratch/x.go",
		"cmd/evener-hub/.hidden/x.go",
	} {
		t.Run(changed, func(t *testing.T) {
			requireRuns(t, planFixture(t, changed), allLaneIDs()...)
		})
	}
}

func TestNoChangedFilesRunsEveryLane(t *testing.T) {
	requireRuns(t, planFixture(t), allLaneIDs()...)
}

// The Go source lanes analyze or compile every package, so any Go change
// runs them.
var goSourceLanes = []string{"static-build", "lint-golangci", "lint-tagged", "fuzz-checks"}

func TestLeafAgentPackageRunsOnlyTheAgentModuleLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "agent/leaf/leaf.go"), append([]string{
		"fuzz",
		"tests-parts/nonroot",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
	}, goSourceLanes...)...)
}

// The hub imports agent/session and cmd/evener imports the hub, so a change
// there reaches every lane that runs either, through the import graph; and
// agent/leaf's tests build cmd/evener through internal/prebuilt.
func TestImportedAgentPackageRunsTheLanesOfItsImporters(t *testing.T) {
	requireRuns(t, planFixture(t, "agent/session/session.go"), append([]string{
		"fuzz",
		"tests-parts/root",
		"tests-parts/nonroot",
		"race-root-parts/hub",
		"race-root-parts/rest",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
		"hub-integration",
		"web-browser",
	}, goSourceLanes...)...)
}

// A package's tests are not part of what its importers build, so a change
// to them runs only the lanes that run the package itself.
func TestTestOnlyChangeRunsOnlyItsOwnPackagesLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "agent/session/session_test.go"), append([]string{
		"fuzz",
		"tests-parts/nonroot",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
	}, goSourceLanes...)...)
}

// The web gate runs on the dev binary, so the packages it imports reach the
// web-unit lane.
func TestDevBinaryDependencyRunsWebUnit(t *testing.T) {
	requireRuns(t, planFixture(t, "agent/tool/tool.go"), append([]string{
		"fuzz",
		"tests-parts/root",
		"tests-parts/nonroot",
		"race-root-parts/rest",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
		"web-unit",
		"web-browser",
	}, goSourceLanes...)...)
}

// The hub's tests build ./cmd/evener, and agent/leaf's tests import
// internal/prebuilt, which names it; so a package the command imports
// reaches both packages' lanes although neither imports it.
func TestPackageBuiltByATestRunsThatTestsLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "cmd/evener/internal/cli/cli.go"), append([]string{
		"fuzz",
		"tests-parts/root",
		"tests-parts/nonroot",
		"race-root-parts/hub",
		"race-root-parts/rest",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
		"hub-integration",
		"web-browser",
	}, goSourceLanes...)...)
}

func TestRootModuleLeafRunsOnlyTheRootRestLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "cmd/evener-doctor/doctor.go"), append([]string{
		"fuzz",
		"tests-parts/root",
		"race-root-parts/rest",
	}, goSourceLanes...)...)
}

// A deleted package's files no longer exist, and the change still reaches
// every lane its module's tests belong to.
func TestDeletedPackageStillRunsItsModuleLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "cmd/evener-gone/gone.go"), append([]string{
		"fuzz",
		"tests-parts/root",
		"race-root-parts/rest",
	}, goSourceLanes...)...)
}

// The TypeScript lanes, plus the build that embeds the frontend.
var typeScriptLanes = []string{"web-unit", "web-browser", "native", "api-package", "static-build"}

// The hub's tests read frontend/src by path, so a frontend change runs the
// hub's lanes. Only its tests read it, so the hub's importers (cmd/evener,
// and through it agent/leaf's tests) stay skipped.
func TestFrontendChangeRunsTheGoPackagesThatReadIt(t *testing.T) {
	requireRuns(t, planFixture(t, "cmd/evener-hub/frontend/src/turn.ts"), append([]string{
		"fuzz",
		"tests-parts/root",
		"race-root-parts/hub",
		"hub-integration",
	}, typeScriptLanes...)...)
}

// The hub names frontend/src/turn.ts and the frontend/dist directory, and
// nothing else in the frontend: a sibling source reaches no Go lane.
func TestFrontendFileNoGoPackageNamesRunsOnlyTheTypeScriptLanes(t *testing.T) {
	for _, changed := range []string{
		"cmd/evener-hub/frontend/src/other.ts",
		"cmd/evener-hub/frontend/scripts/guard/run.mjs",
		"appwire-client/typescript/src/index.ts",
		"mobile-native/src/app.tsx",
	} {
		t.Run(changed, func(t *testing.T) {
			requireRuns(t, planFixture(t, changed), typeScriptLanes...)
		})
	}
}

// //go:embed names a path the package compiles in, so what embeds it is
// changed for every importer too: cmd/evener, and agent/leaf's tests.
func TestEmbeddedFileRunsTheEmbeddingPackagesLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "cmd/evener-hub/frontend/dist/PLACEHOLDER"), append([]string{
		"fuzz",
		"tests-parts/root",
		"tests-parts/nonroot",
		"race-root-parts/hub",
		"race-root-parts/rest",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
		"hub-integration",
	}, typeScriptLanes...)...)
}

// llm's test reads ../mobile-native/app.json, resolved from its own directory.
func TestPackageRelativeReadRunsTheReadersLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "mobile-native/app.json"), append([]string{
		"fuzz",
		"tests-parts/nonroot",
		"race-module-shards/nonagent",
	}, typeScriptLanes...)...)
}

// Go source inside a TypeScript tree is both: a root module package's source
// and a change to the tree.
func TestGoSourceInsideATypeScriptTreeIsAPackageAndATreeChange(t *testing.T) {
	requireRuns(t, planFixture(t, "cmd/evener-hub/frontend/src/dev/copyonly/tsv.go"), append(append([]string{
		"fuzz",
		"tests-parts/root",
		"race-root-parts/rest",
	}, goSourceLanes...), typeScriptLanes...)...)
}

// "." and ".." are everywhere in Go source (string splits, relative walks
// from a package directory) and name no package: the root package's own
// change reaches only the root module.
func TestRootPackageChangeRunsOnlyTheRootModuleLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "evener_test.go"), append([]string{
		"fuzz",
		"tests-parts/root",
		"race-root-parts/rest",
	}, goSourceLanes...)...)
}

// A test that walks or lists the repository from its root reads every file,
// and no name says which, so every change reaches it: agent/leaf's change
// and a frontend source alike run the llm module's lanes. A file that names
// the root and walks any directory counts, since the rule does not follow
// where a walk starts.
func TestTestWalkingTheWholeRepositoryIsReachedByEveryChange(t *testing.T) {
	for name, audit := range map[string]string{
		"walks from a climb to the root": `package audit

import (
	"io/fs"
	"path/filepath"
)

var _ = filepath.WalkDir(filepath.Join("..", ".."), func(string, fs.DirEntry, error) error { return nil })
`,
		"walks the root through fs": `package audit

import (
	"io/fs"
	"os"
)

var _ = fs.WalkDir(os.DirFS("../.."), ".", nil)
`,
		"lists tracked files from a climb to the root": `package audit

import "os/exec"

var list = exec.Command("git", "-C", "../..", "ls-files", "-z")
`,
		"names the root and walks another directory": `package audit

import (
	"io/fs"
	"path/filepath"
)

var root = filepath.Join("..", "..")
var _ = filepath.WalkDir("/tmp/state", func(string, fs.DirEntry, error) error { return nil })
`,
		"lists tracked files from git's top level": `package audit

import "os/exec"

var top = exec.Command("git", "rev-parse", "--show-toplevel")
var list = exec.Command("git", "ls-files", "-z")
`,
	} {
		t.Run(name, func(t *testing.T) {
			extra := map[string]string{"llm/audit/audit_test.go": audit}
			requireRuns(t, planFixtureWith(t, extra, "agent/leaf/leaf.go"), append([]string{
				"fuzz",
				"tests-parts/nonroot",
				"race-module-shards/agent-1", "race-module-shards/agent-2",
				"race-module-shards/nonagent",
			}, goSourceLanes...)...)
			requireRuns(t, planFixtureWith(t, extra, "cmd/evener-hub/frontend/src/other.ts"), append([]string{
				"fuzz",
				"tests-parts/nonroot",
				"race-module-shards/nonagent",
			}, typeScriptLanes...)...)
		})
	}
}

// A root package test runs at the repository root, so listing files there
// lists the whole repository with no climb to name it.
func TestRootPackageTestListingFilesIsReachedByEveryChange(t *testing.T) {
	extra := map[string]string{"audit_test.go": `package evener_test

import "os/exec"

var list = exec.Command("git", "ls-files", "-z")
`}
	requireRuns(t, planFixtureWith(t, extra, "cmd/evener-hub/frontend/src/other.ts"), append([]string{
		"fuzz",
		"tests-parts/root",
		"race-root-parts/rest",
	}, typeScriptLanes...)...)
}

// A climb to the root only says where the paths a test names start: a test
// that builds or reads named paths from there depends on those paths, which
// the names already show. A walk or a listing reads the whole repository
// only when it starts at the root, so walking a temporary directory or
// listing a temporary repository, and a ".." the test hands its code as
// input, reach nothing a change here touches.
func TestTestNamingTheRootWithoutReadingItIsNotReachedByEveryChange(t *testing.T) {
	for name, test := range map[string]string{
		"climbs to the root to build from it": `package audit

import (
	"os/exec"
	"path/filepath"
)

var build = exec.Command("go", "build", "./cmd/evener")
var dir = filepath.Join("..", "..")
`,
		"lists a temporary repository": `package audit

import "os/exec"

var list = exec.Command("git", "-C", "/tmp/fixture", "ls-files", "-z")
`,
		"walks a temporary directory": `package audit

import (
	"io/fs"
	"path/filepath"
)

var _ = filepath.WalkDir("/tmp/fixture", func(string, fs.DirEntry, error) error { return nil })
`,
		"hands its code a climb as input": `package audit

var hostile = []string{"..", "../..", "a/b"}
`,
	} {
		t.Run(name, func(t *testing.T) {
			extra := map[string]string{"llm/audit/audit_test.go": test}
			requireRuns(t, planFixtureWith(t, extra, "cmd/evener-hub/frontend/src/other.ts"), typeScriptLanes...)
		})
	}
}

// Production code that lists a repository's files lists the user's
// repository, not this one, so it reads nothing of a change here.
func TestProductionListingTrackedFilesIsNotAWholeRepositoryRead(t *testing.T) {
	extra := map[string]string{"llm/worktree.go": `package llm

import "os/exec"

var list = exec.Command("git", "ls-files", "-z")
`}
	requireRuns(t, planFixtureWith(t, extra, "cmd/evener-hub/frontend/src/other.ts"), typeScriptLanes...)
}

// TypeScript tests that pin Go constants read the Go source itself: a ?raw
// import resolved from the importing file, or a path resolved from the
// tree, where vitest runs. A change to that Go file runs the TypeScript
// lanes; Go nothing in a tree names still skips them.
func TestGoFileATypeScriptTestReadsRunsTheTypeScriptLanes(t *testing.T) {
	leafLanes := append([]string{"fuzz", "tests-parts/nonroot", "race-module-shards/agent-1", "race-module-shards/agent-2"}, goSourceLanes...)
	for name, reader := range map[string]map[string]string{
		"raw import from the file": {"appwire-client/typescript/src/leaf.test.ts": "import leafGo from \"../../../agent/leaf/leaf.go?raw\";\n"},
		"path from the tree":       {"mobile-native/src/leaf.test.ts": "const s = readFileSync(path.resolve(process.cwd(), '../agent/leaf/leaf.go'), 'utf8');\n"},
	} {
		t.Run(name, func(t *testing.T) {
			requireRuns(t, planFixtureWith(t, reader, "agent/leaf/leaf.go"), append(leafLanes, typeScriptLanes...)...)
			requireRuns(t, planFixtureWith(t, reader, "agent/leaf/leaf_test.go"), leafLanes...)
		})
	}
}

func TestMixedChangeRunsTheUnionOfItsFilesLanes(t *testing.T) {
	requireRuns(t, planFixture(t, "agent/leaf/leaf.go", "mobile-native/src/app.tsx"), append([]string{
		"fuzz",
		"tests-parts/nonroot",
		"race-module-shards/agent-1", "race-module-shards/agent-2",
		"web-unit",
		"web-browser",
		"native",
		"api-package",
	}, goSourceLanes...)...)
}

func TestOutputsNameSkippedJobsAndExcludedCells(t *testing.T) {
	var out strings.Builder
	if err := planFixture(t, "agent/leaf/leaf.go").writeOutputs(&out); err != nil {
		t.Fatal(err)
	}
	got := parseOutputs(t, out.String())
	want := map[string]string{
		"skip":                       `["web-unit","web-browser","native","api-package","race-root-parts","hub-integration"]`,
		"exclude_tests_parts":        `[{"scope":"root"}]`,
		"exclude_race_root_parts":    `[]`,
		"exclude_race_module_shards": `[{"scope":"nonagent"}]`,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("outputs = %q\nwant %q", got, want)
	}
}

func TestOutputsForAFullRunSkipNothing(t *testing.T) {
	var out strings.Builder
	if err := planFixture(t, "Makefile").writeOutputs(&out); err != nil {
		t.Fatal(err)
	}
	got := parseOutputs(t, out.String())
	want := map[string]string{
		"skip":                       `[]`,
		"exclude_tests_parts":        `[]`,
		"exclude_race_root_parts":    `[]`,
		"exclude_race_module_shards": `[]`,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("outputs = %q\nwant %q", got, want)
	}
}

func parseOutputs(t *testing.T, text string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for line := range strings.Lines(text) {
		name, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		if !ok {
			t.Fatalf("output line %q is not name=value", line)
		}
		got[name] = value
	}
	return got
}
