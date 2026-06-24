# Responses Continuation Phase 6 Fallback Clone Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development for each behavior change. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make OpenAI Chat Completions fallback consume `FullHistoryFallbackMessages` for delta-shaped requests, without broadening real-session `responses_delta` selection.

**Architecture:** Keep session continuation selection unchanged. Add an adapter-local fallback request clone for Chat Completions fallback. When `FullHistoryFallbackMessages` is present, the clone replaces `Messages`, clears `PreviousResponseID`, clears `ConversationID`, clears continuation metadata, and sets `HistoryMode=chat_completions_fallback` before `buildChatCompletionsBody` runs. Existing provider-safe replay serialization stays inside the Chat body builder.

**Tech Stack:** Go, deterministic OpenAI adapter tests with `httptest`, existing `FullHistoryFallbackMessages` request field.

---

## Dependency Recheck

Phase 6 depends on:

- Phase 4D-ii delta request shaping and its real-OpenAI fallback-capability guard.
- Phase 5B adapter fallback attempt recording.
- OpenAI adapter fallback sites for immediate Responses errors and empty Responses streams.
- `buildChatCompletionsBody` as the existing provider-safe replay serialization boundary.

## File Structure

- Modify: `llm/providers/openai/adapter.go`
  - Add an adapter-local `chatFallbackRequest` helper.
  - Use it for immediate and empty-stream Chat fallback.
  - Use the clone for Chat fallback attempt records.
- Modify: `llm/providers/openai/adapter_test.go`
  - Add immediate and empty-stream fallback tests proving Chat receives full-history fallback messages, not delta messages or `previous_response_id`.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-6.md`

## Non-Goals

- Do not enable real-session delta selection for fallback-capable OpenAI paths.
- Do not add continuation-error classification.
- Do not add same-Responses-endpoint retry.
- Do not make live provider calls.

---

### Task 1: Add Phase 6 RED Tests

**Files:**
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Immediate fallback test**

Drive a test-only `responses_delta` request with:

- delta-only `Messages` containing a unique delta marker;
- `PreviousResponseID`;
- `Continuation`;
- `FullHistoryFallbackMessages` containing a unique full-history marker.

Make `/v1/responses` return fallback-eligible 404 and `/v1/chat/completions` succeed. Assert the captured Chat request contains the full-history marker, omits the delta marker, and has no `previous_response_id`.

- [ ] **Step 2: Empty-stream fallback test**

Repeat the same assertion when `/v1/responses` returns an empty 200 stream.

- [ ] **Step 3: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages' -count=1 -v
```

Expected: fail because Chat fallback currently builds from the delta request.

---

### Task 2: Implement Chat Fallback Clone

**Files:**
- Modify: `llm/providers/openai/adapter.go`

- [ ] **Step 1: Add `chatFallbackRequest`**

Clone the request, set `HistoryMode=chat_completions_fallback`, clear Responses-only continuation fields, and replace `Messages` with `FullHistoryFallbackMessages` when present.

- [ ] **Step 2: Use the clone in both fallback sites**

Call `streamViaChatCompletions` and `recordChatFallbackAttempt` with the clone for immediate and empty-stream fallback.

- [ ] **Step 3: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_Stream_Records.*FallbackAttempts|TestStream_ResponsesAPI_404_FallsBackToChatCompletions|TestAdapter_Stream_StampsEndpointURL_ChatCompletionsFallback' -count=1 -v
```

Expected: pass.

- [ ] **Step 4: Commit implementation**

```sh
git status --short
git add llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-6-fallback-clone.md
git commit -m "feat(openai): clone full history for chat fallback"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-6.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-6-fallback-clone.md`

- [ ] **Step 1: Add proof artifact**

Record RED, GREEN, `git diff --check`, and the fact that real-session delta selection remains unchanged.

- [ ] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_Stream_Records.*FallbackAttempts|TestStream_ResponsesAPI_404_FallsBackToChatCompletions|TestAdapter_Stream_StampsEndpointURL_ChatCompletionsFallback' -count=1 -v
git diff --check
```

- [ ] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-6-fallback-clone.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-6.md
git commit -m "docs: record responses continuation phase 6 proof"
```
