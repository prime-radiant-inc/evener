# Tool-result expansion shows 100 lines before folding

Date: 2026-10-11
Request (Jesse): "in the web ui, when i expand evener tool results, it currently
truncates at what feels like 20 lines or so. let's flip that to 100 lines before
we do the truncation or windowing or 'show more'"

## What the reader experiences today

Every expanded tool-result body in the web transcript renders its text through
the shared `CodeBlock` widget (`cmd/evener-hub/frontend/src/widgets/codeblock/index.tsx`).
`CodeBlock` folds content longer than `TAIL_VISIBLE_LINES = 14` lines to its tail,
behind a "Show N earlier lines" control; revealing swaps in the full text plus
"Show fewer lines". That 14-line window (plus the fold banner and wrapped lines)
is the "~20 lines" perception. The fold is the mechanism the request names:
windowing (tail view), truncation (head hidden), "show more" (the reveal button).

The fold covers every tool body that renders tool output TEXT as the framed mono
block: shell, read_file (text), grep, list_dir, glob, read_transcript,
memory_read, memory_search, the job_* family's raw output dumps, the delegate
status and worktree/find_session fallback bodies, job_watch's own raw fallback,
and `RawToolOutput` — the default body for every unregistered tool, including
all MCP tools. Bodies with domain-specific shapes keep their own budgets and
are untouched by this change: web_fetch's ContextCard (240-char snippet),
web_search's five 200-char result lines, edit diffs, plain-text confirmations.
Callers that pass `fold={false}` (`ShellCommandBlock`, `MCPToolArguments`,
`compactContext`) are unaffected.

`job_watch` bodies carry a second, separate clamp: notes over
`NOTE_CLAMP_LINES = 20` lines hide behind "Show full note"
(`tools/jobWatch.tsx`).

## Agreed decisions (Jesse, 2026-10-11)

1. The CodeBlock fold window goes from 14 lines to 100 lines.
2. The job_watch note clamp goes from 20 lines to 100 lines — one consistent
   "100 lines before any clamping" rule across tool-result bodies.
3. The 8,000-character retention budgets stay as they are (shell tail buffer,
   read_file tailFold, grep/ls/glob head clip, read_transcript clip). For
   typical output 8,000 chars holds 100+ lines, so the line fold binds first;
   wide-line output can still hit the honest "earlier output not retained"
   elision before 100 lines. That interaction is accepted for this change.

## Behavior

- B1 — CodeBlock fold window. Content at or under 100 lines renders in full
  with no controls. Content over 100 lines renders its last 100 lines behind a
  "Show N earlier lines" control where N = total − 100. Reveal shows the full
  content and a "Show fewer lines" control that re-folds. The fold keeps the
  tail, not the head, exactly as today (the informative end of a dump is at the
  end). Line-number gutters keep true positions in the folded view (first
  visible line number = total − 99).
- B2 — job_watch notes. Notes at or under 100 lines render in full with no
  disclosure; longer notes clamp behind "Show full note" / "Show less", same
  mechanism as today, threshold 100. The clamp has two halves that must move
  together: `NOTE_CLAMP_LINES` in `jobWatch.tsx` (when the clamped class and
  disclosure apply) and `.noteClamped`'s `-webkit-line-clamp` in
  `jobWatch.module.css` (how many lines the clamped view shows). Both go to
  100. jsdom cannot apply line-clamp, so the CSS half is pinned by a
  stylesheet-text assertion (the `toolRowGrammar.test.tsx` pattern), not by
  layout.
- B3 — Character budgets unchanged. The 8,000-char elision lines and Copy
  behavior are exactly as before.
- B4 — Honesty preserved. The fold still never offers expansion over bytes that
  are gone; the character-elision notices still say what was dropped. Copy
  still copies the retained text.

## Non-goals

- No change to mobile-native or the TUI (request names the web UI; the phone
  keeps its own preview line counts).
- No change to the character budgets, DiffBlock, ThinkBlock, or markdown fenced
  blocks (those render static HTML with no fold).
- No change to domain-specific body budgets: web_fetch's 240-char ContextCard
  snippet, web_search's five 200-char result lines, and other non-CodeBlock
  body shapes keep their own limits.
- No redesign of the fold mechanics: tail window + reveal stays; only the
  threshold moves.
- No change to `fold={false}` callers.

## Files

