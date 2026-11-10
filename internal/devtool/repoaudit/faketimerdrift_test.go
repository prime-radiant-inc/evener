package repoaudit

import (
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestFakeTimerDriftHitsNamesEachTrackedUseUnderTheTrees seeds a repository
// with the drift option inside an audited tree, outside every audited tree,
// and in an untracked file, and expects only the tracked use inside a tree.
func TestFakeTimerDriftHitsNamesEachTrackedUseUnderTheTrees(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	writeScriptFixture(t, filepath.Join(root, "web", "src", "clean.test.ts"), "vi.useFakeTimers();\n")
	writeScriptFixture(t, filepath.Join(root, "web", "src", "drift.test.ts"), "// fake clock\nvi.useFakeTimers({ shouldAdvanceTime: true });\n")
	writeScriptFixture(t, filepath.Join(root, "docs", "notes.md"), "shouldAdvanceTime is banned\n")
	git("add", ".")
	writeScriptFixture(t, filepath.Join(root, "web", "src", "untracked.test.ts"), "vi.useFakeTimers({ shouldAdvanceTime: true });\n")

	hits, err := fakeTimerDriftHits(root, []string{"web/src"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"web/src/drift.test.ts:2"}; !slices.Equal(hits, want) {
		t.Fatalf("hits = %q, want %q", hits, want)
	}

	git("rm", "-q", "--cached", "web/src/drift.test.ts")
	hits, err = fakeTimerDriftHits(root, []string{"web/src"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %q after the only tracked use left the index, want none", hits)
	}
}
