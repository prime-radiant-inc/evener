# Task 3 Report: Reconcile Bootstrap and Cold Reads

## Status

Complete. Task 3 production behavior and its single restore/cold-read behavior group are implemented. No Task 2 transition state machine or Task 4 frontend code was changed.

## Base / Head

- Base: `40c655373ff389038c501ccfb5ecd75ddba278a7`
- Head before Task 3 commit: `40c655373ff389038c501ccfb5ecd75ddba278a7`
- Deliverable commit: `fix(delegate): reconcile attention on restore` (final hash is reported in the handoff; a commit cannot embed its own hash)

## Exact paths

- `agent/delegate_tree_attention.go`
- `agent/delegate_tree_restore.go`
- `agent/delegate_runtime.go`
- `agent/status.go`
- `agent/jobs_activity_past.go`
- `agent/status_test.go`
- `agent/jobs_activity_past_test.go`
- `.superpowers/sdd/2026-08-20-stable-delegate-attention-projection/task-3-report.md`

## RED

Command:

```bash
go test ./agent -run '^(TestStableDelegateAttention_RestoreAndColdRead)$' -count=1
```

Result: failed as intended. The clean behavioral RED showed:

- stale false stayed false despite unresolved transcript attention;
- stale true stayed true after transcript attention was resolved/absent;
- a permanently fenced delegate retained stale true;
- eligible missing and malformed transcripts were accepted;
- the owed-generation row exposed the stale journal boolean on its cold snapshot.

The first invocation had missing test imports; those were corrected before recording the behavioral RED above.

## GREEN

- Focused required command: PASS.
- Related restore/cold-read groups, including the existing owed-generation restart group: PASS.
- `go test ./agent -count=1`: PASS (`67.328s` on the final run).
- `go vet ./agent`: PASS.
- Targeted Hub cold-activity consumers: PASS.
- `git diff --check`: PASS.

A broader `go test ./... -count=1` did not pass as a gate: root install-script tests could not create Darwin `mktemp` directories outside the sandbox (`Operation not permitted`). That same run initially exposed Hub fixtures without child transcripts; activity-list compatibility was retained and the targeted Hub jobs-list tests then passed. A subsequent full Hub package run had one separate cold-thread fixture with child metadata but no eligible child transcript; strict `LoadSessionDelegateStatus` now rejects that fixture as required by this task.

## Bootstrap ordering

The final sequence is:

1. stable watch repair;
2. existing stop/resource reconciliation;
3. stable shell-attention repair;
4. missing restore-input cleanup;
5. permanently unreachable attention transfer;
6. committed delivery replay drains at the existing initialization boundary;
7. Task 2 owed-generation admission scans strict eligible transcript folds and admits each exact missing `RunStarted`;
8. ordinary remaining boolean mismatches are appended together through the controller's existing batch path;
9. only after that batch succeeds, the controller replaces its process-local wake map with the exact transcript-unresolved ID sets.

The admission/repair boundary is bootstrap-only: controller-only delivery-pump harnesses without a persisted `SessionConfig.StateDir` do not run restore admission.

## Cold-read behavior

`LoadSessionDelegateStatus` requests a strict replacement-snapshot overlay. It applies the same lifecycle/ancestor eligibility predicate, reads only eligible child transcripts, derives `NeedsAttention` from the existing transcript fold, and errors for an eligible missing or unreadable transcript. It never writes the delegate journal, so the stored field and revision remain unchanged.

The shared historical activity fold overlays the same transcript-derived boolean and eligibility. Its non-status activity caller retains historical missing-as-empty compatibility; the strict status caller and bootstrap final boundary use the existing-transcript check.

Ineligible delegates (closed, non-resumable, stopping, pending-stop, or permanently ancestor-fenced) normalize false without transcript I/O.

## Test count changes

- Agent top-level tests: `3318 -> 3319` (`+1`).
- New top-level group: `TestStableDelegateAttention_RestoreAndColdRead`.
- Table rows in that group: `10`.
- No new test file, package helper, framework, script, meta-test, matrix, cache, worker, or migration.

## Self-review / concerns

- The production diff is limited to the seven brief-named agent paths; the report is the only additional deliverable.
- Ordinary bootstrap repairs are deterministic by delegate ID and commit as one batch.
- Local unresolved IDs are not published on transcript-read or journal-append failure.
- Owed-generation admission remains the Task 2 reservation/`CommitStart` path; Task 3 only places final reconciliation after it.
- Concern: `cmd/evener-hub`'s `TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus` still builds an eligible child descriptor and metadata without its transcript. The new strict status contract correctly returns a missing-transcript error, so that non-brief fixture now needs a valid child transcript in its owning task. It was not modified because Task 3 is constrained to brief-named paths.
- Scratch directory: `/private/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-sandbox-1598484732`; no scratch artifacts need retention.

