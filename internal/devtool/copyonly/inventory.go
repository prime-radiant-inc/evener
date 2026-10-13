package copyonly

import (
	"cmp"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The categories of a copy hit: an assertion whose oracle is production copy.
const (
	// CategoryTSAssert is a frontend or native text assertion against app
	// copy: a by-text or by-title query outside a click, or a text matcher.
	CategoryTSAssert = "ts-assert"
	// CategoryTSLocator is a by-text query used only to find a control to
	// click: cheap to move to a role query, and not itself an assertion.
	CategoryTSLocator = "ts-locator"
	// CategoryTSConstant is a by-text query or matcher against an identifier
	// named like copy, imported from code under test.
	CategoryTSConstant = "ts-constant"
	// CategoryGoErrString is a Go text check on an error or error-ish value
	// against a phrase from production source.
	CategoryGoErrString = "go-errstring"
	// CategoryGoCopy is the same check on other output: CLI output, logs,
	// rendered text.
	CategoryGoCopy = "go-copy"
	// CategoryGoAsset is a Go check against a phrase that lives in a non-Go
	// production text asset (an embedded prompt, a template).
	CategoryGoAsset = "go-asset"
)

// Categories is every hit category in report order.
var Categories = []string{
	CategoryTSAssert, CategoryTSLocator, CategoryTSConstant,
	CategoryGoErrString, CategoryGoCopy, CategoryGoAsset,
}

// The purposes of a copy-only test: every check is a copy claim, and the
// test's name says its subject is wording (a target) or behavior (incidental:
// a behavior test whose only oracle happens to be copy).
const (
	PurposeCopy     = "copy"
	PurposeBehavior = "behavior"
)

// Hit is one assertion whose oracle is production copy.
type Hit struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Category string `json:"category"`
	Literal  string `json:"literal"`
}

// CopyOnlyTest is a test whose every check is a copy claim.
type CopyOnlyTest struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Test    string `json:"test"`
	Purpose string `json:"purpose"`
}

// Report is the copy-oracle inventory of a set of tests: every copy hit, how
// many literals per package were fixture data (they appear only in tests, so
// seeing them come out is a real behavioral check), and the copy-only tests.
// The TypeScript scanner in cmd/evener-hub/frontend/src/dev/copyonly emits
// the same shape as JSON.
type Report struct {
	Hits     []Hit          `json:"hits"`
	Fixtures map[string]int `json:"fixtures"`
	CopyOnly []CopyOnlyTest `json:"copy_only"`
}

// Merge combines reports, summing fixture counts per package.
func Merge(reports ...Report) Report {
	merged := Report{Fixtures: map[string]int{}}
	for _, r := range reports {
		merged.Hits = append(merged.Hits, r.Hits...)
		merged.CopyOnly = append(merged.CopyOnly, r.CopyOnly...)
		for pkg, n := range r.Fixtures {
			merged.Fixtures[pkg] += n
		}
	}
	return merged
}

