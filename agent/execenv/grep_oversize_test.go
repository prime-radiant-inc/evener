package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/searchresult"
)

// stubGrepStreamWindowBytes shrinks one window of the over-cap streaming
// search and restores it when the test ends, so a fixture-sized file forces
// the window boundaries the real cap meets only at megabytes.
func stubGrepStreamWindowBytes(t *testing.T, n int) {
	t.Helper()
	orig := grepStreamWindowBytes
	grepStreamWindowBytes = n
	t.Cleanup(func() { grepStreamWindowBytes = orig })
}

// A file named as the search target is a deliberate request, so one larger
// than the read cap is searched in bounded windows rather than skipped: its
// matches come back — from the first window to the last — while no read ever
// holds the file whole (#4574 round 4, MEDIUM: the capped read reported
// errGrepFileTooLarge and the named-file path swallowed it into an empty
// result, even when the very first line matched).
func TestGrepSearchesANamedOverCapFileInBoundedWindows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	big := filepath.Join(root, "big.log")
	filler := bytes.Repeat([]byte("filler line padding\n"), 16<<20/len("filler line padding\n")+2)
	content := append(append([]byte("needle at the head\n"), filler...), []byte("needle at the tail\n")...)
	if err := os.WriteFile(big, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(content) <= grepMaxFileBytes {
		t.Fatalf("fixture is %d bytes, want more than the %d-byte cap", len(content), grepMaxFileBytes)
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	tailLine := 2 + len(filler)/len("filler line padding\n")
	got, err := env.Grep(t.Context(), "needle", big, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	want := "1:needle at the head\n" + fmt.Sprintf("%d:needle at the tail", tailLine)
	if got != want {
		t.Fatalf("named over-cap file (content) = %q, want %q", got, want)
	}
	got, err = env.Grep(t.Context(), "needle", big, "", false, 100, "count")
	if err != nil {
		t.Fatal(err)
	}
	if want := "2"; got != want {
		t.Fatalf("named over-cap file (count) = %q, want %q", got, want)
	}
}

// The streaming scan applies the same matching and formatting rules as the
// whole-file feed: every output mode, with and without context lines, with a
// results cap that cuts mid-file, and across window boundaries that split
// lines — its output must be identical to feeding the whole file at once.
func TestGrepStreamMatchesWholeFileFeed(t *testing.T) {
	// Not parallel: stubGrepStreamWindowBytes swaps the package's window.
	stubGrepStreamWindowBytes(t, 7)
	corpus := "needle one\n" +
		"plain\n" +
		"plain\n" +
		"needle two\r\n" +
		"plain\n" +
		"needle three\n" +
		"plain\n" +
		"plain\n" +
		"plain\n" +
		"plain\n" +
		"plain\n" +
		"needle four\n" +
		"plain\n" +
		"tail needle five"
	dir := t.TempDir()
	path := filepath.Join(dir, "corpus.txt")
	if err := os.WriteFile(path, []byte(corpus), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"content", "files_with_matches", "count"} {
		for _, contextLines := range []int{0, 1, 2} {
			for _, maxResults := range []int{0, 3} {
				whole, err := newGrepAccum("needle", false, maxResults, mode, contextLines)
				if err != nil {
					t.Fatal(err)
				}
				wholeStop := whole.feed(".", []byte(corpus))
				streamed, err := newGrepAccum("needle", false, maxResults, mode, contextLines)
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				streamStop, serr := streamed.feedLargeFile(t.Context(), f)
				_ = f.Close()
				if serr != nil {
					t.Fatalf("mode %q context %d cap %d: stream: %v", mode, contextLines, maxResults, serr)
				}
				if streamStop != wholeStop {
					t.Errorf("mode %q context %d cap %d: stream stop = %v, want %v", mode, contextLines, maxResults, streamStop, wholeStop)
				}
				if got, want := streamed.finish(), whole.finish(); got != want {
					t.Errorf("mode %q context %d cap %d: stream output = %q, want the whole-file feed's %q", mode, contextLines, maxResults, got, want)
				}
			}
		}
	}
}

// A named over-cap file whose matching line is itself longer than the read
// cap still cannot be searched — no read may hold one line that long whole —
// but the omission is disclosed, never silent (#4574 round 8, medium 4): the
// result carries the incompleteness note even when nothing else matched, in
// every output mode, while the lines around the skipped one still search.
func TestGrepDisclosesOverlongLinesInANamedOverCapFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	line := "needle" + strings.Repeat("x", 17<<20)
	overlong := filepath.Join(root, "oneline.log")
	if err := os.WriteFile(overlong, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mixed := filepath.Join(root, "mixed.log")
	if err := os.WriteFile(mixed, []byte("needle short\n"+line+"\nneedle tail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", overlong, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "" {
		t.Fatalf("overlong single line (content) = %q, want no entries: a line over the cap cannot be searched", got)
	}
	if notes == "" {
		t.Error("overlong single line (content) omitted the incompleteness note: an empty match must say why")
	}
	got, err = env.Grep(t.Context(), "needle", overlong, "", false, 100, "count")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes = searchresult.Split(got)
	if entries != "" {
		t.Fatalf("overlong single line (count) = %q, want no row: no match was counted", got)
	}
	if notes == "" {
		t.Error("overlong single line (count) omitted the incompleteness note: the undercount must say why")
	}
	got, err = env.Grep(t.Context(), "needle", mixed, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes = searchresult.Split(got)
	if want := "1:needle short\n3:needle tail"; entries != want {
		t.Fatalf("lines around an overlong one (content) = %q, want %q: the skipped line must not stop the rest", got, want)
	}
	if notes == "" {
		t.Error("lines around an overlong one (content) omitted the incompleteness note")
	}
}

// A walk over a directory that holds an over-cap file skips it — the cap is
// what keeps one file's bytes bounded — but not silently: the result says
// how many files were left out and how to search one (#4574 round 4,
// MEDIUM). A directory holding nothing else says so too, so an empty result
// never reads as "genuinely no matches".
func TestGrepWalkDisclosesFilesPastTheReadCap(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	onlyBig := filepath.Join(base, "onlybig")
	if err := os.MkdirAll(onlyBig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), bytes.Repeat([]byte("needle big\n"), 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onlyBig, "big.txt"), bytes.Repeat([]byte("needle big\n"), 10), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "small.txt" {
		t.Fatalf("walk past the read cap (files_with_matches) = %q, want only %q", got, "small.txt")
	}
	if notes == "" {
		t.Error("walk past the read cap (files_with_matches) omitted the over-cap note")
	}
	got, err = env.Grep(t.Context(), "needle", root, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes = searchresult.Split(got)
	if entries != "small.txt:1:needle small" {
		t.Fatalf("walk past the read cap (content) = %q, want only %q", got, "small.txt:1:needle small")
	}
	if notes == "" {
		t.Error("walk past the read cap (content) omitted the over-cap note")
	}
	got, err = env.Grep(t.Context(), "needle", onlyBig, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes = searchresult.Split(got)
	if entries != "" {
		t.Fatalf("walk holding only an over-cap file = %q, want no entries", got)
	}
	if notes == "" {
		t.Error("walk holding only an over-cap file omitted the over-cap note from its empty result")
	}

	// A tree with no over-cap file says nothing: the note appears only when
	// something was actually left out.
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "small.txt"), []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = env.Grep(t.Context(), "needle", plain, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if want := "small.txt:1:needle small"; got != want {
		t.Fatalf("walk with no over-cap file = %q, want bare %q: the note must not appear unearned", got, want)
	}
}

// The sandboxed arm's walk discloses the same over-cap omissions.
func TestSandboxedGrepWalkDisclosesFilesPastTheReadCap(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	if err := os.WriteFile(filepath.Join(worktree, "small.txt"), []byte("needle small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "big.txt"), bytes.Repeat([]byte("needle big\n"), 10), 0o600); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)

	got, err := env.Grep(t.Context(), "needle", worktree, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "small.txt" {
		t.Fatalf("sandboxed walk past the read cap = %q, want only %q", got, "small.txt")
	}
	if notes == "" {
		t.Error("sandboxed walk past the read cap omitted the over-cap note")
	}
}

// The sandboxed arm's named over-cap file is searched in bounded windows too.
func TestSandboxedGrepSearchesANamedOverCapFile(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes and stubGrepStreamWindowBytes swap
	// the package's cap and window.
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	big := filepath.Join(worktree, "big.txt")
	if err := os.WriteFile(big, bytes.Repeat([]byte("needle big\n"), 10), 0o600); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	stubGrepStreamWindowBytes(t, 8)

	got, err := env.Grep(t.Context(), "needle", big, "", false, 100, "count")
	if err != nil {
		t.Fatal(err)
	}
	if want := "10"; got != want {
		t.Fatalf("sandboxed named over-cap file (count) = %q, want %q", got, want)
	}
	var wantLines []string
	for i := 1; i <= 10; i++ {
		wantLines = append(wantLines, fmt.Sprintf("%d:needle big", i))
	}
	got, err = env.Grep(t.Context(), "needle", big, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(wantLines, "\n"); got != want {
		t.Fatalf("sandboxed named over-cap file (content) = %q, want %q", got, want)
	}
}

// The streaming scan stays bounded on files a text search should refuse: it
// ends at the first window holding a NUL (a binary tail, the same rule the
// whole-file feed applies to the whole file), and a single line longer than
// the read cap is skipped — its number spent, the scan resuming after it —
// rather than held whole.
func TestGrepStreamSkipsABinaryTailAndOverlongLines(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes and stubGrepStreamWindowBytes swap
	// the package's cap and window.
	root := t.TempDir()
	binaryTail := filepath.Join(root, "binarytail.log")
	if err := os.WriteFile(binaryTail, []byte("needle head\n"+strings.Repeat("a", 40)+"\nneedle mid\n\x00needle tail\n"+strings.Repeat("b", 20)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlong := filepath.Join(root, "overlong.log")
	if err := os.WriteFile(overlong, []byte("needle\n"+strings.Repeat("x", 30)+"needle"+strings.Repeat("x", 40)+"\nneedle end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	stubGrepStreamWindowBytes(t, 16)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", binaryTail, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	// "needle mid" completes inside the last window before the NUL one, so
	// it stands; nothing at or past the NUL window does. A binary stop is
	// the feed's whole-file binary skip, not a length omission: no note.
	if want := "1:needle head\n3:needle mid"; got != want {
		t.Fatalf("over-cap file with a binary tail = %q, want %q", got, want)
	}
	got, err = env.Grep(t.Context(), "needle", overlong, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if want := "1:needle\n3:needle end"; entries != want {
		t.Fatalf("over-cap file with an overlong line = %q, want entries %q", got, want)
	}
	if notes == "" {
		t.Error("over-cap file with an overlong line omitted the incompleteness note")
	}
}
