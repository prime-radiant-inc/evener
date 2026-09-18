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

// TestCovstmtMidListFailureEmitsNoStdout pins the failure seam: a consumer
// reads N lines for N profiles, so a profile that fails MID-list must not
// hand it a prefix of those lines. The command counts everything first and
// prints only on full success, so stdout is empty and the exit is 1 — a
// `read -r tc tt fc ft uc ut` in coverage-floor.sh sees nothing and falls to
// its no-statements branch instead of silently counting with empty fields.
func TestCovstmtMidListFailureEmitsNoStdout(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.cov")
	writeCovFixture(t, good, "mode: set\npkg/a.go:10.1,20.2 40 1\n")
	missing := filepath.Join(dir, "missing.cov")

	var out, errOut strings.Builder
	code := covstmtRun([]string{good, missing, good}, &out, &errOut)
	if code != 1 {
		t.Fatalf("covstmtRun with a mid-list failure exits %d, want 1", code)
	}
	if got := out.String(); got != "" {
		t.Fatalf("stdout on mid-list failure = %q, want empty (all-or-nothing)", got)
	}
	if !strings.Contains(errOut.String(), "missing.cov") {
		t.Fatalf("stderr does not name the failing profile: %q", errOut.String())
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

// gapsFixture exercises every branch the gaps report has: a covered block, an
// uncovered block, a duplicate position unioned to covered, two packages, a
// top-level file with no slash (which groups as its own package), and a --in
// match. The same fixture feeds the expected outputs below, which were captured
// from the Python implementation this report replaced.
const gapsFixture = "mode: set\n" +
	"pkg/a/file.go:10.1,20.2 10 1\n" +
	"pkg/a/file.go:30.1,40.2 100 0\n" +
	"pkg/b/file.go:1.1,2.2 5 0\n" +
	"pkg/b/file.go:1.1,2.2 5 1\n" +
	"pkg/b/other.go:1.1,2.2 20 0\n" +
	"other/z.go:1.1,2.2 7 0\n" +
	"top.go:1.1,2.2 3 0\n"

// TestCovstmtGapsAggregateMatchesPython pins the ranking report's exact bytes
// for the default (by package), by file, --zero, and --top shapes. The expected
// text is the deleted Python's output verbatim, so a refactor that changes a
// column, an ordering, or the summary line fails here rather than in a reader's
// terminal.
func TestCovstmtGapsAggregateMatchesPython(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "by package",
			args: []string{"--gaps", "--by", "package", profile},
			want: " MISSING    total     cov%  package\n" +
				"     100      110     9.1%  pkg/a\n" +
				"      20       25    20.0%  pkg/b\n" +
				"       7        7     0.0%  other\n" +
				"       3        3     0.0%  top.go\n" +
				"\n" +
				"showing 4 of 4 packages with gaps; 130 uncovered of 145 statements overall (10.3%)\n",
		},
		{
			name: "by file",
			args: []string{"--gaps", "--by", "file", profile},
			want: " MISSING    total     cov%  file\n" +
				"     100      110     9.1%  pkg/a/file.go\n" +
				"      20       20     0.0%  pkg/b/other.go\n" +
				"       7        7     0.0%  other/z.go\n" +
				"       3        3     0.0%  top.go\n" +
				"\n" +
				"showing 4 of 4 files with gaps; 130 uncovered of 145 statements overall (10.3%)\n",
		},
		{
			name: "zero only",
			args: []string{"--gaps", "--by", "file", "--zero", profile},
			want: " MISSING    total     cov%  file\n" +
				"      20       20     0.0%  pkg/b/other.go\n" +
				"       7        7     0.0%  other/z.go\n" +
				"       3        3     0.0%  top.go\n" +
				"\n" +
				"showing 3 of 3 files with gaps; 130 uncovered of 145 statements overall (10.3%)\n",
		},
		{
			name: "top truncates the table but not the summary",
			args: []string{"--gaps", "--by", "package", "--top", "2", profile},
			want: " MISSING    total     cov%  package\n" +
				"     100      110     9.1%  pkg/a\n" +
				"      20       25    20.0%  pkg/b\n" +
				"\n" +
				"showing 2 of 4 packages with gaps; 130 uncovered of 145 statements overall (10.3%)\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut strings.Builder
			if code := covstmtRun(tc.args, &out, &errOut); code != 0 {
				t.Fatalf("covstmtRun(%q) exits %d, want 0 (stderr: %q)", tc.args, code, errOut.String())
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("covstmtRun(%q) output:\n%q\nwant:\n%q", tc.args, got, tc.want)
			}
		})
	}
}

