# Task 5 report: dormant subtree stop and restart repair

Date: 2026-08-13
Branch: `wip/delegate-resource-recovery-design`
Implementation commit: `846b7c8c2198a4bed80bdf1ca2e0825494ae6a34`
Merge base with `origin/main`: `0d9b0fb231efdfdb59d32845f79c2ab8bf229794`

## Result

Task 5 is implemented and remains dormant. The controller now owns exact shell
work receipts, one globally serialized durable subtree stop, receiver-aware
delivery receipts, evidence-versioned restart reconciliation, pure shell-store
repair, and root-close fencing. No registered Session or tool route constructs
or invokes the controller. Task 6 remains the sole active flag-day cutover.

The implementation uses one `c.mu`, the existing one durable delegate fold, and
exactly one process-local `c.stop`. A stop is identified only by the assigned
sequence of its durable `delegate_subtree_stop_requested` event. A same-target
retry joins that sequence and re-emits the exact idempotent cancellation plan;
every other target is busy until completion. Cancellation plans are stable and
leaf-first. The request append precedes every cancellation plan, including an
idle stop, and append failure exposes no cancellation or mutation plan.

Starting reservations, runtime leases, shell work tokens plus job IDs, and
delivery tokens are the process evidence that holds a stop open. Admission and
completion serialize under `c.mu`; construction, runtime, and shell cancellation
execute after unlock. Covered delivery receipts admitted before the request
drain before completion. New intersecting receipts cannot enter. The existing
durable fold discards covered-owner packets only at `stop_completed`, retains
root or outside-owner packets, and only then releases retained heads in their
existing strict sender order.

`ReconcileRequirements` snapshots exact jobs paths, transcript references, and
a process-only `evidenceVersion`. Collection reads each source jobs ledger
before its receiver transcript, uses the missing-file-tolerant read-only
`jobstore.ReadEvents` path, and never creates a missing transcript. Reconcile
rejects stale evidence and performs no filesystem, transcript, provider,
process, hook, wait, or notification I/O while locked. The only locked durable
I/O is the pre-approved controller store append/fold boundary; the previously
approved narrow steering/input transcript boundaries remain unchanged.

Restart repair follows durable run-start order. Running generations become
failed/runtime_lost unless covered by the pending stop; settling generations
finish from their already prepared packet; a stopping generation with
`CurrentRunOpen=false` is not finished or released twice. Process-local delivery
receipts disappear on restart while durable heads remain ordered. Pending stops
are reconstructed from their request sequence and exact folded members.

Shell repair opens only the exact existing regular jobs ledger after unlock,
re-folds it, and applies shell-only reconciliation. It validates job ID and
terminal generation, appends runtime_lost once, clears matching terminal
watches, consumes covered pending notifications atomically, preserves ordinary
pending notification outside a stop, and is idempotent after reopen. Any repair
or controller completion append failure leaves the stop pending.

Root close sets `closing` before releasing the lock, rejects all new work and
delivery receipts, reproduces and executes the one pending stop plan if needed,
then drains root subtrees sequentially through the same stop algebra. Exact
child runtimes are closed postorder only after durable stop completion. A
closure failure returns before runtime or worktree teardown. Resumability closes
only through its monotonic durable event and publishes no destruction plan when
that append fails.

## Genuine behavioral RED and GREEN evidence

Compile failures from introducing the new test names and minimal scaffolds were
observed but are not behavioral REDs and are not counted.

### Shell receipt group: 4 RED, 4 GREEN

After compile scaffolding, this exact selector reached real behavior and all
four tests failed because `BeginShellWork` returned `target_busy`:

```text
go test ./agent -run '^TestDelegateController(ShellReceiptHoldsStopOpen|CommitShellWorkAfterStopCancelsImmediately|ShellFinishRequiresTokenAndJobID|AbortShellReceiptReleasesOnce)$' -count=1
exit 1
four behavioral failures
```

After implementing exact token ownership, commit conversion, stop fencing, and
once-only finish/abort release, the same selector passed:

```text
exit 0
ok primeradiant.com/serf/agent 0.568s
```

### Serialized stop group: 1 RED, 1 GREEN

