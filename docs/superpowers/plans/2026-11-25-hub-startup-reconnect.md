# Hub Startup Auto-Reconnect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist remote hosts that connect during a hub run and automatically reconnect them in the background on hub startup with bounded backoff and explicit-Connect semantics, leaving never-connected hosts dormant.

**Architecture:** Add a dedicated atomic JSON state store (`hubcore.AttachedHostsStore`) under `<HubStateRoot>/attached-hosts/state.json` keyed by host name with `(generation, incarnation_id)`, paired with a supervisor-owned `sshconn.Manager.StartReconnect(name)` entry point that runs `ensureOnce(..., explicit=true)` immediately and retries transient errors with `sshconn`'s existing 500ms to 30s backoff before handing off to mid-run `supervise`. Wire `cmd/evener-hub` to record `EventAttached` through a non-blocking serialized persister flushed right after `sshManager.Close()`, clear entries on `Remove` and `Update`, skip remnant-fenced hosts, and gate startup selection through one `shouldAutoReconnect` seam.

**Tech Stack:** Go (`cmd/evener-hub/internal/hubcore`, `cmd/evener-hub/internal/sshconn`, `cmd/evener-hub`), `github.com/spf13/afero`, `testing/synctest`, `internal/testwait`, Markdown product docs.

**Spec:** `docs/superpowers/specs/2026-11-25-hub-startup-reconnect-design.md`

## Global Constraints

- **Copy is not a test oracle (`docs/developing-evener/testing.md`)**: Never assert on human-readable error strings, log text, or prose substrings. Assert typed sentinels (`errors.Is`), typed wire errors (`errors.As`), structured fields, and observable side effects.
- **Deterministic tests (`docs/developing-evener/testing.md`)**: Never use fixed `time.Sleep` to wait for async work. Await completions via channels/callbacks, `testing/synctest`, `sshconn.Options.sleep`/`jitter` hooks, or `internal/testwait.Until`. Keep default tests completely offline with fake runners.
- **Reconnect ownership (`host-reconnect-ownership.md`)**: Reuse `sshconn.Manager`'s `supervisors` map, `supervisorsWG`, `hostLockGate`, and `stopSupervisorsBefore` teardown. Retire a startup supervisor only after a replacement attachment commits supervision.
- **Lock ordering**: Never perform disk I/O or call back into `sshconn.Manager` from `sshconn.Options.OnEvent` while the per-host lock is held.
- **On-disk compatibility (`docs/product/README.md#compatibility`)**: Use a strict versioned JSON envelope (`version: 1`) via `hubcore/state_file.go`. Leave corrupt files untouched on disk; never fail hub startup on a missing or corrupt attached-hosts file.
- **Shutdown ordering**: `runMain` unwinds in reverse startup order (`retireEndpoint` -> `stopBackground` -> `closeWeb` -> `closeHosts` -> `closeListener` -> `release` lock). Drain and flush the attached-hosts persister right after `sshManager.Close()` completes and before releasing the listener or hub lock.
- **Platform and code conventions**: Linux and Darwin only (no Windows-only branches or checks). Every production symbol needs a production caller. Comments explain invariants, never change history or review rounds.
- **Size budget**: Keep total production changes within the ~350 to 450 line budget from the spec.

## Review Focus

1. **Back-to-back hub restart while a remembered host is offline or in mid-run `StateReconnecting`**: Boot 1 attaches `alpha`, Boot 2 restarts while `alpha` is transiently unreachable before it reconnects, and Boot 3 still retries and attaches `alpha` once it comes up (pinned in Task 3).
2. **Manual `Ensure` racing a sleeping startup reconnect loop, followed by transient failure or immediate channel drop**: A failing or canceled manual `Ensure` preserves the startup loop and `StateReconnecting` (`midAttach: true`), while a committing manual `Ensure` retires the startup loop so a subsequent drop runs only one mid-run supervisor (pinned in Task 2).
3. **Host edited (`evener/host/update`) or removed-and-re-added under the same name before restart**: Neither the edited generation nor the re-added incarnation inherits the prior host's auto-reconnect on next boot (pinned in Task 3, plus registration recheck in Task 2).
4. **Late `EventAttached` right at hub shutdown (`Close()` race)**: An attach completing just before `sshManager.Close()` is still drained and flushed to `attached-hosts/state.json` before `runMain` exits and releases the hub lock (pinned in Task 3).
5. **Degraded `opsUnavailable` boot plus corrupt `attached-hosts/state.json`**: A degraded boot falls back to name-only matching without pruning `attached-hosts/state.json` and without overwriting durable `(generation, incarnation_id)` entries with transient minted identities; a corrupt `attached-hosts/state.json` leaves corrupt bytes untouched and lets the hub start normally (pinned in Task 1 and Task 3).

---

## File Map

