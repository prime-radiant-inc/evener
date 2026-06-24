# Responses Continuation Phase 9 Real-Session Delta Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete real-session `responses_delta` behavior beyond the Phase 4D fake-provider slice while keeping full-history fallback and replay sanitization safe.

**Architecture:** Keep anchor selection in `agent/responses_continuation_eligibility.go` and request shaping in `agent/session_model_call.go`. Real fallback-capable OpenAI paths become eligible for delta only when a full-history fallback sidecar is attached before replacing request messages with the delta slice. Delta eligibility is conservative: unsupported turn kinds, orphaned tool results, media/provider-hosted/reasoning content, and non-anchorable intervening assistant turns all use full history.

**Tech Stack:** Go, deterministic package-agent tests with fake adapters and `httptest` OpenAI adapter tests, no live provider calls.

---

## Scope

Phase 9 implements the finite checklist from the continuation design:

- Real-session anchor selection wiring for fallback-capable OpenAI paths.
- Real-path item-kind gate enforcement.
- `call_id` linkage validation for tool-result deltas.
- Intervening non-anchorable assistant handling.
- Media/provider-hosted/reasoning gating on the real path.
- Phase 8 retry repeated through real session anchor selection.
- Full-history replay sanitizer remains active on fallback/recovery paths.
- Session-local continuation-disabled state for endpoint-level rejection.

Non-goals:

- Do not enable production registry defaults.
- Do not add live OpenAI/Codex tests.
- Do not implement Phase 10 token-pressure accounting.
- Do not implement Phase 11 raw-local export.
- Do not change adapter request serialization beyond tests that observe existing OpenAI request bodies.

## File Structure

- Modify: `agent/session_model_call.go`
  - Attach `FullHistoryFallbackMessages` before shaping delta requests.
  - Consult session-local disabled state before selecting a delta.
  - Mark continuation disabled after endpoint-level continuation rejection.
- Modify: `agent/responses_continuation_eligibility.go`
  - Add delta eligibility validation for turn kinds, tool-result `call_id` linkage, and unsafe content.
- Modify: `agent/session_config.go`
  - Add unexported session state for disabled continuation entries if no suitable field exists elsewhere.
- Modify: `agent/session_openai_continuation_phase4d_test.go`
  - Update the real OpenAI/fallback-capable regression from full history to delta-with-sidecar behavior.
- Create: `agent/session_openai_continuation_phase9_test.go`
  - Add real-session Phase 9 tests for fallback-capable delta, gates, retry-through-anchor-selection, sanitizer, and disabled state.
- Modify: `agent/responses_continuation_eligibility_test.go`
  - Add pure eligibility tests for item kinds, tool-result linkage, and unsafe content.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-9.md`
  - Record RED/GREEN evidence and remaining Phase 10-12 work.

---

### Task 1: Real Fallback-Capable Delta Sidecar

**Files:**
- Modify: `agent/session_openai_continuation_phase4d_test.go`
- Modify: `agent/session_model_call.go`

- [x] **Step 1: Write RED regression for fallback-capable path**

Change `TestSession_OpenAIResponsesContinuationPhase4DIIRealOpenAIAdapterUsesFullHistoryUntilFallbackClone` into `TestSession_OpenAIResponsesContinuationPhase9RealOpenAIAdapterUsesFullHistoryWhenAnchorFingerprintMismatches` and add the fake-adapter sidecar test. The real OpenAI fixture uses a helper anchor with intentionally mismatched planner fingerprints, so it must remain full history while still proving continuation-owned storage can produce the next anchor.

Expected assertions:

```go
if _, ok := req["previous_response_id"]; !ok {
	t.Fatalf("real OpenAI request must send previous_response_id after Phase 9: %s", string(bodies[0]))
}
if !responsesInputContainsText(input, "real openai current user marker") {
	t.Fatalf("delta request missing current user marker: %#v", input)
}
if responsesInputContainsText(input, "real openai prior user marker") {
	t.Fatalf("delta request included pre-anchor text: %#v", input)
}
```

Also add a package-agent fake-adapter test in `agent/session_openai_continuation_phase9_test.go` that asserts:

- request `HistoryMode == responses_delta`;
- `PreviousResponseID` is set;
- `FullHistoryFallbackMessages` contains both prior and current markers;
- `Messages` contains only system/developer prefix plus the current delta marker.

- [x] **Step 2: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9RealOpenAIAdapterUsesDeltaWithFallbackSidecar|TestSession_OpenAIResponsesContinuationPhase9FallbackCapableFakePathCarriesFullHistorySidecar' -count=1 -v
```

