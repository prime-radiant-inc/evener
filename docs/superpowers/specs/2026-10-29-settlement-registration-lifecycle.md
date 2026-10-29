# Settlement registration lifecycle and the established predicate

## Intent

One authoritative rule decides when a pane's bounded local opening may treat a
tracked hydration's settlement as having established the display, so the
opening completes instead of arming recovery. This note records that rule and
the settlement-registration lifecycle it completes, after four review findings
on the same seam converged on it. It is a design record of behavior already
implemented in the thread store and the transcript repository; nothing here
authorizes publication, a main merge or deployment.

## The settlement-registration lifecycle

A tracked hydration (an initial claim or a tracked refresh) registers its
settlement beside the reads it composes with. Each registration:

- is **born** with a tracked read. An initial claim registers beside tracked
  refreshes, so a consumer that starts waiting mid-read still captures a
  promise covering the entire read.
- is **displaced** by a newer attempt under the identity guard: the newer
  hydration's registration replaces the map entry, and the older settle wakes
  nothing and deletes nothing.
- is **composed** with a recovery span: the chain's whole-span registration
  may not be displaced by a plain hydration's, and a tracked read joins under
  it. Waiters are notified early, when the current hydration itself settles —
  published or failed — and a wake that fails certification re-arms the wait
  rather than falling through to the backstop.
- is **superseded** by a quiet certification: a claim's post-hydration
  certification whose own publication completed the display, with an older
  frontier still outstanding, runs without the consumer hold, and when it
  establishes the display it takes exclusive opening ownership — retiring the
  superseded published opening's consumer hold and notifying that open's
  parked wait.
- is **retired** with the drained lifetime: `dropUnpinnedModel` retires the
  registration the same way `releaseThread` does, waking parked waiters via
  the sentinel identity so no later consumer parks on a completion whose
  source can no longer publish.

Alongside the lifecycle, one error distinction governs what a settlement may
automatically do next: a **transport failure** keeps the ordinary owner-scoped
retry and may hand a stranded opening to source recovery; a **structured
history failure** (`transcriptHistoryFailed`) records its one visible
diagnostic and never automatically reaches the source retry handoff — the
next read comes from the next event: a resync, a reconnect, or the user.

## The established predicate

An open parked on a local read is woken when the next tracked hydration
settles. Its verdict accepts the wake and completes the open —
`superseded-by-source` — exactly when the settled window is established:

- a **non-empty** window is established when it is both bodied (every
  expected item has a displayed counterpart with a real body) and resident
  (the required foreground range holds).
- an **empty** window is established when it carries accepted-read
  provenance and its recorded-history expectation is empty at the items
  level: every recorded history turn has zero items, reading
  `history.turns` as the authoritative source of the expectation when history
  is present, and an accepted source read must have named the window
  (`#coverageReadId` set, with a read whose coverage named no rows).

Emptiness alone is never evidence. Four review findings on this seam are the
four shapes the rule subsumes:

1. **Empty turns.** A zero-turn accepted window expects nothing, so it is
   missing nothing; the resident range never exists to require it.
2. **Empty items.** An in-progress turn with `items: []` is the same
   expectation at the items level: the predicate asks whether every expected
   turn carries zero items, not whether there are no expected turns.
3. **Unverified cache shells.** The cached lookup seeds a repository from
   metadata with empty turns before any source read succeeds, and rows that
   disappeared between the metadata lookup and the restoration are
   indistinguishable from a genuinely empty transcript — so only an accepted
   read that named the empty window establishes it, and the seed alone never
   certifies.
4. **Overlay versus history.** An accepted read can carry
   `history.turns: []` while live overlays contribute display turns, and
   those overlay turns are not recorded history to restore — the expectation
   reads the recorded history first, the same history-first ordering the
   resident-range and window checks already use.

A wake that cannot certify reports `recovery-needed` and arms the one-owner
opening recovery chain; a structured history failure stops that chain before
its source handoff, per the error distinction above.

## Where it lives

- `TranscriptRepository.expectsEmptyForegroundWindow`
  (`cmd/evener-hub/frontend/src/stores/transcriptRepository.ts`) is the empty
  side of the predicate, provenance-guarded and history-first.
- `TranscriptRepository.holdsBodiedForegroundWindow` and
  `holdsResidentForegroundRange` are the non-empty side.
- The verdict, the quiet certification and the recovery chain live in the
  entry transcript routes module
  (`cmd/evener-hub/frontend/src/stores/entryTranscriptRoutes.ts`); the
  registration lifecycle and the transport-vs-structured distinction live in
  the thread store (`cmd/evener-hub/frontend/src/stores/threads.ts`).
- The behavioral coverage for all four shapes is in
  `cmd/evener-hub/frontend/src/stores/threads.entryProduction.test.ts`, one
  red-first test per shape.