- Create: `cmd/evener-hub/internal/hubcore/attached_hosts_store.go` - Durable JSON store at `<HubStateRoot>/attached-hosts/state.json`.
- Create: `cmd/evener-hub/internal/hubcore/attached_hosts_store_test.go` - Unit, fault-injection, corruption, and race tests for `AttachedHostsStore`.
- Modify: `cmd/evener-hub/internal/sshconn/hostlock.go` - Factor `hostLockLocked(name string) *hostLockGate` under `m.mu`.
- Modify: `cmd/evener-hub/internal/sshconn/manager.go` - Add `Generation` and `IncarnationID` to `Event`, populate in `attachEvent`, emit `StateDisconnected` in `teardownHostChannel` when cancelling a startup loop with no mapped channel.
- Modify: `cmd/evener-hub/internal/sshconn/supervise.go` - Add `startup bool` on `supervisorLoop`, `Manager.StartReconnect`, `superviseStartup`, `reconnectStartupOnce`, `stopStartupSupervisorsLocked`, and `hasActiveSupervisor`.
- Modify: `cmd/evener-hub/internal/sshconn/ensure.go` - Preserve `StateReconnecting` when a manual `Ensure` fails transiently while an active supervisor loop owns the registration.
- Create: `cmd/evener-hub/internal/sshconn/startup_reconnect_test.go` - Deterministic tests for `EventAttached` identity, `StartReconnect` lifecycle, backoff, bootstrap, terminal refusal, teardown, and `Ensure` races.
- Create: `cmd/evener-hub/app_host_reconnect.go` - `attachedHostsPersister` FIFO worker, `shouldAutoReconnect` policy seam, `selectStartupReconnectHosts`, and `startStartupHostReconnect`.
- Modify: `cmd/evener-hub/internal/hubcore/config.go` - Add `ForgetAttachedHost func(name string)` to `WebConfig`.
- Modify: `cmd/evener-hub/app_host_config.go` - Thread `forgetAttachedHost` into `hostManagerConfig`.
- Modify: `cmd/evener-hub/app_host_manage.go` - Call `forgetAttachedHost(name)` inside `dropHostDerivedState`.
- Modify: `cmd/evener-hub/app_host_mutations.go` - Call `forgetAttachedHost(name)` alongside `state.retire` in `UpdateResult`.
- Modify: `cmd/evener-hub/app_host_teardown_ops.go` - Call `forgetAttachedHost(remnant.Host)` alongside `state.retire` in update-remnant `teardown-retry`.
- Modify: `cmd/evener-hub/hub_startup.go` - Open `attachedHostsStore` and `attachedHostsPersister` in `openDurableState`, close persister right after `sshManager.Close()`, record on `EventAttached` in `onSSHEvent`, wire `ForgetAttachedHost` in `buildWeb`, launch `startStartupHostReconnect` in `startBackground`.
- Create: `cmd/evener-hub/app_host_reconnect_test.go` - Unit tests for persister/selection/pruning and multi-boot `runMain` integration tests.
- Modify: `docs/product/subsystems.md` and `docs/evener-hub-remote-operations.md` - Evergreen product documentation updates.

---

### Task 1: `hubcore.AttachedHostsStore` durable attachment store

**Files:**
- Create: `cmd/evener-hub/internal/hubcore/attached_hosts_store.go`
- Test: `cmd/evener-hub/internal/hubcore/attached_hosts_store_test.go`

**Interfaces:**
- Consumes: `writeStateFileAtomic`, `readStateFile`, `decodeStateFileStrict` in `cmd/evener-hub/internal/hubcore/state_file.go`.
- Produces (in package `hubcore`):
  - `type AttachedHostRecord struct { Generation uint64 `json:"generation"`; IncarnationID string `json:"incarnation_id"` }`
  - `type AttachedHostsStore struct`
  - `func NewAttachedHostsStore(stateRoot string) (*AttachedHostsStore, error)`
  - `func (s *AttachedHostsStore) Snapshot() map[string]AttachedHostRecord`
  - `func (s *AttachedHostsStore) Record(name string, rec AttachedHostRecord) error`
  - `func (s *AttachedHostsStore) Remove(name string) error`
  - `func (s *AttachedHostsStore) Prune(keep func(name string, rec AttachedHostRecord) bool) error`

- [ ] **Step 1: Write failing unit tests in `cmd/evener-hub/internal/hubcore/attached_hosts_store_test.go`**

Write table/focused tests (with `t.Parallel()` on every test) covering:
- `TestAttachedHostsStoreMissingFileStartsEmpty`: opening a fresh temp dir returns non-nil store, `err == nil`, empty `Snapshot()`, and creates no file until a write occurs.
- `TestAttachedHostsStoreRecordRemoveReload`: `Record("alpha", {Generation: 2, IncarnationID: "inc-1"})` writes `<root>/attached-hosts/state.json` with `version: 1`; reopening from the same root returns the exact record; `Snapshot()` returns a defensive copy (mutating it does not change store state); `Remove("alpha")` removes it durably across reload.
- `TestAttachedHostsStoreIdempotentNoOpWrites`: recording the same `(name, rec)`, removing an absent host, or calling `Prune` that keeps all entries performs zero disk writes (verified via `afero.MemMapFs` or `beforeRename` fault hook not firing).
- `TestAttachedHostsStorePruneAtomic`: `Prune` removes all entries where `keep` returns `false` in one atomic write and persists across reload; `keep == nil` is a safe no-op.
- `TestAttachedHostsStoreRejectsInvalidNames`: empty or whitespace-only name returns non-nil error and leaves state unchanged; surrounding whitespace is trimmed before storing/removing.
- `TestAttachedHostsStoreCorruptFileUntouched`: malformed JSON, unknown JSON field, trailing JSON value, missing/null `hosts` map, empty host key in JSON, or unsupported `version` (e.g. `version: 0` or `version: 2`) causes `NewAttachedHostsStore` to return `(nil, err)` and leaves the on-disk bytes identical.
- `TestAttachedHostsStoreAtomicWriteFaults`: using internal `newAttachedHostsStoreFS` with `beforeRename` fault keeps previous in-memory state on error; `afterRename` fault updates in-memory state to match the renamed file and returns the error.
- `TestAttachedHostsStoreNilAndMemoryOnly`: `(*AttachedHostsStore)(nil)` safely returns empty map / `nil` on all methods; `NewAttachedHostsStore("")` keeps state in memory without touching disk.
- `TestAttachedHostsStoreConcurrentAccess`: concurrent `Record`, `Remove`, `Prune`, and `Snapshot` goroutines pass under `-race`.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^TestAttachedHostsStore' -count=1`
Expected: FAIL with undefined `AttachedHostsStore` / `NewAttachedHostsStore`.