The broad stop matrix was GREEN on its first execution and is therefore not
misrepresented as TDD RED evidence. Subsequent interleaving review found that
map iteration made cancellation order non-deterministic. The deterministic
depth-three behavior test proved the defect:

```text
go test ./agent -run '^TestDelegateControllerStopCancellationPlanIsLeafFirst$' -count=1
exit 1
cancellation order = [dlg_parent dlg_child dlg_grandchild]
want leaf-first [dlg_grandchild dlg_child dlg_parent]
```

After stable depth and token ordering, the exact selector passed:

```text
exit 0
ok primeradiant.com/serf/agent 0.552s
```

### Pure shell repair group: 4 RED, 4 GREEN

The initial exact shell-repair selector produced three behavioral failures:
runtime loss was not appended, outside-stop notification was not armed, and a
directory repair target did not fail. The initial idempotency assertion was too
weak and passed vacuously; it was strengthened before implementation to require
the first repair to append durable events, producing the fourth causal RED:

```text
go test ./agent -run '^TestDelegateShellRepair' -count=1
exit 1
three behavioral failures

go test ./agent -run '^TestDelegateShellRepairIsIdempotentAfterReopen$' -count=1
exit 1
first repair appended no durable events
```

After implementing exact shell-only reconciliation, notification settlement,
watch clearing, regular-file validation, and reopen idempotency:

```text
go test ./agent -run '^TestDelegateShellRepair' -count=1
exit 0
ok primeradiant.com/serf/agent 0.570s
```

### Restart group: 7 RED, 7 GREEN

The first restart selector produced seven real behavioral failures: eager open
reconciliation, premature prepared-terminal finish, missing pending-stop
reconstruction, absent cold attention evidence, early/missing external delivery
release, covered delivery surviving stop completion, and restart identity
collision behavior. Those seven are counted. One additional descendant-shell
attempt failed while creating its fixture directory before it reached
production behavior; it is explicitly excluded.

```text
go test ./agent -run '^TestDelegateController(Restart|Reconcile)' -count=1
exit 1
seven behavioral failures plus one excluded fixture-setup failure
```

After evidence-driven open, durable pending-stop reconstruction, sequence-order
run repair, raw read-only attention folding, and post-completion delivery
release, the exact selector passed:

```text
exit 0
ok primeradiant.com/serf/agent 0.932s
```

The Task 2-4 restart tests were amended only to call the newly required
evidence-driven `Reconcile` boundary after `openDelegateTreeController`. Their
expected outcomes and packets were not weakened.

### Root-close group: 1 RED, 1 GREEN

The root-close join test proved the initial implementation manufactured a
second sequential stop for a root already covered by the joined operation:

```text
go test ./agent -run '^TestDelegateControllerRootClose' -count=1
exit 1
stop request count = 2, want 1
```

After preserving joined membership across the teardown pass:

```text
exit 0
ok primeradiant.com/serf/agent 0.543s
```

Total genuine causal behavioral evidence: **17 RED, 17 GREEN**. Compile
failures, the descendant-shell fixture setup failure, first-pass-GREEN tests,
and expected prior-test interface adaptations are not counted.

## Fault and interleaving coverage

The deterministic tests cover:

- stop request append failure publishing no cancellation or update plan;
- stop completion append failure retaining the fence, channel, and retry state;
- resumability closure append failure publishing no teardown plan;
- exact shell token plus job ID matching and once-only release;
- commit-after-stop returning immediate cancellation without invoking it locked;
- same-target retry preserving request sequence, channel, and cancellation plan;
- different, covering, and intersecting stop requests returning busy;
- final evidence-version rejection after receipt or attention mutation;
- pre-stop delivery receipt drain and stop-before-delivery admission fencing;
- covered-owner suppression and root/outside-owner deferral until completion;
- three-level provider-free restart, prepared-terminal once-only repair, and no
  double finish after a stopped generation is already closed;
- descendant shell repair and cold attention cleanup with no runtime;
- shell repair append failure leaving the controller stop pending; and
- root close joining the current stop and preserving the global closing fence.

Self-review specifically traced request-append versus cancellation, reservation
commit versus stop, shell commit/report versus stop, delivery begin/complete
versus stop, evidence collection versus every relevant process mutation,
restart run closure versus delivery release, and stop completion versus external
owner packets. No second stop state, wildcard generation, shell mirror,
job-based delegate authority, `already_idle` bypass, provider construction,
child Session construction, schema extension, active wiring, or compatibility
path was introduced.

