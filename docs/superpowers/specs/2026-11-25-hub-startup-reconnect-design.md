# Hub Startup Auto-Reconnect Design

## Intent and Why

When `evener hub` starts after a restart, upgrade, crash, or reboot, it should automatically reconnect to every remote hub that was connected during its last run, without requiring the operator to click Connect on each host again. Configured `[[hosts]]` entries that were never connected remain dormant.

Keeping never-connected hosts dormant also preserves a clean boundary for future one-shot remote hosts (such as `exe.dev` or `sprites.dev`) that should not be started automatically unless the operator explicitly starts them. That one-shot host policy is not designed yet and stays out of scope here, behind a single eligibility hook.

## Goals, Non-Goals, and Anti-Goals

### Goals

- Persist the set of remote hosts that have reached `attached` state in durable hub state under `hub_state_root`, surviving clean shutdown, crashes, transient mid-run link drops, and back-to-back hub restarts while a remote host is temporarily unreachable.
- On hub startup, automatically start non-blocking background reconnect loops for every eligible remembered host, using explicit-Connect semantics (`ensureOnce(..., explicit=true)`) so compatible running hubs attach unchanged and stopped compatible hubs can be started (with existing deploy/upgrade rules when required and permitted).
- Retry transient failures with `sshconn`'s existing bounded exponential backoff (500ms to 30s with jitter), keeping `midAttach: true` across retries and handing off to normal mid-run supervision (`explicit=false`) once attached.
- Stop startup reconnect cleanly on terminal attach failures (`EventFailed`), host removal (`evener/host/remove`), host edits (`evener/host/update`), explicit `DetachHost`, or hub shutdown (`Manager.Close`).
- Bind remembered entries to each host's registration identity `(name, generation, incarnation_id)` when host records are healthy, clear entries on remove and update, and skip hosts fenced by an open teardown remnant.
- Expose one narrow eligibility hook (`shouldAutoReconnect(host hostreg.Host) bool`) so future per-host policies (such as one-shot hosts) plug into one place.
- Update evergreen product documentation (`docs/product/subsystems.md` and `docs/evener-hub-remote-operations.md`) in the same change.

### Non-Goals

- Adding an explicit `evener/host/detach` or Disconnect RPC or UI control (none exists today; tracked separately).
- Adding one-shot host configuration flags or `hub.toml` knobs before that feature is designed.
- Changing mid-run link-drop supervision (`supervise` stays `explicit=false`, so mid-run drops keep their rule of never starting a stopped remote hub).
- Implementing `friction.md` H04 (retaining first-time Connect intent when initial attach fails before ever attaching) or H12 (dormant host notice suppression).
- Changing the AppWire wire protocol or browser/native client code. Existing `midAttach`, `attached`, `lastAttachError`, navigation invalidation, remote-thread cache refresh, and remote admin notification fan-out already react to `sshconn` lifecycle events.

### Anti-Goals

- Never block hub startup or local session serving on SSH dials.
- Never perform disk I/O or call back into `sshconn.Manager` from `sshconn.Options.OnEvent` while the per-host lock is held.
- Never introduce a parallel reconnect loop outside `sshconn.Manager`'s existing supervisor ownership.
- Never mutate `hub.toml` or couple runtime attachment tracking to `hostops.Store`'s staged-commit and quarantine machinery.
- Never auto-reconnect a host that was never connected, was removed and re-added under the same name, was edited via `evener/host/update` without reconnecting, or is fenced by an open teardown remnant.
- Never abort hub startup because the attached-hosts state file is missing or corrupt.

## Constraints

- **Reconnect ownership**: Reuse `sshconn.Manager`'s `supervisors` map, `supervisorsWG`, `hostLockGate`, and `stopSupervisorsBefore` teardown. Retire a startup supervisor only after a replacement attachment commits, so a failed manual `Ensure` retains recovery.
- **Lock ordering**: `sshconn.Options.OnEvent` executes synchronously under the per-host lock. Attachment persistence must happen outside that lock.
- **Shutdown ordering**: `runMain` unwinds in reverse startup order (`retireEndpoint` -> `stopBackground` -> `closeWeb` -> `closeHosts` -> `closeListener` -> `release` lock). Any background persistence writer must drain and flush after `sshManager.Close()` completes (so late attaches before shutdown are durable and no goroutines leak) and before the hub lock is released.
- **On-disk state compatibility**: Follow `docs/product/README.md#compatibility` and `hubcore/state_file.go` (`writeStateFileAtomic`, `readStateFile`, `decodeStateFileStrict`) with a versioned JSON schema (`version: 1`). Leave corrupt files untouched on disk.
- **Hermetic tests**: Existing `runMain` tests use fresh temp `HubStateRoot` directories with no persisted attached hosts and must remain completely offline.
- **Size budget**: Estimated 350 to 450 production lines across `hubcore`, `sshconn`, and `cmd/evener-hub`, plus focused unit and multi-boot integration tests.

## Approaches Considered

