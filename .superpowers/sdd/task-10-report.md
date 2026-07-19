# Task 10 report — Retired treatments: ALL-CAPS labels out, radius literals snapped to the two tokens

## Summary

Removed all 13 `text-transform: uppercase` sites from `cmd/serf-hub/assets/style.css`
(with their accompanying `letter-spacing` declarations and mono→sans on the human
labels), and snapped every literal `border-radius` value to `var(--radius-md)` /
`var(--radius-pill)` / `0`, keeping the documented 30% squircle exception. New
contract test `cmd/serf-hub/jstest/test-retired-treatments-css.js` written first,
watched fail, then pass.

## The 13 ALL-CAPS sites (final enumeration, post-edit line numbers shifted)

| # | Selector | Pre-edit line | Action |
|---|----------|---------------|--------|
| 1 | `section.panel h2` | 350 | removed `text-transform: uppercase` + `letter-spacing: 0.05em`; `font-family: var(--font-mono)` → `var(--font-sans)` |
| 2 | `.msg .role` | 436 | removed `text-transform: uppercase` (no letter-spacing, no font-family on the rule) |
| 3 | `.ask-option-tag` | 1753 | removed `text-transform: uppercase` + `letter-spacing: 0.03em` |
| 4 | `.user-message-tag` | 2300 | removed `text-transform: uppercase` + `letter-spacing: 0.04em` |
| 5 | `.task-list-body .task-type` | 2835 | removed uppercase + `letter-spacing: 0.04em`; mono→sans |
| 6 | `.spawn-recent-header` | 3485 | removed uppercase + `letter-spacing: 0.16em`; mono→sans |
| 7 | `.chip-picker-group` | 3504 | removed uppercase + `letter-spacing: 0.12em`; mono→sans |
| 8 | `.search-section-header` | 3593 | removed uppercase + `letter-spacing: 0.16em`; mono→sans (colors/size untouched — already canonical `--ink-2`) |
| 9 | `.settings-h3` | 3638 | removed uppercase + `letter-spacing: 0.16em`; mono→sans |
| 10 | `.settings-table .val-toggle .state` | 3884 | removed uppercase + `letter-spacing: 0.08em`; mono→sans |
| 11 | `.settings-table .row.section-header dd` | 3905 | removed uppercase + `letter-spacing: 0.14em`; mono→sans; updated the now-stale `.help`-override comment (it referenced the retired "uppercase/mono header treatment") |
| 12 | `.tasks-list .task-type-pill` | 4252 | removed uppercase + `letter-spacing: 0.04em`; mono→sans |
| 13 | `.credentials-type-name` | 4959 | removed uppercase + `letter-spacing: 0.14em`; mono→sans |

All 13 are human labels; none are machine text (paths/commands/code keep mono
elsewhere, e.g. `.msg code`, `.settings-help code`, `kbd`).

Letter-spacing sites NOT tied to an uppercase rule were left alone per the brief:
`.tier-header .count` (0), `.project-name` (0), 1706 (0.02em, awaiting-question
header), `.settings-collection-count` (0.04em), ~5034 (0.12em, mono credential
code — machine text), `.spawn-prompt` / 2383 / 5581 (negative tracking on large
sans display type).

## Radius decisions

| Pre-edit line | Context | Before | After | Rationale |
|---------------|---------|--------|-------|-----------|
| 872 | `.subagent-parent-key` | `3px` | `var(--radius-md)` | rounds a rectangle |
| 1152 | `.status-dot[data-state="awaiting"]` | `1px` | `0` | intentionally square (rotated into the needs-you diamond); `--radius-md` (5px) on an ~8px dot would read as a disc. Comment added. |
| 1156 | `.status-dot[data-state="warning"]` | `30%` | kept `30%` | documented squircle exception (its own shape); existing comment retained |
| 1161 | `.status-dot[data-state="idle"/"ended"]` | `1px` | `0` | intentionally square per the Task-8 shape legend ("idle/ended = neutral SQUARE"). Comment updated. |
| 1523 | archive spinner | `50%` | `var(--radius-pill)` | circle |
| 3815 | `.val-radio input[type="radio"]` | `50%` | `var(--radius-pill)` | circle |
| 3868 | `.val-toggle` thumb | `50%` | `var(--radius-pill)` | circle |
| 4741 | mobile drawer sheet | `16px 16px 0 0` | `var(--radius-md) var(--radius-md) 0 0` | brief's mapping |
| 5456 | `.skeleton.dot` | `50%` | `var(--radius-pill)` | circle |
| 5529 | `.cold-start-dot` | `50%` | `var(--radius-pill)` | circle |
| 5591 | `.empty-state-workspace .btn-ghost kbd` | `4px` | `var(--radius-md)` | rounds a rectangle |

