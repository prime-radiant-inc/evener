package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoverageGapsScriptReportsFixture runs the real coverage-gaps.sh against a
// fixture profile and pins its stdout and exit code. It is an outcome test, not
// a fake-toolchain test: it invokes the actual script and the actual
// `evener-dev/bin dev covstmt`, so it pins the delegation by observing the
// report — the documented way to test dev tooling ("outcomes are exit codes,
// summaries, refusals, and file effects"; see
// docs/developing-evener/testing.md). The two subtests differ only in CDPATH:
// an inherited CDPATH (a common user setting) must not change the report, so
// this is also the regression for the `cd`-echoes-its-target bug.
func TestCoverageGapsScriptReportsFixture(t *testing.T) {
	t.Parallel()
	profile := filepath.Join(t.TempDir(), "fixture.cov")
	body := "mode: set\n" +
		"pkg/a/file.go:10.1,20.2 10 1\n" +
		"pkg/a/file.go:30.1,40.2 100 0\n" +
		"pkg/b/other.go:1.1,2.2 20 0\n"
	if err := os.WriteFile(profile, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture profile: %v", err)
	}
	want := " MISSING    total     cov%  file\n" +
		"     100      110     9.1%  pkg/a/file.go\n" +
		"      20       20     0.0%  pkg/b/other.go\n" +
		"\n" +
		"showing 2 of 2 files with gaps; 120 uncovered of 130 statements overall (7.7%)\n"

	for _, tc := range []struct {
		name   string
		cdpath string
	}{
		{"no CDPATH", ""},
		{"CDPATH set", "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "scripts/coverage/coverage-gaps.sh", profile, "--by", "file")
			if tc.cdpath != "" {
				cmd.Env = append(os.Environ(), "CDPATH="+tc.cdpath)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("coverage-gaps.sh failed (CDPATH=%q): %v\nreport:\n%s", tc.cdpath, err, out)
			}
			if got := string(out); got != want {
				t.Fatalf("report (CDPATH=%q):\n%q\nwant:\n%q", tc.cdpath, got, want)
			}
		})
	}
}

// TestCoverageScriptsUseGoCovstmt is the static side of the consolidation
// guard: neither coverage script may count statements with its own regex, and
// neither may run python3. Per the testing doc, "that every consumer actually
// goes through the library is a static audit's job, not a reason to re-run
// consumers under sabotage" — so this stays a source audit rather than a
// fake-`go` harness (faking the toolchain to test a script is banned outright).
// It strips full-line comments so a comment cannot satisfy or defeat it.
//
// It reads the two paths by name rather than globbing: a new coverage script
// should get its own decision, not inherit (or dodge) this one by accident.
func TestCoverageScriptsUseGoCovstmt(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"scripts/coverage/coverage-gaps.sh",
		"scripts/coverage/e2e-cover.sh",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var code strings.Builder
		for line := range strings.SplitSeq(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			code.WriteString(line)
			code.WriteByte('\n')
		}
		src := code.String()
		if !strings.Contains(src, "evener-dev/bin dev covstmt") {
			t.Errorf("%s does not count through the Go covstmt primitive "+
				"(`evener-dev/bin dev covstmt`); its statement counting must not "+
				"be a second implementation free to drift from "+
				"internal/devtool/covstmt", path)
		}
		if strings.Contains(src, "python3") {
			t.Errorf("%s still runs python3; its statement-counter was supposed to "+
				"be deleted in favor of the Go covstmt primitive", path)
		}
	}
}
