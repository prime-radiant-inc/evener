# Fence spike: the relay fence designed from failing tests

Produced by the round-7 escalation (reviews/round7.md disposition 1): the
relay fence, ask-identity carry, and sufficiency-relay machinery left spec 4
and were re-designed from runnable evidence against the real daemon seams,
red-first, instead of a fourth paper revision. Everything below is grounded in
tests that exist in this branch and were run against today's code; each cites
its file. The implementer lane inherits the red list in the last section.

Status: landed and chief-verified. The spike lane (commit `ec449751a6`,
ported here as `4082ff949c`) produced this record; the chief re-ran every
red and characterization in the spike's worktree before porting — the five
reproduced reds fail for exactly the stated reasons, the four
characterizations pass today as pinned — so the evidence below is
independently confirmed, not taken on the lane's word. The reds fail
against today's code by design; they are the implementer lane's starting
corpus.

Round 8 (reviews/round8.md) re-ran the reds from three independent review
seats — every red reproduced for exactly the documented reason, zero
disputed, and the worktree verified byte-identical to the review base —
and its findings amended the DESIGN half of this record in place: the
capture placement (§1), the clear-site inventory (§2), the restore
evidence (§3), the prepare's scope (§4) and the carrier wiring (§7)
carry the round-8 corrections; spec 4's scopes 2 and 5 are the contract
of record. The evidence table is unchanged — the reds and
characterizations stand as run.

Round 9 (reviews/round9.md) re-audited closure with the same three
seats — 25 of 28 round-8 closures verified CLOSED, all five reds
reproducing exactly — and amended the design half again in place: the
capture representation (§1, `(calls, legacyCount)` and the
replacement-record capture list with its dominance rule), the
clear-site inventory (§2, all five sites whole-set today and the sixth
unfenced clear named), the restore evidence (§3, the fourth writer
`recordClientMutationFailure` and the steer writers' live sampling
replaced), and the §4 heading. Spec 4's scopes 2 and 5 remain the
contract of record.

Round 10 (reviews/round10.md) re-audited closure a second time — 23
of 26 round-9 closures verified CLOSED, all five reds reproducing
exactly — and amended two points in place: the steer writers' rule
split by field kind (§3 — evidence from the capture, links from the
capture minus the active execution, unfenced and legacy behavior
preserved exactly), and the capture's count half riding the resolving
turn (§3 — both halves on one durable carrier). Spec 4's scopes 2 and
5 remain the contract of record.

Round 11 (reviews/round11.md) re-audited closure a third time —
every round-10 fence finding CLOSED across all three seats, all
five reds reproducing exactly, zero new fence defects — and
corrected this record's own §3 citations in place: the steer
writers are `claimSteeringCarrierInput` and `consumeSteeringMessage`
(the names the record previously cited do not exist), and the link
split's empty inline edge is now stated with its suppression
consequence. The fence is converged at the design level; the
remaining round-11 findings sit in specs 2, 3 and 4's read and
visibility surfaces, none in the fence. Spec 4's scopes 2 and 5
remain the contract of record.

Round 12 (reviews/round12.md) re-audited a fourth time — the
convergence held: zero new fence defects across all three seats,
every round-11 fence-adjacent closure verified, all five reds
reproducing exactly. The record's §6 chain enumeration gains the
round-12 design additions spec 4 owns: the set's call-less count
(`PendingAskState.CalllessCount`, minted inside the atomic
accessor beside the pair) and the ask-pageable capability fact
(`AttentionEntry`'s additive bit, derived at the derivation site
from the `LiveEntry` capabilities it reads) both ride the same
chain the pair does, beside the composite. The fence's settled
core is untouched by both additions. Spec 4's scopes 2 and 5
remain the contract of record.

Round 13 (reviews/round13.md) re-audited a fifth time — the
convergence held again: zero fence findings in either direction
across all three seats, all five reds reproducing exactly, all
four characterizations passing, and the seat that attacked the
pageability bit's change-detection interaction came back clean
(the pair, not the bit, sits in the comparison keys). The
record's §6 chain enumeration gains the round-13 clamp metadata
(`PendingAskState.CompositeTotal` and `CallsTruncated`, the
total landing on `AttentionEntry` in a declared field); the
fence's settled core is untouched. Spec 4's scopes 2 and 5
remain the contract of record.

Round 14 (reviews/round14.md) re-audited a sixth time — the
convergence held: zero fence findings from any seat across all
three, all five reds reproducing exactly, all four
characterizations passing. The round's findings sat in the read
and visibility surfaces (the fetch's daemon hop, the roster
segment's cardinality, the frame's composite-list carrier and
its broken comparison, the goal journey's barrier, the
compaction first-persist path); the fence's settled core is
untouched. The §6 chain's clamp metadata gains the round-14
mint-site split: `CompositeTotal` stays minted inside the
atomic accessor with the pair, while `CallsTruncated` — the
ROW's fact about the clamped slice — stamps at the wire
projection where the clamp lands. Spec 4's scopes 2 and 5
remain the contract of record.

