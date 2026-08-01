# sidebar-project-delete-full-cycle: POST /api/project/delete validation, live-refusal, deletion, what happens to an open pane, and re-creation

**What this covers**: `cmd/serf-hub/web_api_project_delete.go`'s full state
machine end to end — path-validated destructive delete, live-session refusal
(409), successful delete (200 plus files removed from disk), what the browser
does when the project whose session it is *showing* is deleted, and that a
deleted project is not silently archived (delete scrubs the archive/favorite
decision rows for that working dir, so a fresh session at the same path comes
back as a normal active project rather than stuck under Archived).

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the selector
map there is the single place these hooks are maintained. `onDeletedRedirect`
and the rest of `sidebar.js` died with the vanilla frontend (`660376f78`); the
delete path is now `Rail.tsx`'s `confirmDelete` (`:399-432`) over
`actions.ts`'s `deleteProject` (`:67-73`).

**Step 5 changed verdict: there is no redirect.** This card used to assert that
deleting the project of the session you are looking at navigates the browser to
`/new`. No such code path exists. `confirmDelete` deletes, refetches the tree,
and reconciles the store (`Rail.tsx:399-432`) — it never touches
`workspaceStore` or any pane. The *single-session* delete path does close
affected panes (`confirmDeleteSession`, `Rail.tsx:438-469`, the `closePane`
loop at `:454-458`), and nothing analogous was ever written for the whole-
project case. Even the generic "main slot went empty" fallback opens the
`welcome` pane, whose URL is `/`, never `/new`
(`shell/DockHost.tsx:153-160`, `shell/routing.ts:88-90`). Step 5 below asserts
what the code actually does; the gap is written up under Sharp edges rather
than dropped.

## Pre-state

- A freshly built `serf-hub` on an isolated `$HOME` and a kernel-assigned port
  — the Setup checklist in `docs/agentic-testing.md`. Never a real hub. Build
  the frontend (`make build-web`) before the hub for step 5.
- Three disposable scratch directories, **none of them inside a git repo** (see
  Sharp edges — `identifier.ResolveProject` collapses paths inside one repo to
  its main checkout, which would give two dirs the same project id):
  - `$W` for the 400/409/200 sequence,
  - `$OTHER`, an existing empty dir used only as a wrong `working_dir`,
  - `$W2` for the open-pane + re-creation sequence.
- Browser authenticated to the test hub, on its own Chrome profile.

## Steps

Steps 1-4 and 6 are **browser-free** and carry every exact assertion. Only step
5 needs a browser.

1. **(a) Path-mismatch guard.** Spawn and let finish a session in `$W`; read
   its project `key` and canonical `working_dir` back from `GET /api/tree` (see
   Sharp edges on symlink resolution). Then
   `POST /api/project/delete {"key":"<key>","working_dir":"$OTHER"}`.
2. **(b) Live refusal.** Do **not** shut the session down. With it still live,
   `POST /api/project/delete {"key":"<key>","working_dir":"<real working_dir>"}`.
3. **(c) Successful delete.** `POST /api/sessions/local:$SID/shutdown`, confirm
   the shutdown landed, then repeat step 2's exact POST.
4. **(e) Tree no longer lists it.** `GET /api/tree`; `<key>` must be gone from
   `projects[]` — and not merely moved into `archived_projects[]` or
   `test_runs[]`.
5. **(d) Open pane, then delete its project.** In `$W2`, spawn and let finish a
   session; navigate the browser to `/auth?token=$TOKEN&next=/s/local:$SID2`
   and confirm the session pane is up. Then delete that project — either from
   the row menu (`⋯` → **Delete project…** → the `Delete project?` dialog's
   **Delete** button; see Sharp edges) or with the same REST POST from another
   shell. Snapshot the page immediately and again ~3s later:
   ```javascript
   ({
     port: location.port,
     path: location.pathname,
     railRow: !!document.querySelector('[data-session-ref="local:<SID2>"]'),
     paneText: document.body.innerText.slice(0, 400),
   })
   ```
6. **(f) Re-creation is not silent-archive.** Spawn a brand-new session at
   `$W2`'s exact working dir. `GET /api/tree`; `$W2`'s project key must
   reappear in `projects[]` containing only the new session.

## Expected

- **Step 1 (exact)**: `400 {"error":"project ID does not match working_dir"}`
  (`web_api_project_delete.go:78-80`) — `$OTHER` exists, so `ResolveProject`
  succeeds and returns a *different* id than the key. Two sibling 400s guard
  the same class and are worth telling apart if you see one:
  `{"error":"resolve project: …"}` (`:73-76`) means the path doesn't exist at
  all, and `{"error":"key does not match workingDir"}` (`:109-122`) is the
  later check — key and path agree on an id, but that key names no project in
  the current tree, or the tree's `working_dir` for it differs. Falsify: a
  `200`, or any files removed.
- **Step 2 (exact)**:
  `409 {"error":"project has live sessions","live":["<short id>"]}`
  (`:147-159`); the session's `.meta.json` and `.transcript.jsonl` still exist
  on disk. Falsify: a delete going through while a daemon is registered.
