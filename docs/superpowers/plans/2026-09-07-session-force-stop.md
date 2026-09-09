# Session Force Stop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Jesse explicitly stop an incompatible or unresponsive local daemon without losing saved session data.

**Architecture:** A hub-owned force-stop operation resolves one local root daemon under the existing session ownership lock. A platform boundary verifies the OS process generation and the daemon's ownership of the session API log, signals that generation, and confirms exit. The frontend asks for explicit confirmation and refreshes only after the operation succeeds.

**Tech Stack:** Go, AppWire, React/TypeScript, Linux pidfds, Darwin proc_info/audit-token signaling. No cgo or new dependencies.

**Spec:** GitHub issue https://github.com/prime-radiant-inc/evener/issues/934 and Jesse's approved design in this task.

## Global Constraints

- Keep this PR separate from #936; initially stack it on codex/running-session-regressions.
- No old-protocol compatibility and no automatic force-stop or restart.
- Refuse stale or ambiguous identity. Never signal by an unverified numeric PID.
- Preserve transcripts and API logs. Do not remove rendezvous or session files as a substitute for process exit.
- Explicitly warn that active turns and jobs may be interrupted.
- Tests use fake OS boundaries or fixture-owned processes and files; never target ambient agent processes.
- Resume and deletion must remain serialized with force-stop.

## Task 1: Verified process termination

**Files:** Create `cmd/evener-hub/internal/daemonprocess/process.go`, `process_darwin.go`, `process_linux.go`, `process_other.go`, and direct behavioral tests.

**Interfaces:** The hub supplies the rendezvous identity and canonical session data location. The package exposes:

```go
type Target struct {
    PID int
    SessionID string
    StateDir string
    StartedAt time.Time
}
type Process interface {
    Kill() error
    Wait(context.Context) error
    Close() error
}
type Controller interface { Open(Target) (Process, error) }
```

- [ ] Write tests for refusal of invalid/self PID, missing session identity, reused process generation, wrong owner, and missing API-log ownership; verify no signal occurs.
- [ ] Verify each test fails before implementation.
- [ ] Implement the platform boundary. Linux opens a pidfd before inspection and signals through that fd. Darwin reads the unique process identifier and pid version, then signals with an audit token carrying that pid version. Compare identity before and after inspecting ownership. Refuse unsupported inspection/signaling APIs.
- [ ] Verify the current user owns a `serve` process and that its writable, locked API-log descriptor identifies `StateDir/sessions/SessionID.api.jsonl`. Compare device/inode, not only a path string. Recheck ownership before signaling. Reject OS process starts after the rendezvous timestamp.
- [ ] Confirm exit using the bound process identity. An already-exited process is idempotent success; a timeout remains an error. A reused PID is never waited on or signaled as the old process.
- [ ] Run direct package tests and cross-compile the supported platform implementations; commit with the identity and failure contracts documented.

## Task 2: Hub force-stop operation

**Files:** Create `cmd/evener-hub/app_force_stop.go` and `app_force_stop_test.go`; modify `appwire/types.go`, the AppWire registration/generation inputs, `cmd/evener-hub/app_rpc.go`, and `cmd/evener-hub/internal/hubcore/config.go`.

**Interfaces:** Add a hub-only `evener/thread/forceStop` request with a local root session ref. Inject Task 1's controller through WebConfig for deterministic OS-boundary tests.

```go
type ThreadForceStopParams struct { Ref string `json:"ref"` }
// The response is EmptyResponse only after confirmed process exit.
```

- [ ] Add real hub RPC tests using real rendezvous files and a fake process controller. Cover incompatible and unresponsive roots, responsive normal shutdown remaining unchanged, already-exited targets, ambiguous/multiple owners, wrong-source refs, stale identity, signal errors, timeout, and saved transcript retention.
- [ ] Add a lock-order test: hold force-stop's exit confirmation and prove resume/deletion cannot replace/remove that session until it completes.
- [ ] Run the new tests and establish failures.
- [ ] Resolve only direct local root claims from strict rendezvous discovery. Do not infer a descendant force-stop as permission to kill its ancestor. Hold the same per-session lock used by resume/deletion across discovery, validation, signaling, and exit confirmation. Call `Open`, `Kill`, then `Wait`; always `Close` the handle. Return errors without changing ownership or saved data.
- [ ] Refresh roster/navigation after confirmed exit. Preserve a successful stop result if ancillary UI refresh fails; retain stale-discovery diagnostics.
- [ ] Register the typed method as hub-only, update required protocol contracts, run `make generate`, and verify focused RPC tests before committing.