Round 15 (reviews/round15.md) re-audited a seventh time — the
convergence held: zero fence findings from any seat across all
three, every red reproducing exactly, all characterizations
passing. The round's seven significants sat in the read and
visibility surfaces again (the roster segment's coalescing
merger and recovery loop, the frame payload's whitelist
carrier, the remote fetch cursor's two-hub round trip and
translated encoding, the first-record signal's resolution
writers and one-shot failure path); the fence's settled core
is untouched. The §6 chain enumeration gains its real last
hop: the frame payload leg is the seven-field `rosterChange`
whitelist (agent/job_watch_hub.go:213-224) — an
`AttentionEntry` fact reaches the `needs_you` frame's roster
payload row only by joining that struct and its positional
literal, so the chain reads row projection → probe row copy →
`LiveEntry` → `AttentionEntry` → `rosterChange` → the frame's
payload row. Spec 4's scopes 2 and 5 remain the contract of
record.

Round 16 (reviews/round16.md) re-audited an eighth time — the
convergence held: zero fence findings from any seat, every red
reproducing for its documented reason, all characterizations
passing. The round's four significants sat one hop further into
the same read and visibility surfaces (the owed notification's
recorded-with-error face at the synced door's own contract, the
truncation recovery line landed on a constant that begins with
the resync marker the merger treats as replacement, the
recovery's reach at fleet scale, and the whitelist's missing
identity fields); the fence's settled core is untouched. The §6
chain's whitelist hop above gains the two identity fields, and
the fence itself is unchanged by this round. Spec 4's scopes 2
and 5 remain the contract of record.

Round 17 (reviews/round17.md) re-audited a ninth time — the
convergence held: zero fence findings from any seat, every red
reproducing for its documented reason, all characterizations
passing, and all three seats re-running the mechanical
comparison sweep themselves (one compile-verified) with the
twelve contexts holding. The round's two significants sat one
hop past the round-16 fixes in the same signal surface (the
owed obligation's discharge at the wrapper and stabilization
writers, and the synchronous rows' durability premise under
the third outcome); the fence's settled core is untouched and
the fence is unchanged by this round. Spec 4's scopes 2 and 5
remain the contract of record.

Round 18 (reviews/round18.md) re-audited a tenth time — the
convergence held: zero fence findings from any seat, every red
reproducing for its documented reason, all characterizations
passing. The round's findings sit entirely in the signal and
frontier surfaces (the discharge's door coverage and the
frontier's recorded semantics); the fence is untouched by this
round and unchanged. Spec 4's scopes 2 and 5 remain the
contract of record.

Round 19 (reviews/round19.md) re-audited an eleventh time — the
convergence held: zero fence findings from any seat, every red
reproducing for its documented reason, all characterizations
passing. The round's findings sit entirely in the signal
chapter's durability gating (now withdrawn in favor of
emit-at-record); the fence is untouched and unchanged. Spec 4's
scopes 2 and 5 remain the contract of record.

Round 20 (reviews/round20.md) re-audited a twelfth time — the
convergence held: zero fence findings from any seat, every red
reproducing for its documented reason, all characterizations
passing. The round's findings sit entirely in the signal
chapter's emission placement (the commit-boundary relocation);
the fence is untouched and unchanged. Spec 4's scopes 2 and 5
remain the contract of record.

Round 21 (reviews/round21.md) re-audited a thirteenth time — the
convergence held: zero fence findings from any seat, every red
reproducing for its documented reason, all characterizations
passing. The round's findings sit entirely in the signal
chapter's predicate, seed, dispatcher and doors text; the fence
is untouched and unchanged. Spec 4's scopes 2 and 5 remain the
contract of record.

Round 22 (reviews/round22.md) re-audited a fourteenth time —
the convergence held: zero fence findings from any seat,
every red reproducing for its documented reason, all
characterizations passing. The round's one significant and
four minor findings sit entirely in the signal chapter's
descendant route and its localizations; the fence is
untouched and unchanged. Spec 4's scopes 2 and 5 remain the
contract of record.

Round 23 (reviews/round23.md) re-audited a fifteenth time —
zero fence findings from any seat, every red reproducing for
its documented reason, all characterizations passing. The
round's three significants sit in the signal chapter's
descendant consumer and dispatcher composition; the fence is
untouched and unchanged. Spec 4's scopes 2 and 5 remain the
contract of record.

Round 24 (reviews/round24.md) re-audited a sixteenth time —
zero fence findings from any seat, every red reproducing for
its documented reason, all characterizations passing. The
round's one significant sits in spec 3's descendant incarnation
fence, not this spike's ask machinery. Spec 4's scopes 2 and 5
remain the contract of record.

