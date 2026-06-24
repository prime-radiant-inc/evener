# Responses Continuation Phase 2B Docs and Help Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the OpenAI Responses continuation launch setting through the envvars registry, deterministic env launch input, CLI/serve help, hub launch-setting metadata, and user-facing environment docs.

**Architecture:** Keep the runtime setting as the same `openai_responses_continuation` session/launch field from Phase 2A. Add `SERF_OPENAI_RESPONSES_CONTINUATION` as a launch-time fallback when no CLI flag or hub launch arg supplies a value. Do not enable continuation, planner/storage eligibility, or provider-side storage.

**Tech Stack:** Go, `envvars`, `cmd/serf`, `cmd/serf-hub/internal/launchconfig`, `docs/environment.md`.

---

## File Structure

- `envvars/envvars.go`: add `SERFOpenAIResponsesContinuation` and include it in `allVars`.
- `cmd/serf/run.go`: resolve `--openai-responses-continuation` over `SERF_OPENAI_RESPONSES_CONTINUATION`.
- `cmd/serf/serve.go`: resolve the serve flag over `SERF_OPENAI_RESPONSES_CONTINUATION`.
- `cmd/serf/openai_responses_continuation_config.go` (or nearby existing file): add a tiny resolver helper if needed to avoid duplicating precedence logic.
- `cmd/serf/main.go`: list the env var in direct CLI help.
- `cmd/serf/serve.go`: list the env var in serve help.
- `cmd/serf/main_test.go` / `cmd/serf/serve_test.go`: add deterministic resolver/help tests.
- `cmd/serf-hub/internal/launchconfig/schema.go`: mark the hub launch setting with the env fallback and document values, restore behavior, retention, and cost implications.
- `cmd/serf-hub/internal/launchconfig/schema_test.go`: assert the schema advertises the env fallback.
- `docs/environment.md`: document the env var, values, default, restore behavior, and retention/cost caveats.
- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2b.md`: record evidence.

## Non-Goals

- Do not enable `responses_delta`.
- Do not send `previous_response_id`.
- Do not change OpenAI `store:false`.
- Do not add planner/storage eligibility.
- Do not add validation beyond existing trim-and-consume behavior; runtime still treats anything except `auto` as off when consumed.
- Do not add provider live tests.

### Task 1: Env Registry and CLI/Serve Resolution

**Files:**
- Modify: `envvars/envvars.go`
- Modify: `cmd/serf/run.go`
- Modify: `cmd/serf/serve.go`
- Add or modify: `cmd/serf/openai_responses_continuation_config.go`
- Modify: `cmd/serf/main_test.go`
- Modify: `cmd/serf/serve_test.go`

- [ ] **Step 1: Add failing resolver/help tests**

Add deterministic tests that prove:

- CLI flag value wins over `SERF_OPENAI_RESPONSES_CONTINUATION`;
- env var value is used when the CLI/serve flag is empty;
- values are trimmed;
- direct CLI help env list includes `SERF_OPENAI_RESPONSES_CONTINUATION`;
- `serf serve` help env list includes `SERF_OPENAI_RESPONSES_CONTINUATION`.

Keep assertions narrow: call the resolver and `printRunEnvVars` / `printServeEnvVars`; do not snapshot the full help output.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run 'TestResolveOpenAIResponsesContinuation|TestPrintRunEnvVars_IncludesOpenAIResponsesContinuation|TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation' -count=1
```

Expected: FAIL because the env var row/resolver/help entries do not exist yet.

- [ ] **Step 2: Add envvars row**

Add:

```go
SERFOpenAIResponsesContinuation = Var{
	Name: "SERF_OPENAI_RESPONSES_CONTINUATION",
	Summary: "Default OpenAI Responses continuation mode: off|auto. CLI and launch config override it.",
	Visibility: Public,
}
```

Include it in `allVars` near the other public `SERF_*` launch/runtime settings.

- [ ] **Step 3: Add shared resolver**

Add a small package-main helper:

```go
func resolveOpenAIResponsesContinuation(flagValue string, getenv func(string) string) string {
	if trimmed := strings.TrimSpace(flagValue); trimmed != "" {
		return trimmed
	}
	return envvars.SERFOpenAIResponsesContinuation.FromTrimmed(getenv)
}
```

Use it from both `run.go` and `serve.go` so fresh sessions and resumed sessions share the same resolved value. Preserve Phase 2A restore behavior: explicit `off` overrides a persisted `auto`; empty means no override, so persisted snapshots can still win.

- [ ] **Step 4: Wire help env lists**

Add `envvars.SERFOpenAIResponsesContinuation` to both `printRunEnvVars` and `printServeEnvVars`.

### Task 2: Hub Schema and User Docs

**Files:**
- Modify: `cmd/serf-hub/internal/launchconfig/schema.go`
- Modify: `cmd/serf-hub/internal/launchconfig/schema_test.go`
- Modify: `docs/environment.md`
- Add: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2b.md`

- [ ] **Step 1: Add failing hub schema assertion**

Extend `TestLaunchOptionSchema_OpenAIResponsesContinuation` to assert:

```go
if opt.EnvFallback == nil || opt.EnvFallback.Name != envvars.SERFOpenAIResponsesContinuation.Name || opt.EnvFallback.Secret {
	t.Fatalf("EnvFallback = %+v, want public %s", opt.EnvFallback, envvars.SERFOpenAIResponsesContinuation.Name)
}
```

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig -run '^TestLaunchOptionSchema_OpenAIResponsesContinuation$' -count=1
```

Expected: FAIL until the schema advertises the env fallback.

- [ ] **Step 2: Update hub schema description**

Add the `EnvFallback` and update the description to mention:

- values are `off` and `auto`;
- default is `off`;
- CLI/launch setting overrides `SERF_OPENAI_RESPONSES_CONTINUATION`;
- resume restore layers explicit launch values over persisted snapshots;
- `auto` may allow provider-side storage/retention and can affect provider-token/cost behavior once a future phase enables continuation.

- [ ] **Step 3: Update environment docs**

Add `SERF_OPENAI_RESPONSES_CONTINUATION` to `docs/environment.md` with the same values, default, precedence, restore behavior, and retention/cost caveat.

- [ ] **Step 4: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2b.md` with:

- scope;
- evidence commands;
- contracts proven;
- explicit statement that runtime continuation remains disabled.

### Task 3: Verification and Commit

- [ ] **Step 1: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run 'TestResolveOpenAIResponsesContinuation|TestPrintRunEnvVars_IncludesOpenAIResponsesContinuation|TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig -run '^TestLaunchOptionSchema_OpenAIResponsesContinuation$' -count=1 -v
GOCACHE=/tmp/serf-gocache go test . -run '^TestSupportedEnvVarsAreDocumented$' -count=1 -v
git diff --check
```

- [ ] **Step 2: Commit**

```sh
git status --short
git add envvars/envvars.go cmd/serf/run.go cmd/serf/serve.go cmd/serf/openai_responses_continuation_config.go cmd/serf/main.go cmd/serf/main_test.go cmd/serf/serve_test.go cmd/serf-hub/internal/launchconfig/schema.go cmd/serf-hub/internal/launchconfig/schema_test.go docs/environment.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2b.md
git commit -m "feat(cmd): document responses continuation env launch setting"
```

If no new helper file is needed, omit it from `git add`.

## Self-Review

- Spec coverage: completes Phase 2B docs/help/env registry requirements.
- Safety: no runtime continuation enablement, no `previous_response_id`, no `store:true`.
- Test quality: resolver and schema tests assert structured contracts, not full generated help text.
