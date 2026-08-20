// Package covstmt counts statements in a Go coverage profile. It is the Go
// counterpart of scripts/lib/covstmt-lib.sh's stmt_counts, which still serves
// the shell coverage runners (coverage-floor.sh); the two
// must agree, because their numbers are compared against the same floors.
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
	"strconv"
)

// blockLine matches a single Go coverage profile block line:
//
//	file.go:startLine.startCol,endLine.endCol stmtCount count
//
// The file path is matched non-greedily so Windows paths containing a drive
// colon still parse; the trailing position and count fields are unambiguous
// since they are digits and dots. This mirrors the python regex in
// covstmt-lib.sh exactly.
var blockLine = regexp.MustCompile(`^(.+?):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$`)

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
// and total statement counts. Non-block lines (the mode header, blank lines,
// comments, anything that does not match the block-line regex) are skipped
// silently, matching the shell helper.
func StmtCountsReader(r io.Reader) (covered, total int, err error) {
	// The python version reads with a 1MB buffer; bufio.Scanner's default
	// 64KB token limit would reject a very long single line, so raise it.
	scanner := bufio.NewScanner(r)
	const bufSize = 1 << 20 // 1 MiB
	scanner.Buffer(make([]byte, bufSize), bufSize)

	// key = "file\x00startLine.startCol,endLine.endCol"; value = (stmtCount, covered)
	type entry struct {
		stmtCount int
		covered   bool
	}
	seen := make(map[string]entry)

	for scanner.Scan() {
		line := scanner.Text()
		m := blockLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file, sl, sc, el, ec, ns, cnt := m[1], m[2], m[3], m[4], m[5], m[6], m[7]

		// Dedupe by the whole (file, position) tuple, not position alone: the
		// same position in two different files is two distinct blocks.
		key := file + "\x00" + sl + "." + sc + "," + el + "." + ec

		stmtCount, serr := strconv.Atoi(ns)
		if serr != nil {
			return 0, 0, fmt.Errorf("covstmt: parsing stmt count %q: %w", ns, serr)
		}
		count, cerr := strconv.Atoi(cnt)
		if cerr != nil {
			return 0, 0, fmt.Errorf("covstmt: parsing count %q: %w", cnt, cerr)
		}

		if prev, ok := seen[key]; ok {
			// stmtCount is the same for every occurrence of a position; keep
			// the existing entry but union the covered flag — ANY hit covers.
			prev.covered = prev.covered || count > 0
			seen[key] = prev
		} else {
			seen[key] = entry{stmtCount: stmtCount, covered: count > 0}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("covstmt: scanning profile: %w", err)
	}

	for _, e := range seen {
		total += e.stmtCount
		if e.covered {
			covered += e.stmtCount
		}
	}
	return covered, total, nil
}
