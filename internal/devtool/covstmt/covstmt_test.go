// Package covstmt_test pins the statement counting this repo's coverage
// ratchet depends on: parse a Go coverage profile, dedupe blocks by position,
// and report covered/total statement counts — including the last-wins NumStmt
// tie-break the deleted Python stmt_counts had.
package covstmt

import (
	"os"
	"path/filepath"
	"strconv"
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

// TestBlocksKeepsPositionDedupAndAnyHitUnion pins the per-block view's two
// load-bearing properties against the same fixtures StmtCounts uses: duplicate
// positions collapse to one block (last-wins stmtCount) and any hit covers.
func TestBlocksKeepsPositionDedupAndAnyHitUnion(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/file.go:10.1,20.2 10 0\n" +
		"pkg/file.go:10.1,20.2 99 1\n" + // same position: last-wins count, any-hit covered
		"pkg/file.go:30.1,40.2 300 0\n" // uncovered
	blocks, err := BlocksReader(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("BlocksReader: %v", err)
	}
	want := []Block{
		{File: "pkg/file.go", StartLine: 10, EndLine: 20, StmtCount: 99, Covered: true},
		{File: "pkg/file.go", StartLine: 30, EndLine: 40, StmtCount: 300, Covered: false},
	}
	if len(blocks) != len(want) {
		t.Fatalf("BlocksReader returned %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for i := range want {
		if blocks[i] != want[i] {
			t.Fatalf("block %d = %+v, want %+v", i, blocks[i], want[i])
		}
	}
}

// TestBlocksFileOrderIsStable means callers (the gaps report) never depend on
// map iteration order: blocks come back sorted by file then line span.
func TestBlocksFileOrderIsStable(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/z.go:10.1,20.2 1 1\n" +
		"pkg/a.go:40.1,50.2 1 0\n" +
		"pkg/a.go:10.1,20.2 1 1\n" +
		"other/b.go:5.1,6.2 1 0\n"
	blocks, err := BlocksReader(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("BlocksReader: %v", err)
	}
	want := []string{"other/b.go:5", "pkg/a.go:10", "pkg/a.go:40", "pkg/z.go:10"}
	if len(blocks) != len(want) {
		t.Fatalf("BlocksReader returned %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for i, b := range blocks {
		got := b.File + ":" + strconv.Itoa(b.StartLine)
		if got != want[i] {
			t.Fatalf("block %d = %s, want %s (all: %+v)", i, got, want[i], blocks)
		}
	}
}

// TestBlocksReaderMatchesStmtCountsTotals is the drift guard between the two
// views: folding the blocks must reproduce StmtCounts exactly on the same
// bytes, including the duplicate-position cases.
func TestBlocksReaderMatchesStmtCountsTotals(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/a.go:1.1,2.2 40 1\n" +
		"pkg/a.go:1.1,2.2 40 0\n" +
		"pkg/a.go:3.1,4.2 60 0\n" +
		"pkg/b.go:1.1,0.0 7 0\n"
	wantCovered, wantTotal, err := StmtCountsReader(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("StmtCountsReader: %v", err)
	}
	blocks, err := BlocksReader(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("BlocksReader: %v", err)
	}
	covered, total := 0, 0
	for _, b := range blocks {
		total += b.StmtCount
		if b.Covered {
			covered += b.StmtCount
		}
	}
	if covered != wantCovered || total != wantTotal {
		t.Fatalf("folded blocks = (%d, %d), want (%d, %d) from StmtCountsReader",
			covered, total, wantCovered, wantTotal)
	}
}

// TestBlocksMissingFile returns an error rather than a silent empty list.
func TestBlocksMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.out")
	if _, err := Blocks(path); err == nil {
		t.Fatalf("Blocks(%q) on a missing file: want error, got nil", path)
	}
}

// TestBlocksOrderIsTotalOverColumns pins the determinism the stable-order
// contract promises: two blocks that share file and line span but differ in a
// column must come back in a fixed order, not map-iteration order. Ordered by
// startCol, the 10.1 block (uncovered) precedes the 10.5 block (covered); a
// sort keyed only on (file, startLine, endLine) would let these swap run to run.
func TestBlocksOrderIsTotalOverColumns(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/f.go:10.1,20.2 1 0\n" +
		"pkg/f.go:10.5,20.9 1 1\n"
	for i := range 100 {
		blocks, err := BlocksReader(strings.NewReader(profile))
		if err != nil {
			t.Fatalf("BlocksReader: %v", err)
		}
		if len(blocks) != 2 {
			t.Fatalf("BlocksReader returned %d blocks, want 2: %+v", len(blocks), blocks)
		}
		if blocks[0].Covered || !blocks[1].Covered {
			t.Fatalf("iteration %d: blocks out of position order: %+v", i, blocks)
		}
	}
}