Expected: fake-adapter sidecar test fails because fallback-capable paths still use full history when `FullHistoryFallbackMessages` is empty.

- [x] **Step 3: Implement sidecar attachment**

In `applyResponsesContinuationAnchorPlanning`, preserve the original full-history messages before replacing request messages with delta:

```go
fullHistoryMessages := append([]llm.Message(nil), req.Messages...)
if plan.CanFallbackToChat && len(fullHistoryMessages) > 0 {
	req.FullHistoryFallbackMessages = fullHistoryMessages
}
```

Remove the old fallback-capable early return that forced full history only because the sidecar was empty.

- [x] **Step 4: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9FallbackCapablePathProducesFullHistoryAnchor|TestSession_OpenAIResponsesContinuationPhase9RealOpenAIAdapterUsesFullHistoryWhenAnchorFingerprintMismatches|TestSession_OpenAIResponsesContinuationPhase9FallbackCapableFakePathCarriesFullHistorySidecar|TestSession_OpenAIResponsesContinuationPhase4DIIConsumesStoredAnchorAsDelta' -count=1 -v
```

Update the old Phase 4D fallback-capable fake test name/expectations if the test now belongs to Phase 9.

- [x] **Step 5: Commit**

```sh
git status --short
git add agent/session_model_call.go agent/session_openai_continuation_phase4d_test.go agent/session_openai_continuation_phase9_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-9-real-session-delta.md
git commit -m "feat(agent): enable fallback-capable responses delta sidecar"
```

---

### Task 2: Delta Eligibility Gates

**Files:**
- Modify: `agent/responses_continuation_eligibility.go`
- Modify: `agent/responses_continuation_eligibility_test.go`
- Modify: `agent/session_openai_continuation_phase9_test.go`

- [x] **Step 1: Add RED pure eligibility tests**

Add tests proving:

- checkpoint or summary inside `Delta` returns full history;
- a delta assistant turn without continuation metadata returns full history;
- a tool result whose `ToolCallID` is present in the anchor assistant tool calls is eligible;
- a tool result whose `ToolCallID` is absent from the anchor assistant tool calls returns full history;
- user image/audio/document content returns full history;
- thinking/redacted-thinking/web-search content returns full history.

Use exact reason strings:

- `continuation_delta_unsupported_turn_kind`
- `continuation_delta_orphaned_tool_result`
- `continuation_delta_unsafe_content`

- [x] **Step 2: Implement eligibility helper**

Add:

```go
func responsesContinuationDeltaIneligibleReason(anchor schema.Turn, delta []schema.Turn) string
```

Rules:

- Allowed delta turn kinds: `TurnUserInput`, `TurnToolResults`, and legacy `TurnTool`.
- Tool-result deltas must reference tool-call ids present in the anchor assistant message.
- Content kinds allowed in delta: text and tool result without image data.
- Content kinds disallowed in delta: image, audio, document, thinking, redacted thinking, web search, tool calls, and tool results with image data/media type.

Call the helper from `selectResponsesContinuationAnchorCandidate` after `delta` is non-empty.

- [x] **Step 3: Add real-session gate tests**

In `agent/session_openai_continuation_phase9_test.go`, add fake-adapter tests where the enabled registry and eligible anchor are present but the post-anchor history includes:

- orphaned tool result;
- media user content;
- intervening non-anchorable assistant.

Assert each request uses `HistoryModeFullHistory`, has no `PreviousResponseID`, and has no continuation metadata with `PreviousResponseIDHash`.

- [x] **Step 4: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate|TestSession_OpenAIResponsesContinuationPhase9.*Gate' -count=1 -v
```

- [x] **Step 5: Commit**

```sh
git status --short
git add agent/responses_continuation_eligibility.go agent/responses_continuation_eligibility_test.go agent/session_openai_continuation_phase9_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-9-real-session-delta.md
git commit -m "feat(agent): gate responses continuation delta inputs"
```

---

### Task 3: Real-Session Retry and Sanitizer

**Files:**
- Modify: `agent/session_openai_continuation_phase9_test.go`
- Modify: `agent/session_openai_malformed_tool_call_test.go` if sanitizer assertions need sharing.

