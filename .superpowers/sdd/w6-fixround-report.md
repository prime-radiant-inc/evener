# W6 fix round report — five-item roster

Worktree `webui-w6-surfaces`, branch `w6-surfaces`. Starting HEAD `c7b9a3134`
(217 test files / 3185 tests, confirmed by a full baseline run before any
change: `tsc --noEmit` exit 0, `vitest run` 217/3185 all passing, `npm run
lint` exit 0). Final HEAD `12bc3c974`. One commit per item, in the brief's
order. All five items: DONE.

Gate interpretation: per the brief's "per commit, AND-chained ... then
before finishing npm run build" phrasing, `tsc --noEmit` → `vitest run`
(bare) → `npm run lint` ran per commit (all five, all green); `npm run
build` + `git restore dist/PLACEHOLDER` ran once at the end, after the
fifth commit, as the final gate.

## Item 1 — Sheet gains a left anchor; the rail drawer uses it

**Finding.** `widgets/sheet/index.tsx:7`'s `SheetSide` was `"right" |
"bottom"` only. `RailHost.tsx` renders its ☰-chip overlay drawer via
`<Sheet side="right" ...>`, but the chip itself sits top-left
(`CLASS.chipBar`) — the drawer opened from the opposite edge it should.

**Change.**
- `widgets/sheet/index.tsx`: added `"left"` to `SheetSide`; added a `left`
  entry to `SIDE_CLASS` via `requireClass(styles.left, ...)`; updated the
  `Sheet` JSDoc to mention the left edge.
- `widgets/sheet/sheet.module.css`: added `.left` (mirrors `.right` —
  `position: fixed; left: 0`, `border-right` kept as the seam, others
  removed, `sheetInLeft` keyframe sliding in via `translateX(-100%to 0)`)
  and added `.left` to the `prefers-reduced-motion` selector list.
- `shell/rail/RailHost.tsx`: `<Sheet side="right" ...>` → `<Sheet
  side="left" ...>` (the drawer's only side prop).
- No change to `OverlayPanel`/dialog trap/Escape/scrim logic — `Sheet`
  composes `OverlayPanel` unconditionally regardless of `side` (`side`
  only ever selects a CSS class), so that contract is untouched by
  construction, not just by convention.

**RED evidence.**
- `sheet.test.tsx`: extended "side=right and side=bottom render distinct
  panel classes" into a 3-way test asserting each of right/bottom/left is
  non-empty and mutually distinct. Pre-fix: `leftClass` was `""` —
  `SIDE_CLASS["left"]` is a plain JS object with no `left` key, so it
  evaluates to `undefined`; React renders no `class` attribute at all for
  an `undefined` `className`, so jsdom's `.className` reads `""`. Exact
  failure: `AssertionError: expected '' not to be ''` at `expect(leftClass
  ).not.toBe("")`.
- `RailHost.test.tsx`: new test "the drawer is anchored left, matching the
  ☰ chip's top-left position" — imports `sheet.module.css` directly and
  asserts the opened drawer's dialog class list contains `sheetStyles.
  left`. Pre-fix (run after the Sheet-widget fix, RailHost still on
  `side="right"`): `AssertionError: expected [ '_panel_3fbb8d',
  '_right_b7f797' ] to include '_left_b7f797'`.

**Mutation proof.**
- Reverted `SIDE_CLASS.left` to `undefined as any` → the 3-way
  distinctness test failed the same way as pre-fix; restored, reran green.
- Reverted `RailHost.tsx`'s `side="left"` back to `"right"` → the new
  RailHost test failed the same way as pre-fix; restored, reran green
  (13/13 in that file).

Files: `cmd/serf-hub/frontend/src/widgets/sheet/index.tsx`,
`.../sheet.module.css`, `.../sheet.test.tsx`,
`cmd/serf-hub/frontend/src/shell/rail/RailHost.tsx`, `.../RailHost.test.tsx`.
Commit `ceb88f08c`. Post-commit full suite: 217 files / 3187 tests (+2: the
1 explicit new RailHost test, plus 1 test `requireclass-contract.test.ts`
generates dynamically per source file that imports a `.module.css` —
RailHost.test.tsx now does, via `sheetStyles`, so that file gained an
auto-generated entry verifying `sheetStyles.left`/`.right` resolve to real
classes. Verified this is the mechanism, not a miscount, by diffing
`test(` occurrences at HEAD vs working tree per file: sheet.test.tsx
11→11, RailHost.test.tsx 12→13).

## Item 2 — ⌘B chord/focus guard

**Finding.** `RailHost.tsx:54`'s keydown handler accepted `Ctrl+B` (or
`⌘B`) and fired unconditionally, with no check on `event.target`. `Ctrl+B`
is the macOS emacs-style "move cursor back one character" binding native
text fields honor while focused — the handler hijacked it while typing in
the composer or any other input.

**Precedent check (per the brief's instruction).** Searched the codebase
for an existing "is this target editable" guard before inventing one:
`grep` for `contentEditable`/`isContentEditable`/`tagName ===`/`TEXTAREA`
found one unrelated hit (`DirField.tsx:157`, a local Enter-key handler
checking `tagName === "INPUT"` to accept a completion — a different
concern, not a global-shortcut guard). Checked every global `keydown`
listener in the codebase (`AppShell.tsx`, `RailHost.tsx`,
`widgets/focusscope/index.tsx`): `AppShell.tsx`'s ⌘K/Ctrl-K handler has no
editable-target guard either, but that's correct as-is — ⌘K has no native
text-editing meaning to collide with, unlike Ctrl+B. No shared predicate
exists. Per the brief, wrote one small local guard.

**Change.** `RailHost.tsx`: added a module-scope `isEditableTarget(target:
EventTarget | null): boolean` (checks `target instanceof HTMLElement`,
then `isContentEditable || tagName is INPUT/TEXTAREA/SELECT`) with a
WHAT/WHY comment explaining the emacs-binding collision and why the guard
stays local (RailHost is its only caller). `onKeyDown` now opens with `if
(isEditableTarget(event.target)) return;` before the existing chord check
— applied uniformly regardless of which modifier (meta or ctrl) triggered
the event, per the brief's framing ("SKIP when the event target is
editable"), not just for the ctrl form.

**RED evidence.** New `RailHost.test.tsx` test: renders `<RailHost/>` plus
a real `<textarea aria-label="Composer"/>`, focuses it, dispatches
`Ctrl+B` (`ctrlKey: true`) directly on the textarea (bubbles to `window`,
where the listener lives). Pre-fix: `AssertionError: expected 'pane' to be
'rail'` — the chord cycled `sidebarMode` and called `preventDefault()`
despite the editable focus.

**Mutation proof.** Removed the guard's early-return line only (left
`isEditableTarget` defined but uncalled) → the new test failed identically
to pre-fix; all 13 other RailHost tests (including both `⌘B cycles ...`
tests, which dispatch on `window` — not an `HTMLElement`, so the guard
never blocks them) stayed green, confirming the mutation broke exactly the
intended behavior and nothing else. Restored, reran green (14/14).

Biome's formatter required wrapping the guard's boolean-OR return
expression (single-line version exceeded the project's 120-col
`lineWidth`); applied via `npm run format -- --files-ignore-unknown=false
src/shell/rail/RailHost.tsx` and confirmed via `git status --short` that
only `RailHost.tsx` changed (no incidental reformatting elsewhere).

Files: `cmd/serf-hub/frontend/src/shell/rail/RailHost.tsx`,
`.../RailHost.test.tsx`. Commit `b97e0c9d1`. Post-commit full suite: 217
files / 3188 tests (+1, the one new focus-guard test; no new CSS import in
this commit, so no additional dynamic `requireclass-contract` entry).

## Item 3 — display-gates regex tighten

**Finding.** `src/styles/display-gates.test.ts`'s "compact multiplier 1"
assertion, `expect(media![1]).toMatch(/--density-scale:\s*1\b/)`, also
matches inside `--density-scale: 1.25` — `\b` is satisfied by the `"1"` →
`"."` transition (`.` is a non-word character), so the regex is really
"the literal digit 1 appears, optionally followed by non-word text." It
therefore cannot distinguish "the base seed exists" from "the base seed
was deleted and only the comfortable rule's `1.25` remains in the
searched block."

**Verified the weakness empirically before changing anything:** temporarily
deleted the `--density-scale: 1;` seed line from `tokens.css` (inside the
bare `body { }` rule under `@media (max-width: 900px)`) and ran the
*unmodified* test file — all 6 tests, including the one in question,
stayed green. Restored `tokens.css` (`git diff` empty, confirmed clean).

**Change.** `display-gates.test.ts`: added `baseDensityScale
(mediaBlockContent)`, mirroring the file's existing `fontScaleFor()`
helper (scoped-rule-then-capture, not a blob-wide pattern search) — it
finds the bare `body { }` rule specifically via `/\bbody\s*\{([^}]*)\}/`
(this cannot match `body[data-phone-density="comfortable"] {` — the `[`
immediately after `body` breaks `\s*\{`), then captures that rule's own
`--density-scale` value. The test now asserts
`baseDensityScale(media![1]!) === "1"` (exact string equality, not a
loose regex match).

**RED evidence.** Confirmed the tightened assertion passes against the
real (unmutated) `tokens.css` first (GREEN), then re-ran the exact same
seed-deletion mutation used to verify the weakness: `AssertionError:
expected null to be '1'` — `baseDensityScale` correctly returns `null`
when the seed rule it's scoped to no longer contains the declaration,
where the old broad regex silently kept matching the comfortable rule's
`1.25` prefix.

**Mutation proof.** The above *is* the mutation proof (brief's own
instruction for this item: "verify by mutation: delete the seed locally,
watch it bite, restore"). Restored `tokens.css`; `git diff` confirmed
empty both before and after.

Files: `cmd/serf-hub/frontend/src/styles/display-gates.test.ts` only (no
production code changed — this item is a test-quality fix).
Commit `4f594d67d`. Post-commit full suite: 217 files / 3188 tests
(unchanged count — no new `test()` block added, only a helper function and
a tightened assertion inside an existing test).

## Item 4 — Theme help-copy gate correction

**Finding.** `panes/settings/sections/theme.tsx`'s Phone density help copy
read "Type-scale variant on phone (≤767px). Compact is the default." The
shipped gate (`tokens.css`: `@media (max-width: 900px)`, added by wave 6,
matching `useIsMobile`'s own 900px-family breakpoint) is 900px, not 767px
— 767 appears to be a stale legacy-Bootstrap-style assumption baked into
wave 7's frozen copy before wave 6 shipped the real gate.

**Change.** `theme.tsx`: `≤767px` → `≤900px`. Sentence-case, plain-verb
phrasing unchanged — only the number was wrong.

**RED evidence.** New `theme.test.tsx` test (in the existing "Phone
density" describe block): renders the section and asserts
`screen.getByText(/900px/)` resolves. Pre-fix: `TestingLibraryElementError:
Unable to find an element with the text: /900px/` (full DOM dump showed
"≤767px" instead).

**Mutation proof.** Reverted the copy to `≤767px` → the new test failed
identically (element not found); restored, reran green (7/7 in that
file).

Files: `cmd/serf-hub/frontend/src/panes/settings/sections/theme.tsx`,
`.../theme.test.tsx`. Commit `28067e51a`. Post-commit full suite: 217
files / 3189 tests (+1, the one new copy-pinning test).

## Item 5 — Stale prefs.ts comments

**Finding.** Four doc-comment locations in `src/stores/prefs.ts` (lines
~53, 103-104, 117, 134 — matches the T5 review's citation exactly) still
named `shell/rail/Rail.tsx`'s removed `readCollapsed`/`persistCollapsed`/
`COLLAPSED_STORAGE_KEY`/`serf.rail.collapsed.v1` boolean-collapse
mechanism, either as a cited precedent or as an open gap ("wiring a real
3-state mode into the shell is out of this store's manifest"). Verified
before editing: `grep` for all four identifiers across `src/` found zero
hits in `Rail.tsx`/`RailHost.tsx` (both files touch no `localStorage`
directly at all now — confirmed by a direct grep), and the "out of
manifest" claim is now false — `RailHost.tsx` + `useSidebarMode.ts`
implement exactly the 3-state auto/pane/rail mode this comment said hadn't
happened yet.

**Change.** Reworded each of the four spots to the current truth, stating
WHAT/WHY as of now with no "here's how it used to work" narration (per the
brief and per CLAUDE.md's own comment rule):
1. (~line 50-55) sidebarMode's document-mirror status + its real consumer
   (`shell/rail/RailHost.tsx`, which drives the 3-state visibility and
   owns the ⌘B cycle) replaces the "legacy delegates to
   window.SerfSidebar..." / "out of manifest" narration.
2. (~line 101-104) `readRaw`'s best-effort rationale stands alone; dropped
   the dangling "matching Rail.tsx's own readCollapsed/persistCollapsed
   precedent" clause.
3. (~line 116-121) `writeRaw`'s catch-block comment: same drop, rationale
   otherwise unchanged (still accurate on its own terms).
4. (~line 132-135) the "1"/"0" encoding is now stated as this store's own
   convention across every boolean field it persists (enterToSend,
   showCost, transcript.*, notifications.*), not borrowed from Rail.tsx's
   removed `COLLAPSED_STORAGE_KEY`.

No other prose in the file was touched — the surrounding historical/WHY
commentary that isn't about the removed mechanism (e.g. the
commit-932eeddca boolean-encoding regression note, the htmx-world
comparisons) is unrelated to this item's scope and still accurate.

**RED evidence / mutation proof.** Not applicable — comment-only change,
no runtime behavior to assert on or mutate. Verified by: (a) `grep -n
"COLLAPSED_STORAGE_KEY\|serf.rail.collapsed\|readCollapsed\|
persistCollapsed" src/stores/prefs.ts` → no matches (exit 1); (b) manual
read-through of the diff against the current `RailHost.tsx`/
`useSidebarMode.ts` implementation to confirm every replacement claim is
accurate; (c) full gate chain (tsc/vitest/lint) run as a sanity check —
all green, test count unchanged at 3189 (expected: no behavior changed).

Files: `cmd/serf-hub/frontend/src/stores/prefs.ts` only. Commit
`12bc3c974`.

## Gates (final)

- `npx tsc --noEmit`: exit 0, all five commits.
- `npx vitest run` (bare, AND-chained): 217 files passed / 3189 tests
  passed at final HEAD (baseline 217/3185 → net +4 explicit tests across
  items 1/2/4, +1 dynamically-generated `requireclass-contract` entry from
  item 1's new CSS import in a test file — accounted for and verified, not
  just observed).
- `npm run lint` (Biome ci): exit 0, all five commits (one intermediate
  formatting fix applied via `npm run format` in item 2, verified scoped
  to the one file being edited).
- `npm run build`: exit 0 (349 modules, all chunks emitted). `git restore
  cmd/serf-hub/frontend/dist/PLACEHOLDER` ran immediately after; `git
  status --short` confirmed a fully clean tree.

## Concerns

- Out-of-scope staleness noticed but NOT fixed, per the brief's fixed
  five-item roster ("Follow it exactly"): `src/stores/prefs.test.ts:200`'s
  comment also cites `Rail.tsx`'s removed `COLLAPSED_STORAGE_KEY` as the
  "1"/"0"-encoding precedent (same staleness class as item 5, different
  file — item 5 named only `prefs.ts`). Also, `prefs.ts` lines ~44-49
  (the phoneDensity/fontSize document-mirror comment) still say "no CSS in
  the new design system keys off those two attributes yet" — untrue since
  wave 6 shipped the `tokens.css` font/density gates this same fix round's
  item 3 touches. Neither was in the T5 review's cited line list for this
  item, so left alone; flagging for a future pass.
- Item 1's dev widget gallery (`src/dev/gallery-sections/sheet.tsx`) still
  only demos `side="right"` and `side="bottom"` — no gallery entry for the
  new `left` variant. `WidgetGallery.test.tsx`'s completeness guard only
  checks one gallery section exists per widget directory, not per side
  variant, so this isn't gate-enforced and wasn't in the brief's item-1
  deliverable list (widget test + RailHost assertion only). Left
  unaddressed; trivial to add later if wanted.
- No blockers, no scope ambiguity encountered, no deviation from the
  brief's ordering or gate sequencing.
