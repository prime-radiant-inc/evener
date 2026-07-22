# W6 pre-merge fix report — spawn pane resets after a successful spawn (§1.14)

Worktree `webui-w6-surfaces`, branch `w6-surfaces`. Starting HEAD `6e3f46aad`
(217 test files / 3189 tests, confirmed by a full baseline run before any
change: `npx vitest run` 217/3189 all passing). Two items: the §1.14 reset
itself, plus a coordinator-adjudicated fold-in of concern #1 from the first
pass (the `busy` flag is the same defect class in the same success path).
Both DONE.

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

## Gates (first commit)

- `npx tsc --noEmit`: exit 0.
- `npx vitest run` (bare): 217 files passed / 3191 tests passed (baseline
  3189 + 2 explicit new tests in this change; no dynamically-generated
  `requireclass-contract` entries since no new `.module.css` import was
  added).
- `npm run lint` (Biome ci): exit 0, 611 files checked, no fixes applied.
- `npm run build`: exit 0 (349 modules, all chunks emitted). `git restore
  cmd/serf-hub/frontend/dist/PLACEHOLDER` ran immediately after; `git status
  --short` confirmed a clean tree apart from the two intended source files.

Commit `d0d186106`.

## Second item — `busy` never resets on a successful spawn (coordinator fold-in)

Coordinator adjudication on the first pass's concern #1: fold in rather than
triage separately — same defect class (post-success state hygiene), same
`doSpawn()` success path, and an empty prompt behind a permanently-disabled
"Spawning…" button is a worse dead end than the original stale-prompt bug.

**Verification requested by the coordinator, done first.** Read
`handleSpawn`/`handleCreateConfirm` again: both already reset `busy` to
`false` on every failure branch — `handleSpawn`'s `catch`, its
`preflightDir` "abort" and "offer-create" early returns, and
`handleCreateConfirm`'s `catch` (its `finally` only resets
`createDialogPath`, but the `catch` alongside it already covers `busy`).
Confirmed true, not just assumed: three existing tests were extended with a
`(button as HTMLButtonElement).disabled === false` assertion covering each
failure branch (thrown `thread/start`, preflight-abort, and — as the
success-side control — `handleCreateConfirm`'s own success path) and all but
the success one were GREEN before any production-code change in this second
item, i.e. before touching anything. Only the success path (shared by both
callers via `doSpawn()`) was broken. No failure-path change was needed or
made.

**The change.** One more line in `doSpawn()`, added alongside the existing
§1.14 resets, before `navigate(url)`: `setBusy(false)`. Same file, same
function, no new state.

**RED evidence.** Two assertions failed identically before this line existed
— both exercising `doSpawn()`'s success path through its two different
callers:
- New test "re-enables the Spawn button after a successful spawn
  (post-success state hygiene, same class as §1.14)" (via `handleSpawn`):
  `AssertionError: expected 'Spawning…' to be 'Spawn'`.
- Extended "offers to create a missing directory, then creates it and
  spawns" (via `handleCreateConfirm`, proving the shared `doSpawn()`
  placement fixes both call sites, not just one): the appended `waitFor`
  timed out with the button still showing `disabled` + "Spawning…".

**Mutation proof.** Removed just the `setBusy(false)` line (and its
comment); reran `Spawn.test.tsx` — the identical two failures reappeared,
the other 9 tests (including the three failure-path lock-in assertions)
stayed green; restored the line, reran, 11/11 green.

**Gates (second commit).**
- `npx tsc --noEmit`: exit 0.
- `npx vitest run` (bare): 217 files passed / 3192 tests passed (3191 + 1 —
  only one of the four touched tests is a new `test()` block; the other
  three are assertions appended to existing tests).
- `npm run lint` (Biome ci): exit 0, 611 files checked, no fixes applied.
- `npm run build`: exit 0. `git restore
  cmd/serf-hub/frontend/dist/PLACEHOLDER` ran immediately after; tree clean
  apart from `Spawn.tsx`/`Spawn.test.tsx`.

Files: `cmd/serf-hub/frontend/src/panes/spawn/Spawn.tsx`,
`.../Spawn.test.tsx` (same two files as the first commit).

## Concerns

- **Resolved this pass:** the `busy`-stuck-on-success gap flagged after the
  first commit is fixed above (coordinator fold-in) — no longer open.
- **Concern #2 from the first pass (mount-vs-unmount under real dockview) —
  resolved-by-design, per the coordinator: no action needed.** The fix (both
  items) is correct under either mounting behavior — if the pane remounts
  fresh, the reset is a no-op; if it stays mounted, the reset is what makes
  the pane usable again. T8/M9's live passes are what will observe the real
  mounting behavior; not re-litigated here.
- No blockers, no scope ambiguity, no deviation from the brief or the
  coordinator's follow-up instruction.
