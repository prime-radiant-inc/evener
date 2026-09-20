# Task 75 — PR #1098, RoboRev round 9 (head 7282eb53c)

Status: **DONE_WITH_CONCERNS** (two of the three findings are factually wrong at
this head; constraint 7 applies).

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`,
branch `codex/mobile-transcript-identity`. Two commits on top of `7282eb53c`.
Not pushed. Main not merged.

| Finding | Disposition | Commit |
| --- | --- | --- |
| Low — nil-unsafe failure counter dereference | **Refuted** | — |
| Medium 1 — interrupt-drain claim not gated on poison | **Fixed** | `2fd9f72bf` |
| Medium 1 — normal-path check-then-pop window (#1165) | **Refuted as in-scope**; #1165 stays open | — |
| Medium 2 — provisional turn count persists on rejection | **Refuted** | guard test `5313d7a4d` |

---

## Low — "Nil-unsafe failure counter dereference" (`transcript.go:548-559`) — REFUTED

The finding claims `countAppendedEntryLocked` "unconditionally dereferences
`w.failures`", so a `NewWriter` without `TrackFailures` panics on the whole-line
failed-write path.

It does not dereference it. `countAppendedEntryLocked` is three lines
(`agent/transcript/transcript.go:567-570`):

```go
func (w *Writer) countAppendedEntryLocked(turn schema.Turn) {
	w.seq++
	w.failures.Observe(turn)
}
```

`Observe` has a pointer receiver that nil-checks first
(`agent/transcript/failures.go:112-115`), as does `Count`
(`failures.go:138-142`). Calling a pointer-receiver method on a nil pointer is
not a dereference in Go. The nil-counter contract is already asserted
explicitly, with a comment saying so, at
`agent/transcript/failures_fuzz_test.go:165-169`:

```go
var nilCounter *FailureCounter
nilCounter.Observe(schema.Turn{})
if nilCounter.Count() != 0 { ... }
```

Verified empirically with a throwaway in-package test (since deleted) that built
a bare `&Writer{}` — `w.failures == nil` — and called both
`countAppendedEntryLocked` and `poisonLandedBytesLocked(turn, 10, 10)` (the
whole-line-landed path the finding names). No panic; `seq` advanced to 2 and
`poisoned` was set. `ok primeradiant.com/evener/agent/transcript 2.348s`.

The `w.failures == nil` check the finding points at for consistency
(`transcript.go:315`, inside `FailedToolCalls`) exists for a different reason:
that function must distinguish "0 failures, counted" from "nobody counted", and
returns `(0, false)` for the latter. It is a semantic check, not a safety one,
so it is not evidence that the counting path needs one.

No code change. The defect cannot occur.

## Medium 1a — interrupt drain claims the queue head on a poisoned transcript — FIXED (`2fd9f72bf`)

Real, and the coordinator's reading is right: round 6 added
`refuseBeforeClaimingOnPoisonedTranscript` /`refuseTurnOnPoisonedTranscript` so
no path claims a queued message the next gate will refuse, and the interrupt
drain at `agent/session_lifecycle.go:1125` was missed. It popped the head
durably, `continue`d, and the gate at the top of the loop
(`session_lifecycle.go:1027`) then refused — leaving the message in no
transcript, no queue and no session.

Fix: the same shared gate, in front of that pop, in the existing
`if cfg, drainable := ...` branch. One encoding, not a third copy. The refusal
is joined onto this turn's error because this branch is the one that returns:
without the join the caller hears only the interrupt and nothing says the
transcript is what stopped the drain.

RED (before the fix):
```
RED PROBE err=session transcript stopped accepting records: transcript writer refuses further appends after an unresolved partial append poisoned=true depth=0 requests=2
--- FAIL: TestPoisonedWriterLeavesTheInterruptDrainedMessageQueued (0.12s)
    durable queue depth after the refusal = 0, want the message still waiting
