# waiting_on_work guard — design

Date: 2026-11-01. Owner decision: Jesse chose the broad "provably dead"
refusal condition over the literal "no live jobs or subagents" one.

## Intent

A session that ends its turn with `end_reason="waiting_on_work"` must have
work that will actually wake it. On 2026-11-01 a long-running shepherd session
told its partner "I'm starting that rebase now", ended on `waiting_on_work`,
and rested forever: no job, delegate, or watch was left running, so no
notification could ever fire. Every internal metric was healthy; only the
session's own last claim was false.

The result tool refuses an `end_turn=true, waiting_on_work` communicate call
when nothing can provably wake the session. The refusal happens before
anything durable is written, so the model keeps the turn and can correct
itself: end with `done`, end with `needs_response`, or start the work.

## Why a refusal, not a warning

The mirror case — ending while jobs still run — is warn-first (2026-08-06
ruling) and works because the jobs keep running and report on their own. A
warning inside a *successful* end_turn result is post-mortem: the turn has
ended and the model never gets another round to read it. Only a failure keeps
the model alive to fix the claim. Honest alternatives always exist, so the
refusal cannot trap the model.

## The predicate

Refuse only when ALL of these are empty — anything non-empty means something
can move the session without its human partner:

- `autonomyInFlight()`: pending job notifications, queued input, a runnable
  client start, pending delegate deliveries and attention, watch-send
  settlement retries, and working children (running/driving/finalizing).
- Running work: this session's own running jobs plus detached processes with
  open completion receipts (`sessionRunningWorkIDs`).
- A pending question (`HasPendingAsk`): the human's answer wakes the session.
- An armed watch registered anywhere it can deliver to this session: this
  session's own job manager plus every holder manager `watchHolderSessions`
  names (self-timers, descendant-job receiver watches). A spent one-shot
  (`watchOneShotHasFired`) does not count; every other registered watch does —
  including one whose receiver is someone else, since overcounting is the
  safe direction.

False negatives remain possible (a watch registered a moment after the check,
an exotic race) and are harmless: the session sleeps as it does today. False
positives — refusing a session that would in fact be woken — are what this
design eliminates.

Deliberately NOT wake sources: a goal that kicks at settle (it moves the
session before the rest happens, and the refusal does not change goal
mechanics), and compaction nudges (they steer a session that is already
running).

## Goals

- The refused call delivers nothing: no communicate entry, no terminal
  capture, no end-reason tool state. It looks like any failed tool call.
- The refusal message names the outs (done / needs_response / start the work
  or arm a watch) so the model corrects in one round.
- `done`, `needs_response`, `end_turn=false`, and delegate sessions are
  untouched; the guard is scoped to `end_turn=true && waiting_on_work` on
  sessions that offer end_reason at all (`hasHumanPartnerToAsk`).

## Non-goals

- No new session state, no hub or client surface, no restore-derivation
  change: restore keeps deriving a `waiting_on_work` rest as idle from the
  transcript, unchanged.
- No change to the awaiting settle. `autonomyInFlight` stays as it is; its
  known incompleteness (running jobs do not suppress an awaiting arm) is
  observed, not fixed here.
- No change to the warn-first ruling for the jobs-still-running case.

## Anti-goals

- Failing a session that would in fact be woken (a timer, a pending
  notification, a descendant's job). Any future wake source added to evener —
  for example the hub watches in the Manager feature — must add its term to
  this predicate in the same change that lands it.

## Constraints

- The guard runs in the communicate Exec before `deliverCommunicate`, beside
  the existing `runningJobIDs` warning hook, through a new `toolDeps`
  function field (same wiring pattern as `runningJobIDs`).
- The end_reason tool description (`internal/tool/definitions.go`) states the
  contract; the pinned prompt goldens under `agent/testdata/messagingprompt/`
  are regenerated with `-update-prompt`; the delegate goldens are unchanged by
  construction (they never carried end_reason).
- `docs/architecture.md` and the S09 row of `docs/product/subsystems.md` get
  the contract sentence in the same change.
- Tests red-first, behavior-asserted (no phrasing oracles): the deps-level
  refusal table, the enumeration cases, and an end-to-end replay of the
  2026-11-01 failure.

## Approaches

1. Refuse at the tool (chosen): the model keeps the turn and corrects; one
   extra round only in the buggy case.
2. Accept but downgrade at the settle: rejected — the mislabel stands in the
   transcript and nothing teaches the model.
3. Warn only: rejected — post-mortem for this call shape; nothing changes.
4. A new surfaced "stranded" state: rejected — product-heavy (state, clients,
   restore) for a small guard; a possible follow-up if the refusal proves
   insufficient.

## Left to the builder

- The Session method computing the predicate (beside `autonomyInFlight`;
  suggested name `hasWakeableWork`), its `toolDeps` wiring, and the error
  text.
- An armed-watch helper on `jobManager` (over `jm.watches` under lock,
  spent one-shots excluded) and the `watchHolderSessions` reuse.
- Test placement: extend `session_communicate_end_reason_test.go` for the
  deps-level table; a new test file for the enumeration and the replay.
