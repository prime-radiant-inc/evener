# spawn-empty-prompt-starts-dormant: a blank prompt starts a dormant session

**What this covers**: kata `ytpa`. The spawn pane's prompt placeholder
reads `What should the agent work on? Leave blank to start it dormant.`
(`panes/spawn/Spawn.tsx:528`). Submitting with the prompt empty must
honour that promise: create the session, start no turn, open it, and
say so on the rail row.

This scenario **replaces** `spawn-empty-prompt-blocked.md`, which
asserted the opposite. That guard (kata `xj9j`, commit `7743e7f`) was
defensive cover for an *accidental* empty submit — pressing Enter in
the model-picker search bubbled into a form's implicit submit — and
that root cause was fixed separately as kata `t13x`. The React spawn
pane has no `<form>` element at all (unlike the session composer,
`panes/session/composer/Composer.tsx:756`), so the accident cannot
recur, while the guard went on contradicting the placeholder.

The dormant case is a three-layer contract, each layer independently
checkable:

- **Wire**: `hubThreadStart` calls `StartTurn` only when
  `len(params.Input) > 0` (`cmd/serf-hub/app_threadlifecycle.go:182-197`).
- **Client**: `buildInput` pushes a text item only `if (text.trim())`,
  and sends it UNTRIMMED when it does
  (`panes/spawn/startThread.ts:39-45`). The REST shim does the same:
  `inputItemsForText` returns nil for a whitespace-only prompt
  (`cmd/serf-hub/web_session.go:177-182`).
- **Presentation**: the fact rides beside the state, not inside it —
  `hubcore.TreeNode.Dormant` (`cmd/serf-hub/internal/hubcore/tree.go:270-280`),
  wired to `/api/tree` as `"dormant"` (`hubapi/types.go:120`,
  `web_api_tree.go:1334`) and to the rail as
  `[data-testid="rail-row-not-started"]`.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. This
card names only `spawn-prompt-card`, `spawn-submit`, `rail-row-not-started`
and the session pane's empty state; anything else, grep `data-testid` in
`cmd/serf-hub/frontend/src` rather than inventing one.

## Pre-state

- Hub running with auth set up, on an isolated `HOME` and a
  kernel-assigned port (never Jesse's port 9180) — see the Setup
  checklist in `docs/agentic-testing.md`. Assert `location.port` in the
  browser before trusting anything you see.
- **`past_index_rebuild_interval = "2s"` in the hub's `hub.toml`.** The
  default is 60s (`cmd/serf-hub/config.go:62,134-135`) and `dormant` is
  read out of session meta via the Past index
  (`navigationSnapshotInputs`, `web_api_tree.go:387-390`), so on the
  default a just-spawned session reports `dormant:false` for up to a
  minute. See Sharp edges — that is the single most likely false
  negative in this card.
- For the browser steps only: a frontend built with `make build-web`
  *before* the hub binary, or the hub serves `dist/PLACEHOLDER` and no
  app at all (rebuild matrix item 3 in the runbook).
- A working directory that exists, so the cwd preflight passes without
  diverting into the "create it?" dialog.

## Steps

### Browser-free (REST + wire; run these first, they carry the exact assertions)

1. `POST /api/spawn` with an **empty** `prompt` and no `items`:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"\",\"model\":\"$MODEL\",\"working_dir\":\"$tmpdir\",
          \"harness\":\"serf\",\"access_mode\":\"full\",\"agent\":\"default\",
          \"launch_overrides\":{}}" "$HUB/api/spawn")
   SID=$(echo "$resp" | jq -r '.session_id')
   ```
2. Poll `GET /api/sessions/local:$SID` for a few seconds and record
   `.state` and `.active_turn_id` throughout.
3. Poll `GET /api/tree` until the session's node appears, then read its
   `dormant` field:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/tree" \
     | jq --arg ref "local:$SID" '[.. | objects | select(.ref? == $ref)
         | {ref, state, dormant, age}] | .[0]'
   ```
4. Read the on-disk transcript: `go run ./cmd/serf-doctor transcript
   "$SID" --state-dir "$state" --format outline --range last:20`.
5. Repeat step 1 with a whitespace-only prompt (`"   \n  "`). The
   outcome must be identical — that is `inputItemsForText`'s
   `strings.TrimSpace(text) == ""` branch.

### Browser (the UI half: does the pane actually offer the blank submit, and does the rail say so)

6. Navigate to `/auth?token=$TOKEN&next=/new`. Leave the prompt textarea
   (`[data-testid="spawn-prompt-card"]`, `aria-label="Prompt"`)
   completely empty. Attach nothing. Set the working directory to a path
   that exists.
7. Click `[data-testid="spawn-submit"]` (labelled `Spawn`; it reads
   `Spawning…` while busy — `Spawn.tsx:558,562`). `⌘↵` / `Ctrl+Enter` in
   the textarea reaches the same `handleSpawn` via `handlePromptKeyDown`
   (`Spawn.tsx:363-369`); exercise both.
