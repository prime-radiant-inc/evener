# Hub install and restart, split into two actions

**Date:** 2026-10-21
**Branch:** `hub-install-restart-split` (base `64c0a81d5a`)
**Status:** approved; implemented in this branch

## Goal

Make "install a new hub binary" and "restart the hub into the binary on disk"
two deliberate actions in Settings, on the phone, and in the CLI. The hub never
runs `git pull`, `make`, or any other build step.

## Install

Install is the existing `evener/upgrade` path: download a release or snapshot
artifact, verify it, and write it. It does not restart the hub. No server change.

On dev builds the client hides Install, as today.

## Restart

Add `evener/hub/restart`, scope `ScopeHub`. Params are empty. The response is
`{restarting, incarnation, currentVersion, targetVersion}`. `incarnation` is
exactly the old process's `/api/health` `started_at` string.

1. At startup, resolve the launch entrypoint from `argv[0]` against the startup
   cwd and PATH, and require it to refer to the running executable. Store the
   path. Restart uses the stored path, never a request-time resolution.
2. Refuse before answering when the platform has no in-place exec, when the
   path is missing or not a regular executable, or when the target probe fails.
3. Probe the target with `target --version`, bounded by a 2-second deadline,
   `/dev/null` stdin, and a small output cap. Require the output to start with
   `evener ` and parse the version token. A non-Evener program, a malformed
   output, or a timeout is a refusal.
4. Pin the target file's SHA-256. Take the in-process update mutex. When the
   launch path has a managed install layout, also take the existing
   cross-process install lock around verify and exec. For a worktree or ad-hoc
   path there is no managed layout; the pinned digest is re-verified immediately
   before exec and a change aborts the restart. If a managed installation is
   read-only and has no lock file, the restart continues without the lock and
   relies on that digest re-verify.
5. Schedule the existing in-place `exec` after the response. It keeps the PID,
   arguments, cwd, environment, and stdio; the lock and listener are released by
   the exec and re-acquired by the new image.
6. If the exec fails, the old hub keeps running and the locks are released.

## Restart confirmation

Confirm by process incarnation, not build version: `/api/health` already reports
`started_at`. A rebuild at the same commit changes no version string, so a
version comparison would misreport a successful restart as a timeout.

- Web: capture `started_at`, poll `/api/health` until it changes, then reload.
  After 30 seconds, show a not-back state with a retry. The page stays usable
  when the old process still answers, and the app does not claim the hub is
  down when it cannot tell.
- Phone: take the baseline from the update check, reconnect, and compare
  `started_at`. Add a 30-second deadline and a terminal "didn't come back" state
  with retry, instead of waiting forever.
- CLI: capture `started_at` from `/api/health` before sending, then poll until
  it changes. A lost response is ambiguous: keep polling and succeed only on a
  changed incarnation. Fail on a refusal, a timeout, or no answer.

## Update check additions

`evener/update/check` gains these fields, keeping every existing field and
meaning:

- `onDiskCommit` and `onDiskError`: the version token from the bounded target
  probe, with a `-dirty` suffix preserved so a dirty on-disk build still reads
  as a pending restart, or the probe failure.
- `installAvailable`: the same commit-prefix comparison the check already uses
  for `updateAvailable`, computed against `onDiskCommit` with the `-dirty`
  suffix removed. False when the probe fails, when the on-disk commit is empty,
  or on a dev build.
- `restartPending`: true when `onDiskCommit` differs from `currentVersion`, or
  when the launch binary's digest differs from the digest recorded at startup
  (a same-version rebuild).
- `startedAt`: the hub process incarnation, exactly the value `/api/health`
  reports as `started_at`.
- `restartSupported`: true only when the platform supports in-place exec and the
  startup launch path is usable.
- `checkError`: a channel-lookup failure. The local fields stay populated, and
  the clients keep Restart available instead of hiding it behind the failure.
  A client older than `checkError` reads `applicable=true` with
  `updateAvailable=false` as "up to date" during the failure. That
  degradation is accepted; the response shape is not negotiated for older
  clients.
- `installTarget` and `installTargetMatchesLaunch`: where Install would write
  the `evener` binary and whether that path resolves to the running launch
  path. Install is unavailable when it does not.

The server computes these before the dev-build early return, so dev builds get
the on-disk build and restart state without a network request.

An older hub omits the new fields. A new client treats the on-disk commit as
unknown, sets `restartPending=false`, and offers the deprecated combined
`evener/update/apply` action (install then restart) instead of the two steps.
An explicit `restartSupported=false` is not an older hub: it keeps the
two-step UI and the install-target guard. The combined path propagates
`checkError` before reporting `restarting:false`.

