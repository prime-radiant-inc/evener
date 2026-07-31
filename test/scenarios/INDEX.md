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
- `spawn-empty-prompt-starts-dormant.md` — empty/whitespace prompt
  starts a dormant session: `input: []` on the wire, no turn, no
  error (kata `ytpa`; replaces the retired `spawn-empty-prompt-blocked`
  card, kata `xj9j`).
- `spawn-keyboard-contract.md` — the pane's whole keyboard contract:
  Enter in the model picker selects the highlighted row and stops there,
  bare Enter in the prompt inserts a newline, and only ⌘/Ctrl+Enter
  spawns — with the structural reason (no `<form>`, no `onSubmit`)
  checked alongside, since it is what makes the absence assertions mean
  anything (kata `rjc5`; replaces the retired `spawn-picker-enter-noop`
  card, kata `v0hg`).
- `spawn-failure-ux-post-ws5.md` — the three remaining spawn-failure
  classes (bogus model id, working dir that doesn't exist, harness
  binary the hub can't execute) come back from `POST /api/spawn` as
  legible errors rather than buried-stderr 500s. Browser-free.

## Session workspace

- `tui-workspace-navigation.md` — serf-tui dashboard + session
  keyboard navigation via tmux send-keys / capture-pane: arrow
  movement, project collapse/expand, command palette, browse vs
  compose mode, double-Ctrl+C exit (kata `57be`).
- `state-stuck-processing-display.md` — deterministic scripted-provider
  checks prove recoverable failed turns return the owning daemon to `idle`;
  an optional live check confirms Hub projections follow that owning state.
- `reconnect-auto-resume.md` — killing the daemon and sending a
  new turn transparently spawns a fresh daemon and replays (katas
  `e465`, `t65c`, `ws5f`, `xcas`).
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
- `lazy-transcript-loading.md` — a large transcript cold-loads only the
  latest window (`thread/read{turnLimit}`) and pages older turns in as
  the reader nears the top, prepended above the live content without
  moving what they were reading (commits `49445a1d`, `2d36b225`).
- `attention-needs-you-end-to-end.md` — the attention & status model's
  single-tab happy path: an `awaiting` session drives its own rail row,
  the tab title, and the favicon live, plus the interrupt guard-rail.
  Notifications must be opted into before the first load.
- `status-vocabulary-roundtrip.md` — the attainable your-move and
  question-waiting states read the same across the web rail, the TUI
  dashboard row, and the TUI session header; a deterministic gate pins
  the whole `hubapi.StateWord` vocabulary (Track A §1-2).

## Compaction (the `compact` tool)

- `compact-tool-pins-note-and-persists.md` — a real model invokes the
  agent-facing `compact` tool: the note is pinned, re-stamped through
  the compaction the call forces, and reaches `meta.json`.
- `compact-note-survives-resume.md` — the pinned note is restored
  across a real process restart from structured `SessionMeta.PinnedNote`
  rather than a history scan, and is re-stamped on the next compaction.

## Model switching

- `web-model-switch-mid-session.md` — two web clients converge on a
  live `thread/model/set` switch without reload; mid-turn and
  queued-input switches stay rejected with no state change.
- `tui-model-switch.md` — serf-tui `/model` switches live, header
  and dashboard row update from `thread/model/changed`, and the
  switch marker (`Switched model: <old> → <new>`) survives a fresh
  attach.
- `model-switch-resume.md` — switch → kill the daemon → resume runs
  the next turn on the switched model (N3 persistence).
- `tui-effort-command.md` — serf-tui `/effort` and the web `⌘K`
  "Set reasoning effort" command both show only the current model's
  levels; both surfaces render current model + effort on a cold
  attach.
- `model-switch-providers-live.md` — live cross-provider switch
  ladder against the `serf serve` daemon's own HTTP surface (marker
  persistence, `response_model` per leg, effort-ladder
  re-derivation, thinking-absence on the wire); AC 8.
- `reasoning-effort-providers.md` — reasoning effort end-to-end on
  Kimi and Anthropic: `llm.ClampReasoningEffort`, the forced
  `tool_choice` downgrade under thinking, and the `max_tokens` vs
  thinking-budget reconciliation (symptom: `'xhigh'` rejected as an
  unsupported `reasoning_effort` with a 400).

## Model picker (Track B)

