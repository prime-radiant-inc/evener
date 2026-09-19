package research

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommittedEnvironmentsValid walks every committed environment under
// research/environments/ and enforces the directory contract.
func TestCommittedEnvironmentsValid(t *testing.T) {
	root := repoRoot(t)
	envDir := filepath.Join(root, "research", "environments")
	entries, err := os.ReadDir(envDir)
	if err != nil {
		t.Fatalf("read env dir: %v", err)
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		found++
		dir := filepath.Join(envDir, e.Name())
		task, err := os.ReadFile(filepath.Join(dir, "task.md"))
		if err != nil || len(strings.TrimSpace(string(task))) == 0 {
			t.Errorf("%s: task.md missing or empty", e.Name())
		}
		st, err := os.Stat(filepath.Join(dir, "verify.sh"))
		if err != nil || st.Mode()&0o111 == 0 {
			t.Errorf("%s: verify.sh missing or not executable", e.Name())
		}
		if _, err := os.Stat(filepath.Join(dir, "repo", "go.mod")); err != nil {
			t.Errorf("%s: repo/go.mod missing", e.Name())
		}
		// Environment-pool contract: alongside task.md + executable
		// verify.sh + repo/go.mod, the rollout runner guarantees GOWORK=off
		// on the environment of every verify.sh and harness child it
		// execs (runner.go childEnv), so verify.sh may rely on the fixture
		// building as a standalone module.
		// The fixture repo must build offline.
		cmd := exec.Command("go", "build", "./...")
		cmd.Dir = filepath.Join(dir, "repo")
		// The fixture module lives outside the repo workspace, so the
		// inherited go.work would otherwise reject the fixture's paths.
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: fixture repo does not build: %v\n%s", e.Name(), err, out)
		}
	}
	if found < 2 {
		t.Fatalf("expected at least 2 committed environments, found %d", found)
	}
}

// repoRoot walks up from the working directory to the repo root (the dir
// containing go.work).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.work)")
		}
		dir = parent
	}
}