## Task 3: Explicit user action and end-to-end verification

**Files:** Modify the session actions UI and its tests, session store method, and generated frontend protocol files.

**Interfaces:** The confirmed action calls `evener/thread/forceStop` and refreshes the session only after success. Ordinary shutdown remains separate.

- [ ] Add frontend behavior tests: no RPC before confirmation, cancel sends nothing, confirm sends one force-stop request, pending confirmation remains disabled, failures retain an actionable error, and successful exit enables explicit resume after refresh.
- [ ] Add a Force stop action for local root sessions, including restart-required sessions whose daemon capabilities are unavailable. Use the existing dialog/button conventions with the warning that active turns and jobs may be interrupted and saved transcripts retained.
- [ ] Run the tests red, implement the action, then run focused tests green.
- [ ] Run Biome on touched src files, `make test-web`, all five browser guards, and `make merge-approval-gate`. Review the whole branch against its stack base and commit verified changes.
- [ ] Create a separate PR closing #934, state the #936 dependency, request RoboRev, and address actionable findings without merging.

## Verification boundaries

Darwin does not expose a PID-specific current flock owner. The verifier combines the bound process generation, user, command and start time with a writable matching API-log inode, CLOEXEC, historical lock evidence, and current exclusive contention. This depends on Evener's API logger closing its descriptor when releasing ownership; it never unlocks and retains that descriptor. The historical flag alone does not establish ownership. Deliberate same-user process impersonation is outside this recovery contract.

Linux binds signaling and exit confirmation through a pidfd. Its process start timestamp is rounded conservatively to the end of the reported clock tick, so a rendezvous published inside that first tick can be refused. Checked arithmetic preserves that bound on long-running hosts. Both platforms refuse unavailable verification rather than fall back to numeric-PID signaling. Historical wall-clock changes can make timestamp ordering ambiguous; generation and inode/lock checks remain required independently.

Recovery must remain reachable when both navigation and initial thread hydration fail. A local session loading surface therefore offers the same confirmation as the session menus; the hub remains the authority for whether the ref has a direct daemon owner.

Serialization tests combine ownership of the actual stable/current mutexes with real resume/delete outcomes and acquisition call-site review. They do not infer scheduler timing from sleeps or runtime stack text; Go synctest does not consider mutex waits durably blocked.

Recovery first opens a verified process handle, then fences and cancels that daemon's direct RPCs before acquiring the ownership locks. An independent bounded recovery slot can admit the request while the normal WebSocket worker is stalled; ordinary mutations retain FIFO order. Cancellation remains distinct from session-unavailable so it cannot trigger automatic resume. Exact-entry reads during spawn confirmation use the persistent source's cancellation scope too. The process key excludes mutable session aliases and normalizes timestamps to UTC.

Retained rendezvous evidence is excluded from ambiguity only after verified process exit. A direct exited claim remains available for idempotent retry when every overlapping claim is verified dead; any live or unresolved competitor still prevents termination. Signaling and exit confirmation remain under the existing ownership locks, with discovery and native identity rechecked before the signal.

The hub retains an explicit-resume requirement after verified force stop. Saved and live thread reads expose that requirement without claiming protocol incompatibility; successful fresh explicit resume acknowledges it across the same recovery alias group. Session mutations carry the recovery generation captured by a pure, in-memory request-admission callback before the WebSocket FIFO queue. The queue retains the derived connection context through execution and retries, so recovery cannot turn previously queued resume or mutation requests into fresh intent. Target extraction follows each handler's supported parameters and precedence; unrelated sessions are unaffected, malformed targets retain native validation, and direct handler calls capture their generation at execution. Admission failures retain the connection's existing error/panic containment.

