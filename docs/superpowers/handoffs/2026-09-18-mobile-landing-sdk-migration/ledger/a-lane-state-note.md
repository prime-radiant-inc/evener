# Lane state note — #1731 piece 2/3, piece 3/3, B (retiring)

Worktree: `.claude/worktrees/sdk-d21-native-model` (absolute path from repo root). Retiring after this note per the coordinator's instruction — do not resume this lane without new direction.

## Branches and heads (all pushed, local == remote, verified byte-identical own-diff at every rebase)

| PR | Branch | Head (full 40-char SHA) |
|---|---|---|
| #1859 (piece 2/3) | `claude/1731-piece2-image-warning` | `0381d57b7777ff7383d986a59e346a9d80996ed7` |
| #1862 (piece 3/3) | `claude/1731-piece3-ask-resolution` | `3af1921280bc4aa0ced610a67467091a8e949286` |
| #1732 (B) | `claude/c2b-b-warning-and-reasoning-web` | `913bbc8e82d1ee3bdc84c796e2a3aa35f48b8a26` |

Stack order: #1859 → #1862 (rebased on #1859) → #1732 (rebased on #1862). All three PRs keep base `main`.

## What each PR still owes

**#1859 — CI green, 6 review rounds done, simplify done.** No known open findings as of round 6 (0381d57). Should be mergeable once CI confirms green on the pushed head — I did not poll/wait on CI per the no-`gh pr view`-polling rule; the coordinator should check `gh api repos/prime-radiant-inc/evener/pulls/1859` before merging.

**#1862 — CI status unknown (pushed, not polled). Past 5 review rounds; per the "five rounds means decompose" rule, this PR is NOT going to get a 7th round from further patching. A fresh lane should decompose it into smaller PRs.** See "#1862 open findings" below for what that lane inherits, and "Mechanism split" for a proposed decomposition.

**#1732 — CI status unknown (pushed, not polled). Own diff is stable (WarningItem.tsx + WarningItem.test.tsx only) and has been byte-identical across the last 3 rebases. No open findings on its own panel as of round 6.** Should be mergeable once #1862 lands (or gets split — see below; #1732 would need to retarget onto whichever #1862 successor PR carries the warning-fold work it depends on).

## #1862 open findings (NOT fixed this round — inherit verbatim)

Per the coordinator's scope trim: I fixed only the `Object.entries` lazy-iteration item (reducer.ts, `prunedForStringify`) from the round-5/B panels this round. The rest of round 5's panel (`.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/reviews/raw/1862-99bde6efb.md` in this worktree — a High and three Mediums) and two items named directly by the coordinator are still open on #1862's current head (`3af1921280bc4aa0ced610a67467091a8e949286`):