Round 25 (reviews/round25.md) re-audited a seventeenth time —
zero ask-fence findings from any seat, every red reproducing
for its documented reason, all characterizations passing. The
round's two significants sit in spec 3's descendant incarnation
registry and its stated characterization, not this spike's ask
machinery. Spec 4's scopes 2 and 5 remain the contract of
record.

Round 26 (reviews/round26.md) re-audited an eighteenth time —
zero ask-fence findings from any seat, every red reproducing
for its documented reason, all characterizations passing. The
round's two significants sit in spec 3's registration
transport and failed-construction writer set, not this
spike's ask machinery. Spec 4's scopes 2 and 5 remain the
contract of record.

Round 27 (reviews/round27.md) re-audited a nineteenth time —
zero ask-fence findings from any seat, every red reproducing
for its documented reason, all characterizations passing. The
round's one significant sits in spec 3's registry release
writer set (the adopted-runtime teardown), not this spike's
ask machinery. Spec 4's scopes 2 and 5 remain the contract of
record.

Round 28 (reviews/round28.md) re-audited a twentieth time —
zero ask-fence findings from any seat, every red reproducing
for its documented reason, all characterizations passing. The
round was CLEAN across the whole spec set — zero significant
findings of any kind, the first such round — and its one timing
qualification sits in spec 3's callback wording, not this
spike's ask machinery. Spec 4's scopes 2 and 5 remain the
contract of record.

## Method

One test per round-7 failure mode, written through existing APIs at the
seam round 7 named, run against TODAY's code (the fence does not exist). Where
the promised protection cannot be expressed because the pair parameters or
the comparison primitive do not exist yet, the test pins TODAY's violating
behavior at the closest observable level and is recorded as red-pending-
machinery with the exact seam a real red needs. Determinism throughout
(testing.md): no sleeps, no races, no phrasing assertions — interleavings are
placed by construction (the two-sample mispair stub), by primitive order (the
check-then-clear reds), or by fixture transcripts (the restore reds).

## Evidence: the eight failure modes

| # | Round-7 finding | Test | Result today |
|---|---|---|---|
| 1 | Publication sampling (Q1 excerpt + Q2 revision) | `server/thread_envelope_ask_mispair_test.go` `TestThreadEnvelopeAskFacetSamplesOneSet` | **RED, reproduced** |
| 2 | Append-between-check-and-clear (R1) | `agent/session_ask_fence_clear_test.go` `TestAskFenceSteerClearSiteComparisonGap`, `TestAskFenceQueueClearSiteComparisonGap` | **RED, reproduced** (both sites) |
| 3 | Mismatch-after-durable-append restores as resolved (S6) | `agent/session_ask_fence_restore_test.go` `TestAskFenceRefusedAnswerMustNotResolveAcrossRestore` | **RED, reproduced** |
| 4 | Empty-set branch undefined (T2) | `agent/session_ask_fence_empty_set_test.go` `TestAskFenceEmptySetArrivalAdmitsUnbranched` | characterized (red pending machinery) |
| 5 | Steer journal carry is the wrong record shape (T3) | `agent/session_client_mutation_fence_carry_test.go` `TestClientMutationSteerCarriesThroughTheJournalRecordNotTheQueueEntry` | characterized (red pending machinery) |
| 6 | Gate coverage: ordinary-text queue/steer never reads the row (S8) | `cmd/evener-hub/appwire_skill_input_gate_test.go` `TestSkillInputGateReadsTheRowOnlyForSkillBearingInput` | characterized (the demanded pin) |
| 7 | Late rejection has no path back (S7) | `agent/session_client_mutation_fence_receipt_test.go` `TestClientMutationQueueReceiptIsTerminalAppliedEvidence` | characterized (red pending machinery) |
| 8 | Composite identity missing (R2=S9) | `cmd/evener-hub/internal/hubcore/prober_ask_identity_test.go` `TestProbeAndRosterRowCarryAskIdentityForTheExactTurnFetch` | **RED, reproduced** |

Supporting pin: `agent/session_ask_fence_restore_test.go`
`TestAskRestoreKeepsAsksPostedAfterADurableAnswer` — passes today, proving
restore's boundary scan is already recency-scoped (an ask posted after a
durable answer survives restore, with its call identity). This is the restore
half of the scoped-clear design below and the anti-regression for it.

Representative failure/output lines, one per reproduced mode:

1. `published row mispairs the ask facet: count-one excerpt with 2 calls at revision 2 — the excerpt was sampled before the append and the state after; one atomic snapshot must publish one set`
2. `stale answer cleared the pending set through the comparison-to-clear gap: 0 questions survive, want 2 (the refused answer may clear nothing)` (both sites; the queue-site red also pins that the sampled names were a strict subset of what the clear removed)
3. `restore resolved an ask whose answer the fence refused: a durable user input must not resolve an unadmitted answer`
8. `the roster row the needs_you frame derives from carries no ask identity: ask turn id "ask-turn-3f9c17d2" and call id "ask-call-77c104" are both absent from the row the probe built (excerpt count 1), so the frame cannot address the exact-turn fetch`

