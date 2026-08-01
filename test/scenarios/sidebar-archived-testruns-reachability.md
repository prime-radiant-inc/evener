# sidebar-archived-testruns-reachability: Archived and Test-runs buckets round-trip (archive, unarchive, delete)

**What this covers**: the server-side project classification in
`cmd/serf-hub/internal/hubcore/tree.go` (`TreeProject.IsArchived`/`IsTestRun`,
`:126-134,939-940`) and `/api/tree`'s projection of it into
`archived_projects[]` / `test_runs[]`, where TestRuns takes precedence over
Archived (`navigationProjectBuckets`, `cmd/serf-hub/web_api_tree.go#navigationProjectBuckets`;
the ordered emit at `:161-176`). Covers the full archive→unarchive round trip,
the `SERF_SESSION_ORIGIN=test` classification path (`envvars/envvars.go:81`,
read once at fresh-session create in `agent/session_init.go:209`), and whole-
project delete through to on-disk removal.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the selector
map there is the single place these hooks are maintained. This card used to
drive `sidebar.js`'s `pushArchivedSection`/`pushTestRunsSection`, poke
`window.SerfSidebar.refresh()`, and match `[data-row-id="section:test-runs"]`.
All of that died with the vanilla frontend (`660376f78`); the rail is React
(`cmd/serf-hub/frontend/src/shell/rail/`) and none of those handles exist.

**Two section shapes, and only one of them is a disclosure** — this is the
biggest change from the card's old text:

- **Test runs** is a plain `RailSection` (`Rail.tsx:100-108,595-601`): an
  `<h3>` reading exactly `Test runs`, with **no count and no disclosure
  button**, rendered whenever `test_runs[]` is non-empty and omitted entirely
  when it is not (`:101`). Its *project rows* are collapsed, not the section.
- **Archived** is the one real disclosure (`ArchivedSection`,
  `Rail.tsx:121-130,602-612`): a `<button aria-expanded>` reading
  `Archived sessions (N)` (`:125`), collapsed by default, holding whole
  archived projects *plus* the archived-tier sessions diverted out of still-
  active projects (`railNodes.ts:306-321,332-335`). There is no `Archived (N)`
  header and no `section:test-runs` key.

## Pre-state

- A freshly built `serf-hub` on an isolated `$HOME` and a kernel-assigned port
  — the Setup checklist in `docs/agentic-testing.md`. Never a real hub.
- The frontend must be built (`make build-web`) *before* the hub for step 5+,
  or the SPA is a one-line placeholder (rebuild matrix item 3 in the runbook).
- Two scratch working directories `$A` and `$B` that must both still exist for
  the whole run — `identifier.ResolveProject` symlink-resolves them and every
  archive/delete POST re-derives the project id from the path.
- A model the hub can spawn against. Nothing here needs a *good* answer, only
  a session that reaches the past index, so the cheapest model wins.

## Steps

Steps 1-4 and 9-10 are **browser-free** (curl + the filesystem). Steps 5-8 need
a browser, and only assert what the rail renders.

1. Spawn a session in `$A` (plain `POST /api/spawn`, no `launch_overrides`),
   let it finish, then `POST /api/sessions/local:$SID_A/shutdown`.
   `GET /api/tree`: `$A`'s project key is in `projects[]`.
2. Spawn a session in `$B` with
   `launch_overrides:{env:{SERF_SESSION_ORIGIN:"test"}}`, let it finish, then
   `POST /api/sessions/local:$SID_B/shutdown`. `GET /api/tree`: `$B`'s key is
   in `test_runs[]` and in neither `projects[]` nor `archived_projects[]`.
3. **Archive `$A`.** `POST /api/archive` with
   `{"kind":"project","id":"<A key>","archived":true,"working_dir":"<A working_dir from /api/tree>"}`.
   Re-`GET /api/tree`.
4. **Unarchive `$A`.** Same POST with `"archived":false`. Re-`GET /api/tree`.
5. **Browser, baseline.** Navigate to `/auth?token=$TOKEN&next=/`. Read the
   section shapes:
   ```javascript
   ({
     port: location.port,
     headings: Array.from(document.querySelectorAll("h3"), (h) => h.textContent),
     archivedDisclosure: Array.from(document.querySelectorAll('button[aria-expanded]'))
       .map((b) => [b.textContent, b.getAttribute("aria-expanded")])
       .filter(([t]) => /Archived sessions/.test(t)),
   })
   ```
