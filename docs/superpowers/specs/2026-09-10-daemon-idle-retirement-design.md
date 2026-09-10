# Daemon idle retirement and resident-process controls

Date: 2026-09-10
Status: policy approved; written design awaiting review
Branch: `wip/daemon-idle-retirement`
Baseline: `2664cc881d128d8b0c4a0af96683126e70d13cd7`

## 1. Purpose and approved policy

Separate durable session lifetime from daemon process lifetime. Saved conversations
and resumable delegates should outlive an inactive daemon without requiring that
process to remain resident indefinitely.

Jesse approved this policy on 2026-09-10:

- Newly Hub-launched daemons automatically retire after one hour of continuous,
  proven inactivity. The duration is configurable; zero disables automatic
  retirement. Standalone `evener serve` remains opt-in.
- Work, active watches, pending deliveries, queued input, and outstanding human
  questions prevent retirement. Passive viewing and Hub probes do not reset the
  inactivity interval.
- Retirement preserves resumable sessions, idle delegates, and required worktrees.
- Hub exposes resident daemons, including archived and incompatible sessions,
  with eligibility information and explicit stop controls.
- Existing incompatible daemons require an explicit, verified stop. Age, PPID,
  an old executable, or a failed probe never authorize an automatic kill.

An unanswered question can therefore keep a daemon resident indefinitely. The
resident list makes that reason visible and provides an explicit operator action.
This release does not make questions or watches suspendable.

### Approaches considered

1. **Daemon-owned retirement, selected.** The process can inspect its whole
   runtime and retire even while Hub is absent. Requires an atomic admission
   boundary and a non-terminal teardown.
2. **Hub-owned sweeping.** Reuses discovery but depends on Hub uptime and sees
   incomplete runtime state. A status probe followed by shutdown has a race.
3. **Manual controls or opt-in only.** Lowest behavioral impact, but leaves
   automatic accumulation unchanged for default Hub usage.

## 2. Evidence and existing boundaries

The investigation found seven `evener serve` processes, including three started
September 3 with PPID 1. All three responded to AppWire v3 and rejected current v5;
one root reported idle and two awaiting. Their reported jobs were terminal. This
establishes old residency, not continuous inactivity since process startup.

Relevant source at the baseline:

| Boundary | Evidence | Design consequence |
| --- | --- | --- |
| Hub spawn | `cmd/evener-hub/spawn.go:336-397`, `spawn_detach_unix.go:12-13` | Daemons detach and survive Hub restarts; preserve that behavior. |
| Daemon lifetime | `cmd/evener/serve.go:1276-1292` | Explicit cancellation ends serving; no idle-retirement owner exists. |
| Discovery | `cmd/evener-hub/internal/hubcore/roster.go:305-312` | Failed probes do not prove exit. Dead-record GC does not retire processes. |
| Archive | `cmd/evener-hub/app_archive.go:49-70`, `internal/hubcore/tree.go:1402-1423` | Archive hides live sessions from navigation without stopping them. |
| Root state | `agent/session_state.go:42-68` | Root wire state excludes child activity and is not an eligibility predicate. |
| Mutation admission | `agent/session_client_mutation.go:244-337,459-496` | Durable acceptance and claimed-but-not-running input must block retirement. |
| Ordinary close | `agent/session_lifecycle.go:338-511`, `delegate_tree_stop.go:517-594` | Close stops durable delegate roots and disposes lanes; it is not suspension. |
| Watches | `agent/jobs.go:731-777,1434-1452` | Close drops pending sends; restore clears active watches as runtime lost. |
| Runtime reclamation | `agent/delegate_tree_reclaim.go:194-320,359-393` | Existing exact subtree claims and runtime-only release are reusable precedent. |
| Delegate restore | `agent/delegate_runtime.go:1930-2053` | Cold idle delegates reconstruct from their committed identity and descriptor. |
| Hub resume | `cmd/evener-hub/app_rpc.go:687-732`, `app_threadlifecycle.go:325-492` | Turn start already resumes eligible offline roots and retries the same mutation ID. |
| Force stop | `cmd/evener-hub/app_force_stop.go:19-185` | Verified process handles, alias ownership, deletion fences, and recovery authority already exist. |

