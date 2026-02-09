# Real-Default, Explicit Test-Shim Provider Execution Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `kilroy attractor run` default to real provider CLIs, and require explicit, auditable opt-in for fake/test shim execution.

**Architecture:** Introduce a run-config execution policy (`llm.cli_profile`) and a CLI safety gate (`--allow-test-shim`) enforced in one centralized provider-executable resolution path used by both preflight and codergen runtime. Preserve backward compatibility for normal real runs while failing fast if shim env overrides are detected outside explicit test-shim mode.

**Tech Stack:** Go (`cmd/kilroy`, `internal/attractor/engine`), JSON/YAML run config schema, preflight report artifacts, unit/integration tests with fake CLIs.

---

## Implementation Notes (Read First)

- Current executable selection happens in `defaultCLIInvocation()` and currently prefers env overrides (`KILROY_CODEX_PATH`, `KILROY_CLAUDE_PATH`, `KILROY_GEMINI_PATH`):
  - `internal/attractor/engine/codergen_router.go:1255`
- Preflight and runtime must use exactly the same resolution policy to avoid drift:
  - `internal/attractor/engine/provider_preflight.go:70`
- `RunWithConfig()` is the right central point to enforce profile + gate before execution:
  - `internal/attractor/engine/run_with_config.go:17`

---

### Task 1: Add Config Surface for CLI Profile and Explicit Executable Paths

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Test: `internal/attractor/engine/config_test.go`

**Step 1: Write failing config tests for new schema fields**

Add tests that expect:
1. `llm.cli_profile` defaults to `real`.
2. Invalid profile (e.g. `banana`) fails validation.
3. `llm.providers.<provider>.executable` is allowed only when `cli_profile=test_shim`.

Example test snippet:
```go
func TestLoadRunConfigFile_DefaultCLIProfileIsReal(t *testing.T) {
    cfg := mustLoadConfigYAML(t, `
version: 1
repo: { path: /tmp/repo }
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  providers:
    openai: { backend: cli }
modeldb: { litellm_catalog_path: /tmp/catalog.json }
`)
    if got := cfg.LLM.CLIProfile; got != "real" {
        t.Fatalf("cli_profile=%q want real", got)
    }
}
```

**Step 2: Run failing tests**

Run:
```bash
go test ./internal/attractor/engine -run 'TestLoadRunConfigFile_DefaultCLIProfileIsReal|TestLoadRunConfigFile_InvalidCLIProfile|TestLoadRunConfigFile_ExecutableOverrideRequiresTestShim' -v
```
Expected: FAIL (fields/validation not implemented).

**Step 3: Implement minimal config schema + validation**

In `RunConfigFile`:
```go
LLM struct {
    CLIProfile string `json:"cli_profile" yaml:"cli_profile"`
    Providers map[string]struct {
        Backend    BackendKind `json:"backend" yaml:"backend"`
        Executable string      `json:"executable,omitempty" yaml:"executable,omitempty"`
    } `json:"providers" yaml:"providers"`
} `json:"llm" yaml:"llm"`
```

In defaults:
```go
if strings.TrimSpace(cfg.LLM.CLIProfile) == "" {
    cfg.LLM.CLIProfile = "real"
}
```

In validation:
```go
switch strings.ToLower(strings.TrimSpace(cfg.LLM.CLIProfile)) {
case "real", "test_shim":
default:
    return fmt.Errorf("invalid llm.cli_profile: %q (want real|test_shim)", cfg.LLM.CLIProfile)
}
```

And if profile is `real`, reject any non-empty provider `Executable`.

**Step 4: Re-run tests**

Run the same command from Step 2.
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/config_test.go
git commit -m "feat(attractor/config): add llm.cli_profile and explicit provider executable config with validation"
```

---

### Task 2: Add CLI Safety Gate Flag (`--allow-test-shim`)

**Files:**
- Modify: `cmd/kilroy/main.go`
- Modify: `internal/attractor/engine/engine.go`
- Test: `cmd/kilroy/main_exit_codes_test.go`

**Step 1: Write failing CLI test for unknown/new flag behavior**

Add tests asserting:
1. `kilroy attractor run ... --allow-test-shim` is accepted.
2. Flag appears in usage text.

**Step 2: Run failing test**

```bash
go test ./cmd/kilroy -run 'TestAttractorRun_AllowsTestShimFlag|TestUsage_IncludesAllowTestShimFlag' -v
```
Expected: FAIL.

**Step 3: Implement flag plumbing**

In `RunOptions` (`engine.go`):
```go
AllowTestShim bool
```

In `cmd/kilroy/main.go` parser:
```go
case "--allow-test-shim":
    allowTestShim = true
