package evener_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioGoCitation matches a card pointing at a place inside a Go file: a
// backticked path carrying an anchor, either the `#symbol` this convention
// prefers or the `:line` hint it replaces.
//
// The anchor is what makes it a citation. A backticked path on its own is a
// mention — "relocated from the deleted `security.go`", "the package was
// extracted out of `cmd/evener-tui/pending.go`" — and naming a file that is gone
// is the whole point of those sentences, so they are not checked.
var scenarioGoCitation = regexp.MustCompile("`([A-Za-z0-9._/-]+\\.go)(?:#([A-Za-z0-9_.]+)|:([0-9][0-9,-]*))`")

// scenarioSourceCitation is scenarioGoCitation widened to every compiled
// surface the corpus cites — the frontend's `.ts`/`.tsx` as well as Go.
//
// The Go-only spelling was not a decision, it was the language kata ypwb
// happened to be looking at, and the corpus cites the frontend nearly as
// heavily: 915 anchored source citations, 418 of them non-Go, none of which any
// audit read until this one. Kata yj52 found the hole from the other side,
// asking why a `.tsx` citation survived a green gate; the answer is that no
// needle in the suite matched a `.tsx` path at all.
var scenarioSourceCitation = regexp.MustCompile(
	"`([A-Za-z0-9._/-]+(?:" + scenarioSourceExtensionAlternation() + "))(?:#[A-Za-z0-9_.]+|:[0-9][0-9,-]*)`")

// scenarioSourceCitationGroups is the length FindStringSubmatch returns for a
// scenarioSourceCitation match: the whole span plus its one captured path.
const scenarioSourceCitationGroups = 2

// TestScenarioSourceCitationsResolve keeps a card's pointer into source code
// attached to a file that still exists. Kata 2mzk's audit deliberately exempts
// source paths from the no-line-numbers rule, because code has no headings to
// anchor to; that exemption said nothing about whether the paths were right.
// Kata ypwb sampled four and found three stale, and a whole-file rename or
// package move breaks every citation into it at once while every existing
// audit stays green.
//
// Cards abbreviate a path down to the part that reads well
// (`internal/hubcore/tree.go` for `cmd/evener-hub/internal/hubcore/tree.go`), so
// resolution is by path SUFFIX against the tree. That still catches the failure
// that matters — the file moved out from under the citation, or was renamed,
// or never existed under that name.
//
// What this does NOT check is the anchor behind the path: `DocPane.tsx:133`
// and `DocPane.tsx:1` are the same claim here. Line ranges rot faster than
// paths do, and nothing in the suite reads one.
func TestScenarioSourceCitationsResolve(t *testing.T) {
	byBase := scenarioSourceFilesByBase(t)
	var findings []string
	resolved := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range scenarioSourceCitation.FindAllStringSubmatch(line, -1) {
				cited := m[1]
				if len(scenarioResolveCitedPath(byBase, cited)) == 0 {
					findings = append(findings, path+":"+strconv.Itoa(i+1)+
						": `"+cited+"` names no source file in the tree: "+strings.TrimSpace(line))
					continue
				}
				resolved++
			}
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. Cards cite source by the hundred today, so zero resolutions means
	// the citation needle broke, not that the citations left.
	if resolved == 0 {
		t.Fatalf("the source-citation needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card that points into a source file must name a file "+
			"that is still there — a package move, a rename, or a deleted "+
			"component silently turns every citation into it into a pointer at "+
			"nothing, and no other audit sees it (katas ypwb, yj52). Repoint at "+
			"the file that carries the code now, or drop the claim if the code "+
			"it described is gone:\n%s", strings.Join(findings, "\n"))
	}
}

