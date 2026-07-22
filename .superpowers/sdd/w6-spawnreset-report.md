# W6 pre-merge fix report — spawn pane resets after a successful spawn (§1.14)

Worktree `webui-w6-surfaces`, branch `w6-surfaces`. Starting HEAD `6e3f46aad`
(217 test files / 3189 tests, confirmed by a full baseline run before any
change: `npx vitest run` 217/3189 all passing). Single item, per the brief.
DONE.

## The finding

`docs/web-ui/parity/parity-m6-surfaces.md` §1.14 L186 (`spawn.js:1331-1336`):
legacy clears the pending-attachment bag and resets the paste marker-counter
on a successful spawn, BEFORE navigating away, so a back-button/singleton
return can't resend a stale image. `docs/superpowers/plans/wave6-report.md`'s
close sweep cited this as the chief gap (13-gap punch list, "most
consequential"): `Spawn.tsx` had no post-success form reset at all — the
spawn pane is registered `singleton: true` (`paneRegistry.ts`), and
`AppShell`/`workspace.ts` open the newly-spawned session pane without closing
spawn, so the prompt textarea and any staged image chip could still be
sitting there if the user returned to it.

## The change

`cmd/serf-hub/frontend/src/panes/spawn/Spawn.tsx`, `doSpawn()` only:
- Snapshot `submittedMarkers` (the currently-staged attachment markers)
  BEFORE the `await startThread(...)` call — mirrors `Composer.tsx`'s
  `submitAction` snapshot-before-await pattern, so an attachment added while
  the spawn request is in flight is not in the set removed afterward (same
  in-flight-safety contract `useAttachments.ts`'s own `clearSubmitted` doc
  comment already documents for the composer).
- After `startThread` resolves (success only) and `saveDefaults` runs: reset
  the prompt via the existing `updatePrompt("")` helper, and call
  `attachments.clearSubmitted(submittedMarkers)` — the attachments hook's own
  reset/clear mechanism (reused verbatim, not reimplemented; it also resets
  the paste marker-counter internally once the result is empty, matching the
  floor row's second clause for free).
- Both calls sit before the existing `navigate(url)` call, matching the
  floor row's "BEFORE navigating away" ordering.
- Sticky defaults (`harness`/`model`/`cwd`/`branch`/`accessMode`,
  `advancedOverrides`, `staleNotice`) are untouched — no new state variable
  was added or reset. A thrown `startThread` (or `saveDefaults`) error
  propagates out of `doSpawn()` before reaching the reset lines, so every
  existing `catch` block in `handleSpawn`/`handleCreateConfirm` (which only
  toast + `setBusy(false)`) still leaves prompt/attachments exactly as the
  user left them — "failure paths keep everything" holds by construction,
  not by an added guard.

## RED evidence

New test in `Spawn.test.tsx`, "resets the prompt and attachments after a
successful spawn, but keeps sticky defaults (floor §1.14 L186)": types a
working dir + prompt, pastes a PNG into the prompt textarea (same
`installCanvasStubs`/paste-event technique as `Composer.test.tsx`, copied in
since it's private to that file), submits, waits for the route to change to
`/s/local%3Aabc123`, then asserts the prompt textarea is empty, the
attachment's remove button is gone, and the working-directory sticky default
is still `/tmp/project`. Pre-fix failure:

```
AssertionError: expected 'do the thing[image 1]' to be ''
 ❯ src/panes/spawn/Spawn.test.tsx:317:24
   expect(prompt.value).toBe("");
```

A companion test, "a failed spawn leaves the prompt and attachment staged
(failure paths keep everything)", pins the explicit non-goal from the brief
(`thread/start` throws; prompt/attachment must remain). It passed before the
fix too (nothing reset on any path yet) and continued passing after — it
exists to catch a regression if a future change moved the reset into a
`finally`-style path.

## Mutation proof

`git stash push -- Spawn.tsx` reverted the fix only (test file kept);
`npx vitest run src/panes/spawn/Spawn.test.tsx` reproduced the identical
pre-fix failure (same assertion, same message) with the other 9 tests in the
file (including the failure-path companion test) still green; `git stash
pop` restored the fix; full gate chain re-run green (below).

## Gates

- `npx tsc --noEmit`: exit 0.
- `npx vitest run` (bare): 217 files passed / 3191 tests passed (baseline
  3189 + 2 explicit new tests in this change; no dynamically-generated
  `requireclass-contract` entries since no new `.module.css` import was
  added).
- `npm run lint` (Biome ci): exit 0, 611 files checked, no fixes applied.
- `npm run build`: exit 0 (349 modules, all chunks emitted). `git restore
  cmd/serf-hub/frontend/dist/PLACEHOLDER` ran immediately after; `git status
  --short` confirmed a clean tree apart from the two intended source files.

## Concerns

- **`busy` is never reset to `false` on a successful spawn**, in either the
  pre-fix or post-fix code (`handleSpawn`'s `try` block falls through after
  `await doSpawn()` with no trailing `setBusy(false)`; only the `catch` and
  the two early-return preflight branches reset it). This is pre-existing,
  not introduced by this change, and is not named by floor row §1.14 L186
  (which only specifies the button restores "on failure", matching legacy's
  full-page-navigation model where success makes the button's state moot).
  If the spawn pane genuinely stays mounted behind the session pane it
  navigated to (the wave6-report's stated reason this gap matters), a
  returning user would see a correctly-emptied prompt but a Spawn button
  permanently stuck disabled/"Spawning…" — arguably a worse dead-end than the
  bug just fixed. Left untouched: not in the brief's enumerated reset list,
  not cited by the floor row or the gap punch list as its own item, and
  fixing it would need a scope decision (e.g. whether to reset in a
  `finally`) the brief didn't make. Flagging for Jesse/the controller to
  triage, possibly as its own micro-item.
- Did not independently re-verify, via a live dockview probe, that the spawn
  pane actually stays mounted (rather than unmount-then-remount-fresh) when
  `navigate()` opens the new session pane beside it — `DockHost.tsx`'s own
  "UNMOUNT, NOT HIDE" comment says dockview unmounts a panel the instant it
  stops being the active tab *in its own group*, which would itself reset
  all component-local state on remount. Took the wave6-report's static-code
  finding (verified against `paneRegistry.ts`'s `singleton: true` +
  `AppShell.tsx`'s open-without-close routing) as authoritative per the
  brief rather than re-litigating the sweep's own methodology; the fix is
  correct defensive engineering (and matches the floor row) regardless of
  which mounting behavior turns out to hold live.
- No blockers, no scope ambiguity otherwise, no deviation from the brief.
