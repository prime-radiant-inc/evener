# Relay Recovery Thread Resync Design

## Problem

An open WebUI session can keep stale thread state after its backing daemon is
rebuilt or restarted while the browser remains connected to the hub. The hub's
relay eventually subscribes to the replacement daemon and resumes forwarding
notifications, but relay recovery discards the replacement subscription's
current thread snapshot. If the replacement daemon's turn became active before
the subscription landed, the browser never receives the missing active status,
turn identity, queue state, or capabilities.

This produces an internally inconsistent page: later notifications still drive
the liveness display, while the composer continues routing against the old
ended or unavailable snapshot and reports `Send is not available for this
session`. A full page reload fixes the symptom because it performs a fresh
`thread/read`.

## Goal

After a hub relay successfully reconnects to a thread source, refresh only the
affected tracked thread models so an already-open page converges to the
replacement daemon's authoritative snapshot without a page reload.

## Design

Add a hub-originated `serf/thread/resync` AppWire notification with `ref` and
`threadId` fields.

The relay supervisor broadcasts this notification to the relay's existing
subscribers immediately after a recovery subscription succeeds. It does not
emit on the initial subscription and does not carry a thread snapshot.

The frontend thread store handles the notification outside the normal
incremental reducer. If the ref is currently tracked as an open thread, a
watched child, or both, it performs the same targeted `thread/read` hydration
used for AppWire reconnect recovery:

- request an authoritative snapshot with the existing read parameters;
- buffer matching notifications while the read is in flight;
- replay those notifications over the snapshot;
- replace the stale model wholesale;
- preserve independent open-thread and watched-thread refcounts and richness.

Untracked refs cause no read. Repeated relay recoveries may each trigger a new
targeted refresh because each one represents a new gap in notification
continuity.

## Why a Targeted Notification

`serf/tree/changed` is intentionally broad: roster, past-index, archive,
favorite, rename, and project-delete changes all emit it. Rehydrating every
open transcript for that event would add unrelated full thread reads,
particularly costly on cellular connections.

Broadcasting the complete `Thread` in the recovery notification would duplicate
`thread/read` hydration, enlarge the push payload, and bypass its existing
notification-buffering behavior. A narrow resync hint reuses the authoritative
path already responsible for reconnect recovery.

## Error Handling

The targeted rehydrate is best-effort, matching existing AppWire reconnect
recovery. A failed read keeps the stale model instead of deleting it. A later
relay recovery, AppWire reconnect, pane remount, or manual reload can retry.

The relay continues forwarding notifications regardless of whether a client
successfully processes the resync hint.

## Testing

Backend tests will prove that:

- initial relay subscription does not emit a resync hint;
- closing the source notification stream and successfully subscribing again
  emits exactly one targeted hint to existing subscribers;
- notification relay continues after that hint.

Frontend tests will prove that:

- a tracked stale ended model receives `serf/thread/resync`, performs one fresh
  `thread/read`, and is replaced by the active replacement snapshot;
- notifications arriving during that read are buffered and replayed;
- an untracked ref does not trigger a read;
- the refreshed active state restores queue-mode submit routing without a page
  reload.

All tests use scripted sources or the existing fake AppWire client. Default
tests perform no live provider or network-dependent model calls.
