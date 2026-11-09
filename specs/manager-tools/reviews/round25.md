# Round 25 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tqS2gZv8bdrxmvtfl6q`), S gpt-6.1-sol-1m
(`dlg_034tqS4unnXBFV7BtBDxYo`), T deepseek-4.1-flash-background
(`dlg_034tqS6N4FuTZQpT1sz7v9`). Reviewed at `dd5c04c637` (the round-24
pass addendum, on top of `df11a1779d` the pass, on top of `84a48b191c`
the round-24 record) over the production-identical base `2be411cd8f`.
All three seats ran the required reds uncached (`-count=1 -v`, bare `|`
alternation, sources inspected first): 5/5 red families reproduced
for their documented reasons, 4/4 characterizations passing, zero
disputed, worktrees clean, owned scratch removed. R additionally
re-ran the two sampling proof tests and the compaction-replay
harness — PASS.

## Closures

- **S24-1 — CLOSED (S).** The producer/ownership contract as landed:
  README rule 6 names spec 4 as the boot-generation field's producer
  (`README.md:971-973`); the non-goal excludes only durable state
  beyond spec 4's shared store (`3-transcript-status-consistency.md:1053-1057`);
  spec 4's initialization covers every path
  (`4-question-surfacing.md:279-293`).
- **T24-1..T24-5 — CLOSED (T's own audit).** The registry contract,
  the after-gate sentence, the notification-less statement, the
  emitter lifetime, and the MISS disposition all landed at the hops
  T named; both citation slips verified correct
  (`agent/session_envelope_sampling.go:19-20`;
  `agent/session_execution.go:131-139` with the ordinal stamped at
  `agent/session_compaction.go:467-472`).
- **R24-R-1 — PARTIAL (R; S and T concur on the parts):** the entry,
  the publication, the replacement-replaces rule, the
  resolve-once-carry, and the MISS rule are named and dynamically
  proven; the two significants below leave the contract's stated
  behavior self-contradictory and one argument false.
- **R's round-22 positives:** four CONFIRMED; the emitter-lifecycle
  positive REGRESSED AS WRITTEN (the unconditional queued-delivery
  promise — R25-R-5 below).

## The round's two significant — every load-bearing claim
verified in-tree by the chair at the exact hop

### R25-R-1 — significant (three-seat: R25-R-1 = S25-1 = N25-T-4) —
the absent-generation characterization is mutually exclusive with
the identity guard the registry specifies

The pass's comparison rule — "registration identity or generation
mismatch = DROP" (`3-transcript-status-consistency.md:817-818`) —
and its absent-when-missing sentence — "both generations compare
equal-empty and the install proceeds UNFENCED ... the
replacement-window regression remains" (`:824-829`, and the journey
at `:1325-1328`) — cannot both hold: a replacement's registration
mints a NEW identity (it must, or the old runtime's release could
remove the replacement), so the identity check drops stale writes
EVEN WITH BOTH GENERATIONS EMPTY. S proved it dynamically through
the real seams (job `..._8FZEdkjsj82t`): the no-mint drive with the
strict identity comparison dropped A once and retained B (identities
1/2, generations equal-empty), and the drive's own prose-expectation
assertion failed against the spec's stated unfenced proceed — the
code the seat drove contradicts the sentence the spec ships. T
reached it statically (N25-T-4) with the added distinctions: the
registration identity's ORIGIN (where minted, where stored, what a
teardown presents) was never named, and MISS-vs-absent were left
conflatable — an implementer could drop every child-frontier install
in the absent build. R graded it significant (two mutually exclusive
rules, unbuildable as written); S and T graded it minor
(a contradictory characterization around a WORKING fence). The
chair grades it SIGNIFICANT: the implementer cannot know which rule
to build — the same class as round 9's verbatim divergence — while
recording that the mechanism itself is sound and S's fix direction
is mandatory: **keep the identity guard; correct the characterization
— do not disable a working guard to reproduce the old value-only
fence's no-mint red.**

### R25-R-2 — significant (R; chair-verified at every hop) — the
attach-before-mint bridge argument is false on restored children,
and the failed-construction release site is unnamed

The pass's interval bridge says "the mint precedes any child
execution, and a first-record event requires a completed provider
round-trip" (`:800-803`). False on restore, chair-verified: the
restore constructor attaches at `agent/session_init.go:1427`, then
runs `recoverClientMutationFailures` INSIDE the constructor
(`:1472`), which begins its own execution
(`agent/session_client_mutation_queue.go:1629`, its own comment:
"the failed start's entries form its own execution") and persists
the recovered user input through the hooked writer
(`:1682-1715`) — a real first execution entry, satisfying the
listener's predicate, emitted BEFORE the constructor returns at
`:1594`, so the cold-restore owner (`agent/delegate_restore_idle.go:161-165`)
cannot have minted or published yet. T's contrary walk repeated the
bridge sentence without checking the recovery-recording path; R's
schedule is the verified one. The fence's BEHAVIOR survives — a
constructor-window write is a CURRENT-runtime write (carried ==
current, same identity) and legitimately proceeds; every stale
shape still mismatches on identity — but the spec's stated
reasoning is false and the failed-construction release (the
discard paths after a constructor error) is a registry writer the
pass never named, violating its own writer-set rule.

