# sidebar-rename-live-and-ended: row-menu rename survives a refetch, survives compaction (namer suppression), and works on ended sessions

**What this covers**: the typed `evener/thread/name/set` method on both of its
hub paths — the live-daemon path (`SetThreadName` through the source) and the
ended meta-edit path (`cmd/evener-hub/app_rename.go#setThreadName`) — the rail's
rename dialog, and the namer-suppression rule in
`agent/session_namer.go`: once a session's name source is `"user"`, a later
`"compaction"`-sourced suggestion must **not** overwrite it
(`shouldApplySessionNameLocked`, `:374-382`; the source constants at `:21-23`).

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the selector
map there is the single place these hooks are maintained. Rows are
`[data-session-ref="local:<SID>"]` (`RailRow.tsx:509`); there is no `.sb-row`,
no `.title` class, and no `window.EvenerSidebar.refresh()`.

**Rename is a dialog now, not an inline edit.** `sidebar.js`'s
`startInlineRename` / `.sb-rename-input` are gone. The flow is: the row's `⋯`
trigger (`button[aria-haspopup="menu"]`, accessible text
`Actions for <session title>`, `RailRow.tsx:299-327`) → the `Rename` menu item
(`li[role="menuitem"]`, portalled to `document.body`,
`widgets/menu/index.tsx:337-366`) → `<Dialog title="Rename session">` holding a
`Name`-labelled text input and footer buttons `Cancel` / `Rename`
(`Rail.tsx:629-653`; `role="dialog" aria-modal="true"` from
`widgets/dialog/OverlayPanel.tsx:92-94`). The `Rename` button is disabled while
the trimmed value is empty (`Rail.tsx:639`).

**Navigation resource request counts are bounded** (`docs/superpowers/specs/2026-08-25-tree-transport-optimization-design.md`): a rename mutation triggers at most one AppWire read per affected loaded navigation representation (manifest, section page, project root); the idle rail issues zero navigation reads after hydration, and the rename mutation plus its matching `navigation invalidation` notification do not duplicate a resource read.

## Pre-state

- A freshly built `evener hub` on an isolated `$HOME` and a kernel-assigned port
  — the Setup checklist in `docs/developing-evener/agentic-testing.md`. Never a real hub. Build
  the frontend (`make build-web`) before the hub for the browser half.
- A **live** session against a real, credentialed model (step 4's compaction is
  a genuine model call) and, separately, an **ended** session. Note both refs
  as `local:<SID>`.
- Browser authenticated to the test hub, on its own Chrome profile.

## Steps

Steps 4 and 6-7 are **browser-free** and carry the exact assertions. Steps 1-3
and 5 need a browser — they are the only way to exercise the dialog and the
optimistic overlay.

1. **Rename the live session through the UI.** Find its row by
   `[data-session-ref="local:<SID>"]`, click the `⋯` trigger inside it, click
   the `Rename` item, replace the input's value, click `Rename`.
2. **Snapshot synchronously, before the AppWire round trip resolves.** Read the row's
   title text immediately after the click, without awaiting anything — the rail
   projects an optimistic `sessionTitle` op onto the rendered tree from before
   the request resolves (`Rail.tsx#runAction`,
   applied by `railPending.ts:80-81`).
3. **Snapshot again after the navigation publication and refetch settle**
   (~2s is ample). Then cross-check the server: read the AppWire navigation
   resource for the row's title, and the session's persisted
   `<SID>.meta.json` for `name` / `name_source`.
4. **Trigger a real compaction turn**:
   `POST /api/sessions/local:<SID>/compact`. Poll
   `GET /api/sessions/local:<SID>` until it settles, and record
   `context_used` (`hubapi/types.go:144`) before and after.
5. Re-read the row's title in the browser. Do not force anything — a successful
   rename commits one `evener/navigation/invalidated` publication through
   `NavigationService.Refresh` (`app_rename.go#completeThreadNameMutation`),
   and the navigation store refetches the affected loaded resources
   (`stores/navigation/store.ts#initNavigation`). Re-read the meta file's
   `name` / `name_source`.
6. **On the separate ended session**, use the authenticated `/rpc` AppWire
   connection from "Driving AppWire directly" in
   `docs/developing-evener/agentic-testing.md`. After `initialize`, send a
   request with an id so its response can be distinguished from the interleaved
   navigation notification:
   ```json
   {"id":2,"method":"evener/thread/name/set","params":{"ref":"local:<SID2>","name":"  <new>  "}}
   ```
   No live daemon is involved.
7. Check that request id 2 has an empty successful result, then check the meta
   file and — in the browser after the refetch — the row title, plus whether
   any toast appeared.

## Expected

