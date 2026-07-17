# Serf Hub Web UI Data-Path Corrections

**Date:** 2026-07-15<br>
**Status:** Approved for implementation planning

## Scope

Correct three production defects in the Serf Hub Web UI:

1. reloading a local session does work proportional to its full history before returning the latest 40 turns;
2. a running in-process subagent renders as `ended` in its workspace; and
3. a healthy browser-to-Hub connection can silently lose its Hub-to-daemon transcript relay.

The defects occur in adjacent data paths but have independent causes. The correction changes each failing boundary without replacing AppWire or making in-process children independently routable.

## Evidence

### Reload latency

The browser requests `thread/read` with `turnLimit: 40`. The local daemon evaluates `appAllTurns` before `WindowTurns`, and `thread/turns/list` does the same before `PageTurns`. `appAllTurns` replays every retained notification and obtains the complete projected transcript. The transcript cache invalidates on each append because its key includes file size and modification time.

On a 10.48 MB active transcript, direct daemon reads measured 121 ms cold and 17–25 ms warm for 40 turns. Through the Hub, the same request measured 129 ms cold and 35–42 ms warm. The response was bounded, but the upstream work was not. Larger saved transcripts in the local corpus exceed 160 MB.

The server-rendered workspace also calls `ReadThread(IncludeTurns: true)` only to obtain capabilities and the active turn ID. Those fields already live on `Thread.Serf`; this duplicates transcript work before browser hydration.

### Subagent lifecycle

A parent daemon reports running delegate jobs with child transcript refs. `StatusProber` stores those refs in `LiveEntry.RunningSubagentIDs`. The tree and persisted-thread projections use that set and correctly report the child as active.

An in-process child has no independent rendezvous or roster entry. `workspaceData(childID)` therefore misses `Roster.Find`, falls back to the persisted child, and hardcodes `State: "ended"`. A live reproduction showed one child simultaneously reported as running by its delegate job, active in `/api/tree`, and ended in its workspace.

### Relay stalls

A Hub relay subscribes to a daemon notification channel. A daemon transport failure closes the AppWire client notification channel; `LocalDaemonSource` converts that into a closed output channel. The relay then exits and deletes its registry entry without notifying the browser or removing the browser's subscription.

The browser-to-Hub socket remains healthy and answers heartbeat pings. No component restarts the inner relay until the renderer's 180-second stall detector rereads the thread. Browser send-queue overflow is not this failure: overflow unregisters and disconnects the browser.

## Goals

- Paint the latest transcript window without first projecting the full history.
- Keep limited read cost proportional to the requested page and newly appended data, not total history, after index creation.
- Preserve turn order, item projection, stable turn IDs, and existing cursor behavior.
- Report an in-process child's lifecycle independently from its routability.
- Restore a daemon relay after a recoverable inner-hop disconnect without replacing the browser connection.
- Stop all relay work after the last downstream subscriber leaves.

## Non-goals

- Replace AppWire or change public request and response shapes.
- Make in-process children independently addressable daemon threads.
- Stream a child's transcript directly from its parent daemon in this change.
- Rewrite the transcript format.
- Remove browser heartbeat or renderer self-healing.
- Optimize Codex-native paging, which does not use the local daemon transcript path.

## Chosen Architecture

### 1. Indexed, incremental local transcript projection

Add an append-aware transcript index behind `internal/apptranscript`. The index records enough information for each wire-visible turn to locate and project that turn without decoding earlier transcript records:

- logical turn ordinal;
- byte offset and byte length;
- transcript sequence number and record kind; and
- whether an `api_call` record emits a failed turn.

The index also records the synthetic prelude, when present. Logical ordinals remain the source of existing `turn_N` IDs. AppWire cursors remain decimal logical boundaries, so browser and Hub callers need no protocol changes.

The index is keyed by transcript path and validated against file identity, indexed size, and modification state. Transcript files are append-only during normal operation:

- if the file is unchanged, reuse the index and projected pages;
- if it grew, scan only the suffix after the indexed byte offset;
- if it shrank, its identity changed, or the suffix is inconsistent, discard and rebuild the index;
- ignore an incomplete final JSONL line until it becomes complete.

The active daemon keeps the index in memory and persists it beside the transcript so a process restart can serve the tail without rescanning a large file. Transcript append succeeds independently of index persistence. If the sidecar lags or is corrupt, the reader repairs the missing suffix or rebuilds from the transcript; the transcript remains the source of truth.

Expose bounded operations from the cache instead of returning the complete turn slice:

- latest `N` turns plus the older cursor;
- the page ending at a logical cursor; and
- a complete projection for legacy callers and equivalence tests.

