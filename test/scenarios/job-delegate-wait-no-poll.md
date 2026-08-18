# job-delegate-wait-no-poll: a parent with no other work waits idle instead of polling

**What this covers**: the `background-jobs.md` prompt guidance that when a parent
has delegated its whole task and has no independent work, it ends its turn and
waits for the completion notification rather than looping `job_status`. Exercises
the prompt fix for the polling-loop failure first observed in session
`01KVXFMMY1QD5CPP6C55V851NQ` (34 `job_status` calls, 0 `job_watch`, parent never
went idle until the user intervened).

This is the falsification of that session: the correct trace makes **at most one**
orientation `job_status` call (ideally zero) and then goes idle.

## Pre-state

- Fresh scenario state dir (own `$SERF_STATE_DIR` / `$HOME`), so the poll-loop
  detector and any nudge counters start clean and nothing pollutes a real
  instance.
- A `evener serve` instance and a hub (or TUI) client built from the code under
  test — confirm the running binary embeds the edited `background-jobs.md`
  (grep the assembled system prompt for "Do not call `job_status` in a loop").
- Parent model `gpt-5.5` (the model that reproduced the loop). Re-run on at least
  one other tool-capable model as a cross-check.

## Steps

1. Start the daemon + client against the fresh state dir.
2. Give the parent a single neutral instruction that induces full delegation but
   says **nothing** about how to wait — do not mention ending the turn, idling,
   notifications, or polling (that would teach to the test):

   > Delegate this entire task to one subagent and report back its result when
   > it's done. Do not do the work yourself. The task: "Run `sleep 30` via the
   > shell tool, then communicate the exact text `DELEGATE_DONE_77`."

3. Capture the parent's tool calls from the moment `delegate` returns a
   `delegate_id` up to the point it stops producing tool calls (goes idle). Use
   the on-disk transcript as ground truth — `evener-doctor transcript <id> --count
   job_status` and `--format outline` — not just the rendered UI.
4. Wait for the delegate to finish, for Evener to inject the
   `<delegate-notification>`, and for the parent's follow-up turn.

## Expected

- The first parent turn calls `delegate`, which returns a `delegate_id` and no
  job identity at all (`agent/session_tools.go#stableDelegateCreateResult`). It
  cannot have set `max_wait_ms`: creation rejects the key outright with
  `invalid_request: delegate creation does not accept max_wait_ms`
  (`agent/session_tools.go#stableDelegateCreateTool`). What matters is what
  happens **after** the delegate is running.
- **The parent goes idle without a poll loop.** Between the `delegate` result and
  the completion notification it makes **at most one** `job_status`/`job_list`
  call (a single orientation check), then ends its turn.
  - **Falsification**: two or more `job_status` calls on the same running
    delegate with no intervening user input or independent work — i.e. the
    poll-loop signature from the source session. `--count job_status` returning
    ≥3 across the whole session is an outright fail. A single nudge about
    "reading without acting" firing during the wait is also a fail signal: it
    means the parent kept its turn alive instead of going idle.
- A later steering/system entry contains a
  `<delegate-notification delegate_id="dlg_...">` frame for the delegate. A
  direct delegate's terminal renders as that frame, never as
  `<job-notification>` (`agent/delegate_delivery.go#delegateNotificationContent`;
  "Notifications" "It never carries `job_id` or `job_type="delegate"`.").
- The follow-up parent turn surfaces `DELEGATE_DONE_77` to the user, with the
  notification (excerpt or a single post-notification read) as provenance — not
  invention, and not the product of polling.

## Cleanup

Stop the daemon, hub/TUI client, and any tmux session this card created. Remove
the scratch state dir. Leave any pre-existing evener instance running and
untouched.

## Sharp edges

- `sleep 30` keeps the delegate running long enough that the parent is left
  holding a live child with nothing to report — that is the exact moment the old
  trace started polling. It is not there to outlast an inline wait: creation
  cannot wait at all, so the parent reaches that moment on every run.
- Keep the inducing prompt neutral. If it hints "end your turn" or "wait for the
  notification," the card stops testing the prompt and starts testing the
  instruction.
- One orientation `job_status` is allowed by design; the failure is the *loop*,
  not the single check. Assert on the count, not on absence.
- Notification excerpts carry small outputs in full, so a post-notification
  `read_transcript` on the SHELL job's `job:<job_id>` ref is acceptable but MUST
  NOT be required. If the thing being read is the delegate rather than its shell
  job, the ref is the delegate's own session `transcript_ref`: `read_transcript`'s
  contract is that "A stable delegate's session ref reads its conversation;
  delegates never use job: refs", and a `job:` ref "reads shell output only".