The source review did not establish complete atomic admission coverage or selected
thread subscription recovery. Both are implementation proof obligations, not
assumed existing guarantees.

## 3. Scope and non-goals

### Deliverables

1. One daemon-owned inactivity controller, configuration, and safe retirement path.
2. Runtime eligibility with concrete blockers and a race-free retirement claim.
3. Non-terminal teardown that preserves saved root and idle-delegate state.
4. Hub resume behavior and selected-thread UI behavior verified across retirement.
5. A resident-process section in Settings → Hub with safe retirement and explicit
   force-stop actions.
6. Typed protocol contracts, generated clients, deterministic tests, operator docs,
   reviewed implementation plan, review records, and a PR.

### Non-goals

- Killing every process whose name contains `evener`, scanning unrelated users,
  or treating PPID 1 as evidence of abandonment.
- Suspending running providers, tools, shell jobs, goals, questions, or watches.
- Making archive equivalent to stop, changing history retention, or deleting data.
- Adding a legacy AppWire adapter or silently changing force-stop recovery policy.
- Retrofitting automatic retirement into already-running older binaries.
- A general checkpoint/restore service, a new process supervisor, or an additional
  dashboard outside the existing Hub settings surface.
- Changing the separate provider stream idle timeout.

## 4. Inactivity and eligibility

The retirement owner belongs to the daemon, above the mutable current root
session. Clearing or replacing the session must not detach the owner from the
new root or leave a timer targeting the old root.

Track time with the existing injectable clock pattern and monotonic durations.
Record a fresh eligible interval only after the full runtime is settled. Any
admitted state-changing operation or blocker resets the interval. Startup and
resume begin a new interval; wall-clock process age and metadata modification
age are not inputs. Disabled automatic retirement arms no expiry timer.

Read-only operations, including discovery, status, history reads, list calls,
subscriptions, and passive viewing, do not extend residency. Mutating settings,
model changes, compaction, environment changes, and user input do extend it.
Read handlers that still borrow runtime resources must finish before those
resources close, without restarting the inactivity interval.
Long-lived subscriptions receive closure rather than pinning the daemon; only
their in-flight access to runtime resources participates in the bounded drain.

Eligibility requires a persisted, reconstructible root and a consistent view of
the whole owned runtime tree. All of these must be absent:

| Blocker category | Required coverage |
| --- | --- |
| Turn execution | Provider/tool work, active turns, claimed pre-turn input, compaction, cancellation or settlement still in progress. |
| Accepted input | Durable starts, pending executions, steering, queue/drain operations, runner-channel input, and reserved mutations. |
| Autonomous work | Active autonomous goals/continuations, notifications, naming or maintenance tasks, delivery/attention retries. |
| Human decisions | Outstanding questions, sandbox escalation, and any unresolved decision callback. |
| Shell jobs | Every non-terminal session-owned shell job, including descendants; terminal records alone do not block. |
| Watches | Active event watches, one-shot or repeating timers, pending sends, claims, receipts, and settlement obligations. |
| Delegate work | Starting/running/stopping/reconstructing delegates, active claims or reservations, pending outcomes/deliveries, waiters and recovery work. |
| Environment work | Worktree creation/swap/rollback/disposal, sandbox setup/teardown and other admitted operations. |
| Persistence | Unsaved/unreconstructible state, failed required flushes, or unreadable durable evidence. |

An idle, resumable delegate is eligible only if its entire subtree is settled,
its committed descriptor and transcript are reconstructible, and the required
environment and lanes can be preserved. Include cold delegates in this check;
a missing runtime pointer is not proof of eligibility. Existing reclamation
checks should be shared where their semantics match, rather than copied or
weakened. Unsupported runtime-only state yields a blocker instead of speculative
teardown.

Diagnostics report stable blocker categories and relevant session/delegate IDs.
They must not expose tokens, prompt content, tool arguments, or other secrets.
Displayed eligibility is advisory. Every retirement action rechecks it atomically.

## 5. Admission and retirement protocol

