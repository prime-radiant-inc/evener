package serf_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The needle audit: a card may only assert on a literal string that production
// can actually emit.
//
// The sibling audits check the ADDRESS of a contract — 2mzk that no card cites a
// doc by line number, gmy6 that a quoted doc anchor still appears in the doc it
// names, ypwb that a Go path resolves and a #Symbol is declared. None of them
// looks at the NEEDLE: the literal token a card tells the runner to search the
// observed output for. Rename a tag or a JSON key in production and every card
// asserting on it goes silently vacuous — the assertion still parses, still
// reads as the card's strongest guard, and can no longer fail. The citation
// audits stay green throughout, because the citation is fine; it is the needle
// that died (kata 8g3j).
//
// What this audit is, precisely: existence, not reachability. It answers one
// question — does this literal appear anywhere in production source — and a
// token that exists somewhere passes even if the subsystem the card is about
// never emits it. That floor is deliberate and it is worth stating, because
// 8g3j's own motivating example sits just outside it: recursion-coordinator-
// fanout.md asserted on `<job-notification>` in a delegate rail, and
// `<job-notification>` is a real tag — it belongs to the shell-job rail
// (agent/session_lifecycle.go), so no existence check can see the mistake.
// Deciding which rail may emit which tag needs a model of the rails, which is a
// scenario-card DSL, which is not this.

// scenarioNeedleSourceExtensions are the compiled surfaces production emits
// from. A needle that survives here is a string some code path can put on the
// wire; one that does not is a string no runner will ever observe.
var scenarioNeedleSourceExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
}

// scenarioNeedleBundledRoot is the embedded agent instruction set (see the
// //go:embed in internal/bundled/bundled.go). It ships inside the binary and is
// handed to models verbatim, so a Finding category or a signature prefix that a
// runbook prescribes is as much production vocabulary as a Go string constant —
// the doctor agent emits `watch_runaway` because
// skills/doctoring-serf/references/finding-contract.md tells it to, and no Go
// file names it at all.
const scenarioNeedleBundledRoot = "internal/bundled"

// scenarioNeedleSkipDirs are trees that are not production. test/ holds the
// card corpus itself plus the fake LLM and fake-429 doubles; docs/ is prose, and
// counting it would let a card assert on a contract nothing implements — which
// is exactly the failure the audit exists to find.
var scenarioNeedleSkipDirs = map[string]bool{
	"node_modules": true, "dist": true, "coverage": true,
	"test": true, "docs": true, "scripts": true, "tools": true,
}

// scenarioNeedleAllowed maps a needle the corpus asserts on, and which is
// genuinely absent from production source, to the one-line reason it may stay.
// Three reasons recur, and each is a case where absence is the point:
//
//   - the token belongs to a tool serf does not ship (the shared Chrome MCP
//     server, a pip package). Serf cannot rename it, so it cannot rot;
//   - the card asserts the token's ABSENCE — "there is no `self_loop` field",
//     "a model that hallucinates a `create_file` tool". Requoting these from
//     production is impossible by construction;
//   - the card invents the token itself, as a probe label or a shell variable
//     it will go on to grep its own output for.
//
// A fourth kind is here under protest and is called out per entry: a canonical
// code that docs/job-control.md defines and no Go file emits. The card is
// citing the contract correctly; whether the contract describes something
// unimplemented is a separate question from whether the card can run, and it is
// not this audit's to settle.
var scenarioNeedleAllowed = map[string]string{
	"use_browser":      "superpowers-chrome MCP tool, not serf's surface",
	"set_profile":      "superpowers-chrome MCP tool, not serf's surface",
	"new_tab":          "superpowers-chrome MCP tool, not serf's surface",
	"switch_tab":       "superpowers-chrome MCP tool, not serf's surface",
	"list_tabs":        "superpowers-chrome MCP tool, not serf's surface",
	"await_element":    "superpowers-chrome MCP tool, not serf's surface",
	"file_upload":      "Chrome DevTools Protocol command, not serf's surface",
	"tabs_context_mcp": "claude-in-chrome extension tool, not serf's surface",
	"tomli_w":          "the pip package a card's TOML-editing one-liner imports",

	"self_loop":       "doctor-forensics.md asserts the report carries NO such field",
	"watch_self_loop": "doctor-forensics.md asserts there is no such Finding category",
	"create_file":     "dev-hello-script.md names it as a tool a model might hallucinate",
	"auto_ssh":        "auth-device-autodetect.md asserts no such value exists",

	"wedge_probe":   "job-watch-caller-send-no-deadlock.md's own name for the probe call it makes",
	"queue_cap":     "the label workspace-title-bar-actions.md's own python one-liner prints",
	"seen_awaiting": "the shell variable attention-needs-you-end-to-end.md polls into",

	"delegate_session_busy": "canonical code at docs/job-control.md \"Status and reason model\"; " +
		"job-delegate-result-schema.md quotes it precisely to say nothing in the Go source emits it here",
	"stop_unconfirmed": "canonical stop reason in docs/job-control.md's reason table, quoted as " +
		"contract by two cards; no Go emitter exists — the gap is in the contract, not the cards",
}