8. Read the pane you land on, and the rail row for it:
   ```javascript
   ({
     port: location.port,                                  // page-identity check, always
     path: decodeURIComponent(location.pathname),          // see Sharp edges: %3A
     toasts: document.querySelector('[aria-label="Notifications"]')?.textContent ?? "",
     emptyTitle: document.body.textContent.includes("Send the first message"),
     emptyHint: document.body.textContent.includes("This session hasn't started yet."),
     composer: !!document.querySelector('[data-testid="composer-input-card"]'),
     turns: document.querySelectorAll('[data-testid="turn-block"]').length,
     notStarted: document.querySelector(
       '[data-session-ref="local:<SID>"] [data-testid="rail-row-not-started"]')?.textContent,
   })
   ```
   Substitute the literal `SID` from step 1 into that selector before
   evaluating — the browser has no shell variable.

## Expected

- **Steps 1-2 (wire, exact)**: HTTP 200 with a `ref`/`session_id`. The
  session reaches `state: idle` and **never** reports `active`, and
  `active_turn_id` stays empty throughout. Falsify: a 4xx/5xx, or any
  poll catching `state: active` / a non-empty `active_turn_id` — a turn
  started from an empty input.
- **Step 3 (dormant flag, exact)**: the node exists with `"dormant":
  true` and `"state": "idle"`. Both, together: `idle` alone is also what
  a session that ran and finished reports, which is the whole reason
  `Dormant` exists as a separate field. Falsify: `dormant` absent or
  `false` after the index has demonstrably caught up (the node itself is
  present, so the rebuild has run).
- **Step 4 (transcript)**: zero turns. Falsify: any `USER_INPUT` turn,
  especially one carrying a `{"kind":"text","text":""}` part.
- **Step 5**: byte-identical outcome to steps 1-4.
- **Step 6-7 (submit is not blocked)**: no error toast — specifically
  nothing reading `Prompt is empty.` — and the page navigates away from
  `/new`. Falsify: a toast, or the pane staying put.
- **Step 8 (pane + rail)**: `path` decodes to `/s/local:<SID>`;
  `emptyTitle` and `emptyHint` are both true (`EmptyTranscript`'s
  zero-turn, not-active branch, `panes/session/Session.tsx:79-84`);
  `turns` is 0; the composer is present and focusable below it
  (placeholder `Message the agent…`), and typing there and sending
  starts the first turn normally. `notStarted` is exactly `Not started`
  (`shell/rail/RailRow.tsx:563-565`). Falsify: the pane shows
  `Waiting for the first reply` (the active branch — a turn started), or
  the rail row shows a relative age where `Not started` belongs.

## Cleanup

- `POST $HUB/api/sessions/local:$SID/shutdown` for every session you
  spawned — a dormant session still holds a daemon process. The old
  `$HUB/s/$SID/shutdown` form-POST shim is gone (404s silently), see the
  runbook's "The REST surface, and what is no longer on it".
- Kill the hub by the PID you captured; `rm -rf` your own `$run` dir.

## Sharp edges

- **`dormant` lags the spawn by one Past-index rebuild.** The tree's
  metas come from `cfg.Past.AllMetas()` and a brand-new session enters
  that index only on the periodic `Rebuild()` — `RefreshOne` updates an
  *already-indexed* id and nothing calls `Rebuild()` on the
  `thread/start` path. `dormantFor` reports false without meta on
  purpose ("the claim is only ever made from evidence",
  `hubcore/tree.go:635-645`). On the default 60s interval that reads as
  a regression for a full minute. Set `past_index_rebuild_interval` short
  and poll rather than sampling once.
- **`Not started` is suppressed on a signal row, by design.** It renders
  only when the row is otherwise quiet — `saysNotStarted` is
  `session.dormant === true && !showsGloss`
  (`RailRow.tsx:473-475`), and `showsGloss` is true for the signal
  states (`RailRow.tsx:487`). A dormant session handed a prompt a second
  ago is genuinely `active`, and a row still calling it "Not started"
  would be flatly wrong. So assert this on an untouched dormant session,
  not one you have already messaged. The dropped age moves into the
  row's `title` tooltip alongside the words `not started`
  (`rowTooltip`, `RailRow.tsx:447-463`).
- **The post-spawn URL percent-escapes the colon.** `paneToURL` builds
  `/s/${encodeURIComponent(ref)}` (`shell/routing.ts:93-96`), so
  `location.pathname` reads `/s/local%3A<SID>` after a spawn navigation
  and `/s/local:<SID>` when you type it. Decode before comparing. A bare
  `/s/<SID>` with no colon renders "Page not found" by design.
- The prompt is trimmed only for the emptiness decision. A real prompt
  with surrounding whitespace (`"   write me a haiku   "`) is still
  sent **untrimmed** (`startThread.ts:41`); only an all-whitespace
  prompt takes the dormant path.
- The cwd guard is unaffected and still fires first: an empty or
  relative working directory aborts the submit before `thread/start`,
  with the validator's own message — `path is required`,
  `absolute path required`, `path is not a directory`
  (`panes/spawn/preflight.ts:21`). A path that merely doesn't exist yet
  is *not* an abort; it opens the in-form "create it?" dialog.
- An attachment with no text was always allowed through and still is —
  that submit carries `input: [{type:"image", ...}]`, which is
  non-empty, so it starts a turn rather than going dormant
  (`startThread.ts:42-44`).