- [ ] **Step 1: Add RED retry-through-anchor-selection test**

Use a fake adapter with enabled continuation plan and fallback-capable path:

- session history contains an eligible anchor and a new user delta;
- first request is `responses_delta` and returns `previous_response_not_found`;
- second request is same model `full_history_fallback`;
- configured fallback model is not called when full-history recovery succeeds.

Assert the request list is `[responses_delta, full_history_fallback]`.

- [ ] **Step 2: Add RED sanitizer-on-fallback test**

Use an eligible anchor containing a malformed historical tool call and a linked tool-result delta. Force the first delta attempt to return `previous_response_not_found`, then inspect the full-history fallback request. Assert:

- the malformed historical function call is present only in sanitized form;
- the linked tool result is present;
- the fallback request has no `PreviousResponseID`;
- the fallback request `HistoryMode == full_history_fallback`.

- [ ] **Step 3: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9.*Retry|TestSession_OpenAIResponsesContinuationPhase9.*Sanitizer' -count=1 -v
```

Expected: retry test passes after Phase 8 only if sidecar work is complete; sanitizer test fails until full-history fallback messages are verified on the real-session delta path.

- [ ] **Step 4: Implement only missing sanitizer glue**

If fallback messages already use `req.Messages` before delta shaping, no code change is needed. If they bypass the existing OpenAI full-history sanitizer, route the same fallback message slice through the existing request serialization path rather than adding a parallel sanitizer.

- [ ] **Step 5: Run focused tests and commit**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9.*Retry|TestSession_OpenAIResponsesContinuationPhase9.*Sanitizer|TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay' -count=1 -v
git status --short
git add agent/session_openai_continuation_phase9_test.go agent/session_openai_malformed_tool_call_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-9-real-session-delta.md
git commit -m "test(agent): prove responses continuation retry real path"
```

---

### Task 4: Session-Local Disabled State

**Files:**
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_config.go` or the nearest session state file containing unexported runtime-only fields.
- Modify: `agent/session_openai_continuation_phase9_test.go`

- [ ] **Step 1: Add RED disabled-state tests**

Add tests proving:

- after endpoint-level continuation rejection, the same live session uses full history for the same provider/model/storage-scope/policy/stream path;
- restore/new session does not inherit disabled state;
- changing storage scope or storage policy does not consult the old disabled entry.

- [ ] **Step 2: Implement disabled key and state**

Add a private key type:

```go
type responsesContinuationDisabledKey struct {
	Provider                  string
	Model                     string
	EndpointFamily            string
	StorageScopeFingerprint   string
	StoragePolicyLabel        string
	Stream                    bool
}
```

Store disabled keys on `Session`, initialized in `NewSession`/restore construction as an empty map. The map is runtime-only and must not enter snapshots.

- [ ] **Step 3: Consult and mark disabled state**

Before selecting a delta, if the key is disabled, use full history. When `callModelWithFallback` sees a continuation rejection for a delta request, mark the key disabled before issuing full-history recovery.

- [ ] **Step 4: Run focused tests and commit**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9.*Disabled' -count=1 -v
git status --short
git add agent/session_model_call.go agent/session_config.go agent/session_openai_continuation_phase9_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-9-real-session-delta.md
git commit -m "feat(agent): add session-local responses continuation disablement"
```

---

### Task 5: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-9.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-9-real-session-delta.md`

- [ ] **Step 1: Run full focused Phase 9 verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9|TestResponsesContinuationAnchorCandidate|TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestFallbackChain_Continuation' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_Continuation|TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_ClassifyResponsesError' -count=1 -v
git diff --check
```

- [ ] **Step 2: Add proof artifact**

Record RED/GREEN for each Phase 9 task, list committed hashes, and state that runtime registry entries remain disabled until Phase 12.

- [ ] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-9-real-session-delta.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-9.md
git commit -m "docs: record responses continuation phase 9 proof"
```

## Self-Review

- Spec coverage: each Phase 9 checklist item maps to a task above. Phase 10-12 remain out of scope.
- Placeholder scan: no task depends on an unspecified helper or deferred behavior.
- Type consistency: plan uses existing `llm.Request`, `schema.Turn`, `Session`, `HistoryModeResponsesDelta`, `HistoryModeFullHistory`, `HistoryModeFullHistoryFallback`, `FullHistoryFallbackMessages`, and current Phase 4D/8 helper patterns.
