# job-watch-caller-notification-delivery: caller-targeted watch fires wake an idle session as notification turns, coalesced latest-frame-wins

**What this covers**: watch-mailbox spec §4.3 — the caller alias's
delivery mechanism is the notification turn. A LEGAL caller-delivery
watch (`output_match` on a concrete job; send omitted → caller
notification, and send.to="caller" → rendered watch-send frame) that
fires while the session is IDLE wakes it without user input, and the
model receives the notification/frame as a job-notification turn
(spec §4.4 wake path). N fires against a BUSY session produce N wake
tokens but coalesce to the latest-frame-wins current frame, rendered
once per delivery boundary (never one frame per fire) — render-by-key
(spec §4.3; contract coalescing-not-silence rule,
`docs/job-control.md` line 549). Executed by plan Phase 5.2.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md`). Serve mode through the hub is REQUIRED:
  idle wake rides the server-wired notify func; a one-shot library/CLI
  run only delivers at natural boundaries and cannot exercise run 1.
- Credentialed model. Two hermetic workdirs:
  `tmpA=$(mktemp -d -t serf-e2e-wake-XXXXX)` and
  `tmpB=$(mktemp -d -t serf-e2e-coalesce-XXXXX)`.
- For run 2, write the AGENTS.md pacing file into `$tmpB` per
  `docs/agentic-testing.md` ("AGENTS.md pacing trick"), phrased for
  serf's shell tool: pause between every paragraph and every action by
  running `sleep 8` via the shell tool, at least four pauses per turn.

## Steps

Run 1 — idle wake, both delivery flavors on one fire:

1. Spawn session A via `/api/spawn` with `working_dir=$tmpA`. Capture
   `SID_A`.
2. Prompt:

   > Do these steps in order.
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 25; echo WAKE_TOKEN_GO; sleep 240'`. Capture the
   >    job_id.
   > 2. Call job_watch with target that job_id and output_match
   >    "WAKE_TOKEN_GO" (no send). Report the full JSON.
   > 3. Call job_watch with target that job_id, output_match
   >    "WAKE_TOKEN_GO", and send {to: "caller", message:
   >    "CALLER_FRAME_MARK", include_frame: true}. Report the full
   >    JSON including replaced_existing.
   > 4. Say WATCHES_ARMED and end your turn. Do not poll; you will be
   >    woken.
3. Poll `/api/sessions/local:$SID_A` and confirm `state` is `idle`
   before the token prints (~+25s from the job start).
4. Keep polling through the fire: watch for the state to leave `idle`
   with NO user input sent, then inspect the transcript
   (`$SID_A.transcript.jsonl`) and the durable job log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID_A/jobs.jsonl"`).

Run 2 — busy session, three fires, one rendered frame:

5. Spawn session B with `working_dir=$tmpB` (AGENTS.md present).
   Capture `SID_B`.
6. Prompt:

   > Read AGENTS.md in your working directory first; its pacing rules
   > are mandatory for this turn. Then:
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 10; echo TICK_MARK_1; sleep 6; echo TICK_MARK_2; sleep 6; echo TICK_MARK_3; sleep 240'`.
   >    Capture the job_id.
   > 2. Call job_watch with target that job_id, output_match
   >    "TICK_MARK_[0-9]", and send {to: "caller", message:
   >    "TICK_FRAME", include_frame: true}.
   > 3. Then write a five-paragraph essay about software engineering,
   >    following the AGENTS.md pacing rules exactly, so this turn
   >    stays busy for at least 40 more seconds.
   > 4. End your turn after the essay.
7. After the busy turn ends, inspect session B's transcript for the
   notification turn, and its `jobs.jsonl` for the send lifecycle.

## Expected

Run 1:

- Both installs succeed; the step-3 result has
  `replaced_existing: false` — the notify flavor and the explicit
  caller-send flavor are distinct watch keys (implicit caller
  endpoint vs `send.to="caller"`) and coexist. If step 3 reports
  `replaced_existing: true` instead, the keys collapsed: split the two
  flavors across two sessions to finish the run, and file the finding
  (see sharp edges — this is a contract ambiguity).
- The session is `idle` before the fire, then wakes WITHOUT user
  input: the state leaves `idle`, and the transcript gains NO new
  USER_INPUT entry — instead a notification turn: a STEERING-kind
  entry whose text contains `<job-notification` content.
  <!-- pin: notification turns persist as kind STEERING transcript
       entries today; re-verify the persisted kind on shipped code. -->