```
(The probe line was scaffolding and is not in the committed test. Note the
pre-fix error already wrapped `ErrWriterPoisoned` — via the *next* iteration's
gate, after the claim was already spent. The queue depth is what the finding is
about, and it was 0.)

GREEN (after the fix): `ok primeradiant.com/evener/agent 0.767s`

New test `TestPoisonedWriterLeavesTheInterruptDrainedMessageQueued` plus helper
`interruptDrainTurnContext`, in
`agent/session_environment_rollback_regression_test.go` alongside its
completed-turn sibling `TestPoisonedWriterLeavesAQueuedMessageQueued`. Building
the RED took one real constraint: `interruptDrainConfig`
(`agent/session_queue.go:846-866`) only drains on a *bare* `context.Canceled`,
so the poisoning has to come from a buffered (warn-and-continue) record before
the model call, and the interrupt from inside the model step — a durable record
failing first would abort the turn with `ErrWriterPoisoned`, which is not
drainable and never reaches the branch.

## Medium 1b — normal-path check-then-pop window (#1165) — NOT CLOSED HERE

Read `gh issue view 1165`. Closing it is not a field/parameter-sized change, so
per the coordinator's instruction I left it and am refuting in-scope closure.

The window is real and is `agent/session_lifecycle.go:1210-1215` /
`agent/session_client_mutation_queue.go:219-224`: the gate reads one snapshot
under `clientMutationStore.stateMu` (`wakeHasClaimableWork`,
`session_client_mutation_queue.go:369-372`), the claims re-clone under
`clientMutationStore.mu` inside `mutate` (`popQueueHead`,
`agent/session_queue.go:664-706`; `claimSteeringCarrierTurn`,
`session_client_mutation_queue.go:309-333`). `QueueHeld`/`InterruptFence` can
flip in between.

Closing it the way #1165 directs — the poison check made part of the claim's own
decision, inside the `mutate` the claim acts in — would require, at minimum:

- `popQueueHead` and `claimSteeringCarrierTurn` gaining a poison decision inside
  their closures and a way to report "refused" distinctly from "nothing to
  claim" (today both return a zero value; the closure's only error channel emits
  an `EventWarning` and swallows the result — `session_queue.go:702-705`);
- the three claim sites reacting to that new outcome
  (`session_lifecycle.go:1125`, `session_lifecycle.go:1210-1215`,
  `session_client_mutation_queue.go:219-224`), plus the start rail's own guard
  at `agent/session_client_mutation.go:448`;
- `wakeHasClaimableWork` / `refuseBeforeClaimingOnPoisonedTranscript` losing
  their current role, which the round-6 tests assert
  (`TestPoisonedWriterDoesNotPopAQueuedMessage`,
  `TestPoisonedWriterDoesNotClaimAStartMutation`, and the idle-wake stand-down
  at `session_environment_rollback_regression_test.go:1075-1085`);
- taking the transcript writer's own lock (`Poisoned()`) inside the
  client-mutation store's `mutate`, which holds its lock across durable
  snapshot/rename I/O — a new lock nesting I am not willing to introduce as a
  side effect of a review round.

That is the "design change to the claim sites, not a line move" #1165 itself
names. #1165 stays open as the recorded follow-up; nothing in `2fd9f72bf`
narrows or widens it (the interrupt site's new gate has exactly the same
residual window as the completed-turn site's, and the same restore behind it).

## Medium 2 — "Provisional turn count persists on input rejection" — REFUTED

The mechanism the finding describes is accurate up to its last step, and the
last step is wrong.

Accurate: `appendEnvironmentContext` does return the original write error in the
`environmentEntryDurable` case, after committing the entry's in-memory side
effects and calling `maybeAutoSave` (`agent/session.go:1661-1707`) while
`acceptUserInput`'s provisional `s.turns++` still stands; `acceptUserInput` then
rolls the turn back at `agent/session_lifecycle.go:2071` and returns the error.

Wrong: "metadata is not saved again". `processOneInput`
(`agent/session_lifecycle.go:1458`) registers an unconditional deferred
`s.maybeAutoSave()` at `session_lifecycle.go:1511-1518` — before it calls
`acceptUserInput` at `session_lifecycle.go:1696` — which fires on *every* exit
including this rejection, i.e. strictly after the rollback. `acceptUserInput`
has exactly one caller (that one), so queued and client-start inputs are covered
by the same defer. The persisted `AcceptedInputTurns` is therefore already 0,
and a restart already loads 0.

On the coordinator's first question — "if reconciliation proves the entry
durable but the function still returns the original write error, the caller
rolls back a turn whose environment entry is already in the transcript": the
tests make that the deliberate contract, not a defect.
`TestEnvironmentAmbiguousWriteDoesNotDuplicateEntry`
(`session_environment_rollback_regression_test.go:466-500`) asserts the durable
case returns an error wrapping *both* the sync and the rollback failure while
the entry stands durable and model history carries it once and does not re-emit
it; `TestQueuedEnvironmentFailureReturnsRunnableClaimAndWakesRetry` asserts the
input is handed back for retry. So: the environment entry is a transcript record
in its own right, it stays, and the user input is retried without re-rendering
it. Making the durable case return `nil` would re-point that test, which
constraint 6 forbids and which would be wrong anyway.

Verified by causation rather than by assertion. With the shipped code the
proposed RED passes. Removing the single line `s.maybeAutoSave()` from
`processOneInput`'s defer and changing nothing else:

```
--- FAIL: TestRejectedInputDoesNotPersistItsProvisionalTurn (0.10s)
    persisted accepted input turns after the rejected input = 1, want the none it accepted