// TestScenarioSourceSymbolsAreDeclared keeps the `#symbol` half of a Go
// citation resolvable. `agent/tree_counter.go#defaultMaxConcurrentDelegateTurns`
// survives every edit to that file — which is the entire reason kata ypwb moved
// the corpus off `:12` — but only until the symbol is renamed or moved to
// another file, and nothing else in the suite would notice that.
//
// A symbol is anything a card legitimately anchors to: a func or method, a
// type, a package-level or grouped const or var, a struct field, an interface
// method. Receiver-, type-, and package-qualified names such as `Type.Method`
// and `openai.IssuerBaseURL` are preserved; an unqualified alias remains
// available only when that name is unique in the file, so a collision cannot
// make a citation appear to resolve by accident.
func TestScenarioSourceSymbolsAreDeclared(t *testing.T) {
	byBase := scenarioGoFilesByBase(t)
	declared := map[string]map[string]bool{}
	var findings []string
	checked := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range scenarioGoCitation.FindAllStringSubmatch(line, -1) {
				cited, symbol := m[1], m[2]
				if symbol == "" {
					continue
				}
				checked++
				found := false
				for _, file := range scenarioResolveCitedPath(byBase, cited) {
					names, err := scenarioDeclarationsIn(declared, file)
					if err != nil {
						t.Fatalf("parsing %s cited by %s: %v", file, path, err)
					}
					if names[symbol] {
						found = true
						break
					}
				}
				if !found {
					findings = append(findings, path+":"+strconv.Itoa(i+1)+
						": `"+cited+"` declares no "+symbol+": "+strings.TrimSpace(line))
				}
			}
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. The corpus carries symbol anchors today, so zero checks means the
	// `#symbol` needle broke, not that the anchors left.
	if checked == 0 {
		t.Fatalf("the `#symbol` needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card's `file.go#symbol` anchor must name something "+
			"that file still declares — a rename or a move to another file "+
			"leaves the anchor parsing fine and pointing at nothing, which is "+
			"the failure the line numbers it replaced had by construction "+
			"(kata ypwb). Repoint at the file and symbol that carry the code "+
			"now:\n%s", strings.Join(findings, "\n"))
	}
}

// scenarioSourceExtensionAlternation builds the regexp alternation for the
// compiled-source extensions from the set itself, longest first, so the needle
// and the file index cannot drift apart as that set grows.
func scenarioSourceExtensionAlternation() string {
	quoted := make([]string, 0, len(scenarioNeedleSourceExtensions))
	for ext := range scenarioNeedleSourceExtensions {
		quoted = append(quoted, regexp.QuoteMeta(ext))
	}
	sort.Slice(quoted, func(i, j int) bool {
		if len(quoted[i]) != len(quoted[j]) {
			return len(quoted[i]) > len(quoted[j])
		}
		return quoted[i] < quoted[j]
	})
	return strings.Join(quoted, "|")
}

// TestScenarioSourceCitationNeedleReadsEveryCompiledExtension pins the widening
// itself. The audit above is a corpus scan, so it is green both when the corpus
// is clean and when the needle quietly stops matching a language; the resolved
// floor catches a needle that matches NOTHING, and this catches one that
// matches only Go again.
func TestScenarioSourceCitationNeedleReadsEveryCompiledExtension(t *testing.T) {
	for extension := range scenarioNeedleSourceExtensions {
		cited := "panes/session/composer/Composer" + extension
		// scenarioSourceCitation captures one group, the path, so a match is
		// [whole, path]. Any other length means the needle lost its group.
		m := scenarioSourceCitation.FindStringSubmatch("(`" + cited + ":641-643`)")
		if len(m) != scenarioSourceCitationGroups || m[1] != cited {
			t.Fatalf("the source-citation needle does not read a %s citation: %v", extension, m)
		}
		if m := scenarioSourceCitation.FindStringSubmatch("`Composer" + extension + "#handleSteerClick`"); m == nil {
			t.Fatalf("the source-citation needle does not read a %s symbol anchor", extension)
		}
	}
	// An anchorless path is a mention, not a citation — "the package was
	// extracted out of `cmd/evener-tui/pending.go`" names a file on purpose.
	for _, mention := range []string{"`Composer.tsx`", "`docs/job-control.md:940`", "`fixture.tsx.snap:12`"} {
		if m := scenarioSourceCitation.FindStringSubmatch(mention); m != nil {
			t.Fatalf("the source-citation needle read %s as a citation: %v", mention, m)
		}
	}
}

// scenarioSourceLineCitation matches the `path:lines` half of a source
// citation, capturing the anchor so it can be read rather than merely parsed.
// A `#Symbol` anchor is deliberately absent: it names a declaration, not a
// range. TestScenarioSourceSymbolsAreDeclared already keeps the Go ones honest;
// a `.tsx#symbol` anchor is checked by nothing today, which kata 26ya tracks.
var scenarioSourceLineCitation = regexp.MustCompile(
	"`([A-Za-z0-9._/-]+(?:" + scenarioSourceExtensionAlternation() + ")):([0-9][0-9,-]*)`")

