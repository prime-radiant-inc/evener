# job-notification-semantics: exactly one terminal notification, correct block format, boundary batching without loss

**What this covers**: the terminal-notification contract
(`docs/job-control.md` lines 943-980). (a) Exactly ONE terminal
notification per notification-armed background job — asserted
separately for a shell job and a delegate job (lines 946, 962, 966);
(b) the block format: `<job-notification>` carrying `job_id`, `event`
(the lifecycle kind, never named `type`), `job_type`, `status`,
`reason`, `output_bytes`, `exit_code` when known, `transcript_ref`
for delegates (lines 951-958); (c) notifications for multiple jobs
finishing while the model is mid-turn queue for the boundary
(line 961) and arrive batched — without loss (line 968) — including
mixed completed/failed outcomes; (d) post-F3, blocks carry a bounded
result excerpt. The basic wake-and-read loop is
job-notification-wake.md; watch-flavored (`event="watch"`) delivery
and idle-wake machinery are job-watch-caller-notification-delivery.md.
Restart-side exactly-once is job-restart-durability.md.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md`). Serve mode through the hub is REQUIRED:
  idle wake rides the server-wired notify func.
- Credentialed model. Two workdirs:
  `tmpA=$(mktemp -d -t serf-e2e-notif-XXXXX)` and
  `tmpB=$(mktemp -d -t serf-e2e-notifbatch-XXXXX)`; write the
  AGENTS.md pacing file (per `docs/agentic-testing.md`) into `$tmpB`
  only.

## Steps

Run 1 — session A in `$tmpA`: per-job cardinality and format.

1. Spawn session A; capture `SID_A`. Prompt:

   > Do these steps in order.
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 10; echo NOTIF_SHELL_TOKEN'`. Report the job_id
   >    (J1).
   > 2. Call delegate (background default) with this exact task: "Run
   >    the shell command `sleep 10`, then communicate exactly
   >    NOTIF_DLG_TOKEN and finish." Report the job_id (J2).
   > 3. Say ARMED and end your turn. Do not poll or read; you will be
   >    notified.
2. Poll `/api/sessions/local:$SID_A` to `idle`; both jobs finish ~10s
   later and wake the session. Let it settle back to `idle`, then keep
   watching for 3 more minutes (re-delivery window).
3. Read the transcript
   (`find ~/.local/state/serf/projects -name "$SID_A.transcript.jsonl"`)
   and the durable log (`...sessions/$SID_A/jobs.jsonl`).

Run 2 — session B in `$tmpB`: mid-turn batching.

4. Spawn session B; capture `SID_B`. Prompt:

   > Read AGENTS.md in your working directory first; its pacing rules
   > are mandatory for this turn. Then:
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 5; echo BATCH_OK_TOKEN'`. Report the job_id (J3).
   > 2. Run the shell tool with background true and command:
   >    `sh -c 'sleep 8; echo BATCH_FAIL_TOKEN; exit 3'`. Report the
   >    job_id (J4).
   > 3. Write a five-paragraph essay about rivers, following the
   >    AGENTS.md pacing rules exactly, so this turn stays busy at
   >    least 40 more seconds.
   > 4. End your turn after the essay.
5. After the busy turn and the boundary delivery settle, read session
   B's transcript and `jobs.jsonl`.

## Expected

Run 1:

- Cardinality, shell: the transcript contains EXACTLY ONE STEERING
  entry with a `<job-notification` block for J1 — across the whole
  transcript, including the 3-minute window — with `event="completed"`,
  `job_type="shell"`, `status="completed"`, `reason="exit_zero"`,
  `exit_code="0"`, and a nonzero `output_bytes`.
  <!-- pin: notification turns persist as kind STEERING transcript
       entries today; re-verify the persisted kind on shipped code. -->
- Cardinality, delegate: EXACTLY ONE block for J2 with
  `event="completed"`, `job_type="delegate"`, `status="completed"`,
  and a `transcript_ref` attribute carrying the child ref. (`reason`
  may be empty for a completed delegate — the attribute's presence,
  not its value, is asserted.)
- The `event` attribute equals the lifecycle kind and is never spelled
  `type` (line 958); `job_type` is the job class. An assistant turn
  follows the notification turn(s), and the session returns to `idle`.
- `jobs.jsonl` settle: for EACH of J1/J2, exactly one
  `job_notification_pending` and exactly one
  `job_notification_delivered`.
- Falsification (duplicate): two blocks for the same job_id anywhere
  in the transcript without an intervening daemon restart — duplicate
  suppression broken (line 962).
- Falsification (wake hole): the jobs reach terminal (their
  `job_finished` events exist) but the idle session shows no
  notification turn within ~120s of the later finish.

Run 2:

- No early delivery: NO `<job-notification` block for J3 or J4
  appears in the transcript BEFORE the busy turn's final assistant
  message — mid-turn terminal events queue for the boundary
  (line 961).
- Batched delivery without loss: after the final assistant message of
  the busy turn, the NEXT notification delivery contains BOTH blocks —
  J3 with `status="completed"`/`reason="exit_zero"`, and J4 with
  `event="failed"`, `status="failed"`, `reason="exit_nonzero"`,
  `exit_code="3"`. When both were queued at the boundary they arrive
  in ONE notification turn (one STEERING entry, two blocks separated
  by a newline); the normative assertion is both-delivered,
  exactly-once-each, after the boundary.
- Falsification (loss): only one of J3/J4 ever gets a block — a
  queued terminal notification was dropped at the boundary
  (line 968: pending state must survive until injection).
- Falsification (mixed-status smearing): J4 reported as anything but
  `failed`/`exit_nonzero` — the failure outcome must survive batching
  intact.
- Result excerpt (d): each terminal block also carries a bounded
  excerpt of the result — for shell, the tail of output (containing
  the TOKEN line); for the delegate, the head of its report.
  <!-- pin: ergonomics §4 F3 lands with Phase 2 (shell → last ~400
       chars, delegate → first ~400 chars). Pre-F3 blocks carry only
       the metadata line — accept that and record which build you
       ran. -->

## Cleanup

- All jobs are terminal by design. Shut down both sessions
  (`POST /s/<sid>/shutdown`); `rm -rf "$tmpA" "$tmpB"`.

## Sharp edges

- Run 1's two jobs finish ~simultaneously while idle; they may arrive
  as one batched notification turn or two — cardinality is per-JOB
  (one block each), not per-turn. Count blocks by job_id, not turns.
- Run 2's pacing must hold the turn past J4's +8s finish; if the model
  skips the pacing and the turn ends early, the jobs deliver as
  idle-wake notifications instead and the batching arm is vacuous —
  rerun rather than reinterpret.
- Counting trick: the listing/answer turns also MENTION job ids;
  count only occurrences of `<job-notification job_id="<id>"`, not
  bare id mentions.
- A foreground shell command that completes inline is NOT
  notification-armed (line 946) and must never produce a block — any
  block for a job_id you never saw (an ephemeral inline call) is a
  finding.
- Duplicate blocks ARE legal across a daemon crash/restore midway
  (at-least-once with durable suppression, line 966); this card's
  duplicate falsification applies only to an uninterrupted run. The
  restart flavor is job-restart-durability.md's exactly-once arm.
