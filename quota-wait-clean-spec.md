# Quota Wait Visibility — Clean Implementation Spec

Fixes #6657. This spec replaces the accreted implementation on PR #6699 with a
design derived from everything 30 review rounds taught about the failure modes.
It is written to be implemented from scratch against `origin/main`.

## Problem

When a provider quota cap pauses a session, the session pane showed nothing:
no error state, no indicator, and nothing in the sidebar row. A user sees a
silent, apparently-wedged session for up to hours while the daemon is
knowingly waiting out a reset.

## User-visible contract

1. While the daemon reports a session waiting out a provider usage limit, the
   session pane shows a wait line at level `quota`, above every
   silence-derived level and outside any quiet gate. It says either when the
   call resumes (a countdown) or that the reset has passed and the retry is
   still pending.
2. The sidebar row shows the same wait, from the same shared text source, so
   both surfaces agree.
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
never news.

A wait carries:
- `deadline` — absolute resume instant (ISO string). Producers: the thread
  read's failure summary (`resumesAt`), and a stamped live announcement
  (`waitStartedAt + delayMs`, the daemon's own arithmetic). An unstamped
  announcement (older daemon) installs no deadline.
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
summary > the client's inference. The server (already landed) refreshes the
failure facet before taking the read cut, so a read is authoritative about any
wait that existed at its cut.

## The reconciliation pass — transitions, not frame pairs

The client's hard problem: notifications buffered against a read that races
with live events. The accreted implementation admitted or rejected individual
frames by pairing rules (a status replays only when a later announcement needs
its turn, unless a later frame replaces the turn, unless...). Every rule's
edge case was the next review finding. **Do not rebuild that.**

The replay pass over a hydration buffer works in three steps:

1. **Gate:** the buffer belongs to the read it was cut against. If the
   repository refused the read (versioned history said it was stale), the
   buffer is unordered against anything — drop it entirely.
2. **Fold the buffer into transitions first.** Walk the buffer in order and
   derive, per turn: establishment (active status), wait start (positive-delay
   announcement), wait end (zero-delay wake, or a same-buffer pair that
   cancels), replacement (an active status naming a different turn, which
   arrives with no idle between), and termination (a terminal status naming no
   turn). A same-buffer start+wake pair is one *cancellation* transition: keep
   the ended stamp, install nothing.
3. **Reconcile each transition against the adopted snapshot**, applying a
   transition only when the snapshot predates it:
   - A snapshot that already reflects a transition (or something later) wins.
     Concretely: a snapshot showing no running turn already reflects any
     Stop; a snapshot naming turn B already reflects the A→B replacement.
   - A terminal transition against a snapshot that still shows the turn
     running is news by construction — apply it.
   - An A→B replacement publishes B and retires A's wait **as one unit**;
     it never depends on whether B has its own buffered announcement.
   - An equal-stamp start against a read that already carries the stamp
     fills chip metadata only; the deadline does not move.
   - A wake for a wait the model does not hold contributes only its ended
     stamp. It never installs a chip ("in progress" over a call the snapshot
     already shows resumed) and never restamps the stall clock.
   - Legacy unstamped frames reconcile by turn ownership: an unstamped wake
     owning the held legacy wait's turn clears that wait — deadline and chip
     together, including the chip-only case (announcement installed a chip,
     no deadline).

## Liveness line rendering

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

## Acceptance scenarios (the regression corpus)

Each of these was a real review finding; a clean implementation turns red on
all of them. Tests assert behavior (levels, computed values, class presence)
— never message phrasing.

1. Limit hits while the pane is open: wait line shows immediately with a
   countdown, no quiet gate, in hours not a seconds countdown.
2. Reload mid-wait: the read's failure summary rehydrates the deadline; the
   line survives with no live frame.
3. Wake ends the wait; the resumed call's ordinary retries do not resurrect
   it; a late retry naming the ended turn cannot restamp a new wait.
4. Two waits of one turn: the first wait's frames neither clear nor
   overwrite the second's deadline, stamp, or chip (stamp ordering, in the
   shared reducer and both read-merge paths).
5. A buffered active-status+announcement pair behind a Stop the snapshot
   reflects does not revive the stopped turn.
6. A same-buffer start+wake pair over a snapshot already showing the resumed
   output cancels: ended stamp kept, no countdown, no chip.
7. A→B replacement with no idle between: the pane publishes B and retires
   A's wait, regardless of B's announcements.
8. An unstamped wake owning the held legacy wait's turn clears deadline and
   chip — including the chip-only legacy wait.
9. A read the repository refuses contributes no replay at all.
10. A buffered terminal status against a snapshot still running that turn
    applies (news by construction).
11. A passed read deadline plus a late unstamped announcement with receipt
    runway: pending, not a countdown leaping forward.
12. An equal-stamp buffered announcement fills model/elapsed/receipt metadata
    without moving the snapshot's deadline.
13. Elapsed advances with time since receipt, clamps at zero, and both it and
    the model tag survive the reset crossing.
14. Snapshot-only waits carry no model tag and no elapsed time.
15. A wake for a wait the model does not hold records only the ended stamp —
    no chip, no stall restamp.

## Anti-requirements (what 30 rounds proved NOT to build)

- No rule that admits or rejects a single frame by pairing it with another
  frame. Reconcile transitions.
- No client-side inference that a wait ended from any clock: no deadline
  expiry, no receipt timer expiry, no `lastFrameAt` heuristic ends a wait.
- No second time basis for a wait that has one.
- No storing the client's receipt instant where the daemon's stamp belongs.
- No phrasing assertions in tests; no copy as an oracle.
- No server changes required: the daemon already refreshes the failure facet
  into the read envelope before the cut.

## Surfaces touched

- `appwire-client/typescript`: shared reducer (wait stamp/ended/retirement
  fields), read merges, `ThreadModel` fields.
- `cmd/evener-hub/frontend/src/stores/transcriptRepository.ts`: the replay
  pass (rebuilt as above).
- `cmd/evener-hub/frontend/src/panes/session/transcript/flow/`: liveness
  line + `describeLiveness`.
- Sidebar row (shared wait text).
- `docs/product/subsystems.md` S09 row + friction punchlist R05
  (evergreen, same change).
