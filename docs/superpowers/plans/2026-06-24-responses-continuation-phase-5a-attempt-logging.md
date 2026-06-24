# Responses Continuation Phase 5A Attempt Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development for each behavior change. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add attempt-group identity and the minimum transcript/API/raw-log fields needed to correlate session-owned provider attempts.

**Architecture:** Reuse the existing single-attempt stamping path from Phase 1A. Generate one attempt group id at the same boundary that assigns `AttemptIndex=1`, then thread that value through `ModelAttemptMetadata`, `llm.APILogContext`, transcript `api_call` records, `llm.APILogger` entries, and raw HTTP log entries. Keep the current terminal-attempt `final_attempt_count` shape chosen by Phase 0A. Do not implement adapter fallback callbacks, retry classification, or multi-attempt transcript emission in this phase.

**Tech Stack:** Go, deterministic `llm` and `agent` unit tests, existing `ulid.Make()` id style, existing raw HTTP body error path.

---

## Dependency Recheck

Phase 5A depends on the following current substrate:

- `singleAttemptRequestMetadata` is the Phase 1A single-attempt stamping boundary.
- `callModelWithFallback` already passes attempt metadata into `llm.WithAPILogAttemptContext` before the provider dispatch.
- `logAPICall` writes the session-owned transcript `api_call` record after dispatch.
- `llm.APILogger` writes both `api.jsonl` and `api-raw.jsonl`; stream errors call `writeRawError` through `apiLogStream.logError`.
- Phase 0A selected terminal-attempt `final_attempt_count`; Phase 5A keeps that shape for single-attempt records.

## File Structure

- Modify: `agent/session_model_call.go`
  - Generate and carry `AttemptGroupID` in `ModelAttemptMetadata`.
  - Pass it into `llm.APILogContext`.
  - Persist it on transcript `api_call`.
- Modify: `agent/transcript/transcript.go`
  - Add optional `attempt_group_id` to `APICall`.
- Modify: `llm/apilog.go`
  - Add optional `attempt_group_id` to `APILogContext`, `APILogEntry`, and `APIRawLogEntry`.
  - Copy attempt fields from API log entries into raw response and raw error entries.
- Modify: `llm/apilog_test.go`, `agent/transcript_test.go`, `agent/session_test.go`
  - Add RED tests for API log, raw log, stream-error raw log, transcript round trip, and real session transcript emission.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-5a.md`
  - Record proof commands and limits.

## Non-Goals

- Do not add the adapter-callable attempt recorder from Phase 5B.
- Do not emit multiple transcript `api_call` records for model fallback or endpoint fallback.
- Do not change retry/fallback classification.
- Do not enable any production continuation registry entry.
- Do not make live provider requests.

---

### Task 1: Add Phase 5A RED Tests

**Files:**
- Modify: `llm/apilog_test.go`
- Modify: `agent/transcript_test.go`
- Modify: `agent/session_test.go`

- [x] **Step 1: Extend API log attempt round-trip coverage**

Update the API log attempt test to assert `AttemptGroupID` round-trips alongside the existing attempt fields.

- [x] **Step 2: Add raw log attempt metadata coverage**

Add a raw-log test proving a complete response raw entry includes:

- `attempt_group_id`
- `attempt_index`
- `attempt_count`
- `final_attempt_count`
- `history_mode`

- [x] **Step 3: Add stream-error raw log coverage**

Add a stream test that sends `StreamEventError` with `NewStreamErrorWithRawBodies` and asserts the raw log entry carries the same attempt metadata. This covers continuation-style rejections delivered after stream setup.

- [x] **Step 4: Extend transcript round-trip and session transcript coverage**

Assert transcript `api_call` records persist a non-empty attempt group id and that a real single-attempt session emits it with the existing 1-based attempt index.

- [x] **Step 5: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILogger.*Attempt|TestTranscriptContinuationMetadataRoundTrips|TestSessionRecordsAssistantResponseMetadata|TestSingleAttemptRequestMetadataKeepsAttemptCountersOffRequest' -count=1 -v
```

Expected: fail because `attempt_group_id` and raw attempt fields are missing.

---

### Task 2: Implement Attempt Group Plumbing

**Files:**
- Modify: `agent/session_model_call.go`
- Modify: `agent/transcript/transcript.go`
- Modify: `llm/apilog.go`

- [x] **Step 1: Generate attempt group ids in the existing stamping path**

Add a small private helper returning `"ag_" + ulid.Make().String()` and assign it in `singleAttemptRequestMetadata`.

- [x] **Step 2: Thread group id into session logging**

Add `AttemptGroupID` to `ModelAttemptMetadata`, copy it into `llm.APILogContext`, and persist it in transcript `APICall`.

- [x] **Step 3: Thread group id and attempt fields into API/raw logs**

Add `AttemptGroupID` to `APILogContext`, `APILogEntry`, and `APIRawLogEntry`; copy API-log attempt fields into both raw response and raw error entries.

- [x] **Step 4: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILogger.*Attempt|TestTranscriptContinuationMetadataRoundTrips|TestSessionRecordsAssistantResponseMetadata|TestSingleAttemptRequestMetadataKeepsAttemptCountersOffRequest' -count=1 -v
```

Expected: pass.

- [ ] **Step 5: Commit implementation**

```sh
git status --short
git add agent/session_model_call.go agent/session_test.go agent/transcript/transcript.go agent/transcript_test.go llm/apilog.go llm/apilog_test.go
git commit -m "feat(agent): add responses continuation attempt group logging"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-5a.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-5a-attempt-logging.md`

- [ ] **Step 1: Add proof artifact**

Record the focused test command, `git diff --check`, and the Phase 5A boundaries.

- [ ] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILogger.*Attempt|TestTranscriptContinuationMetadataRoundTrips|TestSessionRecordsAssistantResponseMetadata|TestSingleAttemptRequestMetadataKeepsAttemptCountersOffRequest' -count=1 -v
git diff --check
```

- [ ] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-5a-attempt-logging.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-5a.md
git commit -m "docs: record responses continuation phase 5a proof"
```
