# Round 20 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tj8nMd5n1WuWZyWZAWc`), S gpt-6.1-sol-1m
(`dlg_034tj8p2FihIhFSWOTd0hm`), T deepseek-4.1-flash-background
(`dlg_034tj8qNzA6TyUikYbW7u4`). Reviewed at `daca7f7e4d` (the round-19
revision pass, on top of `73883f2dba` this record's predecessor) over the
production-identical base `2be411cd8f`. All three seats ran the required
reds uncached (`-count=1 -v`, sources inspected first): 4/4 red failed for
exactly the documented reasons, 4/4 characterizations passed, zero
disputed, every worktree clean, every seat's scratch removed.

## Round-19 closures — all six CLOSED for their reported schedules

- **R19-R-1** (the rollback fsync and non-final Close discharge gaps) —
  CLOSED by all three seats: no manager-visible signal keys on durability
  any more; the doors table is the writer's accounting with both failure
  faces named (`3-transcript-status-consistency.md:351-370`).
- **R19-R-2 = S19-1** (the async recorded-refresh gap) — CLOSED for the
  reported retained-return schedule. S re-proved the returned-outcome fix
  with five producer probes through the real seams (retained attention,
  retained wrapper input with pair adoption, buffered checkpoint inside
  the interval, retained resolution, retained stabilization): each real
  index named the fresh execution with `outcome-first-count=1`, and a
  second producer in the same execution did not increase the count. The
  residual exposure is a NEW cut — the pre-return window below — not the
  held id this finding removed.
- **R19-R-3 = S19-2** (the mismatch preemption) — CLOSED by withdrawal. S
  drove the missing-at-read-back branch, a real intervening
  `appendAssistantTurn`, a successful retry, and an actual conflicting
  second attention entry: the observed count stayed one throughout and the
  final retry failed `attention "attention-new" has conflicting content`.
- **T's N1/N2/N3** — CLOSED (T's own audit, verified at the hops): N1 no
  manager-visible durability key remains (the doors paragraph is writer
  accounting, `:351-374`); N2 no held id exists to clear, the fold's typed
  failure is named at `:237-240` reached by the retry's read `:585-588`;
  N3 "exactly once" now means once per execution via the fresh flip, and
  the remaining "abandon" hits are `completeTurn`/`recoverAbandoned` in an
  unrelated sense.

## New findings — 2 distinct significant, 3 minor; every load-bearing
claim verified in-tree by the chair

### S20-1 = R20-R-1 — significant, two-seat convergence, dynamically proved

**The retained synced-door pre-return window.** The round-19 emission
point — the producer's outcome handler, firing when `Record` reports
`Recorded` — is one unbounded window too late. On the synced door with the
append's own fsync failed and the rollback truncate failed, the whole line
is retained; `commitBatchLocked` (`agent/transcript/transcript.go:854-866`)
takes the ordinal, advances the recorded length and fires `onRecorded` at
`:863-864` INSIDE `appendBatch`; the server's history hook
(`server/thread_history.go:254-285`) stages the record and wakes its
projector at that moment; `recordSynced`
(`agent/transcript/record.go:74-96`) then calls `EstablishDurability` at
`:85` BEFORE returning the outcome, and `file.Sync()` has no deadline
(`transcript.go:881-883`). While the barrier blocks, the producer's
outcome handler cannot run: the live history view and the index name the
new execution, the metadata frontier holds the previous one, and no
refresh event exists. S proved it three ways — a channel gate on the
recovery `Sync` over the real ambiguous-sync fixture with the real index
printing `outcome-first-count=0` while gated; the REAL production
`handleAppThreadRead` serving the fresh execution with proposed outcome
callbacks at zero; and an explicit contract red failing only at
`recorded execution is index-visible but the proposed first-record signal
still waits for the recovery fsync`. R walked the same hops independently
(`record.go:73-95`, `transcript.go:790-797`, `:849-865`, the barrier
`:869-886`, the hook). Chair verification: every hop opened and confirmed.
This is not the ordinary event-dequeue delay — the refresh has not even
been emitted.

### T20-1 — significant, single seat, verified at every hop

**The sampler's source is unpinned, and both candidates fail.** Spec 3
says the frontier is "resolved by the SAMPLER at sample time from the
source" (`:435-438`) and elsewhere that the frontier method is "stamped at
the persist moment" (`:493-503`) — two mechanisms, neither pinned. Both
natural sources fail:

- `s.history` lags the emission: `retainDelegateAttentionTurn` at
  `agent/session_attention.go:627` runs after the `Record` at `:621`, and
  the pair helper's `appendLocked()` at `agent/session.go:2584` runs after
  `write()` at `:2580` — a dequeue in the window samples the previous
  execution, a race the spec's "no path" claim does not admit;
- `s.execution` clears at completion (`agent/session_execution.go:91`,
  before the completion entry is written) and the writer's running turn
  clears at `placeCompletion` (`agent/transcript/placement.go:104-111`),
  while `TURN_ENDED` is `facetAll` (`server/thread_envelope.go:238`) — a
  resample from either would blank the frontier every turn.

Same root as S20-1/R20-R-1: every moment the refresh hangs on sits AFTER
the commit that makes the entry visible to the served view.

### T20-2 — minor — the doors accounting still has gaps

