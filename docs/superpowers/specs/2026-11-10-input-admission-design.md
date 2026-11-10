# Input admission: one place where input enters a model request

Status: draft for discussion with Jesse. Nothing here is implemented.

## Problem

Three fleet-audit fixes kept coming back from review with new findings, and
every finding had the same shape: a rule that holds for one input path is
missing on another.

- Notification turns arriving mid-turn (W1.1, six rounds): notifications
  were settled on paths that never sent a request.
- Quota wait inside the model call (W1.4, about five rounds): turn paths
  that went around the wait or did not wake it.
- Watch rate limit (W2.2, #6068, still open): attention paths that drop
  provenance, a runaway diagnostic with no provenance, and a backstop counted
  before coalescing.

The cause is structural. A model request is built in exactly one place
(`prepareModelRequestWithError`, with one caller in the round loop). But each
input source writes itself into history on its own schedule and settles on
its own terms. So every rule that should hold for all input has to be
repeated on each path:

- (a) settle only once a request carrying the item is sent;
- (b) adopt the item's provenance;
- (c) wake the quota wait;
- (d) count watch deliveries.

Each new path, and each new rule, is a chance to miss one.

## What consumes input today

There are 19 paths. The inventory below is from origin/main on 2026-11-10;
line references live in the PR thread, not here. For each path the table
shows how its item reaches the request, and which of rules (a)–(d) it applies.

| # | Path | How it reaches the request | (a) settle | (b) provenance | (c) quota wake | (d) count |
|---|---|---|---|---|---|---|
| P1 | User input (direct, `turn/start`) | new turn; transcript then history | at append | yes | only behind a recovered turn | n/a |
| P2 | Queued message | own user turn | at append | yes | woken, but the text does not ride the retry | n/a |
| P3 | Steering (client and daemon) | `injectDrainedSteering` at about 8 call sites | at append | yes (daemon only) | yes | n/a |
| P4 | Steering carrier turn | new turn plus drain | at append | via drain | no | n/a |
| P5 | Drain-as-steer, promote-as-steer | becomes P3 | at append | via P3 | yes | n/a |
| P6 | Job notifications | notification turn; `injectLaterJobNotifications` only on notification turns | **on send** (completions only) | union | **no** | n/a |
| P7 | Watch send rail | caller: P6 frame; delegate: attention turn | before the call | the runaway fuse lives only here; delegate frames lose it | no | at settle, before send |
| P8 | Long-running-job reminder | P6 watch-kind frame | before the call | none | no | no |
| P9 | Delegate attention (report, update, watchdog, stable frame) | already in history; attention turn or projection | consumed before launch or at turn success | **dropped** (`replaceActiveProvenance(nil)`) | no | none |
| P10 | Parent steers a running delegate | `CompleteModelRequest` projects pending steers | at request build | none | no | n/a |
| P11 | Delegate `PostUpdate` | deferred delivery, flushed at the next prepare, then P9 | as P9 | none, by design | no | n/a |
| P12 | Idle delegate and child drives | P9 or a child P6 turn | as P9 / P6 | as P9 / P6 | drive slot only | as P6 |
| P13 | Goal continuation | new turn; `recordTurn` (history first) | n/a | nil | no | n/a |
| P14 | Runtime and recovery nudges | steering plus a runtime turn | at append | nil | no | n/a |
| P15 | Hook-injected context | steering queue; some direct appends | at append | none | no | n/a |
| P16 | Daemon steering and reminders | direct `appendSteeringTurn` or queued | at append | none | no | n/a |
| P17 | Skill selection and re-delivery | appended in prepare; obligations committed before the send | before send | n/a | n/a | n/a |
| P18 | Notes, memory and environment projections | appended in prepare | n/a | n/a | n/a | n/a |
| P19 | Restore and recovery | re-enters P1–P9 | as those | as those | as those | as those |

Defects the inventory shows today, beyond the open review findings:

- A user turn in a quota wait never takes in a job notification, because
  only notification turns inject later notifications. A notification also
  does not wake the wait.
- Delegate attention drops provenance. This is what makes the W2.2 hop
  provenance fragile.
- A Stop-hook block queues context after the round's steering drain. When
  the turn has already communicated, nothing drains it before the next
  request. (UNSURE; needs a test.)
- `parentSteer`, the only steer that carries provenance, has no caller.
- `waitingForQuota` has no production caller.

## Design

### The item

Every input source submits an **admission item** and does nothing else:

```go
type admissionItem struct {
    id         string            // stable; replay detection and dedupe key
    kind       admissionKind     // user, queued, steer, notification, watchFrame, attention, delegateSteer, continuation, runtime, hookContext, daemonSteer
    owner      turnOwner         // which turn/lease may carry it (root vs delegate run)
    turns      func() []schema.Turn // rendered at admission time, not at submit
    provenance *provenance.Causal
    startsTurn bool              // may open a turn when the session is idle
    onSent     func(ctx)         // settle: delivered marker, incorporation, coverage, watch count
    onUnsent   func(ctx, admitted bool) // left owed; the undispatched cap applies
}
```

It is rendered at admission time, not at submit time, so coalescing and
self-influence annotation see everything that arrived up to the request.
This is the W2.2 "backstop counted before coalescing" finding, fixed in the
structure itself.

### The inbox

A session keeps one inbox: an ordered set of items keyed by id. Submitting
an item does two things in one place:

1. If the session is idle, it wakes the drain loop. A `startsTurn` item
   opens a turn whose kind is the head item's kind.
2. If the session is in a quota wait, it wakes the wait.

That is rule (c) for every kind at once. The wait retries the round, and
the retry runs admission, so the new item rides that retry.

Durable stores stay where they are: the client-mutation store, the steering
queue, `jobs.jsonl` and the delegate transcript. The inbox is in-memory and
indexes durable items. Restore re-submits from the durable stores (P19), the
same way it re-arms today.

### Admission

There is one function, `admitInput(ctx, round)`. It runs inside
`prepareModelRequestWithError`, after the fold and before the history
snapshot. For a leased delegate it runs inside `CompleteModelRequest`'s
projection. It:

1. Takes every item this turn's owner may carry, in arrival order.
2. Applies the watch rules once, for send and no-send alike:
   - coalesce;
   - suppress true echoes;
   - add the `<system-notification>` caused-by-your-own-message annotation
     with its depth;
   - the runaway fuse at `runawaySelfInfluenceDepth`;
   - the 60-per-hour backstop notice.
3. Renders the items' turns and writes them transcript-first. A failed
   write leaves the items in the inbox.
4. Unions their provenance into the active turn's provenance. That is rule
   (b) for every kind.
5. Records them as **carried** by this request.

The round loop already wraps the call in `llm.WithRequestSentObserver`.
When the request is sent, it runs `onSent` for each carried item: delivered
markers, incorporation, attention coverage and watch delivery counting. That
is rules (a) and (d) for every kind.

When the request is not sent (a Stop, an admission refusal, a failure before
the send), it runs `onUnsent`, and the item stays owed. The existing
two-turn cap (`maxUndispatchedNotificationTurns`) becomes the rule for every
kind that has a delivery state.

Replay detection by item id keeps a re-admission after a restart from
writing the same turns twice. That is the case watch frames currently avoid
by settling before the call.

### Turn openers shrink

`acceptNotificationInput`, `acceptDelegateAttentionInput`,
`acceptContinuationInput`, `acceptRuntimeInput` and
`acceptSteeringCarrierInput` stop appending content and stop setting
provenance. They only open the turn; the opening request admits the content.
`injectDrainedSteering` call sites, `injectLaterJobNotifications` and
`flushPendingDelegateDeliveries` in prepare all collapse into `admitInput`.

The post-tool point stays: after a tool batch the round loop goes to the
next round, and that round's prepare admits whatever arrived. A
non-notification turn no longer has to yield so that a notification turn
can run. The notification rides that turn's next request.

### What stays outside

- **Skill delivery (P17)** keeps its own seam. It is an obligation the
  request must satisfy, not an input. It already lives in prepare.
- **Context projections (P18)** stay as they are.
- **User input (P1, P2)** still opens its own turn and keeps its client
  states. Its incorporation marker could move from "at append" to "on
  send", but that changes what clients see. See open question 1.

### How W2.2 rebuilds on this

Most of #6068 becomes rules inside admission step 2 rather than code spread
across `watch_fire.go`, `watch_send_rail.go` and the attention paths:

- echo suppression and the `<system-notification>` annotation with depth;
- the runaway fuse, which now covers no-send by construction;
- the backstop notice, counted after coalescing;
- the diagnostic not counting as a fire.

Hop provenance (option B: an in-memory map in the controller, reset on
restart) stays. It becomes the `provenance` field of the attention item
that the delegate's admission adopts. That fixes the two adoption gaps,
because there is only one adoption. The rate-limit and budget removal from
#6068 is independent and can be cherry-picked as is.

## Migration order

Each step ships on its own, keeps behavior for paths not yet moved, and
adds that kind to one shared table test.

1. **The inbox, admission and carried set, with job notifications (P6, P8).**
   This generalizes `jobNotifsAwaitingRequest` and
   `settleJobNotificationsCarriedByRequest`, which are already this design
   for one kind. Notifications get admitted on every turn kind and wake the
   quota wait.
2. **Watch frames (P7, caller and delegate).** Settle on send, count at
   settle, and move the watch rules into admission. Then rebase #6068 onto
   it.
3. **Steering (P3, P5, P15, P16 queued).** The about 8 drain call sites
   become admission. This settles the Stop-hook gap one way or the other.
4. **Delegate attention, updates and steers (P9–P12).** Provenance and
   coverage move through admission. `promoteStagedAttentionCoverage` becomes
   the attention item's `onSent`.
5. **Continuations and runtime nudges (P13, P14).** These are mostly
   deletions in the openers.

## Testing

There is one table test, `TestAdmittedInputRules`, with a row per item kind.
It uses the scripted provider and the real session loop. For each kind it
asserts:

- **Unsent.** A Stop before the send leaves the item owed, and the next
  turn carries it.
- **Sent once.** One sent request settles it exactly once, and a
  replay after restart does not write it twice.
- **Provenance.** The turn that carries it reports its provenance.
- **Quota wake.** Submitting it during a quota wait wakes the wait, and the
  retried request carries it.
- **Watch counting** (watch kinds only). Deliveries are counted once, after
  coalescing.

Adding a kind means adding a row. A review finding of the form "path X
misses rule Y" becomes a missing row, not a new round.

## Open questions for Jesse

1. Should user input count as incorporated when its request is sent, not
   when it is appended? It is more truthful, but the queued/incorporated
   chip timing changes on every client.
2. Should the 19-path inventory, as a table without line numbers, go in
   `docs/product/subsystems.md` as the evergreen record of "what feeds a
   request"?
3. What happens to #6068? My recommendation is to leave it open, unmerged,
   until step 2 lands, then rebuild its rules on admission and close it.
   The alternative is to merge it as is and refactor afterwards, which
   leaves its four open findings in place in the meantime.
