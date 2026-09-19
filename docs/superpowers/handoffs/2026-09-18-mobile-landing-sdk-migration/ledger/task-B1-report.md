# Task B1 — One settled-item failure predicate

- **Status:** COMPLETE
- **SHA:** `f00c5a466b9d8863f2457e88b4b2cc4c034b479b` on `claude/sdk-b1-item-failure`, stacked on A1's `30b8c585e`. Not pushed.

## Were the two predicates identical? Yes.

- Web `cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts:140-150` (`isNonZeroExit`,
  `hasFailureStatus`, `hasItemFailure`) and native `mobile/src/conversation/project.ts:114-118`
  (`toolCallFailed`): same three signals (non-blank trimmed `error`, status `failed`/`interrupted`,
  `typeof exitCode === "number" && exitCode !== 0`), same order. `||` chain vs early returns is the
  only textual difference.
- Field types match: `ItemModel` (`protocol/model.ts:94,110,113`) and wire `ThreadItem`
  (`protocol/types.gen.ts:1710,1712,1717`) both declare `error?: string`, `status?: string`,
  `exitCode?: number`, so one structural input type serves both.
- `isInProgressStatus` (native `project.ts:124`) equals the web's two `item.status === "inProgress"` /
  `turn.status === "inProgress"` literals at `projector.ts:155,170`.

## RED / GREEN

- RED: `npx vitest run src/protocol/itemFailure.test.ts` → 1 failed file, "Failed to resolve import ./itemFailure".
- GREEN: same command after writing `itemFailure.ts` → 17 passed.

## Deleted

- Web: `isNonZeroExit`, `hasFailureStatus`, `hasItemFailure` in `transcriptDisplay/projector.ts`.
- Native: `toolCallFailed`, `isInProgressStatus` in `mobile/src/conversation/project.ts`.
- No local copy remains on either side; both import `protocol/itemFailure.ts`.

## Gates

`make test-web` PASS (typecheck/test/lint) · `make test-native` PASS (777 shared tests, tsc) ·
`make test-api-package` PASS · `make test-web-browser` PASS (all five guards).
`projector.test.ts` (35) and `project.test.ts` unchanged — zero edits, both green.

## Lines

7 files, +159 / -42. New module 45 lines, new test 89 lines.

## Concerns

- A third, DIFFERENT failure predicate exists: `toolCallFailed` in
  `cmd/evener-hub/frontend/src/panes/session/transcript/toolRenderers.ts:189`. It drops `trim()`,
  drops `interrupted`, drops `exitCode`, and adds a tool-descriptor hook. Not in the B1 row and not
  mentioned anywhere in the plan or inventory. Needs a decision (issue + its own row) — I did not
  touch it.
- `isInProgressStatus` now rides in the same module. `projector.ts`'s two `"inProgress"` literals were
  switched to it so the shipped module has two real consumers (plan rule 7), a 2-line behavior-preserving
  change beyond the row's literal deletion list.
- Do not run Biome over `mobile/src/**`: `mobile-native` has no Biome config, so a bare run reformats the
  whole file to tabs. I reverted one such accident; the committed native diff is 7 insertions / 26 deletions.

## Follow-up: the third predicate (#1190, B1b) — `d1541bd5b`, merged at `5d62e86d9`

`panes/session/transcript/toolRenderers.ts:189` `toolCallFailed` now reads
`hasItemFailure(item) || (descriptor.failed?.(item) ?? false)`; the descriptor hook is untouched.
No existing assertion pinned the old behavior (nothing in the repo tested `toolCallFailed`,
`interrupted`, or a whitespace-only error in that directory).

RED at `f00c5a466`: three new renderer tests in `ToolCallItem.test.tsx`, all failing —
interrupted `expected null to be 'true'`, whitespace-only error `expected 'true' to be null`,
nonzero exit `expected null to be 'true'`. GREEN after: 81 files / 1922 tests in
`src/panes/session/transcript/`.

**Behavior change (web UI, user-visible):** an interrupted tool call now renders with the
failure state and a nonzero exit code now marks any tool's row failed, not only one whose
descriptor declares a `failed()` hook; and a tool call whose `error` is whitespace only now
renders WITHOUT the failure state, where it rendered as failed before.

**The coordinator's whitespace premise was inverted.** The ruling said the old registry predicate
left a whitespace-only error unmarked while the projector called it failed. It is the other way
round: the shared predicate trims (`error.trim() !== ""`, `itemFailure.ts`) so a blank error is NOT
a failure, while the old registry predicate compared against `""` without trimming and DID mark it.
The mechanism in the ruling is unchanged; only that sentence's direction is. The RED test asserts
the true post-change behavior.

Gates after the fix: `make test-web` PASS · `make test-web-browser` PASS (five guards) ·
`make test-api-package` PASS. Biome run on the two touched web files only.
Merged `origin/main` (`--no-ff`, no conflicts), re-ran `make test-web`: PASS. Not pushed.

Residual, not fixed: `ToolCallItem.tsx:211` `hasErrorText` still uses `item.error !== ""` with no
trim, so a whitespace-only error now renders its (blank) error text on a row that is no longer
marked failed. Cosmetic, out of this row's scope.

## Round 3: blank error block (#1197) — `6dfde0b22`, merged at `a5639624b`

`hasErrorText(item)` joins `hasItemFailure` in `protocol/itemFailure.ts` and `hasItemFailure` now
calls it, so "non-blank error" is encoded once rather than twice. Shipped: `index.ts` export,
qualification-runner smoke call (`hasErrorText({ error: "  " })` is false), three direct cases in
`itemFailure.test.ts`. `ToolCallItem.tsx:211` reads it instead of comparing against `""`.

RED at `5d62e86d9`: new renderer test "a whitespace-only error renders no error block at all" —
`expected <div class="_error_5064a0"></div> to be null`. GREEN after: 83 files / 1978 tests across
the transcript dir, `itemFailure.test.ts` and `projector.test.ts`.

Gates: `make test-web` PASS · `make test-web-browser` PASS (five guards) · `make test-api-package`
PASS. Biome on the five touched web/protocol files only. Merged `origin/main` (`--no-ff`, no
conflicts), re-ran `make test-web`: PASS. Not pushed.

## Round 4: fold rule + comment — `6b42d66d2`, merged at `1766baf29`

`toolRuns.ts:52` `foldable` now bails on `hasItemFailure(item)` instead of `item.error !== ""`.
`hasErrorText` is deliberately NOT part of it: the file header says a run breaks on "a failure",
not on visible error text, and under the shared predicate every non-blank error IS a failure while
a blank one is neither — adding it would be dead weight. No `toolRuns.test.ts` case pinned the old
behavior (the file had no `error` or `exitCode` fixture at all).

RED at `b4e35df0d`: nonzero-exit call `expected [ 'run' ] to deeply equal [ 'item', 'item', 'item',
'item' ]`; whitespace-only error `expected [ 'item', 'item', 'item' ] to deeply equal [ 'run' ]`.
GREEN after: 81 files / 1925 tests in the transcript dir.

Also corrected `ToolCallItem.tsx:208`'s comment: the generic half covers error text, status and exit
code; the descriptor's half is for output-shape-only failures.

Gates: `make test-web` PASS · `make test-web-browser` PASS (five guards). Biome on the three touched
web files, no fixes needed. Merged `origin/main` (`9db3a1e99`, `--no-ff`, no conflicts), re-ran
`make test-web`: PASS. Not pushed.