Coverage for the model picker across all three surfaces it renders on —
web spawn (`/new`), web settings (`/settings/launch-serf`), and the
TUI's `n` new-session picker — over the embedded LiteLLM catalog and
the Past index's Recent list.

- `model-picker-fresh-install-no-recent.md` — with an empty Past index
  every picker shows only the provider-grouped catalog and never an
  empty or degenerate "Recent" group (Tasks 1-3).
- `model-picker-recent-reflects-last-5-global.md` — Recent is the 5
  most-recently-touched distinct `(provider, model)` pairs,
  most-recent-first, and is the same list whichever `cwd` the caller
  scopes the request to.
- `model-picker-badges-match-catalog-data.md` — the rendered capability
  badges (tools / vision / reasoning / web-search / context window /
  max output / price) match both `/api/models?diagnostics=1` and the
  embedded catalog's raw LiteLLM data; found and fixed a real
  spawn-picker data-source bug.
- `model-picker-dated-snapshot-sorts-last.md` — inside a provider group
  the bare family id renders before its dated snapshot
  (`claude-opus-4-6` before `claude-opus-4-6-20251101`) whatever order
  the live listing returned (Tasks 4, 11).
- `model-picker-uncatalogued-model-still-renders.md` — graceful
  degradation: a live model the catalog doesn't know renders with its
  name and provider-qualified id, carries no badge/cost/context
  metadata at all, stays selectable, and launches (Ollama is the real,
  un-stubbed example).

## Goal engine (`/goal`)

- `web-goal-set-and-complete.md` — set a `/goal` from the ⌘K palette
  and watch it drive to completion: appwire `goal/set` accepted (A6
  capability gate), status pill `goal <status> · <N> turns`, the
  compact continuation marker rendered as a "Goal" systemMessage (B6,
  not the full prompt), and the terminal report.
- `tui-goal-set-and-complete.md` — TUI counterpart: `/goal <objective>`,
  the `goal <status> <iter>` header chip, `/goal status`, the B6
  continuation marker, and completion.

## `ask_user` (interactive questions)

End-to-end coverage for the `ask_user` tool (design:
`docs/superpowers/specs/2026-07-03-ask-user-question-tool-design.md`): the model asks
structured questions, the asking round ends the turn into the `awaiting` state, and the
reply is the user's next ordinary message.

- `ask-web-answer.md` — a posted question ends the turn into `awaiting`; the web card
  renders the model's real options, an answer + note composes and reaches the model as the
  next user message.
- `ask-tui-answer.md` — TUI cold-attach: chip + card visible, the overlay never auto-opens,
  `Esc` defers without discarding, and typed prose in the composer answers just as well as
  submitting through the overlay.
- `ask-cross-session-notify.md` — from a *different* session's viewport, an awaiting
  session surfaces in the sidebar's NeedsYou tier + count badge and fires the OS
  notification channel.
- `ask-two-clients.md` — two clients on one awaiting session: the loser's card converges to
  the winner's settled echo; a losing submit never produces a second user message.
- `ask-restart-rederive.md` — a daemon killed with an unanswered ask reports `awaiting` on
  its first `/status` after restart, and the form is still answerable.
- `ask-noninteractive-invisible.md` — `ask_user` is unregistered (not merely disabled) in
  `--non-interactive` and one-shot sessions.
- `ask-subagent-invisible.md` — a delegate can neither see nor call `ask_user`; notes that
  the `grant_tools` protected-grant rejection is deterministic-only today (no live caller
  yet — see the card's Sharp edges).

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

## Inline output images

End-to-end scenario-card coverage for output images produced by tools in the
web UI. These cards are deterministic: use local temp files plus scripted
provider/tool behavior, not live provider credentials or network access.

- `read-image-tool-result-inline.md` — an image-byte tool result projects an
  output-image descriptor and renders a thumbnail under the producing tool row.
- `written-image-inline-after-reload.md` — a structured file-writing tool writes
  an image under session cwd; live and replay/reload both keep the preview under
  the same row.
- `shell-generated-image-path-inline.md` — a shell row that creates an image
  under cwd and prints the relative path receives a conservative server-validated
  `/doc/image` preview.
- `unsafe-image-path-ignored.md` — out-of-cwd, traversal, external, missing,
  non-image, and SVG candidates do not render previews and do not fail the row.
- `output-image-lightbox-and-pane.md` — an output-image thumbnail opens the
  shared lightbox and, for valid same-origin stable URLs, offers open-beside.

## Transcript / debug

- `transcript-endpoint-url.md` — canonical `api_attempt` records identify the
  sanitized HTTP endpoint actually used across provider adapters.

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
  `expand_turn` returns byte-paged exact transcript-v2 JSONL (16 KiB default,
  64 KiB hard maximum); the small 1..200 fixture fits one default page. Asserts
  the outline's Turn numbers are exactly what `range`/`expand_turn` accept (one
  numbering, no translation).
