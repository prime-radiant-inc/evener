//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/searchresult"
)

// writeFakeRg writes a shell-script ripgrep whose --files listings honor
// --max-filesize exactly the way the real one does — one candidate file
// under any size filter, two without — and whose search prints one content
// match in rg's --null shape, or nothing at all for the nomatch pattern, so
// the rg arm's oversize disclosure can be tested without the real binary
// (the same deterministic-rg technique TestGrepRipgrepStopsWhenContextIsCanceled
// uses).
func writeFakeRg(t *testing.T, root string) string {
	t.Helper()
	rg := filepath.Join(root, "rg")
	script := `#!/bin/sh
capped=0
files=0
nomatch=0
dir=
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--max-filesize) capped=1 ;;
	nomatch) nomatch=1 ;;
	*) dir="$arg" ;;
	esac
done
if [ "$files" = 1 ]; then
	printf 'small.txt\000'
	if [ "$capped" = 0 ]; then printf 'big.log\000'; fi
	exit 0
fi
if [ "$nomatch" = 1 ]; then
	exit 1
fi
printf '%s/small.txt\0001:needle small\n' "$dir"
`
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return rg
}

// A directory search on the rg arm leaves over-cap files out just as the
// native walk does — rg skips them by --max-filesize without a trace in its
// output — so the rg arm discloses the same omission note finishWalk
// composes, from ripgrep's own two views of the tree (#4574 round 5, medium
// 3): the files it lists without the size filter minus the ones it lists
// with it.
func TestRgArmDisclosesFilesPastTheReadCap(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	root := t.TempDir()
	rg := writeFakeRg(t, root)
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.log"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "small.txt:1:needle small" {
		t.Fatalf("rg-arm directory search with an over-cap file = %q, want entries %q", got, "small.txt:1:needle small")
	}
	if notes == "" {
		t.Error("rg-arm directory search with an over-cap file omitted the over-cap note")
	}
}

// The no-match path discloses too: a directory whose only matching files are
// over-cap must not return an unexplained empty just because ripgrep is
// installed.
func TestRgArmDisclosesFilesPastTheReadCapOnNoMatches(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	root := t.TempDir()
	rg := writeFakeRg(t, root)
	if err := os.MkdirAll(filepath.Join(root, "onlybig"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	got, err := env.Grep(t.Context(), "nomatch", root, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "" {
		t.Fatalf("rg-arm search with no matches and an over-cap file = %q, want no entries", got)
	}
	if notes == "" {
		t.Error("rg-arm no-match search with an over-cap file omitted the over-cap note from its empty result")
	}
}

// A listing that fails is not a listing that came back empty: when the
// capped listing fails (here it exits 2), the uncapped one's count must not
// be read as "every candidate oversized" — the note is suppressed, because a
// best-effort disclosure must not assert a count it could not obtain
// (#4574 round 8, medium 2).
func TestRgArmSuppressesTheOversizeNoteWhenAListingFails(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	root := t.TempDir()
	rg := filepath.Join(root, "rg")
	script := `#!/bin/sh
capped=0
files=0
dir=
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--max-filesize) capped=1 ;;
	*) dir="$arg" ;;
	esac
done
if [ "$files" = 1 ]; then
	if [ "$capped" = 1 ]; then
		exit 2
	fi
	printf 'small.txt\000big.log\000'
	exit 0
fi
printf '%s/small.txt\0001:needle small\n' "$dir"
`
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "small.txt:1:needle small" {
		t.Fatalf("search with a failed capped listing = %q, want entries %q", got, "small.txt:1:needle small")
	}
	if notes != "" {
		t.Fatalf("a failed capped listing produced a note anyway: %q, want none — a failed listing is not an empty one", notes)
	}
}