```

Pass through into `RunWithConfig`:
```go
RunOptions{ ... AllowTestShim: allowTestShim }
```

For detach, propagate into child args when set.

**Step 4: Re-run tests**

```bash
go test ./cmd/kilroy -run 'TestAttractorRun_AllowsTestShimFlag|TestUsage_IncludesAllowTestShimFlag' -v
```
Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/kilroy/main.go cmd/kilroy/main_exit_codes_test.go internal/attractor/engine/engine.go
git commit -m "feat(cli): add --allow-test-shim gate and thread through run options"
```

---

### Task 3: Centralize Provider Executable Policy Resolution

**Files:**
- Create: `internal/attractor/engine/provider_exec_policy.go`
- Test: `internal/attractor/engine/provider_exec_policy_test.go`
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/provider_preflight.go`

**Step 1: Write failing policy tests**

Add table-driven tests for:
1. `real` profile rejects env-based shim path overrides.
2. `real` profile returns canonical binary names (`codex`, `claude`, `gemini`).
3. `test_shim` profile requires `AllowTestShim=true`.
4. `test_shim` profile requires explicit provider executable path.

**Step 2: Run failing tests**

```bash
go test ./internal/attractor/engine -run 'TestResolveProviderExecutable_' -v
```
Expected: FAIL.

**Step 3: Implement resolver and wire call sites**

Create resolver API:
```go
type ProviderExecPolicy struct {
    CLIProfile    string
    AllowTestShim bool
}

func ResolveProviderExecutable(cfg *RunConfigFile, provider string, opts RunOptions) (string, error)
```

Rules:
- `real`:
  - if any `KILROY_*_PATH` env var is set, return hard error.
  - ignore provider `Executable` fields.
  - return default executable name.
- `test_shim`:
  - require `opts.AllowTestShim`.
  - require per-provider explicit executable path in config.
  - do not auto-fallback to env/default binaries.

Replace executable lookup in:
- `defaultCLIInvocation()` path (runtime)
- `runProviderCLIPreflight()` (preflight)

So both paths call one resolver.

**Step 4: Re-run tests**

```bash
go test ./internal/attractor/engine -run 'TestResolveProviderExecutable_|TestRunWithConfig_Preflight' -v
```
Expected: PASS for new policy tests and unaffected preflight tests.

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_exec_policy.go internal/attractor/engine/provider_exec_policy_test.go internal/attractor/engine/codergen_router.go internal/attractor/engine/provider_preflight.go
git commit -m "feat(attractor): centralize provider executable policy with real-default and explicit test_shim mode"
```

---

### Task 4: Enforce Profile + Gate in `RunWithConfig` and Emit Clear Errors

**Files:**
- Modify: `internal/attractor/engine/run_with_config.go`
- Test: `internal/attractor/engine/run_with_config_test.go`

**Step 1: Write failing run-level guard tests**

Add tests:
1. `cli_profile=test_shim` + missing `AllowTestShim` fails with clear remediation.
2. `cli_profile=real` + `KILROY_CODEX_PATH` set fails before stage execution.

**Step 2: Run failing tests**

```bash
go test ./internal/attractor/engine -run 'TestRunWithConfig_RejectsTestShimWithoutAllowFlag|TestRunWithConfig_RejectsRealProfileWhenProviderPathEnvIsSet' -v
```
Expected: FAIL.

**Step 3: Implement guard + explicit error strings**

In `RunWithConfig`, before preflight:
```go
if strings.EqualFold(cfg.LLM.CLIProfile, "test_shim") && !opts.AllowTestShim {
    return nil, fmt.Errorf("preflight: llm.cli_profile=test_shim requires --allow-test-shim")
}
```

And validate env-var ban for `real` profile via helper.

**Step 4: Re-run tests**

Run command from Step 2.
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/run_with_config.go internal/attractor/engine/run_with_config_test.go
git commit -m "feat(attractor): fail fast on unsafe profile/gate combinations with explicit remediation"
```

---

### Task 5: Improve Preflight Report Transparency

**Files:**
- Modify: `internal/attractor/engine/provider_preflight.go`
- Test: `internal/attractor/engine/provider_preflight_test.go`

**Step 1: Write failing report-shape tests**

Require report to include:
- `cli_profile`
- `allow_test_shim`
- `provider_cli_presence.details` includes `source` (`default`, `config.executable`)

**Step 2: Run failing tests**

```bash
go test ./internal/attractor/engine -run 'TestPreflightReport_IncludesCLIProfileAndSource' -v
```
Expected: FAIL.

**Step 3: Implement metadata additions**

Add fields to `providerPreflightReport` and details payload.

**Step 4: Re-run tests**

Run command from Step 2.
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_preflight.go internal/attractor/engine/provider_preflight_test.go
git commit -m "feat(attractor/preflight): report cli profile, shim gate, and executable source for auditability"
```