- `transcript-multi-session-create-find-read.md` — THE headline:
  Session B discovers Session A by content (`find({query})`), gets A's
  `transcript_ref`, and reads it to reconstruct A's work — B is never
  handed A's ref. Makes the bucket-sharing precondition explicit.
- `transcript-subagent-audit-children-of.md` — a parent starts a delegate
  job; `find({children_of:"<parent ref>"})` enumerates the child (kind
  `subagent`, `parent_ref` set, no transcript opened), then an outline +
  range read judges whether the child actually ran the commands it claims
  (the delegation trust-but-verify loop).
- `transcript-read-jsonl-debug-hatch.md` — `format:"jsonl"` returns bounded
  semantic NDJSON (one header, then entries); provider attempts require an
  explicit API-log summary and attempt/body expansion. Asserts the agent picks
  markdown, not JSONL, when the goal is comprehension.
- `transcript-find-scope-all-projects.md` — `scope:"all_projects"`
  (with a shared `--state-dir`, distinct `--dir`s) finds a session in a
  different project bucket; default scope misses it, the cross-bucket
  hit comes back as a `proj:<project-id>:<id>` ref, and that ref reads.

## Job control (CLI)

- `subagent-cancel-runaway.md` — `job_stop` stops a long-running delegate
  job, then `delegate_send(on_idle="start")` targets the delegate_id to
  continue the preserved child conversation and complete a shorter follow-up.
- `subagent-list-and-output.md` — `job_list` enumerates a delegate job and
  `job_read_output` peeks the result twice without consuming or hiding it.
- `job-shell-lifecycle.md` — the shell tool's whole job-capable
  lifecycle: foreground inline result, a nonzero exit reported honestly
  rather than hidden, `background: true` launch-and-return,
  `max_runtime_ms` killing a runaway into `stopped`/`run_timeout`, and
  the complete-or-handle output window.
- `job-stop-and-children.md` — `job_stop` lands `cancelled` /
  `stopped_by_parent` with retained output still readable (stopping
  deletes nothing), `max_wait_ms` makes the stop call itself wait for
  finalization, and `include_children` fells the delegate's visible
  nested shell job too.
- `job-list-and-recovery.md` — `job_list` as the authoritative durable
  inventory: `status[]`/`type[]` filters, newest-first ordering, the
  short-job race (a job that finished before any running-filtered list
  is still visible unfiltered), and mid-turn re-orientation before any
  terminal notification has been delivered.
- `job-nested-visibility.md` — a delegate's nested shell job appears
  only under `job_list(include_nested=true)`, reads and stops through
  the single parent-visible `job_id` (routing-if-live, so a confirmed
  stop rather than `not_controllable`), and its output stays readable
  after the delegate is finished.
- `job-notification-semantics.md` — exactly one terminal notification
  per notification-armed job (shell and delegate asserted separately),
  the `<job-notification>` block's exact field set, and fires that land
  mid-turn queueing to the turn boundary batched and without loss.
- `job-restart-durability.md` — `kill -9` mid-job: restart finalizes the
  orphaned `running` record exactly once as `stopped`/`runtime_lost`
  with a stable `terminal_generation`, pre-crash output stays readable,
  and the notification is delivered once and deduped durably across a
  second restart. Runtime loss reads as supervision loss, never as
  command failure.
- `job-delegate-result-schema.md` — `delegate.result_schema` end to end:
  a complying result returns `structured_result_valid: true` inline and
  again via `job_read_output`, a violating one is reported honestly
  (validity false plus a machine-readable reason, no invented result),
  and a resumed turn inherits the original schema.
- `job-send-message-surface.md` — the handle split: a `job_id` handed to
  `delegate_send` is rejected with guidance toward the `delegate_id`, a
  RUNNING delegate takes a live steer with no new job, and an IDLE one
  refuses with `target_idle` unless `on_idle:"start"` explicitly starts
  its next job in the same conversation.
