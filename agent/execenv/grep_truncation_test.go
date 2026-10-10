package execenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// writeGrepTruncationTree lays out three files with one match each, and a
// fourth file holding three matching lines for a search of one named file.
func writeGrepTruncationTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"tree/a.txt": "needle a\n",
		"tree/b.txt": "needle b\n",
		"tree/c.txt": "needle c\n",
		"three.txt":  "needle 1\nneedle 2\nneedle 3\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// checkGrepTruncation asks each output mode for three results under a cap of
// two and under a cap of three. A search the cap cut shows the first two
// results and then a line saying it was cut and how to see the rest; a search
// that found exactly as many results as the cap allows says nothing, so a
// model can tell a complete result from a partial one (#4169).
func checkGrepTruncation(t *testing.T, arm string, env *LocalExecutionEnvironment, root string) {
	t.Helper()
	const note = "[results truncated at 2; narrow the path or glob_filter, or raise max_results]"
	// One named file has one files_with_matches or count row, so only its
	// content can hold three results.
	for _, tc := range []struct{ path, mode string }{
		{"tree", "content"}, {"tree", "files_with_matches"}, {"tree", "count"}, {"three.txt", "content"},
	} {
		path, mode := tc.path, tc.mode
		cut, err := env.Grep(t.Context(), "needle", filepath.Join(root, path), "", false, 2, mode)
		if err != nil {
			t.Fatalf("%s: grep %s %s (cap 2): %v", arm, path, mode, err)
		}
		lines := strings.Split(cut, "\n")
		if len(lines) != 3 || lines[2] != note {
			t.Errorf("%s: grep %s %s with three results and a cap of 2 = %q, want two results then %q", arm, path, mode, cut, note)
		}
		whole, err := env.Grep(t.Context(), "needle", filepath.Join(root, path), "", false, 3, mode)
		if err != nil {
			t.Fatalf("%s: grep %s %s (cap 3): %v", arm, path, mode, err)
		}
		if len(strings.Split(whole, "\n")) != 3 || strings.Contains(whole, "truncated") {
			t.Errorf("%s: grep %s %s with three results and a cap of 3 = %q, want the three results and no note", arm, path, mode, whole)
		}
	}
}

func TestGrepSaysWhenTheCapCutItsResults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGrepTruncationTree(t, root)
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	checkGrepTruncation(t, "fallback", fallback, root)

	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed; cannot check the rg arm")
	}
	withRg := NewLocalExecutionEnvironment(root)
	defer withRg.Cleanup()
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	checkGrepTruncation(t, "ripgrep", withRg, root)
}

func TestSandboxGrepSaysWhenTheCapCutItsResults(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	writeGrepTruncationTree(t, worktree)
	checkGrepTruncation(t, "sandboxed", env, worktree)
}