Force stop uses one bounded, short-lived browser recovery connection owned by the primary client. It reuses the endpoint, authentication and strict handshake, sends the stop once, and closes on completion, failure, timeout or owner teardown. This is necessary when the primary connection's normal 64-entry FIFO is full and its receive loop is applying backpressure; the independent server recovery slot alone cannot admit an unread frame. Ordinary queue semantics remain unchanged. Once verified signaling succeeds, the hub retains the explicit-resume requirement even if the connection drops or exit confirmation times out. That requirement records the user's termination intent; the RPC still reports unconfirmed exit as failure.

The WebSocket connection captures a recovery sequence before its HTTP upgrade, before any request frame is accepted. Each affected alias records the latest sequence at both recovery start and completion, invalidating unread socket backlog and connections created during exit confirmation. Action handlers compare that immutable connection sequence as well as request admission epochs; neither another initialize nor explicit resume on an old connection can advance it. This restriction is per affected alias: reads and unrelated-session actions remain available. A stale connection's readable snapshot continues to advertise Resume even after another client acknowledged recovery. Pressing the existing Resume button replaces the primary transport through normal reconnect lifecycle, rejects its old pending calls without replay, and only then sends explicit resume. New connections still cannot automatically clear the hub's explicit-resume requirement. The live hub AppWire endpoint is WebSocket-only; direct handler calls retain their execution-time admission checks.

## Durable recovery authority

Jesse approved extending the explicit-Resume requirement across hub restarts.
Store a versioned recovery snapshot under the same HubStateRoot as hub.lock.
Persist verified alias-group obligations before signaling or reporting an
already-exited stop successful. Restore them before request admission; corrupt
or unsupported authority prevents production startup. An embedded server with
an explicit empty state root remains process-local.

Persist recovery obligations, overlap-safe group identity, and the verified
current session ID needed to resume after rendezvous cleanup. Keep request
epochs, connection sequences, active stop counts and ownership locks in memory.
Serialize atomic snapshot writes separately from admission reads. A completed
explicit Resume clears only the applicable generation, and persistence failure
must remain visible. After committed intent, interrupted or failed signaling
conservatively retains the requirement rather than guessing whether stop took
place. No old-protocol compatibility, automatic Resume or input replay is added.

Validate real hub recreation, automatic-action rejection, explicit Resume and a
second recreation after clearing; signal/write ordering; already-exited and
failed-confirmation paths; malformed authority; before/after-rename failures;
overlapping alias groups and admission responsiveness during held writes.


## Recovery identity through marker cleanup

The durable snapshot must identify the current transcript as well as its alias
group. Dead rendezvous markers can expire during ordinary roster refresh, so
marker restoration cannot be a prerequisite for normal explicit Resume. Use a
new strict snapshot version with the verified current session ID committed
before signaling. Unsupported older snapshots fail startup; no compatibility
path is introduced.

Resolve alias ownership under the same sorted reservations used by force stop.
Distinguish verified dead markers from live or unresolved claims, and use the
durable target when stopped markers no longer exist. Never derive a current
session by sorting aliases. Fresh explicit Resume must also attach the pane to
the returned current thread identity. Preserve uncertain messages under their
original identity without replaying them to a replacement session.

Acceptance includes expired-marker force stop, recreated hub recovery, retained
dead A alongside live cleared B, concurrent stable/current Resume, and the real
client/store/pane transition to the resumed transcript and follow-up send target.


## Descendant admission after an uncertain stop

Commit unconfirmed exit state with the owner recovery authority before signaling.
Only a verified exit followed by a successful durable confirmation write permits
fresh descendant actions. Check journal-verified ancestry even on fresh
connections while any owner remains stopping or unconfirmed. This also covers
delegates committed after a failed wait and restored recovery after hub restart.
Keep descendants out of the owner's alias group: confirmed termination permits
independent descendant Resume, while the owner still requires its own explicit
Resume. The strict recovery snapshot version is 3; unsupported versions fail
startup without a compatibility path.

Regression coverage includes signal failure, failed exit confirmation, late
children, hub recreation, confirmation-write failures, and confirmed retry.
