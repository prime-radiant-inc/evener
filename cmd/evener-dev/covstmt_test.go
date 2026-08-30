package dev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covstmtRun is pinned directly, not via runEvenerDev's real-binary harness:
// its contract is two integers on stdout and an exit code, nothing a process
// boundary would add. The arithmetic it delegates to (position dedup,
// any-hit union) is pinned by internal/devtool/covstmt's own tests.
func TestCovstmtCountsProfiles(t *testing.T) {
	dir := t.TempDir()
	// Two profiles whose union arithmetic is the load-bearing property: the
	// test track covers only block A, the fuzz track only block B, so the
	// concatenated union profile counts every block once and covered.
	test := filepath.Join(dir, "test.cov")
	fuzz := filepath.Join(dir, "fuzz.cov")
	union := filepath.Join(dir, "union.cov")
	writeCovFixture(t, test, "mode: set\npkg/a.go:10.1,20.2 40 1\npkg/a.go:30.1,40.2 60 0\n")
	writeCovFixture(t, fuzz, "mode: set\npkg/a.go:10.1,20.2 40 0\npkg/a.go:30.1,40.2 60 1\n")
	writeCovFixture(t, union, "mode: set\npkg/a.go:10.1,20.2 40 1\npkg/a.go:30.1,40.2 60 0\npkg/a.go:10.1,20.2 40 0\npkg/a.go:30.1,40.2 60 1\n")

	var out, errOut strings.Builder
	code := covstmtRun([]string{test, fuzz, union}, &out, &errOut)
	if code != 0 {
		t.Fatalf("covstmtRun exits %d, want 0 (stderr: %q)", code, errOut.String())
	}
	// One line per profile: test 40/100, fuzz 60/100, union 100/100 — the
	// two-track property the deleted fake-go suite used to pin, restated
	// against real fixtures.
	want := "40 100\n60 100\n100 100\n"
	if got := out.String(); got != want {
		t.Fatalf("covstmtRun output = %q, want %q", got, want)
	}
}

func TestCovstmtMissingProfileFails(t *testing.T) {
	var out, errOut strings.Builder
	code := covstmtRun([]string{filepath.Join(t.TempDir(), "nope.cov")}, &out, &errOut)
	if code != 1 {
		t.Fatalf("covstmtRun on a missing profile exits %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "nope.cov") {
		t.Fatalf("stderr does not name the missing profile: %q", errOut.String())
	}
}

func TestCovstmtRequiresAProfile(t *testing.T) {
	var out, errOut strings.Builder
	code := covstmtRun(nil, &out, &errOut)
	if code != 2 {
		t.Fatalf("covstmtRun with no profiles exits %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("stderr does not print usage: %q", errOut.String())
	}
}

func writeCovFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
