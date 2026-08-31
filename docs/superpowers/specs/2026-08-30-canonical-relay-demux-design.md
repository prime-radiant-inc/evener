# Canonical Relay and Downstream Demultiplexing

## Status

Approved direction. This design follows the immediate root/child alias fanout fix in PR #686.

## Problem

The hub currently keys `hubRelayHandle` by a downstream thread alias. A root session and its in-process descendants use different aliases, but `LocalDaemonSource` maps them to one owner-daemon `RelaySession`. The hub therefore creates several listeners over one upstream stream. Each listener sees every notification, and each handle must filter the stream before broadcasting.

PR #686 stops duplicate delivery by filtering each handle. That repair is intentionally narrow. It leaves redundant listeners, acknowledgements, goroutines, and per-frame target decoding in place.

## Goals

1. Maintain one upstream listener per canonical relay session.
2. Route each targeted notification once, using its authoritative `ref` first and `threadId` second.
3. Preserve independent downstream subscriptions for root and child aliases.
4. Preserve atomic snapshot-to-live handoff semantics for every alias read.
5. Preserve reconnect, identity replacement, unsubscribe, and idle teardown behavior.
6. Preserve local output-image enrichment with the target thread's metadata.
7. Keep malformed or untargeted notifications observable without weakening targeted routing.

## Non-goals

- Change the public AppWire protocol.
- Merge root and child thread models.
- Change Codex relay behavior. Codex does not implement `RelaySessionSource`.
- Add payload-based client deduplication.

## Current data flow

For each alias read, `threadRelayTarget` derives a downstream key such as `local:root` or `local:child`. `newHubRelayFunctions` stores a handle under that key. Each handle acquires a lease and calls `Listen`.

`LocalDaemonSource.AcquireRelaySession` instead keys the upstream actor by `WorkspaceRef`, so root and child leases share one `relaySession`. `relaySession.publishNotification` sends each notification to every listener. The hub then broadcasts once per alias handle.

The immediate fix filters those per-handle deliveries. The canonical design removes the redundant handles and listeners.

## Design

### 1. Give each lease a canonical key

Extend `appsource.RelaySessionLease` with:

```go
RelaySessionKey() string
```

`LocalDaemonSource` returns the canonical workspace ref already used by its `relaySessions` map. Test leases return an explicit key. An empty key falls back to the downstream relay key for defensive compatibility.

The hub may need to acquire a short-lived candidate lease before it knows the canonical key. If a handle already owns that key, the hub closes the candidate without calling `Listen` and uses the existing handle. Acquiring a lease is in-memory; connection establishment still happens only for the winning handle.

### 2. Key atomic handles canonically

For `RelaySessionSource` sources, `relayedThreads` uses the lease's canonical key. Non-atomic sources keep their current downstream key.

Each canonical handle owns:

- one lease and one upstream listener;
- the source ID;
- a set of downstream alias keys;
- per-alias `appwire.Thread` metadata;
- command ownership and readiness state.

A read still calls `handle.lease.Read` with the requested alias. The relay session's command gate serializes reads and preserves each read's snapshot/handoff cut. After the read, the handle records the authoritative response metadata under the downstream alias.

### 3. Demultiplex once at fanout

The single fanout loop extracts the notification target:

1. valid `ref` wins;
2. otherwise, nonempty `threadId` is combined with the source ID;
3. malformed or untargeted payloads use the compatibility fallback below.

For a known target, the hub calls `server.Broadcast` exactly once under that downstream key. A client subscribed to root and child receives a root frame once and a child frame once.

For malformed or untargeted payloads, the hub broadcasts once under every registered alias. This preserves the old visibility behavior for protocol-invalid frames while keeping the valid, cataloged path single-delivery.

### 4. Keep alias metadata on the canonical handle

The handle stores the latest `appwire.Thread` response per alias. Fanout uses the targeted alias's metadata for local output-image enrichment. If metadata is absent, it falls back to the canonical handle's last known thread, matching today's best-effort behavior.

An identity remap moves the alias from its old canonical handle to the new one. The old handle no longer counts that alias during idle checks.

### 5. Idle teardown spans aliases

A canonical handle remains live while either condition holds:

- a command owns it;
- any registered alias has downstream subscribers.

The idle check snapshots the alias set and sums `server.SubscriberCount(alias)`. When all counts reach zero, it removes the canonical handle, removes alias-to-canonical mappings, and closes the lease once.

Unsubscribing one child cannot retire a root relay that still has subscribers. Removing the last alias subscriber allows the existing idle timer to retire the handle.

### 6. Stop and recovery lookup

Maintain `aliasToCanonical map[string]string`. `stopRelay` accepts either a canonical key or a downstream alias, preserving current test and recovery call sites.

Reconnect remains owned by `relaySession`. A recovered canonical listener emits one resync notification through the demultiplexer. The notification's authoritative target selects the downstream alias; aliases that need snapshot replacement still follow their existing `evener/thread/resync` read path.

## Concurrency and lock order

- Resolve/acquire the candidate lease without `relayMu`.
- Hold `relayMu` only to install/find handles, update alias maps and metadata, and count command owners.
- Never call `Listen`, `Read`, `Close`, `server.Broadcast`, or `server.SubscriberCount` while holding `relayMu`.
- Snapshot aliases/metadata under `relayMu`, then perform routing and subscriber-count calls after unlocking.
- Close losing candidate leases after unlocking.

This keeps external callbacks outside the hub registry lock and preserves the existing appserver lock order.

## Failure handling

- If candidate acquisition fails, no registry state changes.
- If the winning handle fails before readiness, waiters may install their still-open candidate under the same canonical key.
- If a target cannot be parsed, compatibility fanout keeps the frame visible.
- If an alias remaps while an old handle is draining, handle identity checks prevent stale fanout from publishing as the new owner.

## Verification

### Required deterministic tests

1. Root and child aliases acquire one canonical handle and one listener.
2. A root+child client receives one root delta; a root-only client receives one.
3. A root+child client receives one child delta; a root-only client receives none.
4. Thread-ID-only reads route notifications by the authoritative response ref.
5. Malformed and untargeted notifications retain compatibility fanout.
6. Concurrent root/child reads install one canonical handle.
7. Unsubscribing one alias keeps the handle alive while another alias is subscribed.
8. Removing the final alias subscriber retires the handle and closes one lease.
9. Reconnect emits one resync and resumes one upstream listener.
10. Alias identity remap moves subscriber accounting to the new canonical handle.
11. Target-specific image enrichment uses the correct alias metadata.

Run targeted tests red before implementation, then the hub/appsource race tests, full affected packages, vet, frontend gates, build, and lint.

## Delivery

Implement on `refactor/hub-canonical-relay-demux`, stacked on PR #686. Open the second PR against `fix/hub-relay-alias-dedup` until #686 merges; then retarget it to `main`.
