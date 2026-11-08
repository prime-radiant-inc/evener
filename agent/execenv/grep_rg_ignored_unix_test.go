//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeIgnoredTreeRg writes a fake ripgrep whose --files listings expose one
// gitignored file only when --no-ignore is passed, and whose search finds
// nothing for the pattern "nomatch".
func writeIgnoredTreeRg(t *testing.T, root string) string {
	t.Helper()
	rg := filepath.Join(root, "rg")
	script := `#!/bin/sh
files=0
noignore=0
nomatch=0
dir=
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--no-ignore) noignore=1 ;;
	nomatch) nomatch=1 ;;
	*) dir="$arg" ;;
	esac
done
if [ "$files" = 1 ]; then
	printf 'visible.txt\000'
	if [ "$noignore" = 1 ]; then printf 'skip.log\000'; fi
	exit 0
fi
if [ "$nomatch" = 1 ]; then
	exit 1
fi
printf '%s/visible.txt\0001:needle\n' "$dir"
`
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return rg
}

// An empty grep must read the same whether or not ripgrep is installed: the
// native walk says how many gitignored paths it skipped, and the rg arm must
// compose the same note from ripgrep's own views of the tree (#4222).
func TestRgArmReportsTheExcludedPathsNoteForAnEmptySearch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"visible.txt", "skip.log"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("nothing here\n"), 0o644); err != nil {
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
	rg := writeIgnoredTreeRg(t, root)
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	got, err := withRg.Grep(t.Context(), "nomatch", root, "", false, 100, "content")
	if err != nil {
		t.Fatalf("rg-arm Grep: %v", err)
	}

	if want == "" {
		t.Fatal("native search returned a bare empty result; the test fixture does not exercise the exclusion note")
	}
	if got != want {
		t.Fatalf("rg-arm empty result differs from the native arm:\nrg:     %q\nnative: %q", got, want)
	}
}

// A no-ignore listing that fails is not a listing that came back empty: the
// note is suppressed rather than asserting a count the rg arm could not get.
func TestRgArmSuppressesTheExclusionNoteWhenAListingFails(t *testing.T) {
	root := t.TempDir()
	rg := filepath.Join(root, "rg")
	script := `#!/bin/sh
files=0
noignore=0
dir=
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--no-ignore) noignore=1 ;;
	*) dir="$arg" ;;
	esac
done
if [ "$files" = 1 ]; then
	if [ "$noignore" = 1 ]; then exit 2; fi
	printf 'visible.txt\000'
	exit 0
fi
exit 1
`
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	got, err := env.Grep(t.Context(), "nomatch", root, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if got != "" {
		t.Fatalf("a failed no-ignore listing produced a note anyway: %q, want none", got)
	}
}
