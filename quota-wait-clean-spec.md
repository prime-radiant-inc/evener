# Quota Wait Visibility — Clean Implementation Spec

Fixes #6657. This spec replaces the accreted implementation on PR #6699 with a
design derived from everything 30 review rounds taught about the failure modes.
It is written to be implemented from scratch against `origin/main`.

## Problem

When a provider quota cap pauses a session, the session pane showed nothing:
no wait state, and nothing in the sidebar row. On reset the pre-existing
countdown in the pane and sidebar disappeared instead of becoming a visible
wait. A user sees a silent, apparently-wedged session for up to hours while
the daemon is knowingly waiting out a reset, and a delegate pane opened or
reloaded mid-wait showed no wait at all.

## Stage 0 — Wire prerequisite (implements before any client work)

**The daemon assigns one orderable identity per wait and carries it on all
three producers:** the wait's start announcement, its zero-delay wake, and the
live thread read's failure summary. On `origin/main` none of these carry a
stamp, and the whole client design below keys on it. Without the wire change,
a steer can trigger two waits in one turn with the same reset deadline and the
client has no identity to tell whether a delayed wake or read belongs to the
first wait or the second.

- `waitStartedAt` on `ThreadModelRetryParams` and on `ThreadFailure`:
  **a strictly increasing identity the daemon issues per turn when a wait
  begins** — not raw wall clock, which can repeat or move backward and would
  let the first wait's wake clear the second, or the second's start read as
  stale. The producer advances it monotonically (a session-local sequence:
  last-issued + 1) so a later wait within a turn always compares greater,
  even when epoch time collides or steps back. The deadline arithmetic
  (`deadline = wall-clock wait start + delayMs`) is separate: the identity
  orders waits; the wall clock times them.
- `resumesAt` (absolute epoch-ms instant) on the live start announcement
  (`ThreadModelRetryParams`): the daemon's own answer for when the call
  resumes, the same instant the read summary carries. The client renders the
  countdown from it directly — never `identity + delayMs`, which is nonsense
  arithmetic once the identity is a sequence. `delayMs` stays for the
  ordinary retry line and the receipt-clock fallback.
- `endedTurnId` on the terminal thread status frame: the id of the turn the
  Stop ended, which the frame today omits. Without it no client rule can
  always tell whether a buffered Stop ended the snapshot's running turn or
  an older turn the snapshot already replaced — the exact ambiguity that
  consumed six rounds of the previous implementation. Older-daemon frames
  (no `endedTurnId`) preserve the running turn from the snapshot; they
  never infer ownership.
  **Production path:** the root server suppresses the event projector's
  execution-end status and can publish settlement through finishProcessing,
  which clears the active turn id before building its fallback frame — so
  Stage 0 must capture the ended turn's id BEFORE the settlement path
  clears it and carry it through every root and delegate terminal
  publisher, or a real root Stop arrives looking like an older-daemon
  frame and Stage 2 ignores it. Stage 2 is gated on observing the correct
  id in emitted Stop frames.
- **Older-daemon fallback:** a frame with no stamp (a daemon that predates the
  field) reconciles by turn ownership only, never by clock, and may not clear
  or overwrite a stamped wait's deadline (see Legacy below).
