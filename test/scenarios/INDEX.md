# Scenario index

One-line summaries. Files are loosely grouped; the names start with
the area they exercise.

## CLI surface

- `cli-help.md` — `--help` prints proper usage across serf-tui +
  serf subcommands (kata `hsmm`).
- `cli-sibling-binary.md` — serf-tui finds serf-hub next to itself
  via `os.Executable` + `EvalSymlinks` (kata `a4w6`).

## Authentication

- `auth-oauth-wins.md` — stored OAuth state takes precedence over
  `OPENAI_API_KEY` env var, daemon-side and hub-UI-side.
- `auth-device-autodetect.md` — `serf openai login` auto-picks
  device vs browser flow based on env / platform (katas `4f93712`,
  `8b7762c`).
- `cli-device-code-flow.md` — full `--device` round trip: Part A
  scripts the device-code request + cancellation against the live
  OpenAI endpoint; Part B documents the manual browser-side
  authorization (kata `p16h`).
- `auth-device-poll-concurrent.md` — `--device` poller detects a
  parallel login that wrote `auth.json` while the device code was
  still waiting for a human, and exits cleanly instead of running
  the 15-minute timeout (kata `24p1`).

## Spawn form (`/new`)

- `spawn-stale-model-cleared.md` — retired models in localStorage
  are detected against the harness list and cleared with an inline
  notice; sweep also drops sibling per-project blobs (katas `bvfh`,
  `hnvv`).
- `spawn-empty-prompt-blocked.md` — empty/whitespace prompt shows
  inline error, no spawn POST (kata `xj9j`).
- `spawn-picker-enter-noop.md` — pressing Enter inside the model
  picker search selects first match and prevents form submit (kata
  `t13x`).

## Session workspace

- `tui-workspace-navigation.md` — serf-tui dashboard + session
  keyboard navigation via tmux send-keys / capture-pane: arrow
  movement, project collapse/expand, command palette, browse vs
  compose mode, double-Ctrl+C exit (kata `57be`).
- `state-stuck-processing-display.md` — past session whose last
  turn errored shows `ended`/`error` not `processing` (kata `r6y9`).
- `reconnect-auto-resume.md` — killing the daemon and sending a
  new turn transparently spawns a fresh daemon and replays (katas
  `e465`, `t65c`, `ws5f`, `xcas`).
- `reconnect-button-source-hub.md` — kill -9 mid-stream surfaces a
  `source=hub` diagnostic; the card shows a `Reconnect & retry`
  button (not `Retry turn`) that re-issues the turn through the
  same auto-resume path (kata `0e7h`).
- `meta-flush-on-completion.md` — `meta.json` `turn_count` tracks
  committed exchanges across happy + error exits (katas `3tgv`,
  `ztne`, `wnfz`).
- `workspace-title-bar-actions.md` — interrupt / compact /
  shutdown title-bar actions hit the daemon end-to-end (kata
  `gx92`; surfaced kata `k7t8` — interrupt is unwired in
  production).
- `tui-steer-live-turn.md` — active turns put the composer in
  queue mode; Ctrl+S is the explicit force-steer path that injects
  a `STEERING` transcript entry before the turn ends. Live-tested
  against `anthropic/claude-haiku-4-5-20251001` (kata `mn4z`).
- `web-steer-live-turn.md` — web-hub counterpart: both the
  input-area `steer` button and the `/steer` ⌘K palette command
  inject a STEERING transcript entry mid-turn, the conversation
  pane renders the steering divider, the model adapts, and the
  session continues to idle (kata `a08v`; surfaced kata `gsv2` —
  palette closes silently if the turn ends between opening and
  submit, masking the no-active-turn error).
- `tui-queue-then-completes.md` — typing during a processing turn
  queues the message; the current turn finishes and the queued
  message processes as the next user turn (kata `111a/0bq1` TUI).
- `tui-queue-then-drain-as-steer.md` — Ctrl+S drains all queued
  messages as a single STEERING joined by `\n\n`; the agent
  receives them mid-turn (kata `111a/0bq1` TUI).
- `web-queue-then-completes.md` — web counterpart of the
  queue-then-completes scenario.
- `web-queue-then-drain-as-steer.md` — web counterpart: Shift+Enter
  (or the `send as steer` button) drains the pending queue as a
  steer event.
- `tui-interrupt-live-turn.md` — serf-tui `/interrupt` palette
  command fires against a real mid-turn session via
  `tmux send-keys`; verifies state transitions to `closed` and
  transcript preserves partial output (kata `9sck`; surfaced kata
  `4yvd` — palette gates on stale capabilities mid-turn).
- `web-steer-in-idle-fails-fast.md` — verifies optimistic-rendering
  reject path (kata wymv): clicking "send as steer" in IDLE returns
  Unavailable; pending chip flips to .optimistic-failed with retry
  link.
- `web-steer-success-reconciles.md` — happy path: pending pulse
  appears on click, replaced by authoritative STEERING divider when
  the daemon's serf/steering/injected notification arrives.
