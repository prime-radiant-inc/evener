# Responses Continuation Phase 11 Raw-Local Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Responses provider handles redacted by default in ATIF export, while allowing explicit local diagnostic `raw-local` export to include raw response handles that are already present in the local transcript.

**Architecture:** Add a small provider-handle export mode enum in the ATIF converter and keep `Convert` as the redacted default. `exportATIF` will use the full transcript reader so it can include request-side handle hashes from `api_call` records; raw-local only adds raw handles when they are directly available or derivable from local transcript data.

**Tech Stack:** Go, deterministic package tests, existing transcript JSONL reader/writer, existing launch config schema.

---

### Task 1: Redacted ATIF Default

**Files:**
- Modify: `agent/internal/atif/atif.go`
- Test: `agent/internal/atif/atif_test.go`

- [x] **Step 1: Write failing ATIF default redaction test**

Add a test that builds one assistant turn with:
- `ResponseID: "resp_raw_phase11"`
- `ResponseIDHash: "cont-handle-v1:response_id:phase11"`
- response endpoint/storage/request/context metadata

Assert `Convert(header, entries)` omits `response_id` and includes:
```go
wantExtra := map[string]any{
	"response_id_hash":                    "cont-handle-v1:response_id:phase11",
	"response_endpoint":                   "openai_responses",
	"response_storage_scope_fingerprint":  "scope-phase11",
	"response_request_fingerprint":        "request-phase11",
	"response_context_marker":             "ctx-phase11",
}
```

- [x] **Step 2: Run red test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif -run 'TestConvertToATIF_ResponsesProviderHandles' -count=1 -v
```
Expected: FAIL because `Convert` currently emits raw `response_id` and does not emit the hash/metadata.

- [x] **Step 3: Implement redacted default**

Add:
```go
type ProviderHandleMode string

const (
	ProviderHandleModeRedacted ProviderHandleMode = "redacted"
	ProviderHandleModeRawLocal ProviderHandleMode = "raw-local"
)

type Options struct {
	ProviderHandles ProviderHandleMode
}
```

Add `NormalizeProviderHandleMode(mode string) (ProviderHandleMode, error)` accepting `""`, `redacted`, and `raw-local`.

Make `Convert` call `ConvertWithOptions(header, entries, Options{ProviderHandles: ProviderHandleModeRedacted})`. Make assistant conversion put hashed response metadata in `step.Extra` and only add raw `response_id` when mode is `raw-local`.

- [x] **Step 4: Run passing test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif -run 'TestConvertToATIF_ResponsesProviderHandles' -count=1 -v
```
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git status --short
git add agent/internal/atif/atif.go agent/internal/atif/atif_test.go
git commit -m "feat(agent): redact ATIF provider handles by default"
```

### Task 2: Raw-Local Response Handles

**Files:**
- Modify: `agent/internal/atif/atif.go`
- Test: `agent/internal/atif/atif_test.go`

- [x] **Step 1: Write failing raw-local test**

Add a test calling:
```go
traj := ConvertWithOptions(header, entries, Options{ProviderHandles: ProviderHandleModeRawLocal})
```

Assert the assistant step includes both:
```go
step.Extra["response_id"] == "resp_raw_phase11"
step.Extra["response_id_hash"] == "cont-handle-v1:response_id:phase11"
```

- [x] **Step 2: Run red test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif -run 'TestConvertToATIF_ResponsesProviderHandlesRawLocal' -count=1 -v
```
Expected: FAIL until raw-local mode is wired through assistant conversion.

- [x] **Step 3: Implement raw-local response IDs**

Thread `Options` through the assistant conversion path:
```go
step := convertAssistantTurn(turn, stepID, opts)
```

Only emit raw `response_id` when:
```go
opts.ProviderHandles == ProviderHandleModeRawLocal && turn.ResponseID != ""
```

