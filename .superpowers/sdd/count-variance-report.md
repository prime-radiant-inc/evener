# Test-count variance investigation

**Verdict: DETERMINISTIC — expansion fully explains the reviewers' inferred-baseline discrepancy.**

There is no runtime nondeterminism in the webui vitest suite at `0713970d8`. The 3217/3220/3226
baseline disagreement is a hand-counting artifact: two design-system contract tests
(`token-contract.test.ts`, `requireclass-contract.test.ts`) dynamically generate one `test()`
per file discovered via a `node:fs.readdirSync` walk of `src/` at module-load time. Adding a new
CSS module or a new CSS-module-importing source file anywhere in the tree silently adds cases to
these two files — which never appear in the feature diff being hand-counted, since their own
source text doesn't change. A diff-based hand count cannot see this by construction, regardless
of how carefully `.each()`-style expansion *within* the diff's own files is counted.

## Step 1 — determinism

`npx vitest run`, three full sequential runs from `cmd/serf-hub/frontend` at `0713970d8`:

| Run | Test Files | Tests | Result |
|---|---|---|---|
| 1 | 237 passed (237) | 3400 passed (3400) | exit 0 |
| 2 | 237 passed (237) | 3400 passed (3400) | exit 0 |
| 3 | 237 passed (237) | 3400 passed (3400) | exit 0 |

Identical across all three runs. No skips, no todos, no hidden failures (checked all three logs
for `skip`/`todo`/`fail` outside the `0 failed` summary line). **Runtime determinism holds.**

## Ground truth baseline

Rather than trust any report's self-measured baseline, checked out the shared integration point
`e3b9c188c` directly (fresh `git worktree`, symlinked `node_modules` — `package.json` /
`package-lock.json` are byte-identical from `e3b9c188c` to HEAD, so no reinstall needed) and ran
the full suite:

```
Test Files  223 passed (223)
     Tests  3217 passed (3217)
```

