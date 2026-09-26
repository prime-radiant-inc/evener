package serialtestcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writePackage writes a fixture package: a production file and a test file.
func writePackage(t *testing.T, production, tests string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"pkg.go": production, "pkg_test.go": tests} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package pkg\n\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// flagged runs the check over dir and returns the names of the tests it
// reports as needlessly serial.
func flagged(t *testing.T, dir string, synced ...string) []string {
	t.Helper()
	findings, err := check(dir, setOf(synced))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range findings {
		names = append(names, f.test)
	}
	return names
}

func setOf(names []string) map[string]bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return set
}

const testingImport = "import (\n\t\"os\"\n\t\"sync/atomic\"\n\t\"testing\"\n)\n\nvar _ = os.Getenv\nvar _ atomic.Int32\n\n"

func TestReportsASerialTestThatTouchesNoSharedState(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, "func add(a, b int) int { return a + b }\n", testingImport+`
func TestAdd(t *testing.T) {
	if add(1, 2) != 3 {
		t.Fatal("bad")
	}
}

func TestAddParallel(t *testing.T) {
	t.Parallel()
	_ = add(1, 2)
}

func parallelAdd(t *testing.T) int {
	t.Parallel()
	return add(1, 2)
}

func TestAddThroughAParallelHelper(t *testing.T) { _ = parallelAdd(t) }
`)
	if got := flagged(t, dir); !slices.Equal(got, []string{"TestAdd"}) {
		t.Fatalf("flagged %v, want only TestAdd", got)
	}
}

func TestLeavesTestsThatChangeProcessStateSerial(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, `
var budget = 5
var hook atomic.Pointer[func()]

func setBudget(v int) { budget = v }
`, testingImport+`
func setEnv(t *testing.T) { t.Setenv("X", "1") }

func TestSetenv(t *testing.T)          { t.Setenv("X", "1") }
func TestOSSetenv(t *testing.T)        { _ = os.Setenv("X", "1") }
func TestChdir(t *testing.T)           { _ = os.Chdir("/") }
func TestThroughAHelper(t *testing.T)  { setEnv(t) }
func TestProductionWrite(t *testing.T) { setBudget(1) }
func TestTestWrite(t *testing.T)       { budget = 2 }
func TestAtomicStore(t *testing.T)     { f := func() {}; hook.Store(&f) }
func TestGlobalSetter(t *testing.T)    { SetProbeForTesting(nil) }

func SetProbeForTesting(any) {}
`)
	if got := flagged(t, dir); len(got) != 0 {
		t.Fatalf("flagged %v, want none: each changes process-wide state", got)
	}
}

func TestADocCommentReasonKeepsATestSerial(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, "", testingImport+`
// TestTiming checks wall-clock pacing. Not parallel: other tests' load would
// skew its measurement.
func TestTiming(t *testing.T) {}
`)
	if got := flagged(t, dir); len(got) != 0 {
		t.Fatalf("flagged %v, want none: the doc comment gives the reason", got)
	}
}

func TestLeavesWrapperTestsAlone(t *testing.T) {
	t.Parallel()
	// A test that calls another test, and the test it calls: a t.Parallel in
	// both would run twice on one T and panic.
	dir := writePackage(t, "", testingImport+`
func TestInner(t *testing.T) {}
func TestOuter(t *testing.T) { TestInner(t) }
`)
	if got := flagged(t, dir); len(got) != 0 {
		t.Fatalf("flagged %v, want none: wrapper and callee must stay as they are", got)
	}
}

func TestAReviewedSynchronizedCacheIsNotShared(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, `
import "sync"

var cacheMu sync.Mutex
var cache = map[string]int{}

func cached(k string) int {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache[k]++
	return cache[k]
}
`, testingImport+`
func TestCached(t *testing.T) { _ = cached("k") }
`)
	if got := flagged(t, dir); len(got) != 0 {
		t.Fatalf("unreviewed cache: flagged %v, want none -- an unknown package write counts as shared", got)
	}
	if got := flagged(t, dir, "cache", "cacheMu"); !slices.Equal(got, []string{"TestCached"}) {
		t.Fatalf("reviewed cache: flagged %v, want TestCached", got)
	}
}

func TestALocalNamedLikeAPackageVariableIsNotShared(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, "var budget = 5\n", testingImport+`
func TestShadow(t *testing.T) {
	budget := 1
	budget = 2
	_ = budget
}

func TestParam(t *testing.T) { set(3) }

func set(budget int) { budget = 4; _ = budget }
`)
	if got := flagged(t, dir); !slices.Equal(got, []string{"TestShadow", "TestParam"}) {
		t.Fatalf("flagged %v, want TestParam and TestShadow: their writes are to locals", got)
	}
}

func TestRunReportsFileLineAndTheFix(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, "", testingImport+"func TestSerial(t *testing.T) {}\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{dir}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, filepath.Join(dir, "pkg_test.go")+":") || !strings.Contains(out, "TestSerial") || !strings.Contains(out, "t.Parallel()") || !strings.Contains(out, "Not parallel:") {
		t.Fatalf("output %q does not name the file, line, test, and both ways to fix it", out)
	}

	clean := writePackage(t, "", testingImport+"func TestP(t *testing.T) { t.Parallel() }\n")
	stdout.Reset()
	if code := run([]string{clean}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("clean package: exit = %d, output %q; want 0 and nothing", code, stdout.String())
	}
}

func TestAReasonCommentInTheBodyKeepsATestSerial(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, "", testingImport+`
func TestAllocations(t *testing.T) {
	// Not parallel: it measures process-wide allocation.
	_ = 1
}
`)
	if got := flagged(t, dir); len(got) != 0 {
		t.Fatalf("flagged %v, want none: the comment in its body gives the reason", got)
	}
}

func TestAParallelSubtestLeavesItsParentSerial(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, "", testingImport+`
func TestParent(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		t.Parallel()
	})
}
`)
	if got := flagged(t, dir); !slices.Equal(got, []string{"TestParent"}) {
		t.Fatalf("flagged %v, want TestParent: only its subtest is parallel", got)
	}
}

func TestAMethodCallOnAPackageVariableIsShared(t *testing.T) {
	t.Parallel()
	dir := writePackage(t, `
type registry struct{ names []string }

func (r *registry) Register(name string) { r.names = append(r.names, name) }

var globalRegistry = &registry{}
`, testingImport+`
func TestRegisters(t *testing.T) { globalRegistry.Register("x") }
`)
	if got := flagged(t, dir); len(got) != 0 {
		t.Fatalf("flagged %v, want none: a method on a package variable can change it", got)
	}
}