// TestCovstmtGapsInMatchesPython pins the --in block listing, including the
// summary's statement total over ALL matching blocks (not just the shown top)
// and Python's repr of the pattern.
func TestCovstmtGapsInMatchesPython(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	var out, errOut strings.Builder
	args := []string{"--gaps", "--in", "file.go", profile}
	if code := covstmtRun(args, &out, &errOut); code != 0 {
		t.Fatalf("covstmtRun(%q) exits %d, want 0 (stderr: %q)", args, code, errOut.String())
	}
	want := "   STMTS  location\n" +
		"     100  pkg/a/file.go:30-40\n" +
		"\n" +
		"showing 1 of 1 uncovered blocks (100 statements) in files matching 'file.go'\n"
	if got := out.String(); got != want {
		t.Fatalf("covstmtRun(%q) output:\n%q\nwant:\n%q", args, got, want)
	}
}

// TestCovstmtGapsInTopTruncatesTableNotSummary pins the invariant the
// single-block --in test above cannot exercise: when more matching uncovered
// blocks exist than --top shows, the TABLE is truncated but the SUMMARY still
// reports every matching block and their combined statement count. Two matching
// uncovered blocks (100 and 40 statements) under --top=1 must print one row yet
// report "1 of 2" and the 140-statement sum; a covered block in a matching file
// must be neither shown nor counted. Expected bytes are the deleted Python's
// output for this fixture verbatim.
func TestCovstmtGapsInTopTruncatesTableNotSummary(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, "mode: set\n"+
		"pkg/a/file.go:10.1,20.2 100 0\n"+
		"pkg/a/file.go:30.1,40.2 40 0\n"+
		"pkg/a/xfile.go:1.1,2.2 5 1\n")

	var out, errOut strings.Builder
	args := []string{"--gaps", "--in", "file.go", "--top", "1", profile}
	if code := covstmtRun(args, &out, &errOut); code != 0 {
		t.Fatalf("covstmtRun(%q) exits %d, want 0 (stderr: %q)", args, code, errOut.String())
	}
	want := "   STMTS  location\n" +
		"     100  pkg/a/file.go:10-20\n" +
		"\n" +
		"showing 1 of 2 uncovered blocks (140 statements) in files matching 'file.go'\n"
	if got := out.String(); got != want {
		t.Fatalf("covstmtRun(%q) output:\n%q\nwant:\n%q", args, got, want)
	}
}

// TestCovstmtGapsInMatchesRunesNotBytes pins --in against Python's Unicode
// substring. A single non-UTF-8 pattern byte (0xa9) decodes to U+DCA9 under
// surrogateescape and must NOT match the 0xc3 0xa9 bytes that end 'é'; a
// byte-wise scan would match and list the block. The expected line is the
// deleted Python's output for this fixture.
func TestCovstmtGapsInMatchesRunesNotBytes(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, "mode: set\npkg/\u00e9.go:1.1,2.2 9 0\n")

	var out, errOut strings.Builder
	args := []string{"--gaps", "--in", "\xa9", profile}
	if code := covstmtRun(args, &out, &errOut); code != 0 {
		t.Fatalf("covstmtRun(%q) exits %d, want 0 (stderr: %q)", args, code, errOut.String())
	}
	want := "no uncovered blocks in files matching '\\udca9'\n"
	if got := out.String(); got != want {
		t.Fatalf("covstmtRun(%q) output = %q, want %q (byte-wise matching would list the block)",
			args, got, want)
	}
}

// TestCovstmtUnionModePrintsOneLine pins the shape the e2e combined report
// reads: --union prints ONE "covered total" line for the whole set, deduped by
// position with any-hit coverage, instead of one line per profile.
func TestCovstmtUnionModePrintsOneLine(t *testing.T) {
	dir := t.TempDir()
	test := filepath.Join(dir, "test.cov")
	fuzz := filepath.Join(dir, "fuzz.cov")
	writeCovFixture(t, test, "mode: set\npkg/a.go:10.1,20.2 40 1\npkg/a.go:30.1,40.2 60 0\n")
	writeCovFixture(t, fuzz, "mode: set\npkg/a.go:10.1,20.2 40 0\npkg/a.go:30.1,40.2 60 1\n")

	var out, errOut strings.Builder
	if code := covstmtRun([]string{"--union", test, fuzz}, &out, &errOut); code != 0 {
		t.Fatalf("covstmtRun --union exits %d, want 0 (stderr: %q)", code, errOut.String())
	}
	if got, want := out.String(), "100 100\n"; got != want {
		t.Fatalf("covstmtRun --union output = %q, want %q", got, want)
	}
}

// TestCovstmtUnionMissingProfileFails: a missing member must fail the whole
// union (exit 1, no stdout) rather than count a partial set.
func TestCovstmtUnionMissingProfileFails(t *testing.T) {
	var out, errOut strings.Builder
	code := covstmtRun([]string{"--union", filepath.Join(t.TempDir(), "nope.cov")}, &out, &errOut)
	if code != 1 {
		t.Fatalf("covstmtRun --union on a missing profile exits %d, want 1", code)
	}
	if got := out.String(); got != "" {
		t.Fatalf("stdout on a missing union member = %q, want empty", got)
	}
}

