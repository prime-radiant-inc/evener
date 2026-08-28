# Canonical Relay Demultiplexing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`. Follow every red/green checkpoint.

**Goal:** Replace per-relay-key upstream listeners with one listener per owner workspace and route each notification once to its authoritative downstream key.

**Architecture:** `RelaySessionSource` resolves a mandatory canonical `appwire.Ref` before acquisition. The existing `relayedThreads` map continues to resolve downstream keys; a second canonical map deduplicates acquisition. One handle owns per-key generation state and one listener. A routing-key normalizer feeds one downstream demultiplexer.

**Spec:** `docs/superpowers/specs/2026-08-30-canonical-relay-demux-design.md`

## Constraints

- Do not change the public AppWire protocol or Codex relay behavior.
- Preserve the existing `RelayHandoff`/`CaptureSubscriptionWithHandoff` lifecycle, reconnect/resync, unsubscribe, identity replacement, deletion fencing, and malformed/untargeted visibility.
- Keep PR #686's authenticated subscription-debug endpoint and tests.
- Do not call source or appserver methods while holding hub locks.

---

### Task 1: Resolve canonical refs before acquisition

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/source.go`
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/evener-hub/internal/appsource/local_daemon_test.go`
- Modify: `cmd/evener-hub/internal/appsource/relay_session_test.go`
- Modify test sources in `cmd/evener-hub/app_relay_test.go` and `app_relay_alias_test.go`

- [ ] Write failing tests: root and read-only child resolve the same nonempty `appwire.Ref`; thread-ID-only resolution returns the owner ref; invalid targets fail; resolution alone does not acquire a lease.
- [ ] Run red:

```bash
go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemon.*RelaySession' -count=1
go test ./cmd/evener-hub -run 'TestHubRelay' -count=1
```

- [ ] Add the source contract:

```go
type RelaySessionSource interface {
    ResolveRelaySession(appwire.ThreadReadParams) (appwire.Ref, error)
    AcquireRelaySession(appwire.Ref) (RelaySessionLease, error)
}
```

`ResolveRelaySession` reuses `localDaemonWorkspaceRef`. `AcquireRelaySession` reuses/creates by that ref. Add one private resolve-and-acquire helper for legacy `LocalDaemonSource` call sites.

- [ ] Run green:

```bash
go test ./cmd/evener-hub/internal/appsource -count=1
go test ./cmd/evener-hub -run 'TestHubRelay' -count=1
```

- [ ] Commit named files only: `refactor(hub): resolve canonical relay sessions`

---

### Task 2: Share one handle while preserving relay-key lookup

**Files:**
- Modify: `cmd/evener-hub/app_relay.go`
- Modify: `cmd/evener-hub/app_relay_alias_test.go`
- Modify: `cmd/evener-hub/app_relay_test.go`

- [ ] Tighten the root/child regression to require one acquisition and listener. Add a gate-controlled concurrent root/child read test.
- [ ] Add a stale-state test: remap a relay key while an earlier read is held; releasing that read must not affect the replacement state.
- [ ] Run red:

```bash
go test ./cmd/evener-hub -run '^TestHubRelay(SharedSessionAliases|ConcurrentAliases|StaleRelayKey)' -count=1
```

- [ ] Keep `relayedThreads map[string]*hubRelayHandle`. Add `canonicalRelays map[appwire.Ref]*hubRelayHandle` and per-handle state:

```go
type relayKeyState struct {
    commands int
    thread   appwire.Thread
}
```

The handle owns `map[string]*relayKeyState`; state pointer identity is the generation token.

- [ ] Resolve outside locks; install/join a canonical placeholder under `relayMu`; only the winner calls `AcquireRelaySession` and `Listen`. On failure, remove the exact placeholder, wake waiters, and permit retry.
- [ ] Bind each read's relay key directly in `relayedThreads`, increment its state count, and return a release closure tied to that state pointer. Record response metadata only if the state remains current. Do not change `RelayHandoff` or `CaptureSubscriptionWithHandoff`.
- [ ] Run green and race:

```bash
go test ./cmd/evener-hub -run 'TestHubRelay' -count=1
go test -race ./cmd/evener-hub -run '^TestHubRelay(SharedSessionAliases|ConcurrentAliases|StaleRelayKey)' -count=1
```

- [ ] Commit named files only: `refactor(hub): share canonical relay handles`

---

### Task 3: Normalize routing keys and demultiplex once

