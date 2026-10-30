# Detached TMPDIR fallback

## Scope and acceptance

Fix issue [#4423](https://github.com/prime-radiant-inc/evener/issues/4423) in
`LocalExecutionEnvironment.DetachCommand`. Detached commands must keep a usable
inherited temp-directory choice when provisioning the session temp container
fails, without inheriting the session scratch whose lifetime they outlive.
The issue's requested behavior is the approved scope for this unattended fix.

Only the detached-environment construction in `agent/execenv/local.go`, its tests,
this specification, and the matching environment contract in
`docs/job-control.md` change. The owning-guide correction is the approved review
amendment to the initial scope. Attached command construction and sandbox policy
remain unchanged. No client-specific behavior changes.

## Environment contract

Start with the existing `commandEnvironment(envVars)` result and local virtualenv
PATH injection. Keep that environment's filtering, precedence, caller overrides,
and unrelated entries unchanged, except for the following detached-only rules:

1. Always remove every `EVENER_SCRATCH_DIR` assignment. Detached processes outlive
   root-session archival, which deletes session scratch.
2. Ask `unsandboxedTmpDir()` for the world-usable session temp container. When it
   returns a nonempty path, remove every previous `TMPDIR` assignment and export
   exactly one `TMPDIR` naming that container. This applies to both unconfined
   and scratch-naming environments and preserves existing successful behavior.
3. When the container is unavailable and `tmpDirNamesScratch()` is false, retain
   the existing `TMPDIR` entry unchanged. In particular, a real temp directory
   inherited from the parent stays available to the detached child. Do not
   validate, canonicalize, create, or replace the inherited path. If no `TMPDIR`
   entry exists, leave it absent rather than exporting an empty assignment.
4. When the container is unavailable and `tmpDirNamesScratch()` is true, remove
   every `TMPDIR` assignment and do not substitute another path. This is the
   scratch-naming exception: an unsandboxed environment with confined file tools
   normally exports its private session scratch as `TMPDIR`. Dropping this
   environment's `TMPDIR` also preserves existing handling of caller overrides
   in that shape, rather than introducing a new detached temp-policy exception.
5. Temp provisioning remains best-effort and its failure remains sticky. The
   detached launch must not fail merely because the container is unavailable.
   Existing container retention, process disowning, launch receipts, stream
   handling, and cleanup ownership remain unchanged.

The four principal cases are:

| Container | Environment temp shape | Existing TMPDIR | Detached TMPDIR |
| --- | --- | --- | --- |
| Available | Either | Any or absent | Container, exactly once |
| Unavailable | Unconfined | Real inherited temp dir | Same inherited value |
| Unavailable | Scratch-naming | Session scratch | Absent |
| Unavailable | Unconfined | Absent | Absent |

`EVENER_SCRATCH_DIR` is absent in all four cases. The fallback is automatic on
that launch; sticky provisioning failure is not retried or turned into a new
recovery workflow. The inherited directory is caller-owned and is not enrolled
in session cleanup.

## Non-goals

- No attached-spawn changes or new environment variables.
- No sandbox redesign, wrapper support for detached commands, or policy changes.
- No new path validation or broader detection of arbitrary scratch-path aliases.
- No changes to scratch/container allocation, retention, sweep, or retry behavior.
- No change to detached command output, process lifecycle, or session archival.

## Verification

Use the existing `detachedRecordingCommandFactory` process-launch boundary and
inspect the `commandRuntimeConfig.Env` actually configured by `DetachCommand`.
Use the inherited-environment seam to supply deterministic variables and real
fixture-owned temp directories. Force actual provisioning failure with
`sandbox.SetWorldTempBasesForTesting(nil)`, not only a pre-set failure flag.
Use the existing world-temp helper for successful provisioning. Keep these
process-global seam tests serial and await each detached receipt's `Done`.
Skip the detached-launch regression outside Linux/macOS, where detached
execution is unsupported even if the POSIX temp container is supported.

Add cases for successful container replacement (including scratch-naming), a
real inherited TMPDIR surviving failure, scratch-naming TMPDIR removal on
failure, and absent TMPDIR staying absent. Assert exact entry presence/counts,
always-absent scratch export, unchanged unrelated variables and PATH/caller
precedence, and successful detached launch. Retain every existing assertion.
The inherited-value regression must fail before the implementation changes and
pass afterward; record both command results in the PR evidence.

Run the new regression, `go test ./agent/execenv/...`, targeted agent detach
tests, and `make build`. Inspect the relevant test/build entry points before
running them. Do not run the full suite locally; CI supplies full-suite evidence.
Run two independent competing PAR reviewers against the frozen spec, repeat PAR
against the immutable implementation diff, run simplify-code, commit, run the
branch roborev review, and shepherd the PR. Refresh origin/main before the final
push and merge only with current-head required checks green and every roborev
finding low or conclusively refuted.
