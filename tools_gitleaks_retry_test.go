package evener_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolsGitleaksRetriesTheInstall runs make tools-gitleaks with a go shim
// that fails a fixed number of times before succeeding, proving the recipe's
// retry loop (issue #1057) survives a transient module-download failure within
// its four attempts and fails loudly past them. A sleep shim records the
// escalating delays instead of waiting them out.
func TestToolsGitleaksRetriesTheInstall(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"make", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH (%v), so nothing can run the recipe", tool, err)
		}
	}
	for _, tc := range []struct {
		failures int
		succeeds bool
	}{{3, true}, {4, false}} {
		dir := t.TempDir()
		counter := filepath.Join(dir, "count")
		// A failed `go install` exits non-zero; the first tc.failures calls do.
		goShim := fmt.Sprintf("#!/bin/sh\nn=$(($(cat %q 2>/dev/null || echo 0)+1)); echo $n >%q; [ $n -gt %d ] || exit 1\n", counter, counter, tc.failures)
		sleeps := filepath.Join(dir, "sleeps")
		for name, body := range map[string]string{"go": goShim, "sleep": fmt.Sprintf("#!/bin/sh\necho \"$1\" >>%q\n", sleeps)} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("make", "tools-gitleaks")
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.succeeds || (!tc.succeeds && !strings.Contains(string(out), "after 4 attempts")) {
			t.Fatalf("failures=%d: err=%v\n%s", tc.failures, err, out)
		}
		if slept, _ := os.ReadFile(sleeps); string(slept) != "3\n10\n30\n" {
			t.Fatalf("failures=%d: sleeps %q, want 3, 10, 30", tc.failures, slept)
		}
	}
}
