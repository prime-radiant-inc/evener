# job-watch-caller-send-no-deadlock: the caller-send loop is rejected at create; the legal observer variant survives a tool-heavy turn

**What this covers**: the watch-send deadlock incident (session
`01KTWN9KEHZ041D77B3GKK572M`; watch-mailbox spec §1) and its fix.
`job_watch(operation="create", target="caller", events=["communicate"], send={to:"caller"})`
used to fire while the session emitted events under `responseSideEffectsMu`
and re-lock the same mutex — wedging the session on its next assistant
message or tool call. That config is now unreachable through the public
tool for a blunter reason than the spec anticipated: `target` and `send`
were deleted from the schema by commit `9d0d777c6` (2026-06-22), so the
call dies in JSON-schema validation before any semantic guard runs.
Meanwhile in-emit delivery was replaced by persist + wake + loop-owned
drains, so the closest legal variant — the same trigger events, watched
by an observer sidecar — must ride through a turn full of tool calls
without wedging. Live half of spec §10's deadlock-regression bullet;
executed by plan Phase 5.2 (`docs/superpowers/plans/2026-06-11-watch-mailbox.md`).

## Pre-state

- Binaries built fresh from the watch-mailbox branch under test (the
  setup checklist in `docs/agentic-testing.md`): an isolated hub
  (never Jesse's real hub on `9180`) spawning the freshly built `serf`.
- Auth token at `$HOME/.serf/auth-token` (the isolated `$HOME`); a
  credentialed model that follows
  multi-step procedural tool instructions (the Phase 5.2 recipe uses
  `oai-work/<model>`; any enumerated live model works).
- Hermetic workdir: `tmpdir=$(mktemp -d -t serf-e2e-wedge-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`. Capture
   `SID`.
2. Send this prompt (one turn, ordered; tool errors are expected in the
   first two steps):

   > Follow these steps exactly, in order. Steps 1 and 2 are expected to
   > return tool errors — report each error verbatim and continue.
   > 1. Call `job_watch` with exactly: operation "create", target
   >    "caller", events ["communicate"], send {to: "caller"}
   >    — and NO other parameters. Report the full response or error
   >    verbatim.
   > 2. Call `job_watch` with exactly: operation "create", target
   >    "caller", events ["communicate"], and NO send. Report
   >    verbatim.
   > 3. Call `job_watch` with exactly: operation "create", source
   >    "self", events ["communicate"]. Report the full JSON.
   > 4. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are the wedge observer. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['communicate', 'assistant.tool']. Then communicate exactly
   >    OBSERVER_READY and finish. When later resumed with a message
   >    containing 'Watch frame', finish with communicate end_turn true
   >    and message exactly FRAME_SEEN." Report the delegate result's
   >    `watching` field and the watch listed under `watches`.
   > 5. Run three separate foreground shell commands, one at a time:
   >    `echo wedge_probe_1`, then `echo wedge_probe_2`, then
   >    `echo wedge_probe_3`.
   > 6. Call communicate with exactly: NO_WEDGE_COMPLETE_71.
3. Poll `/api/sessions/local:$SID` until `state` returns `idle`
   (bound: 240s; note the elapsed time).
4. Find the transcript
   (`find $HOME/.local/state/serf/projects -name "$SID.transcript.jsonl"`)
   and check the turn shape and tool results.

## Expected

- Steps 1 and 2 are REJECTED at watch creation, by SCHEMA validation
  rather than by a semantic self-delivery guard. Step 1's error names
  the first removed property it meets —
  `additionalProperties 'target' not allowed` (or `'send'`) — and step
  2's names `'target'`. Neither call returns `watching: true`, and
  neither reaches a handler. Pinned by
  `TestJobWatchRejectsRemovedPublicShapes`
  (`agent/session_tools_jobs_watch_test.go:239-240`).
  <!-- The deleted handler-level messages ("job_watch uses source, not
       target"; "job_watch delivers to the watcher automatically; send
       is not a public argument", agent/session_tools_jobs.go:1984,1987)
       are unreachable through the public tool — the schema wins first.
       If a run ever sees THOSE strings, the schema stopped enforcing
       additionalProperties. -->
- Step 3 is ACCEPTED, and that is deliberate: the create-time
  self-delivery guard is GONE. `source: "self"` on a self-generated
  event kind installs (`watching: true`) and the runtime breaker bounds
  the loop instead — `TestJobWatchSelfSourceSelfKindInstalls`
  (`agent/job_watch_loopguard_test.go:138`). Falsification: an
  `invalid_request` here means the create-time guard came back, which
  contradicts the contract as it now reads — `docs/job-control.md`
  "`job_watch`" "nothing is rejected at creation for being a potential
  feedback loop. The loop is bounded at runtime instead".
- Step 4 is ACCEPTED: the delegate result reports `watching: true` and
  lists the OBSERVER's own watch (`source: "parent"`). The observer
  creates it; the parent never names a delivery target, because there
  is no longer one to name.
- The turn completes — this is the headline. The session reaches `idle`
  within the bound, and the transcript contains, AFTER the step-4 watch
  install, TOOL_RESULTS entries for all three `wedge_probe_*` shell
  calls plus a final communicate carrying `NO_WEDGE_COMPLETE_71`.
- Falsification (the incident shape): the session never leaves the
  active state — no new semantic transcript entries and no new `api_attempt` records
  for >120s after a `wedge_probe` tool call. That is the deadlock back;
  capture a goroutine dump before tearing anything down (see sharp
  edges).
- Falsification (schema regression): step 1 or step 2 returns
  `watching: true`. The removed `target`/`send` shape is expressible
  again; expect the transcript to begin accumulating runaway
  notification/assistant turns even after the user turn ends.
- Secondary (should-hold, not the headline): the step-4 watch fired on
  the parent's own communicate/tool events and delivered through the
  drain rail — the observer session eventually shows a started follow-up job
  whose input contains `Watch frame` (check `job_list` from a follow-up
  parent turn, or read the observer transcript via its transcript_ref).

## Cleanup

- Follow-up prompt: call `job_watch(operation="clear", watch_id=...)`
  for BOTH the step-3 self-watch and the step-4 observer watch. The
  parent can clear the observer's watch by id: a parent-source watch is
  installed into the parent's own job manager (`parentInstallWatch`,
  `agent/session_tools_jobs.go:218`).
- `job_stop` any observer follow-up still running; shut the session down
  (`POST /api/sessions/local:$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Spec §10's regression bullet describes literally running the incident
  config through a live turn. It cannot be typed any more, so live, the
  rejection IS the incident assertion, and the step-5 tool calls under
  the step-3 and step-4 watches exercise the same fire-under-emit path
  with legal delivery. The incident shape survives only in
  `agent/job_watch_deadlock_test.go`'s `newCallerSendWatchSession`
  helper, which installs it below the public tool schema on purpose —
  the deadlock regression keeps unit coverage this card cannot
  reproduce.
- Ordering matters once: the step-4 install must precede the step-5
  shell probes or the no-wedge assertion is vacuous. Small models
  occasionally reorder; re-prompt rather than accept out-of-order
  evidence.
- On a suspected wedge, `kill -QUIT <serf daemon pid>` makes the Go
  runtime dump all goroutine stacks to the daemon's stderr — look for a
  goroutine parked in `sync.(*Mutex).Lock` beneath an event-emission
  frame. Do this BEFORE shutdown, which would destroy the evidence.
- The observer-frame secondary assertion lags the parent turn (delegate
  follow-up start + model latency); give it ~3 minutes before calling it absent.
- Watch-origin events are NOT excluded any more: under the breaker
  policy an event carrying this watch's own provenance is delivered and
  classified self-influenced, and each such frame carries an escalating
  `<system-reminder>` depth line (`↳ this turn responded to your last
  message.`). Observer resumes with no fresh parent activity between
  them are therefore expected, bounded by the runaway fuse and the
  50-delivery watch budget — clear both watches promptly. Only
  `watch_send_dropped` with `diagnostic_reason: "runaway"` is a
  finding.