1. **`appwire-client/typescript/reducer.ts:1201` (current head)** — `warningMessage`/`hasWarningText` call `.trim()` on unbounded wire strings (`params.message`, `params.warning`, the nested `warning.message`) BEFORE `boundedCodePoints` ever runs (that only applies later, inside `foldWarningParams`'s `text: boundedCodePoints(text)`). A multi-MB `message` string gets `.trim()`'d (an O(length) allocation) unconditionally. Fix direction: either check non-blank without copying (e.g. a bounded prefix check, or a regex anchored test that doesn't allocate a full trimmed copy) or bound the string first, then trim.

2. **`mobile/src/conversation/project.ts:533` (current head)** — `item.text || warningFallbackText(item) || item.output` drops the hint whenever a message is present, while the web `WarningItem.tsx` and the mobile LIVE row (`conversation.ts`'s `case "warning"`) both compose message + hint together (`[folded.text, folded.hint].filter(hasWarningText).join(" — ")`). This is the canonical-projector path only; the live path already got this fix in an earlier round. Fix direction: same composition as the live path, ideally shared (see finding #7 in the simplify review, `warningRowText(fold)` beside `foldWarningParams`).

3. **`mobile/src/state/conversation.ts:2861` (current head)** — the live warning row's id (`` `warning:${title}:${++liveNoticeSerial}` ``) embeds the sanitized `title`, which can be up to 2000 code points (`boundedCodePoints`'s bound) when a real title is present — better than embedding `folded.text` (which round 4 already fixed), but still unbounded-by-a-small-constant. Fix direction: `` `warning:${++liveNoticeSerial}` `` (the serial alone is already unique) or a short fixed-length hash/prefix of the title, not the title itself. Add a test with an oversized (near-2000-char) title asserting the id itself stays short.

4. **`mobile/src/conversation/project.ts:416` area — active reasoning precedence.** This was ALSO named in the coordinator's #1862-scope message, but I measured and fixed it on **#1859** this round (commit `0381d57`, not #1862) — it's visible in #1862's stacked diff only because #1862 sits on top of #1859. No separate action needed on #1862 itself; the fix is inherited via the stack. Recorded here for the decomposition lane's context only, in case they re-audit the stacked diff and wonder why this line looks different from the round-5 review snapshot.

Read the round-5 raw panel (`reviews/raw/1862-99bde6efb.md`) in full for the High and the other two Mediums I have not triaged in detail — I did not read past what the coordinator quoted, since fixing them was explicitly out of scope for this round.

## Mechanism split (proposed decomposition for #1862)

On #1862's current head, the accumulated work naturally separates into four mechanisms. Suggested PR boundaries if a fresh lane decomposes:

**(a) askPending gate + human-note exclusion in `deriveAskQuestions`**
Files: `appwire-client/typescript/deriveAskQuestions.ts`, `deriveAskQuestions.test.ts`, `askDock.test.ts`, `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`, `.../composer/Composer.integration.test.tsx`, `.../composer/Composer.test.tsx`, `.../composer/askDock/AskDock.test.tsx`, `.../composer/askDock/askDockStore.test.ts`, `cmd/evener-hub/frontend/src/dev/surface-sections/composer.tsx`.
Self-contained; no dependency on (b)/(c)/(d). Could land first/independently.

**(b) `prunedForStringify` bounded walk (depth/array/key-count/key-length/total-node caps, lazy `for...in` enumeration)**
Files: `appwire-client/typescript/reducer.ts` (the `prunedForStringify` function and its constants), `reducer.test.ts` (the many-key / oversized-key-name / Proxy-get-count tests).
Depends on nothing; feeds into (c) (`rawWarningFrame` calls it).

**(c) Warning string bounds (`boundedCodePoints`, `rawWarningFrame`, `foldWarningParams`, the still-open `.trim()`-before-bound gap)**
Files: `appwire-client/typescript/reducer.ts` (`warningMessage`, `hasWarningText`, `rawWarningFrame`, `boundedCodePoints`, `foldWarningParams`, the `WarningFold` interface), `reducer.test.ts`. Consumers that read the fold: `cmd/evener-hub/frontend/.../WarningItem.tsx` + test, `appwire-client/typescript/transcriptProjector.ts` + test.
Depends on (b) (rawWarningFrame calls prunedForStringify). Open finding #1 above (trim-before-bound) belongs here.

**(d) Mobile canonical warning composition + live-row ids**
Files: `mobile/src/conversation/project.ts` (`warningFallbackText`, the `item.text || warningFallbackText(item) || item.output` line), `mobile/src/state/conversation.ts` (the live `case "warning"`: uses `foldWarningParams` directly since the simplify round, the id construction). Also where the image-folding work lives (`findFoldedItem`'s caller, `itemAttachments`) if that needs its own slice — image folding and warning folding are currently interleaved in this file's history; a decomposition lane may want to check whether they can split into separate PRs or must land together (they touch overlapping line ranges in `conversation.ts`).
Depends on (c) (reads `foldWarningParams`/`hasWarningText`). Open findings #2 and #3 above belong here.

Ask-resolution (a) is the easiest standalone extraction. (b)→(c)→(d) is a dependency chain; landing them as three stacked PRs (mirroring how this lane already stacked piece2→piece3→B) is probably simpler than trying to fully untangle (c)/(d) from the image-folding work already merged in #1859.

## The 11 /simplify follow-ups (NOT fixed this round — from `.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/reviews/1859-simplify.md`)

The 3 "fix before merge" items ARE fixed (this round, commit `3a4caa73f` on #1859). These 11 "follow-up" items are not, and were never in scope for this lane to fix — listed here for whoever picks up c-2b or the next SDK-migration round:

4. `mobile/src/state/conversation.ts` (`findFoldedItem`'s caller) — two full model scans per live item frame (no `turnId` hint, unlike the reducer's own `findItemTurnId`). Simplify direction: mirror the hint order, or have the package hand the folded item back so the store never searches.
5. `mobile/src/conversation/project.ts:415-416` + `conversation.ts` — three encodings of "don't lose the reasoning output" (canonical projector's fallback, the live path's `projectSingleItem`, and `preservesReasoningOutput`'s patch-up). Simplify direction (c-2b): project the live row from the folded item via the shared `projectItem`, retiring the other two.
6. `appwire-client/typescript/transcriptProjector.ts` + `WarningItem.tsx` — the warning fold's non-blank-string-or-absent guarantee is re-derived at every reader (4 copies). Simplify direction: state the invariant on `ItemModel.warning`'s declaration and let readers trust the type — a judgement call, not mechanical.
7. `mobile/src/conversation/project.ts` (`warningFallbackText`) vs `conversation.ts` (live row) — two different compositions of message+hint for the same frame. Simplify direction: a shared `warningRowText(fold)` beside `foldWarningParams`. (This is also open finding #2 above — fixing it fixes both.)
8. `cmd/evener-hub/frontend/.../WarningItem.tsx` — the `hasWarningText` predicate's narrowing is discarded (re-reads the optional chain in the true branch); `hint !== undefined` diverges from the file's own `!!` style two lines up.
9. `appwire-client/typescript/reducer.test.ts` — six copies of the same 18-line warning-test preamble (`testHydrate()` + `turn/started` + `warning` notification). Simplify direction: local `hydrateWithActiveTurn`/`applyWarning` helpers, ~80 lines saved.
10. `appwire-client/typescript/reducer.test.ts` — a test that admits (in its own comment) it duplicates the `test.each` row above it (the routed `{"warning":42}` case). Simplify direction: delete it, move the provenance comment onto the table row.
11. `appwire-client/typescript/reducer.test.ts` — `const MAX_CHARS = 2000; // mirrors reducer.ts's RAW_WARNING_FRAME_MAX_CHARS`, a mirrored literal in the same package. Simplify direction: `export const RAW_WARNING_FRAME_MAX_CHARS` (module-level, not index.ts) and import it.
12. `mobile/src/state/conversation.test.ts` — six copies of the failure-row lookup + narrowing. Simplify direction: a local `warningRow(store)` helper.
13. `mobile-native/src/liveImages.test.ts` — two 30-line tests differing only in the published `images` and expected `src`. Simplify direction: a `test.each`-style table.
14. `mobile/src/conversation/project.test.ts` — repeated 15-line `applyNotification(... as AnyNotification)` scaffolding in the reasoning tests. Simplify direction: a local `notify(model, method, params, at)` helper.

## Gates cheat-sheet (targeted, foreground only — never the full local suite; CI is the full matrix)

Run from `.claude/worktrees/sdk-d21-native-model`:

- **vitest, package/web files**: from `cmd/evener-hub/frontend`, e.g. `npx vitest run ../../../appwire-client/typescript/reducer.test.ts` (add other touched `*.test.ts(x)` paths as needed).
- **vitest, mobile/mobile-native files**: from `mobile-native`, e.g. `npx vitest run --root .. --config mobile-native/vitest.config.mts mobile/src` (shared `mobile/src` tests) and bare `npx vitest run` (mobile-native's own suite, currently 84 files / 800 tests).
- **typecheck, web**: from `cmd/evener-hub/frontend`, `npm run typecheck`.
- **typecheck, native**: from `mobile-native`, `npm run check`. Required whenever a package port/exported type/index.ts changes.
- **biome, package**: from `cmd/evener-hub/frontend`, `npx biome ci ../../../appwire-client/typescript`. **NEVER** `npx biome --write` (or even `check`) over `mobile/` or `mobile-native/` — neither has a biome config, so a bare run retabs whole files (lesson learned the hard way this session: two files had to be `git checkout --`-reverted after a `--write` reformatted ~1800 unrelated lines). If you need a specific line's formatting fixed in `appwire-client/typescript` (which DOES have a config), `npx biome check --write <exact file>` from `cmd/evener-hub/frontend` is safe.
- **lint, web**: from `cmd/evener-hub/frontend`, `npm run lint` (runs biome over `src` + the package; still package-config-scoped, safe).
- **`make lint-package-imports`**: from the repo root (this worktree). Run whenever any package file changes.
- **`make test-api-package`**: from the repo root. Run whenever `index.ts` / `tsconfig.build.json` / the qualifier script changes (adds/removes/moves a package export).
- **Go/root gates**: not touched this lane; no Go files in any of the three PRs' diffs.

Falsification pattern used throughout: write the test, confirm it fails (or, for a fix already made, temporarily revert the fix — not the test — and confirm red), then restore and confirm green. Never use `git checkout --` to restore a falsification edit if the file has OTHER uncommitted changes you need to keep — edit it back by hand or use `git diff | git apply -R` on an isolated diff.

## Notable process lessons from this lane (for whoever picks up work in this tree)

- **Never `npx biome --write`/`check --write` over `mobile/` or `mobile-native/`.** No config there; it reformats the whole file. Confirmed twice this session.
- **`git checkout -- <file>` during a multi-file falsification wipes ALL uncommitted changes to that file**, not just the falsified line — lost and had to redo 3 files' worth of edits once this round from memory. Prefer scoped `Edit`-tool reverts over `git checkout` when other real changes are staged in the same file.
- **A single bash tool call containing `rm` anywhere gets hard-blocked** (sandbox policy: "rm -rf requires explicit human authorization"), even for a plain `rm /tmp/foo.ts` with no `-rf` and even when chained after other commands that would otherwise succeed — the WHOLE call is blocked before any line runs. Don't chain `rm` with other work in one call; expect to just leave stray `/tmp` scratch files behind (harmless, outside the repo).
- **Rebasing WarningItem.tsx repeatedly conflicts with B's own independent fix of the same class of bug** (non-string title/hint guards) — each round's conflict was resolved by keeping the upstream (piece 2/3) fix and folding B's own additional contribution (the whitespace-message guard) on top using the shared `hasWarningText`, never picking one side wholesale. If #1732 needs another rebase after decomposition, expect this pattern again only if the successor PR touches `WarningItem.tsx` again.
- **`38cac024e` on branch `claude/sdk-d23c2b-rows-projection`** (not yet merged to main) already fixed the exact "absent/empty input images = unchanged, not removed" rule for `mobile-native/src/liveImages.test.ts` that this lane's round-3 fix needed — worth checking that branch before re-deriving similar mobile-native test fixtures in future rounds, since duplicate work across parallel lanes is exactly what happened here.