- `job-read-output-blocking-grep.md` — `job_read_output(max_wait_ms,
  grep=…)` waits for the *match* — including a mandatory entry check
  for a match that already landed — not for any new output; the
  one-call "wait until the server prints ready" primitive that keeps
  monitoring away from `job_watch`.
- `job-notification-wake.md` — the proactive completion wake
  (serve-mode ONLY, driven through the hub): a parent starts a non-blocking
  delegate and ends its turn; when the child reaches a terminal state later,
  Serf wakes the parent with `<job-notification ...>` and the woken model
  reads the result with `job_read_output`.
- `job-delegate-wait-no-poll.md` — a parent that delegated its whole task
  and has no independent work ends its turn and waits for the notification
  instead of looping `job_status`; falsifies the polling-loop regression from
  session `01KVXFMMY1QD5CPP6C55V851NQ`.
- `recursion-coordinator-fanout.md` — recursion behind the double
  opt-in (`maxSubagentDepth=2` + per-spawn `delegation_allowance`): a
  granted coordinator (allowance 1) fans out workers (allowance 0,
  hard leaves); asserts the grant ceiling, the live
  `include_descendants` walk (`owner_session_id`/`depth`), the
  drive-down of worker completions into the coordinator's own turns,
  the OWNER-SCOPED rule (the root hears only the coordinator, never a
  worker), and the cascade stop.
- `recursion-deaf-coordinator-drivedown.md` — the design-spec §9
  headline: an IDLE coordinator is driven so its model gets a
  notification turn for its workers' completions, while the root's
  rail shows only the coordinator's terminal (owner-scoped, asserted
  on the coordinator's own transcript).
- `job-watch-observer-snide-thread.md` - observer commentary stays in
  the observer transcript while watch frames carry enough metadata for
  useful sidecar work.
- `job-watch-actually-monty-python-injection.md` - caller `communicate`
  watch frames include content, an observer injects `PYTHON_QUOTE` only
  for external `actually` messages, and causal provenance suppresses
  injection and acknowledgement loops.
- `job-watch-passive-observer-noop-filter.md` - passive observer
  frames that need no action can finish with bare assistant text and no
  tool call, while `assistant.tool` `event_filter` prevents `job_list`
  and failed `read_file` events from waking the observer.
- `job-watch-caller-notification-delivery.md` - delivery is implicit: a
  watch's fires go to the session that created it, waking an IDLE
  creator as a job-notification turn, while N fires against a BUSY one
  coalesce latest-frame-wins into one render per delivery boundary
  (coalescing must not become silence).
- `job-watch-output-match-catchup.md` - `output_match` is
  level-triggered at attach: a watch attached after the token already
  printed fires once for the whole retained scan carrying the last
  matching line, an `output_match`-only watch on a terminal job is a
  one-shot catch-up rather than an error, and `events` on a terminal
  target still fails `target_terminal`.
- `job-watch-caller-send-no-deadlock.md` - the caller-send config that
  wedged session `01KTWN9KEHZ041D77B3GKK572M` is now unreachable
  (`target`/`send` deleted from the schema at `9d0d777c6`, so it dies in
  JSON-schema validation), and the closest legal observer variant
  survives a tool-heavy turn.
- `sidecar-approval-broker-communicate.md` - approval broker watches
  caller `communicate` frames and packages an explicit approval packet.
- `sidecar-drift-detector-communicate.md` - drift detector flags a
  scope-change signal without waking on ordinary assistant turns.
- `sidecar-artifact-freshness-communicate.md` - artifact freshness
  sidecar reports a missing final draft reference.
- `sidecar-memory-reminder-read-file.md` - memory sidecar watches
  successful `read_file` frames with `event_filter` and reminds on a
  project rule.
- `sidecar-secrets-monitor-read-file.md` - secrets monitor reports a
  redacted finding from `read_file` output without repeating the
  secret.
- `sidecar-stuckness-read-file-error.md` - stuckness observer wakes
  only on `read_file` errors and reports a missing-input alert.
- `sidecar-test-triage-shell-frame.md` - test triage observer reads a
  failure signature out of an `assistant.tool` watch frame; pins that a
  parent-source observer gets event payloads, never a cross-session
  read (renamed from `sidecar-test-triage-output-match`, kata
  `f9gn`).
