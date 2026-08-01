package serf_test

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioMarkdownLineCitation matches a citation of a markdown document by
// line number, e.g. `docs/job-control.md:940` or `docs/job-control.md:148,424`.
var scenarioMarkdownLineCitation = regexp.MustCompile(`[A-Za-z0-9._/-]+\.md:\d+`)

// scenarioBareLineCitation matches a prose line-number citation with no file
// attached, e.g. `(line 940)` or `(lines 984-1005)`.
var scenarioBareLineCitation = regexp.MustCompile(`\blines? \d+`)

// scenarioSourcePathCitation matches a path to a file the contract corpus does
// cite by line number: compiled/typed source, which has no stable headings to
// anchor to. A markdown extension is deliberately excluded.
var scenarioSourcePathCitation = regexp.MustCompile(`[A-Za-z0-9._/-]+\.(go|ts|tsx|js|jsx|sh|json|yaml|yml)\b`)

// TestScenarioCardsNeverCiteADocByLineNumber keeps a card's contract anchors
// resolvable. A bare line number into docs/job-control.md is invalidated by any
// edit to that doc, silently: the citation still parses, still looks precise,
// and now points at an unrelated sentence. Kata 2mzk found eight cards anchored
// that way, and spot-checking one of them at d6824b06d — before any edit that
// night — showed the numbers had ALREADY drifted onto a code fence and a grep
// example. Section headings are the stable identifier a doc offers; a quoted
// phrase an agent can grep for is the other. Both survive an edit or fail
// loudly, which a line number never does.
//
// Source files are exempt: `agent/jobs_nested.go:342-356` is the corpus's
// established way to cite code, which has no headings to anchor to.
func TestScenarioCardsNeverCiteADocByLineNumber(t *testing.T) {
	var findings []string
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			cited := scenarioMarkdownLineCitation.FindAllString(line, -1)
			if len(scenarioBareLineCitation.FindAllString(line, -1)) > 0 &&
				!scenarioCitesSourceFile(lines, i) {
				cited = append(cited, scenarioBareLineCitation.FindAllString(line, -1)...)
			}
			if len(cited) == 0 {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(i+1)+": "+
				strings.Join(cited, " / ")+": "+strings.TrimSpace(line))
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("scenario cards must anchor a doc citation to something that "+
			"survives an edit to that doc — the `## `/`### ` heading it lives "+
			"under, or a quoted phrase an executing agent can grep for. A line "+
			"number goes stale the moment the doc is touched and points at an "+
			"unrelated sentence without ever failing (kata 2mzk):\n%s",
			strings.Join(findings, "\n"))
	}
}

// scenarioCitesSourceFile reports whether the citation at lines[i] names a
// source file, on its own line or the one above it — prose wraps, so
// "`llm/providers/anthropic/request.go`'s\ndowngrade guard (lines 174-183)"
// puts the path and its line range on separate lines.
func scenarioCitesSourceFile(lines []string, i int) bool {
	if scenarioSourcePathCitation.MatchString(lines[i]) {
		return true
	}
	return i > 0 && scenarioSourcePathCitation.MatchString(lines[i-1])
}
