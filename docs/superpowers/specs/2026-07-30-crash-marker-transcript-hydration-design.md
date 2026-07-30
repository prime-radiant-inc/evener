# Crash Marker Transcript Hydration Design

## Problem

`Roster.Refresh` correctly recognizes a rendezvous file whose daemon PID is
gone and retains it as an `errored` crash marker. The retained record serves a
useful diagnostic purpose: the workspace rail can distinguish a crashed
session from one that exited normally.

The Hub currently feeds every roster record into `LocalDaemonSource`, including
that dead record. A subscribed WebUI `thread/read` therefore dials the stale
endpoint and fails instead of hydrating the saved transcript. If the dead
record is omitted without any other change, the read still fails because the
atomic-rejoin guard forbids saved-transcript fallback for every
`RelaySessionSource` failure, including the pre-handoff "no live entry exists"
case.

## Design

Represent crash provenance explicitly on `hubcore.LiveEntry` with a `Crashed`
boolean. `Roster.Refresh` sets it only when a liveness probe failed and the PID
is confirmed gone. A reachable daemon may still report status `errored`; status
alone is not evidence that its endpoint is dead.

Keep crashed entries in `Roster.List` and `Roster.Find` for tree and diagnostic
consumers. Exclude only `Crashed` entries when `newHubSourceRegistry` builds the
current local-daemon routing set. The existing resume-lock double-check will
also use `Crashed` rather than treating every `errored` daemon as a tombstone.

For subscribed `thread/read`, permit the saved-transcript fallback when the
local source reports its precise "thread not found" condition after routing
found no live entry. Continue rejecting fallback after a real canonical actor,
dial, read, or handoff failure. This preserves authoritative rejoin: a success
from a live source still means the snapshot has a live continuation.

## Invariants

- Crash markers remain visible as `errored` in the workspace tree.
- A live daemon reporting `errored` remains routable.
- A crashed daemon endpoint is never dialed.
- A saved local session with no routable daemon can hydrate with
  `subscribe:true`.
- A routable daemon whose actor or endpoint fails cannot be masked by a stale
  saved transcript.
- Resume serialization reuses only a current-protocol, non-crashed daemon.

## Verification

Use the real `Roster`, `LocalDaemonSource`, Hub WebSocket handler, and persisted
transcript fixture:

- prove a dead PID becomes `Status == "errored"` and `Crashed == true`;
- prove a subscribed read with a crash marker returns the persisted turns;
- preserve the existing canonical-actor failure regression;
- prove a reachable `errored` daemon is still routed;
- run focused and package tests, the naming checker, and a fresh-session
  Hub restart exercise.

