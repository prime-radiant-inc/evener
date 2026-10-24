# Issue #4422: the shell schema must not advertise `detached` where the environment refuses it

Status: frozen for review, 2026-10-23. The issue's explicit acceptance criteria authorize this bounded correction.

## The mismatch

`tool.DefShell()` builds the shell tool's `mode` property with the enum
`["foreground", "background", "detached"]` unconditionally
(`agent/internal/tool/definitions.go`). A session's model-facing schema is not that
function's return value directly: `Session.rebuildToolDefsCache` materializes
`s.cachedToolDefs` from the profile's canonical definitions, and
`Session.allToolDefinitions` hands that cache to every model call.

The environment's real answer is different. `LocalExecutionEnvironment.DetachSupported`
returns false when a sandbox wrapper is provisioned — and on a platform without
detached-process attributes — and `DetachCommand` refuses on exactly that predicate
with `ErrDetachUnsupported` (`agent/execenv/local.go`, pinned by
`TestDetachSupportedMatchesDetachCommandsRefusal`). `execenv.DetachSupportReporter`
exists for this answer, and `agent/session_jobtree_drain.go`'s
`detachedShellAvailable` is its only production consumer: the drain escalation tells
the model that the environment cannot disown a process while the schema beside it
still offers the mode. Every sandboxed session therefore invites a call that can only
fail.

## Contract

The advertised shell schema follows the live environment's detach capability.

| Environment | `mode` enum | Dispatch |
| --- | --- | --- |
| `DetachSupported()` false (sandbox wrapper provisioned, or platform without detached attributes) | `["foreground", "background"]` | Unchanged: a `mode:"detached"` call that arrives anyway still refuses with `ErrDetachUnsupported` after the existing cwd/context checks |
| `DetachSupported()` true (unsandboxed, capable platform) | `["foreground", "background", "detached"]` | Unchanged: the call starts a disowned process and returns its PID |

The sandboxed form's `mode` description names `foreground` and `background` and states
that this environment cannot disown a process, so it does not offer `detached`.
`foreground` and `background` schemas, their execution, every other shell parameter,
and every other tool's schema are untouched. A delegate session owns its own
definition cache and builds it from the environment it actually runs on — its own lane
environment, or a parent's shared one — so a sandboxed delegate's schema follows that
environment rather than its parent's schema.

Both model-facing mode descriptions are pinned whole, per
`docs/developing-evener/testing.md`'s prose rule: `DefShell()`'s existing golden in
`agent/testdata/detachedshell/mode-description.md` must stay byte-identical, and the
unavailable form gets its own golden file beside it. No prose is asserted by
substring or presence.

## The seam

`agent/internal/tool` gains `DefShellWithDetach(detachAvailable bool)`.
`ShellModeProperty(detachAvailable bool)` is the shared constructor for the
capability-dependent `mode` property.
`DefShell()` remains the fully capable form (`DefShellWithDetach(true)`) for the
profile's environment-independent seed definitions. The session decides:

- `detachSupportedBy(env execenv.ExecutionEnvironment) bool` is the single capability
  read: the `execenv.DetachSupportReporter` type assertion, false when the
  environment does not report. `Session.detachedShellAvailable` uses it, and so does
  the schema.
- `Session.rebuildToolDefsCache` is the single point that materializes the advertised
  schema cache. For `shell` it substitutes
  `tool.DefShellWithDetach(detachSupportedBy(s.env))`, after the existing
  registry-description step that it would otherwise inherit. It reads `s.env`
  directly, as `delegateToolDefinition` already does in the same loop: callers hold
  `s.mu` (`swapEnvAndRefresh`) or run at construction time before a turn exists, and
  `currentEnv()` would take `s.mu` and deadlock. `detachedShellAvailable` keeps using
  `currentEnv()` for callers outside that lock.
- `Session.profileWireToolDefs` builds the profile's definitions for the prompt's
  tool-availability comparison. It uses the same session shell definition, so its
  documented contract — the definitions sent to the model — stays true for shell
  instead of quietly advertising the profile's static enum in a second place. Its
  consumer compares names only, so the substitution changes no prompt output.
- `Session.shellToolForDiagnostics` gives call preparation the same narrowed mode
  property for its repair and schema-error prose, preserving the registry's intent
  parameter; validation still uses the registry's compiled schema, so a detached call
  passes preparation and reaches the refusal. Without it, a
  schema-invalid mode (for example `"daemon"`) would be explained to the model from
  the registry's fully capable enum and still list `detached` as allowed. The
  registry's compiled schema stays fully capable, so a valid `mode:"detached"` call
  still reaches `DetachCommand` and its `ErrDetachUnsupported` refusal.

No stale mode can survive, because the cache is the only model-facing copy and every
path that can change the answer rebuilds it:

- `NewSession` and `RestoreSessionFromMetaWithConfig` build the cache at init, after
  the environment is installed. The one direct `s.env` assignment outside that
  constructor path, `resumeWorktreeReentry`, runs before `initSessionState` builds
  the cache in both constructors, so the first cache already reflects it.
