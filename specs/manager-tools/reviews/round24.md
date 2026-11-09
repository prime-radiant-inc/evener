# Round 24 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tpDu0bHPdUYTYjWZwgj`), S gpt-6.1-sol-1m
(`dlg_034tpDvriuW36NGVwsbsob`), T deepseek-4.1-flash-background
(`dlg_034tpDxHeTlbhSKg3San2z`). Reviewed at `ca5836f987` (the round-23
revision pass, on top of `f4dad369b2` the round-23 record) over the
production-identical base `2be411cd8f`. All three seats ran the required
reds uncached (`-count=1 -v`, bare `|` alternation, sources inspected
first): 5/5 red families reproduced for their documented reasons, 4/4
characterizations passing, zero disputed, worktrees clean, owned scratch
removed. R additionally ran the three targeted proof/harness tests
(the envelope-sampling pair and the compaction-replay harness) — PASS.

## Closures

- **R23-R-2 — CLOSED (R; S and T concur).** The two-input seed
  contract is implementable at both constructors: fresh `NewSession`
  holds no `transcriptEntries` local and reaches attach at
  `agent/session_init.go:696`; restore owns the validated entries at
  `:1015-1023` and attaches at `:1427`; reopens call the hook-bearing
  `setTranscriptLocked` path without reseeding
  (`agent/session_attention.go:1827-1842`, `:1904-1914`); the attach
  seam at `agent/session.go:2867-2901` accepts the input. S added a
  production-build causal control: replacing fresh nil with
  `transcriptEntries` reproduces `undefined: transcriptEntries` at
  `:696` (job `...oX3hz20vF0ss`); the corrected form compiles.
- **T23-1..T23-5 — CLOSED (T's own audit, chair-verified in round 24's
  spot checks).** The install-point claim withdrawn with the arm's
  true position named; the amended rule cited as inapplicable with its
  drain-goroutine scope; the resolution named as construction-bound
  closures; the setter recomposes; the invariant scoped to the
  nil-`OriginalOrdinal` copy class; the two seed inputs stated.
- **All five of R's round-22 positive verifications — CONFIRMED**
  (dispatcher wiring orders incl. the setter's own `s.mu` hold
  satisfying the compose helper's caller contract
  `agent/session_execution.go:235-258`; the emitter lifecycle against
  the real shutdown ordering `agent/session_close.go:254-283`,
  `:332-343`, `:521-542`; the sampling rule's proof coverage
  unweakened by the inapplicability citation; the compaction-replay
  harness; the doors text).
- **S22-1 durability — the consumer mechanism CLOSED dynamically;
  R23-R-1 PARTIAL** (below).

## The round's one significant — THREE-SEAT convergence:
R24-R-1 = T24-1 = S24-1 — the incarnation fence has no
constructible token publication path

Every load-bearing claim was verified in-tree by the chair at the
exact hop during the round (the mint inventory by grep; the
root-derived descendant token at `appwire/boot_generation.go:19-25`,
whose own comment says "A descendant has no counter of its own"; the
mint-less cold restore at `agent/delegate_restore_idle.go:161-165`;
spec 4's post-construction contract at
`4-question-surfacing.md:279-293`; spec 3's "No new durable state"
non-goal at `3-transcript-status-consistency.md:977`; README rule 6's
lane-independence at `README.md:928-949`; the binder landing zones at
`agent/subagents.go:757-759` and
`agent/delegate_restore_idle.go:137-139`).

The three faces of one defect:

- **The binding premise is false at attach (R, S).** The constructors
  register the sampler at attach — fresh `:696`, restored `:1427` —
  and return at `:721`/`:1594`; the owner mints the child's boot
  generation only AFTER construction returns (spec 4's contract,
  `4-question-surfacing.md:279-293`; the owners sit at
  `agent/subagents.go:890` and
  `agent/delegate_restore_idle.go:161`). Capturing the token at
  registration captures an unset value; capturing the session
  pointer for a later read still supplies no Server-owned CURRENT
  value at the comparison (R), and reading a session field at the
  row write is a session read under `Server.mu` the text's own lock
  rule keeps out.
- **No per-child token exists today, and the only descendant token
  is root-derived (T).** `schema.NextBootGeneration`'s production
  callers are `cmd/evener/serve.go:780` and `:1124` only; the only
  token a descendant carries is
  `DescendantBootGeneration(s.appBootGeneration, s.appThreadID)` =
  root generation + "@" + root session id
  (`server/appwire_histories.go:143-168`,
  `appwire/boot_generation.go:19-25`) — IDENTICAL across an old
  runtime and its cold-restored replacement, so the mismatch that is
  the entire fence never happens as built.