A bounded operation reads and projects only the selected records after index validation. It reads the short transcript prelude separately when needed. It does not allocate the full turn graph.

The daemon replaces:

```go
WindowTurns(s.appAllTurns(thread.ID), params.TurnLimit)
PageTurns(s.appAllTurns(thread.ID), params.Cursor, params.Limit)
```

with bounded snapshot methods. The snapshot chooses between transcript and notification data without eagerly materializing both. Notification-derived state becomes an incremental reducer keyed by the notifier sequence. New notifications advance the reducer; reads take a bounded snapshot. If transcript turns are the authoritative richer source under the existing `useTranscriptTurns` rule, only the requested transcript page is projected.

Legacy full reads (`TurnLimit <= 0`) retain current semantics. Both the local daemon and the Hub's saved-session reader use the indexed bounded operations, so active and ended session reloads share the same complexity guarantee.

### 2. Transcript-free server-rendered workspace metadata

Change `liveWorkspaceSnapshot` to request `IncludeTurns: false`. Capabilities and `Serf.ActiveTurnID` are present without turns. The initial HTML keeps the same controls and active-turn state but no longer blocks on transcript projection.

Browser hydration remains responsible for loading the latest 40 turns. Notifications continue to buffer between subscription establishment and hydration, preserving the no-gap initial-load behavior.

Task-description hydration must not block transcript replay. Renderer initialization may request task descriptions concurrently, but it flushes the transcript snapshot and buffered AppWire events as soon as the thread read resolves. Task labels update later when `serf/tasks/list` returns. A slow or failed task request cannot delay transcript paint or live events.

### 3. Lifecycle state separate from routability

Keep the existing roster model:

- `Roster.Find(id)` means an independently routable live daemon session;
- `Roster.IsSubagentActive(id)` means a parent-owned in-process child has a running delegate job.

When `workspaceData` finds a persisted local child but no direct roster entry, it checks `Roster.IsSubagentActive(id)` before assigning a state:

- active child: `State: "active"`, active state label;
- otherwise: `State: "ended"`, ended state label.

This changes lifecycle presentation only. The child remains `Live: false`, keeps non-live capabilities, and does not enter source routing. `/state`, initial workspace HTML, the sidebar tree, and persisted-thread reads then use the same lifecycle truth.

The design deliberately avoids equating `active` with routable. Callers that need actions or live reads continue to use the `Live` flag and capabilities.

### 4. Supervised Hub-to-daemon relays

A relay handle owns the logical source/thread subscription for as long as at least one browser connection subscribes to its relay key. The handle survives individual upstream channel closures.

After the first successful subscription, the relay loop:

1. forwards notifications unchanged, including existing local output-image enrichment;
2. treats an unexpected upstream channel close as a recoverable inner-hop loss;
3. calls `Source.SubscribeThread` again with the same ref and thread identity;
4. waits with bounded exponential backoff after subscribe failures; and
5. resumes forwarding on the same browser subscription when a new upstream channel opens.

Retry delay starts short, grows to a fixed maximum, and resets after a successful subscription or received notification. Tests inject the retry clock; production code does not use sleeps in deterministic tests.

The supervisor exits when:

- its context is cancelled explicitly;
- the relay is replaced by a different handle; or
- the idle check confirms zero downstream subscribers while holding the existing race guard.

On exit it cancels the current source subscription and removes only its own registry entry. A concurrent `thread/read` joins the existing ready relay rather than creating a duplicate.

Initial subscription failure remains visible to the initiating RPC, preserving current request semantics. Recovery retries begin only after the relay has once become ready. This avoids reporting a successful subscription that never existed.

Browser heartbeat and the 180-second renderer self-heal remain as defense in depth for failures outside the supervised inner hop.

## Data Flows

### Reload

1. The Hub renders workspace metadata with `IncludeTurns: false`.
2. The browser opens or reuses its Hub WebSocket.
3. `thread/read(turnLimit: 40, subscribe: true)` establishes the downstream subscription.
4. The daemon validates or advances the transcript and notification indexes.
5. The daemon projects only the latest 40 authoritative turns and returns the prior logical cursor.
6. The renderer paints the snapshot and immediately flushes buffered notifications.
7. Task labels hydrate independently.
8. Older-scroll requests project only the requested page.

### Running in-process child

1. The parent daemon reports a running delegate job with the child ref.
2. `StatusProber` updates `RunningSubagentIDs`.
3. The tree, persisted-thread read, workspace, and `/state` all derive lifecycle from that set.
4. Routing still fails for the child itself because it has no daemon entry; action capabilities remain non-live.
5. When the job leaves the running set, the next status refresh renders the persisted child as ended.

### Relay recovery