// scenarioLiteralCitationSeparators are the bytes a card may put between the
// literal it quotes and the citation it gives for it. The corpus writes the
// pair as `LITERAL` (`path:lines`) or `LITERAL`,\n  `path:lines`; any other
// byte between the two is prose, and prose means that citation is not that
// literal's.
const scenarioLiteralCitationSeparators = " \t\r\n(,;"

// scenarioApproximationMarkers are the corpus's vocabulary for "this quote is
// not verbatim": an ellipsis for elided code, angle brackets for a
// metavariable, braces for a JSX or Go expression being paraphrased. Nothing
// can be asserted about where a sketch lives.
var scenarioApproximationMarkers = []string{"…", "...", "<", ">", "{", "}"}

// scenarioCodeSpanIndexes is the length FindAllStringSubmatchIndex returns per
// scenarioNeedleCodeSpan match: start/end of the whole span, then of its one
// captured body. scenarioQuotedLineCitations reads all four.
const scenarioCodeSpanIndexes = 4

// scenarioQuotedLineCitation is one literal a card quotes and the `path:lines`
// it cites for it.
type scenarioQuotedLineCitation struct {
	literal string
	cited   string
	anchor  string
	line    int
}

// scenarioQuotedLineCitations extracts every `LITERAL` (`path:lines`) pair in a
// card.
//
// The pair is recognised syntactically rather than by heuristic: a citation code
// span whose immediately preceding code span is separated from it by nothing but
// scenarioLiteralCitationSeparators. That neighbouring-span rule is what keeps
// ordinary prose out — "the request shape is `TurnSteerParams`, described in
// `appwire/types.go:24`" has a word between the two spans and yields no pair.
//
// A single-word literal is skipped. An identifier is what the needle audit
// reads, and by a different rule: a card names a symbol far more often than it
// quotes the line that declares it, so a symbol's citation is an address, not a
// quotation.
func scenarioQuotedLineCitations(text string) []scenarioQuotedLineCitation {
	spans := scenarioNeedleCodeSpan.FindAllStringSubmatchIndex(text, -1)
	var out []scenarioQuotedLineCitation
	next := 0
	for _, citation := range scenarioSourceLineCitation.FindAllStringSubmatchIndex(text, -1) {
		var preceding []int
		for next < len(spans) && spans[next][1] <= citation[0] {
			preceding = spans[next]
			next++
		}
		if len(preceding) < scenarioCodeSpanIndexes ||
			!scenarioOnlyCitationSeparators(text[preceding[1]:citation[0]]) {
			continue
		}
		literal := text[preceding[2]:preceding[3]]
		if !strings.Contains(literal, " ") || scenarioApproximateLiteral(literal) {
			continue
		}
		out = append(out, scenarioQuotedLineCitation{
			literal: literal,
			cited:   text[citation[2]:citation[3]],
			anchor:  text[citation[4]:citation[5]],
			line:    strings.Count(text[:preceding[0]], "\n") + 1,
		})
	}
	return out
}

func scenarioOnlyCitationSeparators(gap string) bool {
	return strings.IndexFunc(gap, func(r rune) bool {
		return !strings.ContainsRune(scenarioLiteralCitationSeparators, r)
	}) < 0
}

func scenarioApproximateLiteral(literal string) bool {
	for _, marker := range scenarioApproximationMarkers {
		if strings.Contains(literal, marker) {
			return true
		}
	}
	return false
}

// scenarioAnchorCovers reports whether a line number falls inside a citation
// anchor. `36,44` is two one-line citations rather than a nine-line span, so
// each comma-separated part is its own interval.
func scenarioAnchorCovers(anchor string, line int) bool {
	for part := range strings.SplitSeq(anchor, ",") {
		first, last, ok := strings.Cut(part, "-")
		if !ok {
			last = first
		}
		low, err := strconv.Atoi(first)
		if err != nil {
			continue
		}
		high, err := strconv.Atoi(last)
		if err != nil {
			continue
		}
		if low <= line && line <= high {
			return true
		}
	}
	return false
}