- **Acceptance (producer layer):** the stamp is strictly increasing per
  turn and present on start, wake, and read summary; the start announcement
  carries `resumesAt` agreeing with the read summary for the same wait;
  the terminal status carries `endedTurnId`. (The stale-wake reconciliation
  this enables is Stage 2's gate.)

## Stage 1 — Daemon projection for delegates

A wait must be readable, not only live-broadcast. `thread/read` serves the
stored descendant projection, which forwards `modelRetry` notifications
without storing the wait — so a delegate pane opened or reloaded mid-wait
would show no wait while its sidebar row shows one.

- Materialize the delegate's quota wait on its own projected thread: the
  projection stores the wait's deadline and stamp the same way the session's
  own failure facet does, so the existing read cut captures it without
  consulting the live session under the cut. Elapsed time is not stored on
  the projection — the rendering contract takes it from the live start
  announcement, whose sample is the call's real elapsed time.
- **Acceptance (read layer):** a fresh delegate read restores the wait
  during a slot hold past the reset, and clears it when the retry begins.
  (The pane-vs-sidebar agreement this enables is Stage 3's gate.)
- The elapsed sample the producer supplies at wait start is the call's real
  elapsed time (time already spent in the model call before the limit hit),
  not zero; verified with a call that spends measurable time before hitting
  quota.

## User-visible contract

1. While the daemon reports a session waiting out a provider usage limit, the
   session pane shows a wait line at level `quota`, above every
   silence-derived level and outside any quiet gate. It says either when the
   call resumes (a countdown) or that the reset has passed and the retry is
   still pending.
2. The sidebar row shows the same wait. **Surface authorities:** an open pane
   holds the wait from its live frames (authoritative for that pane's display);
   the row with no open pane holds it from the navigation failure summary the
   daemon refreshes into reads. Permitted lag: the row may trail the pane by
   the navigation refresh until its next read — an early wake clears the pane
   immediately while the row keeps its last sampled summary until refreshed.
   Both surfaces must agree on the wait's *state* (waiting / countdown /
   pending / ended) at the moment each observes it; they need not agree on
   which instant each learned it.
3. **The wait's lifetime is the daemon's, never the client's clock.** Crossing
   the reset instant changes the line from countdown to pending; it does not
   end the wait. Only three things end a recorded wait: the daemon's
   zero-delay wake announcement (the retry going out), a turn boundary, or an
   authoritative read that shows no wait. A delegate held at its reset while
   every execution slot is occupied can stay waiting for hours.
4. When a live announcement established the wait, the line carries the call's
   model tag (when it differs from the pane's primary model) and the call's
   elapsed time. A snapshot-only wait (rehydrated deadline, no live
   announcement) carries neither — the snapshot cannot attribute them.
5. When the wait ends, ordinary liveness takes over with no residue: no chip,
   no countdown, no stall clock restamped by the wait's own frames.

## Domain model

**Wait identity is the daemon's stamp, not the turn id.** One turn can wait
twice (a steer wakes the first wait; the retry hits the limit again). A turn id
says only that a frame belongs to the same call site. Every ordering decision
between waits compares stamps; a frame for an older wait of the same turn is
never news. **Matching stamps, not turn ids, gate the live retry metadata:**
elapsed time and model tag ride the line only beside the wait whose stamp the
announcement carries.

A wait carries:
- `deadline` — absolute resume instant (ISO string). Producers: the thread
  read's failure summary (`resumesAt`), and a stamped live announcement
  (its own `resumesAt`). An unstamped announcement (older daemon) installs
  no deadline.
- `stamp` — the daemon-issued `waitStartedAt`, stored only as the daemon's
  value, never the client's receipt instant (a fast browser clock would
  otherwise refuse authoritative post-reset reads).
- `endedStamp` — survives the wait it ended, to refuse a later read that
  captured the wait while it still stood.
- chip metadata — model, elapsed sample, receipt instant. From the matching
  live announcement only.

**One time basis per wait.** When an absolute deadline exists, it is the whole
countdown; past it, the wait is pending. The receipt clock is a fallback only
when no absolute deadline exists at all — and past that fallback too, the wait
is pending, never resurrected onto another basis. A known deadline that has
passed can never be overridden by a timer that still has runway (a late
announcement).

**Authority order:** the daemon's live frame > the thread read's sampled
summary > the client's inference. The server refreshes the failure facet
before taking the read cut, so a read is authoritative about any wait that
existed at its cut. A read's authority is also bounded by which execution
supplied the wait it carries: the daemon binds the cached wait summary to
the turn that was running when the facet sampled it, and a read serves it
only while that execution is still the running one. finishProcessing and
SetProcessingTurn change the running turn without re-sampling the facet, so
the drain can lag the session loop and a replacement turn can be published
while the cache still holds the stopped turn's wait — a read taken in that
window must not pair the new turn with the old turn's deadline. The binding
is what retires the projected wait with its owning turn: the read that
observes the ended or replaced execution serves no wait from it, in the same
read. Ordinary failure summaries (a failed turn the session rests on) carry
no binding and serve as before: the status gate alone governs them. A wait
sampled under no published execution is bound to the empty id, which no
running turn matches, so the first turn that publishes un-matches it.

## Stage 2 — The reconciliation pass

The client's hard problem: notifications buffered against a read that races
with live events. The hub delivers the read response **between** the pre-cut
buffer and the post-cut tail, and the two sides are reconciled differently:

- **Post-cut frames apply in delivery order, as themselves.** They postdate
  the snapshot by construction; ordinary reducer rules fold them. A genuinely
  new wait announced post-cut must publish over an idle snapshot. Versioned
  history and queue-revision checks apply to them exactly as to live frames,
  and a frame the repository already applied once is not applied again.
  Counting a physical arrival is not the same act as surviving an adopted
  read: a wait start the repository folded live while its read was in flight
  is still overwritten by the read's own adoption, because the read's cut
  predates it. The read therefore neither retires that wait nor records its
  stamp as ended — the repository is told which wait stamps the tail already
  installed, its wait-free reconciliation spares exactly those, and the tail
  re-publishes the wait after the read applies. A frame whose effect the
  current model still reflects is the only frame replay skips.
- **Pre-cut reconciliation is enrichment, not establishment.** A pre-cut
  frame may fill what the adopted snapshot lacks: an equal-stamp start's
  metadata, or a terminal status whose `endedTurnId` matches the turn the
  snapshot still shows running (the frame supplies its own ownership; a
  missing or different id preserves the snapshot's running turn). It may
  not contradict what the snapshot reflects: a snapshot showing no running
  turn already reflects any Stop; a snapshot naming turn B already reflects
  the A→B replacement.
- **A refused read scopes only its own quota reconciliation.** When the
   repository refuses the read (versioned history said it was stale), the
   pre-cut buffer's *quota transitions* — which were ordered against that
   snapshot — are dropped. Versioned history ingestion, queue updates, and
   every unconsumed post-cut frame are unaffected: they deliver to the
   current owner by their own rules. Frames already applied are applied only
   once.

The pre-cut pass folds the buffer into transitions first, then reconciles
each transition against the adopted snapshot. **The adopted snapshot is
authoritative for pre-cut turn ownership and wait presence**: turn
establishment and replacement are post-cut transitions delivered in order.
The evidence question the rules below all turn on is *who supplies turn
ownership*: a pre-cut terminal status applies only when its own
`endedTurnId` matches the turn the snapshot shows running AND no buffered
frame replaces that turn. A Stop with a missing or different id — including
one for an older turn the snapshot has already replaced — preserves the
snapshot's running turn and retires nothing. Live (post-cut) Stops keep
ordinary delivery-order handling, whatever their vintage.

- **Establishment** (active status): never admissible pre-cut — an idle
  snapshot already reflects every pre-cut Stop the pane could have buffered
  against it, and a genuinely new turn arrives post-cut, in order, and
  publishes by the ordinary reducer rules (scenario 18's start is post-cut).
- **Wait start** (positive-delay announcement): fills the deadline and stamp
  — but the snapshot is authoritative for wait PRESENCE too: the daemon
  clears its live wait before emitting the wake, so a read can legitimately
  show the same turn running with no wait while the buffer still holds the
  start ahead of its wake. A pre-cut start against a snapshot showing no
  wait therefore installs nothing — the wait's end is already reflected.
  Pre-cut starts may only enrich: a matching-stamp start fills chip
  metadata on an existing wait. New waits are established post-cut, in
  order. Skipped when older than the held stamp, or when a later buffered
  transition replaces its turn.
  Absence is evidence only from a snapshot that can report waits for the
  thread it serves: a stamped daemon's read, or a legacy daemon's ROOT read,
  whose failure facet the daemon refreshes when the wait begins. A legacy
  daemon's DELEGATE read is not that — the projection forwards the live
  announcement but stores no wait, so its showing no wait is silence, not a
  clear. Against such a snapshot, an unstamped buffered start whose turn
  matches the snapshot's running turn installs the wait on the receipt-clock
  fallback (its delay from receipt, no absolute deadline, no model tag or
  elapsed attribution), and the wait ends only by its own unstamped wake, a
  turn boundary, or a read from a producer that can report waits — never by
  another absence from the same legacy delegate feed. An unstamped start
  still never clears or overwrites a stamped wait. Known limitation: a cold
  reload of a legacy delegate mid-wait, with no live announcement arriving,
  shows no wait in the pane — nothing the client holds names one. Its row
  still shows the wait, sampled live from the owning root's roster entry.
- **Wait end** (zero-delay wake, or a same-buffer start+wake pair): clears
  the matching wait's deadline, stamp, and chip together; a same-buffer pair
  over a snapshot already showing the resumed output is one cancellation —
  ended stamp kept, nothing installed. A wake for a wait the model does not
  hold contributes only its ended stamp: no chip, no stall restamp.
- **Replacement** (an active status naming a different turn, no idle
  between): a post-cut transition — it publishes the new turn and retires
  the old turn's wait as one unit, never depending on whether the new turn
  has its own announcement. Pre-cut, the snapshot's own transition wins: a
  snapshot naming C is not overwritten by a buffered A→B frame.
- **Termination** (a terminal status naming no turn): applies pre-cut only
  when its `endedTurnId` names the turn the snapshot shows running AND
  nothing in the buffer replaces that turn. The frame supplies its own
  ownership; nothing is inferred. An older-daemon Stop with no
  `endedTurnId` preserves the running turn from the snapshot — scenarios
  10 and 19 differ exactly by whether the frame names the snapshot's turn.
  Post-cut Stops apply in delivery order regardless: a live older-daemon
  Stop still clears the wait through the ordinary reducer rules — only the
  PRE-CUT reconciliation refuses to infer ownership.
- **Legacy unstamped frames** reconcile by turn ownership: an unstamped wake
  owning the held legacy wait's turn clears that wait — deadline and chip
  together, including the chip-only case. An unstamped frame may never clear
  or overwrite a stamped wait.

## Stage 3 — Liveness line and sidebar rendering

- The quota line answers first: `level: "quota"`, no quiet gate, ahead of
  every silence-derived reading.
- Countdown while the deadline is ahead; pending (`passedReset`) once it
  passes; identical metadata in both halves — crossing the reset changes the
  promise, never the facts beside it.
- Elapsed advances from the announcement's receipt instant
  (`sample + (now - receivedAt)`), clamped so it never goes negative across
  tick boundaries.
- The wake's zero-delay announcement and the resumed call's ordinary retries
  are not waits; they drive ordinary retry-progress text.
- The sidebar row always reads the navigation failure summary and catches
  up on its next normal refresh; an open pane never wires its live updates
  into sidebar refetches (see Surface authorities and scenario 16).

## Acceptance scenarios

Each of these was a real review finding; a clean implementation turns red on
all of them. Tests assert behavior (levels, computed values, class presence)
— never message phrasing.

1. Limit hits while the pane is open: wait line shows immediately with a
   countdown, no quiet gate, in hours not a seconds countdown.
2. Reload mid-wait: the read's failure summary rehydrates the deadline; the
   line survives with no live frame.
3. Wake ends the wait; the resumed call's ordinary retries do not resurrect
   it; a late retry naming the ended turn cannot restamp a new wait.
4. Two waits of one turn — **including the case where both waits' reset
   deadlines match exactly**: the first wait's frames neither clear nor
   overwrite the second's deadline, stamp, or chip (stamp ordering, in the
   shared reducer and both read-merge paths).
5. A buffered active-status+announcement pair behind a Stop the snapshot
   reflects does not revive the stopped turn.
6. A same-buffer start+wake pair over a snapshot already showing the resumed
   output cancels: ended stamp kept, no countdown, no chip.
7. A→B replacement with no idle between: the pane publishes B and retires
   A's wait, regardless of B's announcements.
8. An unstamped wake owning the held legacy wait's turn clears deadline and
   chip — including the chip-only legacy wait — and never touches a stamped
   wait.
9. A read the repository refuses drops only its pre-cut quota transitions;
   versioned history, queue updates, and post-cut frames deliver normally,
   each applied at most once.
10. A buffered terminal status whose `endedTurnId` names the turn a snapshot
    still shows running applies, ending that turn; an older-daemon Stop with
    no `endedTurnId` preserves the snapshot's running turn.
11. A passed read deadline plus a late unstamped announcement with receipt
    runway: pending, not a countdown leaping forward.
12. An equal-stamp buffered announcement fills model/elapsed/receipt metadata
    without moving the snapshot's deadline; metadata matching is by stamp,
    not turn id.
13. Elapsed advances with time since receipt, clamps at zero, and both it and
    the model tag survive the reset crossing.
14. Snapshot-only waits carry no model tag and no elapsed time.
15. A wake for a wait the model does not hold records only the ended stamp —
    no chip, no stall restamp.
16. A delegate pane reloaded mid-wait during a slot hold past reset shows the
    pending wait (not silence), and clears it when the retry begins; the
    sidebar row agrees after its next navigation summary refresh — an open
    pane does not force one; the row is a read-sampled surface and trails
    by one refresh whether or not a pane is open (per the
    surface-authority rule; agreement is eventual, not immediate).
17. An early wake clears the open pane immediately; the row with no pane
    trails until its next navigation refresh, then agrees.
18. An idle snapshot followed by a genuine post-cut wait start publishes the
    new wait over the idle snapshot.
19. An older buffered Stop (the buffer holds no replacement frame after it)
    followed by a snapshot naming a newer running turn: the snapshot wins;
    the Stop retires nothing — its `endedTurnId` names a turn that is not
    the snapshot's running one, and nothing infers ownership.
20. The producer's elapsed sample at wait start reflects real call time
    (nonzero for a call that ran before hitting the limit).
21. A wait start the repository accepted live while its read was in
    flight survives the read's own wait-free adoption: the read neither
    retires the wait nor records its stamp as ended, and the post-cut tail
    re-publishes it after the read applies — the replay skips only a
    frame whose effect the current model already reflects.
22. A root read taken after a Stop or an A→B replacement, with the event
    bridge paused so the ended turn's end has not drained, pairs no quota
    wait with the running turn it names: the cached summary belongs to the
    execution that supplied it, and it does not serve — a wait the read
    still carries names the same turn the read names. Ordinary failure
    summaries survive the next turn's status move untouched.
23. On an uninterrupted legacy delegate feed, an unstamped buffered start
    whose turn matches the snapshot's running turn installs the wait on
    the receipt clock against a delegate read that cannot report waits —
    the read's absence is not a clear — and a matching unstamped wake or
    turn boundary ends it. A cold legacy reload with no live announcement
    shows no wait in the pane, and its row still shows the wait from the
    owning root's roster sample.

## Anti-requirements (what 30 rounds proved NOT to build)

- No rule that admits or rejects a single frame by pairing it with another
  frame. Reconcile transitions.
- No client-side inference that a wait ended from any clock: no deadline
  expiry, no receipt timer expiry, no `lastFrameAt` heuristic ends a wait.
- No second time basis for a wait that has one.
- No storing the client's receipt instant where the daemon's stamp belongs.
- No phrasing assertions in tests; no copy as an oracle.
- No unstamped frame clearing or overwriting a stamped wait.

## Staging and surfaces

Dependency order — each stage's acceptance gates the next:

1. **Stage 0, wire:** daemon stamp on all three producers; AppWire fields;
   older-daemon fallback. Gated on the complete Stage 0 producer contract:
   the stamp is strictly increasing per turn and present on start, wake,
   and read summary; the live start announcement's `resumesAt` matches the
   ROOT read summary's for the same wait (delegate read parity is Stage
   1's gate, which Stage 1's materialization makes possible); and emitted
   root and delegate Stop frames carry the correct `endedTurnId`
   (captured before settlement clears it); and a root read taken after a
   Stop or an A→B replacement, with the event bridge paused so the ended
   turn's end has not drained, serves no quota wait for a turn other than
   the execution that supplied it — the cached summary is bound to that
   execution (see Authority order).
2. **Stage 1, projection:** the delegate's wait materialized on its projected
   thread; the elapsed sample produced real at wait start. Gated on read
   behavior: a delegate read restores the wait and clears it at retry.
3. **Stage 2, client reconciliation:** shared reducer fields, both read
   merges, the replay pass — the stale-wake and equal-deadline
   reconciliation acceptance lives here, against synthetic stamped fixtures
   Stage 0 makes possible.
4. **Stage 3, rendering:** pane liveness line, sidebar row, the evergreen
   docs (`docs/product/subsystems.md` S09 row + friction punchlist R05) in
   the same change as the code they describe. The pane-vs-sidebar agreement
   acceptance lives here.
