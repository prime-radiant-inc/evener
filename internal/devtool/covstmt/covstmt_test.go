// Package covstmt_test pins the statement counting this repo's coverage
// ratchet depends on: parse a Go coverage profile, dedupe blocks by position,
// and report covered/total statement counts — including the last-wins NumStmt
// tie-break the deleted Python stmt_counts had.
package covstmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfile writes content to a fresh file in a temp dir and returns its
// path. Profiles are built as plain strings so every fixture is readable
// inline, mirroring how the shell heredoc consumed them.
func writeProfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cov.out")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing profile fixture: %v", err)
	}
	return path
}

// assertCounts calls StmtCounts and reports covered/total against want.
func assertCounts(t *testing.T, path string, wantCovered, wantTotal int) {
	t.Helper()
	gotCovered, gotTotal, err := StmtCounts(path)
	if err != nil {
		t.Fatalf("StmtCounts(%q): %v", path, err)
	}
	if gotCovered != wantCovered || gotTotal != wantTotal {
		t.Fatalf("StmtCounts(%q) = (%d, %d), want (%d, %d)",
			path, gotCovered, gotTotal, wantCovered, wantTotal)
	}
}

// TestBasicProfile counts one covered and one uncovered block. The denominator
// is the sum of all block statement counts; the numerator is the sum of the
// covered ones only.
func TestBasicProfile(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 200 1\n" +
		"pkg/file.go:30.1,40.2 300 0\n"
	assertCounts(t, writeProfile(t, profile), 200, 500)
}

// TestDedupesByPosition is the key feature: a -coverpkg run emits the same
// block once per test binary, so the same position appearing twice must count
// the block once in the denominator. A block is covered if ANY occurrence hit
// it, so the second, hit occurrence covers the first, uncovered one.
func TestDedupesByPosition(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 200 0\n" +
		"pkg/file.go:10.1,20.2 200 1\n"
	assertCounts(t, writeProfile(t, profile), 200, 200)
}

// TestDedupesBothUncovered is the any-hit union in the uncovered direction: a
// duplicate position where neither occurrence hit stays uncovered, and the
// denominator is still the single block's statement count.
func TestDedupesBothUncovered(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 200 0\n" +
		"pkg/file.go:10.1,20.2 200 0\n"
	assertCounts(t, writeProfile(t, profile), 0, 200)
}

// TestDuplicatePositionStmtCountIsLastWins pins the NumStmt tie-break for a
// duplicate position whose occurrences disagree: the deleted Python
// stmt_counts kept the LAST occurrence's count, so last-wins is what keeps
// this package equivalent to it on every input. Real go-toolchain profiles
// never disagree (the same position is emitted with the same NumStmt), but
// the pin is the whole point — an off-toolchain profile must not silently
// diverge.
func TestDuplicatePositionStmtCountIsLastWins(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 10 0\n" +
		"pkg/file.go:10.1,20.2 99 1\n"
	assertCounts(t, writeProfile(t, profile), 99, 99)
}

// TestMultipleFiles counts blocks from different files independently: the
// dedup key is the whole (file, position) tuple, not position alone.
func TestMultipleFiles(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/a.go:10.1,20.2 100 1\n" +
		"pkg/b.go:10.1,20.2 50 0\n"
	assertCounts(t, writeProfile(t, profile), 100, 150)
}

// TestIgnoresNonMatchingLines skips the mode header, blank lines, comments,
// and anything that does not match the block-line regex without erroring.
func TestIgnoresNonMatchingLines(t *testing.T) {
	const profile = "mode: set\n" +
		"\n" +
		"# this is a comment the Go profile never carries\n" +
		"pkg/file.go:10.1,20.2 200 1\n" +
		"\n" +
		"not a coverage line at all\n" +
		"pkg/file.go:30.1,40.2 300 0\n"
	assertCounts(t, writeProfile(t, profile), 200, 500)
}

// TestEmptyProfile has only the mode header: zero covered, zero total, no
// error.
func TestEmptyProfile(t *testing.T) {
	assertCounts(t, writeProfile(t, "mode: set\n"), 0, 0)
}

// TestMissingFile returns an error rather than fabricating zeros.
func TestMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.out")
	_, _, err := StmtCounts(path)
	if err == nil {
		t.Fatalf("StmtCounts(%q) on a missing file: want error, got nil", path)
	}
}

// TestLargeBlockNumbers confirms statement counts well above small synthetic
// fixtures are handled at full precision; a 298-statement block is the real
// wave's scale.
func TestLargeBlockNumbers(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 298 1\n"
	assertCounts(t, writeProfile(t, profile), 298, 298)
}

// TestStmtCountsReaderMatchesFile shows the reader variant produces the same
// result as the file variant on the same bytes.
func TestStmtCountsReaderMatchesFile(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 200 1\n" +
		"pkg/file.go:30.1,40.2 300 0\n"
	path := writeProfile(t, profile)
	wantCovered, wantTotal, err := StmtCounts(path)
	if err != nil {
		t.Fatalf("StmtCounts reference: %v", err)
	}
	gotCovered, gotTotal, err := StmtCountsReader(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("StmtCountsReader: %v", err)
	}
	if gotCovered != wantCovered || gotTotal != wantTotal {
		t.Fatalf("StmtCountsReader = (%d, %d), want (%d, %d) matching StmtCounts",
			gotCovered, gotTotal, wantCovered, wantTotal)
	}
}
