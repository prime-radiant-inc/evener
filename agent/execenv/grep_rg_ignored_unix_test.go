//go:build linux || darwin

package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/searchresult"
)

// writeIgnoredTreeRg writes a fake ripgrep whose --files listings report one
// visible file (so the oversize count is zero) and whose search finds nothing.
// cappedExit is the status the --max-filesize listing exits with; 0 makes it
// succeed.
func writeIgnoredTreeRg(t *testing.T, root string, cappedExit int) string {
	t.Helper()
	rg := filepath.Join(root, "rg")
	script := fmt.Sprintf(`#!/bin/sh
files=0
capped=0
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--max-filesize) capped=1 ;;
	*) : ;;
	esac
done
if [ "$files" = 1 ]; then
	if [ "$capped" = 1 ] && [ %d -ne 0 ]; then exit %d; fi
	printf 'visible.txt\000'
	exit 0
fi
exit 1
`, cappedExit, cappedExit)
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return rg
}

// An empty grep must read the same whether or not ripgrep is installed: the
// native walk says how many gitignored paths it skipped, and the rg arm asks
// that same walk so the count means the same thing on both arms (#4222).
func TestRgArmReportsTheExcludedPathsNoteForAnEmptySearch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build/a.txt", "build/b.txt", "visible.txt", "skip.log"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("nothing here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	native := NewLocalExecutionEnvironment(root)
	defer native.Cleanup()
	native.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	want, err := native.Grep(t.Context(), "nomatch", root, "", false, 100, "content")
	if err != nil {
		t.Fatalf("native Grep: %v", err)
	}

	withRg := NewLocalExecutionEnvironment(root)
	defer withRg.Cleanup()
	rg := writeIgnoredTreeRg(t, root, 0)
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	got, err := withRg.Grep(t.Context(), "nomatch", root, "", false, 100, "content")
	if err != nil {
		t.Fatalf("rg-arm Grep: %v", err)
	}

	if want == "" {
		t.Fatal("native search returned a bare empty result; the test fixture does not exercise the exclusion note")
	}
	// build/ counts once as a directory, skip.log once as a file: two paths,
	// not the three files the old rg-listing subtraction would have counted.
	if !strings.Contains(want, "2 dotfile/gitignored") {
		t.Fatalf("native exclusion note = %q, want two excluded paths", want)
	}
	if got != want {
		t.Fatalf("rg-arm empty result differs from the native arm:\nrg:     %q\nnative: %q", got, want)
	}
}

// The counts are independent disclosures: a failed oversize listing must not
// suppress the exclusion note the native walk can still supply.
func TestRgArmKeepsTheExclusionNoteWhenTheOversizeListingFails(t *testing.T) {
	root := t.TempDir()
	rg := writeIgnoredTreeRg(t, root, 2)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"visible.txt", "skip.log"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("nothing here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	got, err := env.Grep(t.Context(), "nomatch", root, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "" || !strings.Contains(notes, "excluded") {
		t.Fatalf("empty result = %q, want the exclusion note despite the failed oversize listing", got)
	}
	if strings.Contains(notes, "larger than") {
		t.Fatalf("a failed oversize listing produced an oversize note anyway: %q", notes)
	}
}

// The count comes from the native ignore engine, so an ignored directory is
// one excluded path (not one per file) and a .ignore file is not native
// grep's rule at all.
func TestCountNativeExcludedCountsTheNativeWalk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build/a.txt", "build/b.txt", "skip.log", "visible.txt", "secret.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("nothing here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	got, err := env.countNativeExcluded(t.Context(), root)
	if err != nil {
		t.Fatalf("countNativeExcluded: %v", err)
	}
	if got != 2 {
		t.Fatalf("countNativeExcluded = %d, want 2 (build once, skip.log once)", got)
	}
}
