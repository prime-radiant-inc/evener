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

// scenarioBacktickedDocPath matches the head of a doc-anchor citation: a
// markdown path in backticks. Backticks are the discriminator — a bare
// `README.md` inside a shell fence is a file the card creates at runtime, not
// a contract it is citing.
var scenarioBacktickedDocPath = regexp.MustCompile("`([A-Za-z0-9._/-]+\\.md)`")

// scenarioAnchorRunSeparators are the bytes a card may put between the doc
// path and its quoted anchors, and between one anchor and the next. Anything
// else ends the run, which is what keeps ordinary prose quotes out of it.
const scenarioAnchorRunSeparators = " \t\n,;:()[]"

// scenarioAnchorRunConnector is the one word the corpus writes inside an
// anchor run — `docs/job-control.md` "A" ("B") and "C". Every other connector
// tried against the corpus bought nothing, so the list stays at one.
const scenarioAnchorRunConnector = "and"

// TestScenarioDocAnchorsAppearInTheDocTheyName keeps the replacements kata 2mzk
// made honest. TestScenarioCardsNeverCiteADocByLineNumber proves no card cites
// a doc by line number any more; it does NOT prove the section names and quoted
// phrases that replaced those numbers point at anything. A card can quote
// "Nested jobs" "the forwarded copy is a drive signal for the parent" long
// after that heading was renamed or that sentence reworded, and the
// line-number audit stays green (kata gmy6).
//
// The hard part is telling an ANCHOR quote from an ordinary one — the same
// cards quote tool arguments, shell fragments, scare quotes, and observed
// output, all legitimately absent from any contract. This audit takes the
// syntactic answer and needs no allowlist for it: an anchor is a quoted span
// in the RUN that immediately follows a backticked doc path, which is exactly
// the `docs/x.md` "Section" "phrase" shape 2mzk wrote. A quote that is merely
// somewhere in the same paragraph is prose and is not checked.
//
// Matching collapses whitespace and drops backticks on both sides: card prose
// wraps mid-quote, so "delivers a bounded notification/frame back to that\n
// watcher" has a newline the doc does not.
func TestScenarioDocAnchorsAppearInTheDocTheyName(t *testing.T) {
	docs := map[string]string{}
	var findings []string
	anchors := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		text := string(raw)
		for _, m := range scenarioBacktickedDocPath.FindAllStringSubmatchIndex(text, -1) {
			cited := text[m[2]:m[3]]
			quoted, issue := scenarioQuotedAnchorRun(text, m[1])
			where := path + ":" + strconv.Itoa(strings.Count(text[:m[0]], "\n")+1)
			if issue != "" || (len(quoted) == 0 && scenarioCitationRequiresAnchor(text, m[0], m[1])) {
				if issue == "" {
					issue = "has no quoted anchor in its paragraph"
				}
				findings = append(findings, where+": `"+cited+"` "+issue)
				continue
			}
			if len(quoted) == 0 {
				continue
			}
			body, seen := docs[cited]
			if !seen {
				docRaw, err := os.ReadFile(cited)
				if err == nil {
					body = scenarioCollapseForAnchor(string(docRaw))
				}
				docs[cited] = body
			}
			if body == "" {
				findings = append(findings, where+": cites `"+cited+"`, which does not exist")
				continue
			}
			for _, q := range quoted {
				anchors++
				if strings.Contains(body, scenarioCollapseForAnchor(q)) {
					continue
				}
				findings = append(findings, where+": `"+cited+"` no longer contains "+strconv.Quote(q))
			}
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. Every card that names a contract anchors it this way today, so
	// zero anchors means the run parser broke, not that the anchors left.
	if anchors == 0 {
		t.Fatalf("the doc-anchor needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card's contract anchor must still be findable in "+
			"the doc it names — a renamed heading or a reworded sentence leaves "+
			"the citation parsing fine and pointing at nothing, which is the "+
			"exact failure kata 2mzk's line-number audit cannot see (kata "+
			"gmy6). Requote from the doc, or move the anchor to the section "+
			"that now carries the rule:\n%s", strings.Join(findings, "\n"))
	}
}

func TestScenarioDocAnchorParsingFailsClosedAtParagraphBoundaries(t *testing.T) {
	text := "Contract anchors: `docs/first.md`\n\n\"only belongs to the next paragraph\""
	start := strings.Index(text, "`docs/first.md`") + len("`docs/first.md`")
	quoted, issue := scenarioQuotedAnchorRun(text, start)
	if len(quoted) != 0 {
		t.Fatalf("anchor run crossed a paragraph boundary: %v", quoted)
	}
	if issue != "" {
		t.Fatalf("paragraph boundary was reported as a malformed quote: %s", issue)
	}
	if !scenarioCitationRequiresAnchor(text, start-len("`docs/first.md`"), start) {
		t.Fatal("explicit Contract anchors label was not recognized")
	}
	whitespaceParagraph := "Contract anchors: `docs/first.md`\r\n \t\r\n\"only belongs to the next paragraph\""
	whitespaceStart := strings.Index(whitespaceParagraph, "`docs/first.md`") + len("`docs/first.md`")
	if !scenarioCitationRequiresAnchor(whitespaceParagraph, whitespaceStart-len("`docs/first.md`"), whitespaceStart) {
		t.Fatal("explicit Contract anchors label was not recognized across a whitespace-only CRLF paragraph boundary")
	}

	malformed := "Contract anchor: `docs/broken.md` \"unterminated"
	brokenStart := strings.Index(malformed, "`docs/broken.md`") + len("`docs/broken.md`")
	_, issue = scenarioQuotedAnchorRun(malformed, brokenStart)
	if issue == "" {
		t.Fatal("unterminated quoted anchor was silently ignored")
	}
	plain := "See `docs/ordinary-reference.md` for setup details."
	plainStart := strings.Index(plain, "`docs/ordinary-reference.md`")
	if scenarioCitationRequiresAnchor(plain, plainStart, plainStart+len("`docs/ordinary-reference.md`")) {
		t.Fatal("ordinary document reference was treated as a required citation")
	}
	section := "`docs/sectioned.md` §\"Stable heading\""
	sectionStart := strings.Index(section, "`docs/sectioned.md`") + len("`docs/sectioned.md`")
	quoted, issue = scenarioQuotedAnchorRun(section, sectionStart)
	if issue != "" || len(quoted) != 1 || quoted[0] != "Stable heading" {
		t.Fatalf("section-sign citation = %v, %q; want one anchor: %v", quoted, issue, []string{"Stable heading"})
	}
}

// scenarioQuotedAnchorRun returns the double-quoted spans that immediately
// follow a doc citation ending at offset i — the anchor run. It stops at the
// first byte that is neither a separator, the one connector word, nor the
// opening quote of another span, so prose after the citation is never read as
// an anchor. The second return value is non-empty only for malformed quoted
// syntax; a plain path reference is intentionally not an error.
func scenarioQuotedAnchorRun(text string, i int) ([]string, string) {
	var run []string
	for {
		j, paragraphBoundary := scenarioSkipAnchorRunGap(text, i)
		if paragraphBoundary {
			return run, ""
		}
		if j >= len(text) || text[j] != '"' {
			return run, ""
		}
		end := strings.IndexByte(text[j+1:], '"')
		if end < 0 {
			return run, "unterminated quoted anchor"
		}
		run = append(run, text[j+1:j+1+end])
		i = j + end + 2
	}
}

// scenarioSkipAnchorRunGap advances past the separators and connector words
// that may sit between two members of an anchor run. It reports a blank line
// before consuming the next paragraph, because a later paragraph is prose,
// not another anchor for the preceding path.
func scenarioSkipAnchorRunGap(text string, i int) (int, bool) {
	lineBreaks := 0
	for i < len(text) {
		if text[i] == '\n' {
			lineBreaks++
			i++
			if lineBreaks >= 2 {
				return i, true
			}
			continue
		}
		if strings.IndexByte(scenarioAnchorRunSeparators, text[i]) >= 0 {
			i++
			continue
		}
		if strings.HasPrefix(text[i:], "§") {
			i += len("§")
			continue
		}
		next := i + len(scenarioAnchorRunConnector)
		if strings.HasPrefix(text[i:], scenarioAnchorRunConnector) && next < len(text) &&
			strings.IndexByte(scenarioAnchorRunSeparators, text[next]) >= 0 {
			i = next
			lineBreaks = 0
			continue
		}
		return i, false
	}
	return i, false
}

func scenarioCitationRequiresAnchor(text string, start, end int) bool {
	paragraphStart, paragraphEnd := scenarioParagraphBounds(text, start, end)
	paragraph := strings.ToLower(text[paragraphStart:paragraphEnd])
	return strings.Contains(paragraph, "contract anchor")
}

var scenarioParagraphBoundary = regexp.MustCompile(`\r?\n[ \t]*\r?\n`)

func scenarioParagraphBounds(text string, start, end int) (int, int) {
	paragraphStart, paragraphEnd := 0, len(text)
	for _, boundary := range scenarioParagraphBoundary.FindAllStringIndex(text, -1) {
		if boundary[0] < start {
			paragraphStart = boundary[1]
		}
		if boundary[0] >= end {
			paragraphEnd = boundary[0]
			break
		}
	}
	return paragraphStart, paragraphEnd
}

// scenarioCollapseForAnchor normalizes a card quote and a doc body to the same
// shape: runs of whitespace become one space and backticks disappear. Card
// prose wraps mid-quote and both sides mark code spans, so neither newlines nor
// backticks can be part of the comparison.
func scenarioCollapseForAnchor(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
}
