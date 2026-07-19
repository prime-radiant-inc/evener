# Task 1 report: Width scale — `--measure` tokens and the machine bleed

Status: DONE
Commit: 11a823934f62d738ccf2387f6b3c074082e3d654

## What changed

1. `cmd/serf-hub/jstest/test-layout-scale-css.js` (new) — copied verbatim from the brief.
2. `cmd/serf-hub/assets/style.css`:
   - `:root`: after `--space-9: 64px;` added the width-scale comment block and `--measure: 720px;` / `--measure-machine: 1000px;` (verbatim from the brief).
   - `#workspace`: `--workspace-content-max-w: 832px;` → `--workspace-content-max-w: var(--measure-machine);`.
   - After the `.workspace-header, .conversation, .workspace-input { … }` rule: added `.conversation > * { max-width: var(--measure); }` and the five-selector bleed rule (`.tool-call`, `.tool-call-cluster`, `.subs`, `.notification-card`, `.task-card` → `max-width: none`), verbatim from the brief.

## Test commands + results

- `cd cmd/serf-hub/jstest && node test-layout-scale-css.js` (before implementation):
  `FAIL: --measure: 720px defined on :root` (exit 1) — expected red.
- Same command after implementation: `ok layout width scale` (exit 0).
- `cd cmd/serf-hub/jstest && ./run-all.sh`: all tests OK, final line `jstest: all tests passed` (exit 0). The new test was included; no existing test asserted 832px, so no expectation updates were needed.

## Self-review

- Diff is exactly 37 insertions / 1 deletion across the two files; only the brief's code was added.
- `git status` after commit shows only `.superpowers/sdd/progress.md` modified (pre-existing, not touched by this task; not committed per the brief's exact `git add` list).
- Tokens `--measure: 720px` and `--measure-machine: 1000px` on `:root`, and `--workspace-content-max-w: var(--measure-machine)` on `#workspace`, match the interface contract for Tasks 2, 3, 6.

## Deviations

None.