// TestScenarioAssertedNeedlesExistInProductionSource is the audit.
func TestScenarioAssertedNeedlesExistInProductionSource(t *testing.T) {
	source := scenarioNeedleProductionSource(t)
	var findings []string
	needles := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, found := range scenarioAssertedNeedles(string(raw)) {
			needles++
			if strings.Contains(source, found.needle) {
				continue
			}
			if _, ok := scenarioNeedleAllowed[found.needle]; ok {
				continue
			}
			findings = append(findings, path+":"+strconv.Itoa(found.line)+": "+found.needle)
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. Every card names structural tokens, so zero means the extractor
	// broke, not that the assertions left.
	if needles == 0 {
		t.Fatalf("the structural-token extractor matched nothing across the " +
			"corpus — the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card may only assert on a literal string production "+
			"can emit. These %d needles appear in no production source, so the "+
			"assertion naming them can never fail and the card reads as evidence "+
			"it is not (kata 8g3j). Requote from the code that emits it, or add a "+
			"scenarioNeedleAllowed entry saying why the absence is deliberate:\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// scenarioNeedleFinding is one extracted token and the card line it sits on.
type scenarioNeedleFinding struct {
	needle string
	line   int
}

// scenarioNeedleFence matches a fenced code block. Fences hold commands the
// runner RUNS, not tokens it searches observed output for, and their shell
// variables (`owned_sid`, `run_suffix`) look exactly like JSON keys. Blanking
// them removed 35 false positives and cost no real finding.
var scenarioNeedleFence = regexp.MustCompile("(?sm)^```.*?^```")

// scenarioNeedleCodeSpan matches an inline `code span`, the corpus's marker for
// "this is a literal, not prose".
var scenarioNeedleCodeSpan = regexp.MustCompile("`([^`\n]+)`")

// scenarioNeedleToken matches a snake_case identifier: a JSON key, a tool name,
// a status, a reason code. These are the tokens a refactor renames.
var scenarioNeedleToken = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$`)

// scenarioNeedleLabel matches the field name a card may put in front of a value
// it is asserting, as in `category: watch_runaway`.
var scenarioNeedleLabel = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// scenarioNeedlePath matches a file path. A card cites source constantly, and
// `agent/tree_counter.go` would otherwise yield the needle `tree_counter`.
// ypwb's audit owns whether a cited path resolves; this one must not double as
// a broken version of it.
var scenarioNeedlePath = regexp.MustCompile(`[A-Za-z0-9._/-]+\.(go|ts|tsx|js|jsx|sh|md|json|jsonl|yaml|yml|py|txt)\b`)

// scenarioNeedleTag matches an XML-ish frame tag in one of the two shapes that
// prove the card means a real tag: the closing form `</delegate-notification>`,
// or an opening form carrying an attribute assignment,
// `<job-notification job_id="...">`. Requiring a hyphen keeps ordinary HTML
// (`<span class=`, `<button type=`) out, and requiring the `=` keeps
// metavariable placeholders out — `<worker-1 job_id>` is the card naming its
// own worker, not a frame production emits.
var scenarioNeedleTag = regexp.MustCompile(`</([a-z][a-z0-9]*(?:-[a-z0-9]+)+)>|<([a-z][a-z0-9]*(?:-[a-z0-9]+)+)\s+[a-z_][a-z0-9_]*=`)

// scenarioAssertedNeedles extracts the structural tokens a card asserts on.
//
// The hard half is telling a needle from the many strings a card legitimately
// quotes: model prompts, human-readable prose, shell fragments, invented ids.
// gmy6 found the answer there was a syntactic marker rather than a heuristic,
// and this is the syntactic marker available without annotating 134 cards — a
// code span holding ONE token, optionally labelled. `watch_runaway` and
// `category: watch_runaway` are needles; `echo wedge_probe_1` and
// `set_profile <worktree-name>` are commands, and a span with two bare words in
// it is never read as a needle.
func scenarioAssertedNeedles(text string) []scenarioNeedleFinding {
	body := scenarioNeedleFence.ReplaceAllStringFunc(text, scenarioNeedleBlankLine)
	var out []scenarioNeedleFinding
	seen := map[string]bool{}
	add := func(needle string, offset int) {
		if seen[needle] {
			return
		}
		seen[needle] = true
		out = append(out, scenarioNeedleFinding{needle: needle, line: strings.Count(body[:offset], "\n") + 1})
	}
	for _, m := range scenarioNeedleCodeSpan.FindAllStringSubmatchIndex(body, -1) {
		for _, needle := range scenarioNeedlesInCodeSpan(body[m[2]:m[3]]) {
			add(needle, m[0])
		}
	}
	for _, m := range scenarioNeedleTag.FindAllStringSubmatchIndex(body, -1) {
		for _, group := range [][2]int{{m[2], m[3]}, {m[4], m[5]}} {
			if group[0] >= 0 {
				add(body[group[0]:group[1]], m[0])
			}
		}
	}
	return out
}

// scenarioNeedlesInCodeSpan returns the structural tokens a single code span
// asserts on, and nothing for a span that is a command or a sentence.
func scenarioNeedlesInCodeSpan(span string) []string {
	span = strings.TrimSpace(scenarioNeedlePath.ReplaceAllString(span, " "))
	if scenarioNeedleToken.MatchString(span) {
		return []string{span}
	}
	for _, separator := range []string{":", "="} {
		head, tail, found := strings.Cut(span, separator)
		if !found {
			continue
		}
		head, tail = strings.TrimSpace(head), strings.TrimSpace(tail)
		var needles []string
		// `structured_result_reason: schema_validation_failed` — the key is the
		// needle, whatever the value is, as long as the value is one word.
		if scenarioNeedleToken.MatchString(head) && !strings.ContainsAny(tail, " \t") {
			needles = append(needles, head)
		}
		// `category: watch_runaway` — the label is prose, the value is the needle.
		if scenarioNeedleLabel.MatchString(head) && scenarioNeedleToken.MatchString(tail) {
			needles = append(needles, tail)
		}
		if len(needles) > 0 {
			return needles
		}
	}
	return nil
}

// scenarioNeedleBlankLine replaces every byte of a match except its newlines,
// so blanking a fence does not shift the line numbers reported after it.
func scenarioNeedleBlankLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		return ' '
	}, s)
}

// scenarioNeedleProductionSource concatenates every production source file into
// one haystack. Reading it once and asking strings.Contains 644 times is the
// whole implementation; a needle is a literal, so nothing more is needed.
func scenarioNeedleProductionSource(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	files := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// A dot directory is .git, .claude, or a sibling worktree holding a
			// full copy of this tree; descending into one is both wrong and slow.
			if path != "." && (strings.HasPrefix(entry.Name(), ".") || scenarioNeedleSkipDirs[entry.Name()]) {
				return fs.SkipDir
			}
			return nil
		}
		if !scenarioNeedleProductionFile(path, entry.Name()) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		b.Write(raw)
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("walking the source tree: %v", err)
	}
	// The file set is the half of a corpus audit that can die with every needle
	// intact: rename the roots and the haystack empties, and the audit passes
	// forever having read nothing.
	if files < 100 {
		t.Fatalf("production source haystack holds only %d files — the walk is "+
			"reading the wrong tree and every needle would report as absent", files)
	}
	return b.String()
}

func scenarioNeedleProductionFile(path, name string) bool {
	if strings.HasPrefix(filepath.ToSlash(path), scenarioNeedleBundledRoot+"/") {
		return true
	}
	if !scenarioNeedleSourceExtensions[filepath.Ext(name)] {
		return false
	}
	// Test files are not production: a needle that lives only in a _test.go
	// fixture is a string the test author typed, not one the wire carries.
	return !strings.HasSuffix(name, "_test.go") &&
		!strings.Contains(name, ".test.") && !strings.Contains(name, ".spec.")
}

// TestScenarioNeedleExtractorReadsSpansNotProse pins the syntactic rule the
// audit turns on. Every case here is a shape the corpus actually contains, and
// every negative case is one that cost a false positive before the rule was
// tightened.
func TestScenarioNeedleExtractorReadsSpansNotProse(t *testing.T) {
	for _, tc := range []struct {
		name string
		card string
		want []string
	}{
		{"lone token in a span", "assert `structured_result_valid` is true", []string{"structured_result_valid"}},
		{"labelled value", "- `category: watch_runaway`, `severity: high`;", []string{"watch_runaway"}},
		{"key with a one-word value", "expect `queue_cap=True` mid-turn", []string{"queue_cap"}},
		{"closing frame tag", "never `</job-notification>`", []string{"job-notification"}},
		{"opening frame tag with an attribute", `emits <delegate-notification delegate_id="dlg_1">`, []string{"delegate-notification"}},
		{"shell command in a span", "run `echo wedge_probe_1`, then stop", nil},
		{"tool call with an argument", "call `set_profile <worktree-name>` first", nil},
		{"prose sentence in a span", "`the delegate result is durable first`", nil},
		{"source path", "built at `agent/tree_counter.go#defaultMax`", nil},
		{"metavariable placeholder", "report `COORD <worker-1 job_id>` verbatim", nil},
		{"plain html", `the DOM shows <span class="badge">`, nil},
		{"fenced block is not asserted output", "```sh\nowned_sid=$(cat id)\necho `run_suffix`\n```\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, found := range scenarioAssertedNeedles(tc.card) {
				got = append(got, found.needle)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("scenarioAssertedNeedles(%q) = %v, want %v", tc.card, got, tc.want)
			}
		})
	}
}

