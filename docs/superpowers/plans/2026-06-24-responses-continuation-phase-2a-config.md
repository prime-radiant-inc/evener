# Responses Continuation Phase 2A Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an explicit launch-time `openai_responses_continuation` setting with values `off` and `auto`, defaulting to `off`, and thread it into `agent.SessionConfig` without enabling runtime continuation.

**Architecture:** Use the existing launch-config pattern: TOML layer field, appwire field, schema row, TUI display/edit support, direct CLI/serve flags, and session snapshot round trip. The setting stores a string because it is a launch/runtime policy enum, not a boolean; runtime selection still uses the disabled endpoint registry and sends no continuation handles.

**Tech Stack:** Go, launchconfig, appwire, cmd/serf flags, agent session config snapshots, deterministic unit tests.

---

## File Structure

- `agent/session_config.go`: add `OpenAIResponsesContinuation string` and snapshot conversion.
- `agent/schema/config_snapshot.go`: persist the setting on session metadata.
- `agent/snapshot_golden_test.go`: keep snapshot converter fidelity covering the new field.
- `cmd/serf/main.go` and `cmd/serf/serve.go`: add `--openai-responses-continuation` and thread into `SessionConfig`.
- `cmd/serf-hub/internal/launchconfig/*.go`: add layered launch config, schema, appwire conversion, and args projection.
- `appwire/types.go`: add the wire field.
- `cmd/serf-tui/internal/launchconfig/*.go`: make the schema-driven TUI display and edit the enum.

## Non-Goals

- Do not enable `responses_delta`.
- Do not send `previous_response_id`.
- Do not change OpenAI `store:false` defaults.
- Do not add provider storage policy or planner logic.

### Task 1: Add SessionConfig and Snapshot Field

**Files:**
- Modify: `agent/session_config.go`
- Modify: `agent/schema/config_snapshot.go`
- Modify: `agent/snapshot_golden_test.go`

- [ ] **Step 1: Write failing snapshot coverage**

Set `OpenAIResponsesContinuation: "auto"` in `fullSessionConfig()`. Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestConfigSnapshot_ConverterFidelity' -count=1
```

Expected: FAIL until `SessionConfig`, `ConfigSnapshot`, and conversions carry the field.

- [ ] **Step 2: Add fields and conversions**

Add `OpenAIResponsesContinuation string` with JSON tag `openai_responses_continuation,omitempty` to `SessionConfig` and `ConfigSnapshot`. Thread it through `toSnapshot()` and `configFromSnapshot()`.

- [ ] **Step 3: Run focused test**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestConfigSnapshot_ConverterFidelity' -count=1 -v
```

Expected: PASS.

### Task 2: Add Direct CLI and Serve Flags

**Files:**
- Modify: `cmd/serf/main.go`
- Modify: `cmd/serf/serve.go`

- [ ] **Step 1: Add flags**

Add `--openai-responses-continuation <mode>` to both direct run and `serf serve`. Accepted values are not enforced in this phase beyond simple string trimming; the runtime decision helper already treats anything except `auto` as off when it is used later.

- [ ] **Step 2: Thread into session config**

Set `SessionConfig.OpenAIResponsesContinuation` from the flag on new sessions. For restored sessions, include it in `RestoreSessionConfig` only in a later restore-precedence phase; this phase preserves existing restore behavior except snapshot readability.

- [ ] **Step 3: Run compile test**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run 'Test' -count=1
```

Expected: PASS.

### Task 3: Add Hub Launch Config Wiring

**Files:**
- Modify: `appwire/types.go`
- Modify: `cmd/serf-hub/internal/launchconfig/types.go`
- Modify: `cmd/serf-hub/internal/launchconfig/merge.go`
- Modify: `cmd/serf-hub/internal/launchconfig/wire.go`
- Modify: `cmd/serf-hub/internal/launchconfig/args.go`
- Modify: `cmd/serf-hub/internal/launchconfig/schema.go`
- Modify: `cmd/serf-hub/internal/launchconfig/*_test.go`

- [ ] **Step 1: Add failing tests**

Add tests proving:

- launch schema includes `openai_responses_continuation` in the Limits group with choices `off` and `auto`;
- layer merge uses normal scalar precedence and provenance;
- wire conversion round-trips the string;
- `ToArgs` emits `--openai-responses-continuation auto`.

- [ ] **Step 2: Implement launch config wiring**

Add the string field to TOML/appwire structs, merge logic, wire conversion, args projection, and launch option schema.

- [ ] **Step 3: Run focused tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig -run 'TestLaunchOptionSchema|TestMerge|TestWire|TestToArgs' -count=1 -v
```

Expected: PASS.

### Task 4: Add TUI Launch Config Support

**Files:**
- Modify: `cmd/serf-tui/internal/launchconfig/launch_schema.go`
- Modify: `cmd/serf-tui/internal/launchconfig/launch_settings_panel.go`
- Modify: `cmd/serf-tui/internal/launchconfig/*_test.go`

- [ ] **Step 1: Add failing TUI tests**

Update the schema row expected field order to include `openai_responses_continuation`, and add a focused edit/display test for `auto`.

- [ ] **Step 2: Implement display/edit support**

Add `openai_responses_continuation` to schema row value extraction, static fallback rows, and `applyEdit`.

- [ ] **Step 3: Run focused tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-tui/internal/launchconfig -run 'TestSchemaRows|TestLaunchSettings' -count=1 -v
```

Expected: PASS.

### Task 5: Proof and Commit

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2a.md`

- [ ] **Step 1: Run proof commands**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent ./cmd/serf ./cmd/serf-hub/internal/launchconfig ./cmd/serf-tui/internal/launchconfig -run 'TestConfigSnapshot_ConverterFidelity|TestLaunchOptionSchema|TestMerge|TestWire|TestToArgs|TestSchemaRows|TestLaunchSettings|Test' -count=1
git diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 2: Write proof**

Record the commands above and explicitly state that runtime continuation remains disabled and no provider request shape changed.

- [ ] **Step 3: Commit**

Commit the code and proof with focused file adds, not `git add -A`.

## Self-Review

- Spec coverage: covers launch-time config and session snapshot plumbing. Restore-precedence override behavior, storage eligibility, planner behavior, and runtime request changes remain later phases.
- Placeholder scan: no TODO/TBD placeholders.
- Type consistency: the setting name is consistently `openai_responses_continuation` in TOML/JSON and `OpenAIResponsesContinuation` in Go.