- Every post-construction environment change goes through `swapEnvAndRefresh`, which
  installs `s.env` and calls `rebuildToolDefsCache` under the same lock; the next
  model call sees the refreshed enum. Sandbox provisioning (`EnableSandbox`) runs on
  child environments before those children are constructed.
- `Session.RegisterTool` is the only addition path for a running session, and it
  rebuilds the cache. MCP tools are snapshotted once by `initMCP` before the init
  rebuild, not added later.
- The cache is not persisted or restored from durable state; `allToolDefinitions`
  returns the current cache on every round. The model call reads it under `s.mu`;
  `Session.ToolDefinitions` is an unlocked snapshot accessor, and the regression reads
  it only after a swap returns.
- The registry-definition fallback in `rebuildToolDefsCache` (for a profile that
  omits shell) uses the same session shell definition, so the invariant does not rest
  on every profile happening to include `shell_search`.

The registered shell definition keeps the fully capable enum. That is deliberate: the
issue is an advertisement defect, not a refusal defect, and the handler's
`ErrDetachUnsupported` behavior must not change. The registry copy is not what the
model sees: every built-in profile's capability set includes `shell_search`, so shell
is advertised through the profile loop, and the registry fallback applies the same
session definition when a profile omits it.

## Scope and non-goals

Allowed changes are this spec, `agent/internal/tool/definitions.go`, the shell
definition and preparation construction in `agent/session_tools.go` and its
capability helpers in `agent/session_tools_shell.go`, the reuse in
`agent/session_jobtree_drain.go` (including removal of its now-unused `execenv`
import), and their tests.

Non-goals:

- No change to `DetachSupported`, `DetachCommand`, wrapper provisioning, sandbox
  policy, or detached-process lifetime.
- No change to the system prompt. Its background-work section tells a session to use
  `detached` mode unconditionally (`agent/prompts/system.md.tmpl`); that is the same
  advertised-vs-capable mismatch on a different surface, filed as
  [#4922](https://github.com/prime-radiant-inc/evener/issues/4922) rather than folded
  into this bounded change.
- No change to `docs/job-control.md`, whose mode descriptions and decision table still
  present `detached` as unconditionally available. It is outside this change's allowed
  file scope; the same follow-up issue records the stale contract.
- No sandbox redesign, no new capability negotiation protocol, no persisted schema,
  no client or tool-rendering change, no labels.
- No schema change to `foreground`/`background` or any other tool.

## Verification

Add `TestShellModeDetachedFollowsEnvironment` in
`agent/session_tools_shell_test.go`, exercising the real session path
(`swapEnvAndRefresh` → `rebuildToolDefsCache` → `Session.ToolDefinitions`), not a
hand-built enum:

Use one session and drive it through both directions of the swap in sequential
subtests, so a failed sandboxed subtest does not hide the capable-return control and a
hardcoded "false forever after a swap" implementation cannot pass:

1. Baseline at init: on a detach-capable platform the fresh unsandboxed session
   advertises exactly `["foreground", "background", "detached"]`; on a platform
   without detached-process support it advertises exactly
   `["foreground", "background"]`.
   A separately constructed session whose environment is wrapper-bearing at
   construction advertises exactly `["foreground", "background"]`, proving the init
   cache is narrowed without going through a swap.
2. Sandboxed case: clone the baseline environment through `WithWorkingDirectory`,
   provision `Wrapper = &sandbox.Wrapper{}` (the same shape
   `agent/execenv/detach_test.go` uses), swap it in, assert
   `DetachSupported() == false`, assert the advertised `mode` enum is exactly
   `["foreground", "background"]`, and assert a `mode:"detached"` call through the
   registered shell handler and through the real preparation path (`execTool`) still
   refuses with `ErrDetachUnsupported`. Golden-check the session-advertised `mode`
   description against the new golden file.
3. Capable return: swap the same session onto a plain, detach-capable clone, assert
   `DetachSupported() == true`, assert the enum is exactly
   `["foreground", "background", "detached"]`, and assert a real registered `shell`
   call with `mode:"detached"` starts a process (positive PID) through `DetachCommand`;
   await its completion receipt and clean up. Skip only this step where the platform
   itself has no detached-process support.
4. Pin both model-facing `mode` descriptions with the existing detached-shell prose
   golden test: the capable form against its current golden (unchanged bytes), the
   sandboxed form against a new golden file. Regenerate only through `-update-prompt`
   and read the diff.

Run the new regression before the production change and record step 2 failing while
steps 1 and 3 pass; rerun after the fix. Focused checks:
`go test ./agent -run 'TestShellModeDetachedFollowsEnvironment'`,
`go test ./agent/internal/tool -run 'TestDefShell'`,
`go test ./agent/execenv -run 'TestDetach'`, and `make build`. Do not run the full
suite locally; CI is the source of truth.

Freeze this spec and review it with two independent competing PAR reviewers; resolve
legitimate findings. Review the immutable implementation diff with another
two-reviewer PAR round, run simplify-code, commit, run the branch RoboRev review, then
push and open the PR. Fetch current main before the final push and merge it if it
moved. Do not merge; the parent owns the CI wait.