- **The ownership and ordering are unowned (S, T).** Spec 3's fence
  needs the per-child fsynced counter while spec 3's own non-goal
  still says "No new durable state" (`:977`); README rule 6 promises
  no lane blocks another and every shared field has exactly one
  producing spec, but the boot-generation field's producer is not
  named in the ownership list and the fence's absent-when-missing
  behavior is unstated; the attach-registration-to-post-mint
  coordination is unspecified.

**S's dynamic work is the mechanism's vindication and the fix's
proof of concept.** With an explicit per-child mint supplied before
registration in fresh overlays, EVERYTHING is green under
`-race -p 2` (job `...DvIHIvqKPY0G`, `seat-r24-s-final-minted-green`;
final combined run job `..._itNyprRgQ2S8` line included):

- ADDRESSED-CHILD: generation=1, metadata == field == full-history.
- IDLE RESTORED-CHILD: the SessionStart constructor bootstrap matches.
- REPLACEMENT through the REAL seams — constructible, not a unit
  interleaving: the harness spawned a real child through the
  registered delegate tool, held the old first-record callback AFTER
  its sample but BEFORE its row write, finished the child, used
  `releaseIdleRuntimeAfterFinalize` with the real
  `idleTeardownMemberStarted` hook after the actual unhook
  (`agent/delegate_tree_reclaim.go:670-679`), cold-restored the SAME
  child id through registered `delegate_send`, let the replacement's
  synchronous restored SessionStart install frontier B, then released
  the old callback — oldBoot=1, newBoot=2, A DROPPED exactly once,
  the row stays B (drops=1).
- ROOT control: BridgeEvent after WireTranscriptHistory green (the
  dispatcher survives the setter path, including the replacement
  rewire at `cmd/evener/serve.go:1152`).

Three causal controls, each isolating one component: consumer
disabled → blank metadata (job `...bDL8XaKsF226`); token comparison
disabled with distinct tokens retained → A overwrites B, drops=0
(job `...WQ6tkNP9AZKM`); child mint omitted → both tokens zero, A
overwrites B, drops=0 (job `...MRWh6sWWW0PQ`) — the last being the
spec-3-alone counterfactual, not a refutation of the fully
initialized design. S's own grading is MINOR on the ground that the
initialized mechanism passed; the chair grades the converged finding
SIGNIFICANT because the spec AS WRITTEN ships a fence whose token
cannot exist where the spec binds it, against the spec's own
non-goals — the same defect class as R23-R-1, which it leaves
PARTIAL, not closed.

## Minors

- **T24-2 — the outside-gate walk still samples under the gate as
  described.** The route IS the `CommitProjection` closure
  (`server/appwire_record_events.go:386`, closure to `:521`), and
  `CommitProjection` holds `projectionMu` across it
  (`internal/appserver/server.go:174-181`); the text releases only
  `Server.mu`, so its own "nothing under the gate" claim is one
  sentence short: the first-record processing must run AFTER the
  `CommitProjection` call returns, the row re-read under the
  re-acquired `s.mu`. (S's implementation put the fenced write
  entirely outside and it passed — the shape works; the text must
  say how the route exits the closure.)
- **T24-3 — the install is notification-less and the pass does not
  own it.** "No commit arm" means no sequenced notification for the
  row change; a live subscriber's cached child row keeps its
  hydrated frontier until the next event. The daemon's own doctrine
  says bounded losses are invisible because "the notification stream
  carries the truth" (`server/thread_envelope.go:31-40`); the read
  still carries the frontier (`server/appwire_read_handlers.go:350-364`),
  so this is a coherence statement to own, not a broken read.
- **T24-4 — the emitter goroutine's lifetime is undated in both
  directions.** "Starts at dispatcher install" read literally starts
  one goroutine per install (the compose path re-runs on every
  setter call and attach); and "stops at session close" never says
  whether a queued refresh drains across close — the only way the
  REPLACEMENT case's delayed delivery happens. R's positive 2
  verified the real boundary (`closeEventStream` joins `sendersWG`
  then closes, `agent/session_close.go:521-542`), so the statement
  has a home.
- **T24-5 — the sampler-lookup MISS disposition is unstated** (not
  reachable in production today — the binder rides the same
  propagation as `spawn.descendantEvent`, so a miss implies a wiring
  bug). Folded into the significant's registry contract.
