# Crash Tombstone Resume Design

## Problem

After a hub rebuild or restart, `Roster.Refresh` preserves a dead daemon's
rendezvous entry as an `errored` tombstone so the UI can explain the crash.
`hubThreadResume` refreshes the roster under its per-session resume lock, then
currently treats every matching entry as a live daemon started by another
resume. It consequently reads the tombstone's dead endpoint instead of calling
the spawner, producing `local daemon unavailable` with `ECONNREFUSED`.

## Design

Keep errored tombstones in the roster for tree and diagnostic behavior. Narrow
only the resume-lock double-check: reuse a matching roster entry when its status
is not `errored`; otherwise continue to `Spawner.Resume`. This preserves the
concurrent-resume deduplication contract without confusing a crash record with a
running replacement.

No fallback retry is added after a failed endpoint dial. The resume path already
has authoritative roster status at the lock boundary, so making the decision
there fixes the root cause and avoids duplicate spawn attempts.

## Test

Extend the existing appwire `turn/start` auto-resume regression to model the
production state:

- install an unreachable local-daemon entry in the roster with status
  `errored`;
- configure shared `ResumeLocks`;
- submit `turn/start` for the saved local session;
- assert the spawner is called and the turn succeeds through the replacement
  daemon.

The test must fail before the production change because the lock double-check
reuses the errored entry and returns the stale-endpoint dial failure.

## Scope

Only `cmd/serf-hub/app_threadlifecycle.go` and the focused regression in
`cmd/serf-hub/app_rpc_test.go` change. The frontend websocket fixes and unrelated
adversarial-review findings remain out of scope.
