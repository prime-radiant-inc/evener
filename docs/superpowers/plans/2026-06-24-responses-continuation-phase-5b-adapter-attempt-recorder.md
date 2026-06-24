# Responses Continuation Phase 5B Adapter Attempt Recorder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development for each behavior change. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an adapter-callable attempt recorder so OpenAI Responses-to-Chat fallback attempts are visible as separate ordered provider attempts without letting the adapter write transcripts directly.

**Architecture:** Add a value-like adapter-attempt record and recorder callback to `llm.APILogContext`. The session installs the owning recorder before provider dispatch; it assigns the shared attempt group id and 1-based attempt indexes, then stores records for transcript emission at the normal ordered `api_call` boundary. `llm.APILogger` composes with the same callback and writes matching `api.jsonl` / raw HTTP log entries. The OpenAI adapter only calls the recorder before switching endpoints and when the Chat Completions fallback finishes or fails.

**Tech Stack:** Go, deterministic `agent`, `llm`, and `llm/providers/openai` tests, existing OpenAI `httptest` fallback fixtures.

---

## Dependency Recheck

Phase 5B depends on:

- Phase 5A `attempt_group_id` plumbing across transcript/API/raw logs.
- `llm.APILogger.WrapStream`, which already observes final stream responses and stream errors.
- OpenAI adapter fallback sites:
  - immediate Responses open error fallback in `Adapter.Stream`;
  - empty Responses stream fallback in `Adapter.decodeStream`;
  - Chat Completions fallback stream in `fallbackToChatCompletions` / `streamViaChatCompletions`.
- Session `processOneInput` still calls `logAPICall` once at the ordered transcript boundary after `callModelWithFallback` returns.

## File Structure

- Modify: `llm/apilog.go`
  - Add `AdapterAttemptRecord`, `AdapterAttemptRecorder`, and helper functions.
  - Compose recorder callbacks inside `APILogger.WrapStream`.
  - Suppress the outer APILogger stream line when adapter attempts were explicitly recorded.
- Modify: `llm/providers/openai/adapter.go`
  - Report failed Responses attempts and successful/failed Chat fallback attempts through the callback.
- Modify: `agent/session_model_call.go`
  - Install a session-owned recorder that assigns attempt indexes and stores records.
  - Emit stored adapter-attempt records through transcript `api_call` entries instead of the old single final line when records exist.
- Modify tests:
  - `llm/apilog_test.go`
  - `llm/providers/openai/adapter_test.go`
  - `agent/session_openai_continuation_phase5b_test.go` (new)
- Create proof:
  - `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-5b.md`

## Non-Goals

- Do not add continuation rejection classification.
- Do not add same-Responses-endpoint retry.
- Do not complete `FullHistoryFallbackMessages` fallback cloning.
- Do not broaden real-session `responses_delta` selection beyond the Phase 4D fake slice.
- Do not make live provider calls or enable production registry entries.

---

### Task 1: Add Phase 5B RED Tests

**Files:**
- Modify: `llm/apilog_test.go`
- Modify: `llm/providers/openai/adapter_test.go`
- Create: `agent/session_openai_continuation_phase5b_test.go`

- [x] **Step 1: Prove APILogger writes adapter attempt records**

Add a stream test with an adapter recorder callback that records two attempts and asserts `api.jsonl` and `api-raw.jsonl` contain two lines with matching `attempt_group_id`, ordered indexes, history modes, endpoint URLs, and raw bodies.

- [x] **Step 2: Prove OpenAI adapter invokes the recorder for immediate fallback**

Use the existing 404 Responses-to-Chat fallback fixture and assert the callback receives:

- attempt 1: `history_mode=full_history`, Responses endpoint URL, error, raw Responses body;
- attempt 2: `history_mode=chat_completions_fallback`, Chat endpoint URL, final response, raw Chat body.

- [x] **Step 3: Prove OpenAI adapter invokes the recorder for empty-stream fallback**

Use the existing empty Responses stream fallback fixture and assert the same ordered callback shape.

- [x] **Step 4: Prove session transcript emits separate fallback attempts**

Run a real `Session` against a local OpenAI `httptest` fallback fixture. Assert transcript `api_call` records contain exactly two provider attempts in order under the same `attempt_group_id`, with attempt indexes `1` and `2`, the second terminal record carrying `final_attempt_count=2`, and the final assistant response still persisted once.

- [x] **Step 5: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent -run 'TestAPILoggerWritesAdapterAttemptRecords|TestAdapter_Stream_Records.*FallbackAttempts|TestSession_OpenAIResponsesContinuationPhase5B' -count=1 -v
```

Expected: fail because no adapter attempt recorder exists.

---

### Task 2: Implement Adapter Attempt Recording

**Files:**
- Modify: `llm/apilog.go`
- Modify: `llm/providers/openai/adapter.go`
- Modify: `agent/session_model_call.go`

- [x] **Step 1: Add `llm.AdapterAttemptRecord` and recorder helpers**

The record carries request, optional response, optional error, mode, history mode, endpoint URL/family, assigned attempt metadata, terminal flag, and timing/raw body metadata derived from response/error.

- [x] **Step 2: Compose APILogger with the recorder**

When adapter records exist, write per-attempt API/raw entries and suppress the outer stream log line to avoid duplicate final attempts.

- [x] **Step 3: Add session-owned index allocation and transcript storage**

The first adapter-reported attempt gets index `1`; terminal fallback attempts get `final_attempt_count` equal to the number of recorded attempts. Transcript emission happens at the existing `logAPICall` boundary.

- [x] **Step 4: Record OpenAI fallback attempts**

Report immediate Responses errors, empty Responses stream errors, successful Chat fallback completion, and failed Chat fallback errors.

- [x] **Step 5: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent -run 'TestAPILoggerWritesAdapterAttemptRecords|TestAdapter_Stream_Records.*FallbackAttempts|TestSession_OpenAIResponsesContinuationPhase5B' -count=1 -v
```

Expected: pass.

- [x] **Step 6: Commit implementation**

```sh
git status --short
git add llm/apilog.go llm/apilog_test.go llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go agent/session_model_call.go agent/session_openai_continuation_phase5b_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-5b-adapter-attempt-recorder.md
git commit -m "feat(agent): record openai endpoint fallback attempts"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-5b.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-5b-adapter-attempt-recorder.md`

- [x] **Step 1: Add proof artifact**

Record RED, GREEN, `git diff --check`, and the boundaries left for Phases 6-8.

- [x] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent -run 'TestAPILoggerWritesAdapterAttemptRecords|TestAdapter_Stream_Records.*FallbackAttempts|TestSession_OpenAIResponsesContinuationPhase5B' -count=1 -v
git diff --check
```

- [x] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-5b-adapter-attempt-recorder.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-5b.md
git commit -m "docs: record responses continuation phase 5b proof"
```
