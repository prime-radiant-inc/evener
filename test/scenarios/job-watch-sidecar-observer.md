# job-watch-sidecar-observer: a frame resumes the observer, the read grant lets it look, and its comment reaches the caller

**What this covers**: the full sidecar flow of watch-mailbox spec §5
over the §4 delivery rail. An `output_match` watch on a background
shell job delivers a bounded frame (with `delivery_id`) to an observer
delegate, resuming it as a new job; the §5.1 read grant lets the
resumed observer `job_read_output` the WATCHED job across stores (the
grant keys on the observer's session identity, so it survives the
resume's fresh job_id); the observer comments back with
`job_send_message(target="caller")` and the caller's transcript shows
the comment. Contract anchor: `docs/job-control.md` "Observer and
sidecar composition". Executed by plan Phase 5.2.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-sidecar-XXXXX)`.

## Steps

1. Spawn the parent session via `/api/spawn` with
   `working_dir=$tmpdir`. Capture `SID`.
2. Prompt the parent:

   > Do these steps in order.
   > 1. Call `delegate` with background true and this exact task: "You
   >    are an observer sidecar. In this first turn, call communicate
   >    with exactly OBSERVER_READY and finish. If you are later resumed
   >    with a message containing 'Watch frame': read the job_id and
   >    delivery_id lines from that frame; call job_read_output with
   >    that job_id and find the output line containing SIDECAR_TOKEN;
   >    call job_send_message with target caller and message exactly
   >    'OBSERVER_COMMENT delivery=<the delivery_id> line=<the matching
   >    output line>'; then call communicate with exactly OBSERVER_DONE
   >    and finish. Do not start any delegate of your own." Capture the
   >    observer's job_id and transcript_ref.
   > 2. Run the shell tool with background true and this command:
   >    `sh -c 'sleep 20; echo SIDECAR_TOKEN_OK; sleep 240'`. Capture
   >    the shell job_id.
   > 3. Call `job_watch` with: target the shell job_id, output_match
   >    "SIDECAR_TOKEN_OK", send {to: the observer job_id, message:
   >    "Review this frame.", include_frame: true}. Report the full
   >    JSON.
   > 4. Report both job_ids, then end your turn. Do not call
   >    job_read_output or job_list to wait; you will be notified.
3. Wait. Timeline: the token prints ~20s after the shell job starts;
   the watch fires; the rail delivers the frame to the (by now
   terminal, resumable) observer, resuming it; the observer reads and
   comments and finishes; the observer-resume job's terminal
   notification wakes the idle parent and the queued comment drains
   into the same wake. Poll the parent transcript for
   `OBSERVER_COMMENT` (bound: ~4 minutes).
4. Read the observer's child transcript: ask the parent to call
   `read_session_transcript` with the captured transcript_ref, or find
   the child's `.transcript.jsonl` on disk by the ref's session id.
5. Check the parent's durable job log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`)
   for the grant and the send lifecycle.

## Expected

- The step-3 `job_watch` result has `watching: true` and echoes the
  observer job_id as `send.to`.
- The grant is durable at watch creation: the parent's `jobs.jsonl`
  contains a `watch_read_grant` event naming the observer's session id
  and the watched shell job_id.
  <!-- pin: spec §5.1 — event kind name `watch_read_grant` and its
       field names land in the grants phase; re-verify against shipped
       jobstore kinds. -->
- The watch fires once (one matching line) and the frame delivery
  RESUMES the observer: the observer session gains a new delegate job
  beyond the OBSERVER_READY one (a follow-up `job_list` shows the
  resumed job with the same transcript_ref; the parent also receives
  its terminal notification when it finishes).
- The observer's resumed conversation (step 4) contains the frame: the
  configured message, then a `Watch frame` block with `job_id:` equal
  to the WATCHED shell job_id, a non-empty `delivery_id:` line, and a
  `trigger:` line referencing the SIDECAR_TOKEN_OK output match.
  <!-- pin: frame field labels (Watch frame / job_id: / delivery_id: /
       trigger:) are implementation-emergent; re-verify against the
       shipped frame builder. -->
- The observer's `job_read_output` against the watched shell job
  SUCCEEDS: its TOOL_RESULTS contains the `SIDECAR_TOKEN_OK` line —
  although the watched job is owned by the parent session and the
  observer is running under a new job_id minted by the resume.
  <!-- pin: spec §5.1 — session-keyed grant, survives resume and watch
       expiry (the watched job may even be terminal by read time). -->
- Falsification (grant): the observer's read fails — `job ... not
  found`, an authorization error, or an empty result — meaning the
  grant is missing, job_id-keyed, or not consulted on local-store miss.
- The parent transcript gains a STEERING entry containing
  `OBSERVER_COMMENT delivery=<the same delivery_id as the frame>` plus
  the matched line: the comment crossed back via
  `job_send_message(target="caller")`, and the shared delivery_id ties
  frame → grant-read → comment into one verified chain.
- The parent model acknowledges the comment/notification turn without
  erroring, and the session returns to `idle`.
- The parent's `jobs.jsonl` shows the send lifecycle settle:
  `watch_send_pending` followed by a matching `watch_send_delivered`
  for that delivery, and no `watch_send_dropped`.

## Cleanup

- `job_stop` the shell job (its 240s tail outlives the card).
- Shut down the parent session; `rm -rf "$tmpdir"`.

## Sharp edges

- The observer's first turn must FINISH before the ~20s fire so the
  frame hits a terminal resumable delegate — the canonical
  fire→resume→read flow the grant is keyed for. If the observer is
  still mid-first-turn at fire time, the frame arrives as live steering
  into the running job instead (legal, but the "new resumed job"
  assertion shifts). Keep the observer's first task trivial.
- Comment delivery to an idle parent rides the steering queue, which
  does not wake the parent by itself; the observer-resume job's
  terminal notification provides the wake that drains it. Do not assert
  comment arrival before that notification exists.
- Observers must not delegate (nested delegate jobs are post-v1); the
  observer task says so explicitly.
- The observer is a child session: `delegate` and `job_watch` are not
  in its tool set, and without the grant its `job_read_output` resolves
  only its own store — which is exactly what the grant falsification
  checks.
- If the observer reads AFTER the shell job goes terminal (slow run),
  the read must still succeed — grants are not revoked at watch expiry
  or job end; only retention bounds them.