**Files:**
- Modify: `cmd/evener-hub/app_relay.go`
- Modify: `cmd/evener-hub/app_relay_alias_test.go`
- Modify: `cmd/evener-hub/app_relay_closed_capabilities_test.go`

- [ ] Add a backend behavior table mirroring frontend `notificationRoutingKey`: string `ref` precedence, string `threadId` fallback, untargeted params, malformed JSON, and wrong-typed fields.
- [ ] Extend fanout regressions: root/child exact-once routing; malformed/untargeted visibility under each current key; unknown/foreign valid keys dropped; distinct root/child image metadata; missing metadata does not borrow another key.
- [ ] Run red:

```bash
go test ./cmd/evener-hub -run '^(TestRelayNotificationRoutingKey|TestHubRelaySharedSessionAliases|TestHubRelayRelayKeyImageMetadata)' -count=1
```

- [ ] Replace `relayNotificationTargets` with `relayNotificationRoutingKey`. For a target, verify `relayedThreads[relayKey]` still names the handle, copy only that key's metadata, enrich, and broadcast once. For compatibility classifications, snapshot current relay keys and broadcast once under each.
- [ ] Keep deletion-target ownership and acknowledgement behavior unchanged.
- [ ] Run green and race:

```bash
go test ./cmd/evener-hub -run 'TestHubRelay|TestRelayNotificationRoutingKey' -count=1
go test -race ./cmd/evener-hub -run 'TestHubRelay|TestRelayNotificationRoutingKey' -count=1
```

- [ ] Commit named files only: `refactor(hub): demultiplex relay notifications`

---

### Task 4: Retire relay keys and canonical handles safely

**Files:**
- Modify: `cmd/evener-hub/app_relay.go`
- Modify: `cmd/evener-hub/app_relay_test.go`
- Modify: `cmd/evener-hub/app_rpc_test.go`
- Modify: `cmd/evener-hub/internal/appsource/relay_session_test.go`

- [ ] Add deterministic lifecycle tests: inactive child state retires while subscribed root keeps the listener; final unsubscribe closes one lease; subscribe at the idle boundary prevents teardown; one reconnect produces one resync/listener; remap moves ownership; relay-key and canonical stop paths retire only the intended handle.
- [ ] Run red:

```bash
go test ./cmd/evener-hub -run 'TestHubRelay.*(RelayKey|Idle|Reconnect|Remap|Stop)' -count=1
```

- [ ] Idle checks snapshot state pointers/counts, unlock, query subscribers, then revalidate `relayedThreads` ownership, state identity, counts, and subscribers before removal. Retire the canonical handle only after its final state disappears.
- [ ] Keep `stopRelay(relayKey string)` for current call sites. Add a separate canonical teardown helper; neither accepts both namespaces. Clear only relay keys still pointing to the retiring handle.
- [ ] Preserve the relay-recovery contract from `docs/superpowers/specs/2026-07-27-relay-recovery-thread-resync-design.md`; route its existing `ThreadResyncParams` once through the normal routing-key path.
- [ ] Run green and race:

```bash
go test ./cmd/evener-hub ./cmd/evener-hub/internal/appsource -count=1
go test -race ./cmd/evener-hub -run 'TestHubRelay' -count=1
go test -race ./cmd/evener-hub/internal/appsource -run 'TestRelaySession' -count=1
```

- [ ] Commit named files only: `refactor(hub): make relay lifecycle key-aware`

---

### Task 5: Review, verify, and open the stacked PR

- [ ] Run `gofmt` on touched Go files and `git diff --check`.
- [ ] Run affected and repository gates:

```bash
go test ./cmd/evener-hub ./cmd/evener-hub/internal/appsource ./internal/appserver -count=1
go test -race ./cmd/evener-hub -run 'TestHubRelay' -count=1
go test -race ./cmd/evener-hub/internal/appsource -run 'TestRelaySession' -count=1
go vet ./cmd/evener-hub/... ./internal/appserver
make test-web
make test-web-browser
make build
make lint
```

- [ ] Request independent review of canonical ownership, routing, lock order, reconnect, remap generations, idle races, and test fidelity. Fix every Critical/Important finding and rerun affected gates.
- [ ] Verify `git status --short --branch`, `git log --oneline fix/hub-relay-alias-dedup..HEAD`, and `git diff --stat fix/hub-relay-alias-dedup...HEAD`.
- [ ] Push `refactor/hub-canonical-relay-demux` and open a PR against `fix/hub-relay-alias-dedup`. Report URL, commits, tests, and retarget-to-`main` requirement after PR #686 merges.
