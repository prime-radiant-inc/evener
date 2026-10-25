# Issue #4528: non-blocking retire evidence for jobs, watches, the journal and attention

Status: frozen for PAR, 2026-10-25; revised after the two PAR reports (journal cap/coherence, race wording, helper signature, nil guard, tree cost, doc updates, tests).
Base: `dc6c93e0810857db3359983854c8510e0b4a3709` (branch `issue-4528-retire-blocking-evidence`); the final merge target is `origin/main` `013b584c4179a3062c2ad02271e39cb953032763`.

## Goal and scope

Make a manual retire that lands beside a running job, a watch, a pending job-journal notification, or pending attention refuse at once instead of waiting out the session namer's remaining `sessionNameTimeout` and then refusing (#4528, follow-up to #4055 and #4052).

#4055 made `RetirementController.awaitNaming` consult a non-blocking evidence pre-check (`namingExcludedEvidence` -> `nonBlockingEvidenceRead`) before sleeping on the namer. The pre-check keeps only obligations whose read cannot block on admitted work. This change narrows the blocking set for the four sources the issue names, following the owner ruling recorded in #4528: the pre-check must not be widened to blocking reads, but in-memory counters and bounded, lock-free reads are in scope.

Only `agent/retirement.go`, `agent/session_retirement_evidence.go`, `docs/daemon-idle-retirement.md` (the owning guide's pre-check paragraph), and new tests are in scope. No behavior outside the manual retire's wait decision and its diagnostic snapshot changes. The doc comments on `TryManualClaim`, `retirementNonBlockingEvidenceWithNaming`, `claimSnapshotCurrent` and `nonBlockingEvidenceRead` are updated in the same change: the pre-check may now do bounded filesystem I/O and never waits on an owner lock, rather than promising no I/O at all.

## The skip-wait contract

`namingExcludedEvidence` uses the pre-check for one decision only: skip the namer wait when an obligation that TryClaim will refuse over already exists. It never decides the retire; `TryClaim` alone does, and its full evidence read still runs:

- A source that cannot be read without waiting must contribute **no evidence** to the pre-check (`includeNaming=false`), so the retire keeps today's wait. Returning a blocker there would turn contention into a false positive and refuse a retire over the namer (#3921's regression).
- The diagnostic snapshot (`includeNaming=true`, `claimSnapshotCurrent`) may add a shape-honest placeholder for an unreadable source, mirroring `delegateTreeController.retirementNonBlockingEvidenceRead`'s `delegate` placeholder. A placeholder never gates a claim.
- A source read and found clear contributes nothing. An unreadable or oversized source is never treated as clear.

The skip is a single-hop optimization, not a proof about `TryClaim`. An obligation observed by the pre-check can settle before `TryClaim` runs; when the namer is still the only lease, `TryClaim` then refuses over the namer instead of waiting. #4055 accepted this window for its sources, and every source added here widens it slightly; the report names it as an accepted residual. The fix does not re-check or retry the wait, because that would add a second decision path to `TryManualClaim` for a microsecond-scale race.

## Source changes

| Source | Route | New read |
| --- | --- | --- |
| Running job (`jm.running`) | in-memory counters, `jm.mu.TryLock` | `jobManager.retirementNonBlockingEvidence` |
| Watch (`jm.watches`, `terminalFlush` pending, `stableWatchReceipts`, `stableWatchSettlementRetries`, `stableWatchSettlementRetrying`) | same read, same lock | same |
| Job journal (`jobs.jsonl` `NotifyPending`) | bounded stat + scan, no owner lock | `jobManager.retirementNonBlockingJournalPending` |
| Attention registry (`attentionMu`) | `attentionMu.TryLock`, shared predicate | `Session.retirementNonBlockingAttention` |

### Jobs and watches: `jm.mu.TryLock`

The counters live under `jm.mu`. `jm.mu` is also held across `jm.store.Load()` in `rematerializeDurablePendings` (`agent/session_jobtree_drain.go`), so a blocking `Lock` could wait on admitted durable work. The read takes `TryLock`:

- On success it reads the same counters as the full `jobManager.retirementEvidence` (running jobs, active watches and terminal-flush pendings, stable-watch receipts and settlement retries) and returns `job` / `watch` blockers.
- On failure it returns no counter evidence for the pre-check so the retire keeps waiting; the snapshot adds a `job` placeholder with this session's ID (no job ID is invented). The journal read below still runs, independent of `jm.mu`.

### Job journal: bounded, lock-free scan

`pendingJobNotifs` does **not** mirror the journal: `rematerializeDurablePendings` exists precisely because an owner `NotifyPending` record can survive only in the durable store (an interrupted restore arm, or a finalize whose in-memory enqueue never landed). Dropping the journal would therefore leave that case waiting. Route chosen: a bounded read rather than redundancy.

`retirementNonBlockingJournalPending` stats `<jm.dir>/jobs.jsonl`, requires a regular file, and treats an empty one as a genuine clear read (no scan needed). For a non-empty file no larger than `retirementNonBlockingJournalMaxBytes` (4 MiB) it runs `jobstore.ScanEventsFrom` from 0 with both `MaxBytes` and `MaxLineBytes` set to the same cap (jobstore checks `MaxBytes` only after a whole line decodes, so the line bound is what stops a concurrently grown line from buffering the 128 MiB default), stats again, and accepts the fold only when the file identity, size and modification time are unchanged and the scan reached the file end (`offset == size`), mirroring `retirementJobJournal`'s coherence check. It then folds the decoded events and looks for an owner-session `NotifyPending` record. Any stat/scan error, a limit breach, an incomplete trailing line (offset short of the size), a changed file, or a non-regular file returns false, contributing no evidence (the retire keeps waiting). The bounded read never changes the full strict reader (`retirementJobJournal`), which still decides refusal and still fails closed on lost evidence.

