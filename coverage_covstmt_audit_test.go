package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoverageGapsScriptDelegatesToGoCovstmt proves BEHAVIORALLY, not by
// substring, that scripts/coverage/coverage-gaps.sh counts through the Go
// covstmt primitive. It puts a fake `go` on PATH that records its argv and
// prints a canned report, runs the script, and asserts both the exact delegated
// invocation and that the script surfaces the primitive's output. A second,
// drifted counter (a fresh Python heredoc, a shell reimplementation) would
// either never call `go` or call it with different arguments, and fail here —
// something a source-text scan cannot promise.
func TestCoverageGapsScriptDelegatesToGoCovstmt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	fakeGo := filepath.Join(dir, "go")
	// The stub records every argument, one per line, then prints the report the
	// script is expected to pass through unchanged.
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\nprintf 'FAKE-GAPS-REPORT\\n'\n"
	if err := os.WriteFile(fakeGo, []byte(stub), 0o755); err != nil {
		t.Fatalf("writing fake go: %v", err)
	}
	profile := filepath.Join(dir, "fixture.cov")
	if err := os.WriteFile(profile, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatalf("writing fixture profile: %v", err)
	}

	cmd := exec.Command("bash", "scripts/coverage/coverage-gaps.sh",
		profile, "--by", "file", "--top", "3", "--zero", "--in", "needle")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("coverage-gaps.sh failed under the stub go: %v\noutput:\n%s", err, out)
	}
	if got := string(out); got != "FAKE-GAPS-REPORT\n" {
		t.Fatalf("script output = %q, want the Go primitive's output passed through", got)
	}

	argv, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the script never invoked `go`: %v", err)
	}
	// Absolute profile path: the script resolves it before cd-ing into the repo
	// root, so the shape is fixed regardless of the caller's cwd.
	want := strings.Join([]string{
		"run", "./cmd/evener-dev/bin", "dev", "covstmt",
		"--gaps", "--by=file", "--top=3", "--zero", "--in=needle", profile, "",
	}, "\n")
	if got := string(argv); got != want {
		t.Fatalf("delegated invocation:\n%q\nwant:\n%q", got, want)
	}
}

// TestCoverageScriptsUseGoCovstmt guards the #616 consolidation against a
// reintroduced Python counter: neither coverage script may run python3, and each
// must call the Go primitive. The scan ignores comment lines, so a comment
// mentioning `python3` or the invocation cannot satisfy or defeat it — the
// check is about executable code. coverage-gaps.sh's delegation is pinned
// behaviorally above; this tripwire also covers e2e-cover.sh, whose full run
// (it builds the web UI and an instrumented binary) is too heavy to drive here.
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