```

restoring it:

```
ok  	primeradiant.com/evener/agent 0.715s
```

That is the exact 1-vs-0 the finding predicts, and the deferred checkpoint is
the only thing standing between it and the session. A probe also confirmed the
scenario really reaches the branch in question:
`durable env ids=[turn_environment_01M2BDPMQETF0D8GM0S2A9DP44] envTurns=1 turns=0 meta=1`
(with the defer removed) — the entry is durable, the turn is rolled back, the
metadata is the stale one.

No production change. Commit `5313d7a4d` adds `TestRejectedInputDoesNotPersist
ItsProvisionalTurn` as a **guard**, not a regression test: it passes on
unmodified code, and its subject (`test(agent): …`) and doc comment say so
rather than claiming a fix. It drives a real rejected input through
`ProcessInput`, asserts the persisted `AcceptedInputTurns` is 0, and then does
the restart the coordinator asked for — `restoreQueuePersistTestSession` — and
asserts the restored session's `turns` is 0. It exists because one deleted line
reintroduces exactly the defect RoboRev described, and nothing else pinned it.

---

## Gates

All run from `.../worktrees/pr-1098/agent` unless noted.

| Gate | Result |
| --- | --- |
| `gofmt -l` on both touched files | clean (no output) |
| `go vet ./...` | clean |
| `go test -count=1 ./...` | zero non-`ok` lines |
| `go test -race -count=3` on the touched test sets | `ok primeradiant.com/evener/agent 13.079s` |
| `$(go env GOPATH)/bin/golangci-lint run ./...` | `0 issues.` |
| deadline audit | none exists for this module — `grep -n deadline Makefile make/*.mk` and `AGENTS.md` return nothing |

Race set: `-run 'TestPoisonedWriter|TestRejectedInputDoesNotPersistItsProvisionalTurn|TestEnvironment|TestQueued|TestDrainableInterrupt|TestUndrainableInterrupt|TestFailedDirectInput|TestReturnedDirectTurnClaim|TestRestoreDeferredHook'`.

golangci-lint was OOM-killed (exit 137) on its first attempt, which is what
ended the previous session; it completes cleanly at `--concurrency=2 GOGC=50`.

## Concerns

1. **Two of three findings are wrong at this head.** The Low one is wrong on a
   plain reading of three lines of Go with an existing fuzz assertion against it;
   Medium 2 is wrong about a defer registered 238 lines above the code it
   describes. Both would have been caught by reading one call deeper. Worth
   noting for the round-9 reviewers' signal.
2. **#1165 is still open and still reachable from this PR**, now from three
   claim sites rather than two. `2fd9f72bf` does not make it worse, but it does
   not make it better either. It should be fixed as its own change with its own
   review, not folded into a review round.
3. `5313d7a4d` is a guard, not a regression test. If the queue's policy is that
   only RED-first commits land, drop it — the refutation stands on the causation
   probe above without it.

---

# Task 81 — PR #1098, RoboRev round 10 (head `c03a77fb2`)

Status: **DONE**. Two commits on top of `c03a77fb2`. Not pushed, main not merged.

| Finding | Disposition | Commit |
| --- | --- | --- |
| Medium 1 — environment event clears the server's active turn ID | **Refuted** | guard test `e83a4c048` |
| Medium 2 — fold publishes onto a poisoned transcript | **Fixed** | `3a6ba6b91` |

## Medium 1 (`internal/appprojector/appwire_projection.go:263-282`) — REFUTED

The premise is right and the conclusion does not follow. Projecting
`EventEnvironment` does open and close a turn, so `p.activeTurnID` really is
`""` when `RecordAppEvent` reads it back at `server/appwire_runtime.go:434`, and
`server/appwire_runtime.go:441-443` really does write that value over
`s.appActiveTurnID` whenever `appPendingStableTurnID` is empty (which is the
`SetProcessing(true)` path — `server/server.go:859-863` sets `appActiveTurnID`
and leaves `appPendingStableTurnID` alone).

What it reads back is not `p.activeTurnID`. `RecordAppEvent` calls
`ActiveTurnID()`, which falls back to the reservation
(`internal/appprojector/appwire_projection.go:2174-2179`):

```go
func (p *AppEventProjector) ActiveTurnID() string {
	if p.activeTurnID != "" {
		return p.activeTurnID
	}
	return p.reservedTurnID
}
```

and the environment branch restores exactly that reservation on its way out
(`appwire_projection.go:270`, `:272`, `:281` — save, clear, restore), with the
comment saying why: the environment has its own durable identity and cannot
consume the runnable one. `SetProcessing(true)`'s `ReserveTurnID()`
(`appwire_projection.go:2135-2142`) is what put the id in `p.reservedTurnID` in
the first place. So the value written back is the same value that was already
there.

The projector tests already encode this as the single rule for "which events
carry the active turn" — `TestEnvironmentPreservesReservedRunnableIdentity`,
`TestEmptyEnvironmentDoesNotOpenTurnOrConsumeReservation`, and
`TestEnvironmentWithoutIdentityDoesNotUseRunnableReservation` in
`internal/appprojector/environment_reservation_test.go` all assert the
reservation survives and the following user input consumes it. The suggested
fix (a special case for environment events, or skipping active-ID replacement
for non-carriers) would add a second encoding of a rule that already has one.

What was genuinely missing is the server-level coverage the finding asks for, so
`e83a4c048` adds `TestServerAppWireEnvironmentEventKeepsTheProcessingReservation`
(`server/appwire_server_test.go`): `SetProcessing(true)`, then a real
`RecordAppEvent(EventEnvironment)`, asserting `srv.appActiveTurnID` is unchanged
and that the following user-input event runs as that same advertised id.

Verified by causation. Replacing the restore at `appwire_projection.go:281` with
a no-op and changing nothing else:

```
--- FAIL: TestServerAppWireEnvironmentEventKeepsTheProcessingReservation (0.00s)
    appActiveTurnID = "" after the environment event, want the "turn_1" SetProcessing published
```

restoring it: `ok primeradiant.com/evener/server 0.280s`. That is exactly the
empty value the finding predicts, and one line already prevents it.

No production change.

## Medium 2 (`agent/session_compaction.go:519-552`) — REAL, FIXED (`3a6ba6b91`)

Confirmed. `publishFoldTransaction` swaps model history
(`publishFoldedHistory`), resets the environment tracker
(`commit.resetEnvContextTrackerLocked`), claims the pinned note and stamps the
published revision — all before `commit.commitTranscriptsLocked()` writes the
checkpoint/summary entries. Those write errors are carried to `flush` and
surface only as a warning (`reportCompactionTranscriptAppend`,
`agent/session_namer.go:497-501`); nothing unwinds the publication and nothing
refuses it. There was no poison check anywhere in the compaction path.

Fix per the coordinator's ruling — refuse before publication rather than roll
back, which is this PR's own round-4 shape (a poisoned writer fails closed at
turn admission; a fold is the other durable write of the same writer). The gate
sits at the top of `publishFoldTransaction`, under `attentionMu` and before
`publishFoldedHistory`, returning the existing `ok=false` "publication lost"
answer. Both callers (`foldWithForceCompact` at `session_compaction.go:269`,
the round loop's auto-compaction at `agent/session_model_call.go:268`) already
handle that as "nothing committed, neither commit phase runs", with bounded
retries, so no signature or contract changes.

Placing the read under `attentionMu` is deliberate: it is the transcript door
every session append goes through, so unlike the queue-claim guards this check
and the entries it protects are genuinely serialized — no session write can
poison the writer in between.

RED: `compaction against a poisoned transcript reported success`
GREEN: `ok primeradiant.com/evener/agent 0.931s`

New test `TestPoisonedWriterRefusesToPublishAFold` in
`agent/session_environment_rollback_regression_test.go`: seeds 12 turns past
`PreserveRecentTurns`, emits environment context so the tracker has state to
lose, poisons the writer, then asserts `Compact` errors, history and
`historyRevision` are untouched, the environment tracker is not reset, and no
`EventContextCompaction` reached clients.

Checked against the existing contract for ordinary write failures:
`TestFallbackCompactionWriteFailureStillResetsEnvironmentTracker`
(`agent/session_envctx_compaction_write_error_test.go:37`) requires a plain
compaction write failure to still publish and still reset the tracker. Its
fixture returns `(0, err)` from `Write`, and a transfer of zero bytes does not
poison (`poisonLandedBytesLocked`, `agent/transcript/transcript.go:547-549`), so
that test stays green and the two conditions remain distinct: a write that
failed once still publishes; a writer that will refuse everything does not.

## Gates

| Gate | Root module (`internal/appprojector`, `server`) | Agent module |
| --- | --- | --- |
| `gofmt -l` touched files | clean | clean |
| `go vet` | clean | clean |
| `go test -count=1` | zero non-`ok` lines | zero non-`ok` lines |
| `-race -count=3` touched sets | `ok server 3.921s`, `ok internal/appprojector 1.352s` | `ok agent 13.876s` |
| `golangci-lint --concurrency=2 GOGC=50` | `0 issues.` | `0 issues.` |

## Concerns

1. Round 10 repeats round 9's pattern: the one non-clean reviewer's first
   finding is wrong because it read a field (`p.activeTurnID`) where the code
   calls an accessor (`ActiveTurnID()`) one screen below. Three of five Mediums
   across the two rounds have now been refutable on a one-hop read.
2. Medium 2 is a real bug and a good catch — the compaction path was the one
   durable write the round-4 poison rule never reached. Worth checking whether
   any *other* durable write still publishes before it persists; I did not audit
   beyond the fold.

---

# Task 85 — PR #1098, RoboRev round 11 (head `a33600eff`)

Status: **DONE**. One commit on top of `a33600eff`: `2501f17a9`. Not pushed, main not merged.

## Medium — environment publication races with session shutdown — REAL, FIXED (`2501f17a9`)

Both halves of the finding check out.

`appendEnvironmentContext` takes `attentionMu` then `s.mu` and reads the tracker
(`agent/session.go:1609-1616`) without ever consulting the shutdown state. Its
only caller gate is `processOneInput`'s entry check
(`agent/session_lifecycle.go:1547`), which runs ~150 lines before
`acceptUserInput` at `:1696` — so `closing` can be set anywhere in between and
the append proceeds regardless.

`Close` sets `s.closing = true` / `s.state = SessionClosed` in step 1
(`agent/session_lifecycle.go:375-376`) and emits `SESSION_END` at `:563-580`
without holding `attentionMu`; it first takes that lock at
`closeAttachedTranscript` (`agent/session.go:1954-1957`), further down. So the
append and the terminal boundary really were unserialized, and an
`EventEnvironment` plus a durable environment entry could land behind
`SESSION_END`.

Checked the #1109 boundary first, per the ruling. `e812703fc`
("fix(serve): publish terminal session boundary on shutdown") is about *who*
emits the terminal boundary and exactly once (`CloseForShutdown`, `emitTerminal`,
the forced-close-across-clear race); it added no lock that a late append could
observe. The boundary this fix needs already exists and is `attentionMu` — the
transcript door — and the environment event is *already* published under it for
precisely this ordering reason (`agent/session.go:1692-1704`: "while it is held
no fold can publish, so the order a live reader sees is the order the transcript
holds"). Using it is joining the existing boundary, not adding a second one.

Fix, two halves on that one lock:

- `agent/session.go`: the existing `tracker == nil` early return becomes
  `tracker == nil || s.closingOrClosedLocked()` — read under the `attentionMu` +
  `s.mu` hold the function already takes. Refuses silently (returns nil) like
  the two early returns beside it: the caller is a turn the close is already
  ending, and an error would make a dying turn unwind state the close discards.
- `agent/session_lifecycle.go`: `Close` takes `attentionMu` around the
  `SESSION_END` emit and releases it before `closeAttachedTranscript`. That
  waits for an append already in flight (which holds the door across its entry
  and its event) and shuts out any that starts later, since the later one
  re-reads `closing` under this same lock. The send stays bounded by the shared
  close budget — the same bound the emit already relied on — and the
  `RunSessionEnd` plugin hook above deliberately stays outside the hold.

Both halves are needed: the gate alone still allows an append that passed the
check to emit after `SESSION_END`; the lock alone still lets a later append
publish into a closed session.

RED: `model history environment turns after the shutdown append = 1, want none: the session is closed and nothing will read them`
GREEN: `ok primeradiant.com/evener/agent 0.646s`

New test `TestClosingSessionRefusesEnvironmentPublication` in
`agent/session_environment_rollback_regression_test.go`. It runs the append
from `Close`'s own dispose/sweep seam (`closeAfterDisposeSweepJoin`,
`agent/session_lifecycle.go:406`) — a point where `closing` is already set,
`SESSION_END` is still ahead, and no session lock is held — so the interleaving
is produced deterministically, on Close's goroutine, with no second goroutine
and no sleep. It asserts the append is a silent no-op, adds no model-history
turn, writes no durable entry, and that no `EventEnvironment` appears in the
collected stream at all (let alone after the `SESSION_END`), plus positive
checks that the seam ran and a `SESSION_END` was published.

## Gates (agent module)

| Gate | Result |
| --- | --- |
| `gofmt -l` (3 touched files) | clean |
| `go vet ./...` | clean |
| `go vet -tags evenerfuzz ./...` | clean |
| `GOOS=windows go vet -tags evenerfuzz ./...` | clean |
| `go test -count=1 ./...` | zero non-`ok` lines |
| `-race -count=3` shutdown/environment sets | `ok primeradiant.com/evener/agent 143.545s` |
| `golangci-lint run --concurrency=2 GOGC=50 ./...` | `0 issues.` |

Race set: `-run 'TestClosingSessionRefusesEnvironmentPublication|TestSession_SessionEnd|TestPoisonedWriter|TestEnvironment|TestEnvctx|Close|Shutdown|TestRejectedInputDoesNotPersistItsProvisionalTurn'`.

## Concerns

1. `Close` now holds `attentionMu` across the terminal emit. That emit is not a
   bounded send — it parks against a wedged authoritative consumer and runs the
   job-watch fan-out synchronously — so a wedged bridge now stalls this lock
   until the close budget fires, rather than only the emitter.
   `appendEnvironmentContext` already accepts exactly this trade for the same
   lock and says so in its own comment, and the close budget bounds it, but it
   is a real widening and the right thing for a reviewer to look at.
2. Environment appends are the only publication that takes the door; if another
   durable publication is added later it inherits this ordering requirement
   without anything enforcing it. Worth a note in the door's doc comment if a
   third one ever appears.
3. Three straight rounds have found something in the shutdown/poison/ordering
   family around this one code path. The PR is converging, but this area is
   where the remaining risk is.

---

# Task 90 — PR #1098, RoboRev round 12 (head `c48c59751`)

Status: **DONE_WITH_CONCERNS**. No commit this round: finding 1 is refuted on
evidence, finding 2 is refuted by reference per the standing ruling. Tree clean
at `c48c59751`. Not pushed, main not merged.

## Finding 1 — rollback sync fails, `removed` true, writer unpoisoned (`agent/transcript/transcript.go:607-615`) — REFUTED

The premise is accurate and not distinctive; the conclusion does not hold.

Measured all four failed-rollback outcomes through the fault harness
(`fuzz/fault`, the indices the existing tests document: `4=Seek 5=Write
6=Sync`, rollback `Truncate/Seek/Sync` behind whichever faulted). Throwaway
probe, since deleted:

```
B: entrySync + rollbackSync      (removed=true)  ErrRollbackFailed=true poisoned=false seq=1 nextAppend=<nil>
B: entrySync + rollbackTruncate  (removed=false) ErrRollbackFailed=true poisoned=false seq=2 nextAppend=<nil>
A: entryWrite + rollbackSync     (removed=true)  ErrRollbackFailed=true poisoned=false seq=1 nextAppend=<nil>
A: entryWrite + rollbackTruncate (removed=false) ErrRollbackFailed=true poisoned=false seq=1 nextAppend=<nil>
```

The writer is unpoisoned in **every** failed-rollback outcome, including the two
where the entry provably remains in the file. "Unpoisoned after a failed
rollback sync" is the general contract, not a gap in it: poisoning is reserved
for bytes that landed with no rollback attempted
(`poisonLandedBytesLocked`, `transcript.go:541-560`, reached from the buffered
door at `:508-511` and from `appendFailureLocked` at `:611`), while a failed
rollback reports `ErrRollbackFailed` for the caller to reconcile.

Three reasons the proposed fix is wrong here:

1. **It inverts the severity order.** Poisoning only `removed==true && syncErr`
   makes the *better* outcome (the entry was truncated away; only its durability
   is unconfirmed) fatal, while the *worse* ones (`removed==false`: the entry is
   still in the file and a reader will see it) stay survivable. The durable
   door's sync path never poisons at all — `transcript.go:518-523` spends the
   sequence number when `!removed` and returns, deliberately.
2. **An existing test pins the survivable behavior for the worse case.**
   `TestAppendDurable_RetainedEntryAdvancesSequenceAndFailureCount`
   (`agent/transcript/cov_s4_transcript_fault_test.go:376-390`) faults entry-Sync
   plus rollback-Truncate and requires the **next** `AppendDurable` to succeed.
   A uniform poison rule re-points it, which constraint 6 forbids; a
   non-uniform one is the inversion in (1).
3. **The reviewer's own first-choice fix already exists.** "Require durable
   reconciliation before further appends" is exactly what this PR does:
   `reconcileEnvironmentEntryAfterFailedWriteLocked` (`agent/session.go:1746-1763`)
   keys on `errors.Is(err, transcript.ErrRollbackFailed)` and calls
   `EstablishDurability()` — a real `file.Sync()` (`transcript.go:433`) — before
   reading the transcript back, answering `environmentEntryUnknown` if that
   barrier cannot be raised. It is the only consumer of `ErrRollbackFailed` in
   the tree.

On the corruption claim specifically: `AppendDurable` force-syncs, so the very
next durable append's own `Sync()` flushes the earlier truncate together with
its own bytes — there is no lasting non-durable truncated state. `w.dirty` is
also left `true` on that path (`transcript.go:514`, restored only on the
`rollbackErr == nil` branch at `:525`), so `Close` syncs it too
(`transcript.go:657-663`). The residual is a crash strictly inside that window,
where no later append reached the disk either: the file is then exactly the
pre-rollback file, which `OpenWriter`'s recovery already handles (it truncates a
partial last line; a whole line reads as a record — the same outcome the
retained-entry case accepts by design).

Coverage for this exact fault already exists:
`TestAppendDurable_SyncFailsRollbackAlsoFails` case `rollbackSyncFails`
(`cov_s4_transcript_fault_test.go:335`) asserts the `ErrRollbackFailed` contract.

No code change.

## Finding 2 — fold markers vs. environment event ordering (`agent/session_compaction.go:249-256`) — REFUTED BY REFERENCE (#1150)

This is issue **#1150**'s first interleaving, by name. The issue cites the same
two producers — `appendEnvironmentContext`'s event publication
(`agent/session.go`) and `publishFoldTransaction`
(`agent/session_compaction.go`) — and states the same divergence: the fold takes
the door, commits markers, releases, and flushes its events afterwards, so an
environment append landing in between commits after the markers and emits before
the fold's events.

It cannot be fixed on the environment side in this PR. The environment producer
already publishes under the door (`agent/session.go:1692-1704`, the deliberate
documented exception), which is what closed the *other* interleaving; closing
this one requires the **fold's flush** to take the same lock, and that flush was
built specifically not to hold it — session naming, hook user messages and the
nudge latch are unbounded work that re-enters these locks. Both producers must
take one session-scoped publication lock across write-then-announce, and the
fold half lives in #1100.

The maintainer's recorded ruling on #1150 is that it lands as its own PR after
#1100; the issue's own comment notes the general rule "is still unenforced
across producers … until there is one publication path". No code changed.

## Gates (agent module, at `c48c59751`)

| Gate | Result |
| --- | --- |
| `gofmt -l` touched files | clean (see concern 2 for three untouched files) |
| `go vet ./...` | clean |
| `go vet -tags evenerfuzz ./...` | clean |
| `GOOS=windows go vet -tags evenerfuzz ./...` | clean |
| `go test -count=1 ./...` | zero non-`ok` lines |
| `go test -race -count=3 ./transcript/...` | `ok primeradiant.com/evener/agent/transcript 7.062s` |
| `golangci-lint run --concurrency=2 GOGC=50 ./...` | `0 issues.` |

## Concerns

1. Finding 1 is a judgment call and I want it seen as one. The narrow crash
   window the reviewer describes is real; what I dispute is that poisoning is
   the right answer, because the same window exists in the outcomes this writer
   already chose to survive, and one of those is pinned by a test. If the
   maintainer wants a poison rule here it should be uniform across all four
   outcomes and land with the reconciliation contract re-examined — a design
   change, not a point fix, and not a review-round commit.
2. `gofmt -l agent/` flags three files — `delegate_tree_start.go`,
   `responses_continuation_eligibility.go`, `tool_args_fuzz_test.go`. All three
   are byte-identical to `origin/main` and `origin/main`'s own copies fail the
   same check, and the pinned `golangci-lint 2.13.1` reports 0 issues. It is a
   local toolchain difference (go1.27.0's changed indentation for multi-line
   composite literals in multi-value returns), not this PR. Left alone
   deliberately; worth a separate repo-wide pass when CI's Go moves.
3. Four consecutive rounds have produced one real bug (round 12: none) against
   five refutations. The remaining findings in this area are increasingly
   restatements of filed issues (#1150, #1165).

---

# Task 95 — PR #1098, RoboRev round 13 (head `e527df5aa`)

Status: **DONE**. Two commits on `e527df5aa`. Not pushed, main not merged.

| Finding | Disposition | Commit |
| --- | --- | --- |
| Medium 1 — direct input's `recordTurn` ignores append failures | **Refuted by reference (#1181)** | — |
| Medium 2 — commit-phase write failures leave live state compacted | **Refuted by reference (#1181)** | — |
| Low 3 — stale `envTracker` field doc / dead reset helper | **Fixed** | `41c9c418a` |
| Low 4 — poisoned fold reported as a publication race | **Fixed** | `837b405be` |

## Medium 1 — `agent/session_lifecycle.go:2124`, `agent/session.go:1878-1888` — PRE-EXISTING, #1181

The defect is real; this PR neither introduced nor widened it. `recordTurn`
(`agent/session.go:1878-1888`) appends to `s.history`, writes, warns on error
and returns nothing; the direct-input site calls it through `appendTurn`
(`agent/session.go:1579`) at `agent/session_lifecycle.go:2124`.

This PR's only change anywhere near it is a **comment-only** hunk
(`git diff origin/main...HEAD -- agent/session.go`, `@@ -1748,6 +1903,17 @@`):
eleven lines of doc added above `writeTranscript` explaining that a poisoned
writer surfaces through the ordinary doors. No code line changed.

`git blame` on the cited lines, with every commit confirmed an ancestor of
`origin/main`:

```
recordTurn body 1878-1888  b589a8d263 (2026-07-25), 4b384fe790 / 841a199c58 (2026-09-02)
appendTurn      1579-1582  56534533c9 (2026-06-01), a67cbe081f (2026-07-17)
call site       2122-2126  55dfabfd94 (2026-07-28), 31fc13ae34 (2026-08-13)
```

This is exactly issue **#1181** ("audit durable writes that publish in-memory
state before the transcript accepts them"), whose candidate list already names
this class. **Site to add to #1181**: `agent/session.go:1878-1888`
(`recordTurn` — history append then write, warn-only on failure), reached for
direct user input from `agent/session_lifecycle.go:2124` via `appendTurn`
(`agent/session.go:1579`). Ordering guarantee today: **publish-then-write,
warn-only**. Wanted: the write-first shape `appendEnvironmentContext` and
#1100's `emitHookCompleted` now use.

## Medium 2 — `agent/session_compaction.go:242-257` — PRE-EXISTING, #1181

Also real, also untouched here. This PR's whole diff to that file
(`git diff origin/main...HEAD -- agent/session_compaction.go`) is: the
pre-publication poison check at the top of `publishFoldTransaction`, the
`foldCommit.resetEnvContextTrackerLocked` wiring plus `environmentTurnIDs` /
`environmentTurnsRemoved`, and comments. The commit phase the finding cites
(242-257: `commitTranscriptsLocked`, the rewrite-tail `writeTranscriptDurableLocked`
loop, the warnings after `attentionMu` releases) is blame `adf38dddf9`,
`4b384fe790`, `841a199c58` — all 2026-09-02, all ancestors of `origin/main`.

`3a6ba6b91` (round 10) added the **pre-check only**, and said so. Failures
*during* commit are #1100's territory (`ece521c4a` withholds markers whose tail
failed), and the failed-marker publish site is already recorded on #1181. No
code changed.

## Low 3 — `envTracker` doc and the dead reset helper — FIXED (`41c9c418a`)

Confirmed: `resetEnvContextTrackerAfterCompaction` had **no production caller**
(`grep` found only the field doc, the `maybeAppendEnvironmentContext` doc and
one test). Production resets through `resetEnvContextTrackerLocked` under
`attentionMu` then `mu`, from `handleCompactionTurn`
(`agent/session_namer.go:445-453`) and `publishFoldTransaction` via
`foldCommit.resetEnvContextTrackerLocked`.

- Field doc (`agent/session.go:160-175`) now names `resetEnvContextTrackerLocked`,
  states the `attentionMu` → `mu` order, says why `mu` alone is not enough (the
  tracker advance and the entry that carries its block are one publication), and
  names both real callers.
- The `maybeAppendEnvironmentContext` doc gets the same correction.
- `resetEnvContextTrackerAfterCompaction` deleted; its "why reset at all"
  rationale moved onto `resetEnvContextTrackerLocked` so nothing is lost.
- `TestSession_MaybeAppendEnvironmentContext_NoRaceWithCompact` now drives the
  real path — `attentionMu` → `mu` → `resetEnvContextTrackerLocked` → autosave
  outside both, which is byte-for-byte what `handleCompactionTurn`'s
  CHECKPOINT/SUMMARY branch does. Calling that function outright would drag in a
  transcript write, the compaction-turn effects and the async namer; the test
  comment now records that.

Still GREEN under the race detector, which is the only mode it runs in:
`ok primeradiant.com/evener/agent 2.354s`.

## Low 4 — poisoned fold vs. lost race — FIXED (`837b405be`)

RED: `compaction error = a concurrent history change won the publication race; try again, want one wrapping transcript.ErrWriterPoisoned`
GREEN: `ok primeradiant.com/evener/agent 0.666s`

New `TestPoisonedCompactionReportsDurabilityNotARace` in
`agent/session_environment_rollback_regression_test.go` asserts `Compact` on a
poisoned transcript returns an error wrapping `transcript.ErrWriterPoisoned` and
that its text does not say "publication race".

`publishFoldTransaction` gains a third return, `refusal error`: nil for the
publication race (the retryable one), `errTranscriptRefusesRecords()` for the
poisoned refusal. `foldWithForceCompact` returns `(ok bool, refusal error)` and
**stops retrying** when the refusal is non-nil — a second attempt would spend
another summarizer call to lose identically — while keeping its retry for the
genuine race. `Compact` returns the refusal when present, else the unchanged
race message.

Two sibling call sites updated in the same commit, because they encode the same
rule and would otherwise drift:

- `agent/session_model_call.go:268` (round-loop auto-compaction) breaks its
  retry loop on a refusal instead of running a second futile `ForceCompact`; the
  round continues unfolded, as it already does when both attempts lose.
- `agent/session_self_compact.go:183` warned "a concurrent compaction published
  first" for both outcomes — the identical misattribution at a sibling site. It
  now names the real reason, so the model's next attempt at the same steering is
  not sent back into a fold the transcript will refuse again.

## Gates (agent module, at `837b405be`)

| Gate | Result |
| --- | --- |
| `gofmt -l` (6 touched files) | clean |
| `go vet ./...` | clean |
| `go vet -tags evenerfuzz ./...` | clean |
| `GOOS=windows go vet -tags evenerfuzz ./...` | clean |
| `go test -count=1 ./...` | zero non-`ok` lines |
| `-race -count=3` touched sets | `ok primeradiant.com/evener/agent 10.998s` |
| `golangci-lint --concurrency=2 GOGC=50 ./...` | `0 issues.` |

## Concerns

1. Finding 4's fix changed `publishFoldTransaction`'s signature, so all three
   `foldWithForceCompact` callers and the round-loop publish site moved with it.
   Mechanical, but it is the widest blast radius of any round-13 change and
   worth a reviewer's eye on `session_model_call.go`'s new `break`.
2. I fixed the `session_self_compact.go` warning alongside finding 4 rather than
   leaving it. It was not in the verdict, but it is the same rule encoded twice
   and splitting them is how the round-9/11 drift started.
3. Rounds 9-13 total: three real bugs fixed, two Lows fixed, seven findings
   refuted (four by reference to filed issues). The refutation rate is high
   enough that the reviewer's durability findings now mostly restate #1181.

---

# Task 99 — PR #1098, RoboRev round 14 (head `4d8e96993`)

Status: **DONE**. One commit: `800c2563d`. Not pushed, main not merged.

## High — a turn whose own input record poisoned the writer still ran — FIXED (`800c2563d`)

Confirmed, and the reviewer's framing is right: this instance sits inside this
PR's own round-4 admission rule rather than in #1181's generic class. The drain
loop refuses a turn on a poisoned writer at the TOP of each iteration
(`agent/session_lifecycle.go:1027`), which is one iteration too late for the
turn that did the poisoning.

The hole was the **direct** input branch only. The claimed branch (queued /
client-start) already went through `appendTurnAfterTranscriptWrite` and already
propagated the error with a full rollback; the direct branch called
`s.appendTurn(...)` → `recordTurn`, which appends to history, writes, warns and
returns nothing.

**rule-from-tests (required by the ruling): clear.** `grep "transcript write
failed" agent/*_test.go` returns exactly one assertion,
`agent/session_tools_jobs_covtest2_test.go:1482`, and it is the *attention
resolution* producer, not the user-input path. Nothing pins warn-and-continue
for a USER_INPUT write, so there was nothing to stop on.

Fix, three parts, all inside `acceptUserInput`:

- `appendUserInputTurnRefusingPoison` records the same history/transcript pair
  `recordTurn` makes but **writes before it publishes**, so a write that stops
  the writer leaves no history entry to take back out. A write error *plus* a
  poisoned writer returns `errors.Join(writeErr, errTranscriptRefusesRecords())`
  — the guards' shared sentinel, so `ProcessPendingUserInput`'s queued restore
  recognizes this refusal as one of its own. Any other write failure keeps
  `recordTurn`'s warn-and-continue.
- `returnAcceptedUserTurn` is the one rollback shape, extracted from the
  environment-append site (`:2057-2077`) and now used by all three refusals in
  the function: environment append, claimed append, and the new one. Not a
  second shape — the two pre-existing copies collapsed into it.
- The direct branch calls the new append and, on refusal, rolls back and returns
  `append user input: <err>`.

**Other `recordTurn` callers are untouched and stay with #1181** — attention
resolution, steering injection, checkpoint/summary records, the namer. The
change is scoped to what goes through `acceptUserInput`.

RED: `model requests = 2, want only the first turn's: the poisoned turn must not reach the model`
GREEN: `ok primeradiant.com/evener/agent 0.713s`

New `TestTurnWhoseOwnInputPoisonedTheTranscriptNeverRuns` settles the
environment block with a first turn, attaches the faulting FS, arms a partial
write so the USER_INPUT record is the one that stops, and asserts: the error
wraps `transcript.ErrWriterPoisoned`, the model was never asked a second time,
no `EventUserInput` reached clients, model history did not grow, `s.turns` is
unchanged, and the durable `AcceptedTurns` is back to its pre-input value.

Note on the RED: the pre-fix error *already* wrapped `ErrWriterPoisoned` — via
the next iteration's gate, after the turn had run. The model-request count is
what the finding is actually about, and it was 2.

## Gates (agent module, at `800c2563d`)

| Gate | Result |
| --- | --- |
| `gofmt -l` (2 touched files) | clean |
| `go vet ./...` | clean |
| `go vet -tags evenerfuzz ./...` | clean |
| `GOOS=windows go vet -tags evenerfuzz ./...` | clean |
| `go test -count=1 ./...` | zero non-`ok` lines |
| `-race -count=3` poison/claim sets | `ok primeradiant.com/evener/agent 11.778s` |
| `golangci-lint --concurrency=2 GOGC=50 ./...` | `0 issues.` |

## Concerns

1. The direct user turn now writes before it appends to history, where it used
   to append first. That is the direction `appendEnvironmentContext` and #1100's
   `emitHookCompleted` already moved, and `appendTurnAfterTranscriptWriteLocked`
   documents it, but it is a real ordering change on the hottest path in the
   file and deserves a reviewer's eye.
2. Collapsing the two rollback copies into `returnAcceptedUserTurn` changed one
   thing besides deduplication: the claimed-append site used the `pending`
   value captured earlier in the function, and the helper re-reads the snapshot.
   The claim is held by this turn across both points, so the method cannot
   change underneath — but it is the one behavioural difference in that
   extraction.
3. Still open from earlier rounds and unchanged here: #1181 (durable-write
   audit, now the home for every other `recordTurn` caller), #1150 (live vs
   cold event order), #1165 (check-then-claim window).

---

# Task 104 — PR #1098, RoboRev round 15 (head `2111513e1`)

Status: **DONE**. Two commits. Not pushed, main not merged.

| Finding | Disposition | Commit |
| --- | --- | --- |
| High 1 — rounds keep running behind a mid-input poisoning | **Fixed** | `215912545` |
| High 2 — compaction flushes after an in-commit write failure | **Refuted by reference (#1181 / #1100)** | — |
| Medium 3 — live/cold turn-number divergence after a stable turn | **Fixed** | `fb9065c2d` |

## High 1 — mid-input poisoning (`session_lifecycle.go:1961-1963`) — FIXED (`215912545`)

Real. The round-4 rule refuses at admission, but an input does not stop being
admitted once it has started: a tool-result record that lands partially stops
the writer mid-input, and every later round makes another model request and
another batch of tool executions whose results the transcript already refuses.

Fix: the loop head at `agent/session_lifecycle.go:1734` calls
`refuseTurnOnPoisonedTranscript(ctx)` when `round > 0` — the admission
refusal's own function, so the sentinel and the unwind (settle the boundary,
end the input, return the shared error) are one encoding, not a second. Round 0
is deliberately not re-checked: the drain loop's gate and this input's own
user-input record (round 14) already stand in front of it. `recordTurn`'s other
callers were not touched; they remain #1181.

RED: `model requests = 3, want 2 (the first turn and the poisoning round)`
GREEN: `ok primeradiant.com/evener/agent 0.664s`

New `TestPoisonedToolResultStopsTheInputBeforeTheNextRound`: scripts a `glob`
tool call, arms the partial write past the durable assistant record onto the
buffered tool-results one, and asserts the request count stops at 2 with an
error wrapping `transcript.ErrWriterPoisoned`.

## High 2 — compaction in-commit failures — PRE-EXISTING, #1181 / #1100

Third time; checked again that round 14 did not alter the path. `git blame` on
`agent/session_compaction.go:254-270` and `:557-564` is `adf38dddf9`,
`4b384fe790`, `841a199c58` — all 2026-09-02, all ancestors of `origin/main`.
The single line in that range belonging to this PR is `269`
(`return published, true, nil`), the return-value change from round 13's
`837b405be`; `commitTranscriptsLocked`, the rewrite-tail loop and the flush
wiring are untouched. `3a6ba6b91` added the pre-publication check only.
In-commit failures are #1100's (`ece521c4a` withholds markers whose tail
failed); the failed-write publish site is recorded on #1181.

## Medium 3 — live/cold turn numbering — REAL, FIXED (`fb9065c2d`)

Reproduced exactly, and it is **wider than the finding states**. Probe of the
three shapes (live projector vs `apptranscript.ItemTurnsFromEntries`):

```
A env(stable) + user   LIVE="turn_1"  COLD=[turn_environment_1 turn_2]   diverges
B mut(stable) + user   LIVE="turn_1"  COLD=[turn_client_1      turn_2]   diverges
C user + user          LIVE="turn_2"  COLD=[turn_1             turn_2]   agrees
```

Cold numbers by ENTRY INDEX (`persistedTurnID`,
`internal/apptranscript/apptranscript.go:778-782`) and falls back to `turn_%d`
only for entries with no durable id — so **any** stable-id entry consumes an
index. Live spent no number for either stable-id shape. Fixing only the
environment branch would have been the second encoding of the sequence the
ruling told me to avoid, so the fix is at `startTurn`
(`internal/appprojector/appwire_projection.go`): every started turn spends
exactly one number. A reservation the counter minted (`ReserveTurnID`) already
spent its own, so a new `reservedTurnIDIsStable` field marks the reservations
installed from outside the sequence (`ReserveStableTurnID`, and `openTurn`'s
stable id) and only those spend a number at start.

The producers do **not** guarantee a stable id on every user input, which is
why the refutation route was unavailable: `acceptUserInput`'s direct branch
builds `schema.NewTurn(schema.TurnUserInput, ...)` with no `StableTurnID`
(`agent/session_lifecycle.go`, the `queuedIdentity.ClientMutationID == ""`
arm), and `cmd/evener serve`'s `InputCh` path reaches it.

First attempt made `ReserveTurnID` name without spending; two existing tests
refused it and were right — `TestEnvironmentWithoutIdentityDoesNotUseRunnable
Reservation` (`internal/appprojector/environment_reservation_test.go:47`) and
`TestServerAppWireFailedDurableReplacementReleasesOwnedGenericReservation`
(`server/appwire_reasoning_replay_test.go:75`) both pin that a reservation
consumes its number immediately. Backed out; the shipped fix keeps that.

RED: `the trailing user input is "turn_1" live and "turn_2" cold` (both stable-id sub-cases; the no-stable-id control passed)
GREEN: all three sub-cases pass

New `TestTurnSequenceAgreesAcrossLiveAndColdAfterAStableTurn`
(`server/appwire_turn_sequence_parity_test.go`) is a table over all three
shapes, comparing the live projector's minted id against the cold projection of
the equivalent entries.

## Gates

| Gate | Root module | Agent module |
| --- | --- | --- |
| `gofmt -l` touched files | clean | clean |
| `go vet ./...` | clean | clean |
| `go vet -tags evenerfuzz` | clean | clean |
| `GOOS=windows go vet -tags evenerfuzz` | clean | clean |
| tests `-count=1` | `./internal/appprojector/... ./internal/apptranscript/... ./server/...` zero non-`ok` | zero non-`ok` |
| `-race -count=3` touched sets | appprojector 1.333s, apptranscript 114.430s, server 7.606s | agent 10.026s |
| `golangci-lint --concurrency=2 GOGC=50` | `0 issues.` | `0 issues.` |

## Concerns

1. **Finding 3's fix reaches pre-existing behaviour.** The client-mutation shape
   (case B) diverged before this PR; only the environment shape (case A) is new
   here. I fixed both because they are one rule and the ruling asked for one
   encoding, but that means `fb9065c2d` changes turn numbering for
   client-mutation sessions too. Every root-module test passes, including the
   two reservation tests that caught my first attempt, but this is the change in
   rounds 9-15 most worth an independent look.
2. `reservedTurnIDIsStable` is a fourth piece of reservation state alongside
   `reservedTurnID`, `activeTurnID` and `midSessionAnnouncementTurnID`. It is
   set and cleared at five sites; a reservation helper that owns all of them
   together would be better, but that is a refactor, not this round's fix.
3. High 2 has now been refuted three rounds running on the same evidence. If the
   reviewer keeps raising it, #1181 may need the specific `session_compaction.go`
   commit-phase line range recorded on it explicitly so the refutation is
   one lookup rather than a fresh blame each round.