A daemon-scoped admission boundary coordinates the runtime and the current root.
Use one small controller, not a parallel replacement for existing session or
job state machines. The implementation plan must enumerate every mutation entry
point and show how it participates, including descendant and autonomous routes.

The observable phases are `resident`, `preparing`, and `retiring`. An eligible
inactivity interval is metadata on a resident process, not a new session state.

1. Mutating operations obtain admission before their first durable or runtime
   effect. An admitted operation prevents a retirement claim until its effects
   are settled or represented by an explicit blocker.
2. Expiry or manual retirement attempts an exclusive claim. If any work or
   obligation is present, remain resident and return the blockers.
3. With new mutation admission fenced, recheck the complete predicate against
   the same current-root identity. Enter preparing, perform required persistence
   and reconstruction checks, and prepare the non-terminal release.
4. If preparation fails before teardown, release the claim, keep serving, reset
   the interval, and expose a bounded diagnostic. Do not cancel work or report
   successful retirement.
5. After preparation succeeds, commit a monotonic transition to retiring. Stop
   accepting effects, drain borrowed resources, perform non-terminal teardown,
   close transports and discovery ownership, and exit normally.
6. Never return to serving after teardown has begun. A teardown failure remains
   in the retiring phase with a failure diagnostic until exit or operator
   intervention; it does not trigger an automatic force kill or a second owner.

**Race contract:** input admitted first survives and prevents retirement. A
retirement claim acquired first rejects input before durable acceptance with a
retryable unavailable result. An accepted request whose reply is lost remains
recoverable through its original `clientMutationId`. No check-then-close gap is
allowed.

The lifecycle boundary must be outside the locks it coordinates. Do not hold
`Session.mu` across mutation-journal I/O. Do not wait for admitted work while
holding a lock that work needs. Preserve existing controller lock ordering,
reclamation fences, and close budgets. Deterministic barrier tests must establish
these properties before the timer is enabled by default.

## 6. Non-terminal teardown and preservation

Automatic and manual safe retirement share this path. Explicit shutdown,
force stop, delete, and existing close semantics remain unchanged.

Factor only the common cleanup needed by retirement and shutdown. Add an explicit
internal release policy so retirement cannot accidentally enter the durable
stop-all or lane-disposal paths. Do not duplicate the full `Session.Close`
implementation or rewrite unrelated lifecycle code.

Before exit, retirement must:

- Finish required root/child transcript, metadata, mutation, task, attention,
  delegate and job-store writes, propagating persistence failures.
- Keep each idle delegate's identity, phase, resumability, descriptor, latest
  outcome, acknowledgments, and parent linkage intact. Do not append subtree
  stop, cancelled-job, watch-drop, or disposal events solely for retirement.
- Release resident child runtimes without declaring their durable resources
  stopped. Retain the scratch and durable artifacts their established restore
  contract requires.
- Preserve occupied root and delegate worktrees, including clean worktrees and
  uncommitted edits. Release process-owned locks only through the established
  ownership mechanism; reacquire and verify lanes on restore. Retirement must
  not dispose branches or sweep foreign worktree residue.
- Close inactive MCP connections, stores, runtime handles, listeners, and other
  process-local resources in a safe order after readers have drained.
- Remove only the retiring daemon's rendezvous ownership. A stale exit cannot
  remove a replacement process's record.

Quiescence means there are no running session-owned jobs or active watches to
cancel. Detached external processes retain their existing independent lifetime.
The feature must not signal them.

Required preservation is proved by reading the original durable records and git
state before and after retirement, then restoring a fresh root and sending to the
same idle delegate. Testing only an in-memory snapshot is insufficient.

## 7. Configuration, protocol, and rollout

Add the following configuration and CLI surfaces; these names describe new work:

- Hub TOML `daemon_idle_timeout`, default `"1h"`.
- `evener serve --daemon-idle-timeout`, default zero for direct invocation.
- Shared launch configuration carries the effective timeout to both Hub spawn
  and Hub resume. Hub passes it explicitly, including zero.

Parse with existing duration conventions. Reject negative or malformed values.
Initialize Hub defaults before decoding so omitted and explicit zero stay
separate; do not apply a zero-means-default fallback to this field. Document that
changes affect subsequently spawned/resumed daemons, not running processes.
Settings shows both the Hub default and each daemon's reported effective value.

