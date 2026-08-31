# Canonical Relay and Downstream Demultiplexing

## Status

Approved direction. This design follows the immediate root/child fanout fix in PR #686.

## Problem

The hub currently keys `hubRelayHandle` by the downstream `relayKey`. Root sessions and in-process read-only children have different relay keys, but `LocalDaemonSource` maps them to one owner-workspace relay session. The hub therefore creates several listeners over one upstream stream. PR #686 prevents duplicate delivery by filtering each handle, but redundant listeners, acknowledgements, goroutines, and target decoding remain.

## Goals

1. Maintain one upstream listener per canonical relay session.
2. Route each targeted notification once, using authoritative `ref` first and `threadId` second.
3. Preserve independent downstream subscriptions and atomic snapshot-to-live handoffs for every relay key.
4. Preserve reconnect, identity replacement, unsubscribe, and idle teardown behavior.
5. Preserve local output-image enrichment with the target relay key's metadata.
6. Preserve the visibility of malformed or untargeted notifications.

## Non-goals

- Change the public AppWire protocol or merge root and child thread models.
- Change Codex relay behavior. Codex does not implement `RelaySessionSource`.
- Add payload-based client deduplication.

## Design

### 1. Resolve canonical identity before acquisition

Canonical identity belongs to the source. Reuse the existing comparable `appwire.Ref` instead of adding another string-key concept:

```go
type RelaySessionSource interface {
    ResolveRelaySession(appwire.ThreadReadParams) (appwire.Ref, error)
    AcquireRelaySession(appwire.Ref) (RelaySessionLease, error)
}
```

`LocalDaemonSource.ResolveRelaySession` returns the owner workspace ref already produced by `localDaemonWorkspaceRef`. It rejects an empty ref. `AcquireRelaySession` accepts that resolved ref, so identity cannot change between hub installation and source acquisition.

The hub resolves without `relayMu`, installs or joins a canonical placeholder under `relayMu`, and only the winner acquires a lease and calls `Listen`. Concurrent root/child reads perform one acquisition and start one listener. Non-atomic sources keep their existing per-relay-key path.

### 2. Reuse the relay-key registry

Keep `relayedThreads` in its current role: downstream `relayKey` to `*hubRelayHandle`. Add only `canonicalRelays map[appwire.Ref]*hubRelayHandle` for acquisition deduplication. Several relay keys may point directly to one canonical handle.

Each handle owns one lease/listener and current per-key state:

```go
type relayKeyState struct {
    commands int
    thread   appwire.Thread
}
```

A handle's `map[string]*relayKeyState` records the keys it currently owns. State pointer identity is the generation token: stale command releases and idle checks cannot modify a replacement state.

A read resolves its canonical ref, binds the requested relay key to the handle, and increments that key's command count. It calls the shared lease's `Read` with the original parameters. The existing `RelayHandoff` and `appserver.CaptureSubscriptionWithHandoff` `Prepare`/`Commit`/`Abort` lifecycle remains unchanged; canonicalization changes listener ownership, not the snapshot cut. A successful read records the authoritative response thread only if the key state is still current.

Identity remap atomically repoints `relayedThreads[relayKey]`, removes the old handle's state, and creates state on the new handle.

### 3. Normalize routing keys, then demultiplex

Replace the current boolean `relayNotificationTargets` predicate with `relayNotificationRoutingKey`, a key-returning Go counterpart to frontend `notificationRoutingKey`. Both use the same behavior table: a string `ref` has precedence; otherwise a string `threadId` is combined with the source ID; otherwise the frame is untargeted. Invalid JSON or wrong-typed routing fields are malformed.

For a targeted frame, fanout converts the returned `appwire.Ref` to its relay key, verifies that `relayedThreads[relayKey]` still names this handle, copies that key's metadata, and calls `server.Broadcast` exactly once. A valid key unknown to this handle is not published by this listener.

