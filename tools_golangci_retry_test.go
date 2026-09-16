package evener_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolsGolangciRetriesTheDownload runs make tools-golangci with a curl
// shim that fails a fixed number of times before printing a no-op installer,
// proving the recipe's retry loop (issue #1145) succeeds within its four
// attempts and fails loudly past them. A sleep shim records the escalating
// delays (issue #1380) instead of waiting them out.
func TestToolsGolangciRetriesTheDownload(t *testing.T) {
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
		curl := fmt.Sprintf("#!/bin/sh\nn=$(($(cat %q 2>/dev/null || echo 0)+1)); echo $n >%q; [ $n -gt %d ] || exit 22; echo 'exit 0'\n", counter, counter, tc.failures)
		sleeps := filepath.Join(dir, "sleeps")
		for name, body := range map[string]string{"curl": curl, "sleep": fmt.Sprintf("#!/bin/sh\necho \"$1\" >>%q\n", sleeps)} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("make", "tools-golangci")
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
