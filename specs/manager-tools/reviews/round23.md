# Round 23 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tnuqSYGIsqTNRQeZqGr`), S gpt-6.1-sol-1m
(`dlg_034tnwXRUSjUYL8oAXsuz2`), T deepseek-4.1-flash-background
(`dlg_034tnurtkBseXSdVGDWgIy`). Reviewed at `500471fe8d` (the round-22
pass addendum, on top of `47dc961802` the pass, on top of `62896e9309`
the round-22 record) over the production-identical base `2be411cd8f`.
All three seats ran the required reds uncached (`-count=1 -v`, sources
inspected first): 4/4 red families reproduced for their documented
reasons (five individual reds; T and R both noted the brief's
single-quoted `\|` regexes match a literal pipe in Go and reran with
bare `|`), 4/4 characterizations passing, zero disputed, worktrees
clean, owned scratch removed.

## Closures

- **S22-1 — CLOSED (S, dynamically; the round's headline).** S rebuilt
  the designed mechanism in scratch overlays and closed its own
  ADDRESSED-CHILD red: a REAL delegate spawn through the registered
  tool, the first assistant entry committed, the second provider call
  held, the typed first-record callback waited for AFTER
  `RecordDescendantAppEvent` returned — metadata == field ==
  full-history, the root control passing through `BridgeEvent` (no
  manual facetAll concealment), and the IDLE RESTORED-CHILD payload
  bootstrap passing (real restore constructor, fresh server child
  projection, zero commit signals), all under `-race -p 2`. The causal
  red: disabling ONLY the descendant consumer reproduces the blank
  metadata. Evidence: S's jobs `W7V2PANnddqB` (pass),
  `PdSz4AHBtVog` (red), `O4VUnHb0Qbrf` (the sampling-rule proof tests
  pass with the accessor added).
- **T22-2 — CLOSED (T, and R's positive re-verification).** The three
  direct field writers leave plain callbacks installed before attach;
  `setTranscriptLocked` reads the field's current value at install; no
  path wraps a dispatcher in a dispatcher AT THE NAMED SITES. The
  setter's own slot write is the new finding T23-2.
- **T22-4 — CLOSED (T; S and R concur).** The restated guard matches
  the real schedule; the pre-stamp/unit-call pin is honest.
- **T22-1 / T22-3 — PARTIAL, subsumed.** T22-1's residue is the
  three-seat seed finding below (T23-5); T22-3's residue is the
  overbroad invariant sentence (T23-3).
- **All five of R's round-22 positive verifications — CONFIRMED**
  (dispatcher wiring orders; emitter lifecycle at session shutdown;
  the amended rule's proof coverage for the child accessor; the
  compaction-replay harness as the PRESERVED-TAIL seam; the doors
  text).

## New findings — 3 significant, 3 minors; every load-bearing claim
verified in-tree by the chair at the exact hop

### T23-1 — significant — the consumer's gate/lock placement contradicts
the pinned projection-gate invariant, and its stated rule is inapplicable

The pass text attached "OUTSIDE the projection gate" to the INSTALL,
while naming as the install point the `ThreadStartedParams` arm at
`server/appwire_record_events.go:428-445` — which lives INSIDE the
route's `CommitProjection` closure (`:386-387`), and
`CommitProjection` holds `projectionMu` across the commit
(`internal/appserver/server.go:173-180`). A child-field sample taken
there is a session call under the projection gate — exactly what
`server/thread_envelope.go:15-21` pins to zero (asserted by
`thread_envelope_test.go:679-681`, "want 0"). And the cited "amended
sampling rule's lock order" cannot cover this path: the
`EnvelopeSampling` rule's own doc scopes sampling to the DRAIN
GOROUTINE (`agent/session_envelope_sampling.go:15-18`), while the
descendant route runs synchronously on the child's emitter goroutine
(`agent/session_events.go:501-507`). The pass text INVERTED S's causal
control (S: sample outside the gate, then commit the field on the
row). Verified by the chair at every hop.

### R23-R-1 — significant — the descendant handoff has neither an
emitting-child carrier nor an incarnation/order fence

The child invokes only `s.descendantEvent(ev)` after releasing
`eventsMu` (`agent/session_events.go:501-517`); `SessionEvent` carries
IDs and data, no session handle or sampler
(`agent/events/events.go:186-192`); production closes over the ROOT
(`cmd/evener/serve_bridge.go:191-197`); the route fences only the root
identity (`server/appwire_record_events.go:386-390`) and the cached
child row's `InstanceID` is the owner's (`:409-411`). The reclamation
path's own comment documents the accepted race: idle release UNHOOKS
the old runtime (record removal plus live-pointer clear) BEFORE
teardown, and a racing send cold-restores a replacement reusing the
child session ID (`agent/delegate_tree_reclaim.go:656-679`); work a
callback registers between the pre-gate snapshot and the teardown
"lands on a runtime whose release is already committed."
`SessionStartData` carries no child-incarnation token
(`agent/events/payloads.go:18-54`). Production-triggerable order: the
old runtime queues frontier A, idle release unhooks, a cold restore
advances the row to frontier B, the delayed old callback writes A —
the root fence passes, the row regresses. The metadata promise
(`3-transcript-status-consistency.md:188-192`) and the ADDRESSED-CHILD
journey are not closed without a fence. Verified by the chair at every
hop, including the reclamation comment and the owner-only fence.

### T23-2 — significant — `SetTranscriptRecordedFunc` replaces the
dispatcher after attach, so the internal listener is uninstalled at
every daemon start

`SetTranscriptRecordedFunc` (`agent/session_execution.go:241-246`)
writes the field AND calls `s.transcript.OnRecorded(fn)` directly — a
plain replacement of the hook slot. Production wires
`WireTranscriptHistory` AFTER construction on every daemon start
(`cmd/evener/serve.go:727`/`:758` build, `:819` wires, `:1152`
rewires; `server/appwire_histories.go:52` calls the setter), so the
root writer's slot holds the plain history callback, not the
dispatcher, until a reopen re-runs `setTranscriptLocked`
(`agent/session_attention.go:1836`, `:1911`). The root's metadata
frontier never refreshes from the record listener in the ordinary
lifetime. The round-22 pass enumerated the field's direct writers but
not the setter's hook-SLOT write. Verified by the chair at every hop.

### T23-3 — minor — the survey invariant is overbroad (T22-3 residue)

"No `placeVerbatim` production caller runs under a live session hook"
is false as worded: the compaction callers at
`agent/session_compaction.go:371` and `:390` run `PlaceVerbatim`
through `recordTranscriptLocked` — under the live hook — and are
excluded only by the `OriginalOrdinal` predicate (`:467-472`). Scope
the invariant to the nil-`OriginalOrdinal` copy class (the fork
prefix). Chair-verified: both callers exist; the fork's writer is bare
(`agent/fork.go:249-293`).

### T23-5 = R23-R-2 = S23-1 — minor, THREE-SEAT convergence — the seed
input is not in scope at the fresh attach site

The pass said the validated `transcriptEntries` is "in scope at both
attach call sites (`:696`, `:1427`)." False at `:696`: that attach is
in `NewSession` (`agent/session_init.go:408-724`); the local exists
only in the restore function (`:1015`, filled `:1023`, in scope at
`:1427`). S proved it by compilation: the literal instruction FAILS at
`agent/session_init.go:696` ("undefined: transcriptEntries"); passing
nil there compiles (S's jobs `wD9TtJY3k2Ae` failing,
`APmOyCv4bbsM` corrected). The design still works — a fresh
transcript has no prior executions of its own (the held boundary turn
at `:556` and the inherited context at `:690` record via
`PlaceSession`, which re-stamps kinds). The implementable contract:
fresh passes nil, restore passes its validated entries, reopens
reinstall the dispatcher without reseeding.

### T23-4 — minor — the child-session resolution hop unnamed (folded
into Fix 1)

The route receives only `(ownerThreadID, event)`; the server holds no
child-session resolver. S's working implementation supplies the shape
(Fix 1): construction-bound sampler closures, NOT a live-tree walk
(`subagent.mu` at `agent/jobs_nested.go:194-216` is not a permitted
sampler lock).

## The working shape (S's dynamically-proven implementation, the
mechanism of record for Fix 1)