- [x] **Step 4: Run passing test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif -run 'TestConvertToATIF_ResponsesProviderHandles' -count=1 -v
```
Expected: PASS for both redacted and raw-local tests.

- [x] **Step 5: Commit**

```bash
git status --short
git add agent/internal/atif/atif.go agent/internal/atif/atif_test.go
git commit -m "feat(agent): add raw-local ATIF response handles"
```

### Task 3: Request-Side Hash Export From API Calls

**Files:**
- Modify: `agent/internal/atif/atif.go`
- Test: `agent/internal/atif/atif_test.go`

- [ ] **Step 1: Write failing transcript API-call metadata test**

Add a test for:
```go
traj := ConvertTranscriptWithOptions(header, entries, []transcript.APICall{call}, Options{ProviderHandles: ProviderHandleModeRedacted})
```

Use one assistant turn with a locally known response hash and one API call with:
```go
PreviousResponseIDHash: "cont-handle-v1:response_id:phase11",
ConversationIDHash:     "cont-handle-v1:conversation_id:phase11",
HistoryMode:            llm.HistoryModeResponsesDelta,
Request: llm.APILogRequest{
	EndpointFamily:                  llm.EndpointFamilyOpenAIResponses,
	HistoryMode:                     llm.HistoryModeResponsesDelta,
	ResponseStorageScopeFingerprint: "scope-phase11",
	ResponseRequestFingerprint:      "request-phase11",
	ResponseContextMarker:           "ctx-phase11",
},
```

Assert the assistant step includes `previous_response_id_hash`, `conversation_id_hash`, and the request metadata, but no raw `previous_response_id` or `conversation_id`.

- [ ] **Step 2: Run red test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif -run 'TestConvertTranscriptToATIF_ResponsesRequestHandleHashes' -count=1 -v
```
Expected: FAIL because `Convert` ignores API calls.

- [ ] **Step 3: Implement API-call metadata merge**

Add:
```go
func ConvertTranscriptWithOptions(header transcript.Header, entries []transcript.Entry, apiCalls []transcript.APICall, opts Options) Trajectory
```

Keep `ConvertWithOptions` delegating with `nil` API calls. While converting assistant turns, attach the next API call with OpenAI Responses continuation metadata to the next agent step. In redacted mode, emit only hashes and request metadata.

- [ ] **Step 4: Run passing test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif -run 'TestConvertTranscriptToATIF_ResponsesRequestHandleHashes' -count=1 -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add agent/internal/atif/atif.go agent/internal/atif/atif_test.go
git commit -m "feat(agent): include ATIF continuation request hashes"
```

### Task 4: Export API and CLI Mode Plumbing

**Files:**
- Modify: `agent/atif.go`
- Modify: `agent/atif_test.go`
- Modify: `agent/session_config.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `cmd/serf/main.go`
- Modify: `cmd/serf/run.go`
- Modify: `cmd/serf/serve.go`
- Test: `agent/atif_test.go`

- [ ] **Step 1: Write failing export mode test**

In `agent/atif_test.go`, create a transcript with an assistant response ID and call:
```go
err := exportATIF(transcriptPath, outputPath, "raw-local")
```

Assert output JSON contains `response_id`. Add a second assertion for default/redacted mode that omits raw `response_id`.

- [ ] **Step 2: Run red test**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestExportATIF_ProviderHandleModes' -count=1 -v
```
Expected: FAIL because `exportATIF` has no mode argument and does not read API calls.

- [ ] **Step 3: Implement export mode plumbing**

Change `exportATIF` to:
```go
func exportATIF(transcriptPath, outPath, providerHandleMode string) error
```

Use `readTranscriptFull`, normalize the mode, and call:
```go
traj := atif.ConvertTranscriptWithOptions(data.Header, data.Entries, data.APICalls, atif.Options{ProviderHandles: mode})
```

Add `ExportATIFProviderHandles string` to `agent.SessionConfig`, pass it from session close, add `--export-atif-provider-handles` to direct CLI and `serve`, and propagate it into `SessionConfig`.

- [ ] **Step 4: Run passing tests**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestExportATIF_ProviderHandleModes|TestExportATIF_WritesFile' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run 'Test' -count=1
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add agent/atif.go agent/atif_test.go agent/session_config.go agent/session_lifecycle.go cmd/serf/main.go cmd/serf/run.go cmd/serf/serve.go
git commit -m "feat(agent): plumb ATIF provider handle export mode"
```