---

### Task 6: Add End-to-End Safety Regression Tests

**Files:**
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`
- Modify: `cmd/kilroy/main_exit_codes_test.go`

**Step 1: Write failing integration tests**

Add e2e-ish tests:
1. Real profile run fails if fake executable override env is present.
2. Test-shim profile run succeeds only when `AllowTestShim=true` and explicit executable paths are configured.

**Step 2: Run failing tests**

```bash
go test ./internal/attractor/engine -run 'TestRunWithConfig_RealProfileRejectsShimOverrideE2E|TestRunWithConfig_TestShimRequiresExplicitGateAndExecutable' -v
```
Expected: FAIL.

**Step 3: Implement any minimal glue needed**

Only if tests reveal missing wiring.

**Step 4: Re-run tests**

Run command from Step 2.
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/run_with_config_integration_test.go cmd/kilroy/main_exit_codes_test.go
git commit -m "test(attractor): add e2e guardrails for real-default and explicit test_shim execution"
```

---

### Task 7: Docs + Runbook Commands (Real vs Test)

**Files:**
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/README.md`
- Modify: `docs/strongdm/attractor/reliability-troubleshooting.md`

**Step 1: Write docs updates with explicit command blocks**

Add:
- Real run command that unsets `KILROY_*_PATH`.
- Test-shim run command requiring:
  - `llm.cli_profile: test_shim`
  - explicit provider executable paths
  - `--allow-test-shim`

**Step 2: Validate docs references via grep**

```bash
rg -n "cli_profile|allow-test-shim|test_shim|KILROY_CODEX_PATH|fake" README.md docs/strongdm/attractor/*.md
```
Expected: all new guidance present.

**Step 3: Commit**

```bash
git add README.md docs/strongdm/attractor/README.md docs/strongdm/attractor/reliability-troubleshooting.md
git commit -m "docs(attractor): document real-default execution and explicit test_shim opt-in workflow"
```

---

### Task 8: Full Validation Matrix

**Files:**
- No code changes expected (validation only)

**Step 1: Run focused engine tests**

```bash
go test ./internal/attractor/engine -run 'TestLoadRunConfigFile_|TestResolveProviderExecutable_|TestRunWithConfig_.*(Shim|Profile|Preflight)' -v
```
Expected: PASS.

**Step 2: Run CLI tests**

```bash
go test ./cmd/kilroy -run 'TestAttractorRun_.*|TestUsage_.*allow-test-shim' -v
```
Expected: PASS.

**Step 3: Run broad suite**

```bash
go test ./... -v
```
Expected: PASS.

**Step 4: Commit test-only adjustments if any**

```bash
git add <only changed files>
git commit -m "test(attractor): stabilize real-default and test_shim safety matrix"
```

---

### Task 9: Manual Smoke Commands (Operator Proof)

**Files:**
- No source edits expected

**Step 1: Real smoke (should fail fast if shim env leaked)**

```bash
KILROY_CODEX_PATH=/tmp/fake/codex ./kilroy attractor run --graph demo/dttf/dttf.dot --config /tmp/run_config_real.json --run-id smoke-real --logs-root /tmp/k-smoke-real/logs
```
Expected: immediate preflight failure with explicit message about real profile + shim override.

**Step 2: Explicit test-shim smoke**

```bash
./kilroy attractor run --graph demo/dttf/dttf.dot --config /tmp/run_config_test_shim.json --allow-test-shim --run-id smoke-shim --logs-root /tmp/k-smoke-shim/logs
```
Expected: preflight/report shows `cli_profile=test_shim` and explicit executable source.

**Step 3: Verify report artifacts**

```bash
jq . /tmp/k-smoke-real/logs/preflight_report.json
jq . /tmp/k-smoke-shim/logs/preflight_report.json
```
Expected: clear profile/gate/source metadata in both.

---

## Definition of Done

- Default run mode is effectively real and rejects accidental fake provider execution.
- Fake/test runs require both:
  1. config profile `test_shim`, and
  2. CLI flag `--allow-test-shim`.
- Preflight and runtime use a single provider executable resolution policy.
- Preflight report clearly indicates execution mode and executable source.
- Docs include explicit real vs test command recipes.
- All tests pass.