The cap is a deliberate residual: `jobs.jsonl` is append-only and never compacted, so a long-lived session can exceed it, and that session keeps today's namer wait for a journal-only pending. Raising the cap trades more disk work on the manual-retire path for fewer fallbacks; a full-fold-from-store shortcut is not available because `jobstore.Store.Load` takes `store.mu`, which an append holds across durable I/O. The journal contributes no snapshot placeholder: unlike a busy owner lock, an unreadable or oversized journal is fully re-read by the strict decision path, and a placeholder would have to call a clean, oversized file "unsupported".

The pre-check reads the journal only when the job/watch counter read produced no blocker yet, because one blocker is all the wait decision needs. The diagnostic snapshot (`includeNaming=true`) always scans it, so a refusal still reports every source the snapshot is meant to show.

The pre-check runs once per live Session in the shared tree. Each member pays one `stat` (and a bounded scan only when its journal is non-empty and under the cap), so a retire in a large tree pays at most one cap per live member inside the #4052 lock window. This is accepted: scanning only the root would leave a delegate member's journal source unfixed, and the aggregate remains bounded by tree membership and far below the namer wait it replaces.

### Attention: `attentionMu.TryLock`

`attentionMu` is held across durable transcript work (`rearmRootDelegateAttentionFromTranscript` folds the transcript under it), so the pre-check cannot block on it. The new read mirrors the full read's order: read `rootAttentionRailParked()` first (its client-mutation `stateMu` read-only locks are memory-only), then `TryLock` the registry.

- On success, compute the exact full-read predicate through one shared helper `retirementAttentionBlockedLocked(parked bool) bool` so the two reads cannot drift. The helper takes `parked` as an argument and must never call `rootAttentionRailParked` itself; it reads `rootAttentionPaused`/`rootAttentionWake`/`rootAttentionWakeIDs`/`rootAttentionRetry.active`/`attentionCallbacks`/`delegateAttentionArmIDs`/`delegateAttentionArmRetry.active` with `attentionMu` held. `stableAttentionRetry` is deliberately excluded because the full read does not count it.
- On failure, return no evidence for the pre-check (keep waiting) and a `notification` placeholder for the snapshot. The full evidence read is unchanged except that it now calls the shared predicate.

`retirementNonBlockingEvidenceWithNaming` guards `s.jobManager != nil` exactly as the full read does; a nil manager contributes no job, watch or journal evidence.

## Tests (red first)

New tests in `agent/retirement_manual_blocking_evidence_test.go`, all built on the existing `blockedNamerSession` fixture (a fake clock and a namer blocked in a scripted provider; no live provider). Each fails before the change by parking the retire's timer on the namer, and passes after it:

1. Running job: a real `createShell` job beside the namer refuses at once, its snapshot names `job`, and no timer is parked. `createShell` registers the run without spawning a process, so the transition settles it through `finalizeWithRunNoNotification` (no owner notification) and a second retire waits for the namer and claims after release (transition back to useful operation).
2. Watch: an active watch beside the namer refuses at once (snapshot `watch`, no parked timer). After the watch is cleared, the retire waits and claims (transition).
3. Journal: a terminal job with a journal-only `NotifyPending` record (no in-memory queue entry) refuses at once (snapshot `notification`, no parked timer). After the `NotifyDelivered` event, the retire waits and claims (transition). Direct cases pin the bounded read's fallbacks: empty journal clears, an incomplete trailing line or a file over the cap contributes no evidence (keeps waiting), and a delivered record clears.
4. Attention: a pending root attention ID beside the namer refuses at once (snapshot `notification`, no parked timer). After the registry is cleared, the retire waits and claims (transition).
5. Attention contention: with `attentionMu` held, the retire keeps waiting (timer parked); after the lock is released and the namer settles, it claims. This pins the fail-closed branch.
6. Job-state contention: with `jm.mu` held, the retire keeps waiting (timer parked); after release and namer settlement it claims. Same fail-closed branch.
7. No obligations is the existing #3921 test (`TestManualRetireWaitsForTheSessionNamer`): with an empty evidence set the retire does not skip the namer wait; it claims only after the namer releases.
8. Snapshot placeholder: a refusal taken while `attentionMu` is held names the `notification` caution without deciding the claim.

Existing #4055 tests (`agent/retirement_manual_evidence_test.go`) and #3921 tests (`agent/retirement_manual_naming_test.go`) must keep passing unchanged.

## Non-goals

- No change to `TryClaim`'s decision path, the full evidence read's contents, the strict journal reader, or the tree's full read.
- No widening of the pre-check to `retirementDelegateBlockers`, tree durable-member validation, or any other blocking read.
- No change to job/watch/attention runtime behavior, no new state maintained for retirement.
- No new labels, no deployment, no full local suite.

## Verification and review

- Red-first: run each new test against `dc6c93e081` before the implementation and record the failure that shows the namer wait.
- Focused Go tests: `go test ./agent -run 'Test.*(Retire|Retirement|Naming|Evidence)' -count=1`.
- Delegate-tree reclaim tests: `go test ./agent -run 'Test.*(Reclaim|DelegateTree)' -count=1`.
- `make build`.
- PAR: two independent competing reviewers receive this frozen spec and the baseline sources; legitimate findings are resolved before implementation. PAR then reviews the immutable implementation diff.
- simplify-code over the diff, then the repository's branch RoboRev review.
- Merge the current `origin/main` when it is not an ancestor before the final push. The briefed snapshot was `013b584c`; main had moved to `794e2c7e` (two commits) by the merge, and that head was merged. Push and open a PR; do not merge.
