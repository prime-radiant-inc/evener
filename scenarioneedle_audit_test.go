package serf_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
// READ THIS BEFORE TRUSTING A GREEN RUN. The claim this audit establishes is
// narrow, and sitting green beside ten other scenario audits it will be read as
// broader than it is. Stated exactly: NO CARD NAMES A snake_case TOKEN, IN A
// CODE SPAN THIS EXTRACTOR RECOGNISES, THAT APPEARS NOWHERE IN SERF'S SOURCE.
// That is not "the corpus is checked". Four gaps, all deliberate, all measured:
//
//  1. Existence, not reachability. A token that exists ANYWHERE passes, even if
//     the subsystem the card is about never emits it. 8g3j's own motivating
//     example sits outside this: recursion-coordinator-fanout.md asserted on
//     `<job-notification>` in a delegate rail, and `<job-notification>` is a
//     real tag — it belongs to the shell-job rail (agent/session_lifecycle.go),
//     so no existence check can see the mistake. Deciding which rail may emit
//     which tag needs a model of the rails, which is a card DSL, which is not
//     this.
//
//  2. It has never produced an actionable finding. Every card repair on the
//     branch that introduced it was found by hand; reverting those six cards to
//     their pre-repair content and re-running this audit still PASSES. Raw
//     absences before/after that work: 29 and 28, and all 29 are allowlisted
//     tokens. The audit is a floor against future rot, not a tool that found
//     the rot already here.
//
//  3. Tag coverage is 6 of the corpus's 23 frame-tag mentions. scenarioNeedleTag
//     requires `</tag>` or `<tag attr=`, which is a reasoned choice — it keeps
//     the metavariables the corpus writes in the same shape out
//     (`<worktree-name>`, `<project-id>`, `<session-id>`) — but it means the
//     bare opening form is invisible. A fabricated `<bogus-frame>` passes;
//     `</bogus-frame>` fails.
//
//  4. 26 of the 135 files read yield ZERO needles, so they are entirely
//     unchecked —
//     including hooks-claude-compat-matcher.md, whose whole subject is
//     PreToolUse / "Bash" / SerfToClaude, and state-stuck-processing-display.md,
//     whose headline assertion is `state: "idle"`. The shapes the extractor does
//     not read, all of which the corpus uses: quoted values inside a span, JSON
//     fragments, a token with a trailing period, two tokens in one span,
//     camelCase, dotted paths, kebab-case attributes, bold-not-code-span, and
//     anything uppercase. Widening any of them means widening the allowlist;
//     that trade was not taken.

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

// scenarioNeedleSkipRoots are trees that are not production, named by their
// path from the repo root. test/ holds the card corpus itself plus the fake LLM
// and fake-429 doubles; docs/ is prose, and counting it would let a card assert
// on a contract nothing implements — which is exactly the failure the audit
// exists to find.
//
// These are ROOT-RELATIVE on purpose. Matching them by base name at any depth
// silently blanked cmd/serf-hub/frontend/src/panes/session/transcript/tools —
// 20 files and ~3.7k lines of the transcript renderers for jobs, delegates and
// subagents, which is precisely the subsystem this kata is about. Two live wire
// tokens live only there (`not_delivered` in jobTools.tsx's delegate-send status
// set, `auto_started` in taskCard.tsx's row keys), so a card quoting them
// CORRECTLY failed the audit, and the failure message then steered the author
// toward a false allowlist entry because requoting was impossible.
var scenarioNeedleSkipRoots = []string{
	"test", "docs", "scripts", "tools",
}