## Minors

- **R25-R-3 = N25-T-3 — the release-by-identity landing zone and
  the identity's origin unnamed.** The shared seam is
  `unhookReclaimedRuntimeRecord` (`agent/delegate_tree_reclaim.go:435-443`,
  its own comment: "Both reclamation paths — capacity eviction and
  the idle release — unhook identically"), called at `:466`
  (capacity) and `:673` (idle). T adds the precedent the
  registration should ride: the EXISTING per-descendant binder
  factory, `SetDescendantTranscriptPublicationFunc`
  (`agent/session_conversation_provenance.go:217-231`, its own
  comment: "Each invocation binds fresh hooks, so a retired producer
  cannot publish into its replacement"), invoked at
  `agent/session_init.go:530`/`:1189` before attach.
- **R25-R-4 = N25-T-1 = S25-2 (three-seat, S DYNAMIC) — the
  notification-less correction claim is false for child rows.** The
  "same bounded loss ... the next mode-row facet corrects
  subscribers" analogy fails: the facet machinery is ROOT-ONLY (the
  spec's own grounding: `refreshFacets` samples only the root
  envelope), the descendant projector emits field-scoped
  notifications with the whole thread riding only `ThreadStarted`
  (`internal/appprojector/appwire_projection.go:81-183`, `:435-436`),
  and the async-attention case fires no mode row by construction. S
  drove it: a hydrated subscriber's cached frontier stayed stale
  (`subscriberHydrated=""`, `newFrontierNotifications=0`), and a
  subsequent routed status event carried no frontier. The staleness
  is potentially INDEFINITE, not bounded; the read carries the
  truth (`server/appwire_read_handlers.go:350-365`).
- **R25-R-5 = N25-T-2 = S25-3 (three-seat, S DYNAMIC) — the queued-
  refresh delivery promise is unconditional and the close join is
  not.** `joinWithinCloseBudget` gives up and drops remaining
  events (`agent/session_close.go:70-97`, "a nicety, not a safety
  property"), bounding the `sendersWG` join
  (`:521-542`). S drove it: budget expired, `closeReturnedBeforeEmitter=true`,
  `queuedRefreshDelivered=0`, warnings=2. The promise must be
  best-effort within the shared close budget, and journey 8's
  REPLACEMENT case must hold the callback after its sample (S's
  proof shape) rather than ride the close drain — a drain the
  budget can abandon would make the case vacuous.
- **N25-T-5 — the absent-when-missing "pin" referenced S's overlay
  job, not a branch red.** The eight branch tests contain no
  descendant red; the spec's pinned-cost claim must name the reds
  the spec-3 lane itself owns in its starting corpus.

## S's dynamic registry proof (the mechanism of record, jobs
`..._aTz15MPYUE3d` final combined, `..._8G9AmTX7YyyV` comparison-off,
`..._JARN28lS2yqd` consumer-off, `..._8FZEdkjsj82t` no-mint)

Through the REAL seams (registered delegate + delegate_send, real
`releaseIdleRuntimeAfterFinalize`, the actual teardown hook):
ADDRESSED green (boot=1, field = metadata = history); REPLACEMENT
green (A and B nonempty and distinct, oldBoot=1/newBoot=2,
carried=1/current=2, A dropped once, B retained; the replacement's
entry was a different pointer and identity; its publication did not
mutate the carried entry; the old registration's unregister did not
remove the replacement); mid-flight unregister MISS dropped
(`drops=1`); the publication interval clean (owner held before mint:
attachRegistered=true, generationEmpty=true, firstRecordCount=0;
after release: publishedBoot=1, firstRecordCount=1,
incompleteEntryObserved=false); after-gate overlap produced no
incoherent row (the plain write preserved the concurrent mode
commit's status and installed the frontier). Causal controls:
consumer-off → blank metadata; comparison-off with distinct entries
→ A installed, drops=0. The registry mechanism — completion,
replacement, carried-vs-current, MISS — is CLOSED dynamically.

## Disposition — one reconciliation plus three statements, one pass

1. **THE IDENTITY IS THE FENCE (R25-R-1 = S25-1 = N25-T-4).** The
   registration identity is the fence: carried-vs-current mismatch
   = DROP, unconditionally — S's no-mint drive proves it holds with
   no generation at all. The boot generation is the DURABLE
   CORROBORATION, compared when present (the wire-comparable token
   spec 4 supplies). The absent-when-missing sentence is REPLACED:
   spec 4's field not landed means the entry's generation stays
   empty and the identity check still drops every stale write —
   spec 3's lane closes the replacement window ON ITS OWN; there is
   no unfenced proceed, and the "known cost" and "pinned by the
   no-mint red" sentences are WITHDRAWN. The identity's origin named:
   minted at REGISTRATION, unique per registration, stored in the
   entry. MISS (registry present, no entry) = drop, distinguished
   from the absent build (entries present, empty generations,
   installs proceed through the identity check). The registration
   rides the EXISTING binder-factory shape
   (`agent/session_conversation_provenance.go:217-231`, invoked at
   `agent/session_init.go:530`/`:1189` — fresh bindings per
   construction, a retired producer cannot publish into its
   replacement); the factory is the registration mechanism, attach
   (`:696`/`:1427`) the timing. [AMENDED in round 26: the factory
   is the DISCIPLINE precedent, not the mechanism — the existing
   factory returns only Recorded/Published, is keyed by the
   durable session id, and runs before attach; the registration
   is additive owned work extending that factory's return with
   register/publish/release closures, per reviews/round26.md
   disposition 1.] README rule 6's consumer wording
   corrected (the fence holds without the field; the field is the
   corroboration). Journey 8's absent-build clause corrected: it
   pins the IDENTITY DROP (red if the comparison is disabled), and
   the landed build additionally asserts A and B nonzero and
   distinct. The REPLACEMENT red and the comparison-disabled red
   are named as the spec-3 lane's OWN starting-corpus items.
2. **THE CONSTRUCTOR WINDOW RESTATED (R25-R-2).** The
   provider-round-trip argument WITHDRAWN. The real rule:
   constructor-window writes — fresh or restored, INCLUDING the
   recovery-recorded first entries
   (`agent/session_init.go:1472` →
   `agent/session_client_mutation_queue.go:1629`, `:1682-1715`,
   before the return at `:1594`) — are CURRENT-runtime writes
   (carried == current, same identity) and proceed legitimately;
   every stale shape mismatches on identity, so the fence needs no
   interval argument at all. The FAILED-CONSTRUCTION release named
   as the fourth registry writer: the restore-discard path after
   `agent/delegate_restore_idle.go:161` and the spawn error path
   release the incomplete entry by its registration identity.
   [AMENDED in round 26: those named sites cannot release — the
   restore caller's error branch returns nil with no child and
   the spawn caller receives nil; the release is a
   constructor-local conditional defer on the existing
   failure-defer conditions plus the discardRestoredCandidate
   and disposeUnadoptedSubagentSession funnels, per
   reviews/round26.md disposition 2.]
3. **THE RELEASE LANDING ZONE (R25-R-3 = N25-T-3).** Release-by-
   identity lands at `unhookReclaimedRuntimeRecord`
   (`agent/delegate_tree_reclaim.go:435-443`, called at `:466`
   capacity and `:673` idle), removing only the entry whose
   identity the released runtime holds.
4. **THE NOTIFICATION-LESS COST HONEST (R25-R-4 = N25-T-1 = S25-2).**
   The bounded-loss analogy and the "next mode-row facet corrects
   subscribers" clause WITHDRAWN (the facet machinery is root-only;
   the child projector emits field-scoped notifications only).
   The honest cost: a live subscriber's cached child row keeps the
   old frontier until a full metadata read or the child's next
   `ThreadStarted` (a replacement) — potentially INDEFINITE for the
   async-attention case; the READ carries the truth
   (`server/appwire_read_handlers.go:350-365`). Stated as the known
   cost, the doctrine cited as the DEVIATION it is (the doctrine's
   carrier — the notification stream — is exactly what the install
   lacks).
5. **THE CLOSE-BUDGET QUALIFIER (R25-R-5 = N25-T-2 = S25-3).** The
   delivery and stop promises restated as best-effort WITHIN THE
   SHARED CLOSE BUDGET (`joinWithinCloseBudget` gives up and drops
   remaining events, `agent/session_close.go:70-97`); journey 8's
   REPLACEMENT case holds the callback after its sample and never
   rides the close drain.
6. The round-24 record's absent-when-missing claim (round24.md
   disposition item 1) amended in place with the correction named.

## Readiness

- Spec 1 — READY from all three seats.
- Spec 2 — READY from all three seats.
- Spec 4 — READY from all three seats.
- Spec 3 — NOT READY (R and S; T's READY-with-minors judgment
  recorded — the chair sides with R and S on the unbuildable
  contradiction).

Significant trajectory r11-r25: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1,
3, 1, 2. Both significants are residues of the same fence contract
the last two rounds have been completing — and this round's evidence
is the strongest yet: the mechanism is dynamically closed with the
registry semantics through the real seams, and every remaining
defect is a sentence the code S drove has already contradicted or
an argument my own text got wrong on the restore path. The fix is
textual reconciliation, not mechanism work.

## Round 26

Same three seat families, new delegates: closure audits of their
own round-25 findings (R: R25-R-1..R-5; S: S25-1..S-3 plus a
re-probe against the reconciled identity-fence text — the no-mint
drive now expecting the identity drop; T: N25-T-1..T-5) plus fresh
attack on the pass's new text: the identity-fence reconciliation,
the registration's factory home and identity origin, the
constructor-window restatement and the failed-construction release,
the release landing zone, the honest notification-less cost, the
close-budget qualifier, and the journey's corrected pins.