- `tui-steer-in-idle-fails-fast.md` — TUI counterpart of
  web-steer-in-idle-fails-fast; Ctrl+S keybind in IDLE.
- `tui-steer-success-reconciles.md` — TUI counterpart of the success
  reconcile; spinner prefix replaced by authoritative steering.

## Goal engine (`/goal`)

- `web-goal-set-and-complete.md` — set a `/goal` from the ⌘K palette
  and watch it drive to completion: appwire `goal/set` accepted (A6
  capability gate), status pill `goal <status> · <N> turns`, the
  compact continuation marker rendered as a "Goal" systemMessage (B6,
  not the full prompt), and the terminal report.
- `tui-goal-set-and-complete.md` — TUI counterpart: `/goal <objective>`,
  the `goal <status> <iter>` header chip, `/goal status`, the B6
  continuation marker, and completion.

## Image attachments

End-to-end coverage for the composer image-attachment surfaces (kata
`2frx`; the implementation chain was katas
`t5j6 → c7pv/r6a1 → xy3t/65mm → re91/v80q`). Each scenario builds a
tiny PNG fixture, drives the gesture through the live UI, spawns or
sends through the live hub against
`anthropic/claude-haiku-4-5-20251001`, and confirms the transcript
contains a `ContentImage` part on the `USER_INPUT` message and the
assistant references the image content.

- `web-paste-image-from-clipboard.md` — synthetic `ClipboardEvent`
  with a DataTransfer holding a 64×64 PNG; the `attachComposerImage
  Handlers` paste listener canvas-re-encodes and pushes onto the
  pending bag. Verified live 2026-05-18.
- `web-drag-drop-image.md` — full dragenter/dragover/drop sequence
  on `[data-drop-zone]` with a synthetic DataTransfer; visual
  `.drop-active` toggles, chip renders, spawn ships bytes. Verified
  live 2026-05-18.
- `web-file-picker-image.md` — CDP `file_upload` (or
  `input.files = dt.files`) on the hidden `[data-file-picker]`
  fires the change event; the helper ingests and re-encodes.
  Verified live 2026-05-18.
- `tui-paste-image-from-clipboard.md` — Xvfb + xclip seeds the
  clipboard, TUI Ctrl+V runs the multi-source clipboard read
  (`clipboard_system.go`) and pushes a `serf-clipboard-*.png` chip
  onto pendingAttachments. Verified live 2026-05-18.
- `tui-paste-image-path.md` — `tmux load-buffer` +
  `tmux paste-buffer -p` delivers a bracketed-paste containing an
  on-disk PNG path; `handleBracketedPaste` attaches instead of
  inserting text. Verified live 2026-05-18.

## Transcript / debug

- `transcript-endpoint-url.md` — api_call entries record
  `response.endpoint_url` (typed) + `response.raw.endpoint_url`
  (legacy) across all adapters (katas `v5pm`, `dyph`).

### Session-transcript tools (`find_session_transcripts` / `read_session_transcript`)

Live end-to-end coverage for the two read-only agent tools defined in
`docs/tools/transcripts.md`. Each spawns a real `./serf` CLI run against
a real model (`oai-work/gpt-5.5`), shares a per-project state-dir bucket
across runs (same `--dir` ⇒ same bucket — the precondition for
cross-session discovery), and asserts the tool's documented response
shape (refs, snippets, scan stats, window headers, Turn numbering).

- `transcript-find-catalog-read-markdown.md` — `find({})` returns the
  metadata-only catalog (no snippets/scan); a second run picks the
  earlier session by title/`approx_turns` and reads it in markdown,
  confirming the default last-40 window header self-announces.
- `transcript-find-by-query-content-search.md` — `find({query})` turns
  on the bounded content scan: matches carry `snippets`, response carries
  `scanned`/`scan_truncated`; decoy sessions are excluded (agent-tool
  complement to the `⌘K` overlay in
  `search-finds-content-across-sessions.md`).
- `transcript-read-outline-range-expand-turn.md` — the read ladder:
  outline maps the shape, a `range` taken from the outline reads a span,
  `expand_turn` opens one truncated result whole; asserts the outline's
  Turn numbers are exactly what `range`/`expand_turn` accept (one
  numbering, no translation).
- `transcript-multi-session-create-find-read.md` — THE headline:
  Session B discovers Session A by content (`find({query})`), gets A's
  `transcript_ref`, and reads it to reconstruct A's work — B is never
  handed A's ref. Makes the bucket-sharing precondition explicit.
- `transcript-subagent-audit-children-of.md` — a parent spawns a
  subagent; `find({children_of:"<parent ref>"})` enumerates the child
  (kind `subagent`, `parent_ref` set, no transcript opened), then an
  outline + range read judges whether the child actually ran the
  commands it claims (the delegation trust-but-verify loop).
- `transcript-read-jsonl-debug-hatch.md` — `format:"jsonl"` returns raw
  NDJSON (header + system prompt + api_call lines) with a hint to
  re-read as markdown; asserts the agent picks markdown, not jsonl, when
  the goal is comprehension.
