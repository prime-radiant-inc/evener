package repoaudit

import (
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/gittest"
)

// TestFakeTimerDriftHitsNamesEachTrackedUseUnderTheTrees seeds a repository
// with the drift option inside an audited tree, outside every audited tree,
// and in an untracked file, and expects only the tracked use inside a tree.
func TestFakeTimerDriftHitsNamesEachTrackedUseUnderTheTrees(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) { gittest.Run(t, root, args...) }
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

	// A renamed or removed tree is an error, not a clean scan of nothing.
	if hits, err := fakeTimerDriftHits(root, []string{"web/src", "moved-away"}); err == nil {
		t.Fatalf("hits = %q with no error for a missing tree", hits)
	}
}