// TestCovstmtRejectsUnionWithGaps: --union counts a set as one; --gaps ranks a
// single profile. Together they are meaningless, so it is a usage error.
func TestCovstmtRejectsUnionWithGaps(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)
	var out, errOut strings.Builder
	if code := covstmtRun([]string{"--gaps", "--union", profile}, &out, &errOut); code != 2 {
		t.Fatalf("covstmtRun --gaps --union exits %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Fatalf("stderr does not explain --union vs --gaps: %q", errOut.String())
	}
}

// TestCovstmtGapsNoMatch pins the no-match line: the pattern is repr'd, and the
// exit is still 0 (an empty result is information, not a failure).
func TestCovstmtGapsNoMatch(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	var out, errOut strings.Builder
	args := []string{"--gaps", "--in", "nowhere", profile}
	if code := covstmtRun(args, &out, &errOut); code != 0 {
		t.Fatalf("covstmtRun(%q) exits %d, want 0 (stderr: %q)", args, code, errOut.String())
	}
	want := "no uncovered blocks in files matching 'nowhere'\n"
	if got := out.String(); got != want {
		t.Fatalf("covstmtRun(%q) output = %q, want %q", args, got, want)
	}
}

// TestCovstmtGapsRejectsBadBy keeps the validation the shell script performed —
// --by is package or file, nothing else.
func TestCovstmtGapsRejectsBadBy(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	var out, errOut strings.Builder
	if code := covstmtRun([]string{"--gaps", "--by", "bogus", profile}, &out, &errOut); code != 2 {
		t.Fatalf("covstmtRun with --by bogus exits %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--by must be package or file") {
		t.Fatalf("stderr does not explain the bad --by: %q", errOut.String())
	}
}

// TestCovstmtGapsRequiresExactlyOneProfile: the report is a single profile's
// ranking; two profiles have no defined output, so it is a usage error.
func TestCovstmtGapsRequiresExactlyOneProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	var out, errOut strings.Builder
	if code := covstmtRun([]string{"--gaps", profile, profile}, &out, &errOut); code != 2 {
		t.Fatalf("covstmtRun --gaps with two profiles exits %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("stderr does not print usage: %q", errOut.String())
	}
}

// TestCovstmtGapsRejectsNegativeTop: a negative --top is a typo, not "show
// nothing". Clamping it to zero would exit 0 with an empty table that reads as
// a clean report, so it is rejected like any other unusable flag value — and no
// partial table is printed.
func TestCovstmtGapsRejectsNegativeTop(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	var out, errOut strings.Builder
	if code := covstmtRun([]string{"--gaps", "--top", "-1", profile}, &out, &errOut); code != 2 {
		t.Fatalf("covstmtRun with --top -1 exits %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--top must not be negative") {
		t.Fatalf("stderr does not explain the negative --top: %q", errOut.String())
	}
	if got := out.String(); got != "" {
		t.Fatalf("stdout on negative --top = %q, want empty", got)
	}
}

// TestCovstmtCountModeRejectsGapsOnlyFlags: --by/--top/--zero/--in mean nothing
// without --gaps. Silently ignoring them would print counts where a caller who
// forgot --gaps expected a ranking, so each is a usage error.
func TestCovstmtCountModeRejectsGapsOnlyFlags(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	writeCovFixture(t, profile, gapsFixture)

	for _, args := range [][]string{
		{"--by", "file", profile},
		{"--top", "5", profile},
		{"--zero", profile},
		{"--in", "x", profile},
	} {
		var out, errOut strings.Builder
		if code := covstmtRun(args, &out, &errOut); code != 2 {
			t.Errorf("covstmtRun(%q) exits %d, want 2", args, code)
		}
		if !strings.Contains(errOut.String(), "--gaps is required for") {
			t.Errorf("covstmtRun(%q) stderr = %q, want the gaps-only message", args, errOut.String())
		}
		if got := out.String(); got != "" {
			t.Errorf("covstmtRun(%q) stdout = %q, want empty", args, got)
		}
	}
}

// TestPyReprMatchesPython pins pyRepr against Python's repr() for the shapes an
// --in pattern can take, including the control bytes that would otherwise reach
// the terminal raw.
func TestPyReprMatchesPython(t *testing.T) {
	tests := []struct{ in, want string }{
		{"file.go", "'file.go'"},
		{"a'b", `"a'b"`},      // single quote only: switch to double
		{`a'b"c`, `'a\'b"c'`}, // both: stay single, escape the single
		{`back\slash`, `'back\\slash'`},
		{"tab\there", `'tab\there'`},
		{"esc\x1b[31m", `'esc\x1b[31m'`},
		{"nul\x00end", `'nul\x00end'`},
		{"café", "'café'"}, // printable non-ASCII stays literal
		// Non-UTF-8 bytes: Python decodes argv with surrogateescape and reprs
		// the resulting U+DC80..U+DCFF as \udcXX, so these must too (a plain
		// rune range would emit U+FFFD here).
		{"\x80", `'\udc80'`},
		{"\xff\xfe", `'\udcff\udcfe'`},
		{"caf\xe9", `'caf\udce9'`},
	}
	for _, tc := range tests {
		if got := pyRepr(tc.in); got != tc.want {
			t.Errorf("pyRepr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
