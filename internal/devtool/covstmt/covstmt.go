// Package covstmt counts statements in a Go coverage profile. It is the one
// definition of that count in the repo: coverage-floor.sh and the other shell
// coverage runners invoke it through the `evener dev covstmt` subcommand, so
// the numbers the scripts report and the numbers these tests pin can never
// drift apart. (It replaced the deleted Python stmt_counts helper, whose
// semantics — including the last-wins NumStmt tie-break — it preserves.)
//
// Two properties, both inherited from the shell definition, are the heart of
// the algorithm:
//
//   - Blocks are deduped BY POSITION. A -coverpkg run emits the same block once
//     per test binary, so summing raw lines multiplies the denominator.
//   - A block counts as covered if ANY occurrence hit it. That is what makes it
//     valid to concatenate several profiles — the per-binary duplicates of one
//     run, or the test-track and fuzz-track profiles of the same package — and
//     count the union by reading the concatenation.
package covstmt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
)

// blockLine matches a single Go coverage profile block line:
//
//	file.go:startLine.startCol,endLine.endCol stmtCount count
//
// The file path is matched non-greedily so Windows paths containing a drive
// colon still parse; the trailing position and count fields are unambiguous
// since they are digits and dots. This mirrors the regex the deleted Python
// stmt_counts used, byte for byte.
var blockLine = regexp.MustCompile(`^(.+?):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$`)

// Block is one coverage block after position dedup: the file and line span it
// covers, its statement count, and whether ANY occurrence of its position was
// hit. It is the per-block view of the same parse StmtCounts folds into two
// totals; the gaps report (coverage-gaps.sh, via `evener dev covstmt --gaps`)
// ranks these rather than inventing a second parser.
type Block struct {
	File      string
	StartLine int
	EndLine   int
	StmtCount int
	Covered   bool
}

// blockPos is the dedup identity of a block: the whole (file, start, end)
// position tuple, not position alone — the same position in two different files
// is two distinct blocks.
type blockPos struct {
	file                string
	startLine, startCol int
	endLine, endCol     int
}

type blockEntry struct {
	stmtCount int
	covered   bool
}

// parseProfile reads a Go coverage profile and returns its blocks keyed by
// position, deduped and unioned exactly as the counting contract requires:
// last-wins stmtCount, any-hit covered. Non-block lines (the mode header, blank
// lines, comments, anything that does not match blockLine) are skipped
// silently, matching the deleted shell helper.
func parseProfile(r io.Reader) (map[blockPos]blockEntry, error) {
	// The python version reads with a 1MB buffer; bufio.Scanner's default
	// 64KB token limit would reject a very long single line, so raise it.
	scanner := bufio.NewScanner(r)
	const bufSize = 1 << 20 // 1 MiB
	scanner.Buffer(make([]byte, bufSize), bufSize)

	seen := make(map[blockPos]blockEntry)
	for scanner.Scan() {
		m := blockLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		sl, _ := strconv.Atoi(m[2])
		sc, _ := strconv.Atoi(m[3])
		el, _ := strconv.Atoi(m[4])
		ec, _ := strconv.Atoi(m[5])

		stmtCount, serr := strconv.Atoi(m[6])
		if serr != nil {
			return nil, fmt.Errorf("covstmt: parsing stmt count %q: %w", m[6], serr)
		}
		count, cerr := strconv.Atoi(m[7])
		if cerr != nil {
			return nil, fmt.Errorf("covstmt: parsing count %q: %w", m[7], cerr)
		}

		key := blockPos{file: m[1], startLine: sl, startCol: sc, endLine: el, endCol: ec}
		if prev, ok := seen[key]; ok {
			// stmtCount is the same for every occurrence of a position on
			// real profiles, but the tie-break is pinned anyway: the deleted
			// Python stmt_counts kept the LAST occurrence's count, so
			// last-wins keeps the two implementations equivalent on every
			// input, not just toolchain-produced ones. covered unions
			// separately — ANY hit covers.
			prev.stmtCount = stmtCount
			prev.covered = prev.covered || count > 0
			seen[key] = prev
		} else {
			seen[key] = blockEntry{stmtCount: stmtCount, covered: count > 0}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("covstmt: scanning profile: %w", err)
	}
	return seen, nil
}

// StmtCounts opens the coverage profile at path and reports the covered and
// total statement counts. A missing or unreadable file is an error rather
// than a silent zero.
func StmtCounts(path string) (covered, total int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	return StmtCountsReader(f)
}

// StmtCountsReader parses a Go coverage profile from r and reports the covered
// and total statement counts.
func StmtCountsReader(r io.Reader) (covered, total int, err error) {
	seen, err := parseProfile(r)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range seen {
		total += e.stmtCount
		if e.covered {
			covered += e.stmtCount
		}
	}
	return covered, total, nil
}

// Blocks opens the coverage profile at path and returns its deduped blocks in
// a stable (file, start line, end line) order. A missing or unreadable file is
// an error rather than a silent empty list.
func Blocks(path string) ([]Block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return BlocksReader(f)
}

// BlocksReader parses a Go coverage profile from r and returns its deduped
// blocks. It is the per-block companion to StmtCountsReader, built on the same
// parse so the two can never disagree about what a block is or whether it is
// covered.
func BlocksReader(r io.Reader) ([]Block, error) {
	seen, err := parseProfile(r)
	if err != nil {
		return nil, err
	}
	// Order by the FULL position, columns included. The dedup key distinguishes
	// blocks that share a file and line span but differ in a column, so ordering
	// by (file, startLine, endLine) alone leaves those in map-iteration order —
	// the opposite of the stable-order contract this function promises.
	positions := make([]blockPos, 0, len(seen))
	for p := range seen {
		positions = append(positions, p)
	}
	sort.Slice(positions, func(i, j int) bool {
		a, b := positions[i], positions[j]
		if a.file != b.file {
			return a.file < b.file
		}
		if a.startLine != b.startLine {
			return a.startLine < b.startLine
		}
		if a.startCol != b.startCol {
			return a.startCol < b.startCol
		}
		if a.endLine != b.endLine {
			return a.endLine < b.endLine
		}
		return a.endCol < b.endCol
	})
	out := make([]Block, 0, len(positions))
	for _, p := range positions {
		e := seen[p]
		out = append(out, Block{
			File:      p.file,
			StartLine: p.startLine,
			EndLine:   p.endLine,
			StmtCount: e.stmtCount,
			Covered:   e.covered,
		})
	}
	return out, nil
}