// scenarioMinCheckedLineCitations is the floor on how many quoted-literal line
// citations the audit below must actually check. A zero-floor only catches a
// needle that died outright; this catches the corpus drifting out of the
// audit's reach one reworded sentence at a time, which is gap 4 and leaves no
// other trace. The corpus checked 28 when this landed, so the floor sits there:
// raise it as the corpus grows, and lower it only with a reason.
const scenarioMinCheckedLineCitations = 28

// scenarioLineCitationExemption identifies one citation the two-witness rule
// mis-reads.
type scenarioLineCitationExemption struct {
	card    string
	cited   string
	literal string
}

// scenarioLineCitationExempt holds the citations whose quoted literal is a value
// production PRODUCES rather than an excerpt of the file cited for it, where
// that value also happens to appear verbatim elsewhere in the same file. The
// audit cannot tell the two apart by string search, and repointing at the
// coincidence would move a correct citation onto the wrong branch — the exact
// laundering scripts/scenario-cite-migrate.sh refuses to do. Each entry states
// why, and TestScenarioLineCitationExemptionsStillApply deletes any that stops
// being needed.
var scenarioLineCitationExempt = map[scenarioLineCitationExemption]string{
	{
		card:    "test/scenarios/written-image-inline-after-reload.md",
		cited:   "output_images.go",
		literal: `source: "written-file"`,
	}: "the card quotes the outputImages descriptor field, and cites the " +
		"write_file/edit_file case that sets it (addArgPath(..., \"written-file\")). " +
		"The verbatim `source: \"written-file\"` further down the file is the " +
		"apply_patch branch, which is a different tool and not what the card is about.",
}