No round-7 finding was found unreachable; every mode reproduced or
characterized exactly as recorded. Round 7's evidence-corrected T3 fact is
re-confirmed by test 5: steers ARE durably journaled — through
`clientMutationRecord`, never through a `clientMutationQueueEntry`, which
steers do not create at all.

## What the reds prove, jointly

Three structural facts, each now runnable, drive the whole design:

- **Any comparison separated from the clear is defeatable** (reds 2). The
  spec's "comparing immediately before clearAskPending" is two lock
  acquisitions; the model goroutine appends between them and the clear wipes
  the append. This is true of the steer site (check → clear), the queue site
  (sample → durable append → clear), and — decisively — of an admission-time
  comparison too: a queue answer accepted against pair (G, N) waits for the
  turn boundary while the live turn can still append, so the window between
  an admission check and the consumption clear is even longer. A fence that
  compares anywhere and clears later loses.
- **Any refusal after the durable answer is unenforceable** (red 3). Restore
  resolves any durable `TurnUserInput` (`turnResolvesAskBoundary`), so a
  clear-site refusal cannot deliver "never admitted as an answer" across a
  crash. The only refusal that holds is one that happens BEFORE the answer is
  durable.
- **The answer already records what it resolves — under one lock** (red 2,
  queue site). `turn.AnswerToTurnIDs = s.pendingAskTurnIDs()` samples the
  resolved asks in a single `s.mu` acquisition at admission
  (agent/session_lifecycle.go:2331), and the durable answer carries it. What
  the sample names is exactly what the answer legitimately resolves; what the
  clear removes today is the whole set. The names are the scoping mechanism
  the fence needs — captured atomically with the pair, not a comparison
  re-run at clear time.

And two shape facts:

- **The one carrier both mutation modes journal is the record**
  (characterization 5): `clientMutationRecord` is the durable, crash-restart
  carrier for queue AND steer, it already has the precondition idiom
  (`clientMutationPreconditions`, the `ExpectedQueueRevision` admission-check
  pattern), and the queue entry has no precondition field. A pair placed on
  the queue entry fences nothing.
- **The identity the fetch needs is already on the row and dropped by the
  probe** (red 8): `EvenerThread.PendingAsk` carries the full per-call
  `(TurnID, CallID)`; `ProbeResultFromThread` keeps only the flag and the
  text. The frame cannot address `{turn: askTurnId}` — and a payload that
  carried only the call id could not either, which is why the composite must
  travel end to end.

## The redesigned fence

### 1. The comparison moves inside the admission transaction, before anything is durable

All three input modes already share one daemon-side admission transaction:
`acceptClientMutation` (agent/session_client_mutation.go) — start at
`:399`, queue at `session_client_mutation_queue.go:122`, steer at `:758`.
The fence lands there:

- The pair rides the mutation params — `TurnStartParams`,
  `TurnQueueParams`, `TurnSteerParams` — as two additive fields
  (`expectedAskRevision`, `expectedAskBootGeneration`; the
  `ExpectedQueueRevision` idiom, appwire/types_turn.go:886).
- At the top of `acceptClientMutation`, BEFORE the store transaction
  begins, a new session accessor captures, in ONE `s.mu` hold: the
  current pair (boot generation, revision) AND the pending set's exact
  calls (the `PendingAskCall` (TurnID, CallID) list), and passes the
  capture in. The comparison inside the transaction reads the passed-in
  capture, never live state; the serializer never waits on `s.mu`, so
  the hold must complete before the store transaction — a capture taken
  inside it could deadlock, and one released early would be a sample,
  not a capture.
- Compare: params pair vs captured pair. Mismatch — including an empty
  pending set (the pair names a set that no longer exists) — is a typed
  refusal recorded through the existing rejection idiom
  (`rejectClientMutation`; pinned durable-and-replayed by
  `TestClientMutation_QueueRejectionIsRecordedDurably`). **The receipt IS
  the refusal**: acceptance returns it synchronously; no late-rejection
  channel, watch event, or re-read trigger is built (red 7's terminality pin
  flips to assert the refusal receipt).