6. **Archive `$A` again** (step 3's POST) and let the rail refetch on its own —
   the archive handler broadcasts `serf/tree/changed` unconditionally
   (`web_api_archive.go:71` → `notifyMutation`, `web_api_tree.go#notifyMutation`) and
   the store refetches on a 250ms debounce
   (`stores/tree.ts:443-450,455-467`). Re-read step 5's probe,
   then click the `Archived sessions (…)` button and confirm `$A`'s project row
   appears inside it.
7. Open `$A`'s row menu — the `⋯` trigger is
   `button[aria-haspopup="menu"]` whose accessible text contains
   `Actions for <project name>` (`RailRow.tsx:299-327,611`) — and read its
   items (`li[role="menuitem"]`, portalled to `document.body`,
   `widgets/menu/index.tsx:337-366`). Click **Unarchive project**.
8. Find `$B`'s project row under the `Test runs` heading and read its menu the
   same way. Do **not** click Delete project… here; drive the destructive step
   over REST in step 9 so the assertion is on the server and the disk, not on
   a dialog. (If you do want the UI path, see Sharp edges: it is a real
   in-app `Dialog`, never `window.confirm`.)
9. **Delete `$B`.** `POST /api/project/delete` with
   `{"key":"<B key>","working_dir":"<B working_dir from /api/tree>"}`.
10. `GET /api/tree` and check the disk under the isolated state root.

## Expected

- **Step 1/2 (exact, browser-free)**: `$A` in `projects[]` only; `$B` in
  `test_runs[]` only. Falsify: a test-origin project leaking into `projects[]`
  or `archived_projects[]` — the precedence at `web_api_tree.go:359-362` is
  what routes it, and a mixed project (one unmarked session) is correctly
  *not* a test run (`fuzzScenarioAllTestSessionsClassifyAsTestRun`,
  `internal/hubcore/tree_test.go#fuzzScenarioAllTestSessionsClassifyAsTestRun`).
- **Step 3 (exact)**: `200 {"ok":true}`; `$A` moves to `archived_projects[]`
  and is gone from `projects[]`. Its entry is a **stub**: `"sessions": null`
  with `session_count` carrying the real row count
  (`web_api_tree.go:167-176`). Falsify: `$A` still in `projects[]`, or the
  archived entry ships its sessions inline.
- **Step 4 (exact)**: `$A` back in `projects[]`, gone from
  `archived_projects[]`.
- **Step 5**: `h3` headings include both `Projects` and `Test runs` (no count,
  no parenthetical on either) and there is no `Archived sessions` button yet —
  the disclosure renders only when it has something to hold (`Rail.tsx:602`).
  `$A`'s project row sits under `Projects` and `$B`'s under `Test runs`, each
  collapsed, so neither project's *session* row is in the DOM
  (`default_expanded` is `rollup_live>0||rollup_attn>0`, `tree.go:946`, false
  for an ended session). Falsify: a `Test runs (1)` header, or a collapsible
  Test-runs section — that is the pre-rewrite shape and would mean this card is
  describing a UI that no longer exists.
- **Step 6**: the `Archived sessions (1)` button now exists with
  `aria-expanded="false"`, and `$A` is absent from the DOM until it is
  clicked. After the click, `$A`'s project row renders. Falsify: the count is
  wrong, or `$A`'s row is in the DOM while the disclosure is collapsed.
- **Step 7**: `$A`'s menu offers exactly `New session`,
  `Add to pinned`/`Remove from pinned`, **`Unarchive project`**, and
  `Delete project…` (`projectMenuItems`, `RailRow.tsx:415-439` — the archive
  item's label flips on `project.is_archived`, `:430`). After clicking
  Unarchive project, the `Archived sessions` disclosure disappears entirely —
  there is no `(0)` state. Falsify: the menu says `Archive project` for a row
  living inside the Archived disclosure (the wire's `is_archived` didn't reach
  the row), or a zero-count header lingers.
- **Step 8**: `$B`'s menu offers `Archive project` (not Unarchive — `$B` was
  never archived, only classified as a test run) and `Delete project…`.
- **Step 9 (exact)**: `200 {"deleted":["<SID_B>"],"skipped":[]}`
  (`web_api_project_delete.go:193`). Falsify: `deleted` empty on a genuine
  delete, or a 409 (means the session never actually shut down — see Sharp
  edges).
- **Step 10 (exact)**: `$B`'s key absent from all three of `projects[]`,
  `archived_projects[]`, `test_runs[]`; and
  `find "$HOME/.local/state/serf/projects" -name "$SID_B*"` returns nothing.
  In the browser, the `Test runs` heading is gone (its bucket is empty and
  `RailSection` returns null at `Rail.tsx:101`). Falsify: files surviving a
  `200`, or a heading rendering for an empty bucket.

## Cleanup

- Both sessions are shut down in steps 1-2; `$B`'s bookkeeping is removed by
  step 9. Delete never touches the working directory itself.
- Unarchive or ignore `$A` — the whole state root goes away with the run dir.
- Kill the hub by the PID you captured and `rm -rf` the run directory
  (Cleanup recipe in `docs/agentic-testing.md`).

## Sharp edges

- **Confirmation is a real in-app dialog, not `window.confirm`.** Deleting a
  project from the UI opens `<Dialog title="Delete project?">` with body text
  `Permanently delete every session in "<name>"? …` and footer buttons
  `Cancel` / `Delete` (`Rail.tsx:655-676`); the widget renders
  `role="dialog" aria-modal="true"` (`widgets/dialog/OverlayPanel.tsx:92-94`).
  Stubbing `window.confirm` does nothing — there is no native dialog to
  intercept, and a card that stubs it will look like it "handled" a
  confirmation it never saw.
- **Two independent disclosure levels, and they are separate keys.** The
  Archived section's own state is stored under the id `section:archived`
  (`Rail.tsx:75`) in the same override map every row uses; a project row's is
  `projectnode:<key>` (`railNodes.ts:209-211`). Expanding the section reveals
  project rows only — a project's session rows stay collapsed until that
  project is expanded too. An ended session's project inside a freshly
  expanded section legitimately shows a header with nothing under it.
- **An archived project's sessions are not in the payload.** They ship as
  stubs and lazy-load from `/api/tree/project?key=<key>` on the project row's
  first expand (`Rail.tsx:241-253`, handler at `web_api_tree.go:285-322`).
  Until that resolves the row has a single placeholder child
  (`railNodes.ts:365-367`) rendering `Loading…` with `role="status"`
  (`RailRow.tsx:663-672`), so "expanded but empty" for a beat is normal.
- **`SERF_SESSION_ORIGIN` must travel through `launch_overrides.env`, not the
  hub's own environment.** `agent/session_init.go:209` reads it from the
  *daemon's* process env at fresh-create time, which the hub controls
  per-spawn: `launchconfig.ToEnv` applies per-launch env last so it wins over
  the inherited parent env (`cmd/serf-hub/internal/launchconfig/env.go#ToEnv`).
  Setting it in the hub's own environment would stamp `origin=test` onto every
  session the hub ever spawns.
- **"Live" for the delete refusal means a registered daemon, not a running
  turn.** A session sitting in `awaiting` still 409s
  (`web_api_project_delete.go:147-159`). Shut it down first and confirm the
  shutdown landed, or step 9 fails for a reason that has nothing to do with
  classification.
- **The TestRuns-over-Archived overlap case is server-side only** and this
  card does not re-derive it live. The old text pointed at
  `cmd/serf-hub/jstest/test-sidebar-testruns.js`; that directory no longer
  exists. The precedence itself is the `switch` at `web_api_tree.go:359-362`,
  and the classification feeding it is pinned by
  `internal/hubcore/tree_test.go:1730-1752`.
- Claim your own Chrome profile before the first browser call — see "Claim
  your own Chrome instance first" in the runbook. The auth cookie is not
  port-scoped, so two hubs sharing one profile clobber each other's session
  and produce a spurious unauthorized page that has nothing to do with the
  rail.