- [ ] **Step 3: Implement `cmd/evener-hub/internal/hubcore/attached_hosts_store.go`**

Implement the store following `creation_store.go` and `state_file.go` idioms:
- Constants: `attachedHostsSnapshotVersion uint64 = 1`, `attachedHostsStateSubdir = "attached-hosts"`, `attachedHostsStateFilename = "state.json"`.
- Envelope type:
  ```go
  type attachedHostsSnapshot struct {
      Version uint64                        `json:"version"`
      Hosts   map[string]AttachedHostRecord `json:"hosts"`
  }
  ```
- Unexported constructor `newAttachedHostsStoreFS(fs afero.Fs, stateRoot string, faults attachedHostsStoreFaults) (*AttachedHostsStore, error)` so tests inject `beforeRename` / `afterRename` faults without exporting test-only symbols outside the package.
- `loadAttachedHostsFS`: if `strings.TrimSpace(stateRoot) == ""`, return empty map and `nil`. Call `readStateFile(fs, path, "attached hosts")`. If `!ok`, return empty map and `nil`. Decode with `decodeStateFileStrict(data, "attached hosts", &snap)`. Validate `snap.Version == attachedHostsSnapshotVersion`, `snap.Hosts != nil`, and every key is non-empty and already trimmed (`strings.TrimSpace(k) == k && k != ""`). Return normalized map.
- `persistLocked(next map[string]AttachedHostRecord) error`: if `s.root == ""`, assign `s.hosts = next` and return `nil`. Marshal `attachedHostsSnapshot{Version: attachedHostsSnapshotVersion, Hosts: next}` with trailing newline (matching other `hubcore` stores), call `writeStateFileAtomic(s.fs, path, "attached hosts", encoded, s.faults.BeforeRename, s.faults.AfterRename)`. Only assign `s.hosts = next` when `renamed == true` (including when `err != nil && renamed`), keeping pre-rename failures rolled back in memory.
- `Record`: nil-safe (returns `nil`); trims `name` and rejects empty `name`; skips write if existing record equals `rec`; clones map, sets `next[name] = rec`, calls `persistLocked(next)`.
- `Remove`: nil-safe; trims `name`; returns `nil` if empty or absent; clones map, deletes `name`, calls `persistLocked(next)`.
- `Prune`: nil-safe; if `keep == nil` returns `nil`; iterates current entries, builds `next` with kept entries; if `len(next) == len(s.hosts)` returns `nil`; calls `persistLocked(next)`.
- `Snapshot`: nil-safe; returns a freshly cloned `map[string]AttachedHostRecord`.

- [ ] **Step 4: Run `hubcore` tests with `-race` to verify pass**

Run: `go test -race ./cmd/evener-hub/internal/hubcore -run '^TestAttachedHostsStore' -count=1`
Expected: PASS, 0 failures, no race warnings.

- [ ] **Step 5: Commit Task 1**

```bash
git add cmd/evener-hub/internal/hubcore/attached_hosts_store.go cmd/evener-hub/internal/hubcore/attached_hosts_store_test.go
git commit -m "feat(hubcore): add durable AttachedHostsStore for connected remote hosts"
```

---

### Task 2: `sshconn` registration identity on `EventAttached` and `Manager.StartReconnect`

**Files:**
- Modify: `cmd/evener-hub/internal/sshconn/hostlock.go:136-146`
- Modify: `cmd/evener-hub/internal/sshconn/manager.go:58-63,907-929,1824-1831`
- Modify: `cmd/evener-hub/internal/sshconn/supervise.go:138-354`
- Modify: `cmd/evener-hub/internal/sshconn/ensure.go:150-180`
- Test: `cmd/evener-hub/internal/sshconn/startup_reconnect_test.go`

**Interfaces:**
- Consumes: existing `hostreg.Registry` (`Get`, `SameRegistration`), `Manager.ensureOnce(ctx, host, explicit)`, `Manager.publishChannel`, `Manager.startSupervise`, `Manager.stopSupervisorsBefore`, `Manager.attachEvent`, `Manager.failedEvent`, `Manager.stateEvent`.
- Produces (in package `sshconn`):
  - `Event.Generation uint64` and `Event.IncarnationID string` (populated on `EventAttached` from `ch.host`).
  - `func (m *Manager) StartReconnect(name string) error`

- [ ] **Step 1: Write failing tests in `cmd/evener-hub/internal/sshconn/startup_reconnect_test.go`**

Write deterministic tests (using `fakeRunner`, injected `sleep`/`jitter`, channels, and `testwait` where needed; NO fixed `time.Sleep`, NO phrasing assertions):
- `TestAttachEventCarriesRegistrationIdentity`: attaching a host with explicit `(Generation, IncarnationID)` emits `EventAttached` whose `ev.Generation` and `ev.IncarnationID` equal the registered host's values.
- `TestStartReconnectValidationAndIdempotency`:
  - `StartReconnect` on a manager with `nil` registry or unknown host returns `ErrHostNotFound` (`errors.Is`).
  - `StartReconnect` on a closed manager returns `ErrManagerClosed` (`errors.Is`).
  - `StartReconnect` on an already-attached host returns `nil` and triggers zero additional runner calls.
  - Calling `StartReconnect` twice concurrently or back-to-back while reconnecting starts only one supervisor loop.