Construction-bound sampler closures: a binder propagated beside
`spawn.descendantEvent`, the child's own accessor registered at
attach, looked up by event SessionID. The route RELEASES `Server.mu`,
samples the CHILD's `Session.mu` then the frontier leaf, and installs
the value on the cached child row under `Server.mu` OUTSIDE the
projection gate, with the owner identity rechecked. No cross-session
cycle found; the sampling-rule proof tests pass with the accessor
added. Attach precedes `SessionStart` on both constructors
(`:696`→`:708`, `:1427`→`:1563`); `SessionStart` is already a sealed
registered payload (`agent/events/eventdata.go:18-24,40`), so the
additive field needs no new kind; the SessionStart construction samples
the CURRENT stamped field (fresh forwarding is synchronous before the
child can begin, so an empty baseline cannot erase a live frontier).

## Disposition — three fix families plus two localizations, one pass

1. **THE CONSUMER'S PLACEMENT, RESOLUTION, AND LOCK ORDER (T23-1 +
   T23-4 + the resolution half of R23-R-1).** Both the sample and the
   install run OUTSIDE `CommitProjection`: the ThreadStartedParams-arm
   install-point claim is WITHDRAWN (that arm is inside the gate); the
   route resolves the child through construction-bound sampler
   closures (S's shape above, not the tree walk); the lock argument is
   stated in its own terms (the sampler takes only the child's
   `Session.mu` then the frontier leaf; no server lock is held across
   the sample; the child's commit holds the append lock plus the leaf —
   no cycle), and the amended drain-goroutine rule is cited as
   inapplicable to this path rather than load-bearing on it.
2. **THE INCARNATION FENCE (R23-R-1).** The emitting child binds its
   sampled value WITH an incarnation token while it is still
   authoritative — the child's boot generation
   (`schema.NextBootGeneration`, fsynced, never reused; the same
   incarnation token spec 4's fence uses) carried on the registered
   sampler closure — and the outside-gate row write checks the token
   against the current runtime's under `Server.mu`: a delayed
   old-runtime callback's token mismatches and is DROPPED. Draining
   the emitter before the runtime unhook is REJECTED as a design
   change to the reclamation path's documented bounded-release contract
   (its own comment accepts the callback race rather than joining
   unbounded callback sources); the token check inside the consumer
   respects that contract. Journey 8's ADDRESSED-CHILD case gains the
   REPLACEMENT sibling: old runtime queues frontier A, idle release,
   cold restore advances the row to B, the delayed A callback is
   dropped (red if A lands).
3. **THE SETTER RECOMPOSES (T23-2).** `SetTranscriptRecordedFunc`
   writes the field, then installs the dispatcher through the compose
   path (the `setTranscriptLocked`-equivalent install reading the
   field's current value) instead of `s.transcript.OnRecorded(fn)`
   directly; the spec names the setter as the fourth writer of the
   slot surface, the production wiring order (construction, then
   `WireTranscriptHistory` at `serve.go:817-820`/`:1152`), and the
   mechanism behind "survives every setter call."
4. **THE SCOPED INVARIANT (T23-3):** the survey-maintained invariant is
   scoped to `PlaceVerbatim` callers that record NIL-`OriginalOrdinal`
   copies.
5. **THE TWO SEED INPUTS (T23-5/R23-R-2/S23-1, three-seat):** fresh
   construction passes nil (no decoded view exists); restore passes
   its validated `transcriptEntries`; reopens reinstall the dispatcher
   without reseeding.

## Readiness

- Spec 1 — READY from all three seats (fourteenth consecutive round).
- Spec 2 — READY from all three seats.
- Spec 3 — NOT READY: T23-1, T23-2, and R23-R-1 each break the
  descendant handoff or the dispatcher composition as written.
- Spec 4 — READY from all three seats; the fence again at zero
  findings.

Significant trajectory r11-r23: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1, 3.
The count rose because the round-22 pass introduced a whole new
mechanism and the seats broke its text in three places; the mechanism
itself survived — S closed the original defect dynamically under
`-race`.

## Round 24

Same three seat families, new delegates: closure audits of their own
round-23 findings (R: R23-R-1/R23-R-2; S: S23-1 plus the S22-1
closure's durability under the fence; T: T23-1..T23-5) plus fresh
attack on the pass's new text — the outside-gate sample/install
placement and lock argument, the construction-bound resolution, the
boot-generation incarnation fence at the row write, the setter's
recomposition through the compose path, the scoped invariant, and the
two seed inputs.