// scenarioNeedleSkipDirNames are build and dependency output, which may appear
// at any depth and is never source anyone edits.
var scenarioNeedleSkipDirNames = map[string]bool{
	"node_modules": true, "dist": true, "coverage": true,
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
//
// Entries are keyed by CARD AND needle, not by needle alone. Every reason below
// names a specific card, and a corpus-wide exemption would not honour that: with
// a needle-only key, appending `structured_result_reason: create_file` to an
// unrelated card passed both tests. The tokens that scoping protects hardest are
// exactly the ones that sound real and are not — `self_loop`, `watch_self_loop`,
// `create_file`, `auto_ssh` are allowlisted BECAUSE a card asserts they do not
// exist, which is the sentence a future author is most likely to copy.
var scenarioNeedleAllowed = map[scenarioNeedleException]string{
	{"docs/agentic-testing.md", "use_browser"}: "superpowers-chrome MCP tool, not serf's surface",
	{"docs/agentic-testing.md", "set_profile"}: "superpowers-chrome MCP tool, not serf's surface",
	{"docs/agentic-testing.md", "new_tab"}:     "superpowers-chrome MCP tool, not serf's surface",
	{"docs/agentic-testing.md", "switch_tab"}:  "superpowers-chrome MCP tool, not serf's surface",
	{cardAskCrossSessionNotify, "use_browser"}: "superpowers-chrome MCP tool, not serf's surface",
	{cardAskTwoClients, "use_browser"}:         "superpowers-chrome MCP tool, not serf's surface",
	{cardAskTwoClients, "new_tab"}:             "superpowers-chrome MCP tool, not serf's surface",
	{cardAskTwoClients, "switch_tab"}:          "superpowers-chrome MCP tool, not serf's surface",
	{cardAskTwoClients, "list_tabs"}:           "superpowers-chrome MCP tool, not serf's surface",
	{cardAskTwoClients, "await_element"}:       "superpowers-chrome MCP tool, not serf's surface",
	{cardAskWebAnswer, "use_browser"}:          "superpowers-chrome MCP tool, not serf's surface",
	{cardAttentionNeedsYou, "use_browser"}:     "superpowers-chrome MCP tool, not serf's surface",
	{cardSpawnKeyboard, "use_browser"}:         "superpowers-chrome MCP tool, not serf's surface",
	{cardWebDragDrop, "use_browser"}:           "superpowers-chrome MCP tool, not serf's surface",
	{cardWebDragDrop, "set_profile"}:           "superpowers-chrome MCP tool, not serf's surface",
	{cardWebFilePicker, "use_browser"}:         "superpowers-chrome MCP tool, not serf's surface",
	{cardFontSizePresets, "set_profile"}:       "superpowers-chrome MCP tool, not serf's surface",
	{cardWebModelSwitch, "new_tab"}:            "superpowers-chrome MCP tool, not serf's surface",
	{cardWebFilePicker, "file_upload"}:         "a use_browser action driving CDP Input.setFileInputFiles, not serf's surface",
	{cardIndex, "file_upload"}:                 "a use_browser action driving CDP Input.setFileInputFiles, not serf's surface",
	{cardCostEstimate, "tabs_context_mcp"}:     "claude-in-chrome extension tool, not serf's surface",
	{cardTurnMetaBadge, "tabs_context_mcp"}:    "claude-in-chrome extension tool, not serf's surface",
	{cardCredentialsPage, "tomli_w"}:           "the pip package this card's TOML-editing one-liner imports",

	{cardDoctorForensics, "self_loop"}:       "this card asserts the report carries NO such field",
	{cardDoctorForensics, "watch_self_loop"}: "this card asserts there is no such Finding category",
	{cardDevHelloScript, "create_file"}:      "this card names it as a tool a model might hallucinate",
	{cardAuthDeviceAutodetect, "auto_ssh"}:   "this card asserts no such value exists",

	{cardJobWatchCallerSend, "wedge_probe"}:  "this card's own name for the probe call it makes",
	{cardWorkspaceTitleBar, "queue_cap"}:     "the label this card's own python one-liner prints",
	{cardAttentionNeedsYou, "seen_awaiting"}: "the shell variable this card polls into",
	{cardAuthDevicePollConcurrent, "elapsed_ms"}: "the shell variable this card computes at :108 and asserts at :126; " +
		"it is not serf vocabulary and only ever resolved by riding inside group_elapsed_ms",
}

// scenarioNeedleException is one card's exemption for one needle.
type scenarioNeedleException struct {
	card   string
	needle string
}

// Card paths used by the allowlist, spelled once so a rename fails to compile
// rather than silently widening an exemption to nothing.
const (
	cardIndex                    = "test/scenarios/INDEX.md"
	cardAskCrossSessionNotify    = "test/scenarios/ask-cross-session-notify.md"
	cardAskTwoClients            = "test/scenarios/ask-two-clients.md"
	cardAskWebAnswer             = "test/scenarios/ask-web-answer.md"
	cardAttentionNeedsYou        = "test/scenarios/attention-needs-you-end-to-end.md"
	cardAuthDeviceAutodetect     = "test/scenarios/auth-device-autodetect.md"
	cardAuthDevicePollConcurrent = "test/scenarios/auth-device-poll-concurrent.md"
	cardCostEstimate             = "test/scenarios/cost-estimate-display-and-gating.md"
	cardCredentialsPage          = "test/scenarios/credentials-page-displays-sources.md"
	cardDevHelloScript           = "test/scenarios/dev-hello-script.md"
	cardDoctorForensics          = "test/scenarios/doctor-forensics.md"
	cardFontSizePresets          = "test/scenarios/font-size-presets-visible.md"
	cardJobDelegateResultSchema  = "test/scenarios/job-delegate-result-schema.md"
	cardJobStopAndChildren       = "test/scenarios/job-stop-and-children.md"
	cardJobWatchCallerSend       = "test/scenarios/job-watch-caller-send-no-deadlock.md"
	cardSpawnKeyboard            = "test/scenarios/spawn-keyboard-contract.md"
	cardTurnMetaBadge            = "test/scenarios/turn-meta-badge-always-visible.md"
	cardWebDragDrop              = "test/scenarios/web-drag-drop-image.md"
	cardWebFilePicker            = "test/scenarios/web-file-picker-image.md"
	cardWebModelSwitch           = "test/scenarios/web-model-switch-mid-session.md"
	cardWorkspaceTitleBar        = "test/scenarios/workspace-title-bar-actions.md"
)

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
			if scenarioNeedleInSource(source, found.needle) {
				continue
			}
			key := scenarioNeedleException{card: filepath.ToSlash(path), needle: found.needle}
			if _, ok := scenarioNeedleAllowed[key]; ok {
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

// Fences hold commands the runner RUNS, not tokens it searches observed output
// for, and their shell variables (`owned_sid`, `run_suffix`) look exactly like
// JSON keys — so blanking them keeps a card's own scratch variables from
// reading as production vocabulary.
//
// scenarioNeedleFence matches a fenced code block, INCLUDING one indented
// inside a list item — which is how this corpus writes 760 of its 972 fence
// lines. An anchor that only accepts a column-0 fence left 78% of fenced
// content being scanned as prose, and was measurably inert: extracting with it
// and without it both yielded 685 code-span needles. Indent-aware, the pass
// removes 2. That is a small effect honestly stated, not the large one an
// earlier version of this comment claimed.
//
// The closing fence is matched as a newline plus optional indent rather than a
// second ^-anchored run: the opening fence's line always ends in a newline, so
// the two are equivalent, and gocritic reads a second ^ inside one pattern as a
// dangling anchor.
var scenarioNeedleFence = regexp.MustCompile("(?sm)^[ \t]*```.*?\n[ \t]*```")

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

// scenarioNeedleInSource reports whether the haystack contains needle as a whole
// identifier. A plain substring test is not enough: `elapsed_ms` — a shell
// variable auth-device-poll-concurrent.md computes for itself, and exactly the
// kind of card-local label the allowlist exists to name — passed for a year by
// riding inside `group_elapsed_ms` (agent/events/payloads.go). That made the
// allowlist look like a complete census of the corpus's deliberate absences when
// it was one short.
func scenarioNeedleInSource(haystack, needle string) bool {
	for offset := 0; ; {
		i := strings.Index(haystack[offset:], needle)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(needle)
		if !scenarioNeedleIdentifierByte(haystack, start-1) && !scenarioNeedleIdentifierByte(haystack, end) {
			return true
		}
		offset = start + 1
	}
}

// scenarioNeedleIdentifierByte reports whether the byte at i would extend an
// identifier. Out-of-range counts as a boundary.
func scenarioNeedleIdentifierByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// scenarioNeedleProductionSource concatenates every production source file into
// one haystack. Reading it once and scanning it per needle is the whole
// implementation; a needle is a literal, so nothing more is needed.
//
// The file set comes from `git ls-files`, not a filesystem walk (kata 6zst). A
// walk reads whatever the checkout happens to hold on disk right now — build
// output `make build` left behind (the gate builds before it tests), a
// scratch file, anything else local — so the same commit gave a different
// verdict in a checkout that had run the gate than in a clean worktree of it.
// git ls-files names the same tracked paths in every checkout of a commit; a
// locally modified tracked file's on-disk content still counts (that is
// correct — it is what the checkout would actually ship), but nothing
// untracked can enter the haystack at all.
func scenarioNeedleProductionSource(t *testing.T) string {
	t.Helper()
	paths, err := scenarioNeedleTrackedFiles(t)
	if err != nil {
		t.Fatalf("listing tracked source files: %v", err)
	}
	var b strings.Builder
	files := 0
	for _, path := range paths {
		if scenarioNeedleSkippedPath(path) || !scenarioNeedleProductionFile(path, filepath.Base(path)) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// Tracked in the index but absent from the working tree (deleted,
				// not yet staged) — nothing on disk to add to the haystack.
				continue
			}
			t.Fatalf("reading %s: %v", path, err)
		}
		files++
		b.Write(raw)
		b.WriteByte('\n')
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

// scenarioNeedleTrackedFiles lists every git-tracked path that could hold a
// needle: the compiled-surface extensions, plus everything under
// scenarioNeedleBundledRoot (scenarioNeedleProductionFile and
// scenarioNeedleSkippedPath decide which of these actually count).
func scenarioNeedleTrackedFiles(t *testing.T) ([]string, error) {
	t.Helper()
	cmd := exec.Command("git", "-C", ".", "ls-files", "-z", "--",
		"*.go", "*.ts", "*.tsx", "*.js", "*.jsx", scenarioNeedleBundledRoot)
	raw, err := cmd.Output()
	if err != nil {
		// git says WHY on stderr ("not a git repository", a bad pathspec), and
		// Output() files that under ExitError rather than in the error text —
		// so without this the whole diagnosis is "exit status 128".
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("git ls-files: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var paths []string
	for path := range strings.SplitSeq(string(raw), "\x00") {
		if path == "" {
			continue
		}
		paths = append(paths, filepath.ToSlash(path))
	}
	return paths, nil
}

// scenarioNeedleSkippedPath reports whether path sits under a directory the
// haystack excludes: a dot directory (.git, .claude, a sibling worktree — see
// scenarioNeedleTrackedFiles's comment), a build/dependency directory named in
// scenarioNeedleSkipDirNames, or one of scenarioNeedleSkipRoots (matched
// root-relative only, per that var's comment). git ls-files should never
// return a path under any of these — they are gitignored or untracked — so
// this is a defense-in-depth check, not the mechanism that makes the audit
// hermetic; that is scenarioNeedleTrackedFiles reading the index instead of
// the disk.
func scenarioNeedleSkippedPath(path string) bool {
	dirs := strings.Split(path, "/")
	dirs = dirs[:len(dirs)-1] // the file's own name is never skip-checked
	for i, dir := range dirs {
		if strings.HasPrefix(dir, ".") ||
			scenarioNeedleSkipDirNames[dir] ||
			(i == 0 && slices.Contains(scenarioNeedleSkipRoots, dir)) {
			return true
		}
	}
	return false
}

// ---- kata xmag: docs/job-control.md's reason-code table vs Go emitters ----
//
// 8g3j's needle audit above checks that a CARD never asserts on a token
// production cannot emit. It does not check the inverse claim docs/job-control.md
// itself makes: that its "Status and reason model" table is a complete,
// accurate account of what the shell/delegate job surface actually returns.
// Kata xmag found three codes in that table with no Go emitter anywhere in
// the tree (`stop_unconfirmed`, `supervision_lost`, `delegate_session_busy`)
// and, running the same check on the table's own codes rather than a card's
// quotes of them, several more: `awaiting_permission` and `permission_denied`
// have never had a Go emitter either, `startup_failed` is a typo for the
// `start_failed` the code actually emits, and `finalize_failed`,
// `forward_failed`, `missing_terminal`, `terminal_error`, and a `cancelled`
// reason under `stopped` are real, currently-undocumented wire vocabulary.
// See kata xmag's report for the per-code provenance (git log -S evidence:
// never built vs. deliberately removed) each ruling rests on.

// jobControlReasonDoc is the normative contract this audit and 8g3j's needle
// audit both check cards and code against.
const jobControlReasonDoc = "docs/job-control.md"

// jobControlReasonTableHeader anchors the pipe-table in "Status and reason
// model" whose "Normative reasons" column is this audit's forward-direction
// source.
var jobControlReasonTableHeader = regexp.MustCompile(`(?m)^\| Value \| Surface \| Meaning \| Normative reasons \| Owner attention \|\n`)

// jobControlReasonCanonicalCodesSentence anchors the "Canonical codes
// include" prose sentence in the same section, which lists synchronous
// tool-error codes outside the table proper. delegate_session_busy lived
// here, not in the table -- kata xmag's fix removes it from this sentence.
var jobControlReasonCanonicalCodesSentence = regexp.MustCompile(`(?s)Canonical codes include.*?\.\n`)

// jobControlReasonCanonicalUnaudited names codes in the "Canonical codes
// include" sentence that kata xmag deliberately does NOT rule on, alongside
// why. Unlike the table's "Normative reasons" (which claims completeness per
// status), this sentence hedges with "include" -- a weaker claim -- and three
// of its codes turned out to need more than a grep to settle:
// `permission_required` has no Go emitter, but a synchronous
// permission-denial path may simply not exist yet, which is a design
// question, not a naming slip; `target_not_messageable` and
// `target_not_resumable` have no bare-string emitter either, but
// `NotResumableReason`/`Resumable` typed fields carry the same concept
// through session_tools_jobs.go, `job-delegate-result-schema.md`, and
// agent/internal/delegatestore -- possibly the string convention was
// superseded by a structured field the way `delegate_session_busy` was
// superseded by live-steering, which this kata's report flags as a
// follow-up rather than rules on here. Widening this audit to those codes
// without that investigation would risk a wrong ruling on a normative doc.
var jobControlReasonCanonicalUnaudited = map[string]bool{
	"permission_required":    true,
	"target_not_messageable": true,
	"target_not_resumable":   true,
}

// jobControlReasonEmitterFiles are the files that construct the terminal
// Status+Reason a model-facing job or delegate result carries: the shell and
// delegate job managers, the tool handlers that translate their outcomes,
// and the two durable-store fold/reconcile paths that classify a restart.
// This is the reverse-direction half of the audit's source scope. It
// deliberately excludes job_watch.go (a separate, already-documented
// end_reason vocabulary at "recent_watches" -- `cleared`, `replaced`,
// `budget_exhausted`, `auto_removed_terminal`, `job_manager_closed`) and the
// sprawling delegate_tree_*.go lease-retry machinery, whose
// errDelegateTargetBusy sentinel ("target_busy") is internal control flow
// compared/joined with errors.Is and never formatted into a model-facing
// Reason string. Scoping to a whole tree scan here would flag dozens of
// unrelated fields that also happen to be named Reason (LLM finish reasons,
// session/turn lifecycle, hook payloads).
var jobControlReasonEmitterFiles = []string{
	"agent/job_shell.go",
	"agent/job_delegate.go",
	"agent/jobs.go",
	"agent/session_tools_jobs.go",
	"agent/delegate_tree_finish.go",
	"agent/internal/jobstore/reconcile.go",
	"agent/internal/delegatestore/fold.go",
}

// jobControlReasonAssignment matches a literal string assigned to a field or
// local named (case-insensitively) "reason" -- `Reason: "x"`, `Reason =
// "x"`, `reason = "x"`. The \b before "eason" keeps "endReason" and
// "stopReason" -- different fields carrying different vocabularies -- from
// matching.
var jobControlReasonAssignment = regexp.MustCompile(`(?i)\breason\s*[:=]\s*"([a-z][a-z0-9_]*)"`)

// TestJobControlReasonTableCodesHaveGoEmitters is kata xmag's forward
// direction: every code the "Status and reason model" table's Normative
// reasons column names, and every code the adjoining "Canonical codes
// include" sentence names, must be a string some Go production code path can
// actually put on the wire. A card can be exempted from the needle audit
// with a documented reason; the contract itself cannot -- this test has no
// allowlist, on purpose, because docs/job-control.md is supposed to already
// be true.
func TestJobControlReasonTableCodesHaveGoEmitters(t *testing.T) {
	raw, err := os.ReadFile(jobControlReasonDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", jobControlReasonDoc, err)
	}
	text := string(raw)
	loc := jobControlReasonTableHeader.FindStringIndex(text)
	if loc == nil {
		t.Fatalf("%s: the reason-table header has moved or been reworded; "+
			"update jobControlReasonTableHeader to match", jobControlReasonDoc)
	}
	rest := text[loc[1]:]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		t.Fatalf("%s: could not find the blank line ending the reason table", jobControlReasonDoc)
	}
	tableBody := rest[:end]

	canon := jobControlReasonCanonicalCodesSentence.FindString(text)
	if canon == "" {
		t.Fatalf("%s: the \"Canonical codes include\" sentence has moved or "+
			"been reworded; update jobControlReasonCanonicalCodesSentence to match", jobControlReasonDoc)
	}

	source := jobControlReasonGoSource(t)
	seen := map[string]bool{}
	var findings []string
	check := func(section string, skipUnaudited bool) {
		for _, found := range scenarioAssertedNeedles(section) {
			if seen[found.needle] {
				continue
			}
			seen[found.needle] = true
			if skipUnaudited && jobControlReasonCanonicalUnaudited[found.needle] {
				continue
			}
			if !scenarioNeedleInSource(source, found.needle) {
				findings = append(findings, found.needle)
			}
		}
	}
	check(tableBody, false)
	check(canon, true)
	if len(seen) == 0 {
		t.Fatalf("no reason codes extracted from %s's table or canonical-codes "+
			"sentence -- the extractor is reading the wrong text", jobControlReasonDoc)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("%s names %d reason code(s) with no Go emitter anywhere in "+
			"production source (kata xmag). Either implement the emitter or "+
			"remove the code from the table/sentence:\n%s",
			jobControlReasonDoc, len(findings), strings.Join(findings, "\n"))
	}
	for code := range jobControlReasonCanonicalUnaudited {
		if !seen[code] {
			t.Errorf("jobControlReasonCanonicalUnaudited names %q, but it no "+
				"longer appears in the canonical-codes sentence -- the exemption "+
				"is stale, remove it", code)
		}
	}
}

// TestJobControlReasonCodesEmittedAreDocumented is kata xmag's reverse
// direction: every literal Reason string the model-facing job/delegate
// surface actually constructs (see jobControlReasonEmitterFiles) must appear
// somewhere in docs/job-control.md -- not necessarily the table itself, since
// e.g. synchronous routing codes are documented in their own tool sections.
func TestJobControlReasonCodesEmittedAreDocumented(t *testing.T) {
	raw, err := os.ReadFile(jobControlReasonDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", jobControlReasonDoc, err)
	}
	doc := string(raw)

	codes := 0
	var findings []string
	for _, path := range jobControlReasonEmitterFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, m := range jobControlReasonAssignment.FindAllStringSubmatch(string(src), -1) {
			code := m[1]
			codes++
			if !scenarioNeedleInSource(doc, code) {
				findings = append(findings, path+": "+code)
			}
		}
	}
	if codes == 0 {
		t.Fatalf("no Reason string literals matched across jobControlReasonEmitterFiles " +
			"-- the pattern or file list is stale")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("these Reason literals are constructed by the job/delegate "+
			"surface but appear nowhere in %s (kata xmag). Document them, or "+
			"if they are genuinely internal-only, narrow "+
			"jobControlReasonAssignment/jobControlReasonEmitterFiles to exclude them:\n%s",
			jobControlReasonDoc, strings.Join(findings, "\n"))
	}
}

// jobControlReasonGoSource concatenates every production Go source file into
// one haystack, exactly like scenarioNeedleProductionSource's walk but
// narrowed to compiled Go: kata xmag's acceptance is specifically "a Go
// emitter", and widening the haystack to TS/JS or the embedded bundled
// prose (as the general needle audit does for its own, different reason)
// would let a code pass by virtue of appearing in a skill runbook's prose
// rather than because any code path can put it on the wire -- which is
// exactly how the general needle audit above already misses
// `supervision_lost`'s absent Go emitter (it lives in
// internal/bundled/skills/doctoring-serf/references/failure-modes.md).
func jobControlReasonGoSource(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	files := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "." && (strings.HasPrefix(entry.Name(), ".") ||
				scenarioNeedleSkipDirNames[entry.Name()] ||
				slices.Contains(scenarioNeedleSkipRoots, filepath.ToSlash(path))) {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
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
	if files < 100 {
		t.Fatalf("Go production source haystack holds only %d files -- the walk "+
			"is reading the wrong tree and every code would report as absent", files)
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

// TestScenarioNeedleHaystackReachesTheTranscriptRenderers pins the skip-root
// fix. Matching skip directories by BASE NAME blanked
// cmd/serf-hub/frontend/src/panes/session/transcript/tools — the renderers for
// job, delegate and subagent tool calls — because its last path element is
// "tools". `not_delivered` is the witness: it is a delegate-send status in that
// tree's jobTools.tsx and appears in no other production file, so a card quoting
// it correctly was told to requote it from code the audit had hidden.
//
// taskCard.tsx's `auto_started` is deliberately NOT a witness here. It only ever
// appears as `auto_started_${id}`, so the whole-identifier rule rejects the bare
// prefix — correctly: the bare token is not a string production emits.
func TestScenarioNeedleHaystackReachesTheTranscriptRenderers(t *testing.T) {
	source := scenarioNeedleProductionSource(t)
	if !scenarioNeedleInSource(source, "not_delivered") {
		t.Error("not_delivered is not in the production haystack: the skip list is blanking " +
			"cmd/serf-hub/frontend/src/panes/session/transcript/tools again, and a " +
			"card quoting it correctly would be reported as asserting on nothing")
	}
	// The roots that SHOULD be skipped still are, or the audit would count the
	// card corpus and the docs as production and never fail at all.
	for _, needle := range []string{"COORDINATOR_SPAWNED", "NEST_TOKEN_1"} {
		if scenarioNeedleInSource(source, needle) {
			t.Errorf("%q reached the haystack — test/ is no longer skipped, so the "+
				"corpus is checking itself", needle)
		}
	}
}

// TestScenarioNeedleMatchesWholeIdentifiersOnly pins the boundary fix. The
// corpus's `elapsed_ms` is a card's own shell variable and resolved for real
// only by riding inside `group_elapsed_ms`.
func TestScenarioNeedleMatchesWholeIdentifiersOnly(t *testing.T) {
	haystack := "GroupElapsedMS int64 `json:\"group_elapsed_ms\"`\nconst ok = \"delegate_id\"\n"
	if scenarioNeedleInSource(haystack, "elapsed_ms") {
		t.Fatal("elapsed_ms matched inside group_elapsed_ms: a substring test lets a " +
			"card-local label pass as production vocabulary")
	}
	if !scenarioNeedleInSource(haystack, "group_elapsed_ms") {
		t.Fatal("the whole identifier did not match itself")
	}
	if !scenarioNeedleInSource(haystack, "delegate_id") {
		t.Fatal("a quoted identifier did not match; punctuation must count as a boundary")
	}
}

// TestScenarioNeedleExemptionsAreScopedToOneCard pins the allowlist-key fix.
// Every reason string names a specific card; a needle-only key let any card
// inherit any other card's exemption, and the tokens most exposed by that are
// the ones allowlisted BECAUSE a card asserts they do not exist.
func TestScenarioNeedleExemptionsAreScopedToOneCard(t *testing.T) {
	owner := scenarioNeedleException{card: cardDoctorForensics, needle: "self_loop"}
	if _, ok := scenarioNeedleAllowed[owner]; !ok {
		t.Fatalf("%v is no longer the allowlisted pair this test is written against", owner)
	}
	borrower := scenarioNeedleException{card: cardJobStopAndChildren, needle: "self_loop"}
	if _, ok := scenarioNeedleAllowed[borrower]; ok {
		t.Fatalf("%v inherited another card's exemption: the allowlist is keyed too widely", borrower)
	}
}

// TestScenarioNeedleAllowlistEntriesStillMatch keeps the carve-outs honest. An
// entry whose needle has left the corpus, or which production has since started
// emitting, silently widens the exemption to nothing and nobody notices until a
// real vacuous assertion slips through the same hole.
func TestScenarioNeedleAllowlistEntriesStillMatch(t *testing.T) {
	source := scenarioNeedleProductionSource(t)
	inCorpus := map[scenarioNeedleException]bool{}
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, found := range scenarioAssertedNeedles(string(raw)) {
			inCorpus[scenarioNeedleException{card: filepath.ToSlash(path), needle: found.needle}] = true
		}
	}
	var stale []string
	for key, reason := range scenarioNeedleAllowed {
		where := key.card + " " + key.needle
		if reason == "" {
			stale = append(stale, where+" (no reason recorded)")
		}
		if !inCorpus[key] {
			stale = append(stale, where+" (that card no longer asserts on it)")
		}
		if scenarioNeedleInSource(source, key.needle) {
			stale = append(stale, where+" (production emits it now; the exemption is dead weight)")
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("scenarioNeedleAllowed has %d entry/entries that no longer "+
			"exempt anything. Drop each one:\n%s", len(stale), strings.Join(stale, "\n"))
	}
}