**3217 is the correct baseline.** It is not a stale or wrong measurement — every one of the four
wave-8 stream reports (T2/T5/T6/T3) independently claimed this exact same baseline because they
all branched from this exact same commit, and the number is real. This means the reviewers'
*inferred* baselines (3220 for T5, 3226 for T3 — each computed as "my branch's measured total
minus my hand-counted new-test count") are the ones in error, not 3217.

## Step 2 — expansion accounting

For each disputed diff, got per-file runtime counts by running the full suite with
`--reporter=json` at both the shared base (`e3b9c188c`) and the stream tip, then diffing every
file's case count (not just the files the code diff touches).

### T5 (`e3b9c188c..ee5d09823`) — claimed +43, reviewer hand-count +40, gap +3

Files touched by the diff (all four accounted for exactly, matching the reviewer's hand count):

| File | Before | After | Delta |
|---|---|---|---|
| `panes/doc/DocPane.test.tsx` | — (new) | 13 | +13 |
| `panes/doc/docFile.test.ts` | — (new) | 15 | +15 |
| `panes/doc/register.test.ts` | — (new) | 2 | +2 |
| `protocol/docContent.test.ts` | 3 | 13 | +10 |
| **in-diff subtotal** | | | **+40** |

Files **not present in the diff at all**, whose case counts still changed:

| File | Before | After | Delta |
|---|---|---|---|
| `styles/requireclass-contract.test.ts` | 182 | 183 | +1 |
| `styles/token-contract.test.ts` | 272 | 274 | +2 |
| **out-of-diff subtotal** | | | **+3** |

**Total: 40 + 3 = 43 — exact match to the report's claimed delta and to `3260 − 3217`.**

Mechanism, confirmed by reading both contract files: `token-contract.test.ts` walks every `.css`
file under `src/` (`walkCssFiles`, recursive `readdirSync`) into `OTHER_STYLESHEETS`, then runs
**two** `for (const [path, text] of OTHER_STYLESHEETS) { test(...) }` loops — one test per
stylesheet file, per loop. `requireclass-contract.test.ts` walks every `.ts`/`.tsx` file under
`src/` into `SOURCE_PATHS`, then runs `for (const absPath of SOURCE_PATHS) { ...; test(...) }` —
one test per source file that imports a CSS module. T5 added exactly one new CSS file
(`docpane.module.css`, → +2 in token-contract, one test per loop) and exactly one new
CSS-module-importing source file (`DocPane.tsx`, → +1 in requireclass-contract). Both numbers
match the observed deltas exactly.

### T3 (`e3b9c188c..578c9dd2b`) — claimed +76, reviewer hand-count +67, gap +9

(`578c9dd2b` is a report-only commit over `b50fbe211`, verified via `git diff --stat`: one file,
the report markdown, zero code.)

Files touched by the diff (matches the reviewer's hand count exactly, including the two
pre-existing files they explicitly enumerated):

| File | Before | After | Delta |
|---|---|---|---|
| `panes/session/transcript/ToolCallItem.test.tsx` | 17 | 28 | +11 |
| `.../messages/SteeringItem.test.tsx` | 16 | 21 | +5 |
| `.../TurnFailureEndCap.test.tsx` | — (new) | 9 | +9 |
| `.../messages/NotificationCard.test.tsx` | — (new) | 9 | +9 |
| `.../messages/steeringClassify.test.ts` | — (new) | 16 | +16 |
| `.../tools/taskCard.test.tsx` | — (new) | 9 | +9 |
| `.../turnFailure.test.ts` | — (new) | 8 | +8 |
| **in-diff subtotal** | | | **+67** |

(`tools/index.test.ts`'s 1-line diff hunk adds no new `test()`, consistent with 0 delta.)

Files **not present in the diff at all**:

| File | Before | After | Delta |
|---|---|---|---|
| `styles/requireclass-contract.test.ts` | 182 | 185 | +3 |
| `styles/token-contract.test.ts` | 272 | 278 | +6 |
| **out-of-diff subtotal** | | | **+9** |

**Total: 67 + 9 = 76 — exact match to the report's claimed delta and to `3293 − 3217`.**

Same mechanism, same exact arithmetic: T3 added three new CSS files (`notificationcard.module.css`,
`taskcard.module.css`, `turnfailure.module.css` — `steeringitem.module.css` was a modification of
an existing file, not a new one, so it contributes no *new* walk entry) → 3 files × 2 loops = +6 in
token-contract; and three new CSS-module-importing source files (`TurnFailureEndCap.tsx`,
`NotificationCard.tsx`, `taskCard.tsx`) → +3 in requireclass-contract.

### Why the reviewers' hand counts were not "wrong," just structurally blind

Both reviewers' arithmetic on the files they looked at was accurate — 40 and 67 are exactly right
for the files present in each diff. Their `.each()`/dynamic-test check (T3's reviewer explicitly
verified "no `.each`/dynamic tests are affected by T3's changes") was also correct as far as it
went: neither diff touches a file using vitest's literal `.each()` API in a way that would expand
under this change. What both missed is a different, subtler flavor of the same category the
controller's hypothesis named: parameterized/dynamic **registration**, hand-rolled as a
`for (...) { test(...) }` loop over a `readdirSync` walk, living in two pre-existing contract test
files that **never appear in the diff being hand-counted** because their own source text is
untouched. A `git diff`-scoped hand count is structurally incapable of seeing this — it would
require independently reasoning about every dynamically-driven test file in the whole suite, not
just the files a diff touches.

## Step 3 — not applicable

Step 1 showed no variation across three runs, so the conditional bisection step was not invoked.

## Fix

None required. This is not a test-hygiene defect — the contract tests are working exactly as
designed (a real completeness guard that should scale with the file population), and the suite is
fully deterministic. No code changes were made in this worktree.

## Bottom line

The controller's hypothesis is **correct**: there is no runtime nondeterminism, and the four
merges' measured deltas compose exactly to the measured HEAD total (`3217 + 43 + 41 + 23 + 76 =
3400`, confirmed live at `237 files / 3400 tests` three times). The reviewers' inferred baselines
(3220, 3226) were artifacts of hand-counting a diff against two contract test files whose case
counts are a function of the whole tree's file population, not of the diff itself.