For malformed or untargeted frames, fanout snapshots the handle's current relay keys and broadcasts once under each. This retains pre-refactor compatibility for subscribers that rely on protocol-invalid frames. The targeted path never scans all keys.

### 4. Enrich only from the routed key

Local output-image enrichment uses only the routed key state's `SessionID` and `CWD`. Missing metadata means best-effort enrichment is skipped; metadata from another key is never borrowed. Retiring inactive key state prevents a long-lived root from retaining historical children.

### 5. Retire inactive keys and handles

A relay-key state remains live while either condition holds:

- one of its reads owns a command reference;
- `server.SubscriberCount(relayKey) > 0`.

An idle check snapshots state pointers and command counts, releases hub locks, and queries subscriber counts. It removes each still-current state with neither owner nor subscriber. It short-circuits once an active key proves the handle cannot retire. The canonical handle retires only after no states remain; retirement deletes the exact canonical placeholder and closes the lease once.

Final removal revalidates `relayedThreads` ownership, state pointer identity, command counts, and subscriber counts. This preserves the existing subscribe-versus-idle race protection. Unsubscribing one child cannot retire a root relay that remains subscribed.

### 6. Keep stop and recovery roles explicit

Existing alias-facing and test call sites continue to use `stopRelay(relayKey string)`, which resolves through `relayedThreads`. Internal canonical teardown is a separate helper accepting an `appwire.Ref` or handle. Neither function accepts both namespaces. Removal clears only relay keys still pointing to the retired handle.

Reconnect remains owned by `relaySession` under `2026-07-27-relay-recovery-thread-resync-design.md`. The only delta here is that one recovered canonical listener routes the existing typed `ThreadResyncParams` notification exactly once through `relayNotificationRoutingKey`; snapshot replacement continues through the established `evener/thread/resync` read path.

## Concurrency and lock order

- Resolve source identity and call `AcquireRelaySession`, `Listen`, `Read`, and `Close` without hub locks.
- `relayMu` protects canonical placeholder installation and `relayedThreads` ownership changes.
- A handle mutex protects its key states, metadata, readiness, and command counts.
- When both are required, acquire `relayMu` before a handle mutex; never hold two handle mutexes simultaneously.
- Never call `server.Broadcast` or `server.SubscriberCount` while holding a hub lock.
- Targeted routing copies one key's metadata. Only compatibility fanout and idle checks snapshot all keys.

## Failure handling

- Resolution or winning acquisition failure leaves no relay-key state behind and wakes placeholder waiters with the error.
- A later caller may replace a failed placeholder and retry acquisition.
- Missing canonical refs are errors; there is no downstream-key fallback.
- Malformed and untargeted frames retain compatibility fanout.
- State identity checks prevent stale reads, idle checks, or draining handles from acting on a remapped relay key.

## Verification

Required deterministic tests:

1. Root and child relay keys resolve one canonical ref, acquire one lease, and start one listener.
2. Root+child and root-only clients receive each root notification once.
3. A root+child client receives each child notification once; a root-only client receives none.
4. Thread-ID-only reads route by the authoritative response ref.
5. Malformed and untargeted notifications remain visible under every current relay key.
6. Routing-key behavior matches the frontend table; unknown/foreign valid keys are not published.
7. Concurrent root/child reads install one handle and listener.
8. Target-specific image enrichment uses only that relay key's metadata.
9. An inactive child key retires while another key keeps the handle alive.
10. Removing the final subscriber retires the handle and closes one lease.
11. Reconnect emits one resync and resumes one upstream listener.
12. Identity remap moves ownership and subscriber accounting; stale releases cannot affect the replacement.
13. Relay-key and canonical stop paths retire the intended handle only.

Run each new regression red before implementation, then the hub/appsource race tests, full affected packages, vet, frontend gates, build, and lint.

## Delivery

Implement on `refactor/hub-canonical-relay-demux`, stacked on PR #686. Open the second PR against `fix/hub-relay-alias-dedup` until #686 merges; then retarget it to `main`.
