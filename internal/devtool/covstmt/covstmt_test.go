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

// TestDedupKeyKeepsRawPositionText pins the equivalence the dedup key owes the
// deleted Python stmt_counts: it keyed on the RAW regex captures, so `010` and
// `10` are distinct positions even though they parse to the same integer.
// Keying on the parsed integer instead would collapse the two blocks and
// silently drop one from the denominator — Python reports 50/90 here, a
// parsed-int key reports 50/50.
func TestDedupKeyKeepsRawPositionText(t *testing.T) {
	const profile = "mode: set\n" +
		"pkg/a.go:010.1,20.2 40 0\n" +
		"pkg/a.go:10.1,20.2 50 1\n"
	assertCounts(t, writeProfile(t, profile), 50, 90)

	// The per-block view must see both blocks too, or the two views disagree.
	blocks, err := BlocksReader(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("BlocksReader: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("BlocksReader returned %d blocks, want 2 (`010` and `10` are distinct positions): %+v",
			len(blocks), blocks)
	}
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

// TestWhitespacePaddedLinesParse pins the deleted Python's `line.strip()`: a
// block line with surrounding whitespace, or a trailing CR from a CRLF profile,
// must still be counted rather than silently skipped.
func TestWhitespacePaddedLinesParse(t *testing.T) {
	const profile = "mode: set\n" +
		"\tpkg/file.go:10.1,20.2 200 1  \n" +
		"pkg/file.go:30.1,40.2 300 0\r\n"
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

// TestStmtCountsUnionCountsProfilesAsOne pins the union arithmetic the shell
// scripts used to get by concatenating profiles: a block is deduped by position
// across the files and counts once, covered if ANY profile hit it.
func TestStmtCountsUnionCountsProfilesAsOne(t *testing.T) {
	testTrack := writeProfile(t, "mode: set\n"+
		"pkg/a.go:10.1,20.2 40 1\n"+ // covered by the test track only
		"pkg/a.go:30.1,40.2 60 0\n")
	fuzzTrack := writeProfile(t, "mode: set\n"+
		"pkg/a.go:10.1,20.2 40 0\n"+
		"pkg/a.go:30.1,40.2 60 1\n") // covered by the fuzz track only
	covered, total, err := StmtCountsUnion([]string{testTrack, fuzzTrack})
	if err != nil {
		t.Fatalf("StmtCountsUnion: %v", err)
	}
	if covered != 100 || total != 100 {
		t.Fatalf("StmtCountsUnion = (%d, %d), want (100, 100): each block counted once, covered",
			covered, total)
	}
}

// TestStmtCountsUnionHandlesUnterminatedProfile pins the hazard the union avoids
// by reading profiles instead of concatenating them: a profile whose final block
// line lacks a trailing newline must still contribute that block, and must not
// swallow the next profile's header (which concatenation would).
func TestStmtCountsUnionHandlesUnterminatedProfile(t *testing.T) {
	a := writeProfile(t, "mode: set\npkg/a.go:10.1,20.2 40 1\npkg/a.go:30.1,40.2 60 0") // no final newline
	b := writeProfile(t, "mode: set\npkg/b.go:1.1,2.2 5 0\n")
	covered, total, err := StmtCountsUnion([]string{a, b})
	if err != nil {
		t.Fatalf("StmtCountsUnion: %v", err)
	}
	if covered != 40 || total != 105 {
		t.Fatalf("StmtCountsUnion = (%d, %d), want (40, 105): the unterminated block must survive",
			covered, total)
	}
}

// TestStmtCountsUnionMissingFileFails returns an error rather than a partial
// union, so a caller cannot count a set with a silently missing member.
func TestStmtCountsUnionMissingFileFails(t *testing.T) {
	_, _, err := StmtCountsUnion([]string{filepath.Join(t.TempDir(), "nope.out")})
	if err == nil {
		t.Fatalf("StmtCountsUnion on a missing file: want error, got nil")
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
	// One fixture per tie-break field, so a sort keyed on only part of the
	// position fails here. The startCol fixture differs in startCol and endCol;
	// the endCol fixture shares file, startLine, startCol, AND endLine and
	// differs only in endCol. In both, ascending order puts the lower-position
	// block (count 0, uncovered) first; without the tie-break the two stay in
	// map order and the assertion flips run to run.
	fixtures := map[string]string{
		"startCol tie-break": "mode: set\n" +
			"pkg/f.go:10.1,20.2 1 0\n" +
			"pkg/f.go:10.5,20.9 1 1\n",
		"endCol tie-break": "mode: set\n" +
			"pkg/f.go:10.1,20.2 1 0\n" +
			"pkg/f.go:10.1,20.9 1 1\n",
	}
	for name, profile := range fixtures {
		t.Run(name, func(t *testing.T) {
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
		})
	}
}

// TestBlocksReaderRejectsOutOfRangePosition pins the position-field parse
// errors: a block line whose line or column overflows int must fail the parse
// rather than collapse to 0 and merge two distinct positions into one, which
// would silently change the statement total.
func TestBlocksReaderRejectsOutOfRangePosition(t *testing.T) {
	const huge = "99999999999999999999" // > int64 max
	tests := []struct {
		name  string
		line  string
		wants string
	}{
		{"start line", "pkg/f.go:" + huge + ".1,2.2 1 1\n", "parsing start line"},
		{"start column", "pkg/f.go:1." + huge + ",2.2 1 1\n", "parsing start column"},
		{"end line", "pkg/f.go:1.1," + huge + ".2 1 1\n", "parsing end line"},
		{"end column", "pkg/f.go:1.1,2." + huge + " 1 1\n", "parsing end column"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BlocksReader(strings.NewReader("mode: set\n" + tc.line))
			if err == nil {
				t.Fatalf("BlocksReader accepted an out-of-range %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}
