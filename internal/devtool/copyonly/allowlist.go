package copyonly

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// allowlistHeader is the first line of allowlist.tsv.
const allowlistHeader = "file\ttest\treason"

// Allowed is an allowlist entry: a copy-only test kept on purpose, keyed by
// file and test name (never by line, which moves with every edit above it),
// and why it was kept.
type Allowed struct {
	File, Test, Reason string
}

type testKey struct{ file, test string }

// ReadAllowlist parses allowlist.tsv: a header, then one tab-separated
// file, test, reason row per kept test. Blank lines are skipped. Every entry
// needs a reason, and a test may be listed once.
func ReadAllowlist(r io.Reader) ([]Allowed, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() || sc.Text() != allowlistHeader {
		return nil, fmt.Errorf("allowlist: first line must be the header %q", allowlistHeader)
	}
	var entries []Allowed
	seen := map[testKey]bool{}
	for line := 2; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 3 || strings.TrimSpace(cols[2]) == "" {
			return nil, fmt.Errorf("allowlist line %d: want file, test and a non-empty reason, tab-separated", line)
		}
		key := testKey{cols[0], cols[1]}
		if seen[key] {
			return nil, fmt.Errorf("allowlist line %d: %s %q is listed twice", line, cols[0], cols[1])
		}
		seen[key] = true
		entries = append(entries, Allowed{File: cols[0], Test: cols[1], Reason: cols[2]})
	}
	return entries, sc.Err()
}

// Compare returns the targets the allowlist doesn't cover, and the entries
// that no longer name a target: stale, because the test was converted,
// deleted or renamed.
func Compare(targets []Target, allowed []Allowed) (unlisted []Target, stale []Allowed) {
	listed := map[testKey]bool{}
	for _, a := range allowed {
		listed[testKey{a.File, a.Test}] = true
	}
	found := map[testKey]bool{}
	for _, t := range targets {
		key := testKey{t.File, t.Test}
		found[key] = true
		if !listed[key] {
			unlisted = append(unlisted, t)
		}
	}
	for _, a := range allowed {
		if !found[testKey{a.File, a.Test}] {
			stale = append(stale, a)
		}
	}
	return unlisted, stale
}