Pre-existing `0` values (status-dot errored triangle, 2304, 3510, 4523, 4655,
4801) kept as-is. No new exceptions were needed beyond the documented 30%
squircle, so the test's allow-list covers exactly: `var(--radius-md|pill)`, `0`,
`30%`.

## Deviations from the brief

1. **Contract-test zero-regex fix (bug in the brief's snippet).** The brief's
   allow-check `/^border-radius:\s*0[;\s]/` can never match: the capture regex
   `[^;]*` stops before the `;`, so a plain `0` literal has no trailing
   terminator character in the matched string — every legitimate
   `border-radius: 0` would be flagged as bad. Fixed to
   `/^border-radius:\s*0(\s+0)*\s*$/` (anchored on end-of-string), with a
   comment in the test explaining why. Behavior otherwise identical to the
   brief's intent ("keep 0 values").
2. **Stale comment update at `.settings-table .row.section-header .help`**
   (was: "must not inherit the uppercase/mono header treatment"). The reset
   declarations were kept (harmless, and `font-weight`/`font-size`/`color`
   still do work); only the comment was corrected.
3. **Brief line numbers were stale** (as the integration notes warned). Used
   `grep -n 'text-transform'` / `grep -nE 'border-radius:\s*[0-9]'` on the live
   file; final enumeration above uses the actual pre-edit line numbers.

## Existing-test migration

Grep of `cmd/serf-hub/jstest` for `uppercase` / `letter-spacing` /
`border-radius` found only **negative** assertions (must-not-be-uppercase:
test-color-system-css.js:142/147, test-mobile-css.js:203) and shape-difference
assertions (test-style-colorblind-shapes.js compares awaiting vs warning shape
rules, which remain distinct: `0` + rotate(45deg) vs `30%` squircle). No
expectation migration was needed; full suite confirms.

## Observation (out of scope, not changed)

`.val-toggle .state` label *content* is the literal string `"ON"`/`"OFF"` written
by JS (`launchconfig.js:99,550`, `settings-display.js:25`, `notifications.js:320`),
so that one label still renders in caps via its text content rather than via a
CSS treatment. The task scope was the stylesheet; changing JS copy was not
requested and no jstest asserts on those strings. Flagging for the plan owner.

## Test commands + results

1. `cd cmd/serf-hub/jstest && node test-retired-treatments-css.js`
   - Before implementation: `FAIL: no ALL-CAPS label treatment anywhere` (exit 1) ✓ expected
   - After implementation: `ok retired treatments` (exit 0)
2. `cd cmd/serf-hub/jstest && ./run-all.sh` → `jstest: all tests passed` (exit 0;
   run-all.sh globs `test-*.js`, so the new contract test is included in the suite)
3. `make build-hub` → exit 0
4. `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub` →
   `ok  primeradiant.com/serf/cmd/serf-hub  21.014s`

## Self-review notes

- Verified post-edit that the only remaining `text-transform` declarations are
  the three pre-existing `none` resets (`.project-name`, section-header `.help`,
  mobile chip-picker override) — left untouched.
- Verified post-edit that the only remaining literal radii are `0` values and the
  one documented `30%` squircle.
- Diff reviewed in full: 13 uppercase sites + 10 radius hunks + 2 comment
  touch-ups; nothing else.
- The worktree also contains pre-existing modifications to `.superpowers/sdd/*`
  report files from earlier sessions; those were NOT included in this commit
  (only `style.css` + the new test file, per the brief).
