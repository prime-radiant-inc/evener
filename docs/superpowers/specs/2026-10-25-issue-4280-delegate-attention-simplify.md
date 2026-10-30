# Simplify delegate attention stop pre-read and retirement wake scan

Issue: <https://github.com/prime-radiant-inc/evener/issues/4280>

Lane: `issue-4280-delegate-attention-simplify`, branched from
`dc6c93e0810857db3359983854c8510e0b4a3709`. `origin/main` was
`7c57edb6e56c5a4124aabf78fcee298c0906753e` when this spec was written (the
issue's base note named `013b584c4179a3062c2ad02271e39cb953032763`, which is an
ancestor); the lane merges current `origin/main` before the final push.

This is a cleanup of the #4274 stop/clear reconciliation. The stop and attention
contracts that must not move:

- A stop still discards an attention item the transcript still owes, and still
  re-records a wake whose item the transcript already resolved (same
  disposition and resume generation, appending nothing) while renewing that
  marker's durability barrier.
- A stop and its cleanup append exactly one resolution for an item; a
  conflicting second resolution stays an error.
- Retirement still refuses while any delegate transcript owes attention, and
  still does not block on a closed delegate's resolved wake.

One retirement outcome narrows deliberately: a resumable delegate's wake whose
item the transcript already resolved used to block the full retirement read
through the in-memory wake alone. The fold decides that nothing is owed, so the
full read now proceeds. The non-blocking diagnostic snapshot, which skips
transcript folds, can still name that stale wake as a blocker; it is a
conservative caution and never the retire decision, and the manual-retire wait
pre-check used the same wake set before and after this change.

## Item 1: make "discard unless already resolved" a resolver mode

**Invariant.** For a stop cleanup plan with `disposition == discarded`, the
recorded outcome is: the transcript's own `(disposition, resumeGeneration)` when
one exists, otherwise `(discarded, 0)`. On the resident path the pair is chosen,
validated against, and appended under `s.attentionMu` from the cursor fold. On
the cold path it is chosen from the fold built from the entries the writer
opened with, and that same fold is the one `appendDelegateAttentionResolutions`
validates. If the fold already records the pair, the writer re-runs
`EstablishDurability` and appends nothing.

**Why it is safe.** `resolveAttentionDurablyForGeneration` already folds,
validates, reopens the writer with `EstablishDurability` for an exact recorded
pair, appends only ids the fold does not record, and verifies the result;
`appendDelegateAttentionResolutions` skips an exact recorded resolution, and
`validateDelegateAttentionResolutions` rejects a conflicting one. Moving the
mode decision into each resolver, after the fold it will validate against,
removes the controller pre-read and its check/append gap.

The cold path has two folds today: a preflight read before the writer opens and
the writer's own fold after. The mode must be resolved from the **writer's**
fold, not the preflight, or a resolution landing between them would still
surface as a conflict. The cold resolver therefore skips the preflight
validation for the mode, opens first, resolves from the writer fold, and lets
`appendDelegateAttentionResolutions` validate the concrete pair against that
same fold. The wrapper `appendColdAttentionResolutionWithOpen` passes generation
0 into that function; the mode's conversion replaces it with the transcript's
recorded generation, so a recorded `consumed`/generation-2 marker still survives
and its barrier is renewed.

`resumeGeneration` leaves the controller helper because only the stop path ever
supplied a non-zero value and that value now comes from the resolver's fold.
The `0` at `escalateOneUnreachableDelegateAttention` is removed with the
parameter. The dormancy inventory row for
`(*delegateTreeController).resolveDelegateAttentionDurably` changes its symbol
from `resolveAttentionDurablyForGeneration` to `resolveAttentionDurably`, and
`TestResolveDelegateAttentionDurablyNilRuntimeInvalidRef`
(`agent/delegate_tree_restore_branches_test.go`) drops its trailing `0`.