- That wake delivers BOTH flavors of the single fire (same
  notification turn, or at minimum the same wake):
  - the caller-notification block: `event="watch"` and
    `job_type="watch"` attributes, the concrete job_id, and a reason
    referencing `output_match:` with `WAKE_TOKEN_GO`;
  - the rendered watch-send frame: `CALLER_FRAME_MARK` plus a
    `Watch frame` body with `job_id:`, a non-empty `delivery_id:`
    line, and `trigger:` referencing WAKE_TOKEN_GO.
    <!-- pin: spec §4.3 render-by-key branch — current rendering uses
         a job-notification block with event="watch_send",
         delivery_id= and trigger= attributes; re-verify attribute
         names on shipped code. -->
- An assistant turn follows the notification turn (the model received
  the wake and reacted), and the session returns to `idle`.
- The durable log settles after the turn persists: `jobs.jsonl` gains
  a `watch_send_pending` for the fire and then a matching
  `watch_send_delivered`; no `watch_send_dropped`.
- Falsification (wake hole): the token printed — confirm with a
  manual follow-up `job_read_output` if needed — but the session sat
  `idle` past ~90s with the pending recorded: observation without
  wake.
- Falsification (rail gap): the notify-flavor block arrives but
  CALLER_FRAME_MARK never does — caller sends persisted but
  undeliverable, the spec §11 "rail without §4.3" failure shape.
- Falsification (old mechanism): CALLER_FRAME_MARK appears as a bare
  steering message with no job-notification framing and no
  corresponding settle in `jobs.jsonl` — the deleted steering-turn
  delivery path is back.

Run 2:

- All three TICK lines print while the turn is still busy (ticks land
  ~+10/+16/+22s after the job starts; the paced essay holds the turn
  well past that). TICK_FRAME never appears mid-stream (inside a
  streaming model response) — but it MAY surface between tool rounds:
  the contract's owner-session boundaries are "between tool rounds, at
  input end, or on idle wake" (`docs/job-control.md` "Delivery
  modes"), so a between-rounds delivery during the paced essay is
  contract-true, not a leak.
- Each rendered `TICK_FRAME`'s `trigger:` references the LATEST tick
  that had fired by its delivery time — latest-frame-wins per watch
  key; a superseded stale token renders nothing. Observed shipped
  behavior with this pacing: one frame, trigger `TICK_MARK_3`.
  <!-- pin: spec §4.3 — fires between deliveries coalesce to one
       durable latest-frame-wins pending per watch key; each delivery
       renders exactly the current frame, never one frame per fire. -->
- `jobs.jsonl` shows superseded pendings and a matching
  `watch_send_delivered` per rendered frame; no `watch_send_dropped`.
- Falsification: more rendered frames than deliveries (token-per-fire
  leaked into rendering — coalescing broken); zero rendered (the
  matched condition turned into silence — violates the contract's
  coalescing rule); a frame whose `trigger:` references a tick OLDER
  than the latest fire at its delivery time (a stale frame won); or
  any frame content injected mid-stream.

## Cleanup

- In each session: `job_stop` the sleeper job (240s tails), then shut
  the session down.
- `rm -rf "$tmpA" "$tmpB"`.

## Sharp edges

- Contract ambiguity surfaced while writing this card: line 541 of
  `docs/job-control.md` says the notify flavor's key uses "the
  implicit caller notification endpoint" but never states whether that
  endpoint equals the literal `caller` send target. Run 1 assumes they
  are distinct keys; `replaced_existing: true` on step 3 is the signal
  they are not.
- Run 1 needs the session idle before +25s: a three-tool-call turn
  fits easily, but if the model dawdles the fire lands mid-turn and
  run 1 degrades into run-2 shape. Re-run rather than reinterpret.
- Run 2 inverts the risk: the essay must HOLD the turn past the last
  tick (+22s). The pacing file plus model latency gives >40s; if the
  model skips the pacing, later ticks fire while idle and arrive as
  separate wakes — rerun rather than reinterpreting a 2-frame result.
- Latency note (accepted by the spec): during one long uninterrupted
  model stream, delivery waits for the stream to end — frames landing
  a few seconds after a long assistant message is normal, not a bug.
- Duplicate frames across a daemon crash/restore are legal
  (at-least-once; the frame's delivery_id identifies them) — only
  duplicates within one uninterrupted run falsify coalescing.
