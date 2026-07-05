# sidebar-archived-testruns-reachability: Archived + Test runs sections round-trip (unarchive, delete)

**What this covers**: the two collapsed-by-default sidebar sections added on
top of the rebuilt sidebar — `Archived (N)` (`pushArchivedSection`, commit
`3df2a7694`) and `Test runs (N)` (`pushTestRunsSection`, commit `f3d2ca870`) —
against their server-side classification in
`cmd/serf-hub/internal/hubcore/tree.go` (`TreeProject.IsArchived`/`IsTestRun`)
and `web_api_tree.go`'s `/api/tree` projection (`archived_projects[]`/
`test_runs[]`; TestRuns takes precedence over Archived). Covers the full
archive→unarchive round-trip via the project row menu, and the
`SERF_SESSION_ORIGIN=test` (Task 15) classification path through to the
Test-runs section's Delete… action and on-disk removal.

## Pre-state

- Hub running a fresh build, browser authenticated against it (dedicated
  Chrome profile if other Serf instances/worktree builds are running
  concurrently — see Sharp edges).
- Two scratch working directories, neither with prior archive/favorite
  decisions.
- A model the hub can spawn against live (`openai/gpt-5.4-mini` verified
  live 2026-07-05). Falls back to file-seeded real metas (on-disk
  `.meta.json` + a real hub, no live model) if credentials are unavailable —
  the sidebar/menu paths under test only care that the project shows up in
  `/api/tree`, not how its session got there.

## Steps

1. Spawn + let finish an ordinary session in project dir `$A` (plain
   `POST /api/spawn`, no `launch_overrides`), then
   `POST /api/sessions/<id>/shutdown`. `GET /api/tree`: confirm `$A`'s
   project key is in `projects[]`.
2. Spawn + let finish a session in a second project dir `$B` with
   `launch_overrides.env.SERF_SESSION_ORIGIN` set to `"test"`, then shut it
   down too. `GET /api/tree`: confirm `$B`'s project key is in
   `test_runs[]` — **not** `projects[]` or `archived_projects[]`.
3. Open `/` in the browser. Confirm `$B`'s project renders **only** as a
   collapsed `Test runs (1)` section header
   (`[data-row-id="section:test-runs"]`, `aria-expanded="false"`) — no
   project header or session row for `$B` anywhere in the DOM. Confirm `$A`'s
   project header renders normally at the top level, not wrapped in any
   section.
4. On `$A`'s project header, open the row menu (⋯) and click **Archive**.
5. After the mutation lands (poll `/api/tree`, or force a client refresh via
   `window.SerfSidebar.refresh()`): confirm `$A` moved to
   `archived_projects[]` and is gone from `projects[]`. In the DOM, confirm
   an `Archived (1)` section header now exists, collapsed by default
   (`aria-expanded="false"`), with `$A`'s header/rows absent from the DOM.
6. Click the `Archived (1)` header to expand it. Confirm `$A`'s project
   header renders (reusing the normal project-header component) and its ⋯
   menu now offers **Unarchive** (not Archive).
7. Click **Unarchive**. After the mutation lands, confirm via `/api/tree`
   that `$A` is back in `projects[]` and gone from `archived_projects[]`. In
   the DOM, confirm the `Archived` section header **disappears entirely**
   (there is no "(0)" state — the header only renders when its bucket is
   non-empty) and `$A`'s header renders back at the top level, unwrapped.
8. Expand the `Test runs (1)` section from step 3. Confirm `$B`'s project
   header renders; open its ⋯ menu and confirm it offers **Delete…** (and,
   per the section's contract, plain `Archive` rather than `Unarchive` — `$B`
   was never placed in the archived bucket).
9. Override `window.confirm` to auto-accept (`confirmDeleteProject` calls the
   real dialog), then click **Delete…**.
10. After the mutation lands, confirm via `/api/tree` that `$B`'s project is
    gone from every bucket (`projects[]`, `archived_projects[]`,
    `test_runs[]`). On disk, confirm the session's `.meta.json` and
    `.transcript.jsonl` no longer exist
    (`find <state-dir>/serf/projects -iname "<id>*"` returns nothing). In the
    DOM, confirm the `Test runs` section header disappears entirely (bucket
    now empty).