1. **Dedicated `hubcore` attachment store + `sshconn.Manager` startup reconnect (Selected by Jesse)**
   - Persist connected host identities in `<HubStateRoot>/attached-hosts/state.json` via a new `hubcore.AttachedHostsStore` reusing `hubcore/state_file.go`, written through a serialized non-blocking persister flushed on shutdown.
   - Add `StartReconnect(name string) error` on `sshconn.Manager` inside its existing `supervisors` map and `supervisorsWG`, running `ensureOnce(..., explicit=true)` immediately and retrying transient errors with `sshconn`'s 500ms to 30s backoff until attached or terminally failed.
   - **Why selected**: Keeps all reconnect ownership in `sshconn` (so Remove, Update, Detach, Close, manual Connect, and `midAttach` UI state stay unified) and keeps runtime connection state decoupled from `hostops`'s crash-fenced journal and `hub.toml`.

2. **Store `last_attached` inside `hostops.Store` (`HostRecord`) + `sshconn.Manager` startup reconnect**
   - Keeps all per-host metadata in one file, at the cost of routing runtime connect events through `hostops`'s staged-commit, boundary-mirror, quarantine, and legacy `hub.toml` migration paths, and losing persistence whenever `opsUnavailable` is active.

3. **Dedicated `hubcore` attachment store + hub-layer retry goroutines calling `sshManager.Ensure`**
   - Leaves `sshconn` untouched, at the cost of a second reconnect loop outside `sshconn`'s supervisor ownership (`midAttach` flickers on each retry because `Ensure` emits `StateDisconnected` on retryable failures, and Remove/Update/manual Connect must coordinate with a parallel loop).

## Decisions

### Decided by Jesse

- Startup reconnect runs in the background with bounded exponential backoff and **explicit-Connect semantics** (`explicit=true`), so stopped compatible remote hubs can be started on controller startup.
- Only hosts connected on the last run reconnect at startup; never-connected hosts stay dormant.
- Implementation uses Approach 1: dedicated `hubcore` JSON store plus `sshconn.Manager.StartReconnect`.

### Proposed Calls (for builder and partner verification)

1. **State file location and schema (`cmd/evener-hub/internal/hubcore/attached_hosts_store.go`)**:
   - Path: `<HubStateRoot>/attached-hosts/state.json` (empty `stateRoot` runs in-memory only, matching `CreationStore`).
   - JSON envelope (`version: 1`):
     ```json
     {
       "version": 1,
       "hosts": {
         "buildbox": {
           "generation": 2,
           "incarnation_id": "019..."
         }
       }
     }
     ```
   - Decoded with `decodeStateFileStrict`. Missing file yields an empty store (`err == nil`). Corrupt or unsupported-version file returns `(nil, err)` and leaves the file untouched on disk; `openDurableState` logs the error and the hub serves with attachment persistence paused (matching `CreationStore`).
   - Methods on `*AttachedHostsStore` (all nil-safe and mutex-guarded): `Snapshot() map[string]AttachedHostRecord`, `Record(name string, rec AttachedHostRecord) error`, `Remove(name string) error`, and `Prune(keep func(name string, rec AttachedHostRecord) bool) error`. Each write is an idempotent no-op when state is unchanged and otherwise commits via `writeStateFileAtomic`. If rename fails before publish, in-memory state stays unchanged; if post-rename directory sync fails (`renamed == true`), in-memory state updates to match the published file and returns the error.

2. **Registration identity on `sshconn.Event` (`cmd/evener-hub/internal/sshconn/manager.go`)**:
   - Add `Generation uint64` and `IncarnationID string` fields to `sshconn.Event`.
   - `attachEvent(name, ch, state)` populates them directly from `ch.host.Generation` and `ch.host.IncarnationID`, so `onSSHEvent` records the exact registration that attached with no registry lookup race.