## Files and necessary prior-interface amendments

The planned Task 5 package files and manifest are:

```text
agent/delegate_tree_work.go
agent/delegate_tree_stop.go
agent/delegate_tree_restore.go
agent/delegate_shell_repair.go
agent/delegate_delivery.go
agent/delegate_tree_work_test.go
agent/delegate_tree_stop_test.go
agent/delegate_tree_restore_test.go
agent/delegate_shell_repair_test.go
agent/delegate_delivery_test.go
agent/delegate_tree_restart_fuzz_test.go
scripts/run-fuzz.sh
```

The genuine Task 2-4 interface/invariant amendments are:

```text
agent/delegate_tree_controller.go
  add Task 5 work/stop/evidence/closing state; replace eager open repair with
  pending-stop reconstruction and explicit evidence-driven reconciliation

agent/delegate_tree_start.go
  reject closing admission, version reservations/bindings, drain stop starts,
  and invoke construction/runtime cancellation only after unlock

agent/delegate_tree_steer.go
  version pending-steer process evidence

agent/delegate_tree_finish.go
  drain exact active stop leases, version finish evidence, and cancel after unlock

agent/delegate_tree_start_test.go
agent/delegate_tree_finish_test.go
  preserve prior outcomes while invoking the new external-evidence boundary
```

No active Session/tool file, schema file, progress/Kata ledger, provider path,
or abandoned delegate-identity branch was inspected or changed.

## Commits and gates

The implementation was explicitly staged by path, never with `git add -A`, and
committed through repository hooks as:

```text
846b7c8c2198a4bed80bdf1ca2e0825494ae6a34
feat: add delegate subtree stop and restart repair
```

The complete prescribed Task 5 lease on that commit was:

```text
gofmt -w agent/delegate_tree_work.go agent/delegate_tree_stop.go agent/delegate_tree_restore.go agent/delegate_shell_repair.go agent/delegate_delivery.go agent/delegate_tree_work_test.go agent/delegate_tree_stop_test.go agent/delegate_tree_restore_test.go agent/delegate_shell_repair_test.go agent/delegate_delivery_test.go agent/delegate_tree_restart_fuzz_test.go
exit 0

go test ./agent -run '^TestDelegate(Controller|ShellRepair)' -count=20
exit 0
ok primeradiant.com/serf/agent 46.981s

go test -race ./agent -run '^TestDelegate(Controller|ShellRepair)' -count=20
exit 0
ok primeradiant.com/serf/agent 61.284s

SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzDelegateRestartEquivalence$' -count=1
exit 0
ok primeradiant.com/serf/agent 0.596s

make fuzz-registry-check
exit 0
registry includes native agent FuzzDelegateRestartEquivalence with the exact four-file source manifest

git diff --check
exit 0

git status --short
exit 0
<empty>
```

The final selector includes `TestDelegateControllerRemainsDormant` twenty times;
the semantic inventory remained exact. The old production route is still the
only registered route.

## Source-control and environment evidence

Before implementation, all required preflight values matched exactly:

```text
pwd
/Users/jesse/prime-radiant/toil-suite/serf/.worktrees/delegate-resource-recovery-design

git branch --show-current
wip/delegate-resource-recovery-design

git rev-parse HEAD
4dee12c47499f8f098c1aec6b3ed4a0860d9139d

git merge-base HEAD origin/main
0d9b0fb231efdfdb59d32845f79c2ab8bf229794

git status --porcelain=v1
<empty>
```

At the signed implementation checkpoint:

```text
git branch --show-current
wip/delegate-resource-recovery-design

git rev-parse HEAD
846b7c8c2198a4bed80bdf1ca2e0825494ae6a34

git merge-base HEAD origin/main
0d9b0fb231efdfdb59d32845f79c2ab8bf229794

git status --porcelain=v1
<empty>
```

The required first command `true` exited 0. The exact string
`Too many open files (os error 24)` did not occur in that command or any later
process. No retries were needed for resource exhaustion.

Signed: Bot, 2026-08-13, America/Los_Angeles