## Expected

- Step 1/2: `$A` in `projects[]`; `$B` in `test_runs[]` only.
- Step 3: `Test runs (1)` header collapsed, `$B` fully absent below it; `$A`
  renders normally, unwrapped.
- Step 5: `$A` in `archived_projects[]`, gone from `projects[]`;
  `Archived (1)` header present and collapsed, `$A` absent from the DOM.
- Step 6: `$A`'s header renders on expand; menu offers Unarchive.
- Step 7: `$A` back in `projects[]`, gone from `archived_projects[]`; the
  `Archived` header is gone from the DOM (not merely showing "(0)").
- Step 8: `$B`'s header renders on expand; menu offers Delete… (and Archive,
  not Unarchive).
- Step 10: `$B` absent from all three `/api/tree` buckets; its session files
  gone on disk; the `Test runs` header gone from the DOM.
- Falsification: any project appearing in the wrong bucket (especially a
  test-origin project leaking into `projects[]`/`archived_projects[]`, or an
  archived project staying in `projects[]`); a section header rendering with
  a stale or zero count instead of disappearing entirely when its bucket
  empties; a collapsed section's project header or session rows present in
  the DOM before the section is expanded; a menu offering the wrong verb
  (Archive where Unarchive is expected, or vice versa); `Delete…` failing to
  remove the session's on-disk files; the confirm dialog never firing (would
  mean `confirmDeleteProject`'s gate was bypassed or removed).

## Cleanup

- Both sessions are already shut down by Steps 1/2; `$B`'s project is
  removed by the Delete… step itself.
- Remove `$A`'s scratch working directory (delete only scrubs Serf's session
  bookkeeping, never the working directory itself) and any scratch
  HOME/state-dir trees.
- Kill the test hub process.

## Sharp edges

- **Two independent disclosure levels.** A section's own expand state
  (`section:archived`/`section:test-runs` in `model.expanded`) is separate
  from each project's own expand state (`p.key`). Expanding a section reveals
  only the project header(s) inside it; a project's session rows stay
  collapsed unless the project is *also* expanded — which happens
  automatically only when it has a live/needs-you session
  (`rollup_live>0 || rollup_attn>0`). An ended session's project inside a
  freshly-expanded section legitimately shows a header with zero visible
  rows underneath — that's not a bug, and the row-menu / Archive-Unarchive /
  Delete… actions all work at the header level regardless of whether the
  rows are expanded.
- **`SERF_SESSION_ORIGIN` must travel through `launch_overrides.env`, not
  the hub's ambient environment.** `agent/session_init.go`'s `NewSession`
  reads the var once, at fresh-session-creation time, from the *daemon's
  own* process environment — which the hub controls per-spawn via
  `launch_overrides.env` (`cmd/serf-hub/internal/launchconfig/env.go`'s
  `ToEnv`, applied last so it wins over the daemon's inherited parent env).
  Setting the var in the hub's own environment before starting it would not
  achieve the same thing (and would leak `origin=test` onto every session
  the hub spawns if it somehow did).
- **A section renders no chrome at all when its bucket is empty** —
  `pushSection` returns early on `!list.length`; there is no `Archived (0)`
  state to look for, the header node is simply absent from the DOM.
- **`window.confirm` override required before clicking Delete…** —
  `confirmDeleteProject` calls the real `window.confirm(...)`; without
  stubbing it first, a scripted click hangs waiting on a dialog no one
  answers (same footgun documented in
  `sidebar-project-delete-full-cycle.md`).
- **Precedence between the two buckets is server-side only** (round-2 B6:
  TestRuns wins over Archived when a project would qualify for both) and is
  already covered deterministically by
  `cmd/serf-hub/jstest/test-sidebar-testruns.js`; this card doesn't
  re-derive that overlap case live, only the two buckets' independent
  round-trips.
- If other concurrent Serf/browser-automation work is running on the
  machine, use a dedicated Chrome profile and a non-default hub port — the
  auth cookie is not port-scoped (same footgun documented in
  `sidebar-expand-survives-live-resync.md`).