- `TestStartReconnectAttachesImmediatelyAndHandsOffToMidRunSupervise`:
  - When remote hub is already running and compatible, `StartReconnect("alpha")` dials immediately without calling `sleep` first, emits `EventAttached` with `alpha`'s `(Generation, IncarnationID)`, and marks `Attached("alpha") == true`.
  - When that channel subsequently drops and the remote hub is stopped, the mid-run supervisor (`explicit=false`) does NOT issue a bootstrap hub-start command (proving handoff from startup `explicit=true` to mid-run `explicit=false`).
- `TestStartReconnectBootstrapsStoppedCompatibleHub`:
  - When remote hub reports `state: "stopped"` with a compatible installed executable, `StartReconnect("alpha")` runs the `explicit=true` bootstrap start path and attaches once the hub comes up.
- `TestStartReconnectRetriesWithBoundedBackoff`:
  - When initial preflight/attach attempts fail with retryable SSH errors, `StartReconnect("alpha")` emits `StateReconnecting` between attempts, backs off from `BackoffBase` up to `BackoffMax`, and attaches as soon as the fake runner starts succeeding.
- `TestStartReconnectStopsOnTerminalError`:
  - When preflight/attach returns a terminal error (e.g. `ErrHostProtocolNewer` or `ErrProtocolIncompatible` with `DeployDisabled: true`), `StartReconnect("alpha")` emits `EventFailed` (matching sentinel via `errors.Is`) followed by `StateDisconnected`, and makes zero further attempts.
- `TestStartReconnectStopsCleanlyOnCloseRemoveUpdateAndDetach`:
  - Subtests for `Close()`, `RemoveHost("alpha")`, `UpdateHost(updated, nil)`, and `DetachHost("alpha")` while `StartReconnect` is parked in backoff sleep:
    - Each cancels the startup loop promptly and `Close()` returns with all goroutines exited (`supervisorsWG` drained) and zero `EventFailed`.
    - `DetachHost("alpha")` while no channel was mapped (`ch == nil`) emits `StateDisconnected` so consumers clear `midAttach`.
