package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/searchresult"
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
// results and then, after a blank line, a note saying it was cut and how to
// see the rest (#4186: a note never reads as a result); a search
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
		if len(lines) != 4 || lines[2] != "" || lines[3] != note {
			t.Errorf("%s: grep %s %s with three results and a cap of 2 = %q, want two results, a blank line, then %q", arm, path, mode, cut, note)
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

// stubGrepRetainedBytes shrinks the retained-output byte budget and restores
// it when the test ends, so fixture-sized result lines can cross it without
// megabytes.
func stubGrepRetainedBytes(t *testing.T, n int) {
	t.Helper()
	orig := grepRetainedBytes
	grepRetainedBytes = n
	t.Cleanup(func() { grepRetainedBytes = orig })
}

// A search's retained output is bounded by bytes, not only by maxResults:
// every emitted line can be as long as the read cap, so a hundred matching
// lines of that shape would retain gigabytes before the join doubles them
// (#4574 round 10, HIGH). The byte budget cuts the way the results cap does
// — the line that crosses it is left out with everything after it, and a
// note says so — while an ordinary search, whose output sits far below the
// budget, comes back unchanged.
func TestGrepBoundsItsRetainedOutputByBytes(t *testing.T) {
	// Not parallel: stubGrepRetainedBytes swaps the package's byte budget.
	stubGrepRetainedBytes(t, 240)
	// 58-byte fixture lines, 60-byte emitted lines once the "N:" prefix
	// counts: the budget measures what is retained, the formatted line.
	lines := make([]string, 5)
	for i := range lines {
		lines[i] = "needle" + strings.Repeat("a", 52) // 58 bytes
	}
	a, err := newGrepAccum("needle", false, 100, "content", 0)
	if err != nil {
		t.Fatal(err)
	}
	a.feed(".", []byte(strings.Join(lines, "\n")+"\n"))
	entries, notes := searchresult.Split(a.finish())
	want := make([]string, 4)
	for i, l := range lines[:4] {
		want[i] = fmt.Sprintf("%d:%s", i+1, l)
	}
	if entries != strings.Join(want, "\n") {
		t.Errorf("five 60-byte matches under a 240-byte budget = %q, want the first four lines %q: the fifth crosses the budget and must be left out", entries, strings.Join(want, "\n"))
	}
	if notes == "" {
		t.Error("byte-budget cut omitted its note: a model must not read the first lines as all the matches")
	}

	// One line longer than the whole budget leaves nothing retained, and
	// the note says why the result is empty.
	huge := "needle" + strings.Repeat("b", 300)
	a2, err := newGrepAccum("needle", false, 100, "content", 0)
	if err != nil {
		t.Fatal(err)
	}
	a2.feed(".", []byte(huge+"\n"))
	entries, notes = searchresult.Split(a2.finish())
	if entries != "" {
		t.Errorf("a 306-byte match under a 240-byte budget = %q, want no entries: a line that alone crosses the budget cannot be retained", entries)
	}
	if notes == "" {
		t.Error("a single over-budget match omitted its note: the empty result must say why")
	}
}

// The same bound holds with the real constants: a named 10 MiB file of ten
// matching 1 MiB lines retains at most the byte budget's worth, cut at the
// line that crosses it, while count mode — one small row — still reports
// every match.
func TestGrepRetainsAtMostTheByteBudgetFromALargeNamedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	big := filepath.Join(root, "big.log")
	line := "needle" + strings.Repeat("x", 1<<20-len("needle"))
	if err := os.WriteFile(big, bytes.Repeat([]byte(line+"\n"), 10), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", big, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	gotLines := strings.Split(entries, "\n")
	// 8 MiB admits seven whole 1 MiB lines once each "N:" prefix counts;
	// the eighth crosses it.
	if len(gotLines) != 7 || len(entries) > grepRetainedBytes+10 {
		t.Errorf("ten 1 MiB matches under the byte budget = %d lines, %d bytes, want seven lines within %d bytes", len(gotLines), len(entries), grepRetainedBytes)
	}
	if notes == "" {
		t.Error("byte-budget cut omitted its note")
	}
	got, err = env.Grep(t.Context(), "needle", big, "", false, 100, "count")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes = searchresult.Split(got)
	if entries != "10" || notes != "" {
		t.Errorf("count mode under the byte budget = %q (notes %q), want the one row 10 and no note: counting retains no lines", entries, notes)
	}
}

// The streaming search's before-context ring is retention too: a context
// line longer than its share of the retained budget keeps its tail — the
// part adjacent to the match the context orients — with the cut disclosed
// in the line itself.
func TestGrepStreamTruncatesOversizedContextToItsByteShare(t *testing.T) {
	// Not parallel: stubGrepRetainedBytes swaps the package's byte budget.
	stubGrepRetainedBytes(t, 300)
	ctx1 := "filler" + strings.Repeat("a", 139) + "END1" // 150 bytes
	ctx2 := "filler" + strings.Repeat("b", 139) + "END2" // 150 bytes
	a, err := newGrepAccum("needle", false, 100, "content", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.feedLargeFile(t.Context(), strings.NewReader(ctx1+"\n"+ctx2+"\nneedle\n")); err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(a.finish())
	gotLines := strings.Split(entries, "\n")
	if len(gotLines) != 3 || gotLines[2] != "3:needle" {
		t.Fatalf("two truncated context lines and a match = %q, want three lines ending in the match", entries)
	}
	for i, want := range []string{"END1", "END2"} {
		if !strings.HasSuffix(gotLines[i], want) {
			t.Errorf("context line %d = %q, want its tail kept: the tail is the end adjacent to the match", i+1, gotLines[i])
		}
		if len(gotLines[i]) > 102 { // "1-" prefix + the 100-byte share
			t.Errorf("context line %d is %d bytes, want at most its 100-byte share plus the line-number prefix", i+1, len(gotLines[i]))
		}
	}
	if notes != "" {
		t.Errorf("context truncation raised a result note %q, want none: the whole output fits the budget", notes)
	}
}
