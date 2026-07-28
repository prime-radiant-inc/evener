# Task 11 report — Full verification + decisions.md update

Repo: `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/transcript-view-design`

## Summary
Updated `docs/web-ui/decisions.md` to mirror the ratified tiered-density revisions for topics 03, 04, 05, and 07, then ran the requested verification steps and committed the change.

Commit: `6c32720be` — `transcript: decisions.md verdicts for tiered-density revisions (task 11)`

## Edits made
File: `docs/web-ui/decisions.md`

- Topic 03:
  - Replaced the old 40px inline gutter-column discussion with the stacked `You` eyebrow revision.
  - Kept the measured exchange-boundary rationale, now framed as the 32px exchange-boundary geometry.
  - Cited `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md` §Decision revisions requiring Jesse's ratification, item 3.

- Topic 04:
  - Changed Alt A from LIVE to `CHANGED, SUPERSEDED` for the size rule.
  - Added the new verdict line for the visible agent label / exchange-boundary eyebrow, marked `SUPERSEDED`.
  - Kept the contrast rationale (`--ink-hi` vs `--ink-mid`) and the measured exchange-boundary note.
  - Cited ratification items 1 and 2.

- Topic 05:
  - Appended that the preview is closed-only and the open state shows the short label.
  - Cited ratification item 4.

- Topic 07:
  - Added that round timings join the quiet one-liner rule and the scaffold box no longer applies.
  - Cited ratification item 5.

No other structure in the file was changed.

## Commands run and results
1. `git status --short`
   - Result: repository had existing modified files at start; `docs/web-ui/decisions.md` was the intended file to update.

2. Read brief and spec files
   - `task-11-brief.md`
   - `docs/web-ui/decisions.md`
   - `docs/web-ui/specs/2026-07-27-transcript-tiered-density-design.md`
   - Result: confirmed the four decisions.md targets and the ratification items.

3. `npm test`
   - Result: PASS
   - Evidence: `283 passed`, `4860 passed` tests.

4. `npm run typecheck && npm run lint && npm run layoutguard && npm run overflowguard`
   - `typecheck`: PASS
   - `lint`: PASS
   - `layoutguard`: PASS
   - `overflowguard`: FAIL
   - Failure details:
     - `disclosure exception safety: found=1, threw=true, original=[false], restored=[false]`
     - `disclosure browser contract found 1 of 2 fixtures`
     - `system-prompt body was closed during horizontal-overflow scan`
   - These failures appeared during the requested guard run.

5. `npm run build`
   - Result: PASS
   - Evidence: clean `vite build` completed and produced `dist/` artifacts.

6. `git status --short`
   - Result: only `M docs/web-ui/decisions.md` before commit.

7. `git rev-parse --short HEAD` / `git log --oneline -1`
   - Result before commit: `4dde14d9f transcript: hint disclosure as controlled button in failure cap (task 10 fix)`

8. `git diff -- docs/web-ui/decisions.md`
   - Result: confirmed only the intended verdict-line edits.

9. `git add docs/web-ui/decisions.md && git commit ...`
   - Result: commit created successfully.
   - Commit SHA: `6c32720be`

## Concern
- `npm run overflowguard` failed during the requested verification sequence.
- I did not change code outside `docs/web-ui/decisions.md`, so I left that failure as a concern rather than trying to repair it in this documentation-only task.

## Fix follow-up

Changed `cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx` so the harness expands the notification card during the settle phase before `measure()` runs. The change chains a synthetic click on `[data-testid="notification-card"]` into the existing `settled` promise after two animation frames and a macrotask, which causes the notification body and its raw-notification disclosure fixture to exist in the DOM for the overflow scan.

Commands and results:

- `npm run overflowguard` — PASS at 390px, 700px, 1024px, and 1400px.
- `npx vitest run src/dev 2>/dev/null; npm run typecheck` — PASS. Vitest ran the existing `src/dev` suites (`WidgetGallery`, `ThemeFlip`, `DevHarness`); no separate overflow-harness-specific test file exists, so this was the nearest harness-adjacent coverage.
- Commit: `7fcf0faef` — `overflowguard: expand notification card at harness boot (task 11 fix)`
