package testredundancy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/gittest"
)

func TestMain(m *testing.M) {
	// Mutate runs git status and git restore in the fixture repository.
	removeGitConfig, err := gittest.IsolateProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testredundancy TestMain: %v\n", err)
		os.Exit(2)
	}
	code := m.Run()
	removeGitConfig()
	os.Exit(code)
}

// fixtureRunner copies testdata/fixture into a fresh directory and returns a
// Runner over it with its own cache. The toolchain resolves the fixture's own
// go.mod, and the developer's GOFLAGS cannot change what is built.
func fixtureRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "-trimpath")
	dir := filepath.Join(t.TempDir(), "calc")
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "fixture"))); err != nil {
		t.Fatalf("copying fixture: %v", err)
	}
	// A relative cache directory, as a developer passes it: the runner's
	// subprocesses run in the package directory, so it must resolve the path
	// against the caller's working directory first.
	t.Chdir(t.TempDir())
	r, err := NewRunner(t.Context(), dir, "cache", io.Discard)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r, dir
}

func statuses(runs []TestRun) map[string]Status {
	out := map[string]Status{}
	for _, run := range runs {
		out[run.Name] = run.Status
	}
	return out
}

// Profiles are cached by the test binary's content: a second request for the
// same tests runs nothing, a source change runs them again, a failing test is
// reported and left uncached, and a name that matches no test says so.
func TestProfileTestsCachesByTestBinary(t *testing.T) {
	r, dir := fixtureRunner(t)
	runLog := filepath.Join(t.TempDir(), "runs.log")
	t.Setenv("FIXTURE_RUN_LOG", runLog)
	t.Setenv("FIXTURE_FAIL", "1")
	ranTests := func() []string {
		data, err := os.ReadFile(runLog)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(string(data))
	}
	names := []string{"TestAbsNegative", "TestSignZero", "TestFailsWhenAsked", "TestNoSuchTest"}

	first, err := r.ProfileTests(t.Context(), names)
	if err != nil {
		t.Fatalf("ProfileTests: %v", err)
	}
	want := map[string]Status{
		"TestAbsNegative":    StatusRan,
		"TestSignZero":       StatusRan,
		"TestFailsWhenAsked": StatusFailed,
		"TestNoSuchTest":     StatusMissing,
	}
	if got := statuses(first); !mapsEqual(got, want) {
		t.Fatalf("first run statuses = %v, want %v", got, want)
	}
	for _, run := range first {
		if run.Status == StatusRan {
			if _, err := LoadProfile(run.Profile); err != nil {
				t.Fatalf("%s profile: %v", run.Name, err)
			}
		}
	}
	if got := ranTests(); !slices.Equal(got, []string{"TestAbsNegative", "TestSignZero"}) {
		t.Fatalf("tests that ran = %v", got)
	}

	second, err := r.ProfileTests(t.Context(), names)
	if err != nil {
		t.Fatalf("ProfileTests: %v", err)
	}
	want["TestAbsNegative"], want["TestSignZero"] = StatusCached, StatusCached
	if got := statuses(second); !mapsEqual(got, want) {
		t.Fatalf("second run statuses = %v, want %v", got, want)
	}
	if got := ranTests(); len(got) != 2 {
		t.Fatalf("a cached profile ran its test again: %v", got)
	}

	src := filepath.Join(dir, "calc.go")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, append(data, []byte("\nfunc unused() int { return 7 }\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := r.ProfileTests(t.Context(), names[:1])
	if err != nil {
		t.Fatalf("ProfileTests: %v", err)
	}
	if third[0].Status != StatusRan {
		t.Fatalf("after a source change TestAbsNegative is %v, want ran", third[0].Status)
	}
}

// A package run that did not finish its tests (a panic, an exit before any
// test, or a hang where the testing package's timeout cannot reach) covers
// only part of the package, so it is an error and is not cached: the run after
// the cause is gone runs afresh. A single test whose binary wrote no profile
// fails rather than caching an empty one.
func TestProfileRestRefusesToCacheACrashedRun(t *testing.T) {
	for _, tc := range []struct {
		env string
		// wholeBinary: the cause stops every test, not only the one that panics.
		wholeBinary bool
	}{{"FIXTURE_PANIC", false}, {"FIXTURE_EXIT_EARLY", true}, {"FIXTURE_INIT_HANG", true}} {
		env := tc.env
		t.Run(env, func(t *testing.T) {
			r, _ := fixtureRunner(t)
			r.testTimeout, r.restTimeout = time.Second, time.Second
			t.Setenv(env, "1")
			if _, err := r.ProfileRest(t.Context(), nil); err == nil {
				t.Fatal("an unfinished package run returned a profile")
			}
			if tc.wholeBinary {
				runs, err := r.ProfileTests(t.Context(), []string{"TestSignZero"})
				if err != nil {
					t.Fatalf("ProfileTests: %v", err)
				}
				if runs[0].Status != StatusFailed {
					t.Fatalf("a test whose binary did not finish is %s, want failed", runs[0].Status)
				}
			}
			t.Setenv(env, "")
			// The rerun is a real package run; give it the real budget.
			r.testTimeout, r.restTimeout = defaultTestTimeout, defaultRestTimeout
			rest, err := r.ProfileRest(t.Context(), nil)
			if err != nil {
				t.Fatalf("ProfileRest after the cause is gone: %v", err)
			}
			p, err := LoadProfile(rest.Profile)
			if err != nil {
				t.Fatal(err)
			}
			if p.Covered() == 0 {
				t.Fatalf("the rerun returned an empty profile: %d of %d", p.Covered(), p.Total())
			}
		})
	}
}

// A test that passes but leaves a child holding its output is profiled like
// any other passing test; the child does not stop the batch.
func TestProfileTestsKeepsGoingPastALeakedChild(t *testing.T) {
	r, _ := fixtureRunner(t)
	t.Setenv("FIXTURE_LEAK", "1")
	runs, err := r.ProfileTests(t.Context(), []string{"TestLeavesAChildWhenAsked", "TestSignZero"})
	if err != nil {
		t.Fatalf("ProfileTests: %v", err)
	}
	if got := statuses(runs); got["TestLeavesAChildWhenAsked"] != StatusRan || got["TestSignZero"] != StatusRan {
		t.Fatalf("statuses = %v, want both ran", got)
	}
}

func mapsEqual(a, b map[string]Status) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Unique runs the package without the candidates, then reports what each
// candidate alone reaches: a duplicate reaches nothing the rest does not, and
// two candidates that share a branch each keep only their own.
func TestUniqueFindsWhatOnlyEachCandidateReaches(t *testing.T) {
	r, dir := fixtureRunner(t)
	report, err := r.Unique(t.Context(), []string{"TestAbsNegative", "TestSignZero", "TestSignPositiveUnchecked"})
	if err != nil {
		t.Fatalf("Unique: %v", err)
	}
	if len(report.Failed) != 0 || len(report.Rest.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v / %+v", report.Failed, report.Rest.Failed)
	}
	got := map[string]Unique{}
	for _, u := range report.Unique {
		got[u.Name] = u
	}
	if got["TestAbsNegative"].Stmts != 0 {
		t.Errorf("TestAbsNegative has %d unique statements; TestAbsNegativeAgain covers the same code", got["TestAbsNegative"].Stmts)
	}
	for _, name := range []string{"TestSignZero", "TestSignPositiveUnchecked"} {
		u := got[name]
		if u.Stmts != 1 || len(u.Blocks) != 1 {
			t.Fatalf("%s unique = %+v, want its one return statement", name, u)
		}
		if file := r.DisplayFile(u.Blocks[0].File); file != filepath.Join(dir, "calc.go") {
			t.Errorf("%s block file displays as %q, want the package-relative source file", name, file)
		}
	}
	if got["TestSignZero"].Assertions <= got["TestSignPositiveUnchecked"].Assertions {
		t.Errorf("the checked test reports %d assertions and the unchecked one %d",
			got["TestSignZero"].Assertions, got["TestSignPositiveUnchecked"].Assertions)
	}
	if got["TestSignZero"].Blocks[0] == got["TestSignPositiveUnchecked"].Blocks[0] {
		t.Errorf("both candidates claim the same unique block: %+v", got["TestSignZero"].Blocks[0])
	}
}

// A whole-package profile counts every statement, and the diff between two
// such profiles lists each statement covered before and not after.
func TestCoverageDiffListsLostStatements(t *testing.T) {
	r, _ := fixtureRunner(t)
	before, err := r.ProfileRest(t.Context(), nil)
	if err != nil {
		t.Fatalf("ProfileRest: %v", err)
	}
	after, err := r.ProfileRest(t.Context(), []string{"TestSignZero"})
	if err != nil {
		t.Fatalf("ProfileRest: %v", err)
	}
	bp, err := LoadProfile(before.Profile)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := LoadProfile(after.Profile)
	if err != nil {
		t.Fatal(err)
	}
	diff := DiffCoverage(bp, ap, 0)
	if diff.LostStmts != 1 || len(diff.Lost) != 1 || diff.Lost[0].StartLine != 20 {
		t.Fatalf("diff = %+v, want Sign's return 0 (line 20) lost", diff)
	}
	if diff.WithinNoise() {
		t.Fatalf("one lost statement is within a zero noise band: %+v", diff)
	}
	if !DiffCoverage(bp, ap, 1).WithinNoise() {
		t.Fatalf("one lost statement is outside a noise band of one")
	}
	if DiffCoverage(bp, bp, 0).LostStmts != 0 {
		t.Fatalf("a profile lost statements against itself")
	}
}
