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
	"strings"
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
// is two distinct blocks. The position fields are the RAW regex captures, not
// parsed integers, because the deleted Python stmt_counts keyed on the raw text
// (`key = (f, sl, sc, el, ec)`): `010` and `10` are the same number but
// different text, and collapsing them would silently change the totals for an
// accepted off-toolchain profile. Integers are derived only for validation and
// for the rendered output.
type blockPos struct {
	file                                 string
	startLine, startCol, endLine, endCol string
}

// blockEntry is a block's decoded values: the parsed position (for output and
// ordering) alongside the statement count and any-hit coverage the dedup folds.
type blockEntry struct {
	startLine, startCol, endLine, endCol int
	stmtCount                            int
	covered                              bool
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
		// The deleted Python matched `line.strip()`, so a block line padded with
		// whitespace (or carrying a trailing CR from a CRLF profile) still
		// parsed. Match the trimmed line so those are not silently skipped.
		m := blockLine.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if m == nil {
			continue
		}
		// Every numeric field is parsed, so a malformed one (a position beyond
		// int range) fails loudly instead of being carried as an unusable
		// position. The dedup key itself is the RAW capture, not this integer:
		// keying on the parsed value would collapse `010` and `10` into one
		// block and silently change the total, whereas the deleted Python
		// stmt_counts kept them distinct.
		sl, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, fmt.Errorf("covstmt: parsing start line %q: %w", m[2], err)
		}
		sc, err := strconv.Atoi(m[3])
		if err != nil {
			return nil, fmt.Errorf("covstmt: parsing start column %q: %w", m[3], err)
		}
		el, err := strconv.Atoi(m[4])
		if err != nil {
			return nil, fmt.Errorf("covstmt: parsing end line %q: %w", m[4], err)
		}
		ec, err := strconv.Atoi(m[5])
		if err != nil {
			return nil, fmt.Errorf("covstmt: parsing end column %q: %w", m[5], err)
		}

		stmtCount, serr := strconv.Atoi(m[6])
		if serr != nil {
			return nil, fmt.Errorf("covstmt: parsing stmt count %q: %w", m[6], serr)
		}
		count, cerr := strconv.Atoi(m[7])
		if cerr != nil {
			return nil, fmt.Errorf("covstmt: parsing count %q: %w", m[7], cerr)
		}

		key := blockPos{file: m[1], startLine: m[2], startCol: m[3], endLine: m[4], endCol: m[5]}
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
			seen[key] = blockEntry{
				startLine: sl,
				startCol:  sc,
				endLine:   el,
				endCol:    ec,
				stmtCount: stmtCount,
				covered:   count > 0,
			}
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

// StmtCountsUnion counts several profiles as one: a block is deduped by
// position across all of them and counts as covered if ANY profile hit it —
// the same arithmetic as concatenating the files, without the concatenation.
// That matters because a profile whose final line lacks a newline would fuse
// with the next profile's header under plain concatenation, and the parser
// would skip both lines and silently drop a block. Profiles are merged in argument
// order, so a duplicate position's statement count is the LAST profile's, the
// tie-break the counting contract pins.
func StmtCountsUnion(paths []string) (covered, total int, err error) {
	seen := make(map[blockPos]blockEntry)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return 0, 0, err
		}
		merged, err := parseProfile(f)
		_ = f.Close()
		if err != nil {
			return 0, 0, err
		}
		for k, e := range merged {
			if prev, ok := seen[k]; ok {
				prev.stmtCount = e.stmtCount
				prev.covered = prev.covered || e.covered
				seen[k] = prev
			} else {
				seen[k] = e
			}
		}
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
		ae, be := seen[a], seen[b]
		if ae.startLine != be.startLine {
			return ae.startLine < be.startLine
		}
		if ae.startCol != be.startCol {
			return ae.startCol < be.startCol
		}
		if ae.endLine != be.endLine {
			return ae.endLine < be.endLine
		}
		if ae.endCol != be.endCol {
			return ae.endCol < be.endCol
		}
		// Same parsed position but different raw text (`010` vs `10`): order by
		// the raw captures so the order stays total and map-independent.
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
			StartLine: e.startLine,
			EndLine:   e.endLine,
			StmtCount: e.stmtCount,
			Covered:   e.covered,
		})
	}
	return out, nil
}