- `transcript-find-scope-all-projects.md` — `scope:"all_projects"`
  (with a shared `--state-dir`, distinct `--dir`s) finds a session in a
  different project bucket; default scope misses it, the cross-bucket
  hit comes back as a `proj:<bucketHash>:<id>` ref, and that ref reads.

## Subagent control plane (CLI)

- `subagent-cancel-runaway.md` — `cancel_agent` aborts a child mid-run
  (child told to `sleep 30`) returning `status:"cancelled",
  success:false`, then `resume_agent` starts a fresh run on the
  preserved history and completes — proving cancel keeps the child
  resumable (the child analog of Esc, vs close which destroys).
- `subagent-list-and-output.md` — `list_agents` enumerates a live child
  (status/reason/task/transcript_ref); `subagent_output(view:result|
  outline)` peeks it WITHOUT consuming (a second peek still returns the
  result) and REDACTS a planted `sk-LIVETEST123456` to `«redacted»`.
  Scope: redaction is subagent_output-only — the token stays verbatim in
  the spawn result and the `list_agents` task field.
- `subagent-close-retains.md` — `close_agent` destroys the child session
  but RETAINS a `closed` record: default `list_agents` HIDES it
  (`count:0`), `include_closed:true` SURFACES it with `status:"closed"`
  and the retained `reason:"completed"`.
- `subagent-notification-wake.md` — the proactive completion wake
  (serve-mode ONLY, driven through the hub): a parent spawns NON-blocking
  (`spawn_agent blocking:false`) and ENDS its turn (goes idle without
  waiting); when the child reaches a terminal state ~15s later, serf wakes
  the parent by appending the `<subagent-notification ... status="completed"
  ...>` block as a `STEERING` turn that drives a fresh model turn. Proof is
  the post-idle `STEERING` entry in the PARENT transcript; the woken model
  then `subagent_output`s the child and surfaces `CHILD_DONE_42`. One-shot
  `serf run` does NOT deliver (no later turn to wake). Verified live against
  `openai/gpt-5.4-mini`, first attempt.

## Regression sweep (older surfaces)

- `credentials-page-displays-sources.md` — `/credentials` shows
  correct effective source per provider with env/file shadow
  badges.
- `index-sidebar-lists-projects.md` — hub home page sidebar
  enumerates projects + sessions.
- `search-finds-content-across-sessions.md` — `⌘K` overlay
  searches transcripts.

## End-to-end software development

These are the lightweight "does serf actually build software"
smoke tests. They spawn a real session against a real model,
use a run-specific `mktemp -d` for hermeticity, and verify the
output by running it.

- `dev-hello-script.md` — agent writes hello.py + runs it. Most
  basic write+exec loop. Verified live against `openai/gpt-5.5`.
- `dev-fix-broken-script.md` — agent reads, edits, and re-runs a
  syntactically-broken Python script. Exercises the
  read→edit→exec→verify flow that most real serf use looks like.
- `dev-plugin-superpowers-brainstorming.md` — clones
  `obra/superpowers` into a tmpdir, points serf at it via
  `launch_overrides.pluginDirs`, and confirms the `brainstorming`
  skill loads into the agent's catalog. Plugin-discovery smoke
  test; doesn't run brainstorming.

## Plugin hooks (lifecycle)

- `hooks-claude-compat-matcher.md` — a Claude-style
  `hooks/hooks.json` loaded via `--plugin-dir` gates a real
  `shell` tool call: `PreToolUse` matcher `"Bash"` fires (exact,
  not substring) on serf's shell tool (Claude name `Bash`), runs
  in exec-form (`args`, no shell), and exits 0 without blocking; a
  `"Bas"` matcher does NOT fire (commits `a4685d3d`, `28bd828e`).

## Open katas surfaced while writing scenarios

- `96pr` — legacy diagnostics with stored source=serf never get
  reclassified (sharp edge).
- `6bdb` — serf-hub doesn't find sibling serf binary either
  (same shape as a4w6, sharp edge).
- `k7t8` — workspace interrupt button is non-functional
  (`cancelFunc` never wired in `cmd/serf/serve.go`); covered by
  `workspace-title-bar-actions.md`.
- `4yvd` — serf-tui palette gates `/interrupt` on stale
  capabilities cached at session-open; needs a `/status` refresh
  to unlock mid-turn (workaround documented in
  `tui-interrupt-live-turn.md`).
- `gsv2` — web `/steer` palette closes silently when the turn
  ends between opening and submit; the `no active turn` toast
  fires but the dialog still closes, so the user thinks the steer
  went through (surfaced by `web-steer-live-turn.md`).
- `1pgw` — production CSP `img-src 'self' data:` blocks the
  `blob:` URL the composer-attachments helper creates in
  `reencodeToPng`; every web paste / drop / file-picker rendered
  "Not an image: <name>" until the fix. Discovered while writing
  the `web-*-image.md` scenarios (kata `2frx`). Fix: add `blob:`
  to `cmd/serf-hub/security.go:CSPMiddleware`. The jstest harness
  stubs `Image` so unit tests miss this; only live browser
  verification catches it.

All filed via `kata create`; not blocking.