### Task 5: Hub and TUI Launch Config Plumbing

**Files:**
- Modify: `cmd/serf-hub/internal/launchconfig/types.go`
- Modify: `cmd/serf-hub/internal/launchconfig/schema.go`
- Modify: `cmd/serf-hub/internal/launchconfig/args.go`
- Modify: `cmd/serf-hub/internal/launchconfig/merge.go`
- Modify: `cmd/serf-hub/internal/launchconfig/wire.go`
- Modify: `cmd/serf-hub/internal/launchconfig/*_test.go`
- Modify: `cmd/serf-tui/internal/launchconfig/launch_schema.go`
- Modify: `cmd/serf-tui/internal/launchconfig/launch_settings_panel.go`
- Modify: `cmd/serf-tui/internal/launchconfig/*_test.go`

- [ ] **Step 1: Write failing launch config tests**

Add assertions that `ExportATIFProviderHandles: "raw-local"`:
- survives hub wire round trips,
- appears in CLI args as `--export-atif-provider-handles raw-local`,
- appears in launch schema rows with choices `redacted` and `raw-local`,
- can be edited in the TUI launch settings panel.

- [ ] **Step 2: Run red tests**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig ./cmd/serf-tui/internal/launchconfig -run 'Test.*ATIF|TestSchemaRows|TestLaunchSettingsPanel' -count=1 -v
```
Expected: FAIL until the field is wired.

- [ ] **Step 3: Implement launch config plumbing**

Add `ExportATIFProviderHandles string` with TOML field `export_atif_provider_handles`. Add a debug select schema option with choices:
```go
[]LaunchOptionChoice{
	{Value: "redacted", Label: "redacted"},
	{Value: "raw-local", Label: "raw-local"},
}
```

Merge non-empty launch-layer values, include it in wire structs, emit the CLI flag only when non-empty, and add TUI schema/panel handling matching the existing string-select fields.

- [ ] **Step 4: Run passing launch tests**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig ./cmd/serf-tui/internal/launchconfig -run 'Test.*ATIF|TestSchemaRows|TestLaunchSettingsPanel' -count=1 -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add cmd/serf-hub/internal/launchconfig cmd/serf-tui/internal/launchconfig
git commit -m "feat(hub): expose ATIF provider handle export mode"
```

### Task 6: Phase 11 Proof

**Files:**
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-11-raw-local-export.md`
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-11.md`

- [ ] **Step 1: Run focused verification**

Run:
```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif ./agent ./cmd/serf ./cmd/serf-hub/internal/launchconfig ./cmd/serf-tui/internal/launchconfig -run 'Test.*ATIF|TestSchemaRows|TestLaunchSettingsPanel|TestConvertToATIF|TestConvertTranscriptToATIF' -count=1 -v
git diff --check
```

- [ ] **Step 2: Record proof**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-11.md` with the commands, pass/fail outcome, and any explicit limits, especially that raw `conversation_id` is not emitted unless a future transcript field persists it.

- [ ] **Step 3: Check off plan**

Mark completed steps in this plan.

- [ ] **Step 4: Commit proof**

```bash
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-11-raw-local-export.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-11.md
git commit -m "docs: record responses continuation phase 11 proof"
```

---

## Self-Review

- Spec coverage: default redacted ATIF export, explicit raw-local mode, shared `redacted|raw-local` enum, CLI/serve/hub/TUI plumbing, and diagnostic proof are covered.
- Scope limit: raw response IDs are already persisted on assistant turns and can be exported in raw-local. Raw request-side `previous_response_id` can only be emitted when derivable from a matching local response ID. Raw `conversation_id` is not persisted in the transcript today, so Phase 11 should export the hash and record the limitation rather than inventing or scraping a value.
- Placeholder scan: no TBD/TODO steps.
- Type consistency: the mode name is `ExportATIFProviderHandles` at agent/run/launch layers and `ProviderHandleMode` inside `agent/internal/atif`.
