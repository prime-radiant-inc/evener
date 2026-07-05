# sidebar-project-delete-full-cycle: POST /api/project/delete validation, live-refusal, deletion, redirect, and re-creation

**What this covers**: `cmd/serf-hub/web_api_project_delete.go`'s full state
machine end to end — path-validated destructive delete, live-session refusal
(409), successful delete (200 + files removed from disk), the client's
open-workspace redirect (`onDeletedRedirect` in `sidebar.js`), and that a
deleted project is not silently archived (delete scrubs the archive/favorite
decision rows for that working dir, so a fresh session at the same path shows
up as a normal active project again, not stuck under Archived).

## Pre-state

- Hub running, browser authenticated.
- Two disposable scratch working directories (one for the 400/409/200
  sequence, a second for the open-workspace-redirect + re-creation sequence).

## Steps

1. **(a) Path-mismatch guard.** Spawn + let finish a session in project dir
   `$W`; read its project `key` and canonical `working_dir` back from
   `GET /api/tree` (see Sharp edges on symlink resolution).
   `POST /api/project/delete {"key":"<key>","workingDir":"/tmp/some-other-dir"}`.
2. **(b) Live refusal.** Do **not** shut the session down. With it still
   live, `POST /api/project/delete {"key":"<key>","workingDir":"<real $W>"}`.
3. **(c) Successful delete.** `POST /api/sessions/<id>/shutdown`, then repeat
   the exact same delete POST from step 2.
4. **(e) Tree no longer lists it.** `GET /api/tree`; confirm `<key>` is gone
   from `projects[]` — and not merely moved to `archived_projects[]`.
5. **(d) Open-workspace redirect.** In the second working dir `$W2`, spawn +
   let finish a session, navigate the browser to its workspace (`/s/<id>`),
   then trigger **Delete…** from that project's row menu (⋯ → Delete…) —
   override `window.confirm` to auto-accept *before* clicking, since the real
   handler calls the native `confirm()` dialog, which otherwise blocks a
   scripted click. Confirm the browser's `location.pathname` becomes `/new`.
6. **(f) Re-creation is not silent-archive.** Spawn a brand-new session at
   `$W2`'s exact working dir. `GET /api/tree`; confirm `$W2`'s project key
   reappears in `projects[]` (not `archived_projects[]`), containing only the
   new session.

## Expected

- Step 1: `400 {"error":"key does not match workingDir"}`.
- Step 2: `409 {"error":"project has live sessions","live":["session <shortid>"]}`;
  the session's `.meta.json` / `.transcript.jsonl` still exist on disk.
- Step 3: `200 {"deleted":["<id>"],"skipped":[]}`; the session's
  `.meta.json`, `.transcript.jsonl`, and session subdirectory are gone
  (`find <state-dir>/serf/projects/*/sessions -iname "<id>*"` returns
  nothing).
- Step 4: `<key>` absent from `projects[]`.
- Step 5: `location.pathname === "/new"`.
- Step 6: `$W2`'s project key is back in `projects[]`; `archived_projects[]`
  has no matching entry.
- Falsification: any status-code mismatch; `deleted` empty on a genuine
  delete; files still present after a `200`; the browser staying on
  `/s/<id>` after its project is deleted; or the re-created project landing
  in `archived_projects[]` instead of `projects[]`.

## Cleanup

- Shut down any sessions still live.
- Remove all scratch working directories. (The delete endpoint only removes
  Serf's own session bookkeeping files — it never touches the working
  directory / any git worktrees on disk.)

## Sharp edges

- `mktemp -d` gives `/var/folders/...` on macOS; `/api/tree`'s `working_dir`
  is symlink-resolved to `/private/var/folders/...`. Always read
  `working_dir` back from `/api/tree` before using it in a delete POST — a
  raw shell-variable mismatch reads exactly like the intentional 400 case in
  step 1, and will falsely validate step 1 for the wrong reason if you don't
  separately confirm steps 2/3 use the *correct* value.
- "Live" here means the daemon process is registered in the hub's roster —
  it does **not** require the model turn to still be in progress. A session
  that already finished its first turn and is sitting in `awaiting` is still
  "live" (and will 409 a delete) until you `POST .../shutdown`.
- The 409 body's `live` array uses short IDs (`"session " + last 6 chars`),
  not full ULIDs — don't match on the full session ID.
- Step 5 requires overriding `window.confirm` before clicking —
  `confirmDeleteProject` calls the real `window.confirm(...)`; a scripted
  click on Delete… will otherwise hang waiting on a dialog no one answers.
- If your browser tooling shares a Chrome profile with another concurrent
  hub/test run, the auth cookie collision described in
  `sidebar-expand-survives-live-resync.md`'s Sharp edges applies here too —
  use a dedicated profile.
