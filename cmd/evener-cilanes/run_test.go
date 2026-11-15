package cilanes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/gittest"
)

func TestMain(m *testing.M) {
	remove, err := gittest.IsolateProcess()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	remove()
	os.Exit(code)
}

func TestGitRunsIsolated(t *testing.T) {
	gittest.RequireIsolated(t, git)
}

// commitFixture writes the fixture into a real repository and commits it,
// returning the repository root.
func commitFixture(t *testing.T) string {
	t.Helper()
	root, _ := writeFixture(t, fixtureFiles())
	gittest.Run(t, root, "init", "-q", "-b", "main")
	gittest.Run(t, root, "add", "-A")
	gittest.Run(t, root, "commit", "-q", "-m", "base")
	return root
}

// git reports a moved file under its new name alone unless renames are
// turned off, which would hide the package the file left. Real git, because
// the property is what git diff reports.
func TestRunReportsBothSidesOfAMove(t *testing.T) {
	root := commitFixture(t)
	gittest.Run(t, root, "mv", "agent/leaf/leaf.go", "cmd/evener-doctor/leaf.go")
	gittest.Run(t, root, "commit", "-q", "-m", "move")

	var stdout, stderr strings.Builder
	if code := Run([]string{"--repo", root, "--base", "HEAD^"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr.String())
	}
	got := parseOutputs(t, stdout.String())
	// The agent side runs both race-module-shards agent parts and the root side runs
	// race-root-parts/rest, so neither appears among the exclusions.
	want := map[string]string{
		"skip":                       `["web-unit","web-browser","native","api-package","hub-integration"]`,
		"exclude_tests_parts":        `[]`,
		"exclude_race_root_parts":    `[{"part":"hub"}]`,
		"exclude_race_module_shards": `[{"scope":"nonagent"}]`,
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %s, want %s", name, got[name], value)
		}
	}
}

// The workflow runs every lane when the classifier fails, so a failure must
// write no outputs at all: a partial set could skip lanes nothing decided.
func TestRunWritesNoOutputsWhenItCannotDiff(t *testing.T) {
	root := commitFixture(t)

	var stdout, stderr strings.Builder
	if code := Run([]string{"--repo", root, "--base", "no-such-revision"}, &stdout, &stderr); code == 0 {
		t.Fatal("exit 0 for a base that does not exist")
	}
	if stdout.Len() != 0 {
		t.Fatalf("wrote outputs on failure: %q", stdout.String())
	}
}

func TestRunRequiresABase(t *testing.T) {
	root := commitFixture(t)

	var stdout, stderr strings.Builder
	if code := Run([]string{"--repo", root}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d without --base, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("wrote outputs on a usage error: %q", stdout.String())
	}
}

// A Go file that does not parse leaves the import graph unknown, so the
// classifier fails and the workflow runs everything.
func TestRunFailsOnUnparsableGo(t *testing.T) {
	root := commitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "agent", "leaf", "leaf.go"), []byte("package leaf\nfunc {"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "commit", "-q", "-am", "broken")

	var stdout, stderr strings.Builder
	if code := Run([]string{"--repo", root, "--base", "HEAD^"}, &stdout, &stderr); code == 0 {
		t.Fatal("exit 0 for a tree whose Go does not parse")
	}
	if stdout.Len() != 0 {
		t.Fatalf("wrote outputs on failure: %q", stdout.String())
	}
}
