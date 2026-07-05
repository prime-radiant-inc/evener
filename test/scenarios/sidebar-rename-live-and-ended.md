# sidebar-rename-live-and-ended: row-menu rename survives a live resync, survives compaction (namer suppression), and works on ended sessions

**What this covers**: `POST /api/sessions/<id>/rename` (both the live-daemon
path and the ended-meta-edit path in `cmd/serf-hub/web_api_rename.go`), the
row menu's inline rename (`sidebar.js`'s `startInlineRename`/`rename`), and
the namer-suppression rule in `agent/session_namer.go`
(`shouldApplySessionNameLocked`): once a session's name source is `"user"`, a
later `"compaction"`-sourced suggestion must **not** overwrite it.

## Pre-state

- A **live** session running against a real, credentialed model (a real
  compaction turn later needs a real model call) and, separately, an **ended**
  session. Browser authenticated against the hub.

## Steps

1. Rename the live session via the UI: open its row menu (⋯ next to the row),
   click **Rename**, type a new title into the `.sb-rename-input` that
   replaces the row's title, press Enter.
2. Immediately after Enter, read the row's `.title` text (the optimistic
   value — `sidebar.js`'s `rename()` applies it to the local model before the
   POST resolves).
3. Wait more than 2s (past the sidebar's resync-coalescing window), re-read
   the row's `.title`, and cross-check the session's persisted meta file
   (`name` / `name_source` in `<id>.meta.json` on disk) or `GET /api/tree`.
4. Trigger a real compaction turn: `POST /api/sessions/<id>/compact`. Poll
   `GET /api/sessions/<id>` until state returns to `awaiting`/`idle`.
5. Force a fresh resync (`window.SerfSidebar.refresh()`, not just reading the
   cached model) and re-read the row's title; re-read the meta file's `name`
   / `name_source`.
6. On the separate **ended** session, `POST /api/sessions/<id>/rename
   {"name":"<new>"}` directly (no live daemon involved).
7. Check the response status, the meta file, and — in the browser after a
   resync — the row's title, and confirm no error/rollback toast appeared.

## Expected

- Step 2: the title already shows the new value (optimistic).
- Step 3: the title is *still* the new value 2+ seconds later, and the meta
  file / `/api/tree` agree (`"name_source":"user"`) — this is what proves the
  update is a real server round trip, not just an un-reverted optimistic
  echo.
- Step 4: `204`.
- Step 5: the meta file still shows the user-assigned name and
  `"name_source":"user"` (unchanged), despite a genuine compaction turn
  having run. Confirm the compaction actually did something (e.g.
  `context_used` in `GET /api/sessions/<id>` drops sharply versus before —
  in one live run this went from ~15000 to 133 tokens) so a no-op compaction
  can't produce a false pass. The sidebar row shows the same, un-reverted
  title.
- Step 7: `204`; the meta file shows the new name + `"name_source":"user"`;
  the row shows the new title on the next resync; no `.toast` / error element
  appears in the DOM.
- Falsification: the title reverts to a compaction-derived name in step 5
  (namer suppression is broken), or the ended-session rename in step 7
  produces any visible error/rollback despite the `204`.

## Cleanup

- Shut down the live session.
- Discard the scratch working directories.

## Sharp edges

- A newly-created project only auto-expands in the sidebar while it has a
  live or attention-needing session (`rollup_live>0` or `rollup_attn>0`);
  once ended it collapses. After shutting a session down you may need to
  click its project header again before its row is visible in the DOM.
- `GET /api/sessions/<id>` (the session **detail** endpoint, as distinct from
  `/api/tree`) sources its `title` from the live daemon's `ReadThread`
  response for a still-live session. In live testing this did **not**
  reflect the rename (it fell back to the bare session ID) even though the
  sidebar's `/api/tree`-driven title, and the on-disk meta file, were both
  correct. This looks like a real inconsistency in the session-detail
  projection for live local sessions worth a follow-up bug report — but it
  is a different surface than the sidebar this task covers, so don't let it
  block the assertions above, which read `/api/tree` and the meta file, not
  `/api/sessions/<id>`'s `title` field.
- Compaction on a very short/cheap transcript can complete fast enough that a
  1s poll already sees the finished state — don't mistake "already done by
  the first poll" for "didn't run." Confirm via the `context_used` /
  `context_pressure` drop, or via
  `go run ./cmd/serf-doctor transcript <id> --format outline`.
- The row menu's Rename item is hidden (`hidden: !n.rename` in
  `sessionMenuItems`) for non-local (e.g. Codex-bridged) rows — this card
  only applies to local serf sessions.