// TestScenarioNeedleExtractorReportsTheLineTheNeedleIsOn keeps the finding
// actionable, and keeps fence-blanking from shifting the numbers after it.
func TestScenarioNeedleExtractorReportsTheLineTheNeedleIsOn(t *testing.T) {
	card := "# title\n\n```sh\nfoo_bar=1\n```\n\nassert `structured_result_reason: schema_validation_failed`\n"
	found := scenarioAssertedNeedles(card)
	if len(found) != 2 {
		t.Fatalf("scenarioAssertedNeedles = %v, want the key and the reason code", found)
	}
	for _, want := range []scenarioNeedleFinding{
		{needle: "structured_result_reason", line: 7},
		{needle: "schema_validation_failed", line: 7},
	} {
		if !slices.Contains(found, want) {
			t.Fatalf("scenarioAssertedNeedles = %v, want %v among them", found, want)
		}
	}
}

// TestScenarioNeedleAllowlistEntriesStillMatch keeps the carve-outs honest. An
// entry whose needle has left the corpus, or which production has since started
// emitting, silently widens the exemption to nothing and nobody notices until a
// real vacuous assertion slips through the same hole.
func TestScenarioNeedleAllowlistEntriesStillMatch(t *testing.T) {
	source := scenarioNeedleProductionSource(t)
	inCorpus := map[string]bool{}
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, found := range scenarioAssertedNeedles(string(raw)) {
			inCorpus[found.needle] = true
		}
	}
	var stale []string
	for needle, reason := range scenarioNeedleAllowed {
		if reason == "" {
			stale = append(stale, needle+" (no reason recorded)")
		}
		if !inCorpus[needle] {
			stale = append(stale, needle+" (no card asserts on it any more)")
		}
		if strings.Contains(source, needle) {
			stale = append(stale, needle+" (production emits it now; the exemption is dead weight)")
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("scenarioNeedleAllowed has %d entry/entries that no longer "+
			"exempt anything. Drop each one:\n%s", len(stale), strings.Join(stale, "\n"))
	}
}