- **Step 2**: the row already shows the new title. Falsify: it still shows the
  old one — the optimistic overlay never rendered, and step 3 then cannot
  distinguish "the round trip worked" from "the overlay was never dropped".
- **Step 3**: the title is *still* the new value after the refetch settles, and
  the AppWire navigation resource plus the meta file agree, with `"name_source":"user"`. That
  agreement is what proves a real server round trip rather than an un-reverted
  optimistic echo. Falsify: the title snaps back — the AppWire request was rejected and
  the overlay was dropped (a failure also toasts, `Couldn't rename session: …`,
  `Rail.tsx:388,324`).
- **Step 4**: confirm the compaction genuinely did something — `context_used`
  should drop sharply
  (one live run went from ~15000 to 133) — so a no-op compaction cannot produce
  a false pass in step 5.
- **Step 5 (the core assertion)**: the meta file still shows the user-assigned
  name with `"name_source":"user"`, unchanged, despite a genuine compaction
  turn having run; the rail row shows the same un-reverted title. Falsify: the
  title or `name_source` changes to a compaction-derived value — namer
  suppression is broken. `shouldApplySessionNameLocked` only lets a
  `compaction`-sourced name land when the current source is `prompt` or
  `compaction` (`agent/session_namer.go:378-381`), never over `user`.
- **Step 7 (exact)**: request id 2 has `"result":{}`; the name is trimmed to
  `<new>` and the ended meta-edit path stamps `Name`, `NameSource: "user"`, and
  `NameUpdatedAt` (`app_rename.go#mutateThreadName`). The meta file shows the
  normalized name and `"name_source":"user"`; the row shows the new title
  after the refetch; and no error toast appears — the toast
  region is `section[aria-live="polite"][aria-label="Notifications"]`
  (`widgets/toast/index.tsx:36`), which must stay empty. Falsify: any visible
  error or rollback despite the successful AppWire response.

## Cleanup

- `POST /api/sessions/local:<SID>/shutdown` for the live session.
- Kill the hub by the PID you captured; `rm -rf` the run directory and the
  scratch working directories (Cleanup recipe in `docs/developing-evener/agentic-testing.md`).

## Sharp edges

- **The Rename menu item is server-gated.** It renders only when the wire's
  `rename` flag is set on the node (`RailRow.tsx:362-364`), and the hub sets
  that from `rowRenameable` (`web_api_tree.go:972,1308-1312`), which is just
  "does this id parse as a local ref" (`isLocalRouteID`, `web.go#isLocalRouteID`). So
  non-local source-backed rows and synthetic `cluster:` fold rows never offer it — this
  card is about local top-level evener sessions.
- **The ref is typed data, not a URL segment.** `threadsStore.rename` sends the
  exact ref in `ThreadNameSetParams` (`stores/threads.ts#threadsStore`), and the hub
  validates and canonicalizes it with `appwire.ParseRef`
  (`app_rename.go#setThreadName`). Keep the `local:` prefix: a bare id is not a
  valid AppWire ref and is rejected as invalid parameters.
- **A newly-created project only auto-expands while it has a live or
  attention-needing session** (`rollup_live>0 || rollup_attn>0`,
  `internal/hubcore/tree.go:946`); once its sessions end it collapses. After
  shutting a session down you may need to expand its project row again before
  its `[data-session-ref]` row is in the DOM at all.
- **`GET /api/sessions/local:<SID>`'s `title` no longer comes from the daemon
  for a local session.** The old text warned that the detail endpoint sourced
  the title from the live daemon's `ReadThread` and would show the bare session
  id after a rename. That path is now taken only for **non-local** refs
  (`web_workspace.go:91-105`); a local session — live or ended — gets
  `liveTitle` / `pastTitle`, which re-read the on-disk `meta.json` fresh
  (`web_workspace.go:112,175`, `web_format.go:132-157`). So the detail object's
  `title` *should* now agree with the AppWire navigation resource and the meta file. It is worth a
  spare glance during step 3: if it still reports a short id after a confirmed
  rename, that is a live regression worth its own kata. The remaining honest
  fallback is `hubcore.ShortID` when the past index has never seen the id at
  all (`web_format.go:132-138`) — a brand-new live session can legitimately be
  in that window.
- **Compaction on a short transcript can finish before your first poll.** Don't
  read "already done at poll #1" as "didn't run"; confirm via the
  `context_used` / `context_pressure` drop, or
  `go run ./cmd/evener doctor transcript <SID> --format outline`.
- **Don't stub `window.confirm`.** Nothing in the rail's rename or delete paths
  uses it; every confirmation is an in-app `Dialog`. A stub silently does
  nothing and makes a skipped interaction look handled.
- Use a dedicated Chrome profile; the auth cookie is not port-scoped. Keep the
  `location.port` assertion inside every `eval`.