// blankZero is a table cell's count, left empty for zero.
func blankZero(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// packageOf is the directory a test file lives in, "." for the root.
func packageOf(file string) string {
	return path.Dir(file)
}

// WritePackageTable writes the per-package Markdown table of hit counts by
// category, largest first, with the fixture literals each package excluded.
// top > 0 keeps only that many packages; the totals row always covers all.
func WritePackageTable(w io.Writer, r Report, top int) {
	per := map[string]map[string]int{}
	totals := map[string]int{}
	for _, h := range r.Hits {
		if per[packageOf(h.File)] == nil {
			per[packageOf(h.File)] = map[string]int{}
		}
		per[packageOf(h.File)][h.Category]++
		totals[h.Category]++
	}
	sum := func(counts map[string]int) int {
		n := 0
		for _, c := range counts {
			n += c
		}
		return n
	}
	pkgs := make([]string, 0, len(per))
	for pkg := range per {
		pkgs = append(pkgs, pkg)
	}
	sort.Slice(pkgs, func(i, j int) bool {
		if a, b := sum(per[pkgs[i]]), sum(per[pkgs[j]]); a != b {
			return a > b
		}
		return pkgs[i] < pkgs[j]
	})
	shown := pkgs
	if top > 0 && top < len(shown) {
		shown = shown[:top]
	}
	_, _ = fmt.Fprintf(w, "| package | total | %s | fixture (excluded) |\n", strings.Join(Categories, " | "))
	_, _ = fmt.Fprintf(w, "|---|---:|%s---:|\n", strings.Repeat("---:|", len(Categories)))
	for _, pkg := range shown {
		cells := make([]string, len(Categories))
		for i, c := range Categories {
			cells[i] = blankZero(per[pkg][c])
		}
		_, _ = fmt.Fprintf(w, "| `%s` | %d | %s | %s |\n", pkg, sum(per[pkg]), strings.Join(cells, " | "), blankZero(r.Fixtures[pkg]))
	}
	cells := make([]string, len(Categories))
	for i, c := range Categories {
		cells[i] = fmt.Sprintf("**%d**", totals[c])
	}
	_, _ = fmt.Fprintf(w, "| **all (%d packages)** | **%d** | %s | **%d** |\n",
		len(pkgs), len(r.Hits), strings.Join(cells, " | "), sum(r.Fixtures))
}

// WriteHitsTSV writes every hit as file, line, category, literal, sorted;
// a literal's tabs and newlines become spaces and it is cut to 160 runes.
func WriteHitsTSV(w io.Writer, hits []Hit) {
	sorted := slices.Clone(hits)
	slices.SortFunc(sorted, func(a, b Hit) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Category, b.Category), cmp.Compare(a.Literal, b.Literal))
	})
	_, _ = fmt.Fprintln(w, "file\tline\tcategory\tliteral")
	for _, h := range sorted {
		lit := []rune(strings.NewReplacer("\t", " ", "\n", " ").Replace(h.Literal))
		lit = lit[:min(len(lit), 160)]
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", h.File, h.Line, h.Category, string(lit))
	}
}

// areaOf groups a test file for the copy-only table: the frontend as a
// whole, each cmd/ binary, and otherwise the top-level directory.
func areaOf(file string) string {
	parts := strings.Split(file, "/")
	if parts[0] == "cmd" && len(parts) > 2 && parts[2] == "frontend" {
		return strings.Join(parts[:3], "/")
	}
	if parts[0] == "cmd" {
		return strings.Join(parts[:min(len(parts), 2)], "/")
	}
	return parts[0]
}

// WriteCopyOnly writes the copy-only Markdown table per area to table (the
// targets, then the incidental behavior tests) and every copy-only test as
// TSV to tsv: file by file, targets before incidental ones.
func WriteCopyOnly(table, tsv io.Writer, tests []CopyOnlyTest) {
	rows := slices.Clone(tests)
	purposeRank := func(p string) int {
		if p == PurposeCopy {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(rows, func(a, b CopyOnlyTest) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(purposeRank(a.Purpose), purposeRank(b.Purpose)),
			cmp.Compare(a.Line, b.Line))
	})

	per := map[string]map[string]int{}
	totals := map[string]int{}
	for _, t := range rows {
		area := areaOf(t.File)
		if per[area] == nil {
			per[area] = map[string]int{}
		}
		per[area][t.Purpose]++
		totals[t.Purpose]++
	}
	areas := make([]string, 0, len(per))
	for area := range per {
		areas = append(areas, area)
	}
	sort.Slice(areas, func(i, j int) bool {
		if a, b := per[areas[i]][PurposeCopy], per[areas[j]][PurposeCopy]; a != b {
			return a > b
		}
		return areas[i] < areas[j]
	})
	_, _ = fmt.Fprintln(table, "| area | copy-only (targets) | behavior, copy-only oracle (incidental) |")
	_, _ = fmt.Fprintln(table, "|---|---:|---:|")
	for _, area := range areas {
		_, _ = fmt.Fprintf(table, "| `%s` | %s | %s |\n", area, blankZero(per[area][PurposeCopy]), blankZero(per[area][PurposeBehavior]))
	}
	_, _ = fmt.Fprintf(table, "| **all** | **%d** | **%d** |\n", totals[PurposeCopy], totals[PurposeBehavior])

	_, _ = fmt.Fprintln(tsv, "file\tline\ttest\tpurpose")
	for _, t := range rows {
		_, _ = fmt.Fprintf(tsv, "%s\t%d\t%s\t%s\n", t.File, t.Line, t.Test, t.Purpose)
	}
}
