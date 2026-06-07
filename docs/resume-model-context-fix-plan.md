# Plan: Fix resumed-session model/profile/context restoration

## Problem statement

Session `01KTHGQ3P1HZNX5B6919W8Z0HE` showed symptoms after resume where the agent appeared to use or reason about the wrong model/context state. The investigation found two likely causes:

1. Restored sessions lose runtime-only profile resolution plumbing (`SessionConfig.ResolveProfile` / `Session.resolveProfile`).
2. Resume model selection currently allows `SERF_MODEL` to override the persisted session model, which can surprise hub/browser resumes because spawned daemons inherit environment variables.

The fix should make resume faithfully restore the session's persisted provider/model/profile-derived context, while still allowing explicit user-requested overrides in a controlled way.

## Current behavior

### Fresh sessions

Fresh `serf serve` sessions build a `SessionConfig` containing:

```go
ResolveProfile: cmdutil.BuildResolveProfile(provCfg, hasProvConfig),
```

Relevant files/functions:

- `cmd/serf/serve.go`
  - `buildInitialProfile(...)`
  - fresh `agent.SessionConfig{... ResolveProfile: ...}`
  - `agent.NewSession(...)`
- `cmd/serf/run.go`
  - fresh `agent.SessionConfig{... ResolveProfile: ...}`
- `agent/session_init.go`
  - `NewSession(...)` stores `resolveProfile: cfg.ResolveProfile`
- `agent/session.go`
  - `resolveProfileForRef(...)`
  - `SetModel(...)`

### Restored sessions

Restored sessions do this instead:

```go
sess, err = agent.RestoreSessionFromMeta(client, profile, env, resumedMeta, sd)
```

Inside `RestoreSessionFromMeta`:

```go
cfg := configFromSnapshot(meta.Config)
cfg.StateDir = stateDir
cfg.SessionStartKind = plugin.SessionStartKindResume
```

But `ResolveProfile` is runtime-only (`json:"-"`), so it is not present in `meta.Config`, and `RestoreSessionFromMeta` does not accept or reattach it.

Consequence: post-resume `Session.SetModel(...)` cannot use the configured resolver and falls back to `Profile.WithModel(...)` for cases that require full provider resolution.

## Failure modes to cover

1. **Post-resume cross-provider model changes degrade.**
   - `resolveProfileForRef(...)` only calls `s.resolveProfile` when non-nil.
   - On restored sessions, `s.resolveProfile` is nil.
   - Cross-provider refs can fall through to `base.WithModel(ref)`, leaving provider/model semantics incorrect.

2. **Context window can become wrong/tiny.**
   - `Session.ContextMetrics()` calls `contextMgr.EstimateUsage(...)`.
   - `contextmgr.Manager.EstimateUsage(...)` uses `cm.currentProfile().ContextWindowSize()`.
   - If the restored/current profile is wrong or shallowly mutated, context metrics/compaction thresholds can be wrong even if the UI displays a model label that looks correct.

3. **Initial resume can be unintentionally overridden by environment.**
   - `cmdutil.ResolveModelRef(modelValue, envModel, resumeProvider, resumeModel)` currently prefers CLI, then `SERF_MODEL`, then resume metadata.
   - Hub resume does not pass `--model`, but the daemon inherits env, so `SERF_MODEL` can silently override persisted resume metadata.

## Desired behavior

1. Restoring a session should retain full provider/model resolution capability.
2. Restored sessions should report context metrics from the correctly resolved restored profile.
3. Resume should default to the persisted session provider/model.
4. Model override on resume should be explicit and observable.
5. Fresh-session behavior should not regress.

## Implementation plan

### Step 1: Add tests for restored resolver plumbing

Add focused tests in `agent/session_resolve_profile_test.go` or a new `agent/session_restore_profile_test.go`.

Test shape:

1. Create a `SessionMeta` with persisted config snapshot and model/profile data.
2. Restore through `RestoreSessionFromMeta` using a runtime resolver.
3. Call `sess.SetModel("other-provider/some-model")`.
4. Assert the injected resolver was called and the resulting profile is the resolver-returned profile.

This likely requires changing the restore API first or adding a test-only pathway. Prefer writing the test against the desired public/internal API shape, then implement.

Acceptance criteria:

- Fails before the fix because restored sessions have no resolver.
- Passes after `RestoreSessionFromMeta` can receive and install runtime-only config.

### Step 2: Introduce an explicit restore config/API

Avoid overloading `SessionMeta` or persisting non-serializable fields. Add a small runtime config for restore, for example:

```go
type RestoreSessionConfig struct {
    StateDir string
    RuntimeConfig SessionConfig
}
```

Or, simpler:

```go
func RestoreSessionFromMeta(
    client *llm.Client,
    profile *provider.Profile,
    env execenv.ExecutionEnvironment,
    meta schema.SessionMeta,
    cfg SessionConfig,
) (*Session, error)
```

Recommended direction: keep existing behavior but make runtime fields explicit:

```go
func RestoreSessionFromMeta(client, profile, env, meta, stateDir, runtimeCfg)
```

Implementation rule:

- Start from persisted config: `cfg := configFromSnapshot(meta.Config)`.
- Layer runtime-only fields from caller:
  - `StateDir`
  - `ResolveProfile`
  - possibly `LLMRetryPolicy`, `LLMSleep` in tests if needed
  - explicit CLI/env reasoning-effort override should still be applied by caller or passed in deliberately
- Preserve forced restore fields:
  - `SessionStartKind = plugin.SessionStartKindResume`

In `RestoreSessionFromMeta`, set:

