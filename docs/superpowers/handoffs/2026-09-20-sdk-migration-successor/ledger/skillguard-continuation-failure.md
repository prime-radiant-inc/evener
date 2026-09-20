# Skillguard continuation failure receipt

Date: 2026-09-20

## CI evidence

Post-merge run `35486854632`, web job `106014704785`, failed in `TestSkillComposerBrowser` while waiting 30s for the continuation leg carrying `PROSE_STEER_14e redirect the running turn`.

Artifact directory: `/tmp/evener-skillguard-qHbtk5/skillguard-browser-1330649717/artifacts/`

Relevant files:

- `appwire-trace.jsonl`
- `driver.log`
- `helper-alpha-requests.jsonl`
- `helper-alpha-control`
- `milestones.jsonl`
- `failure-dump.json`
- failure PNG

The trace proves a distinct continuation turn: `turn_m5` completed at `03:50:44.092914Z`, then `turn_m6` started at `03:50:44.092996Z`. Provider helper alpha has a separate unheld continuation request (seq 7 at `03:50:44.291838Z`) carrying `PROSE_STEER_14e`; the hub emitted `item/completed` for m6 at `03:50:44.295432Z` and `turn/completed` for m6 at `03:50:44.298409Z`.

## Root cause

After m6 started, the browser issued `thread/read` request 29 at `03:50:44.210049Z` with `subscribe:false`. Its response at `03:50:44.217920Z` reported `activeTurnId: turn_m6` but returned turns only through m5. The frontend refresh replaced the live model with that snapshot. The later m6 item and completion frames were buffered/replayed against a model with no m6 row, so the reducer could neither attach the assistant item nor settle a turn row. The virtualized transcript therefore remained at m5 and the driver timed out.

## Fix

Branch `codex/skillguard-continuation-failure`, final head `8642272c2`, based on `5c407b152`:

- `252a65680`: preserve a live active turn when the refresh snapshot names the same active id but omits its row, then replay post-cut frames.
- `8642272c2`: require fused thread/instance identity before carrying the turn across the refresh and cover a same-numbered replacement instance.

## Validation

- `npx vitest run src/stores/threads.test.ts`: 363 passed.
- `npm run typecheck`: passed.
- Biome on touched files: passed.
- `make test-web-browser`: all six browser guards and `web-skillguard` passed twice, including the affected continuation scenario.
- Exact-base frontend control (`5c407b152`, `npm test`): 547 files, 12,782 tests passed.
- Final-head frontend test (`npm test`): 547 files, 12,784 tests passed.
- RoboRev branch review job `2707` against exact base `5c407b152`: no issues found.

## Full-gate disposition

The first combined `make test-web` run on the initial fix commit reported two Session assertions while 12,781 of 12,783 tests passed:

1. `src/panes/session/Session.test.tsx:2782`, `hydrated restart recovery works without navigation: refused`: expected one `evener/thread/forceStop` call for `local:incompatible-root`, received none.
2. `src/panes/session/Session.test.tsx:2840`, `confirmed force stop refreshes the session and exposes explicit Resume`: expected one force-stop call, received two.

Both Session tests pass in the exact-base standalone control and in the changed worktree; all 92 Session tests pass standalone, and the targeted threads+Session pair passes 454/454. The exact-base full frontend control and final-head full frontend test both pass, so the two assertions did not persist and no code mechanism tying them to this hydration fix was reproduced. Treat the combined-gate result as a host/load-sensitive one-off, with the exact errors retained above; the final frontend test disposition is green.

No push or merge has occurred.