- **Two citation slips (T):** the drain-goroutine scope line is
  `agent/session_envelope_sampling.go:19-20` (":15-18" carries the
  interface's purpose); the PREDICATE lives at
  `agent/session_execution.go:131-139` — `agent/session_compaction.go:467-472`
  is where the copies' `OriginalOrdinal` is STAMPED.

## Disposition — one significant fix family plus three statements,
one pass

1. **THE TOKEN PUBLICATION CONTRACT (R24-R-1 = T24-1 = S24-1,
   subsuming T24-5).** The registry becomes a Server-owned map of
   immutable entries `{SessionID, boot generation, sampler,
   registration identity}`: attach registers the sampler (the
   entry's generation empty); the owner's post-construction mint —
   spec 4's initialization, its child landing zones named (the spawn
   constructor's post-return path, `agent/subagents.go:890`; the
   cold restore, `agent/delegate_restore_idle.go:161-165`) —
   PUBLISHES the minted generation into the current entry BEFORE any
   child work (the attach-before-mint interval is bridged by
   ordering: the mint precedes any child execution, and a
   first-record event requires a completed provider round-trip).
   The route resolves the event's entry ONCE at the start of its
   processing and CARRIES it across the sample (this is what S's
   proof drove; it also answers T's self-comparison argument — the
   comparison is carried-vs-current, never current-vs-current); the
   row write compares the carried entry against the registry's
   CURRENT entry under `Server.mu` — identity or generation
   mismatch = DROP. Release removes an entry by its registration
   identity only (the old runtime's teardown cannot unregister its
   replacement). Lookup MISS = drop, install nothing. Absent-when-
   missing (README rule 2/6): spec 4's field not landed means both
   tokens compare equal-empty and the install proceeds UNFENCED —
   the replacement-window regression remains until the field lands,
   stated as the known cost, pinned by S's no-mint red as the
   absent-when-missing characterization. [AMENDED in round 25:
   this pass's own identity guard contradicts that sentence —
   a replacement's registration identity differs even with both
   generations empty, and S's strict-form drive dropped the
   stale write with no mint at all. The identity fence carries
   the replacement window on spec 3's lane alone; there is no
   unfenced proceed. See reviews/round25.md.] Spec 3's non-goal is
   scoped: no durable state BEYOND the shared session
   boot-generation store spec 4's design owns (the fence reads it;
   it adds no store). README rule 6's ownership list names the
   session boot-generation field's producer (spec 4) and spec 3 as
   its consumer here. Journey 8's REPLACEMENT case asserts A and B
   NONZERO and DISTINCT (R), in a build where spec 4's field has
   landed.
2. **THE AFTER-GATE SENTENCE (T24-2).** The route's first-record
   processing runs AFTER the `CommitProjection` call returns — the
   mode arms commit inside the closure; the descendant arm completes
   the call first, then samples and installs outside it, re-reading
   the row under the re-acquired `s.mu`.
3. **THE NOTIFICATION-LESS COHERENCE (T24-3).** The install is
   deliberately notification-less; the loss is bounded exactly as
   the facet machinery's own overlapping-refresh loss is
   (`server/thread_envelope.go:31-40`): the metadata read always
   carries the truth, and the next mode-row facet corrects
   subscribers. Owned in the consumer block.
4. **THE EMITTER LIFETIME (T24-4).** ONE emitter goroutine starts at
   the FIRST dispatcher install (installs are idempotent for the
   goroutine; setter calls and reattaches do not start more) and
   stops at session close by joining the same boundary the history
   drain joins (`closeEventStream`'s `sendersWG`,
   `agent/session_close.go:521-542`); a queued refresh delivers
   before the close completes — the drain that journey 8's delayed
   delivery rides.
5. **The two citation slips** fixed in place.

## Readiness

- Spec 1 — READY from all three seats (R: fifteenth consecutive).
- Spec 2 — READY from all three seats.
- Spec 4 — READY from all three seats (its own boot contract is
  untouched; the open defect is spec 3's consumer side).
- Spec 3 — NOT READY: the publication contract above.

Significant trajectory r11-r24: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1, 3,
1. The count fell 3→1 and the one is the SAME mechanism's residue:
each pass completes the contract, the seats find the next hop, and
this round's hop is fully proven dynamically — S's overlay IS the
working implementation. The replacement schedule is no longer
hypothetical: S drove it through the real reclamation seams.

## Round 25

Same three seat families, new delegates: closure audits of their own
round-24 findings (R: R24-R-1; S: S24-1 plus a re-probe against the
publication contract — re-run the replacement drive asserting the
registry shape and the nonzero-distinct tokens; T: T24-1..T24-5) plus
fresh attack on the pass's new text — the registry entry shape and
its publication/removal/MISS rules, the carried-vs-current comparison,
the absent-when-missing honesty, the after-gate sentence, the
notification-less coherence, the emitter lifetime, and the two
citation slips' landing.
