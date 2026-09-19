# #1862 decomposition — state note (lane retiring, >540k tokens)

Worktree: `.claude/worktrees/sdk-d21-native-model` (absolute path from repo root). #1862 was decomposed into
four stacked PRs (#1892 ask-resolution, #1893 prunedForStringify, #1894 warning-string bounds, #1895 mobile
warning composition), each stacked on the previous, each rebased onto `main` as its predecessor merged. #1892
and #1893 and #1894 are merged. #1732 (piece B, web `WarningItem` polish) stacks on #1895.

## The two open heads

| PR | Branch | Head (full 40-char) | Base |
|---|---|---|---|
| #1895 | `claude/1862-split-d-mobile-warning-compose` | `40c8057ab30c7e85c00361e610a3105ccdc16cfa` | main |
| #1732 | `claude/c2b-b-warning-and-reasoning-web` | `6dbfda78ca378ddb2f90c31b425a2e4c4f19c25f` | main |

Both report `mergeable: true`, `mergeable_state: "blocked"` (normal post-force-push state while GitHub
re-evaluates required checks, not a conflict) as of this note.

### What #1895 still owes

- **No review panel yet at this head.** The last panel (`reviews/raw/1895-8e479df7f.md`) reviewed the PRE-round-3
  head; the round-3 fix (title dropped when a message is present — `joinWarningParts` added to the package,
  both mobile call sites use it; see disposition comment on the PR) and the subsequent rebase onto `main` after
  #1894 merged have NOT been reviewed by a fresh panel. Expect one more round before merge.
- Gates last run clean at head `40c8057ab`: 464 mobile-native tests (`project.test.ts` + `conversation.test.ts`),
  216 reducer tests, `npm run typecheck`, `npx biome ci ../../../appwire-client/typescript`, `mobile-native npm
  run check`, `make test-api-package` (touches `index.ts`), `make lint-package-imports`. CI not polled (no-poll
  rule) — check `gh api repos/prime-radiant-inc/evener/pulls/1895 --jq .mergeable_state` and the checks list
  before merging.

### What #1732 still owes

- **No review panel yet at this head either** (last panel `reviews/raw/1732-18f9cfafb.md` predates the #1895
  round-3 fix and the main rebase). Own diff (`WarningItem.tsx` + `WarningItem.test.tsx`, 65+/10- lines)
  confirmed byte-identical across every rebase since it was first built — no risk of drift, just needs a fresh
  round once #1895 is reviewed.
- Depends on #1895 merging first (or landing in the same batch) since it stacks on it.
- Gates last run clean at head `6dbfda78c`: 15/15 `WarningItem.test.tsx`, typecheck, biome, lint clean.

## Follow-up PR — complete list (file:line, one-line fix)

One PR, opened from `main` after #1895 and #1732 merge. Everything below is a Low or a follow-up — nothing
here blocked a merge; RoboRev and /simplify only require this list to exist, not to be actioned before landing
the stack.

### From `reviews/1892-simplify.md` (4)

1. `appwire-client/typescript/deriveAskQuestions.ts:86-90` — `isResolutionItem`'s steering clause duplicates
   `cmd/evener-hub/frontend/src/panes/session/transcript/layoutRoles.ts:46` character-for-character. Fix:
   export `isUserAuthoredSteer(item: ItemModel): boolean` from the package next to `isResolutionItem`, have
   `layoutRoles.ts:46` call it. ~10 lines net, no behavior change.
2. `askDockStore.test.ts:151-156` — the new `askPendingStatusChanged` helper exists in one suite while
   `Session.test.tsx:2157,2233`, `Composer.integration.test.tsx:955`, `AskDock.test.tsx:145` inline the same
   `thread/status/changed` + `askPending: true` literal. Fix: move `askPendingStatusChanged(ref)` (and the one
   `ackAskUserCall` the three suites differ on) into `panes/session/composer/askDock/askDock.testFixture.ts`,
   import at all five sites. ~40 lines net deletion.