The writer's accounting omits the two mid-session `EstablishDurability`
barriers — `agent/session_tool_round.go:377`
(`consumePersistedTerminalJobNotifications`) and
`agent/session_retirement_prepare.go:238` (retirement preparation), both
verified — and `DOORSYNCED` "proves the whole file (the barrier)" needs
its no-error qualifier: `recordSynced` returns `Recorded:true` with a
`*RetainedUnsyncedError` when both its own fsync and the barrier fail
(`record.go:83-95`). Documentation-level; nothing manager-visible keys on
them.

### T20-3 — minor — "every record site" overstates the named seam

The free resolution helper `appendDelegateAttentionResolutions`
(`agent/session_attention.go:1924-1944`, verified: a free function with no
`*Session` anywhere in its signature) cannot host a session-level outcome
handler without a signature change the spec does not name. Dissolved by
the disposition below rather than patched.

### R20-R-2 — minor, the chair's own sweep miss, owned

Spec 3 `:313-317` still defines the event as "write-then-emit after the
verified read-back", contradicting the revised contract at `:333-350`
(emission before any caller decision) and `:374-383` (verification has NO
signal interaction). The round-19 stale-phrase sweep greped for
held/discharge/owed/abandon vocabulary and missed ordering phrases. The
pass widens the sweep's pattern list mechanically; this miss is the
chair's, not the seats'.

## Judgment calls accepted

- T's restore re-arm ruling: `BeginExecution(turnID, reopen=false)` re-arms
  the flip after a restore (`agent/session_execution.go:63`, `:76-79`),
  but each process fires once and the frontier value is the same turn id —
  judged outside the manager-visible guarantee. The stamped-field design
  keeps the re-stamp idempotent; recorded, not re-litigated.
- S's admissibility ruling: a stalled filesystem `Sync` is an admissible
  schedule; the code supplies no deadline. Accepted; the journey sibling
  gates the recovery `Sync`.

## Disposition — one fix family: the commit-boundary relocation

All five findings share one root: the frontier refresh hangs on moments
AFTER the commit (the outcome return, then the sampler's source state),
while the commit itself — `commitBatchLocked`, `OnRecorded`, the history
hook, the index anchor — is the moment the served view learns the entry.
The fix keys BOTH the frontier stamp AND the refresh-event enqueue to the
commit:

1. **The emission moves to the commit.** The file's recorded-entry hook
   (`OnRecorded`, `record.go:109-118` — fired per recorded entry, in
   ordinal order, under the append lock, seeing EVERY writer's appends in
   this process) hands the committed first-entry fact to a
   session-registered leaf-lock-only listener. The first entry of the
   running execution is identifiable from the record alone: the join stamps
   `TurnKind == TurnSpanExecution` on it and consumes the fresh flip there
   (`placement.go:72-77`). The listener stamps the frontier field and
   queues the refresh event under its OWN mutex, waking the session's
   emitter — the same queue-and-wake shape `threadHistory.recorded`
   (`server/thread_history.go:254-285`) already uses at the same moment
   under the same leaf constraint (`record.go:113-118`: no appends, no lock
   an appender may hold). The woken emitter runs OUTSIDE the append lock
   and may take `s.mu`.
2. **The frontier's source is pinned.** The stamped field: written at the
   commit (the persist moment), read by samplers under `s.mu` then the
   field's own leaf lock — order stated, leaf inside `s.mu`; the listener
   never takes `s.mu`, so no inversion. `s.history` and `s.execution` are
   named NON-sources with the verified reasons. The two-moments
   contradiction dissolves: the value is FIXED at the commit and READ at
   sample time — one mechanism, both sentences rewritten to it.
3. **The refresh is in flight before the barrier.** The event is enqueued
   at the commit, before `recordSynced`'s `EstablishDurability`
   (`record.go:85`); during a blocked recovery `Sync` the bridge can
   already process the refresh — the proved window becomes ordinary
   dequeue processing, which S20-1 itself distinguishes from the finding.
   The caller's durability result, the pair helper's adoption contract,
   and content verification are unchanged.
4. **The producer-outcome machinery is withdrawn.** "The outcome handler
   at every producer" goes: the hook sees every writer (the free helper's
   records included — T20-3 dissolved; no producer signature changes), and
   the wrapper/free-helper/stabilization emission list collapses into the
   hook. Population = every recorded entry, by construction.
5. **The doors accounting completes** (T20-2) and the event definition
   loses the stale ordering phrase (R20-R-2).
6. **Journey 8 gains:** the gated-recovery-`Sync` sibling (the metadata
   frontier names the new execution while the gate holds — the
   ambiguous-sync fixture already models the two syncs distinctly,
   `agent/session_attention_test.go:4672-4677`, rollback failure forced at
   `:4680-4686` and `:4720-4726`); the `TURN_ENDED`-resample case (the
   resample keeps the completed execution's frontier because the source is
   the stamped field, not the cleared execution); and the
   dequeue-before-retain race case (the sampler reads the stamped field,
   not history).

## Readiness

- Spec 1 — READY from all three seats (eleventh consecutive round).
- Spec 2 — READY from all three seats (fifth consecutive round).
- Spec 3 — NOT READY: S20-1/R20-R-1 and T20-1 break the central refresh
  invariant until the relocation lands.
- Spec 4 — READY from all three seats; the fence stayed at zero findings.

Significant trajectory r11-r20: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2.

## Round 21

Same three seat families, new delegates: closure audits of their own
round-20 findings plus fresh attack on the commit-boundary text — the
hook's leaf contract against the emitter's wake path, the double-emission
question at the hook (two producers, the ordinal-order guarantee), the
buffered door's commit ordering, the restore re-arm's idempotence, and the
widened stale-phrase sweep re-run mechanically.