```go
resolveProfile: cfg.ResolveProfile,
```

just like `NewSession` does.

Affected call sites:

- `cmd/serf/serve.go`
- `cmd/serf/run.go`
- tests that call `RestoreSessionFromMeta`

### Step 3: Wire restore call sites with runtime resolver

In `cmd/serf/serve.go`, build runtime restore config from the same runtime data fresh sessions use:

```go
restoreCfg := agent.SessionConfig{
    StateDir: sd,
    ResolveProfile: cmdutil.BuildResolveProfile(provCfg, hasProvConfig),
    // Include runtime-only/test-only fields only if relevant.
}
```

Then call the new restore API.

Do the same in `cmd/serf/run.go`.

Important: do not overwrite persisted config fields wholesale with CLI defaults. Runtime config should layer only non-persisted or explicitly overridden values.

### Step 4: Add context-window restoration tests

Add a regression test proving that restored sessions use the correct context window.

Suggested test cases:

1. Restored OpenAI profile with live model metadata:
   - Use a fake `llm.Client` list-model response that returns `ContextWindow: 1_000_000` for `gpt-5.5`.
   - Restore session.
   - Assert `sess.ContextMetrics().Window == 1_000_000`.

2. Restored provider-instance/cross-provider switch:
   - Restore with an initial profile.
   - Call `SetModel` to a provider/model whose resolver returns a profile with distinct context window.
   - Assert `sess.Profile().ContextWindowSize()` and `sess.ContextMetrics().Window` match the resolver profile.

Relevant existing tests to mirror:

- `agent/session_live_model_metadata_test.go`
- `agent/context_manager_session_test.go`
- `agent/session_resolve_profile_test.go`

### Step 5: Decide and encode resume model precedence

Current `cmdutil.ResolveModelRef(...)` is used for both fresh and resumed sessions and has precedence:

1. CLI `--model`
2. `SERF_MODEL`
3. resume metadata

For faithful resume, change behavior so resumed sessions use:

1. explicit CLI `--model` if provided
2. resume metadata
3. `SERF_MODEL` only if no resume metadata exists

Options:

#### Option A: Add a separate function

```go
func ResolveResumeModelRef(modelValue, envModel, resumeProvider, resumeModel string) (ModelRef, error)
```

Precedence:

1. `modelValue`
2. `resumeProvider/resumeModel`
3. `envModel`

Use it only in `cmd/serf/serve.go` and `cmd/serf/run.go` when resuming.

#### Option B: Add a mode/boolean to `ResolveModelRef`

Less preferred; easier to misuse.

Recommended: **Option A** for clarity.

Add tests in `cmdutil/cmdutil_test.go`:

- `ResolveResumeModelRef` uses persisted metadata when `SERF_MODEL` is set.
- explicit CLI model still overrides persisted metadata.
- env model works when no persisted metadata is available.

### Step 6: Surface explicit override in logs/events

If resume is started with an explicit model override, log it clearly:

```text
[serve] resumed session <id> with model override <provider/model> (was <provider/model>)
```

Do not log this for inherited `SERF_MODEL` if the new precedence ignores it.

Optional but useful:

- Add a warning event if persisted model metadata is missing and env is used.

### Step 7: Verify browser/hub resume path

Hub resume code:

- `cmd/serf-hub/spawn.go`
  - `ResumeDaemon(...)` runs `serf serve --resume <sessionID>` and does not pass `--model`.

With the changed precedence, hub resume should continue the persisted model regardless of `SERF_MODEL` in env.

Add/adjust tests around hub resume if practical:

- Ensure `ResumeDaemon` does not pass `--model` by default.
- Unit-test lower-level `cmd/serf/serve.go`/`cmdutil` behavior rather than spawning full daemons if full e2e is too heavy.

### Step 8: Run verification

Minimum focused tests:

```sh
go test ./agent -run 'Restore|ResolveProfile|ContextMetrics|LiveModelMetadata'
go test ./cmdutil -run 'Resolve.*ModelRef|BuildResolveProfile'
go test ./cmd/serf -run 'Resume|buildInitialProfile'
go test ./cmd/serf-hub -run 'Resume'
```

Then broader smoke:

```sh
go test ./agent ./cmdutil ./cmd/serf ./cmd/serf-hub
```

If full `cmd/serf-hub` has known local-environment failures, document exact failing tests and run focused coverage instead.

## Non-goals

- Do not change transcript schema unless strictly necessary.
- Do not persist `ResolveProfile` or any function-valued config.
- Do not alter fresh-session model precedence except as required to split resume behavior.
- Do not add fuzzy model/context inference in the browser UI; fix the session runtime state instead.

## Acceptance criteria

- A restored session has a non-nil profile resolver when the caller has configured provider resolution.
- Post-resume `SetModel("provider/model")` uses the same resolution path as fresh sessions.
- `ContextMetrics().Window` after restore and after post-restore model switch matches the resolved profile's context window.
- Hub/browser resume without explicit model override uses persisted `meta.ProfileID/meta.Model`, not inherited `SERF_MODEL`.
- Explicit `--model provider/model` on resume still works and is visible in logs.
- Existing fresh-session model switching tests continue to pass.

## Suggested patch structure

1. `cmdutil`: add `ResolveResumeModelRef` and tests.
2. `agent`: extend restore API/config; set `s.resolveProfile`; add restore resolver/context tests.
3. `cmd/serf`: update `run.go` and `serve.go` resume call sites and model precedence; add tests.
4. Optional `cmd/serf-hub`: add a small regression test or document that hub relies on `serf serve --resume` with no model override.
