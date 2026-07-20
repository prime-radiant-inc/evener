# Wave 2 Task 2 report — PRIMITIVES widget batch

Stream: `webui-w2-primitives` (branch `w2-primitives`, off Task-1 tip `094bf2b38`).
13 widgets, 13 commits, purely additive (2418 insertions, 0 deletions, 65 files), zero touches
to any owned-by-others path (verified via `git diff --stat` against `tokens.css`, `global.css`,
`widgets/index.ts`, `widgets/button/**`, `widgets/cadence/**`, `src/protocol/**`, `src/stores/**`,
and every config file — all empty diffs).

## Commits

| Commit | Widget |
|---|---|
| `56ad846fa` | Card |
| `cfbaf90e0` | Badge |
| `673f9d6de` | StatusDot |
| `df442ee84` | Skeleton |
| `ef8213874` | EmptyState |
| `aa12d11ef` | Chip |
| `f9ef3d9fe` | Meter |
| `973099a6d` | KeyHint |
| `36abef294` | IconButton |
| `d00a228f5` | Input |
| `5680f68f3` | Textarea |
| `92f331b85` | Select |
| `9cf85bca3` | Switch |

## TDD evidence

Every widget followed red→green: test file written first, run to confirm failure (missing
`./index` / missing `.module.css` import), then implementation added until green. Two cases
where "red" caught a real bug rather than just "not implemented yet":

- **Select**: the first version of `"selecting a different option fires onChange with its
  value"` asserted on `onChange.mock.calls[0][0].target.value` *after* the `await`. That failed
  (`"us-east"` instead of `"eu-central"`) because `event.target` is a live DOM node and, with a
  fixed `value` prop and no-op `onChange`, React's controlled-element reconciliation resets the
  select back to `"us-east"` before the assertion runs. Fixed by driving the test through a
  `ControlledSelect` `useState` harness (same pattern already used for Input/Textarea), which
  also exercises the more realistic usage path.
- **Skeleton** and **IconButton**: both regex-based "does this CSS avoid X" tests (no
  keyframes/animation/transition; no `:focus-visible`) initially failed against my *own*
  explanatory CSS comments, which happened to contain the literal string being scanned for
  (`@keyframes`, `:focus-visible`). Reworded the comments rather than weakening the assertions.

Full suite: 243 tests across 20 files (73 pre-existing + 170 new: 13 widget test files +
dynamically-generated `token-contract.test.ts` entries for 19 new CSS files, 2 tests/file).
239 pass; 4 fail, all in `token-contract.test.ts`, all the same known cause (next section).
Confirmed stable across two full reruns (`npm run test` twice, identical 239/243 both times) —
no flakiness. `npx tsc --noEmit`, `npm run lint`, `npm run build` all exit 0, no output.

## Concern: `token-contract.test.ts`'s `SEMANTIC_USE_ALLOWLIST` needs 4 entries added

**This is the one thing standing between this stream and a fully green `npm run test`.**