- **Step 3 (exact)**: `200 {"deleted":["<SID>"],"skipped":[]}` (`:193`), and
  the session's `.meta.json`, `.transcript.jsonl`, and session subdirectory are
  gone — `find "$HOME/.local/state/serf/projects" -name "<SID>*"` (the isolated
  state root from the Setup checklist) returns nothing. Falsify: `deleted`
  empty on a genuine delete, or files surviving a `200`.
- **Step 4 (exact)**: `<key>` absent from `projects[]`, `archived_projects[]`,
  and `test_runs[]`.
- **Step 5**: `location.pathname` is **still** `/s/local:<SID2>` — unchanged,
  in both snapshots. The rail row for that session is gone (the tree refetched
  and the session no longer exists). The pane itself stays mounted with no
  model, which renders the `Loading transcript…` empty state
  (`panes/session/Session.tsx:169-174`) — `ensureThread` cannot resolve a
  thread that no longer exists, and the pane has no teardown of its own; its
  own comment says a rejection here "leaves the pane on its loading state"
  (`:123-129`). Record the actual `paneText` you observe, since the store keeps
  retrying and a toast may accompany it. Falsify **against the old
  expectation**:
  if `location.pathname` becomes `/new`, someone has *added* a redirect and
  this card's Sharp-edges gap is closed — update the card rather than reporting
  a failure. Falsify for real: the rail row survives a completed delete (the
  refetch/reconcile didn't run), or the whole app errors out rather than
  leaving one stranded pane.
- **Step 6 (exact)**: `$W2`'s key is back in `projects[]` with only the new
  session; `archived_projects[]` has no matching entry. This is the
  decision-scrubbing half: on a whole-project delete with nothing skipped, the
  project's own archive and favorite decision rows are deleted
  (`web_api_project_delete.go:362-373`), so the same path starts clean rather
  than inheriting a stale "archived" decision. Falsify: the re-created project
  landing in `archived_projects[]`.

## Cleanup

- Shut down any session still live; delete `$W2`'s project again if you want a
  clean slate.
- Remove all three scratch directories. The delete endpoint only removes serf's
  own session bookkeeping — it never touches the working directory or any git
  worktrees on disk.
- Kill the hub by the PID you captured and `rm -rf` the run directory (Cleanup
  recipe in `docs/agentic-testing.md`).

## Sharp edges

- **A whole-project delete strands an open session pane.** This is the
  discrepancy step 5 documents, kept visible rather than silently dropped:
  `confirmDeleteSession` closes every pane whose `params.ref` matches
  (`Rail.tsx:454-458`) and `confirmDelete` has no equivalent — a session pane
  is left routed at `/s/local:<SID>` for a session that no longer exists.
  `Session.tsx`/`SessionChrome.tsx` don't subscribe to the tree store, so
  nothing else notices either. Whether that should be fixed (close the panes,
  the way the single-session path does) is a product call, not a test failure;
  file it rather than asserting the old `/new` behaviour, which no version of
  this code performs.
- **`mktemp -d` gives `/var/folders/…` on macOS; `/api/tree`'s `working_dir` is
  symlink-resolved to `/private/var/folders/…`.** Always read `working_dir`
  back from `/api/tree` before putting it in a delete POST. A raw shell
  variable produces the *same* 400 as step 1's intentional mismatch, which will
  falsely validate step 1 for the wrong reason unless steps 2/3 separately
  prove the correct value works.
- **Keep the scratch dirs out of any git repo.** `ResolveProject` selects the
  repo's main checkout for a Git project (`identifier/project.go:78-87`), so
  two directories inside one repo resolve to one project id — `$W` and `$OTHER`
  would then agree on the id, and step 1 would take the *later* 400 branch
  (`"key does not match workingDir"`) instead of the one it asserts.
- **"Live" means the daemon is registered in the hub's roster, not that a turn
  is running.** A session sitting in `awaiting` still 409s. The predicate is
  `projectSessionLive` (`web_api_project_delete.go#projectSessionLive`), which deliberately
  does *not* count a retained crash marker as live (kata 8at6) — a crashed
  session is deletable.
- **The 409 body's `live` array carries short ids**
  (`hubcore.ShortID`, `web_api_project_delete.go:152`), not full session ids.
  Don't match on the full id.
- **Confirmation is a real in-app dialog, not `window.confirm`.** The row
  menu's `Delete project…` item (`RailRow.tsx:433-437`) opens
  `<Dialog title="Delete project?">` with `Cancel` / `Delete` footer buttons
  (`Rail.tsx:655-676`, `role="dialog" aria-modal="true"` from
  `widgets/dialog/OverlayPanel.tsx:92-94`). Stubbing `window.confirm` does
  nothing at all here — there is no native dialog, and a card that stubs it
  will believe it answered a prompt that never appeared.
- **A partially-refused delete is a `200`, not an error.** When a session races
  back to live mid-pass it lands in `skipped` (`{"id":…,"reason":…}`,
  `web_api_project_delete.go:20-24`) and the rail toasts
  `Deleted N session(s); M could not be removed` (`Rail.tsx:421-426`). Read
  both arrays, not just the status code.
- Use a dedicated Chrome profile; the auth cookie is not port-scoped. Keep the
  `location.port` assertion inside every `eval`.
