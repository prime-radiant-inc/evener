# Issue #4999: drop `hasPendingDelegateAttention`'s redundant `PhaseClosed` guard

## Intent and scope

`hasPendingDelegateAttention` (`agent/delegate_tree_attention.go:591-607`)
decides whether the attention drive has work. Its per-delegate test is:

```go
len(state.wakeIDs) != 0 && aggregate != nil && aggregate.Resumable &&
	aggregate.PendingStopSeq == 0 && aggregate.Phase != delegatestore.PhaseClosed
```

In the delegatestore fold, every transition into `PhaseClosed` requires
`Resumable == false`, so `Resumable` alone proves the delegate is not closed.
The `Phase != PhaseClosed` conjunct is therefore redundant on folded state.
#4280 removed the same conjunct from the retirement proxy but left this drive
predicate out of scope.

Deliverables: `agent/delegate_tree_attention.go`,
`agent/delegate_tree_attention_branches_test.go`, this specification. No product
behavior change on states the fold can produce.

Non-goals: the retirement proxy (`retirementOwnerScanLocked` and its helpers),
`delegateAttentionProjectionEligible`, `delegateAttentionOpenEventLocked`, the
fold, the same redundant conjunct in `delegate_shell_repair.go`'s bootstrap
scan, and any product guide (the observable drive behavior does not change).

## Invariant proof

Every assignment to `PhaseClosed` in `agent/internal/delegatestore/fold.go` is
guarded by a false `Resumable`:

- `applyCreated` (lines 292-313) seeds `Resumable` from the descriptor at line
  308 and sets `PhaseClosed` only when `!descriptor.Resumable` (lines 310-312).
- `applyRunFinished` (lines 370-444) sets `PhaseClosed` only in the
  `!aggregate.Resumable` branch (lines 430-435).
- `applyResumabilityClosed` (lines 446-468) sets `aggregate.Resumable = false`
  at line 457 before setting `PhaseClosed` for an idle aggregate (lines
  459-461).
- `applySubtreeStopCompleted` (lines 500-549) sets `PhaseClosed` only in the
  `!aggregate.Resumable` branch (lines 542-546).

`Resumable` becomes true only at creation from the descriptor;
`applyRunStarted` refuses a non-resumable aggregate and never assigns it. Older
journals replay through the same fold. `TestFoldClosedPhaseIsNeverResumable`
(`agent/internal/delegatestore/fold_test.go:451`) pins the invariant across
created-not-resumable, closed-while-idle, closed-while-running-then-finished,
and stop-completion transitions.

## Chosen change

1. Remove only `&& aggregate.Phase != delegatestore.PhaseClosed` from
   `hasPendingDelegateAttention`. Keep `Resumable`, `PendingStopSeq`, parked and
   wake checks unchanged.
2. Rebuild `TestHasPendingDelegateAttentionClosedPhase`'s fixture from fold
   events: use the existing controller test harness, `seedDelegateControllerIdle`
   and `closeDelegateControllerResumability` so the real append/fold path
   (`delegatestore.AppendBatch` assigns sequences and applies each event),
   assert the folded aggregate is closed and not resumable, then assert the
   predicate is false for its retained wake. The current hand-built
   `{Resumable: true, Phase: PhaseClosed}` aggregate cannot come from the fold;
   after step 1 it would make the test assert the opposite of its name.

No new test is added: `TestHasPendingDelegateAttentionWithPending` already pins
the true case, `TestHasPendingDelegateAttentionNotResumable` pins
non-resumable false, and the fold test owns the invariant. There is no
reachable state on which the old and new predicates differ, so a red test of
observable behavior is impossible by construction; the red is that the old
hand-built fixture fails once the guard is removed, before it is rebuilt.
With only the conjunct removed through a Go `-overlay`, exactly
`TestHasPendingDelegateAttentionClosedPhase` fails; the rebuilt folded fixture
passes under the same overlay.

The rebuilt test pins the predicate on a fold-produced closed delegate; it does
not distinguish the removed conjunct, because no fold-produced state can. A
future change that re-adds the conjunct would not fail it.
`TestFoldClosedPhaseIsNeverResumable` is the pin for the invariant the removal
relies on.

## Verification

- Pre-change: `rg -n 'Phase != delegatestore.PhaseClosed' agent/delegate_tree_attention.go`
  reports line 602, and `go test ./agent -run '^TestHasPendingDelegateAttention' -count=1`
  passes with the hand-built fixture.
- `go test ./agent/internal/delegatestore -run '^TestFoldClosedPhaseIsNeverResumable$' -count=1 -v`
- `go test ./agent -run 'TestHasPendingDelegateAttention' -count=1 -v`
- Focused attention/retirement tests:
  `go test ./agent -run 'Test.*(DelegateAttention|Retirement|OwnerScan|Cleanup)' -count=1`
- `make build`