Global Constraints requires every interactive widget to have "a visible `:focus-visible` ring
(accent token)" — i.e. `var(--accent)`, mirroring Button's own exemplar rule exactly
(`button.module.css`: `.button:focus-visible { outline: 2px solid var(--accent); ... }`).
`token-contract.test.ts`'s `SEMANTIC_VAR_RE` (`/var\(\s*--(?:attention|alive|danger|accent)\b/`)
matches `--accent` and every `--accent-*` derivative — confirmed directly in Node, not just by
reading the regex:
```
> /var\(\s*--(?:attention|alive|danger|accent)\b/.test("var(--accent-bg)")
true
```
So *any* widget whose CSS module reaches for `var(--accent)` — for any reason, including a
plain focus ring — must be on `SEMANTIC_USE_ALLOWLIST`, or the "only reaches for semantic vars
if allowlisted" test fails for that file. The current allowlist (`cadence, button, chip, badge,
statusdot, meter, toast, dialog`) covers every widget with a `tone`/status-color use, but was
never extended to cover the *universal* focus-ring use of `--accent` — none of Task 1's own
widgets (Button, Cadence) exercises "needs `--accent` for a focus ring and nothing else", so the
gap wasn't visible until this batch hit it. It affects exactly 4 widgets in this batch — the
native/custom interactive form controls that can't inherit another widget's focus ring the way
IconButton inherits Button's (see below):

- `widgets/input/input.module.css`
- `widgets/textarea/textarea.module.css`
- `widgets/select/select.module.css`
- `widgets/switch/switch.module.css` (also uses `--accent` for its checked-track fill)

**Fix**: add `"input", "textarea", "select", "switch"` to `SEMANTIC_USE_ALLOWLIST` in
`src/styles/token-contract.test.ts`. That's the entire fix — I verified each of these 4 files
has no other contract violation once allowlisted (spot-checked by temporarily adding the
entries locally and re-running; reverted before committing, since that file is outside this
stream's owned dirs).

**Why this isn't fixed in this stream's commits**: `token-contract.test.ts` is not among the
dirs this stream owns (`src/widgets/{iconbutton,chip,badge,statusdot,input,textarea,select,
switch,keyhint,meter,skeleton,emptystate,card}/**` and the matching `gallery-sections/*.tsx`),
and it's shared, cross-stream infrastructure in the same category as the controller-owned
barrel (`src/widgets/index.ts`) — T3/T4 almost certainly hit the identical gap for their own
interactive widgets (of T3/T4's locked-API widgets, only `toast`/`dialog` are pre-allowlisted;
Menu/Tooltip/Combobox/Tree/etc. all need focus rings too), so this array is likely to get
touched by more than one stream. I judged self-editing a shared file outside this stream's
explicit ownership riskier than leaving 4 well-understood, precisely-diagnosed failures for the
controller to resolve in one line each at merge time — same handoff shape as the barrel exports
below, just a different file.

**This is not a real defect**: I verified end-to-end, in a live `npm run dev` + Chrome session
(not just reasoning about the regex), that the focus ring actually ships and renders correctly
despite the test gap — `token-contract.test.ts`'s allowlist is a static source-text lint, not a
build-time exclusion:
```
input.focus() → getComputedStyle(input).outlineColor === "rgb(108, 160, 245)"
             === getComputedStyle(document.documentElement).getPropertyValue("--accent")  (#6CA0F5)
```
2px solid, exactly matching Button's own ring. Switch's live click also verified end-to-end in
that session (`aria-checked` flips `false→true` on click against the real Vite bundle, not just
jsdom). Console was clean (no React warnings) across all 13 gallery sections in both themes.

## Barrel exports (for the controller to add to `src/widgets/index.ts`)

```ts
export { IconButton } from "./iconbutton";
export type { IconButtonProps } from "./iconbutton";

export { Chip } from "./chip";
export type { ChipProps, ChipTone } from "./chip";

export { Badge } from "./badge";
export type { BadgeProps, BadgeTone } from "./badge";

export { StatusDot } from "./statusdot";
export type { StatusDotProps } from "./statusdot";

export { Input } from "./input";
export type { InputProps } from "./input";

export { Textarea } from "./textarea";
export type { TextareaProps } from "./textarea";

export { Select } from "./select";
export type { SelectProps, SelectOption } from "./select";

export { Switch } from "./switch";
export type { SwitchProps } from "./switch";

export { KeyHint } from "./keyhint";
export type { KeyHintProps } from "./keyhint";

export { Meter } from "./meter";
export type { MeterProps, MeterTone } from "./meter";

export { Skeleton } from "./skeleton";
export type { SkeletonProps } from "./skeleton";

export { EmptyState } from "./emptystate";
export type { EmptyStateProps } from "./emptystate";

export { Card } from "./card";
export type { CardProps } from "./card";
```

## Design decisions worth flagging

- **IconButton composes Button's CSS classes, not the `<Button>` component.** Button's props
  don't include `aria-label` and its render body doesn't forward arbitrary attributes onto the
  native `<button>`, so rendering `<Button>` as a child can't put `aria-label` on the DOM node.
  `iconbutton/index.tsx` imports `button.module.css` directly (read-only) and reuses its
  `.button`/variant/`.icon` classes verbatim — identical colors and the identical focus ring,
  zero duplicated CSS, and `iconbutton.module.css` itself carries only square sm/md sizing (no
  color at all), so it needs no allowlist entry.
- **`EmptyState.action` is optional** (`action?: ReactNode`) despite the wave-2 plan's locked-API
  line reading `EmptyState {title; hint?; action}` (no `?` on `action`, unlike every other
  optional slot in that same line-family — `PaneScaffold`'s `footer?`, `hint?` itself). Read as
  a documentation typo, not a deliberate requirement — a pane with nothing actionable to offer
  (e.g. a read-only empty log) is completely ordinary, and every sibling slot prop in this wave
  is optional. Documented inline on `EmptyStateProps`.
- **StatusDot duplicates Cadence's private `STATE_FAMILY`/label mapping** (~10 lines) rather than
  importing it — Cadence keeps that mapping unexported, and `widgets/cadence/**` is outside this
  stream's owned dirs, so there's no shared home to extract it to without touching a forbidden
  dir. Small, self-contained, commented as intentional.
- **KeyHint's `<kbd>` glyphs use `--font-mono`**, not `--font-sans` (which every gallery "chrome
  label" in this codebase uses per Direction's "mono never for chrome labels" rule). Judgment
  call: a kbd glyph is a verbatim rendering of a literal keystroke — the same category Direction
  reserves mono for (code, tool output, paths, timings) — not a designer-authored label, and
  it's the universal convention for shortcut hints elsewhere (VS Code, GitHub, etc.).
- **Textarea's `autoGrow` counts line breaks in `value`, never reads `scrollHeight`.** Not just a
  testability convenience (though jsdom can't compute real layout, so a scrollHeight approach
  would be unverifiable in RTL anyway): it recomputes exactly once per value change, purely from
  the prop already in hand, with no DOM read/write cycle — "no scrollHeight thrash" by
  construction rather than by careful sequencing.
- **Select stays a plain native `<select>`, no `appearance:none`/custom chevron** — avoids a
  data-URI SVG background-image, which would risk smuggling a literal color past
  `token-contract.test.ts`'s literal-color scan (that scan runs against raw file text, comments
  included, so an embedded SVG's `fill="#..."` would trip it).
- **Switch's accessible name is wired via `aria-labelledby` + `useId()`**, not implicit HTML
  label-wrapping — a `<button role="switch">` isn't guaranteed the same label-wrapping
  accessible-name treatment native form controls (input/select) get, so this is the explicit,
  verifiable path (confirmed via `getByRole("switch", {name})` in tests).
- Chip's remove button uses a static `aria-label="Remove"`, not "Remove {label}" — `children` is
  arbitrary `ReactNode` (the locked API's exact shape), not guaranteed to be a string, so it
  can't always be safely interpolated into a more specific label.

## Self-review

- Every widget's CSS module was checked against the token-contract test individually as it was
  built (not just at the end) — each of the 13 was confirmed to either pass cleanly or fail only
  the one, already-diagnosed allowlist assertion.
- No widget beyond the four listed above reaches for `--attention`/`--alive`/`--danger`/
  `--accent` at all except the ones already pre-allowlisted (Chip/Badge/StatusDot/Meter, for
  their `tone`/`state` props, per the wave-2 plan's own allowlist seed list).
- No inline styles except Meter's `--fill` custom property (the one place Global Constraints
  explicitly calls for exactly this pattern).
- All `afterEach(cleanup)` present per file, matching the established project precedent (no
  global RTL auto-cleanup wired into `vite.config.ts`, which this stream doesn't touch).
- Ran a live `npm run dev` + Chrome session against the real gallery route as extra due
  diligence beyond the stated verification list (test/typecheck/lint/build) — zero console
  errors/warnings across all 13 sections in both themes; confirmed the actually-shipped CSS/JS
  behaves correctly, not just what the source text says.
- `git status` clean at the end (a `npm run build` run transiently deleted the gitignore-carve-out
  file `dist/PLACEHOLDER` via `emptyOutDir: true`; restored via `git checkout --` since it's an
  artifact of running verification, not a real change).
