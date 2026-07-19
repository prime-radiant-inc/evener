# Task 7 report — Token migration: canonical definitions, scripted rename, legacy/dead deletion

Commit: `f8c508be` — "web: canonical color-token vocabulary (alias → rename → delete) + dead CSS removal"

## What changed

### `cmd/serf-hub/assets/style.css`
- **Step 3 scripted rename**: ran the brief's exact 14-pattern sed. Post-rename counts:
  `var(--ink)` ×160, `var(--ink-2)` ×265, `var(--surface)` ×66 (brief's ≥167 estimate for
  `--ink` counted the alias definitions, which were then deleted — 160 is the 159 `var(--text)`
  uses + 1 `var(--user)`? No: `--ink` = 159 `var(--text)` + 1 other exact-paren match in a
  comment-free rule; `--ink-2` = 256 `--text-muted` + 7 `--muted` + 1 `--user` + 1 = 265. All
  consistent with the mapping table.)
- **4 theme blocks rewritten** (`:root`, `@media light :root`, `[data-theme="dark"]`,
  `[data-theme="light"]`): canonical head `--surface/--surface-2/--ink/--ink-2/--ink-3/--ink-4/
  --line/--hair/--accent/--attention/--done` with shipped values; `--ink-3` = `#7e8593` (dark)
  / `#6b6b76` (light); `--done: var(--ink-3)`. Deleted `--surface-secondary`,
  `--accent-secondary`, and the 6-line legacy alias block (+ its comment) from every block.
  `--state-*`, `--error`, `--success`, diff palette, diagnostics, `--btn-primary-text`, and all
  spacing/type/motion/z sections untouched. `--diagnostic-ui` now reads `var(--accent)` (sed
  already rewrote its value).
- **Dead CSS**: removed `.composer-send:active`/`#send-btn:active` from the scale-pop rule;
  deleted the Pass-8 `.btn:active, … { transform: translateY(0.5px); }` group and the
  `transform` on `.btn-primary:active`, keeping the surface-drop restyles. Kept
  `.btn-icon:active`/`.btn-chip:active` (surface-only, no transform — they postdate the plan).
- **Comment prose**: updated ~15 stale comments that referenced retired names
  (`--text`, `--text-muted`, `--text-dim`) to the canonical names — no assertion required it,
  but leaving prose pointing at dead tokens would rot.

### `cmd/serf-hub/jstest/test-color-system-css.js` (rewritten in place)
- Brief's contract verbatim (canonical tokens in all 4 blocks, `--ink-3` values, zero legacy
  names, dead-CSS assertions) **plus** the pre-existing mockup-contract assertions folded in,
  with expectations migrated per the mapping table (`--text-dim` → `--ink-4` in the five
  body-text exclusions, `var(--bg-raised)` → `var(--surface)` in the pill/code background
  exclusions). The `--user must not map to --state-idle` assertion was dropped — it is
  subsumed by the "no legacy tokens" sweep (`--user` no longer exists at all).
- Switched the harness from `assert/exit(1)` to the file's existing `failures[]` collector so
  one run reports every broken assertion (this is how the old file worked).

### Existing tests migrated (same commit, assertions not weakened)
| File | Migration |
|---|---|
| `test-composer-layout-css.js` | `var(--bg-raised)` → `var(--surface)` (+ message) |
| `test-context-pressure-css.js` | `var(--text)` → `var(--ink)` (+ message) |
| `test-pane-and-sidebar-css.js` | `var(--text-muted)` → `var(--ink-2)` (turn-meta color) |
| `test-renderer-plan.js` | `var(--rule)` → `var(--line)` (task-card rail) |
| `test-subagents.js` | `var(--rule)` → `var(--line)` (.subs rail) |
| `test-transcript-typography.js` | `var(--text-muted)` → `var(--ink-2)` (tool-disclosure glyph) |

### `docs/web-ui/design-system.md`
- §3 neutral-ramp paragraph replaced with current-truth prose (shipped values, both themes,
  `--hair` as the 50% `--line` mix) — clean replacement, no strike-through annotation style.

## Test commands + results
- `node test-color-system-css.js` before migration: **FAIL** (68 failures — canonicals
  undefined, legacy present, dead selectors present). After: **ok canonical color tokens**.
- `cd cmd/serf-hub/jstest && ./run-all.sh` → **exit 0, 184 tests OK, "jstest: all tests passed"**.
- `make build-hub` → **exit 0**.
- `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub` → **ok primeradiant.com/serf/cmd/serf-hub 20.802s** (the Go contract test needed no changes).

## Deviations from the brief
1. **Pass-8 surface drops kept `var(--surface-2)`, not the brief's `var(--surface)`.** The
   brief's Step-5 target block shows `var(--surface)` and claims it "reflects the Step 3 sed",
   but its own mapping table sends `var(--surface-secondary)` → `var(--surface-2)`, and the
   pre-task value was `var(--surface-secondary)` (#1c1c24). Using `--surface` (#16161e) would
   have been a real visual change, contradicting the task's "SHIPPED values (no visual
   change)" premise. Chose no-visual-change.
2. **Kept `.btn-icon:active`/`.btn-chip:active` surface drops** in the Pass-8 block — they
   were added after the plan was written and contain no scale-pop-cancelling transform;
   deleting them would remove live styling beyond the task's scope.
3. Updated stale comment prose referencing retired token names (cosmetic, in-scope hygiene).

## Self-review notes
- Type scale `--text-2xs…--text-2xl` verified untouched (`grep -c 'var(--text-'` unchanged in
  behavior; font-size presets blocks intact).
- No non-CSS uses of legacy tokens existed (pre-rename grep of `assets/*.js templates/` was
  empty), so the sed scope was style.css only.
- `git status` before commit showed pre-existing unrelated modifications to
  `.superpowers/sdd/{progress,task-1-report,task-6-report}.md` — left unstaged, not mine.