3. `appwire-client/typescript/deriveAskQuestions.ts:111-114` — `flatMap` then `slice` allocates two arrays per
   call past the `askPending` gate. Fix: fold the boundary check into one `forEach` instead of `flatMap` +
   `slice`.
4. `appwire-client/typescript/scripts/qualify-package.mjs:62,66,69` — the same `{ turns: [{ items: [askItem] }],
   askPending: true }` literal written three times. Fix: hoist one `const askModel = ...` beside `askItem`
   at :52, reuse at all three call sites.

### From `reviews/1893-simplify.md` (3)

5. `appwire-client/typescript/reducer.ts` (current: `RAW_WARNING_FRAME_MAX_FIELD_CHARS = RAW_WARNING_FRAME_MAX_CHARS`,
   near line 1034) — the field-cap constant is an alias of the frame-cap constant with no independent value,
   despite three read sites treating it as separately tunable. Fix: either delete the alias and use
   `RAW_WARNING_FRAME_MAX_CHARS` at all three sites, or give it its own literal (e.g. `500`) and say why in one
   line.
6. `appwire-client/typescript/reducer.test.ts` — the warning-fold test cluster (now ~20 tests deep) repeats the
   same 9-line `testHydrate()` + `turn/started` preamble in nearly every test, and three tests build the same
   `vi.spyOn(JSON, "stringify")` recorder verbatim. Fix: two local helpers above the cluster —
   `warningTurnModel(): ThreadModel` and `recordStringifyOutputLengths(): { lengths: number[]; restore: () =>
   void }` — then each test is its `params` plus its assertions.
7. ~~`reducer.ts:1054-1056,1082` hand-rolled string bound duplicating `boundedCodePoints`~~ — **already fixed**:
   #1894 extracted `boundedPrefix` and made `prunedForStringify`'s value/key truncation call it (commit
   "one boundedPrefix primitive...").

### From `reviews/1894-simplify.md` (3)

8. `appwire-client/typescript/reducer.ts` (current: `hasWarningText` ~line 1020, `foldWarningParams`'s
   `boundedContent` calls for title/hint/source) — `hasWarningText` scans once, then `foldWarningParams` calls
   `boundedContent` a second time per field (title/hint/source each pay two walks where one would do). Fix: one
   function that does the walk once and answers both questions, e.g. `boundedWarningText(value): string |
   undefined` (bound + non-blank check, `undefined` when blank), with `hasWarningText` kept as the exported type
   predicate built on top of it for the three consumers (`WarningItem.tsx`, `transcriptProjector.ts`,
   `mobile/src/conversation/project.ts`) that still need the boolean.
9. `appwire-client/typescript/reducer.ts` (`warningMessage`, current ~lines 998-1003) — three
   `typeof x === "string" && hasWarningText(x)` guards in front of a predicate that already narrows `unknown`
   to `string`. Fix: drop the redundant `typeof` half at all three sites — `hasWarningText`'s own narrowing is
   sufficient and tsc agrees.
10. `appwire-client/typescript/reducer.test.ts` — `const MAX_CHARS = 2000; // mirrors reducer.ts's
    RAW_WARNING_FRAME_MAX_CHARS` is hand-copied in four separate tests. Fix: export
    `RAW_WARNING_FRAME_MAX_CHARS` from `reducer.ts` and import it in the test file, or hoist one `const
    MAX_CHARS` above the warning-fold cluster. Bundle with #6 above (same cluster, same pass).

### From `reviews/1895-simplify.md` (4)

