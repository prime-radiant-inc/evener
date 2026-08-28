# AppWire Daemon Status Design

## Purpose

Replace the hub's remaining daemon `GET /status` reads with bounded, typed
AppWire reads, then remove the legacy HTTP status route. Preserve the hub's
existing liveness and hydration behavior while making running non-agent jobs
observable in the hub's live status projection.

## Scope

This work changes only daemon status reads and the `GET /status` route. It does
not remove or migrate the daemon's other legacy HTTP control routes.

The production consumers in scope are:

- `hubcore.StatusProber`, which resolves the daemon's current root identity and
  state, pending ask/escalation state, descendant identities and states, and
  liveness.
- `WebServer.fetchStatus`, which refreshes a live local workspace's model,
  profile, state, working directory, turn count, context metrics, usage, work
  duration, and active-turn start time.

## Decision

Use the existing typed AppWire `thread/read` and `thread/list` methods. Do not
add a status RPC and do not add a second job model.

- `thread/read` with no turns requested returns the authoritative root thread.
  The probe uses its identity, status, pending ask/escalation state,
  diagnostics, and hydration metrics.
- `thread/list` with subagents included returns the daemon's root plus every
  non-closed in-process descendant. The probe excludes the root and preserves
  each descendant thread's identity and status.
- `EvenerDiagnostics.Jobs` remains the only job inventory. The live hub status
  projection carries `[]appwire.EvenerJobInfo`, filtered to non-terminal jobs
  whose `jobType` is not `delegate`. Delegate identity and status come only
  from descendant threads, so legacy delegate job rows cannot create duplicate
  children.

The root `thread/read` and `thread/list` responses must agree on the root
identity. A disagreement is a failed probe, not a mixed snapshot.

## Typed status projection

`ProbeResult` and `LiveEntry` gain a `RunningJobs []appwire.EvenerJobInfo`
field. This is not a new wire or domain model; it carries the existing AppWire
job rows unchanged, including at least `jobId`, `jobType`, and `status`.

The projection rules are:

1. Read jobs only from the root thread's `Evener.Diagnostics.Jobs`.
2. Exclude rows with `jobType == "delegate"`; descendants already describe
   agent work.
3. Exclude the known terminal statuses `completed`, `failed`, `cancelled`,
   `stopped`, and `exhausted`.
4. Preserve rows with empty or unknown additive fields. Older/partial typed
   data must degrade to zero values rather than fail a live probe.
5. Preserve wire order in the consumer projection. Defensive copies protect
   pointer fields when roster snapshots are copied.

The roster fingerprint includes each projected job's identity, type, and
status so a job start, terminal transition, or status change invalidates the
hub's derived snapshots. The local source's roster-backed `thread/list`
projection includes these jobs under the existing `Evener.Diagnostics.Jobs`
field, making them observable to typed hub consumers before a full
`thread/read` round trip.

## Hydration metrics

The existing AppWire thread snapshot already carries model, profile, status,
working directory, context pressure and counts, usage, work milliseconds, and
active-turn start time. It does not currently carry the daemon's total
completed-turn count.

Add `turnCount` to `EvenerThread` and populate it from the daemon's materialized
status snapshot. This is an additive field on the existing typed thread
contract. It avoids an unbounded `thread/read(includeTurns: true)` solely to
count transcript history. Older producers omit it and consumers receive zero,
matching the existing partial-data behavior.

`WebServer.fetchStatus` maps the root AppWire thread into its existing internal
workspace hydration shape. It requests no turns and returns `nil` on any dial,
authentication, initialization, RPC, or decode failure, preserving the current
fail-soft behavior.

## Transport and liveness

Both reads use the rendezvous entry's WebSocket endpoint and hub bearer token,
the existing AppWire WebSocket transport, the typed client, and the standard
initialize handshake. Calls are bounded by the existing probe timeouts:

- `StatusProber`: configured timeout, default 500 ms.
- `WebServer.fetchStatus`: 1 second.

Any endpoint, dial, authentication, initialization, RPC, empty-root, or
root-identity consistency failure returns the existing zero/`nil` failure
result. `Roster.Refresh` therefore retains the last good live snapshot when a
process is still alive, exactly as it does for a failed HTTP probe today.

There is no REST fallback. Adding one would be backward compatibility and is
outside this approved migration.

## Server cleanup

Remove `/status` registration and its HTTP handler only after both production
callers and the live test harnesses use AppWire. Keep the server's internal
materialized status structs and setters where AppWire projection still uses
them. Remove endpoint-only JSON/security/method tests; migrate behavioral
tests that used `/status` merely as an observation seam to typed AppWire reads.
Do not add a source-text or route-absence test.

## Testing

Follow red-green-refactor in this order:

1. A real AppWire server/client integration test drives a root thread with a
   running shell job, a legacy running delegate job, and descendant threads.
   It must fail until `StatusProber` reads AppWire, must contain the shell job
   with its identity/status, and must not duplicate the delegate as a job.
2. Focused prober tests cover authentication, bounded failures, root identity,
   pending ask/escalation, descendant states, partial diagnostics, and terminal
   job filtering through real AppWire transport where the transport behavior is
   the subject.
3. A real AppWire web hydration test proves `fetchStatus` obtains turn count
   and the remaining metrics without requesting transcript turns.
4. Roster/local-source tests prove running jobs are defensively carried into
   the typed consumer projection and affect snapshot invalidation.
5. Existing serve/integration tests that need a live state observation use a
   real AppWire client instead of HTTP polling.

After focused green tests, run generated-output refresh if the AppWire type
change requires it, then `make lint`, `make vet`, `make test`, and the canonical
`make merge-approval-gate`. This is Go/protocol work with no touched frontend
`src/` files, so `make test-web-browser` is not a claimed gate unless the final
diff expands into browser behavior.