// TestScenarioQuotedLiteralsSitInsideTheCitedLineRange reads the anchor its
// sibling above deliberately ignores. TestScenarioSourceCitationsResolve proves
// a cited FILE is still there; it says nothing about the numbers behind the
// colon, and those are what rot first — every insertion above a citation moves
// the code out from under it while the citation still parses and still reads as
// precise. Kata ypwb moved the Go corpus onto `#Symbol` anchors for exactly this
// reason, but the frontend half has no symbol spelling and stayed on line
// numbers with nothing watching them.
//
// The oracle is the card's own quotation: when a card writes `LITERAL`
// immediately followed by `path:lines`, and that file carries LITERAL verbatim,
// the cited range must contain one of the lines that carries it. Two independent
// witnesses have to agree before anything is claimed, which is the same standard
// scripts/scenario-cite-migrate.sh applies before it will rewrite an anchor;
// resolving a stale number to whatever happens to enclose it is the failure that
// script exists to avoid.
//
// READ THIS BEFORE TRUSTING A GREEN RUN. Stated exactly: NO CARD QUOTES A
// VERBATIM LITERAL AND CITES A LINE RANGE THAT DOES NOT CONTAIN IT. Measured
// when written: 56 quoted-literal line citations in the corpus, 28 of them
// checkable, and **16 of those 28 were stale** — 57% wrong, all repaired in the
// same change. Three gaps, all deliberate:
//
//  1. A literal the cited file does not carry verbatim is UNCHECKED, and that is
//     the other 28. Cards paraphrase (`active = processing ||
//     appReservedTurnID != ""`) and quote messages production composes from a
//     template (`Steer failed: no active turn`, assembled in Composer.tsx from
//     the route), and neither can be located by string search. Kata yj52 mistook
//     one of those for a wrong-FILE citation, which is the trap this audit must
//     not repeat: absence of the literal is not evidence of a wrong citation.
//
//  2. A literal that is a produced VALUE rather than an excerpt can match the
//     cited file somewhere the card does not mean. That is what
//     scenarioLineCitationExempt holds, one entry today.
//
//  3. Only the range is checked, never the claim. A card may quote a real string
//     from the right line and still describe its behaviour wrongly.
//
//  4. The adjacency rule drops a pair SILENTLY. Put a word between the quote and
//     its citation and the pair stops being extracted, with no signal — and this
//     is not hypothetical: two edits made while repairing the corpus did exactly
//     that, taking the checked count from 28 to 26 without a test noticing. That
//     is why scenarioMinCheckedLineCitations exists below; it turns a mass drop
//     into a failure, but it cannot see one or two. A card rewording a sentence
//     around a citation can still quietly remove it from this audit's reach.
//     Gap 1 drops a pair just as silently from the other side, and with the card
//     untouched: edit the production string a card quotes and the literal stops
//     appearing verbatim in the cited file, so the pair leaves through the
//     `continue` below and the checked count falls.
func TestScenarioQuotedLiteralsSitInsideTheCitedLineRange(t *testing.T) {
	byBase := scenarioSourceFilesByBase(t)
	sources := map[string][]string{}
	var findings []string
	checked := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, citation := range scenarioQuotedLineCitations(string(raw)) {
			// A path that resolves to nothing is TestScenarioSourceCitationsResolve's
			// finding, not this one; reporting it twice buries the difference.
			candidates := scenarioResolveCitedPath(byBase, citation.cited)
			if len(candidates) == 0 {
				continue
			}
			var carriers []string
			covered := false
			for _, candidate := range candidates {
				lines, err := scenarioSourceLinesOf(sources, candidate)
				if err != nil {
					t.Fatalf("reading %s cited by %s: %v", candidate, path, err)
				}
				for i, line := range lines {
					if !strings.Contains(line, citation.literal) {
						continue
					}
					carriers = append(carriers, candidate+":"+strconv.Itoa(i+1))
					if scenarioAnchorCovers(citation.anchor, i+1) {
						covered = true
					}
				}
			}
			if len(carriers) == 0 {
				continue // composed or paraphrased: gap 1 above
			}
			checked++
			if covered {
				continue
			}
			key := scenarioLineCitationExemption{
				card:    filepath.ToSlash(path),
				cited:   citation.cited,
				literal: citation.literal,
			}
			if _, exempt := scenarioLineCitationExempt[key]; exempt {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(citation.line)+
				": `"+citation.cited+":"+citation.anchor+"` does not contain "+
				strconv.Quote(citation.literal)+"; it is at "+strings.Join(carriers, ", "))
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. Cards quote code they cite by the dozen, so zero checks means the
	// pair extractor broke, not that the quotations left.
	if checked < scenarioMinCheckedLineCitations {
		t.Fatalf("this audit checked %d quoted-literal line citations, below the "+
			"floor of %d. Three causes, all silent by construction, which is the "+
			"only reason this floor exists: the pair extractor broke; cards were "+
			"reworded so that prose now sits between a quote and its citation "+
			"(gap 4 above); or — the likeliest — a PRODUCTION STRING a card quotes "+
			"was edited, so the literal no longer appears verbatim in the file the "+
			"card cites and the pair leaves as unchecked (gap 1 above) with the "+
			"card never touched. For that last one the repair is the card: requote "+
			"the string production carries now, or repoint at the line that carries "+
			"the old one. Otherwise restore the adjacency, or lower the floor "+
			"deliberately and say why here.", checked, scenarioMinCheckedLineCitations)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card that quotes a literal and cites a line range for "+
			"it must cite the range that holds it. Every edit above a citation "+
			"moves the code out from under it silently: the path still resolves, "+
			"the anchor still parses, and it now points at an unrelated statement "+
			"(katas ypwb, yj52). Repoint at the line carrying the string the card "+
			"itself quotes — the quotation and the line agreeing is what makes the "+
			"repair safe (scripts/scenario-cite-migrate.sh's two-witness "+
			"rule):\n%s", strings.Join(findings, "\n"))
	}
}

// TestScenarioLineCitationExemptionsStillApply keeps the exemption list from
// outliving what it excuses. An entry whose card, citation or quotation has
// moved on is a blanket over a citation nobody is looking at any more, which is
// the failure mode an allowlist has by construction (kata 8g3j's needle
// allowlist carries the same guard).
func TestScenarioLineCitationExemptionsStillApply(t *testing.T) {
	byBase := scenarioSourceFilesByBase(t)
	sources := map[string][]string{}
	for exemption, reason := range scenarioLineCitationExempt {
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("exemption %+v carries no reason", exemption)
		}
		raw, err := os.ReadFile(exemption.card)
		if err != nil {
			t.Fatalf("exempted card %s: %v", exemption.card, err)
		}
		matched := false
		for _, citation := range scenarioQuotedLineCitations(string(raw)) {
			if citation.cited != exemption.cited || citation.literal != exemption.literal {
				continue
			}
			matched = true
			// The exemption must still be load-bearing: if the cited range now
			// holds the literal, the audit would pass on its own and the entry
			// is dead weight.
			for _, candidate := range scenarioResolveCitedPath(byBase, citation.cited) {
				lines, err := scenarioSourceLinesOf(sources, candidate)
				if err != nil {
					t.Fatalf("reading %s: %v", candidate, err)
				}
				for i, line := range lines {
					if strings.Contains(line, citation.literal) && scenarioAnchorCovers(citation.anchor, i+1) {
						t.Fatalf("exemption %+v is no longer needed: %s:%d is inside the cited range — delete the entry", exemption, candidate, i+1)
					}
				}
			}
		}
		if !matched {
			t.Fatalf("exemption %+v matches no citation in %s — the card was repaired or reworded, so delete the entry", exemption, exemption.card)
		}
	}
}