1. Browser subscription creates or joins one relay handle.
2. The relay subscribes to the daemon and becomes ready.
3. The daemon channel closes unexpectedly.
4. The handle remains registered and retries the upstream subscription.
5. The browser connection and subscription remain unchanged.
6. The replacement upstream channel's next notification reaches the browser.
7. The handle retires when the last browser subscriber leaves.

## Error Handling

- A transcript sidecar is an acceleration structure, never authoritative data.
- Corrupt, stale, truncated, or missing sidecars trigger repair or rebuild; they never produce partial fabricated turns.
- A full-read caller may fall back to the complete reference projector during rebuild.
- A bounded read returns an error only if the underlying transcript cannot be read safely; malformed JSONL records retain the current skip behavior.
- Relay retries do not synthesize transcript notifications or hide an initial subscription error.
- Repeated recovery failure consumes one bounded retry loop per logical relay, not one loop per browser.
- Lifecycle state does not grant capabilities. A parent-owned active child remains read-only and non-routable.

## Compatibility Constraints

- Preserve AppWire methods and JSON fields.
- Preserve `turn_N` identities, oldest-first page order, item projection, prelude behavior, and failed `api_call` turns.
- Preserve decimal cursor semantics accepted by `WindowTurns` and `PageTurns`.
- Preserve `TurnLimit <= 0` as a legacy full read.
- Preserve `ReplaceSubscription` behavior and one logical relay per source/thread.
- Preserve image enrichment for local notifications and paged transcript items.
- Preserve existing tree semantics: an in-process child may have `State: active` and `Live: false` simultaneously.
- Default tests remain deterministic and require no provider credentials, network, quota, or ambient daemon.

## Test Plan

### Transcript index and bounded reads

Add package-level tests that compare bounded indexed output with `TurnsFromFile` over fixtures containing:

- header and prelude data;
- normal entries;
- ordinary and failed `api_call` records;
- malformed records;
- an incomplete final line;
- appended records;
- truncation and replacement; and
- records larger than the scanner's default buffer.

Assertions cover exact turn IDs, items, timestamps, usage, order, older cursors, next cursors, and full-read equivalence.

Add instrumentation hooks in tests to count bytes or records projected. After an initial index exists, append one record to a large transcript and assert latest-40 reads inspect only the suffix plus the selected 40 records. Page reads must not project records outside the requested page.

Add benchmarks with fixed page size and increasing history. Report time and allocations for:

- cold index build;
- unchanged latest-40 read;
- latest-40 after one append; and
- a 30-turn older page.

### Workspace reload

Add server tests that assert:

- `liveWorkspaceSnapshot` requests `IncludeTurns: false`;
- capabilities and active turn ID remain present; and
- latest-window and older-page responses match the reference full projection.

Add a renderer test where `serf/tasks/list` remains unresolved while `thread/read` resolves. The transcript snapshot and a buffered live event must render before tasks resolve.

### Subagent lifecycle

Add focused tests with one parent roster entry and one persisted child:

- the parent's `RunningSubagentIDs` contains the child: workspace and `/state` return `State: active`, `Live: false`, and non-live capabilities;
- the running ID is absent: both return `State: ended`;
- an unrelated running child does not affect the target; and
- a direct child daemon entry continues through the normal live branch.

### Relay supervision

Use a scripted source and injected retry clock:

1. establish a relay and receive event A;
2. close the upstream channel;
3. wait on a subscribe-call signal;
4. provide a replacement channel;
5. send event B; and
6. assert the same browser connection receives A and B.

Also assert:

- no duplicate relay under concurrent reads;
- initial subscribe failure still fails the initiating RPC;
- repeated recovery failures back off within configured bounds;
- successful recovery resets backoff;
- cancellation interrupts a pending retry;
- zero subscribers stop retries and remove the handle; and
- an old handle cannot delete its replacement.

## Acceptance Criteria

1. Initial workspace metadata performs no turn read.
2. Task hydration cannot delay snapshot paint or buffered live-event replay.
3. Latest-40 and older-page reads return byte-for-byte equivalent AppWire turn data and compatible cursors relative to the reference full projection.
4. After index creation, appending one transcript record does not rescan or reproject the historical prefix.
5. A bounded read does not allocate a full transcript turn graph.
6. A parent-owned running child renders `active` in workspace and `/state` while remaining `Live: false` and non-routable.
7. Removing that child from `RunningSubagentIDs` renders it ended.
8. After a post-subscribe daemon channel closure, the same browser subscription receives notifications from a replacement upstream channel without waiting for renderer self-heal.
9. Relay recovery stops after cancellation or confirmed zero-subscriber retirement.
10. Focused tests, package tests for every changed module, static checks, and repository lint pass.