3. **Startup reconnect supervisor (`cmd/evener-hub/internal/sshconn/supervise.go` and `manager.go`)**:
   - Export `func (m *Manager) StartReconnect(name string) error`.
   - Validation and idempotency:
     - Returns `ErrHostNotFound` if `m.reg == nil` or `name` (trimmed) is absent from `m.reg`.
     - Under `m.mu`: returns `ErrManagerClosed` if `m.closed`. Returns `nil` without starting a loop if `m.chans[name]` is already live for `host.Generation`, or if `m.supervisors[name]` already holds a loop for `host.Generation`.
     - Acquires the host lock entry reference under `m.mu` (`hostLockLocked`), registers `&supervisorLoop{host: host, cancel: cancel, startup: true}` in `m.supervisors[name]`, and spawns the goroutine under `m.supervisorsWG.Go`.
   - Loop behavior:
     - Runs the first attempt immediately with no initial backoff delay.
     - Acquires `lock.Lock()` with holder `{Kind: hostops.HolderManager, Activity: "reconnect"}`. Re-checks `ctx.Err() != nil`, `m.currentChannel(host.Name) != nil`, and `m.reg.SameRegistration(host.Name, host)`.
     - Calls `m.ensureOnce(ctx, host, true)` (`explicit=true`).
     - On success: validates channel liveness, `SameRegistration`, and `publishChannel` (matching `reconnectOnce`), emits `attachEvent(host.Name, nch, StateAttached)`, hands off to `m.startSupervise(host, nch, lock)` (normal mid-run `explicit=false` supervision), and exits.
     - On terminal error (`isTerminal(err)`): unless `m.baseCtx.Err() != nil`, emits `failedEvent`, emits `stateEvent(StateDisconnected)`, stops same-or-older loops for `host.Name`, and exits.
     - On retryable error (or post-publish link drop): if `ctx.Err() != nil`, exits silently; otherwise emits `stateEvent(StateReconnecting)`, releases `lock`, sleeps `waitSleep(ctx, jitterFor(delay))` starting at `min(backoffBase, backoffMax)` and doubling via `nextBackoff` up to `backoffMax`, and retries.
   - Coordination with `Ensure`, `AttachUnderGate`, and teardown:
     - A manual `Ensure` acquires `lock` without waiting behind backoff sleep. While `Ensure` is attempting, any `startup` loop stays registered so a failed or canceled `Ensure` does not strand recovery.
     - Once `startSupervise` succeeds for a newly published channel (from `Ensure`, `AttachUnderGate`'s `handoff`, or `StartReconnect`), cancel any other `startup` loops for `host.Name` with generation `<= host.Generation` so a sleeping startup loop cannot wake later and race the new channel's mid-run supervisor.
     - In `Ensure`'s retryable failure path, if an active supervisor loop already exists for `(name, host.Generation)`, emit `StateReconnecting` (keeping `midAttach: true`) rather than `StateDisconnected`.
     - `RemoveHost`, `UpdateHost`, and `DetachHost` run `teardownHostChannel` -> `stopSupervisorsBefore`, cancelling any startup loop. When `teardownHostChannel` stops a startup loop while no channel was mapped (`ch == nil`), emit `stateEvent(name, StateDisconnected)` so `midAttach` clears cleanly.

4. **Async persistence bridge and shutdown order (`cmd/evener-hub/`)**:
   - Wrap `*hubcore.AttachedHostsStore` in a small serialized FIFO worker (`attachedHostsPersister`) with non-blocking `Record(name, rec)` and `Remove(name)` methods plus a synchronous `Close()` that drains and flushes all queued mutations before returning.
   - `onSSHEvent` calls `persister.Record(ev.Host, ...)` on `EventAttached` only. It never clears on `EventDetached` (preserving state across hub shutdown and transient drops) or `EventFailed` (so restarting the controller after fixing a build or flag error retries automatically once).
   - `hubHostManager` clears entries via a `forgetAttachedHost(name string)` hook on `UpdateResult` (beside `state.retire`, and in `teardown-retry` for an update remnant) and `dropHostDerivedState` (Remove and restored-runtime removal).
   - In `runMain` / `hub_startup.go`, `persister.Close()` runs right after `r.sshManager.Close()` (after `stopBackground()` and `closeWeb()`, before `closeListener()` and hub lock release), ensuring every attach up to manager close is flushed before exit.

5. **Startup selection and pruning (`cmd/evener-hub/hub_startup.go`)**:
   - In `startBackground(web)` (after `buildWeb`, `bootRecords`, `SetEnsureDeployHook`/`SetEnsureRestartHook`, and `wireNavigationInvalidation` are all in place), select hosts from `r.hostRegistry.All()` in deterministic name order:
     - Present in `r.attachedHostsStore.Snapshot()`.
     - When host records are healthy (`!r.opsUnavailable && !r.recordsUnreadable`) and the saved record has a non-empty `IncarnationID`: require `entry.Generation == saved.Generation && entry.IncarnationID == saved.IncarnationID`.
     - When degraded (`r.opsUnavailable || r.recordsUnreadable`, where identities were minted fresh at load): match by `Name` alone so transient `opsStore` unavailability still reconnects the fleet.
     - Skip hosts with an open teardown remnant (`web.hostManage.openRemnantID(entry.Name)`).
     - Gate on `shouldAutoReconnect(entry)` (returns `true` today; single extension point for future one-shot hosts).
   - When host records are healthy, prune entries from `r.attachedHostsStore` whose host is absent from `hostRegistry` or whose `(Generation, IncarnationID)` no longer matches (retaining entries that only skipped due to an open remnant so resolving the remnant leaves intent intact).
   - Call `r.sshManager.StartReconnect(entry.Name)` for each selected host, logging any error.

6. **Evergreen docs**:
   - Update `docs/product/subsystems.md` (S06 and S07) to document `AttachedHostsStore` ownership, startup reconnect, and `sshconn.Manager.StartReconnect`.
   - Update `docs/evener-hub-remote-operations.md` to document automatic startup reconnection of previously connected hosts and dormant retention of never-connected hosts.

## Left to the Builder

- Exact helper names, file organization inside `cmd/evener-hub/` (for example, placing `attachedHostsPersister` and `startupReconnectHosts` in a focused `app_host_reconnect.go` file), and test helper structure.
- Choice of fake runner / synctest vs real localhost harnesses for unit and multi-boot tests (both `hubcore`, `sshconn`, and `runMain` two-boot integration coverage should be included).
- Log line wording for startup reconnect errors and persistence errors (using `r.logf` / `componentlog`).