- `sidecar-handoff-packager-job-notification.md` - handoff sidecar
  packages a completed delegate result from a `job.notification`
  frame, and pins the observer read boundary: no cross-session read
  grant, and no `job_read_output` tool at all.
- `sidecar-feedback-governor-communicate.md` - loop governor reports
  repeated-tool-choice risk from an explicit caller frame.
- `sidecar-quality-auditor-communicate.md` - quality auditor flags a
  TODO left in a deliverable draft.

## serf-doctor & forensics

The read-only inspector (`cmd/serf-doctor` over `agent/doctor`) and the
`doctor` agent type that drives it. The watch/provenance material these
cards read is produced by the `job-watch-*` cards above.

- `serf-doctor-forensics.md` — the six corrections the tool exists for:
  `watches` collapses `watch_send_pending` coalescing into distinct
  settled deliveries, `--self-loops` reads the recorded breaker
  telemetry instead of re-deriving from the provenance chain,
  `transcript --count` separates structural tool calls from prose
  mentions, `locate` resolves the per-session `jobs.jsonl` subdir,
  `jobs` folds the log into per-job status-plus-reason off settled
  disk, and each `watches` row carries the state of the job it was
  watching so an unfired watch is not mistaken for broken delivery.
- `doctor-agent-diagnose.md` — the `doctor` agent type runs a real
  LLM-driven diagnosis: the `doctoring-serf` skill loads, the tools run
  through the shell tool, and a healthy session yields zero Findings
  while a real defect yields exactly one schema-correct Finding.
- `doctor-forensics.md` — both halves in one pass: the forensic tools
  (`locate`, `watches` with its target-job join, `transcript --count`,
  `apilog`, `tree`, `jobs`) read settled on-disk state through serf's own
  folds and types, and the doctor agent diagnoses what they report.

## Sidebar (rebuilt)

Live end-to-end coverage for the rebuilt client-rendered sidebar
(`cmd/serf-hub/assets/sidebar.js` + `/api/tree`): needs-you, favorites/Pinned,
top-level active-project session rows, and the row menu. Each card was
verified against a real hub + a real model turn (`openai/gpt-5.4-mini`).

- `sidebar-expand-survives-live-resync.md` — a manually-expanded collapsed
  project's session rows survive a `doResync()` triggered by live activity in
  a different project; surfaced a real (non-blocking) bug where a collapsed
  project's `aria-expanded` renders the literal string `"undefined"`.
- `sidebar-favorite-pinned-across-reload.md` — `POST /api/favorite` is
  reflected in `/api/tree`'s `favorites[]` and renders as a Pinned row that
  survives a hard reload; confirms no `localStorage` favorite cache exists.
- `sidebar-project-delete-full-cycle.md` — `POST /api/project/delete`'s full
  state machine: path-mismatch 400, live-session 409 (files intact),
  post-shutdown 200 (files removed), the open-workspace `/new` redirect, and
  that a re-created project at the same working dir is not silently
  archived.
- `sidebar-rename-live-and-ended.md` — row-menu rename on a live session
  survives its own post-POST resync and a subsequent real compaction turn
  (namer suppression via `name_source:"user"`); rename on an ended session
  edits the meta file directly with no rollback toast. Notes a possible
  follow-up bug: `/api/sessions/<id>`'s detail `title` field doesn't reflect
  a live session's rename the way `/api/tree` and the meta file do.
- `sidebar-archived-testruns-reachability.md` — the collapsed-by-default
  `Archived (N)` and `Test runs (N)` sections end to end: a project's full
  archive→unarchive round-trip via the row menu, and a
  `SERF_SESSION_ORIGIN=test` project's classification into Test runs through
  to its Delete… action and on-disk removal.

## Rail navigation & session refs

- `local-sidebar-url-stability.md` — a local rail row opens its session
  at the one canonical `/s/local:<session-id>` ref (the single ref form
  commit `8cea30ca6` settled on), and clicking the same row again does
  not open a second copy of the session beside the first.
- `codex-sidebar-open.md` — a Codex row opens through the
  source-qualified `/s/<source>:<thread-id>` route into that thread's
  workspace, rather than collapsing to a bare local session id.
- `codex-sidebar-drive.md` — the opened Codex workspace exposes the
  action its source advertises, and the source's own logs show the click
  routed back to the exact `source:thread-id` the row named, with no
  fallback to a local session or a different thread.
