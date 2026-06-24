# Responses Continuation Phase 2A Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the Phase 2A config contract by adding restore-precedence coverage and proving `auto` still preserves full-history OpenAI request shape while the endpoint registry is disabled.

**Architecture:** Keep `openai_responses_continuation` as a launch/session config string. Add an explicit restore override carrier to `RestoreSessionConfig`, thread CLI/serve launch values through that carrier on resume, and keep runtime request construction unchanged.

**Tech Stack:** Go, `agent.SessionConfig`, `RestoreSessionConfig`, `cmd/serf`, deterministic OpenAI fake HTTP server tests.

---

## File Structure

- `agent/session_init.go`: add a restore-only continuation mode override and layer it over persisted config before defaults.
- `agent/session_resolve_profile_test.go`: add restore-precedence tests for persisted `auto -> off` and persisted `off -> auto`.
- `cmd/serf/run.go`: pass normalized direct CLI continuation mode into restore config.
- `cmd/serf/serve.go`: pass normalized serve continuation mode into restore config.
- `agent/session_openai_continuation_phase0a_test.go`: prove a session configured with `OpenAIResponsesContinuation: "auto"` still sends full history with the default disabled registry.
- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2a.md`: patch evidence and contracts.

## Non-Goals

- Do not add env var support; Phase 2B-docs-help owns `SERF_OPENAI_RESPONSES_CONTINUATION`.
- Do not enable `responses_delta`.
- Do not send `previous_response_id`.
- Do not change OpenAI `store:false`.
- Do not add planner/storage eligibility.

### Task 1: Restore Precedence and Disabled Registry Proof

**Files:**
- Modify: `agent/session_init.go`
- Modify: `agent/session_resolve_profile_test.go`
- Modify: `cmd/serf/run.go`
- Modify: `cmd/serf/serve.go`
- Modify: `agent/session_openai_continuation_phase0a_test.go`
- Modify: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2a.md`

- [ ] **Step 1: Add failing restore-precedence tests**

Add tests in `agent/session_resolve_profile_test.go`:

```go
func TestRestoreSessionFromMetaWithConfig_LayersOpenAIResponsesContinuation(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	tests := []struct {
		name      string
		persisted string
		override  string
		want      string
	}{
		{name: "global auto overrides persisted off", persisted: "off", override: "auto", want: "auto"},
		{name: "global off overrides persisted auto", persisted: "auto", override: "off", want: "off"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := schema.SessionMeta{
				ID:        "01JRESTORECONTINUATION000000001",
				ProfileID: "openai",
				Model:     "gpt-5.4",
				Config: (SessionConfig{
					OpenAIResponsesContinuation: tc.persisted,
				}).toSnapshot(),
			}
			sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
				StateDir:                    dir,
				OpenAIResponsesContinuation: tc.override,
			})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer sess.Close()
			if got := sess.Meta().Config.OpenAIResponsesContinuation; got != tc.want {
				t.Fatalf("OpenAIResponsesContinuation = %q, want %q", got, tc.want)
			}
		})
	}
}
```

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestRestoreSessionFromMetaWithConfig_LayersOpenAIResponsesContinuation$' -count=1
```

Expected: FAIL because `RestoreSessionConfig` does not carry the override.

- [ ] **Step 2: Implement restore override**

In `agent/session_init.go`, add this exported field to `RestoreSessionConfig`:

```go
OpenAIResponsesContinuation string
```

In `RestoreSessionFromMetaWithConfig`, after the `ModelFallbacks` override, add:

```go
if strings.TrimSpace(restoreCfg.OpenAIResponsesContinuation) != "" {
	cfg.OpenAIResponsesContinuation = strings.TrimSpace(restoreCfg.OpenAIResponsesContinuation)
}
```

- [ ] **Step 3: Thread CLI/serve resume overrides**

In `cmd/serf/run.go`, pass the normalized value in the restore config:

```go
OpenAIResponsesContinuation: strings.TrimSpace(cfg.openAIResponsesContinuation),
```

In `cmd/serf/serve.go`, pass:

```go
OpenAIResponsesContinuation: strings.TrimSpace(*openAIResponsesContinuation),
```

- [ ] **Step 4: Prove disabled registry behavior with config auto**

In `agent/session_openai_continuation_phase0a_test.go`, set:

```go
OpenAIResponsesContinuation: "auto",
```

on the existing `SessionConfig` in `TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory`. The existing assertions prove no `previous_response_id`, explicit `store:false`, and full history input markers.

- [ ] **Step 5: Run focused tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestRestoreSessionFromMetaWithConfig_LayersOpenAIResponsesContinuation|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run '^$' -count=1
```

Expected: PASS.

- [ ] **Step 6: Patch proof**

Update `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2a.md` to include the focused restore/disabled-registry command and explicitly state:

- persisted `off` can be overridden to `auto` on restore;
- persisted `auto` can be overridden to `off` on restore;
- configured `auto` still sends full-history OpenAI Responses requests with no `previous_response_id` and `store:false` while the default registry is disabled.

- [ ] **Step 7: Commit**

Run:

```sh
git status --short
git add agent/session_init.go agent/session_resolve_profile_test.go cmd/serf/run.go cmd/serf/serve.go agent/session_openai_continuation_phase0a_test.go docs/superpowers/proofs/2026-06-24-responses-continuation-phase-2a.md
git commit -m "fix(agent): complete responses continuation config restore"
```

## Self-Review

- Spec coverage: completes the Phase 2A restore-precedence and disabled-registry proof gaps left by the first config slice.
- Placeholder scan: no TODO/TBD placeholders.
- Type consistency: `OpenAIResponsesContinuation` remains the Go field and `openai_responses_continuation` remains the JSON/TOML field.
