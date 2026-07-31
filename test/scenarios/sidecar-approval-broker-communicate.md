# sidecar-approval-broker-communicate: approval broker reacts to a caller communicate frame

**What this covers**: the human approval broker use case from the
observer-sidecar research. The sidecar should stay idle until the main
agent emits an explicit approval request, then package the decision and
hand it back as an observer callback. Driving mechanism:
`delegate(watch_parent:true)` + observer-installed
`job_watch(source:"parent")` + the observer's terminal
`communicate(end_turn:true)` callback — see `docs/job-control.md`
"Observer and sidecar composition" and the reference card
`job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh `serf` and `serf-hub` from the branch under test.
- Hub running on a free port, never Jesse's real hub on `9180` (see
  the Setup checklist in `docs/agentic-testing.md`);
  `TOKEN=$(cat "$HOME/.serf/auth-token")`.
- `tmpdir=$(mktemp -d -t serf-e2e-approval-broker-XXXXX)`.
- When testing Kimi fluency, spawn with `model` set to
  `kimi/kimi-for-coding`.
- Repeat with `openai/gpt-5.4-mini` when that model is available. If an
  inherited `OPENAI_API_KEY` is exhausted but `serf openai status` shows
  OAuth is signed in, start the test hub with `OPENAI_API_KEY=` and keep
  the normal XDG state home so OAuth remains visible — that means this run
  shares Jesse's real `~/.serf/hub.lock`, auth-token, and credentials with
  his real hub (it will fail to start at all while his real hub already
  holds the flock — check `pgrep -f 'serf-hub.*:9180'` first rather than
  debugging a mysterious startup failure). Session history does NOT have
  to be shared even then: export `XDG_STATE_HOME=$(mktemp -d -t
  serf-e2e-approval-broker-state-XXXXX)` before starting the hub to keep
  this run's sessions out of Jesse's real
  `~/.local/state/serf/projects` — this only relocates session storage
  (`cmd/serf-hub/config.go`'s `DefaultStateGlob`), it has no effect on
  the shared credentials/token/lock above. `rm -rf "$XDG_STATE_HOME"` in
  Cleanup.

## Steps

1. Spawn a parent session via `/api/spawn` with `working_dir=$tmpdir`.
2. Use this prompt:

   > Run the approval broker sidecar scenario.
   > 1. Call `delegate` with `agent_type` "subagent", `watch_parent`
   >    true, and `max_wait_ms` 120000. Its task is: "You are
   >    APPROVAL_BROKER. First: call job_watch with operation 'create',
   >    source 'parent', events ['communicate']. Then call the
   >    communicate tool with exactly APPROVAL_READY and finish. When
   >    later resumed with a message containing 'Watch frame' whose
   >    event message contains APPROVAL_REQUEST, finish with communicate
   >    end_turn true and message exactly APPROVAL_PACKET
   >    decision=approve target=staging-deploy. For unrelated Watch
   >    frames, finish with communicate end_turn true and message
   >    exactly APPROVAL_IGNORED."
   > 2. After the delegate result reports `watching: true` and
   >    APPROVAL_READY, capture the watch_id from its `watches:` line,
   >    then call the communicate tool with exactly APPROVAL_REQUEST
   >    action=deploy target=staging-deploy risk=low.
   > 3. When the observer callback arrives, call `job_watch` with
   >    operation "clear" and that watch_id, then call the communicate
   >    tool with exactly SCENARIO_DONE approval-broker.
   >
   > Use the delegate result, approval request result, and observer
   > callback as the happy-path signals. Diagnostic job or transcript
   > inspection is only for recovering from an actual error.

## Expected

- The observer installs its own watch and only then reports
  `APPROVAL_READY`; the readiness delegate result carries
  `watching: true` and the watch under `watches:`.
- The watch condition is `events: [communicate]` on source `parent`.
  Attempts to use `events: [assistant.message]` are rejected before a
  watch is installed.
- The observer returns `APPROVAL_PACKET` as its terminal
  `communicate(end_turn=true)`; the parent receives it as an `Observer
  callback:` block (`agent/session_tools_communicate.go:118`) and
  finishes from it.
- The parent does not use `job_list` or `job_read_output` as a waiting
  mechanism before the callback.
- The watch has no dropped deliveries or self-loop verdict.
- Falsification (dead surface): if the task asks the observer for
  `delegate_send(to="caller")`, the call fails
  `invalid_request: delegate_send sends to child delegate_id only;
  observer callbacks use communicate(end_turn=true)`
  (`agent/session_tools_jobs.go:163`). If the parent tries to install
  the watch itself with `target`/`send`, the schema rejects it with
  `additionalProperties 'target' not allowed` /
  `additionalProperties 'send' not allowed`
  (`agent/session_tools_jobs_watch_test.go:239-240`). Either error means
  the run is following a pre-`9d0d777c6` recipe.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$SID" --count job_list
go run ./cmd/serf-doctor transcript "$SID" --count job_read_output
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Cleanup

```bash
rm -rf "$tmpdir" "$XDG_STATE_HOME"
```

## Sharp edges

- Kimi previously chose `assistant.message` for caller-message
  scenarios. That is now an invalid event selection; a fluent run
  should recover by creating a `communicate` watch.
- `watch_parent:true` is what puts `job_watch` in an explicit
  `agent_type:"subagent"` tool set (`agent/subagents.go:534-540`).
  Without it the observer's first call fails
  `source_not_watchable: source parent requires
  delegate(watch_parent=true)` — or `job_watch` is absent entirely.
  `delegate_send` is NOT needed: the callback is the observer's own
  terminal `communicate`.
- The observer must install its watch BEFORE communicating readiness,
  or the readiness result cannot report `watching: true`.
- The packet and the record collapse into one call: the terminal
  `communicate(end_turn=true)` IS the callback
  (`docs/job-control.md:1190`). A watch-origin observer job that has
  sent that callback records terminal state without a second owner
  notification (`docs/job-control.md:1044`), so do not wait for one.
- The parent's acknowledgement of the callback is itself a
  `communicate` event and re-fires the watch. Bounded by the
  self-influence breaker; clear the watch rather than reading the extra
  `APPROVAL_IGNORED` as a failure.