Extend typed AppWire contracts using the repository's catalog and generator:

- A Hub-scoped resident-list operation, `evener/daemon/list`, reports
  known process identities and current lifecycle diagnostics.
- A safe retirement operation, `evener/daemon/retire`, addresses the
  owning root and shares the automatic retirement claim. Hub serializes it with
  existing resume/deletion/stop ownership and forwards to the daemon.
- Existing `evener/thread/forceStop` remains the explicit destructive operation.
- Daemon diagnostics supply effective timeout, eligible-since/deadline, phase,
  and blocker categories. Hub consumes these through its existing probing path.

Responses use typed durations/timestamps consistent with the surrounding
protocol. Expose a lifecycle-unavailable reason for a retiring daemon so callers
can distinguish retry from rejection of an input's contents. Include contracts
in the method catalog, Go tests, generated TypeScript and SDK checks. Follow the
repository's protocol-version policy; do not add a legacy handshake fallback.

An older or incompatible daemon is visible but has unknown eligibility and no
safe-retirement capability. It is never presumed idle. Operators may explicitly
force-stop a verified process and then explicitly resume under the current
binary. Existing recovery fences and warnings remain effective.

## 8. Hub recovery and resident UI

### Safe recovery

Reuse the existing `turn/start` resume flow and sorted ownership locks. Retirement
must not create the explicit-resume-required fence used by force stop. A new
message after confirmed retirement resumes the same saved root and uses the same
mutation ID on retry. Concurrent clients must produce one daemon and one accepted
turn for the same mutation, without bypassing deletion or protocol fences.

A request encountering a still-retiring daemon must not spawn a replacement
while ownership remains live or unconfirmed. Within the existing request/resume
budget, observe retirement through discovery/process ownership and retry only
when safe. If exit cannot be established, return retryable unavailability rather
than changing authority. Do not resurrect a daemon for a read, probe, shutdown,
or stale background retry.

Do not generalize turn-start replay to steer, queue, or settings operations that
lack the same existing resume semantics. Report unavailable safely and retain
unsent client input. A selected session must retain its transcript and draft when
the daemon retires, then attach to the replacement source and receive subsequent
turn events. Verify this with the actual client/store/relay path and a browser
check; backend resume alone does not prove UI recovery.

### Resident-process section

Reuse Settings → Hub and existing widgets, error handling, confirmation dialogs,
and authenticated AppWire access. Keep this inventory separate from the sidebar's
archive filtering. Use the configured rendezvous discovery scope, not a new
machine-wide process scanner.

List one row per known resident process identity, not per delegate or session
alias. Include root reference/name, PID, start time, reported protocol/compatibility,
archive status, effective timeout, retirement phase/deadline and blockers. Unknown
or stale probe data stays explicitly unknown. Sorting is deterministic. Do not
label this as a complete inventory of OS processes without rendezvous records.

Controls:

- **Retire now:** skips the elapsed-time requirement, retains every safety check,
  and works even when automatic retirement is disabled. A fresh server refusal
  displays its blockers and leaves the daemon running.
- **Force stop:** uses the existing verified-identity path, requires an explicit
  confirmation naming the session/process and warning that work and watches may
  be interrupted, and preserves the explicit Resume requirement.
- **Refresh/retry:** follows existing UI patterns. Polling runs only while the
  section is mounted, reuses discovery snapshots, and never counts as activity.

Archived and incompatible rows remain visible. If process identity changes after
a row was rendered, the action must refuse the stale target rather than stop a
replacement. Do not expose rendezvous tokens or allow arbitrary-PID targeting.

## 9. Acceptance evidence

Read `docs/developing-evener/testing.md` before changing tests. Use real Evener
plumbing with a scripted provider at the LLM boundary. Default gates must remain
deterministic and independent of credentials, network, quota and ambient state.
Use an injected clock and barriers, not sleeps as race assertions.