11. ~~`project.ts:550` / `conversation.ts:2856` two encodings of "join the non-blank warning parts with ` — `"~~ —
    **already fixed**: `joinWarningParts` added to the package (`reducer.ts`, exported from `index.ts`), both
    mobile call sites use it (#1895 round-3 commit "canonical warning rows compose title, message, and hint").
12. `mobile/src/conversation/project.ts` (`warningFallbackText`, current ~lines 547-551) — the `string |
    undefined` return and the two-branch part-selection buy nothing at the one call site (`item.text ===
    warningFallbackText(item) || item.text || item.output` treats `""` and `undefined` identically). Fix:
    `const message = hasWarningText(item.text) ? item.text : item.warning.title; return
    joinWarningParts([message, item.warning.hint]);` with the function returning plain `string`, dropping the
    `undefined` branch.
13. `mobile/src/state/conversation.test.ts:7143` and `:7167` (or nearby — the "keeps the live row id short..."
    pair) — two near-verbatim tests differing only in `params` (`extra: "x".repeat(500)` vs `title:
    "T".repeat(2000)`). Fix: `it.each([["a message-less frame", {...}], ["an oversized title", {...}]])`.
14. `mobile/src/conversation/project.test.ts:1501` and `:1529` (the "surfaces a warning's title/hint... /
    composes a warning's message and hint..." pair, now three tests deep after round-3's title+message+hint
    test) — near-verbatim tests differing only in `params` and the expected substrings. Fix: `it.each` table
    over the shared `hydrateThread`/`applyNotification`/`projectConversation` body, one row per case.

### Panel Lows (not simplify-review findings — RoboRev findings marked Low, never fixed)

15. `appwire-client/typescript/reducer.ts:1132-1134` (`pruned[boundedKey] = ...` inside `prunedForStringify`) —
    two distinct wire keys that share their first `RAW_WARNING_FRAME_MAX_FIELD_CHARS` (2000) code points
    collapse to the same `boundedKey` (both become `prefix + "…"`), and the second assignment silently
    overwrites the first — one of the two fields vanishes from the message-less raw-frame fallback. Fix:
    accumulate `[boundedKey, prunedValue]` pairs and disambiguate collisions when building `pruned` (e.g. suffix
    an index), or accept the collision explicitly with a comment (found on `reviews/raw/1894-8b40112e2.md`,
    pi member 2).
16. `appwire-client/typescript/reducer.test.ts:4808` (coordinator's earlier reference: `:4767` — same test,
    line drifted; `"...bounds a many-key object, not just deep nesting or long strings"`) — this test is
    described as covering `RAW_WARNING_FRAME_MAX_NODES` but its 100,000-flat-key input is fully bounded by
    `RAW_WARNING_FRAME_MAX_OBJECT_KEYS` (50) first, so the node budget is never actually exercised; a regression
    that dropped/weakened the node budget would pass unchanged. Fix: add a case that exceeds 500 nodes only via
    branching under the per-object/array caps (e.g. 50 keys → each a 50-key object ≈ 2,500 nodes), spy on
    `JSON.stringify`, assert the output stays small (found on `reviews/raw/1893-9a9b9e45f.md`, pi member 2).
17. `appwire-client/typescript/reducer.ts:1158-1160` (`boundedContent`) — always starts at the first
    non-whitespace character even when the value already fits within the bound, so a short value like `"
    hello"` loses its leading whitespace for no bounding reason. Fix: only skip leading padding when truncation
    is actually needed (e.g. return `s` unchanged when `s.length <= RAW_WARNING_FRAME_MAX_CHARS`, matching
    `boundedPrefix`'s own fast path, before computing `start`) (found on `reviews/raw/1894-bb6dd7ca0.md`, member
    1).
18. `appwire-client/typescript/reducer.ts:1048` (`prunedForStringify`'s doc comment) — still says "every string
    truncated to `RAW_WARNING_FRAME_MAX_FIELD_CHARS` UTF-16 units," but the implementation truncates to code
    points via `boundedPrefix` now. Fix: change "UTF-16 units" to "code points" (found on
    `reviews/raw/1894-bb6dd7ca0.md`, member 1).
19. `appwire-client/typescript/reducer.test.ts` — global spies (`vi.spyOn(JSON, "stringify")` at lines 4681,
    4751, 4824, 4915; `vi.spyOn(Array, "from")` at 4713; `vi.spyOn(String.prototype, "trim")` at 5016) call
    `spy.mockRestore()` only on the happy path (after the exercised `applyNotification` call, not in a
    `finally`) — if that call throws, the mock leaks into subsequent tests. Fix: wrap the exercised call in
    `try { ... } finally { spy.mockRestore(); }` at all six sites (found on `reviews/raw/1894-bb6dd7ca0.md`,
    member 1, who cited 3 representative lines — all six sites share the pattern).
20. `appwire-client/typescript/reducer.ts:1017-1018` (`hasWarningText`'s doc comment) — says bounding "happens
    separately, only when a value is actually stored (`boundedCodePoints` below, applied at each call site that
    assigns into the model)," but the function actually applied at every `foldWarningParams` assignment site is
    `boundedContent`, not `boundedCodePoints` (`boundedCodePoints` is only used inside `rawWarningFrame`). Fix:
    change the reference to `boundedContent` (found on `reviews/raw/1894-bb6dd7ca0.md`, pi member 2).

## Gates cheat-sheet (targeted, foreground only — never the full local suite; CI is the full matrix)

Run from `.claude/worktrees/sdk-d21-native-model` (or wherever the follow-up PR's worktree lives):

- **vitest, package/web files**: from `cmd/evener-hub/frontend`, e.g. `npx vitest run
  ../../../appwire-client/typescript/reducer.test.ts` (add other touched `*.test.ts(x)` paths as needed).
- **vitest, mobile/mobile-native files**: from `mobile-native`, e.g. `npx vitest run --root .. --config
  mobile-native/vitest.config.mts mobile/src/conversation/project.test.ts mobile/src/state/conversation.test.ts`
  (shared `mobile/src` tests) and bare `npx vitest run` (mobile-native's own suite, ~84 files / 800+ tests).
- **typecheck, web**: from `cmd/evener-hub/frontend`, `npm run typecheck`.
- **typecheck, native**: from `mobile-native`, `npm run check`. Required whenever a package port/exported
  type/`index.ts` changes (several items above touch `index.ts` or exported types).
- **biome, package**: from `cmd/evener-hub/frontend`, `npx biome ci ../../../appwire-client/typescript`. NEVER
  `npx biome --write`/`check` over `mobile/` or `mobile-native/` — neither has a biome config, a bare run
  retabs whole files. `npx biome check --write <exact file>` from `cmd/evener-hub/frontend` is safe for a
  single `appwire-client/typescript` file.
- **lint, web**: from `cmd/evener-hub/frontend`, `npm run lint`.
- **`make lint-package-imports`**: from the repo root. Run whenever any package file changes.
- **`make test-api-package`**: from the repo root. Run whenever `index.ts` / `tsconfig.build.json` / the
  qualifier script changes. Falsify by removing the new export line and confirming `mobile-native npm run
  check` (or the frontend typecheck) fails at the importing consumer's line, then restore.
- **Falsification pattern for every item above**: revert only the production file(s) for that item, run the
  test the finding names (or the nearest existing test), confirm it fails or the invariant breaks, restore, run
  clean. For pure refactors (dead-code deletion, comment fixes) with no behavior change, running the existing
  suite green is the falsification — no new test needed.
- **Go/root gates**: not touched by any file in this list; no Go files in scope.
- Not run locally: `make test-web`, `make test-native`, `make test-web-browser`, `go test -race`, `make lint`,
  `secret-scan` — CI is the full matrix (Jesse, 2026-09-17: "lean on the CI runner").

## Process notes for whoever picks this up

- Every rebase in this stack was verified with a normalized diff (`sed -E 's/^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@/@@ HUNK @@/'`
  on both sides) before trusting "content identical" — line-number-only hunk-header drift from unrelated
  upstream commits is expected and not a real conflict.
- One rebase this lane did (`git rebase --onto <new> <old> <branch>`) used the WRONG `<old>` argument once (the
  branch's own current head instead of its predecessor's prior tip), which silently dropped a whole commit with
  no error — caught immediately via `git log` before pushing, recovered via `git reflog` + the still-correct
  `origin/<branch>` ref. Always sanity-check `git log --oneline -5 <branch>` after a rebase before trusting it.
- `joinWarningParts` and `boundedContent`/`boundedPrefix` are already merged (via #1894 and #1895's round-3
  commit) — items #8, #9, #12 above build ON TOP of those, not instead of them.
