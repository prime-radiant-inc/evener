//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/searchresult"
)

// writeFakeRg writes a shell-script ripgrep whose --files listings honor
// --max-filesize exactly the way the real one does — the over-cap big.log
// only when no size filter is given, the under-cap small.txt either way —
// and whose search prints one content match in rg's --null shape, or nothing
// at all for the nomatch pattern, so the rg arm's oversize disclosure can be
// tested without the real binary (the same deterministic-rg technique
// TestGrepRipgrepStopsWhenContextIsCanceled uses).
func writeFakeRg(t *testing.T, root string) string {
	t.Helper()
	rg := filepath.Join(root, "rg")
	script := `#!/bin/sh
files=0
capped=0
nomatch=0
dir=
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--max-filesize) capped=1 ;;
	--null) ;;
	nomatch) nomatch=1 ;;
	needle) ;;
	*) dir="$arg" ;;
	esac
done
if [ "$files" = 1 ]; then
	if [ "$capped" = 1 ]; then
		printf '%s/small.txt\000' "$dir"
		exit 0
	fi
	printf '%s/small.txt\000%s/big.log\000' "$dir" "$dir"
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
	// The listing names big.log, so the stat the arm takes of it must find a
	// file over the cap: the fixture has to carry what it claims.
	if err := os.WriteFile(filepath.Join(root, "big.log"), make([]byte, 100), 0o644); err != nil {
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

// A directory search launches the search plus one listing, and no more: the
// over-cap count comes from the files the search's own listing names, so it
// costs one tree walk where it used to cost two (#6426).
//
// One listing is the floor for a result that keeps the note. rg publishes no
// file list alongside search output and reports size skips on stderr only
// under --debug — which costs far more than the listing it would replace, and
// whose stderr outgrew the capture cap on an ignore-heavy tree — so there is
// no way to reach one launch without losing the note. Both result paths pay
// the second launch, the zero-match one included: a directory whose only
// matching files are over the cap must not come back as an unexplained empty.
func TestRgArmLaunchesOneListingPerSearch(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	root := t.TempDir()
	rg := filepath.Join(root, "rg")
	log := filepath.Join(root, "launches")
	script := `#!/bin/sh
files=0
nomatch=0
dir=
for arg in "$@"; do
	case "$arg" in
	--files) files=1 ;;
	--max-filesize) ;;
	--null) ;;
	nomatch) nomatch=1 ;;
	needle) ;;
	*) dir="$arg" ;;
	esac
done
if [ "$files" = 1 ]; then
	printf 'listing\n' >> "` + log + `"
	printf '%s/small.txt\000%s/big.log\000' "$dir" "$dir"
	exit 0
fi
printf 'search\n' >> "` + log + `"
if [ "$nomatch" = 1 ]; then
	exit 1
fi
printf '%s/small.txt\0001:needle small\n' "$dir"
`
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The listing names big.log, so the stat the arm takes of it must find a
	// file over the cap: the fixture has to carry what it claims.
	if err := os.WriteFile(filepath.Join(root, "big.log"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	cases := []struct {
		name, pattern string
		// entries are the result's own lines, with the note split off; a
		// search that matched nothing still owes the over-cap note.
		entries string
	}{
		{name: "with matches", pattern: "needle", entries: "small.txt:1:needle small"},
		{name: "with no matches", pattern: "nomatch", entries: ""},
	}
	for _, tc := range cases {
		if err := os.WriteFile(log, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := env.Grep(t.Context(), tc.pattern, root, "", false, 100, "content")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		launches, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("%s: read launch log: %v", tc.name, err)
		}
		search, listing := 0, 0
		for line := range strings.SplitSeq(strings.TrimSpace(string(launches)), "\n") {
			switch line {
			case "search":
				search++
			case "listing":
				listing++
			}
		}
		if search != 1 {
			t.Fatalf("%s: launched %d ripgrep searches, want 1", tc.name, search)
		}
		if listing != 1 {
			t.Fatalf("%s: launched %d rg listings, want 1: the over-cap count must come from the search's own listing, not a second tree walk", tc.name, listing)
		}
		// The launch is spent on something the caller sees, not on a count
		// that then goes unused: the note survives on both paths.
		if entries, notes := searchresult.Split(got); entries != tc.entries || notes == "" {
			t.Fatalf("%s: result = %q (entries %q, notes %q), want entries %q and the over-cap note", tc.name, got, entries, notes, tc.entries)
		}
	}
}

// The over-cap note is part of the result a caller sees, so its bytes are
// pinned for both the match and the no-match paths: a change that alters the
// count, the wording, or which path carries it is a behavior change, not an
// optimization. The goldens were captured from the code as it stands, so this
// passes before and after any change that keeps the output and fails on one
// that does not — it is the guard on what the second ripgrep launch buys.
func TestRgArmOverCapNoteBytesAreStable(t *testing.T) {
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

	for _, tc := range []struct{ name, pattern, golden string }{
		{name: "match", pattern: "needle", golden: "rg-arm-match.txt"},
		{name: "no match", pattern: "nomatch", golden: "rg-arm-nomatch.txt"},
	} {
		got, err := env.Grep(t.Context(), tc.pattern, root, "", false, 100, "content")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", "grepoversize", tc.golden))
		if err != nil {
			t.Fatalf("%s: read golden %s: %v", tc.name, tc.golden, err)
		}
		if got != string(want) {
			t.Fatalf("%s: result drifted from the pinned note.\n got: %q\nwant: %q", tc.name, got, string(want))
		}
	}
}
