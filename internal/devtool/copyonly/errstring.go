package copyonly

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// errStringHeader is the first line of errstring_allowlist.tsv.
const errStringHeader = "file\tcount"

// ErrStringBudget is the number of error-message phrase assertions a test
// file may still hold: checks of an error's text against a phrase from
// production source (the go-errstring hits). The budget only goes down.
type ErrStringBudget struct {
	File  string
	Count int
}

// ErrStringDelta is a test file whose count of error-message assertions
// differs from its budget.
type ErrStringDelta struct {
	File          string
	Count, Budget int
}

// ScanErrStrings counts the error-message phrase assertions in each Go test
// git tracks in the checkout at root, keyed by file. Files with none are left
// out.
func ScanErrStrings(root string) (map[string]int, error) {
	r, err := inventory(root, "", true)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, h := range r.Hits {
		if h.Category == CategoryGoErrString {
			counts[h.File]++
		}
	}
	return counts, nil
}

// ReadErrStringBudgets parses errstring_allowlist.tsv: a header, then one
// tab-separated file, count row per test file. Blank lines are skipped. A
// count is a positive integer and a file may be listed once.
func ReadErrStringBudgets(r io.Reader) ([]ErrStringBudget, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() || sc.Text() != errStringHeader {
		return nil, fmt.Errorf("error-string allowlist: first line must be the header %q", errStringHeader)
	}
	var budgets []ErrStringBudget
	seen := map[string]bool{}
	for line := 2; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 2 {
			return nil, fmt.Errorf("error-string allowlist line %d: want file and count, tab-separated", line)
		}
		count, err := strconv.Atoi(cols[1])
		if err != nil || count < 1 {
			return nil, fmt.Errorf("error-string allowlist line %d: count must be a positive integer", line)
		}
		if seen[cols[0]] {
			return nil, fmt.Errorf("error-string allowlist line %d: %s is listed twice", line, cols[0])
		}
		seen[cols[0]] = true
		budgets = append(budgets, ErrStringBudget{File: cols[0], Count: count})
	}
	return budgets, sc.Err()
}

// CompareErrStrings returns the files holding more error-message assertions
// than their budget (a file with no budget has none), and the files holding
// fewer: slack to take back by lowering or removing the budget, so the list
// stays a true record of what is left to convert.
func CompareErrStrings(counts map[string]int, budgets []ErrStringBudget) (over, slack []ErrStringDelta) {
	budget := map[string]int{}
	for _, b := range budgets {
		budget[b.File] = b.Count
	}
	files := map[string]bool{}
	for f := range counts {
		files[f] = true
	}
	for f := range budget {
		files[f] = true
	}
	for f := range files {
		d := ErrStringDelta{File: f, Count: counts[f], Budget: budget[f]}
		switch {
		case d.Count > d.Budget:
			over = append(over, d)
		case d.Count < d.Budget:
			slack = append(slack, d)
		}
	}
	byFile := func(a, b ErrStringDelta) int { return cmp.Compare(a.File, b.File) }
	slices.SortFunc(over, byFile)
	slices.SortFunc(slack, byFile)
	return over, slack
}