A missing transcript changes the resident path's error from the pre-read's
`fs.ErrNotExist` to `attention "<id>" is not pending` (the cursor treats a
missing file as an empty fold). The cold mode opens first, so its error stays a
file-open `fs.ErrNotExist`. No caller branches on either error; the cleanup
fails and the stop stays pending for retry.

**Change.** Add `delegateAttentionDiscardUnlessResolved` as a resolver mode (it
is never written to a transcript). Add one helper that maps it to a concrete
pair from a fold and is called by both resolver paths. Change
`resolveDelegateAttentionDurably` to
`(runtime, transcriptRef, ids, disposition)` and route the cold path through
`appendColdAttentionResolutionWithOpen`. Delete
`recordedDelegateAttentionResolution`.

**Tests.** A recorded `consumed`/generation-2 marker survives a
discard-unless-resolved pass with exactly one resolution turn, on both the cold
and resident writer paths, and a recorded consumed marker whose earlier fsync
failed has its durability barrier renewed without a second marker. An
unresolved item records exactly one `discarded` marker. A cold-path test using
the existing `c.attentionOpen` seam appends a
`consumed`/generation-2 marker before returning the writer entries: before the
change the resolver requests discarded/0 and fails with a conflicting
resolution; after it resolves the mode from the writer fold and succeeds. The
existing stop tests keep covering end-to-end stop completion, the unsynced
discard's barrier renewal, and the consumed-then-stop scenario.

## Item 2: the full retirement read does not need the wake check

**Invariant.** `retirementEvidence` blocks exactly when an obligation is real.
For attention, the transcript fold is the authority; no wake noted before or
during the read can be missed.

**Proof that the fold read leaves no window.**