- `TestEnsureRacingStartupReconnectPreservesOrRetiresLoop`:
  - Subtest A (Review Focus #2, transient failure / cancellation): while `StartReconnect` is parked in backoff sleep, a manual `Ensure` runs immediately without waiting on backoff. When that manual `Ensure` fails transiently (or its `ctx` is canceled), `EventState(StateDisconnected)` is NOT emitted for the retryable failure (`StateReconnecting` holds), the startup loop stays alive, and the next wake attaches cleanly.
  - Subtest B (Review Focus #2, commit then immediate drop): while `StartReconnect` is parked in backoff sleep, a manual `Ensure` succeeds and commits supervision, retiring the startup loop. When the new channel immediately drops and the startup loop's wake releases, only the new channel's mid-run supervisor runs (never a duplicate startup attempt).

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./cmd/evener-hub/internal/sshconn -run '^Test(AttachEventCarriesRegistrationIdentity|StartReconnect|EnsureRacingStartupReconnect)' -count=1`
Expected: FAIL with undefined `StartReconnect`, `Event.Generation`, `Event.IncarnationID`.

- [ ] **Step 3: Implement the `sshconn` changes**

Make the minimal, exact edits:
1. `cmd/evener-hub/internal/sshconn/hostlock.go`:
   - Extract `func (m *Manager) hostLockLocked(name string) *hostLockGate` (caller holds `m.mu`, creates `hostLockEntry` if absent, increments `refs`, returns `&entry.gate`) and have `hostLock(name)` call it under `m.mu`.
2. `cmd/evener-hub/internal/sshconn/manager.go`:
   - Add `Generation uint64` and `IncarnationID string` to `Event`.
   - In `attachEvent(name string, ch *Channel, s State)`: populate `Generation: ch.host.Generation` and `IncarnationID: ch.host.IncarnationID` on the emitted `Event` (guard `ch != nil`).
   - In `teardownHostChannel`: update `stopSupervisorsBefore` (only existing caller is `manager.go:911`) to return `(stoppedAny, reconnectingNewer bool)`. When `ch == nil && stoppedAny && !reconnectingNewer`, emit `m.stateEvent(name, StateDisconnected)` so `DetachHost` / teardown clears `midAttach` on a startup-reconnecting host that never had a mapped channel.
3. `cmd/evener-hub/internal/sshconn/supervise.go`:
   - Add `startup bool` field on `supervisorLoop`.
   - Add helper `func (m *Manager) hasActiveSupervisor(name string, gen uint64) bool` (reads `m.supervisors[name]` under `m.mu` and returns `true` if any loop has `loop.host.Generation == gen`).
   - Inside `startSupervise(host, ch, lock)`: once the new channel supervisor is registered in `m.supervisors[host.Name]`, collect any other loops in `m.supervisors[host.Name]` with `other.startup && other.host.Generation <= host.Generation`, remove them from `m.supervisors[host.Name]`, and cancel them after unlocking `m.mu` (honoring `host-reconnect-ownership.md`: retirement happens only *after* replacement attachment commits supervision!).
   - Add `func (m *Manager) StartReconnect(name string) error`:
     - Check `m.reg == nil` -> `ErrHostNotFound`; resolve trimmed `name` via `m.reg.Get(name)` -> `ErrHostNotFound`; normalize `name = host.Name`.
     - Lock `m.mu`:
       - If `m.closed`, unlock and return `ErrManagerClosed`.
       - If `ch := m.chans[name]; ch != nil && !ch.isClosed() && !ch.isLost() && ch.host.Generation == host.Generation`, unlock and return `nil`.
       - If any loop in `m.supervisors[name]` has `loop.host.Generation == host.Generation`, unlock and return `nil`.
       - `lock := m.hostLockLocked(name)` (takes live-user ref under `m.mu` before spawning).
       - `ctx, cancel := context.WithCancel(m.baseCtx)`
       - `loop := &supervisorLoop{host: host, cancel: cancel, startup: true}`
       - Register `loop` in `m.supervisors[name]`.
       - Launch `m.supervisorsWG.Go(...)` with the exact same `defer` deregister + `cancel()` + `releaseHostLock(host.Name)` + `superviseExited` pattern as `startSupervise`, calling `m.superviseStartup(ctx, host, lock)`.
     - Unlock `m.mu` and return `nil`.
   - Add `superviseStartup(ctx context.Context, host hostreg.Host, lock *hostLockGate)` and `reconnectStartupOnce(ctx context.Context, host hostreg.Host, lock *hostLockGate) bool`:
     - `superviseStartup` calls `m.reconnectStartupOnce(ctx, host, lock)` immediately (no initial backoff wait); while it returns `true`, sleeps `m.opts.waitSleep(ctx, m.jitterFor(delay))` (returning silently on `err != nil`), advances `delay = nextBackoff(delay, m.opts.backoffMax())`, and retries.
     - `reconnectStartupOnce` acquires `lock.Lock()`, calls `lock.holdAs(hostops.Holder{Kind: hostops.HolderManager, Activity: "reconnect"})`, defers `lock.Unlock()`:
       - If `ctx.Err() != nil` or `m.currentChannel(host.Name) != nil`, returns `false`.
       - If `!m.reg.SameRegistration(host.Name, host)`, emits `m.stateEvent(host.Name, StateDisconnected)` and returns `false`.
       - Calls `nch, err := m.ensureOnce(ctx, host, true)`.
       - On `err == nil`: follows `reconnectOnce`'s exact validation/publish/post-publish-liveness/`attachEvent`/`startSupervise` handoff sequence, returning `false` on attach or terminal stop, `true` on transient post-publish drop.
       - On `err != nil`: `err = m.mapAttemptErr(err)`. If `isTerminal(err)`: emit `failedEvent` (if `m.baseCtx.Err() == nil`), emit `stateEvent(StateDisconnected)`, call `m.stopSupervisorsBefore(host.Name, func(gen uint64) bool { return gen > host.Generation })`, return `false`. If `ctx.Err() != nil`, return `false` (canceller owns events). Otherwise emit `m.stateEvent(host.Name, StateReconnecting)` and return `true`.
4. `cmd/evener-hub/internal/sshconn/ensure.go`:
   - Around line 174 (after `if isTerminal(err) { ... }` in `Ensure`): when `!isTerminal(err)` and `m.hasActiveSupervisor(name, host.Generation)`, emit `m.stateEvent(name, StateReconnecting)` instead of `m.stateEvent(name, StateDisconnected)` so a transiently failing manual `Ensure` does not clear `midAttach` while the startup or mid-run supervisor is still backing off.

- [ ] **Step 4: Run full `sshconn` test suite with `-race`**

Run: `go test -race ./cmd/evener-hub/internal/sshconn -count=1`
Expected: PASS (all new and existing `sshconn` tests green, zero races).

- [ ] **Step 5: Commit Task 2**

```bash
git add cmd/evener-hub/internal/sshconn/hostlock.go cmd/evener-hub/internal/sshconn/manager.go cmd/evener-hub/internal/sshconn/supervise.go cmd/evener-hub/internal/sshconn/ensure.go cmd/evener-hub/internal/sshconn/startup_reconnect_test.go
git commit -m "feat(sshconn): add Manager.StartReconnect and EventAttached registration identity"
```

---

### Task 3: Hub persistence bridge, host mutation clears, and startup reconnect wiring

**Files:**
- Create: `cmd/evener-hub/app_host_reconnect.go`
- Modify: `cmd/evener-hub/internal/hubcore/config.go:223-235`
- Modify: `cmd/evener-hub/app_host_config.go:106-120,275-305`
- Modify: `cmd/evener-hub/app_host_manage.go:400-415`
- Modify: `cmd/evener-hub/app_host_mutations.go:640-660`
- Modify: `cmd/evener-hub/app_host_teardown_ops.go:615-636`
- Modify: `cmd/evener-hub/hub_startup.go:160-180,477-630,665-720,830-955`
- Test: `cmd/evener-hub/app_host_reconnect_test.go`

**Interfaces:**
- Consumes:
  - `hubcore.AttachedHostsStore`, `hubcore.AttachedHostRecord`, `hubcore.NewAttachedHostsStore` (Task 1).
  - `sshconn.Event.Generation`, `sshconn.Event.IncarnationID`, `sshconn.Manager.StartReconnect` (Task 2).
- Produces:
  - `hubcore.WebConfig.ForgetAttachedHost func(name string)`
  - `attachedHostsPersister` (unexported in package `hub`) with `Record`, `Remove`, `Close`
  - `shouldAutoReconnect(host hostreg.Host) bool` (unexported policy seam in `app_host_reconnect.go`)
  - `selectStartupReconnectHosts(...)` and `startStartupHostReconnect(...)` called during `hubRuntime.startBackground`.

- [ ] **Step 1: Write failing unit and multi-boot `runMain` tests in `cmd/evener-hub/app_host_reconnect_test.go`**

Write deterministic tests (reusing `newTraceMainTestDeps` + `deps.newSSHManager` with a fake `sshconn.Runner`, `internal/testwait.Until`, and channels; NO fixed `time.Sleep`, NO phrasing assertions):
- `TestAttachedHostsPersisterOrderingAndShutdownFlush` (Review Focus #4): queuing rapid `Record`/`Remove`/`Record` operations completes in exact FIFO order without blocking callers, and `Close()` flushes every pending mutation to disk before returning (including a `Record` enqueued just before `Close()`).
- `TestSelectStartupReconnectHostsPolicyAndDegradedMode` (Review Focus #5):
  - Never-connected host is excluded; matching `(Generation, IncarnationID)` is included.
  - Mismatched `Generation` (edited host) or mismatched `IncarnationID` (removed-and-re-added host) is excluded when `degraded == false`.
  - Host fenced by an open teardown remnant (`hasOpenRemnant(name) == true`) is excluded.
  - When `degraded == true` (`opsUnavailable` or `recordsUnreadable`), matching falls back to `Name` alone (still skipping remnant-fenced hosts).
  - Saved record with empty `IncarnationID` (recorded during a prior degraded boot) matches by `Name` on a healthy boot.
- `TestRunMainStartupAutoReconnectTwoBootCycle`:
  - Boot 1 (`runMain` over a shared `HubStateRoot` and `hub.toml` with `alpha` and `beta`, fake `sshconn.Runner` via `deps.newSSHManager`): explicitly attach `alpha` via `evener/host/attach`, leave `beta` dormant, shut down cleanly (`Close()` emits `EventDetached` for `alpha`). Verify `<HubStateRoot>/attached-hosts/state.json` persists `alpha` (with its real `Generation` and `IncarnationID`) and not `beta`.
  - Boot 2 (`runMain` over the same `HubStateRoot` and `hub.toml`, with `alpha` initially reporting `stopped` so bootstrap runs): without any client `evener/host/attach`, `alpha` automatically starts and attaches (verified via `evener/host/status` / `sshManager.Attached("alpha")`), while `beta` stays dormant with zero dials.
- `TestRunMainStartupAutoReconnectSurvivesBackToBackRestart` (Review Focus #1):
  - Boot 1 attaches `alpha` and shuts down cleanly.
  - Boot 2 runs while fake runner fails `alpha` transiently (retrying in backoff), then Boot 2 shuts down before `alpha` attaches.
  - Boot 3 starts with fake runner healthy for `alpha`; Boot 3 automatically reconnects `alpha`.
- `TestRunMainStartupAutoReconnectRetainsEntryAcrossTerminalFailure`:
  - Boot 1 attaches `alpha` and shuts down.
  - Boot 2 fails `alpha` terminally (e.g., `ErrHostProtocolNewer`), stops retrying, surfaces `lastAttachError`, and shuts down.
  - Boot 3 starts with compatible fake runner and automatically reconnects `alpha`.
- `TestRunMainHostRemoveAndUpdateClearAutoReconnect` (Review Focus #3):
  - Subtest A (Remove + Re-add): Boot 1 attaches `alpha`, removes `alpha` via `evener/host/remove`, re-adds `alpha` via `evener/host/add` (new `IncarnationID`), shuts down. Boot 2 keeps `alpha` dormant with zero dials.
  - Subtest B (Update): Boot 1 attaches `alpha`, edits `alpha` via `evener/host/update` (retiring channel and advancing `Generation`), shuts down without re-attaching. Boot 2 keeps `alpha` dormant with zero dials.
- `TestRunMainStartupAutoReconnectSkipsOpenRemnantAndPrunesStale`:
  - Seeding `attached-hosts/state.json` with a stale removed host `ghost` and a mismatched-generation host `stale` alongside a valid host `alpha`: on healthy boot, `ghost` and `stale` are pruned from `attached-hosts/state.json` and never dialed, while `alpha` reconnects; a host with an open teardown remnant is never dialed and its matching entry is not pruned.
- `TestRunMainDegradedOpsStorePreservesDurableIdentityAndCorruptStateStarts` (Review Focus #5):
  - Subtest A (Degraded `opsUnavailable`): Seed `attached-hosts/state.json` with `alpha`'s durable `(gen=1, inc="durable-inc")`. Boot with `opsStore` failing to open (or inject degraded state): `alpha` still reconnects by name fallback, `attached-hosts/state.json` is NOT pruned, and `EventAttached` with Boot 2's freshly minted `IncarnationID` does NOT overwrite `"durable-inc"` in `attached-hosts/state.json`.
  - Subtest B (Corrupt `attached-hosts/state.json`): Write invalid JSON to `<HubStateRoot>/attached-hosts/state.json`. `runMain` starts and serves normally, logs the diagnostic, and leaves the corrupt bytes untouched on disk.
- `TestRunMainShutdownFlushesLateAttachedHostBeforeLockRelease` (Review Focus #4):
  - Verify `runMain` flushes `attachedHostsPersister` after `sshManager.Close()` and before releasing the hub lock, and existing `TestRunMainShutsDownInDependencyOrder` remains green.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./cmd/evener-hub -run '^Test(AttachedHostsPersister|SelectStartupReconnectHosts|RunMainStartupAutoReconnect|RunMainHostRemoveAndUpdateClearAutoReconnect|RunMainDegradedOpsStorePreservesDurableIdentityAndCorruptStateStarts|RunMainShutdownFlushesLateAttachedHostBeforeLockRelease)' -count=1`
Expected: FAIL with undefined `attachedHostsPersister` / `selectStartupReconnectHosts`.

- [ ] **Step 3: Implement `cmd/evener-hub/app_host_reconnect.go` and hub wiring**

1. Create `cmd/evener-hub/app_host_reconnect.go`:
   - `attachedHostsPersister`:
     - Holds `store *hubcore.AttachedHostsStore`, `logf func(string, ...any)`, `mu sync.Mutex`, `queue []attachedHostMutation`, `closed bool`, `wake chan struct{}` (buffered 1), `done chan struct{}`.
     - `newAttachedHostsPersister(store *hubcore.AttachedHostsStore, logf func(string, ...any)) *attachedHostsPersister`: returns nil if `store == nil`; otherwise spawns one worker goroutine draining `queue` in FIFO order and calling `store.Record` / `store.Remove`. For `RecordDegraded(name)`, the serialized worker checks `if _, exists := p.store.Snapshot()[name]; !exists` before calling `p.store.Record(name, hubcore.AttachedHostRecord{})`, so a transient minted identity never overwrites an existing durable `(Generation, IncarnationID)` without adding extra methods to `hubcore.AttachedHostsStore`.
     - `Record(name string, rec hubcore.AttachedHostRecord)` and `RecordDegraded(name string)` and `Remove(name string)`: nil-safe, non-blocking (lock `mu`, if `!closed` append op and non-blocking send to `wake`, unlock).
     - `Close()`: nil-safe, idempotent (`sync.Once` or `if p.closed` check under `mu`): sets `closed = true`, signals `wake`, waits on `<-p.done` so every queued write has finished before returning.
   - `shouldAutoReconnect(host hostreg.Host) bool`: returns `true` (documented as the single extension point for future per-host policies such as one-shot hosts).
   - `selectStartupReconnectHosts(entries []hostreg.Host, saved map[string]hubcore.AttachedHostRecord, degraded bool, hasOpenRemnant func(string) bool) (toReconnect []hostreg.Host, keepInStore map[string]bool)`:
     - Iterates `entries` in order (`hostRegistry.All()` is name-sorted):
       - Checks `rec, ok := saved[entry.Name]`; if `!ok`, skip.
       - Identity check: if `!degraded && rec.IncarnationID != ""` and `(entry.Generation != rec.Generation || entry.IncarnationID != rec.IncarnationID)`, skip (not in `keepInStore`, so healthy boot prunes it).
       - Marks `keepInStore[entry.Name] = true` (retaining remnant-fenced hosts in the store so resolving the remnant preserves intent).
       - If `hasOpenRemnant != nil && hasOpenRemnant(entry.Name)`, skip reconnect.
       - If `!shouldAutoReconnect(entry)`, skip reconnect.
       - Append `entry` to `toReconnect`.
   - `startStartupHostReconnect(r *hubRuntime, web *WebServer)`:
     - If `r.attachedHostsStore == nil || r.sshManager == nil || r.hostRegistry == nil`, return.
     - `degraded := r.opsUnavailable || r.recordsUnreadable`
     - `saved := r.attachedHostsStore.Snapshot()`
     - `var hasOpenRemnant func(string) bool`; if `web != nil && web.hostManage != nil`, bind `func(name string) bool { _, open := web.hostManage.openRemnantID(name); return open }`.
     - Compute `toReconnect, keepInStore := selectStartupReconnectHosts(r.hostRegistry.All(), saved, degraded, hasOpenRemnant)`.
     - If `!degraded`: call `r.attachedHostsStore.Prune(func(name string, _ hubcore.AttachedHostRecord) bool { return keepInStore[name] })` synchronously before spawning reconnect loops (logging any error).
     - For each `entry` in `toReconnect`, call `if err := r.sshManager.StartReconnect(entry.Name); err != nil { r.logf("startup reconnect %q: %v", entry.Name, err) }`.
2. Modify `cmd/evener-hub/internal/hubcore/config.go`:
   - Add `ForgetAttachedHost func(name string)` to `WebConfig`.
3. Modify `cmd/evener-hub/app_host_config.go`:
   - Add `forgetAttachedHost func(name string)` to `hostManagerConfig`, populated from `cfg.ForgetAttachedHost` in `newHubHostManager`.
4. Modify `cmd/evener-hub/app_host_manage.go`:
   - In `dropHostDerivedState(name)`: call `if m.cfg.forgetAttachedHost != nil { m.cfg.forgetAttachedHost(name) }`.
5. Modify `cmd/evener-hub/app_host_mutations.go`:
   - In `UpdateResult`: call `if m.cfg.forgetAttachedHost != nil { m.cfg.forgetAttachedHost(name) }` right alongside `m.cfg.state.retire(name, ...)` in both the manager `afterSwap` callback and the `default` branch.
6. Modify `cmd/evener-hub/app_host_teardown_ops.go`:
   - In `hostTeardownKindUpdate`: call `if m.cfg.forgetAttachedHost != nil { m.cfg.forgetAttachedHost(remnant.Host) }` right alongside `m.cfg.state.retire(remnant.Host, ...)` in the manager `afterSwap` callback and after `m.cfg.hosts.Update(entry)` in the no-manager branch.
7. Modify `cmd/evener-hub/hub_startup.go`:
   - Add fields on `hubRuntime`: `attachedHostsStore *hubcore.AttachedHostsStore`, `attachedHostsStoreErr error`, `attachedHostsPersister *attachedHostsPersister`.
   - In `openDurableState()`: open `r.attachedHostsStore, r.attachedHostsStoreErr = hubcore.NewAttachedHostsStore(r.hubStateRoot)` and log error if non-nil.
   - In `connectHosts()`: create `r.attachedHostsPersister = newAttachedHostsPersister(r.attachedHostsStore, r.logf)` right before building `r.sshManager`, and update `closeSSH` cleanup to close `r.sshManager` FIRST and then call `r.attachedHostsPersister.Close()` (ensuring every `onSSHEvent` up to `sshManager.Close()` returning is flushed to disk, and zero goroutines outlive `connectHosts`'s cleanup!).
   - In `onSSHEvent(ev sshconn.Event)`: when `ev.Kind == sshconn.EventAttached && r.attachedHostsPersister != nil`:
     - If `r.opsUnavailable || r.recordsUnreadable`, call `r.attachedHostsPersister.RecordDegraded(ev.Host)` (records `{0, ""}` only if absent, preserving any existing durable identity!).
     - Else call `r.attachedHostsPersister.Record(ev.Host, hubcore.AttachedHostRecord{Generation: ev.Generation, IncarnationID: ev.IncarnationID})`.
   - In `buildWeb`: set `ForgetAttachedHost: func(name string) { r.attachedHostsPersister.Remove(name) }` on `hubcore.WebConfig`.
   - In `startBackground`: call `startStartupHostReconnect(r, web)` right after `refreshHubRemoteThreads` starts (every navigation hook, `hostAdmin`, `hostManageEvents`, `EnsureDeploy`/`EnsureRestart`, and `bootRecords` is already wired!).

- [ ] **Step 4: Run targeted `cmd/evener-hub` tests with `-race`**

Run: `go test -race ./cmd/evener-hub -run '^Test(AttachedHostsPersister|SelectStartupReconnectHosts|RunMainStartupAutoReconnect|RunMainHostRemoveAndUpdateClearAutoReconnect|RunMainDegradedOpsStorePreservesDurableIdentityAndCorruptStateStarts|RunMainShutdownFlushesLateAttachedHostBeforeLockRelease|RunMainShutsDownInDependencyOrder|RunMainReleasesTheLockLastAfterASignal|HostManageReconnectingDetachKeepsMidAttach)' -count=1`
Expected: PASS, 0 failures, no races.

- [ ] **Step 5: Commit Task 3**

```bash
git add cmd/evener-hub/app_host_reconnect.go cmd/evener-hub/app_host_reconnect_test.go cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/app_host_config.go cmd/evener-hub/app_host_manage.go cmd/evener-hub/app_host_mutations.go cmd/evener-hub/app_host_teardown_ops.go cmd/evener-hub/hub_startup.go
git commit -m "feat(hub): persist attached hosts and reconnect them automatically on startup"
```

---

### Task 4: Evergreen product documentation updates

**Files:**
- Modify: `docs/product/subsystems.md` (S06 and S07 rows)
- Modify: `docs/evener-hub-remote-operations.md` (Managed execution hosts section)

**Interfaces:**
- Consumes: Final paths and symbol names from Tasks 1 to 3 (`hubcore.AttachedHostsStore`, `sshconn.Manager.StartReconnect`, `<hub_state_root>/attached-hosts/state.json`).

- [ ] **Step 1: Update `docs/product/subsystems.md`**

- In the **S06 Hub routing and discovery** row, note that `hubcore.AttachedHostsStore` (`attached-hosts/state.json` under `hub_state_root`) records attached remote host identities outside `hub.toml` and `startBackground` triggers non-blocking background reconnect for previously connected hosts while leaving never-connected hosts dormant.
- In the **S07 Remote host lifecycle** row, note that `sshconn.Manager.StartReconnect` owns startup background reconnection inside the existing supervisor lifecycle with explicit-Connect semantics (allowing stopped compatible remote hubs to start) and bounded backoff, handing off to mid-run attach-only supervision once connected, while `Remove`, `Update`, and open teardown remnants clear or fence startup reconnect.

- [ ] **Step 2: Update `docs/evener-hub-remote-operations.md`**

- Under **Managed execution hosts and deployment**, document in present tense that the controller remembers connected remote hosts under `<hub_state_root>/attached-hosts/state.json` across restarts and automatically reconnects them in the background on startup (including starting stopped compatible remote hubs), while never-connected configured hosts remain dormant and removed or edited hosts stay disconnected until reconnected.

- [ ] **Step 3: Verify docs and run verification checks**

Run:
1. `make docs-check` (if present) or verify all relative markdown links in the two modified docs resolve to real files.
2. `go test -race ./cmd/evener-hub/internal/hubcore ./cmd/evener-hub/internal/sshconn -count=1`
3. `go test ./cmd/evener-hub -run '^Test(AttachedHostsPersister|SelectStartupReconnectHosts|RunMain|HostManage|HostAttach)' -count=1`

Expected: All pass with exit 0.

- [ ] **Step 4: Commit Task 4**

```bash
git add docs/product/subsystems.md docs/evener-hub-remote-operations.md
git commit -m "docs(hub): document startup auto-reconnect for previously connected remote hosts"
```