| Scenario | Required proof |
| --- | --- |
| Default expiry | A real newly Hub-launched idle daemon exits after the configured interval; its saved root remains resumable. |
| Disabled/direct serve | Explicit zero and direct-serve defaults remain resident; positive direct configuration retires. |
| Continuous inactivity | Work before expiry resets the full interval; reads/probes/passive tabs do not. Startup/resume starts fresh. |
| Safety matrix | Each blocker in section 4 independently prevents retirement, including descendant-only work and cold delegate obligations. |
| Idle delegates | Nested idle delegates survive retirement and cold restore under the same IDs, config, tasks, sandbox and lane ownership. |
| Durable preservation | Primary transcripts, mutation journal, delegate/job records, required artifacts and git worktrees retain the promised data and lifecycle. |
| Admission races | Start, claimed pre-turn input, queue/steer, goal/maintenance, delegate restore/send and worktree operations each race the claim in both orders. No lost acknowledged work or deadlock. |
| Save/teardown failures | Failed preparation stays resident and explains why; failed committed teardown never reopens admission or authorizes a duplicate owner. |
| Resume race | Concurrent message senders produce one replacement; retrying the same mutation ID produces one accepted turn. |
| Hub restart | Timer retirement still happens with Hub absent; a restarted Hub rediscovers residency or resumes saved history. |
| Selected-thread UI | Open transcript and draft survive retirement; next message resumes, appears once, and its streamed events reach the selected view. |
| Archive/incompatibility | Both remain visible in resident inventory. Failed probes do not enable safe retirement or automatic killing. |
| Stale actions | Changed/reused PID or session ownership refuses stop; valid force stop retains explicit-resume-required behavior. |
| Scope | No changes to archive retention, explicit shutdown, deletion fences, standalone defaults, or detached external process lifetime. |

### Implementation and review sequence

After written-design approval, create a runnable TDD implementation plan with
named file ownership, exact tests, commands, expected red/green evidence and
commits. Order the work around the smallest real path: admission/eligibility,
non-terminal release and cold restore, then daemon timing/configuration and Hub
recovery, then typed resident controls and UI. Do not enable automatic retirement
until preservation and admission evidence pass.

Before implementation, assign two independent parallel adversarial reviewers.
Each competes for five points by finding the most distinct, legitimate significant
issues. False findings or artificial severity inflation disqualify that reviewer.
Adjudicate against source evidence, fix accepted findings, and repeat for at most
five rounds or until both reviewers find no unresolved significant issue. Record
findings, duplicates, decisions, scores and convergence honestly. Reaching the
round cap with unresolved significant issues is not convergence.

Then run the requested four-angle simplify-code pass on the plan: reuse,
simplification, efficiency and abstraction level. Preserve all safety and evidence
requirements. Implement with subagent-driven development and TDD, with bounded
ownership and independent specification/code-quality review per task.

Run a fresh parallel competitive adversarial loop on the implementation under the
same rules and cap. Then run the four-angle simplify-code pass on the code without
changing behavior or weakening tests. Verify simplification against the suite.

Final checks include focused lifecycle tests and race tests, generated-output
freshness, `make vet`, the canonical `make merge-approval-gate`,
`make test-api-package` for protocol changes, and `make test-web-browser` on this
Chrome-capable host. Format touched frontend `src/` files with Biome before gates.
Investigate every failure; environmental failures leave the gate incomplete.

Create a PR only after inspecting final repository state and the evidence. Include
the design/plan, operational policy, limitations, review results and test commands.
Do not modify the unrelated provider-onboarding branch, merge to main, install the
new binary, restart production Hub, or stop existing daemons as part of this PR.

## 10. Baseline and known separate finding

`make test` passed in the isolated worktree: root, agent, llm, auth, envvars,
invariant, identifier and frontend, exit zero. Earlier focused discovery/archive
checks also passed. No production daemon was stopped during investigation.

`npm audit --json` returned exit one with three moderate package reports for the
same Vitest path-traversal advisory, GHSA-82fw-gwwq-j7x9. Installed 4.1.10 is
affected; 4.1.11 contains the fix. This was reported to Jesse. Dependency remediation
is separate from lifecycle changes; do not silently alter the lockfile or present
the audit as passing.
