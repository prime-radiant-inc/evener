# Task 9 report: Contrast pass — `--ink-4` stops coloring words

## What changed

`cmd/serf-hub/assets/style.css` had **43** `var(--ink-4)` sites post-Task-7.
**36 flips** (`color: var(--ink-4)` → `color: var(--ink-3)`) + **7 keeps**
(decorative fills/borders). Matches the brief's arithmetic exactly
(43 − 7 = 36).

### Flipped to `--ink-3` (36 `color:` declarations)

Words/numbers:
- `.project-count` (:735)
- `.ask-question-num` (:1720)
- `.queue-preview-help` (:2014)
- `.terminal-footer` (:2797)
- `.sub-r .dur` (:2936), `.sub-r .lk` (:2943), `.sub-r .res` (:2954)
- `.sub-r .res .steps` (:2962), `.sub-r .res .age` (:2963)
- `.sub-r.act-quiet .res .live` (:2968)
- `.tie-link` (:2981)
- `.task-time` (:3208)
- `.notification-card-sub` (:3300), `.notification-card-facts dt` (:3307)

Meaning-carrying glyphs (carets/chevrons/separators/markers/status):
- `.subagent-parent-sep` (:858)
- `.project-rollup .rollup-sep` (:1207)
- `.cluster-header .cluster-chevron` (:1235)
- `details.session-tier.archived > summary.session-tier-label::before` (:1364)
- `.archived-projects > details > summary.tier-header::before` (:1397)
- `button.composer-model-value .caret` (:2102)
- `.user-image-cap-sep` (:2345)
- `.assistant-message li::marker` (:2393)
- `.tool-call .tool-status-pending` (:2564)
- `.tool-call.has-purpose .result-detail` (:2653)
- `.task-list-body .task-arrow` (:2832)
- `.sub-r .g.unk` (:2920)
- `.plan-item.pending .plan-glyph` (:3239), `.plan-item.cancelled .plan-glyph` (:3244)
- `.tasks-list .task-row-chevron` (:4244)
- `.spawn-row-caret` (:4691, phone media band)

Icon-button affordances (reveal-on-hover chrome, still interactive meaning):
- `.project-new-btn.btn-icon, .project-gear-btn.btn-icon` (:798)
- `.project-chevron.btn-icon` (:821)
- `.sb-row-wrap > .archive-btn` (:1445)
- `.project-header > .archive-btn.btn-icon` (:1469)
- `.ask-note-toggle` (:1835)
- `.open-beside-btn` (:3014)

### Kept at `--ink-4` (7 decorative fills/borders — the full keep-list)

1. `.project-rollup-dot` background (:743)
2. `.assistant-message code` border-bottom (:2398)
3. `.subs[data-stale="true"]` border-left-color (:2877)
4. `.task-card-meter-fill` background (:3193)
5. 10px radio-dot `border: 1px solid var(--ink-4)` (:3815)
6. `.details-meter-fill` background (:4225)
7. Phone sheet grab-handle background (:4756)

No background/border uses of `--ink-4` exist outside the keep-list, so no
contract-test extension candidates beyond the two already added (below).

`.think` tiers untouched (bottom out at `--ink-2`, AA) per the brief.

## Test

Created `cmd/serf-hub/jstest/test-contrast-css.js` (contract test).
Watched it fail pre-implementation (`FAIL: --ink-4 never colors words`),
then pass post-implementation (`ok contrast: --ink-4 is non-text only`).

### Deviations from the brief's verbatim test snippet (both documented, intentional)

1. **Blanket assertion regex adapted.** `/color:\s*var\(--ink-4\)/` also
   matches the keep-list longhand `border-left-color: var(--ink-4)` (and
   caught my own sed doing the same in reverse — see self-review). Changed to
   `/(?<![\w-])color:\s*var\(--ink-4\)/` so only real `color:` declarations
   match. This is the brief-sanctioned "adapt to actual declaration text"
   case; without it the test can never pass while keep-list site #3 exists.
2. **Keep-list patterns extended from 5 to 7.** Added patterns for the two
   brief keep-list sites the snippet didn't assert: the 10px radio-dot border
   (`/width:\s*10px;[^}]*height:\s*10px;[^}]*border:\s*1px solid var\(--ink-4\)/`)
   and the phone grab-handle (`/width:\s*36px;[^}]*height:\s*4px;[^}]*background:\s*var\(--ink-4\)/`).
   Note the radio dot's actual rule uses `border-radius: 50%`, not
   `var(--radius-pill)` as the task text guessed — the pattern keys off
   width/height/border instead.

## Commands + results

- `cd cmd/serf-hub/jstest && node test-contrast-css.js`
  → pre-implementation: `FAIL: --ink-4 never colors words` (exit 1);
  post-implementation: `ok contrast: --ink-4 is non-text only` (exit 0)
- `cd cmd/serf-hub/jstest && ./run-all.sh`
  → `jstest: all tests passed` (exit 0)
- `make build-hub` → exit 0
- `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub`
  → `ok  primeradiant.com/serf/cmd/serf-hub  21.139s`

## Self-review notes

- **sed substring trap (caught and fixed):** the bulk flip
  `s/color: var(--ink-4)/color: var(--ink-3)/` also rewrote
  `border-left-color: var(--ink-4)` at :2877 (keep-list site #3). Detected by
  re-grepping `--ink-4` post-flip (6 sites instead of 7) and restored
  immediately. The adapted contract test now guards this both ways:
  keep-pattern asserts :2877 is `--ink-4`, lookbehind assertion forbids real
  `color:` uses.
- Final `--ink-4` census: exactly the 7 keep-list sites, all
  background/border/border-left-color declarations. Zero `color:` uses.
- Diff reviewed: 36 removed `color: var(--ink-4)` lines, 36 added
  `color: var(--ink-3)` lines, no other CSS touched.
- Committed only `cmd/serf-hub/assets/style.css` and
  `cmd/serf-hub/jstest/test-contrast-css.js`; pre-existing unrelated
  worktree modifications (`.superpowers/sdd/*`) left unstaged.
