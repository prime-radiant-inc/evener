// Command testchanged selects and runs only the Go tests affected by the
// current diff. It is invoked by `make test-changed` and never installed: the
// Makefile rule is the interface.
//
// The repo is a go.work workspace of independent modules, so a changed file is
// first mapped to its longest module root from GO_MODULES, then to the package
// that owns its directory. The selection is the changed package plus every
// workspace package whose test-inclusive import graph reaches it, across
// modules, so a change in llm/ runs the root and agent packages that import
// llm. The graph comes from `go list -deps -test` run once per module; there
// is no hardcoded module dependency list.
//
// A test-only change selects only its own package: its importers' code did not
// change. A non-Go file inside a package directory selects that package.
// Files outside every module are reported and skipped. A diff with no Go
// changes and no selection exits 0 with an explicit message; Go changes that
// resolve to no package fail loudly instead of silently passing.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	base := flag.String("base", "main", "git ref the change set is measured from")
	race := flag.String("race", "0", `"1" adds -race to each go test run`)
	modules := flag.String("modules", "", "space-separated module roots from GO_MODULES")
	root := flag.String("root", ".", "repository root")
	testRun := flag.String("run", "", "go test -run regex, from the gate's GATE_TEST_RUN")
	testSkip := flag.String("skip", "", "go test -skip regex, from the gate's GATE_FUZZ_TEST_SKIP")
	flag.Parse()

	switch *race {
	case "0", "1":
	default:
		fmt.Fprintf(os.Stderr, "test-changed: -race must be 0 or 1 (got %q)\n", *race)
		os.Exit(2)
	}

	moduleRoots := strings.Fields(*modules)
	if len(moduleRoots) == 0 {
		fmt.Fprintln(os.Stderr, "test-changed: -modules is empty; make passes GO_MODULES")
		os.Exit(2)
	}

	os.Exit(run(os.Stdout, os.Stderr, *root, *base, moduleRoots, *race == "1", *testRun, *testSkip))
}

// run is main's testable body: it writes to the given streams and returns the
// process exit status. Tests capture the streams so a passing suite cannot
// print a FAIL line that nothing asserts.
//
// testRun and testSkip carry the gate's own -run/-skip regexes so this target
// runs the same surface `make test` does: ordinary Test/Example functions, not
// the fuzz-family tests the regular gate excludes.
func run(stdout, stderr io.Writer, root, base string, modules []string, race bool, testRun, testSkip string) int {
	changed, err := changedFiles(root, base)
	if err != nil {
		sayf(stderr, "test-changed: collecting the diff against %s: %v\n", base, err)
		return 2
	}
	sayf(stdout, "test-changed: base %s, %d changed file(s)\n", base, len(changed))
	if len(changed) == 0 {
		sayln(stdout, "test-changed: no changed files; nothing to run")
		return 0
	}

	g, err := loadGraph(root, modules)
	if err != nil {
		sayf(stderr, "test-changed: loading the import graph: %v\n", err)
		return 2
	}

	res := selectPackages(g, changed, modules)
	for _, f := range res.Outside {
		sayf(stdout, "test-changed: skipped %s: outside every module in GO_MODULES\n", f)
	}
	for _, f := range res.NoPackage {
		sayf(stdout, "test-changed: skipped %s: no Go package in its directory\n", f)
	}
	for _, f := range res.Removed {
		sayf(stdout, "test-changed: skipped %s: removed, and no surviving package imports it\n", f)
	}
	for _, p := range res.GraphOnly {
		sayf(stdout, "test-changed: skipped %s\n", p)
	}
	for _, f := range res.AuditOnly {
		sayf(stdout, "test-changed: skipped %s: only the repo-root package reads it, and this target does not run the root package\n", f)
	}
	if len(res.Selected) == 0 {
		// GoChanged already counts only changes test-changed could have run;
		// out-of-scope files are counted separately in OutOfScope. Subtracting
		// again would let an out-of-scope file cancel a real unrun change.
		if res.GoChanged > 0 {
			sayf(stderr, "test-changed: FAIL: %d Go file(s) changed but no runnable package was selected; refusing to report a passing run\n", res.GoChanged)
			return 2
		}
		if res.OutOfScope > 0 {
			sayf(stdout, "test-changed: %d Go file(s) changed in modules test-changed does not run; nothing to run here\n", res.OutOfScope)
			return 0
		}
		sayln(stdout, "test-changed: diff touches no Go package; nothing to run")
		return 0
	}

	for _, s := range res.Selected {
		sayf(stdout, "test-changed: select module %s package %s reason: %s\n", s.ModuleRoot, s.ImportPath, s.Reason)
	}

	failed := false
	mods := orderedModules(res.Selected, modules)
	// `make test` runs each module under a disposable HOME and TMPDIR. Go owns
	// the directory here so the Makefile needs no recursive delete, and so the
	// removal happens even if the run is interrupted.
	privateRoot, err := os.MkdirTemp("", "evener-test-changed-")
	if err != nil {
		sayf(stderr, "test-changed: creating a private test root: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(privateRoot) }()
	testEnv, err := privateTestEnv(privateRoot)
	if err != nil {
		sayf(stderr, "test-changed: preparing a private test environment: %v\n", err)
		return 2
	}
	for _, m := range mods {
		sels := selectionsFor(res.Selected, m)
		args := []string{"test", "-short", "-count=1"}
		if race {
			args = append(args, "-race")
		}
		if testRun != "" {
			args = append(args, "-run", testRun)
		}
		if testSkip != "" {
			args = append(args, "-skip", testSkip)
		}
		for _, s := range sels {
			args = append(args, packageArg(g, s))
		}
		sayf(stdout, "test-changed: module %s: go %s (in %s)\n", m, strings.Join(args, " "), g.moduleDir(m))
		// The test run is deliberately unbounded: a package may legitimately
		// take minutes, so it takes the caller's context rather than the
		// inspection deadline the git and go list calls use.
		cmd := exec.CommandContext(context.Background(), "go", args...)
		cmd.Dir = g.moduleDir(m)
		cmd.Env = testEnv
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			sayf(stderr, "test-changed: FAIL module %s: %v\n", m, err)
			failed = true
		}
	}
	if failed {
		sayln(stderr, "test-changed: FAIL: at least one selected module failed")
		return 1
	}
	sayf(stdout, "test-changed: PASS (%d package(s) in %d module(s))\n", len(res.Selected), len(mods))
	return 0
}

// packageArg spells a selected package as a path relative to its module
// directory, the form `go test` accepts from that directory.
func packageArg(g *graph, s selection) string {
	rel, err := relPath(g.moduleDir(s.ModuleRoot), s.Dir)
	if err != nil || rel == "." {
		return "."
	}
	return "./" + rel
}