- `sidebar-project-order-lastactivity-feel.md` — a just-touched project
  surfaces at the top, promptly. The `LastActivity` comparator is
  already pinned by hubcore fuzz scenarios, so this card covers the
  layer they cannot see: a completed turn propagating into `/api/tree`'s
  memoized `Past.AllMetas()` input.

## Cost display & Display settings (Track C)

Coverage for the consistency-sweep Track C additions: LLM pricing
(`llm/pricing.go`), session/turn cost display (`~$` in the status row,
details panel, and the always-visible per-turn `.turn-meta` badge), the TUI
details-drawer gap-fill, and the Display settings section (Enter-to-send,
Show-cost). `ended-session-metrics-tui-and-web.md` was verified fully live
against a real isolated hub + a real `openai/gpt-5.5` turn; the other four
cards' browser-only assertions were substituted with real server-rendered
HTML fragments (curl), direct CSS/JS source inspection, and the jstest suite
(loads production `renderer.js`/`style.css`, not mocks) because the
`claude-in-chrome` browser tool was unavailable in that session — see each
card's Sharp edges for the exact substitution and what a future live rerun
should replace it with.

- `cost-estimate-display-and-gating.md` — `~$` cost estimate on the status
  row and details panel from a real completed turn; the Show-cost toggle
  CSS-gates all three cost surfaces with no reload.
- `turn-meta-badge-always-visible.md` — the per-turn duration/tokens/cost
  badge (`.turn-meta`) is always-visible in the rendered transcript, NOT
  hover/focus-reveal (plan correction, commit `09ead1c4`).
- `ended-session-metrics-tui-and-web.md` — an ended session's work
  time/tokens/cost surface via both the TUI `/details` drawer and the web
  details panel (the WS2 gap-fill).
- `enter-to-send-toggle-composer.md` — the Enter-to-send Settings toggle
  live-swaps Enter/Shift+Enter between newline-insertion and send/steer.
- `font-size-presets-visible.md` — cycling S/M/L/XL visibly changes text
  size across the sidebar, transcript, and Settings pane itself.

## Regression sweep (older surfaces)

- `credentials-page-displays-sources.md` — `/credentials` shows
  correct effective source per provider with env/file shadow
  badges.
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

## Worktrees (`manage_worktree`)

Live end-to-end coverage for the native worktree tool (branch
`worktree-native-worktree-tools`). These are ergonomics tests as much as
functional ones: each runs against a real provider (billed) and asks
whether the tool's own description and error strings are enough for a
model to do the right thing, so they are also where model tiers separate.

- `worktree-cold-discovery.md` — the prompt never says "worktree",
  "branch", or names a tool; it describes a need. Does the description
  alone lead the agent to `manage_worktree` over a directory copy, an
  in-place branch, or a stash?
- `worktree-create-and-orient.md` — first contact: the agent picks
  `operation: create`, understands the name doubles as the branch, and
  correctly reports the worktree path and branch from the tool result.
- `worktree-lifecycle-merge-back.md` — create → commit → `exit` →
  confirm the work is isolated from the main checkout → `remove`; the
  comprehension test for the isolation boundary.
- `worktree-list-and-cleanup.md` — given one untouched lane and one with
  committed work, is `list` legible enough that the agent disposes of
  only the disposable one (staleness fields, prune vs remove)?
- `worktree-resume-reentry.md` — a session killed while occupying a lane
  re-enters and re-locks it on resume; the foreign-lock variant lands at
  the restore root with a legible notice.
- `worktree-delegate-isolation.md` — the auto-removing path, the
  feature's highest-risk code: a delegate spawned with
  `isolation: "worktree"` gets a lane named for it, and parent close
  disposes that lane only if it is unchanged, keeping any lane with
  commits or a dirty tree resumable.
- `worktree-error-legibility.md` — refusals (dirty removal without
  force, a name collision, removing a branch with unmerged work) give
  the agent enough to recover on the next turn; run on the weakest and
  strongest tiers, where error comprehension fails first.
- `worktree-foreign-lock-legibility.md` — a lane locked by another live
  session refuses `switch`/`remove` with a message naming the owner and
  the recovery path, and the agent reads it instead of thrashing.
- `worktree-ergonomics-findings.md` — not a card: the 2026-07-03 write-up
  of running these cards across tiers (`kimi/kimi-for-coding` vs
  `openai/gpt-5.4-mini`), recording how the tool feels to an agent.

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