| File | Change |
| --- | --- |
| `widgets/codeblock/index.tsx` | `TAIL_VISIBLE_LINES` 14 → 100; rewrite the 67zh comments (prop doc and constant rationale) to state the 100-line window and keep the tail-keeping rationale. |
| `tools/jobWatch.tsx` | `NOTE_CLAMP_LINES` 20 → 100; update the note-policy comment (mockup's "~20 lines" superseded by Jesse's 2026-10-11 100-line call). |
| `tools/jobWatch.module.css` | `.noteClamped` `-webkit-line-clamp` 20 → 100; update the "~20 lines" comment. |
| `widgets/codeblock/codeblock.test.tsx` | Move every fold-dependent sample past 100 lines so each test keeps exercising the fold, not just failing loudly: the boundary pair (exactly 100 renders with no controls; 101 folds with "Show 1 earlier line"), the reveal/refold cycle, true gutter numbers in the folded view, copy-while-folded (the folded state is the point of that test), and ANSI SGR continuation across the fold. Rewrite the 67zh rationale comment for the 100-line window. |
| `tools/jobWatch.test.tsx` | Clamp test uses a 101-line note; add a stylesheet-text pin that `.noteClamped` carries `-webkit-line-clamp: 100`. The full-render test needs no change — its `TIMER_NOTE` is a one-line note, and lengthening it would break the summary pins that share `TIMER_RAW`. |
| `panes/session/transcript/ToolCallItem.test.tsx` | The real recorded memory fixture (24/21-line outputs) now renders in full: drop the fold-click, assert both markers directly. Add an integration test with a synthetic 101-line output through the generic body so the expand → fold → reveal journey stays covered at the tool-row level. The recorded fixture itself is not modified. |
| `dev/gallery-sections/codeblock.tsx` | The 21-line pytest sample no longer folds; lengthen it past 100 lines so the gallery still demos the folded state; update the comment and row label. |
| `dev/editorial-preview/data.ts` | Lengthen the "long native source evidence" sample from 120 to 160 lines so the long line (31) stays in the hidden head ("Show 60 earlier lines"), with short per-line payloads so the whole sample stays under read_file's 8,000-char retention budget — an over-budget sample elides its head (most of the long line) before the fold ever runs. |
| `dev/editorial-preview/boundary.test.ts` | Update the fixture-shape pins (160 lines; last-line text) and pin the sample at or under the 8,000-char retention budget so the scenario cannot silently rot. |
| `docs/web-ui/editorial-preview.md` | Update the manual scenario's fold count to match the fixture ("Show 60 earlier lines"). |
| `scripts/editorial-preview-browser.mjs` | The hand-run scenario script keeps asserting the complete long line renders after reveal (made true again by the budget pin); its native-evidence assertion moves to the revealed head line. |

No product guide changes: no evergreen doc pins the fold threshold; the design
law ("never offer expansion over bytes that are gone") is untouched.

## Test plan

Whole journey, red first:

1. Unit (widget): the boundary pair — exactly 100 lines renders with no
   controls; 101 lines folds with "Show 1 earlier line". Reveal shows line 1,
   offers "Show fewer lines"; re-fold returns to the tail. Gutter in the folded
   view starts at the true line number (total − 99). ANSI sample over 100
   lines keeps SGR state that began on a hidden line.
2. Unit (job_watch): a 101-line note clamps behind "Show full note"; a
   ≤100-line note renders in full with no disclosure; a stylesheet-text
   assertion pins `.noteClamped`'s `-webkit-line-clamp` at 100 (jsdom cannot
   apply line-clamp, so layout cannot prove this half).
3. Integration (tool row): a synthetic 101-line generic-body output folds in
   the expanded transcript row and reveals; the recorded memory fixture test
   keeps asserting its real output renders whole.
4. Fixture pins: editorial boundary test updated to the 160-line sample.
5. Gates: targeted vitest runs while red-green; `make test-web` (typecheck,
   vitest, Biome) green before the PR; `make test-web-browser` on this
   Chrome-capable host before merge (no guard pins the fold, but the editorial
   fixture feeds real transcript rendering, and expanded bodies grow ~7x
   taller — the transcript's VirtualList is dynamically measured, so
   correctness is safe; the browser guards prove the real geometry still
   behaves with the taller rows).
6. Baseline recorded before any change: `make test-web` PASS in this lane at
   base 246e68444b (typecheck 28.7s, web-test 194.4s, web-lint 2.2s).

## Recovery and preservation

Display-only change. Copy semantics, retained text, and elision notices are
unchanged; the disclosureStore's persistent per-item open/closed choice is
unchanged; CodeBlock's reveal stays component-local state that re-folds on
remount, as today. Nothing here destroys or duplicates user work under any
condition; no recovery path changes.

## Spec review round (2026-10-11)

Two competing independent reviewers examined this spec (PAR); every reported
finding was reproduced against the tree before revision, and none were
invented or inflated. The significant finding, reported by both reviewers:
the job_watch clamp has a CSS half (`jobWatch.module.css` `.noteClamped`
`-webkit-line-clamp: 20`) the first draft missed — now in B2 and the files
table. Further revisions: the mechanism paragraph now enumerates the
fold-covered bodies completely and names the domain-specific bodies that keep
their own budgets (web_fetch, web_search); the `fold={false}` list includes
`ShellCommandBlock`; the codeblock test row keeps copy-while-folded
exercising the folded state; the jobWatch test row no longer claims the
one-line-note full-render test changes; the test plan names the taller-row
geometry risk and its browser-gate coverage.

## Implementation review round (2026-10-11)

Two competing reviewers examined the committed diff
(b3ed1ea78e..27b0113a69). The significant finding (reviewer B, reproduced
with a char-budget simulation): a 160-line editorial fixture with full
payloads runs ~10.5k chars, so read_file's 8,000-char tailFold elides the
head — most of the long line included — before the fold runs; the rendered
button would read "Show 31 earlier lines", not the documented 60, and the
click cannot expose the long line whole. Resolution: the fixture's per-line
payloads shrink so the whole sample fits the budget (7,444 chars),
boundary.test.ts pins that budget, and the scenario script's native-evidence
assertion moves to the revealed head line. Also resolved: the stale "~20
lines" phrase in jobWatch.tsx's header comment (found by both reviewers) and
the jobWatch CSS pin regex gained a value terminator so it cannot match
"100px". Everything else both reviewers checked verified clean: spec
conformance, non-goals, flip correctness, test quality, leftovers, and
fixture math.

## Approval

Jesse approves this spec before implementation proceeds. Approved 2026-10-11;
the review-round revisions above stay inside the approved decisions (the CSS
half is the same "align job_watch to 100" call completed).
