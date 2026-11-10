package cilanes

import "slices"

// A lane is one CI job, or one cell of a job's matrix, that a pull request
// may skip. Jobs absent from the catalog always run.
type lane struct {
	// job is the job's id in .github/workflows/ci.yml.
	job string
	// cell is the matrix value this lane runs, or "" for a job without a
	// matrix.
	cell string
	// typeScript lanes run for any change inside a TypeScript tree.
	typeScript bool
	// goSource lanes analyze or compile every Go package, so any change to
	// a package's Go source runs them.
	goSource bool
	// runsPackage reports whether the lane runs the Go package in dir,
	// which belongs to the workspace module in moduleDir. A lane runs when
	// the change reaches a package it runs.
	runsPackage func(dir, moduleDir string) bool
}

func (l lane) id() string {
	if l.cell == "" {
		return l.job
	}
	return l.job + "/" + l.cell
}

const (
	hubPackage       = "cmd/evener-hub"
	devBinaryPackage = "cmd/evener-dev/bin"
)

func inModule(moduleDir string) func(string, string) bool {
	return func(_, m string) bool { return m == moduleDir }
}

func outsideModules(moduleDirs ...string) func(string, string) bool {
	return func(_, m string) bool { return !slices.Contains(moduleDirs, m) }
}

func isPackage(dirs ...string) func(string, string) bool {
	return func(dir, _ string) bool { return slices.Contains(dirs, dir) }
}

// catalog lists the skippable lanes in ci.yml's job order, with what each
// one runs. web-unit runs the frontend gate through the dev binary, and
// web-browser runs the hub's tests under the browserguard tag through it;
// static-build's make build compiles the frontend into the hub. The matrix
// lanes mirror make test's TEST_SCOPE and make test-race's RACE_SCOPE and
// RACE_ROOT_PART. make fuzz replays the seed corpus in every module.
var catalog = []lane{
	{job: "web-unit", typeScript: true, runsPackage: isPackage(devBinaryPackage)},
	{job: "web-browser", typeScript: true, runsPackage: isPackage(hubPackage, devBinaryPackage)},
	{job: "native", typeScript: true},
	{job: "api-package", typeScript: true},
	{job: "static-build", typeScript: true, goSource: true},
	{job: "lint-golangci", goSource: true},
	{job: "lint-tagged", goSource: true},
	{job: "fuzz-checks", goSource: true},
	{job: "race-root-parts", cell: "hub", runsPackage: isPackage(hubPackage)},
	{job: "race-root-parts", cell: "rest", runsPackage: func(dir, m string) bool { return m == "." && dir != hubPackage }},
	{job: "race-module-shards", cell: "agent", runsPackage: inModule("agent")},
	{job: "race-module-shards", cell: "nonagent", runsPackage: outsideModules(".", "agent")},
	{job: "tests-parts", cell: "root", runsPackage: inModule(".")},
	{job: "tests-parts", cell: "nonroot", runsPackage: outsideModules(".")},
	{job: "hub-integration", runsPackage: isPackage(hubPackage)},
	{job: "fuzz", runsPackage: func(string, string) bool { return true }},
}

// matrixKeys names each matrix job's matrix variable in ci.yml.
var matrixKeys = map[string]string{
	"race-root-parts":    "part",
	"race-module-shards": "scope",
	"tests-parts":        "scope",
}

// typeScriptTrees are the directories the TypeScript lanes build and test.
// They share sources (the native app bundles frontend modules, and every
// tree imports the AppWire package), so a change in one runs them all.
var typeScriptTrees = []string{
	"cmd/evener-hub/frontend",
	"appwire-client/typescript",
	"mobile-native",
}

// everyLanePaths are files and directories that steer every lane: the Go
// toolchain and module graph, the make targets the lanes run, the workflow
// and its actions, and the scripts the make targets call.
var everyLanePaths = []string{
	".tool-versions",
	"Makefile",
	"make",
	".github",
	"scripts",
}

// everyLaneBaseNames are file names that steer every lane wherever they sit.
var everyLaneBaseNames = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}