- On match, the capture is recorded on the journal
  record — `clientMutationRecord.Preconditions` extended (the carry both
  modes share; characterization 5's evidence) — and only then does applied
  state commit. The capture's representation is `(calls, legacyCount)`:
  the set's accessor skips questions without both ids, so a call-less
  legacy question present at admission is COUNTED, not named — the
  scoped clear removes the named calls AND that many call-less
  questions, and the durable evidence carries the same pair (§3). A
  replacement record (drain/promote) carries a LIST of captures, one
  per replaced record, under the dominance rule: all-fenced sources →
  the union of their captured sets, scoped clear; ANY unfenced source
  → whole-set clear, today's semantics — the fence never weakens an
  unfenced input's contract. A refused answer never becomes a durable
  user turn, so
  red 3's state is unreachable and "never admitted as an answer" holds
  across crashes by construction.

### 2. The consumption clear is scoped to the captured names — no comparison at the clear

Because an admission-time comparison alone still leaves the
admission-to-consumption window open (the red-2 lesson applied one level
earlier), the clear performs NO pair comparison at all. The consumption path
clears exactly the calls the admission captured and recorded:

- New primitive in agent/session_tools_ask.go: remove the named calls from
  the pending set in one `s.mu` acquisition, bumping the revision
  (scoped sibling of `clearAskPending`) — plus a `...Locked` sibling
  for call sites that already hold `s.mu`: the steering-carrier entry
  site clears under the lock it takes at
  `session_lifecycle.go:1543` (it calls `setAskPendingLocked` there
  today), so the scoped primitive must be callable both ways,
  mirroring the existing clear's own locked/unlocked pair; calling
  the locking variant at that site deadlocks (round 9's T10).
- EVERY answer-reachable clear site uses it for fenced records (records
  carrying the pair): `processOneInput`'s clear
  (session_lifecycle.go:2344), `clearAskPendingForResolvingSteer`
  (session_tools_ask.go:231-235), the steering-carrier entry clear
  (session_lifecycle.go:1530-1575), the already-incorporated replay
  branch (:2218-2222) and the recovered pre-append-failure branch
  (:2303-2313) — round 8's inventory, corrected by round 9: today ALL
  FIVE sites wipe the whole set (the record's earlier "all but the
  first two" was wrong). Each reads the consumed record's captured names instead
  of `pendingAskTurnIDs()` at consumption time. An ask appended after
  admission is not in the captured list and survives — deterministically,
  with no window at all. THREE classes keep the whole-set clear, none
  fenceable: the accepted-interrupt branch (:864) and the close branch
  (:873) — user actions, not answer paths — and the direct-user-input
  branch (:2352), a sixth answer-reachable whole-set clear today: a
  user input that arrives through no client-mutation record has no
  journal record to carry a pair, so no fence can bind it (round 9's
  named exception class).
- Unfenced records (omission = today's legacy behavior, per spec 4's
  compatibility rule) keep the whole-set clear.

### 3. Restore resolves exactly what the CAPTURED evidence names

Round 8 broke the shape this section originally recorded on two counts,
both verified at the exact hop: `AnswerToTurnIDs` names EXECUTIONS, not
calls ("names the pending question executions", agent/schema/turn.go:429-431;
every ask in one turn stamps the same id, agent/session_tools_ask.go:156-159),
and it is SAMPLED at consumption (`turn.AnswerToTurnIDs =
s.pendingAskTurnIDs()`, session_lifecycle.go:2331), not at admission —
so two same-turn asks are indistinguishable on the wire, and an ask
appended between admission and consumption lands in the durable names
but not in the captured clear list, putting live and restore in
disagreement on exactly the append case the fence exists for.

- The redesign: the admission-time captured composites are the SOLE
  resolution evidence, carried durably from the one capture. The journal
  record's preconditions carry them for the scoped clear (§1), and a new
  additive `schema.Turn` field names the resolved calls on the resolving
  turn for restore — written by the consumption path FROM the record's
  captured list, never re-sampled from live state. Round 9 completed the
  writer inventory: the two failure
  boundary writers (`TurnFailure` with `SteeringCarrier`,
  agent/session_events.go:276-292, agent/session_queue.go:1141-1214)
  write the same field from their records; the recovered
  pre-append-failure writer — `recordClientMutationFailure` persists its
  OWN resolving `TurnUserInput` directly
  (agent/session_client_mutation_queue.go:1694-1715, written before
  the normal consumption assignment), the fourth writer, which the round-8
  inventory missed — copies the capture from its journal record onto
  the recovered user input; and the STEER writers, which today SAMPLE
  the pending set live at carrier claim (`claimSteeringCarrierInput`,
  agent/session_client_mutation_queue.go:379-418) and again at
  delivery (`consumeSteeringMessage`, agent/session_queue.go:959-1019),
  unioning the sample into the durable turn. Round 10 split their
  rule by the two fields' KINDS: the captured-calls field —
  AUTHORITATIVE resolution EVIDENCE — is set from the record's
  captured list, never live state, and may name the active
  execution's calls; `AnswerToTurnIDs` — a LINK field — keeps the
  existing active-execution exclusion in EVERY regime (today's
  `consumeSteeringMessage` strips the active execution from its live
  sample; the continuation validator rejects non-earlier edges,
  `internal/transcriptindex/answer_continuation.go:103-117`), so a
  FENCED resolving turn writes its links from the capture MINUS the
  active execution, and UNFENCED or legacy records keep today's
  behavior exactly — live names recorded at carrier claim
  (`claimSteeringCarrierInput`, agent/session_client_mutation_queue.go:401-419),
  live-and-recorded union at delivery, active execution stripped —
  preserving the runnable answer-link contracts
  (agent/session_ask_answer_link_test.go:203-295).
  `job_watch_hub.go:420`'s suppression key keeps working on the link
  field; the split's EMPTY edge is named — a fenced INLINE
  self-resolving steer's capture minus the active execution is
  empty, its link list empty, exactly today's unfenced inline strip
  result, and the suppression key correctly does not fire (no
  communicate delivery fires for the self-edge either); spec 4's
  journey 6 pins it as the regression case. Turns predating the
  fields keep whatever they carry,
  named legacy.
- The capture's `(calls, legacyCount)` representation subtracts the
  same way at restore — from the resolving turn's additive field,
  which carries BOTH halves of the consumed capture (the named
  calls AND the count, one durable carrier; round 10's addition —
  the count is evidence on the turn, not only on the journal
  record): the named calls AND that many call-less
  questions resolve; an all-zero capture against a nonempty set is
  unreachable in a well-formed admission (the capture is
  exhaustive) and remains the typed malformed-state guard, never a
  silent whole-set clear; the mixed
  case resolves naturally.
- `turnResolvesAskBoundary` / `deriveRestoredAskPending`
  (agent/session_tools_ask.go:597-612, :875-950): a resolving turn (or
  failure boundary) subtracts exactly the captured names it carries
  from earlier ask rounds and continues. The shortcut boundaries
  (assistant-final, generic completion, round-resolves-everything)
  fire only when the captured names are EXHAUSTED — a round that found
  asks but resolved none keeps scanning, preserving the existing
  accumulate-across-rounds behavior. Turns and records predating the
  field (legacy) keep today's resolve-everything semantics.
- With this, live and restore agree on the scoped semantics — both read
  the one capture — and red 3's promised state holds across
  crash-restart.

### 4. One hub-side prepare that all three modes route through, its row read scoped to pair-bearing requests

Red 6's pin: today only `turn/start` reads the row
(`hubRelay.startTurn` → `prepareRelay`, cmd/evener-hub/app_relay.go:71-101);
queue and steer call `ensureSkillInputSupported`, which returns before any
row read for ordinary text (appwire_validation.go:55-70) — precisely the
modes the append window is answerable through. The fix is not a gate next to
the skill gate; it is one prepare step every mode traverses:

- The `AskFence` `ThreadCapabilities` bit advertises daemon-side fence
  support. A fence-bearing relay (params carry the pair) is refused
  pre-dispatch with the typed `ask-fence-unsupported-on-daemon` error when
  the serving daemon lacks the bit — checked in ONE prepare step that all
  three mode entry points run (three call sites sharing one function),
  with its ROW READ scoped to PAIR-BEARING requests only: an ordinary
  text input never pays a row read. Landing: extend the Queue and Steer
  handlers
  (cmd/evener-hub/session_input.go:133-180) to run the same
  `prepareRelay`-shaped read `startTurn` already runs (app_relay.go), or
  hoist one `prepareInputMode` into `relayOwnedMutation` /
  `withSessionResume` so all three modes share it. `RemoteHubSource`'s
  capability rebuild forwards the bit (spec 2's named landing zone).
- Removal condition: every supported daemon advertises the bit. The
  characterization keeps its ordinary-text arm (ordinary input still
  reads no row) and GAINS the arm the fence needs: a pair-bearing
  text-only queue/steer reads the row through the common prepare.

### 5. One atomic snapshot for the row

- New locked session accessor in agent/session_tools_ask.go returning the
  excerpt text, count, flags and the identity pair together;
  `refreshFacets`' ask facet calls it ONCE, replacing the two-call sampling
  at server/thread_envelope.go:420-426. Red 1 turns green; `assign`'s
  unconditional excerpt install (thread_envelope.go:568-573) becomes safe
  because the pair it installs is internally coherent by construction.

### 6. The composite identity travels end to end

- `PendingAskCall`'s `(TurnID, CallID)` — both ids, never the call id alone —
  lands on `ProbeResultFromThread` (cmd/evener-hub/internal/hubcore/
  prober.go:165-197), `LiveEntryThreadFacts` (:206+), `AttentionEntry`
  (appwire/attention.go), and the `needs_you` frame's roster payload
  row — whose builder is a WHITELIST: a seven-field `rosterChange`
  struct filled by a positional literal (agent/job_watch_hub.go:213-224,
  flags-only today), so the ids, the question text, the count, the
  pair and the entry's own identity fields — `RosterInstanceID`
  (appwire/attention.go:12-14), the journey-6 clear token, and
  `ID` (:15), the fetch addressing — reach a row ONLY by
  joining that struct and its literal (round
  15's correction of the round-14 "marshals entry fields" claim;
  round 16 added the identity fields, whose intermediate fill site
  is the hubcore entry builder,
  cmd/evener-hub/internal/hubcore/attention.go:100-113). The
  frame's payload declaration names the ask
  TURN id as the exact-turn fetch's address; a call-id-only payload cannot
  address it and no resolver exists (red 8's finding). Red 8 turns green.
- The composite is also what the scoped clear and the restore subtraction
  address (items 2-3): the identity is not presentation chrome, it is the
  fence's addressing mechanism.
- The PAIR travels the same chain, end to end (round 9's design, spec 4
  scope 2): `PendingAskState` gains `BootGeneration` — additive, minted
  beside `Revision` inside `PendingAskCalls` under the same lock
  (`agent/session_tools_ask.go:101-116`; the state type today carries
  `Version/InstanceID/Revision/Calls` and no boot field,
  `appwire/types_pending_ask.go:6-17`) — so the row projection clones it
  with the state it already clones
  (`server/thread_envelope.go:423-425`), the probe's row copy, the
  `LiveEntry`, `AttentionEntry`, frame and relay legs above carry both
  components, and the trigger's comparison keys and the relay's fence
  read a pair that actually arrives. Round 12's spec-4 additions
  and round 13's clamp metadata ride
  the same chain beside the pair: the set's call-less count
  (`PendingAskState.CalllessCount`, minted inside the same accessor
  where the questions are walked, cloned with the state), the
  clamp's composite total and truncation fact
  (`PendingAskState.CompositeTotal`, minted inside the same
  accessor with the pair, and `PendingAskState.CallsTruncated`,
  stamped at the wire projection where the clamp lands — round
  14's mint-site split — the total
  landing on `AttentionEntry` in a declared field, so the frame's
  truncated-list marker is computable from the entry), and the
  ask-pageable capability fact (`AttentionEntry`'s additive bit,
  derived at `DeriveAttentionFromRoots` from the `LiveEntry`
  capabilities the derivation reads) — all enumerated in spec 4's
  scopes 2 and 5, this record's chain being their shared carrier
  path.

### 7. Carrier storage decided once (round-7 T1)

**Decision: the agent session carries its boot generation as a field, set
by post-construction initialization.** Round 8 verified the constructor
wiring this section originally sketched is impossible in the mint's own
order: `NextBootGeneration` runs after it owns the session
(agent/schema/boot_generation.go:22-34) — fresh and restore construct at
cmd/evener/serve.go:725-765 and mint at :776-788, Clear constructs at
:1104 and mints at :1124 — and the production restore entry is
`RestoreSessionFromMetaWithConfig` (agent/session_init.go:888), not the
test helper this record originally named.

- `agent.Session` gains a boot-generation field beside `askPendingRevision`
  (agent/session.go:753). The hub sets it immediately after construction
  on every path — fresh (`NewSession`), restore
  (`RestoreSessionFromMetaWithConfig`), Clear's replacement, and the
  delegate-restore paths — BEFORE the first admission or projection
  touches the session. `PendingAskCalls()` reads it beside `Revision`
  under the same lock
  (agent/session_tools_ask.go:101-112).
- Why not the server-side overlay: the comparison and the name capture
  (item 1) run daemon-side under the session lock, where the boot
  generation must be known to mint and compare the pair; an overlay at
  projection time can serve the row but cannot feed admission, so the
  session field is the one decision that serves both the wire and the
  fence. Restart safety is the pair's boot component: a restored session
  runs under a NEW boot generation, so a delayed answer fenced against the
  pre-crash boot is refused on the boot half and the Manager re-reads —
  exactly what the revision alone cannot do (spec 4's restart journey).

## The red-test list the implementer lane inherits

Existing on this branch, with what each becomes when the machinery lands:

1. **`TestThreadEnvelopeAskFacetSamplesOneSet`** (server; RED) — green with
   the atomic snapshot (item 5).
2. **`TestAskFenceSteerClearSiteComparisonGap`** /
   **`TestAskFenceQueueClearSiteComparisonGap`** (agent; RED) — placement
   refutations: the states they build are unreachable once the comparison
   moves inside the admission transaction. Retire with the machinery and
   replace with: (a) the admission-refusal red — accept a queue/steer
   carrying a stale pair → typed refusal receipt, pending set untouched,
   nothing durable; (b) the scoped-survival red — accept a fenced answer
   against {Q1}, append Q2 mid-turn, run consumption → Q1 resolved, Q2
   pending (the queue-site red's name-subset observation is the assertion
   seed).
3. **`TestAskFenceRefusedAnswerMustNotResolveAcrossRestore`** (agent; RED) —
   placement refutation; the refused-but-durable state is unreachable once
   acceptance refuses before durability. Replace with the named-subtraction
   restore red: a resolving turn whose CAPTURED names cover a subset of
   earlier pending calls → the unnamed asks survive restore. Keep
   **`TestAskRestoreKeepsAsksPostedAfterADurableAnswer`** (passes today) as
   the anti-regression for the restore changes.
4. **`TestAskFenceEmptySetArrivalAdmitsUnbranched`** (agent; characterization)
   — flips to the empty-set refusal: a fenced arrival against an
   already-cleared set returns the typed refusal. Seam: the pair fields on
   `TurnQueueParams`/`TurnSteerParams` plus the admission comparison
   treating an empty pending set as a mismatch.
5. **`TestClientMutationSteerCarriesThroughTheJournalRecordNotTheQueueEntry`**
   (agent; characterization) — its queue-entry-has-no-preconditions arm
   stays as the shape guard; add the machinery red: accept a steer carrying
   the pair → the persisted `clientMutationRecord` carries it across a store
   reopen, and the drain path reads it. Seam:
   `clientMutationPreconditions` extended with the pair and the captured
   names.
6. **`TestSkillInputGateReadsTheRowOnlyForSkillBearingInput`**
   (cmd/evener-hub; characterization) — keeps its ordinary-text arm
   (ordinary input still reads no row) and GAINS the arm the fence
   needs: a PAIR-BEARING text-only queue/steer reads the row through
   the common prepare. Keep
   the skill-bearing and fail-closed arms.
7. **`TestClientMutationQueueReceiptIsTerminalAppliedEvidence`** (agent;
   characterization) — flips to: a stale-pair acceptance returns the typed
   refusal receipt (not applied; the record replays the refusal). The
   matched-accept terminality arms stay unchanged.
8. **`TestProbeAndRosterRowCarryAskIdentityForTheExactTurnFetch`** (hubcore;
   RED) — green when the composite lands on the probe/row/frame path.

New journeys the implementer lane owes (spec 4's plan, re-pointed at this
design): the delayed queued and steered answers racing an append (now
refused at acceptance, never at consumption); the restart collision (refused
on the boot component); the older-daemon pre-dispatch refusal through the
common prepare on all three modes; the append-delivers trigger on the pair
(broker change detection, DESIGNED IN SPEC 4 — unchanged by this spike);
and the end-to-end relay
journey on the real queue path. The round-8 revision adds one red per
evidence finding: the same-turn composite restore (two asks in ONE turn —
today's execution-granular answer cannot distinguish them; the captured
names can); the post-admission append (in NEITHER the captured list nor
the durable names — live and restore agree, where today's
consumption-time sample puts it in the durable names only); the three
newly-routed clear sites (steering-carrier entry
session_lifecycle.go:1530-1575, incorporated-replay :2218-2222,
recovered-failure :2303-2313 — an append racing each survives); the
drain/promote carry (a queued ask's captured set survives the REPLACEMENT
record through `AcceptClientMutationDrainAsSteerAs`); the failure-writer
names (a `TurnFailure`-with-`SteeringCarrier` resolving turn carries the
captured names); and the wiring initialization (a freshly constructed
session admits against a real pair, never a zero value). Round 9
adds: the legacy-calls capture (call-less and mixed sets, live and
across restore, plus the all-zero capture's typed empty-set refusal);
the crash-cut writer (a scoped-survivor ask admitted before a
pre-append store failure survives the recovery's
`recordClientMutationFailure` resolve — the fourth evidence writer);
the steer-writer narrowing (a fenced steer's durable turn names
exactly its captured executions, where today's claim/delivery sampling
unions a post-admission append in); and the drain dominance (a
fenced-plus-unfenced drain resolves at the unfenced source's
whole-set semantics, never weakening it).

## What this spike did not verify

- No live two-process run: the older-daemon gate (item 4) is pinned at the
  unit seam (`ensureSkillInputSupported` + the handler wiring cited from
  source); the `AskFence` bit and the common prepare do not exist, so no
  e2e could run. The hub-integration lane owns that journey post-change.
- The check-then-clear reds pin the gap at primitive level, in the real call
  order the two sites use; the spike did not drive a full mid-turn model
  race against a carrier steer. The harness for that exists
  (session_drain_as_steer_turn_boundary_test.go's
  `steeringCarrierClaiming`/`steeringCarrierClaimed` channel seams) and is
  the implementer lane's instrument for the replacement reds.
- The restore reds use fixture transcripts built from the same append
  primitives the live paths use (`appendTurn`,
  `appendTurnAfterTranscriptWrite` + `appendClientMutationTranscriptLocked`);
  they were not produced by a killed daemon.
- The R1 preemption itself is scheduler behavior; a deterministic proof that
  the gap is preemptible is impossible without adding a seam inside the
  window — which is the finding. The reds prove the consequence of the gap
  by constructing the interleaving the gap permits.