- A wake is only noted beside durable attention content: the arm and delivery
  paths append with the synced door before noting, and `commitStart` replaces
  attention only after the start's transcript content exists.
  `clearResolvedDelegateAttention` already relies on this ("a wake ID is only
  ever noted beside a durable transcript entry").
- The owner scan and every wake note run under `c.mu`. `CompleteDelivery` holds
  `c.mu` while it notes and while its `c.deliveries` receipt exists, then bumps
  `evidenceVersion`; `commitStart` holds `c.mu` from its reservation through
  `replaceDelegateAttentionLocked` and its `evidenceVersion` bump. A scan that
  returns no blockers therefore cannot interleave either note: the delivery
  receipt or start reservation is still counted and blocks, or the note and
  version bump have already happened.
- `TryClaim` sets phase `preparing` and the exact tree fence before
  `retirementEvidence`; `Prepare` rereads under the same claim. A note after
  the scan but before the final revalidation bumps `evidenceVersion` and makes
  the read busy.
- A wake noted before the scan has its durable content already in the
  transcript, so the fold read after the scan reports its item pending.
- In-memory obligations the fold cannot see stay in the scan (`NeedsAttention`,
  `PendingStopSeq`, `live.attentionIDs`, deliveries, stop and turn counters).

The deleted check also blocked a **resolved-item wake** on a resumable
non-closed delegate. That state is reachable — a resumable descendant of a
closed delegate keeps its wake because closing resumability lowers descendants'
`NeedsAttention` but drops no wakes — and it is obligation-free: the transcript
has the resolution, the escalation path only forgets such a wake, and a closed
or ancestor-fenced delegate cannot rebuild it. Retirement may proceed.

The shared scan is also used by `retirementNonBlockingEvidenceRead`, which
deliberately skips transcript folds to avoid unbounded I/O while work is
admitted. That read is a diagnostic snapshot and the manual-retire wait
pre-check, never the retire decision, so it keeps the wake check as its
in-memory proxy.

**Change.** Move the wake check out of `retirementOwnerScanLocked` into a new
`retirementWakeBlockersLocked`, called only by
`retirementNonBlockingEvidenceRead` under the `c.mu` it already holds. The full
read keeps its one-contract scan and the non-blocking proxy lives in the read
that deliberately skips folds.

**Tests.** A stale resolved wake on an idle resumable delegate no longer blocks
`TryClaim` (it did before the change). A pending transcript item with no
in-memory wake still refuses `TryClaim`; that is the guard for the fold being
the witness, and it is the test that would fail if a future change skipped the
fold for a member without wakes. The existing pending-attention refusal and
resolved-closed-wake eligibility tests stay green. A scan-level test pins the
non-blocking read's wake scope on folded states (a closed delegate's retained
wake is quiet, a resumable delegate's wake reports); the reduced `PhaseClosed`
clause itself is proven equivalent by the fold transition analysis and pinned
by the fold invariant test, because no reachable state can distinguish it.

## Item 3: leave the per-delegate fold cost alone

**Decision: not changed.** Skipping the fold for a closed delegate with no
wakes is not safe. `delegateAttentionProjectionEligible` excludes closed
delegates and `reconcileDelegateAttentionFromTranscripts` rebuilds only eligible
delegates' wakes, so after a cold restore a closed delegate's pending attention
has no wake; the transcript fold is the only witness. A resident closed
delegate reaches the same shape on the failure path where content was appended
and the note never landed: `tryOpenDelegateAttention` appends its open event
before it notes, so a failed journal append leaves durable content with no
wake, and a later closure keeps it. #4274 round 3 fixed exactly this escape, and
`docs/subagent-management/11-delegate-resource-model.md` records that a closed
delegate's wakes do not block but its transcript evidence still does for
anything pending. The saving is one fold-cache hit per closed member per
evidence pass; the guarantee it would trade away is larger.

**Tests.** A closed delegate with pending transcript attention and no in-memory
wake still refuses `TryClaim`. The existing closed-delegate pair covers the
resolved-wake and unresolved-with-wake directions.

## Item 4: `Resumable` implies not `PhaseClosed`

**Invariant.** In the delegatestore fold, `Phase == PhaseClosed` implies
`!Resumable`; equivalently, `Resumable` implies `Phase != PhaseClosed`.

**Proof.** Every assignment to `PhaseClosed` is guarded by a false `Resumable`:

- `applyCreated` sets `PhaseClosed` only when `descriptor.Resumable` is false,
  and seeds `Resumable` from that descriptor.
- `applyRunFinished` sets `PhaseClosed` only in the `!aggregate.Resumable`
  branch.
- `applyResumabilityClosed` sets `Resumable = false` before setting
  `PhaseClosed` for an idle aggregate.
- `applySubtreeStopCompleted` sets `PhaseClosed` only in the
  `!aggregate.Resumable` branch.

`Resumable` becomes true only at creation from the descriptor, `applyRunStarted`
refuses a non-resumable aggregate before setting `PhaseRunning`, and no other
production code assigns either field. `store.Load` returns events, and the fold
replays them, so older journals carry the same invariant. The invariant covers
fold-produced state only; hand-built test aggregates (including the one in
`TestHasPendingDelegateAttentionClosedPhase`) may construct the opposite pair.

**Change.** In `retirementWakeBlockersLocked` (the non-blocking read's proxy),
the wake check uses `aggregate.Resumable` alone; the removed
`Phase != PhaseClosed` conjunct is equivalent by the invariant above.
`hasPendingDelegateAttention` has the same composite but is the drive's
predicate, not the retirement scan, and stays out of this change.

**Tests.** A fold test pins the invariant across created-not-resumable,
closed-while-idle, closed-while-running-then-finished, and stop-completion
transitions.

## Verification

- `go test ./agent -run 'Test.*(DelegateAttention|Retirement|OwnerScan|Cleanup)' -count=1`
- `go test ./agent -run TestDelegateControllerProductionIntegrationMatchesInventory -count=1`
- `go test ./agent/internal/delegatestore -count=1` (fold invariant)
- `go test ./agent -run 'Test.*(Attention|Stop|Retirement)' -count=1` resolver
  coverage
- `make build`

No full-suite run; CI owns it. The delegate resource model's retirement
paragraph is updated for the fold-decided wake rule; the product guide and
subsystem rows keep their existing contracts.