After install, run a fresh check. The probe sees the new on-disk build, so
`installAvailable` is false and `restartPending` is true.
A lost install response sets `installReconcile`; the host runs that fresh check
when its connection is ready, rather than racing a reconnect.

## Settings and phone

- Always show the running build, the on-disk build (or the probe error), and the
  channel. The running build comes from the check's `currentVersion` and
  `currentCommit`, not the Settings overview's product `Version` (`0.1.0`).
- Release and snapshot: Check, Install (enabled only when `installAvailable`),
  Restart (only when `restartSupported`). The restart confirm names the running
  and target builds.
- Dev builds: hide Install, keep the "Rebuild with `make build-hub`" note, show
  Restart when supported.
- Disable both actions during either operation.

The phone uses the same shared controller and the same states.

## Shared client and CLI

`createHubUpdateController` gains `install()` and `restart()`. The
`awaitRestart` port takes `previous: {version, incarnation}` and is documented
as such; the package version bumps to `0.2.0` so an out-of-tree host compiled
against the old signature fails to compile rather than silently comparing a
timestamp as a version. `apply()` stays as a deprecated wrapper around the
retained `evener/update/apply` RPC, preserving the old behavior against older
hubs; the web and phone stop calling it. `restart()` uses the restart
response's incarnation when it arrives, captures `currentIncarnation()` before
the request, and falls back to the check after a lost response when the host
has no health read. A lost install response leaves `installReconcile=true` for
the host to retry when it is ready.

`evener hub restart` reuses `attach`'s config, loopback, and token resolution,
captures `started_at`, sends the RPC, and waits for the new incarnation. It
prints the target version and exits zero only when the new process answers.
It validates the loopback address before reading the token, and the health
probe carries no credential.

## Non-goals

- No build step in the hub and no restart on binary change.
- No supervisor.
- No change to release verification, remote-host deploy, or session daemons.
- No automatic retry of a restart that failed after its response. That part of
  the recorded H10 outcome remains deferred.

## Recovery

- Install failure leaves the running hub and current binary untouched.
- Restart refusal, digest mismatch, or exec failure leaves the old hub running.
- A new binary that crashes at startup leaves the hub down; the client reports
  "not answering" rather than a false success. State and transcripts are safe.
- Config, auth token, state root, and transcripts are not rewritten.

## Recorded friction outcome

`docs/product/friction.md` H10 asks for a pending restart until the new version
runs, a visible installed-versus-running distinction, digest and lock
validation, and automatic retry. This change implements the first three; it
does not auto-retry. Update H10 to record that remaining deferral.

## Test plan

1. Probe and handler: version output accepted; non-Evener, malformed, hanging,
   missing, unsupported-platform, and digest-changed targets refused before or
   during exec with the old hub still running.
2. Same-build restart confirmed by a changed `started_at` on web, phone, and
   CLI.
3. Install gating: the check compares a short on-disk commit with the full
   `latestCommit` using the existing prefix rule; after install, a fresh check
   disables Install and reports `restartPending`; a probe failure and a dev
   build each leave Install off and populate the new fields.
4. CLI: lost response still succeeds if the incarnation changed; old hub
   without the method reports unsupported; timeout and refusal fail.
5. Old hub with a new phone: missing `restartSupported` hides Restart.
6. Phone: a hub that never reconnects reaches the terminal state within the
   deadline.
7. Retained `evener/update/apply` still works for an older client, and the
   deprecated `apply()` still calls it against an older hub.
8. Real re-exec: a deterministic test binary proves the new process serves, the
   PID is preserved, and `hub.lock` is re-acquired.
9. Whole journey: check, install, installed-but-not-running, restart, new
   incarnation, reload or reconnect, on both clients. The `awaitRestart` port
   receives both the version and the incarnation.
10. Regenerate `docs/appwire-protocol.md` and
    `appwire-client/typescript/types.gen.ts`; pass `make lint-generated`.

## Documentation

- `docs/evener-hub.md`: the two-step Settings flow, dev builds, the incarnation
  wait, and `evener hub restart`.
- `appwire/protocol.go`: the restart method description.
- `appwire-client/typescript/README.md`: `install()`, `restart()`, the
  deprecated `apply()`, and the new check fields.
- `docs/product/subsystems.md` S06: one sentence on install/restart ownership.
- `docs/product/friction.md` H10: the remaining deferral.
