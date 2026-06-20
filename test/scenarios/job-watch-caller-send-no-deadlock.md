# job-watch-caller-send-no-deadlock: the caller-send loop is rejected at create; the legal observer variant survives a tool-heavy turn

**What this covers**: the watch-send deadlock incident (session
`01KTWN9KEHZ041D77B3GKK572M`; watch-mailbox spec §1) and its fix.
`job_watch(operation="create", target="caller", events=["communicate"], send={to:"caller"})`
used to fire while the session emitted events under `responseSideEffectsMu`
and re-lock the same mutex — wedging the session on its next assistant
message or tool call. Under the watch-mailbox design that config can no
longer exist: spec §6.1 rejects self-delivery feedback loops at create,
and spec §3/§4.1-4.2 replace in-emit delivery with persist + wake +
loop-owned drains, so the closest legal variant (same trigger events,
observer-delegate delivery) must ride through a turn full of tool calls
without wedging. Live half of spec §10's deadlock-regression bullet;
executed by plan Phase 5.2 (`docs/superpowers/plans/2026-06-11-watch-mailbox.md`).

## Pre-state

- Binaries built fresh from the watch-mailbox branch under test (the
  setup checklist in `docs/agentic-testing.md`): hub on
  `127.0.0.1:9180` spawning the freshly built `serf`.
- Auth token at `~/.serf/auth-token`; a credentialed model that follows
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
   >    — and NO other
   >    parameters (no every, no include_excerpt).
   >    Report the full response or error verbatim.
   > 2. Call `job_watch` with exactly: operation "create", target
   >    "caller", events ["communicate"], and NO send. Report
   >    verbatim.
   > 3. Call `delegate` (max_wait_ms unset — returns immediately)
   >    with this task: "Call communicate with exactly OBSERVER_READY
   >    and finish. If you are ever started with a message containing
   >    'Watch frame', call communicate with exactly FRAME_SEEN and
   >    finish." Capture the returned delegate_id.
   > 4. Call `job_watch` with exactly: operation "create", target
   >    "caller", events ["communicate", "assistant.tool"],
   >    send {to: that delegate_id}
   >    — and NO other parameters (in particular do NOT pass `every`;
   >    it requires a single event kind). Report the full JSON.
   > 5. Run three separate foreground shell commands, one at a time:
   >    `echo wedge_probe_1`, then `echo wedge_probe_2`, then
   >    `echo wedge_probe_3`.
   > 6. Call communicate with exactly: NO_WEDGE_COMPLETE_71.
3. Poll `/api/sessions/local:$SID` until `state` returns `idle`
   (bound: 240s; note the elapsed time).
4. Find the transcript
   (`find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`)
   and check the turn shape and tool results.

## Expected

- Steps 1 and 2 are REJECTED at watch creation: each `job_watch`
  TOOL_RESULTS entry contains `invalid_request` and a message that names
  the self-delivery feedback loop (a watch on the session's own
  self-generated event kinds whose delivery returns to that same
  session). Neither call returns `watching: true`.
  <!-- pin: spec §6.1 — the exact rejection sentence lands with the
       create-time guards; assert the invalid_request code plus
       loop-naming, and record the shipped wording for future runs. -->
- Step 4 is ACCEPTED: result has `watching: true`, a `watch_id`, and
  echoes the observer delegate_id as the send target (sidecar configs stay allowed,
  spec §6.1).
- The turn completes — this is the headline. The session reaches `idle`
  within the bound, and the transcript contains, AFTER the step-4 watch
  install, TOOL_RESULTS entries for all three `wedge_probe_*` shell
  calls plus a final communicate carrying `NO_WEDGE_COMPLETE_71`.
- Falsification (the incident shape): the session never leaves the
  active state — no new transcript appends and no new api_call entries
  for >120s after a `wedge_probe` tool call. That is the deadlock back;
  capture a goroutine dump before tearing anything down (see sharp
  edges).
- Falsification (guard regression): step 1 or step 2 returns
  `watching: true`. The loop config is expressible again; expect the
  transcript to begin accumulating runaway notification/assistant turns
  even after the user turn ends.
- Secondary (should-hold, not the headline): the step-4 watch fired on
  the parent's own communicate/tool events and delivered through the
  drain rail — the observer session eventually shows a started follow-up job
  whose input contains `Watch frame` (check `job_list` from a follow-up
  parent turn, or read the observer transcript via its transcript_ref).

## Cleanup

- Follow-up prompt: call `job_watch(operation="clear",
  watch_id=<step-4 watch_id>)`.
- `job_stop` any observer follow-up still running; shut the session down
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Spec §10's regression bullet describes literally running the incident
  config through a live turn, but spec §6.1 rejects that config at
  create — live, the rejection IS the incident assertion, and the
  step-5 tool calls under the step-4 watch exercise the same
  fire-under-emit path with legal delivery. (Surfaced as a spec tension
  while writing this card.)
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
- Watch frames exclude watch-origin events, so the observer's own
  follow-up start does not re-trigger the step-4 watch into a cross-session loop;
  if you observe observer resumes feeding new frames with no parent
  activity between them, file that as a feedback-suppression bug.
