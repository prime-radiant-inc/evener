# job-notification-semantics: shell terminal notifications are durable and lossless

**What this covers**: terminal notifications for notification-armed shell jobs
(`docs/job-control.md` "Notifications" and "Shell jobs"). Direct delegate
completion is a separate resource path: it arrives as
`<delegate-notification delegate_id="dlg_...">` with the delegate session's
`transcript_ref`; it is not a `job.notification`, `job_type: delegate`, or
`job:` read. This card intentionally tests shell notification semantics only.

## Pre-state

- Fresh `evener serve` and hub client with an isolated state directory.
- A model that follows the exact shell-only probe below.

## Steps

1. Start a background shell job:

   > Run the shell tool with mode `"background"` and command
   > `sh -c 'sleep 10; echo NOTIF_SHELL_TOKEN'`. Report the returned `job_id`
   > as J1. Say ARMED and end your turn. Do not poll or read; wait for the
   > automatic terminal notification.

2. After the notification arrives, report its complete frame verbatim and
   surface `NOTIF_SHELL_TOKEN`. End the turn.

3. Repeat with two background shell jobs while the model is mid-turn:

   > Start `sh -c 'sleep 5; echo BATCH_OK_TOKEN'` and report J2. Start
   > `sh -c 'sleep 8; echo BATCH_FAIL_TOKEN; exit 3'` and report J3. Then write
   > a five-paragraph essay about rivers, following the pacing rules, so this
   > turn remains busy past both completions. Do not poll or read jobs.

4. At the next turn boundary, report both terminal notification frames and
   surface both tokens and J3's nonzero exit honestly.

## Expected

- J1 produces exactly one `<job-notification job_id="J1" ...>` frame with
  `event="completed"`, `job_type="shell"`, `status="completed"`,
  `reason="exit_zero"`, `exit_code="0"`, nonzero `output_bytes`, and the
  shell `transcript_ref`.
- The notification's excerpt contains `NOTIF_SHELL_TOKEN`; a `job:` ref names
  retained shell output if a later evidence read is needed.
- J2 and J3 each produce exactly one terminal frame. The frames may arrive as
  one batched notification turn or two turns, but neither completion is lost
  while the model is busy. J3 reports `status="failed"`, `exit_code="3"`, and
  its retained output includes `BATCH_FAIL_TOKEN`.
- Duplicate terminal frames for the same `job_id` do not appear without a
  daemon restart. A foreground shell command that completes inline is
  ephemeral and produces no terminal notification.
- Stable delegate completion remains outside this card: it uses the direct
  delegate's `delegate_id` and session `transcript_ref`, never a shell
  `job_id`, `job_type="delegate"`, or delegate `job:` ref.

## Durable audit

Inspect the owning session's transcript and `jobs.jsonl`. For each shell job,
there is one durable pending notification and one delivered notification. Count
only `<job-notification job_id="...">` blocks, not bare IDs in reports.

## Sharp edges

- End the first turn immediately after starting J1; polling changes the
  behavior under test.
- Keep the shell commands backgrounded so they return concrete shell IDs.
- A job list row or a delegate notification is not a shell terminal frame.
- If a notification is batched at a turn boundary, assert cardinality per
  shell job, not per turn.