// scenarioSourceLinesOf returns a source file split into lines, memoized across
// citations into the same file.
func scenarioSourceLinesOf(cache map[string][]string, path string) ([]string, error) {
	if lines, ok := cache[path]; ok {
		return lines, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	cache[path] = lines
	return lines, nil
}

// TestScenarioQuotedLineCitationExtractorReadsNeighbouringSpans pins the
// syntactic rule the audit turns on. Every positive is a shape the corpus
// contains; every negative is one that cost a false positive while the rule was
// measured against it.
func TestScenarioQuotedLineCitationExtractorReadsNeighbouringSpans(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []scenarioQuotedLineCitation
	}{{
		name: "the wrapped parenthesised pair the corpus writes",
		text: "the row reads `Remove from queue`\n  (`QueueStrip.tsx:350`); nothing else changes\n",
		want: []scenarioQuotedLineCitation{{literal: "Remove from queue", cited: "QueueStrip.tsx", anchor: "350", line: 1}},
	}, {
		name: "a comma-separated pair carrying a multi-line range",
		text: "the toast says `Steer failed: now`,\n  `panes/session/composer/Composer.tsx:947-949`\n",
		want: []scenarioQuotedLineCitation{{literal: "Steer failed: now", cited: "panes/session/composer/Composer.tsx", anchor: "947-949", line: 1}},
	}, {
		name: "prose between the literal and the citation is not a citation of it",
		text: "the shape is `some observed message` as described in `appwire/types.go:24`\n",
	}, {
		name: "a single-word span is a symbol, which the resolve audit owns",
		text: "emits `steer_failed` (`commands.ts:515`)\n",
	}, {
		name: "an elided or metavariable quote claims nothing verbatim",
		text: "renders `{expanded && …}` (`ToolCallItem.tsx:106`) and `Remove <children>` (`widgets/chip/index.tsx:71`)\n",
	}, {
		name: "a symbol anchor carries no range to check",
		text: "the cap is `the default concurrency` (`agent/tree_counter.go#defaultMaxConcurrentDelegateTurns`)\n",
	}, {
		name: "a markdown citation belongs to the doc-anchor audit",
		text: "the rule is `never poll a job` (`docs/job-control.md:940`)\n",
	}, {
		name: "the reported line is the literal's own, not the file's first",
		text: "intro\n\nmore prose\n\nthe row reads `Queued messages one`\n  (`composer/queue/QueueStrip.tsx:302`)\n",
		want: []scenarioQuotedLineCitation{{literal: "Queued messages one", cited: "composer/queue/QueueStrip.tsx", anchor: "302", line: 5}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := scenarioQuotedLineCitations(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("scenarioQuotedLineCitations = %+v, want %+v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Fatalf("citation %d = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

// TestScenarioAnchorCoversReadsCommaListsAsSeparateCitations pins the one place
// the anchor grammar could silently widen: `36,44` must not accept line 40.
func TestScenarioAnchorCoversReadsCommaListsAsSeparateCitations(t *testing.T) {
	for _, tc := range []struct {
		anchor string
		line   int
		want   bool
	}{
		{"350", 350, true}, {"350", 349, false}, {"350", 351, false},
		{"947-949", 947, true}, {"947-949", 949, true}, {"947-949", 946, false}, {"947-949", 950, false},
		{"36,44", 36, true}, {"36,44", 44, true}, {"36,44", 40, false},
		{"1044-1047,1055", 1055, true}, {"1044-1047,1055", 1050, false},
	} {
		if got := scenarioAnchorCovers(tc.anchor, tc.line); got != tc.want {
			t.Fatalf("scenarioAnchorCovers(%q, %d) = %v, want %v", tc.anchor, tc.line, got, tc.want)
		}
	}
}

// scenarioSourceFilesByBase indexes the tracked compiled-source files in the
// worktree by base name.
func scenarioSourceFilesByBase(t *testing.T) map[string][]string {
	t.Helper()
	byBase, err := scenarioTrackedFilesByBase(".", scenarioNeedleSourceExtensions)
	if err != nil {
		t.Fatalf("listing tracked source files: %v", err)
	}
	return byBase
}

func TestScenarioDeviceFlowCitationCitesPackageDefault(t *testing.T) {
	const (
		cardPath = "test/scenarios/cli-device-code-flow.md"
		cited    = "auth/openai/config.go"
		symbol   = "openai.IssuerBaseURL"
	)
	raw, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatalf("reading %s: %v", cardPath, err)
	}
	for _, m := range scenarioGoCitation.FindAllStringSubmatch(string(raw), -1) {
		if m[1] != cited || m[2] != symbol {
			continue
		}
		names, err := scenarioDeclarationsIn(map[string]map[string]bool{}, cited)
		if err != nil {
			t.Fatalf("parsing %s: %v", cited, err)
		}
		if !names[symbol] {
			t.Fatalf("%s package default %q does not resolve", cited, symbol)
		}
		return
	}
	t.Fatalf("%s must cite the package default `%s#%s`", cardPath, cited, symbol)
}

func TestScenarioDeclarationsPreserveReceiverQualification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "methods.go")
	const source = `package fixture

const IssuerBaseURL = "https://auth.example.test"

type Alpha struct {
	Field int
}
type Beta struct {
	Field int
}
type Config struct {
	IssuerBaseURL string
}
type Contract interface {
	Do()
}

func (Alpha) Run() {}
func (*Beta) Run() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	names, err := scenarioDeclarationsIn(map[string]map[string]bool{}, path)
	if err != nil {
		t.Fatalf("scenarioDeclarationsIn: %v", err)
	}
	for _, want := range []string{"fixture.IssuerBaseURL", "Alpha.Run", "Beta.Run", "Alpha.Field", "Config.IssuerBaseURL", "Contract.Do"} {
		if !names[want] {
			t.Fatalf("declarations missing qualified method %q: %v", want, names)
		}
	}
	for _, ambiguous := range []string{"IssuerBaseURL", "Run", "Field"} {
		if names[ambiguous] {
			t.Fatalf("ambiguous unqualified declaration %q was accepted: %v", ambiguous, names)
		}
	}
	if !names["Do"] {
		t.Fatalf("unique unqualified interface method was rejected: %v", names)
	}
}

func TestScenarioTrackedFilesByBaseIgnoresUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"tracked.go", "deleted.go", "untracked.go", "excluded.tsx"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "tracked.go", "deleted.go", "excluded.tsx"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatalf("remove deleted fixture: %v", err)
	}

	byBase, err := scenarioTrackedFilesByBase(root, map[string]bool{".go": true})
	if err != nil {
		t.Fatalf("scenarioTrackedFilesByBase: %v", err)
	}
	if got := byBase["tracked.go"]; len(got) != 1 || got[0] != "tracked.go" {
		t.Fatalf("tracked Go files = %v, want tracked.go only", got)
	}
	if _, ok := byBase["untracked.go"]; ok {
		t.Fatalf("untracked Go file appeared in tracked index: %v", byBase["untracked.go"])
	}
	if _, ok := byBase["deleted.go"]; ok {
		t.Fatalf("deleted Go file appeared in checkout index: %v", byBase["deleted.go"])
	}
	if _, ok := byBase["excluded.tsx"]; ok {
		t.Fatalf("file outside the requested extensions appeared in the index: %v", byBase["excluded.tsx"])
	}
	withTSX, err := scenarioTrackedFilesByBase(root, map[string]bool{".go": true, ".tsx": true})
	if err != nil {
		t.Fatalf("scenarioTrackedFilesByBase with .tsx: %v", err)
	}
	if got := withTSX["excluded.tsx"]; len(got) != 1 || got[0] != "excluded.tsx" {
		t.Fatalf("requested .tsx files = %v, want excluded.tsx", got)
	}
}

// scenarioDeclarationsIn returns every name a Go file declares — funcs and
// methods, types, package-level and grouped consts and vars, struct fields,
// interface methods — memoized across citations into the same file.
func scenarioDeclarationsIn(cache map[string]map[string]bool, path string) (map[string]bool, error) {
	if names, ok := cache[path]; ok {
		return names, nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	unqualifiedCounts := map[string]int{}
	for _, decl := range parsed.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			unqualifiedCounts[decl.Name.Name]++
			if receiver := scenarioReceiverName(decl.Recv); receiver != "" {
				names[receiver+"."+decl.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					unqualifiedCounts[spec.Name.Name]++
					scenarioAddMemberNames(names, unqualifiedCounts, spec.Name.Name, spec.Type)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						unqualifiedCounts[name.Name]++
						names[parsed.Name.Name+"."+name.Name] = true
					}
				}
			}
		}
	}
	for name, count := range unqualifiedCounts {
		if count == 1 {
			names[name] = true
		}
	}
	cache[path] = names
	return names, nil
}

// scenarioAddMemberNames records a struct type's field names and an interface
// type's method names; cards anchor to both. It records the qualified name
// immediately and defers the unqualified alias until its file-wide count is
// known, so same-named members cannot mask one another.
func scenarioAddMemberNames(names map[string]bool, unqualifiedCounts map[string]int, typeName string, typ ast.Expr) {
	var fields *ast.FieldList
	switch typ := typ.(type) {
	case *ast.StructType:
		fields = typ.Fields
	case *ast.InterfaceType:
		fields = typ.Methods
	default:
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			unqualifiedCounts[name.Name]++
			names[typeName+"."+name.Name] = true
		}
	}
}

// scenarioReceiverName returns the declared receiver type name, including the
// base type inside pointer and generic receiver expressions.
func scenarioReceiverName(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	var receiverName func(ast.Expr) string
	receiverName = func(expr ast.Expr) string {
		switch expr := expr.(type) {
		case *ast.Ident:
			return expr.Name
		case *ast.StarExpr:
			return receiverName(expr.X)
		case *ast.IndexExpr:
			return receiverName(expr.X)
		case *ast.IndexListExpr:
			return receiverName(expr.X)
		case *ast.ParenExpr:
			return receiverName(expr.X)
		default:
			return ""
		}
	}
	return receiverName(fields.List[0].Type)
}

// scenarioTrackedFilesByBase indexes the tracked files carrying one of the
// given extensions by base name, so a cited path suffix can be resolved without
// rewalking the tree per citation.
func scenarioTrackedFilesByBase(root string, extensions map[string]bool) (map[string][]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	byBase := map[string][]string{}
	for path := range strings.SplitSeq(string(raw), "\x00") {
		if path == "" {
			continue
		}
		path = filepath.ToSlash(path)
		if !extensions[filepath.Ext(path)] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat tracked file %s: %w", path, err)
		}
		byBase[filepath.Base(path)] = append(byBase[filepath.Base(path)], path)
	}
	return byBase, nil
}

// scenarioGoFilesByBase indexes tracked Go files in the worktree by base name.
func scenarioGoFilesByBase(t *testing.T) map[string][]string {
	t.Helper()
	byBase, err := scenarioTrackedFilesByBase(".", map[string]bool{".go": true})
	if err != nil {
		t.Fatalf("listing tracked Go files: %v", err)
	}
	return byBase
}

// scenarioResolveCitedPath returns every file whose path is, or ends with, the
// cited path. Cards abbreviate, so `internal/hubcore/tree.go` must find
// `cmd/evener-hub/internal/hubcore/tree.go`; a bare `main.go` legitimately finds
// many, which is imprecise but not stale.
func scenarioResolveCitedPath(byBase map[string][]string, cited string) []string {
	var out []string
	for _, candidate := range byBase[scenarioCitedBaseName(cited)] {
		if candidate == cited || strings.HasSuffix(candidate, "/"+cited) {
			out = append(out, candidate)
		}
	}
	return out
}

// scenarioCitedBaseName is the file name at the end of a citation path.
func scenarioCitedBaseName(cited string) string {
	if i := strings.LastIndexByte(cited, '/'); i >= 0 {
		return cited[i+1:]
	}
	return cited
}