## Authorized Hub fixture follow-up

The parent reproduced the concern above and authorized one additional existing
test path: `cmd/evener-hub/app_threadread_test.go`. The cold/reconnected parity
fixture now creates and closes a valid `child-cold.transcript.jsonl` with the
existing `transcript.NewWriter` pattern and matching child, parent, profile, and
model header fields. Production strictness is unchanged; no test/helper was
added, and no other Hub fixture changed.

Follow-up verification:

- `go test ./cmd/evener-hub -run '^(TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus)$' -count=1`: PASS.
- `go test ./cmd/evener-hub -count=1`: PASS (`33.799s`).
- `go test ./agent -run '^(TestStableDelegateAttention_RestoreAndColdRead)$' -count=1`: PASS.
- `git diff --check`: PASS.

The earlier Hub-fixture concern is resolved. The separate follow-up commit hash
is reported in the final handoff because a commit cannot embed its own hash.

## Fix round 1/5: defer recovered owed launches

Review identified a HIGH bootstrap race: recovered owed generations launched
before the final transcript fold/repair/publication and could open attention that
the stale final wake-map replacement then erased.

The runtime boundary now separates owed admission from launch. It commits each
exact owed `RunStarted`, reconstructs and attaches the runtime, and retains a
deferred launch record. Bootstrap then performs the final strict transcript fold,
deterministic repair batch, and exact unresolved-ID publication. Only after that
succeeds are the deferred generations launched.

If admission or final reconciliation fails, every already committed deferred
start goes through the existing `FailCommittedRestart` and mutation-plan cleanup.
Its exact attached runtime is then detached, removed from the owner, and discarded.
If the failure journal append itself fails, durable recovery state remains while
the unlaunched runtime pointer is still severed so the failed bootstrap leaks no
candidate run.

The existing `TestStableDelegateAttention_RestoreAndColdRead` table gained one
row, not a new top-level test. Using the existing reconstruction hook and scripted
provider boundary, it seeds a stale witness delegate plus an owed generation. The
behavioral RED showed the recovered launch observing the witness at
`NeedsAttention=false` with no published ID. GREEN proves launch observes the
final repaired witness and that attention opened by the released generation
afterward remains true and present in the exact wake map.

Fix-round verification:

- Focused Task 3 behavior group: PASS.
- `go test ./agent -count=1`: PASS (`69.256s`).
- `go test ./cmd/evener-hub -count=1`: PASS (`37.590s`).
- `go vet ./agent ./cmd/evener-hub`: PASS.
- `gofmt` check and `git diff --check`: PASS.

The separate fix-round commit hash is reported in the final handoff because a
commit cannot embed its own hash.

## Fix round 2/5: defer recursive cold-owner restore effects

Review found a second HIGH bootstrap path: reconstructing a cold non-root owner
for an owed child immediately ran that owner's deferred restore side effects. A
durable terminal notification could arm and drive the owner before final
attention publication, recreating the stale-fold race and invalidating the owed
child before the round-1 launch boundary.

Owed bootstrap now carries one bootstrap-local restore collection through every
recursive cold-owner reconstruction. Newly restored runtimes attach normally,
but their `runDeferredRestoreSideEffects` calls are recorded rather than run.
After all exact owed admissions, bootstrap performs the final strict fold,
deterministic repair batch, and exact wake-map publication. It then releases
cold-owner effects parent-first. For each owed candidate it validates the exact
generation/binding/runtime, releases that candidate's own deferred restore
effects, validates again at the immediate prelaunch boundary, and launches only
if the same ready attention binding still exists.

A released side effect that stops or settles a candidate therefore produces no
stale launch. The invalid candidate is settled through the existing committed
restart failure path when needed, or its already-severed runtime is removed and
discarded while durable recovery evidence remains. Admission, fold, restore
effect, invalidation, and launch failures clean all remaining unlaunched owed
candidates plus recursively restored idle owners.

The existing behavior table gained one nested row. It creates a nested owed
child, a cold parent with a durable pending terminal notification, and a stale
reconciliation witness. Its synchronized notify callback both checks the witness
and stops the parent subtree. RED proved the parent side effects ran while the
witness was still false/unpublished. GREEN proves publication happens first and
that the stopped owed child reaches generation 1 without an open run, binding,
runtime, provider request, or stale launch.

Round-2 verification:

- Focused Task 3 group (including lifecycle-inventory integration): PASS.
- `go test ./agent -count=1`: PASS (`70.698s`).
- `go test ./cmd/evener-hub -count=1`: PASS (`35.358s`).
- `go vet ./agent ./cmd/evener-hub`: PASS.
- `gofmt` check and `git diff --check`: PASS.

The separate round-2 commit hash is reported in the final handoff because a
commit cannot embed its own hash.
