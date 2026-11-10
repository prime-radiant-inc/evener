package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
