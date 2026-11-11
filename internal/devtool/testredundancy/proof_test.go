package testredundancy

import (
	"strings"
	"testing"
)

// unescapedPipes counts the Markdown cell separators in a table row.
func unescapedPipes(row string) int {
	return strings.Count(row, "|") - strings.Count(row, `\|`)
}

// Every row of the mutation table has the header's cell count, even when a
// run pattern or a source change carries a pipe, so no result shifts columns.
func TestWriteProofKeepsEachMutationOnOneTableRow(t *testing.T) {
	var b strings.Builder
	WriteProof(&b, CoverageDiff{}, []MutationResult{{
		Name: "alt", File: "a.go", Old: "a || b", New: "a && b", Run: "^(TestA|TestB)$",
		Outcome: OutcomeCaught,
		Failed:  []string{"TestA"},
	}}, func(s string) string { return s })

	var header, rows []string
	for line := range strings.Lines(b.String()) {
		if strings.HasPrefix(line, "| Mutation") {
			header = append(header, line)
		} else if len(header) > 0 && strings.HasPrefix(line, "| ") {
			rows = append(rows, line)
		}
	}
	if len(header) != 1 || len(rows) != 1 {
		t.Fatalf("want one mutation header and one row, got %d and %d:\n%s", len(header), len(rows), b.String())
	}
	if got, want := unescapedPipes(rows[0]), unescapedPipes(header[0]); got != want {
		t.Fatalf("row has %d cell separators, header %d:\n%s", got, want, b.String())
	}
}
