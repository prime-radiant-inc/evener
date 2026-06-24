# Responses Continuation Phase 0A Audits Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the disabled-by-default OpenAI Responses continuation registry and the Phase 0A proof that `auto` plus disabled endpoint support still uses full-history requests.

**Architecture:** Keep this phase as production-no-op plumbing. The new `llm` helper owns endpoint-family names, support registry defaults, history-mode names, and a pure decision function; session runtime is not wired to use continuation. The proof artifact records the two required audits from current code: model-call serialization and API/raw logging write shape.

**Tech Stack:** Go, deterministic `httptest` OpenAI Responses fixture, existing Serf session loop, markdown proof artifact.

---

## File Structure

- Create `llm/responses_continuation.go`
  - Owns `HistoryMode`, `ResponsesContinuationMode`, `ResponsesEndpointFamily`, `ResponsesContinuationSupport`, production registry defaults, registry lookup, and the disabled/enabled decision helper.
- Create `llm/responses_continuation_test.go`
  - Tests production defaults and pure `auto`/`off` decisions without provider credentials or network.
- Create `agent/session_openai_continuation_phase0a_test.go`
  - Uses a local `httptest.Server` and the real OpenAI Responses adapter to prove current session runtime still sends full history when the new registry defaults are disabled.
- Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0a.md`
  - Records the three separately checkable Phase 0A lines: registry defaults, serialization audit with `reservation required: yes`, and logging audit with terminal-attempt `final_attempt_count` shape.

## Task 1: Endpoint-Family Registry

**Files:**
- Create: `llm/responses_continuation.go`
- Create: `llm/responses_continuation_test.go`

- [ ] **Step 1: Write the failing registry tests**

Create `llm/responses_continuation_test.go` with this content:

```go
package llm

import "testing"

func TestDefaultResponsesContinuationSupportRegistryDisabled(t *testing.T) {
	registry := DefaultResponsesContinuationSupportRegistry()

	for _, family := range []ResponsesEndpointFamily{
		ResponsesEndpointFamilyOpenAIPublic,
		ResponsesEndpointFamilyOpenAICodex,
	} {
		support, ok := registry[family]
		if !ok {
			t.Fatalf("registry missing endpoint family %q", family)
		}
		if support.EndpointFamily != family {
			t.Fatalf("support.EndpointFamily = %q, want %q", support.EndpointFamily, family)
		}
		if support.Enabled {
			t.Fatalf("%s Enabled = true, want false", family)
		}
		if support.StorageShapeProven {
			t.Fatalf("%s StorageShapeProven = true, want false", family)
		}
		if support.ProductionPathProven {
			t.Fatalf("%s ProductionPathProven = true, want false", family)
		}
		if support.MaxAnchorAgeSeconds != 0 {
			t.Fatalf("%s MaxAnchorAgeSeconds = %d, want 0", family, support.MaxAnchorAgeSeconds)
		}
		if support.StorageShapeProofID != "" {
			t.Fatalf("%s StorageShapeProofID = %q, want empty", family, support.StorageShapeProofID)
		}
		if support.ProductionPathProofID != "" {
			t.Fatalf("%s ProductionPathProofID = %q, want empty", family, support.ProductionPathProofID)
		}
	}
}

func TestResponsesContinuationSupportForUnknownFamilyDisabled(t *testing.T) {
	support := ResponsesContinuationSupportFor(
		DefaultResponsesContinuationSupportRegistry(),
		ResponsesEndpointFamily("unknown_endpoint"),
	)

	if support.EndpointFamily != ResponsesEndpointFamily("unknown_endpoint") {
		t.Fatalf("EndpointFamily = %q, want requested family", support.EndpointFamily)
	}
	if support.Enabled {
		t.Fatal("unknown endpoint family must be disabled")
	}
}

func TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge(t *testing.T) {
	enabled := ResponsesContinuationSupport{
		EndpointFamily:          ResponsesEndpointFamilyOpenAIPublic,
		StorageShapeProven:      true,
		ProductionPathProven:    true,
		Enabled:                 true,
		MaxAnchorAgeSeconds:     3600,
		StorageShapeProofID:     "phase-0b-public",
		ProductionPathProofID:   "phase-12a-public",
	}

	tests := []struct {
		name    string
		mode    ResponsesContinuationMode
		support ResponsesContinuationSupport
		want    ResponsesContinuationDecision
	}{
		{
			name:    "off ignores enabled support",
			mode:    ResponsesContinuationOff,
			support: enabled,
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_off",
			},
		},
		{
			name: "auto with default disabled public support uses full history",
			mode: ResponsesContinuationAuto,
			support: ResponsesContinuationSupportFor(
				DefaultResponsesContinuationSupportRegistry(),
				ResponsesEndpointFamilyOpenAIPublic,
			),
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_endpoint_not_enabled",
			},
		},
		{
			name: "auto with enabled support but no anchor age uses full history",
			mode: ResponsesContinuationAuto,
			support: ResponsesContinuationSupport{
				EndpointFamily:       ResponsesEndpointFamilyOpenAIPublic,
				StorageShapeProven:   true,
				ProductionPathProven: true,
				Enabled:              true,
			},
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_anchor_age_unbounded",
			},
		},
		{
			name:    "auto with proven enabled support allows responses delta",
			mode:    ResponsesContinuationAuto,
			support: enabled,
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeResponsesDelta,
				Reason:      "continuation_enabled",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideResponsesContinuation(tc.mode, tc.support)
			if got != tc.want {
				t.Fatalf("decision = %+v, want %+v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run registry tests and verify they fail**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestDefaultResponsesContinuationSupportRegistryDisabled|TestResponsesContinuationSupportForUnknownFamilyDisabled|TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge' -count=1 -v
```

Expected: FAIL with undefined identifiers such as `DefaultResponsesContinuationSupportRegistry`.

- [ ] **Step 3: Add the registry implementation**

Create `llm/responses_continuation.go` with this content:

```go
package llm

type HistoryMode string

const (
	HistoryModeFullHistory         HistoryMode = "full_history"
	HistoryModeResponsesDelta      HistoryMode = "responses_delta"
	HistoryModeFullHistoryFallback HistoryMode = "full_history_fallback"
	HistoryModeChatFallback        HistoryMode = "chat_completions_fallback"
)

type ResponsesContinuationMode string

const (
	ResponsesContinuationOff  ResponsesContinuationMode = "off"
	ResponsesContinuationAuto ResponsesContinuationMode = "auto"
)

type ResponsesEndpointFamily string

const (
	ResponsesEndpointFamilyOpenAIPublic ResponsesEndpointFamily = "openai_public"
	ResponsesEndpointFamilyOpenAICodex  ResponsesEndpointFamily = "openai_codex"
)

type ResponsesContinuationSupport struct {
	EndpointFamily        ResponsesEndpointFamily
	StorageShapeProven    bool
	ProductionPathProven  bool
	Enabled               bool
	MaxAnchorAgeSeconds   int64
	StorageShapeProofID   string
	ProductionPathProofID string
}

type ResponsesContinuationDecision struct {
	HistoryMode HistoryMode
	Reason      string
}

func DefaultResponsesContinuationSupportRegistry() map[ResponsesEndpointFamily]ResponsesContinuationSupport {
	return map[ResponsesEndpointFamily]ResponsesContinuationSupport{
		ResponsesEndpointFamilyOpenAIPublic: disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAIPublic),
		ResponsesEndpointFamilyOpenAICodex:  disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAICodex),
	}
}

func ResponsesContinuationSupportFor(registry map[ResponsesEndpointFamily]ResponsesContinuationSupport, family ResponsesEndpointFamily) ResponsesContinuationSupport {
	if support, ok := registry[family]; ok {
		return support
	}
	return disabledResponsesContinuationSupport(family)
}

func DecideResponsesContinuation(mode ResponsesContinuationMode, support ResponsesContinuationSupport) ResponsesContinuationDecision {
	if mode != ResponsesContinuationAuto {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_off",
		}
	}
	if !support.Enabled {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_endpoint_not_enabled",
		}
	}
	if support.MaxAnchorAgeSeconds <= 0 {
		return ResponsesContinuationDecision{
			HistoryMode: HistoryModeFullHistory,
			Reason:      "continuation_anchor_age_unbounded",
		}
	}
	return ResponsesContinuationDecision{
		HistoryMode: HistoryModeResponsesDelta,
		Reason:      "continuation_enabled",
	}
}

func disabledResponsesContinuationSupport(family ResponsesEndpointFamily) ResponsesContinuationSupport {
	return ResponsesContinuationSupport{EndpointFamily: family}
}
```

- [ ] **Step 4: Run registry tests and verify they pass**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestDefaultResponsesContinuationSupportRegistryDisabled|TestResponsesContinuationSupportForUnknownFamilyDisabled|TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit registry changes**

Run:

```sh
git status --short
git add llm/responses_continuation.go llm/responses_continuation_test.go
git commit -m "feat(llm): add disabled responses continuation registry" -m "Add the Phase 0A endpoint-family support registry and pure history-mode decision helper. Production endpoint-family rows for public OpenAI Responses and Codex Responses default to Enabled=false, no proof ids, and no max anchor age, so auto mode still resolves to full_history until a later proof-backed enablement commit flips a single endpoint family."
```

## Task 2: Disabled Registry Full-History Session Proof

**Files:**
- Create: `agent/session_openai_continuation_phase0a_test.go`

- [ ] **Step 1: Write the deterministic OpenAI Responses session test**

Create `agent/session_openai_continuation_phase0a_test.go` with this content:

```go
package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory(t *testing.T) {
	dir := t.TempDir()

	decision := llm.DecideResponsesContinuation(
		llm.ResponsesContinuationAuto,
		llm.ResponsesContinuationSupportFor(
			llm.DefaultResponsesContinuationSupportRegistry(),
			llm.ResponsesEndpointFamilyOpenAIPublic,
		),
	)
	if decision.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("default registry decision = %+v, want full_history", decision)
	}

	var mu sync.Mutex
	var requestBodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeResponsesText(t, w, flusher, "resp_new", "continued with full history")
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient()
	client.Register(&openai.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.history = append(sess.history,
		schema.Turn{
			Kind:       schema.TurnUserInput,
			Message:    llm.User("earlier user marker"),
			ResponseID: "resp_existing_user_field_ignored",
			Timestamp:  time.Now().UTC().Add(-time.Minute),
		},
		schema.Turn{
			Kind:       schema.TurnAssistant,
			Message:    llm.Assistant("earlier assistant marker"),
			ResponseID: "resp_existing_anchor",
			Timestamp:  time.Now().UTC().Add(-time.Minute),
		},
	)

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := sess.ProcessInput(ctx, "new user marker", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(got, "continued with full history") {
		t.Fatalf("ProcessInput output = %q, want server text", got)
	}

	sess.Close()
	<-eventsDone

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("OpenAI Responses request count = %d, want 1", len(bodies))
	}

	req := decodeResponsesRequest(t, bodies[0])
	if _, ok := req["previous_response_id"]; ok {
		t.Fatalf("disabled registry must not send previous_response_id: %s", string(bodies[0]))
	}
	if gotStore, ok := req["store"].(bool); !ok || gotStore {
		t.Fatalf("disabled registry request store = %#v, want explicit false", req["store"])
	}

	input := responsesInputItems(t, req)
	for _, marker := range []string{"earlier user marker", "earlier assistant marker", "new user marker"} {
		if !responsesInputContainsText(input, marker) {
			t.Fatalf("full-history request missing %q in input: %#v", marker, input)
		}
	}
}

func writeResponsesText(t *testing.T, w io.Writer, flusher http.Flusher, responseID, text string) {
	t.Helper()
	item := map[string]any{
		"id":      "msg_" + responseID,
		"type":    "message",
		"status":  "completed",
		"role":    "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
	writeSSE(t, w, flusher, "response.output_item.done", map[string]any{
		"type": "response.output_item.done",
		"item": item,
	})
	writeSSE(t, w, flusher, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     responseID,
			"model":  "gpt-5.2",
			"status": "completed",
			"output": []any{item},
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
		},
	})
}

func responsesInputContainsText(items []any, want string) bool {
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, partAny := range content {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok && strings.Contains(text, want) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 2: Run the session proof and verify it passes**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory -count=1 -v
```

Expected: PASS.

- [ ] **Step 3: Commit session proof**

Run:

```sh
git status --short
git add agent/session_openai_continuation_phase0a_test.go
git commit -m "test(agent): prove disabled responses continuation uses full history" -m "Add a deterministic OpenAI Responses session proof for Phase 0A. The test ties the disabled production registry decision to the current session runtime: even with an existing assistant ResponseID in history and auto mode, the request sends no previous_response_id, keeps public OpenAI store false, and includes the prior and current turn markers as full history."
```

## Task 3: Phase 0A Proof Artifact

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0a.md`

- [ ] **Step 1: Create the proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0a.md` with this content:

```markdown
# Responses Continuation Phase 0A Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 0A-audits

## Registry Defaults

Checkable line: `DefaultResponsesContinuationSupportRegistry` has production rows for `openai_public` and `openai_codex`; both rows have `Enabled=false`, `StorageShapeProven=false`, `ProductionPathProven=false`, `MaxAnchorAgeSeconds=0`, and empty proof ids.

Evidence:
- `llm/responses_continuation.go`
- `llm/responses_continuation_test.go:TestDefaultResponsesContinuationSupportRegistryDisabled`
- `llm/responses_continuation_test.go:TestResponsesContinuationSupportForUnknownFamilyDisabled`
- `llm/responses_continuation_test.go:TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge`

Runtime no-op proof:
- `agent/session_openai_continuation_phase0a_test.go:TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory` proves `auto` plus the disabled public OpenAI registry decision still sends a full-history OpenAI Responses request with no `previous_response_id` and `store:false`.

## Serialization Audit

Checkable line: reservation required: yes.

Evidence:
- `agent/session_lifecycle.go:274-317` starts a `ProcessInputKind` drain loop and calls `processOneInput` sequentially inside that invocation.
- `agent/session_lifecycle.go:491-527` marks state processing but does not reject a second external `ProcessInputKind` call while the first one is in progress.
- `agent/session_lifecycle.go:557-594` prepares the model request, dispatches it, and logs it in order for one round.
- `agent/session_model_call.go:52-126` snapshots `s.history` under `s.mu`, repairs/context-manages, expands history, and builds one `llm.Request`.
- `agent/session_model_call.go:306-371` calls the primary model and configured fallbacks sequentially using one API-log context.
- `agent/session_queue.go:173-203` queues typed input for the active turn, and `agent/session_lifecycle.go:361-373` drains queued input after interruption; this queue behavior helps UI callers avoid concurrent `ProcessInputKind`, but it is not a `Session` API reservation.
- `agent/session.go:49-71` documents that the turn loop is the primary mutable-state owner while external callers race it under locks, not that `ProcessInputKind` itself is single-flight.

Verdict:
- The current code is serialized within one `ProcessInputKind` invocation, and model fallback dispatch is sequential.
- The public `Session` API does not provide a history-base reservation spanning future continuation anchor selection through provider dispatch.
- Phase 4C must add a reservation or single-flight guard before Phase 4D-i/4D-ii/9 rely on an immutable history base for anchor selection.

## Logging Audit

Checkable line: Phase 5A should use terminal-attempt `final_attempt_count` on the terminal attempt record, not a separate group-summary record.

Evidence:
- `llm/apilog.go:20-24` currently carries only `SessionID` and `Round` in `APILogContext`.
- `llm/apilog.go:36-85` defines API and raw log records without attempt group, attempt index, history mode, or final attempt count fields.
- `llm/apilog.go:145-189` logs one API entry per wrapped complete/stream attempt and writes raw HTTP bodies from the same attempt.
- `llm/apilog.go:222-255` appends log records and never rewrites prior lines.
- `agent/transcript/transcript.go:60-78` defines transcript `api_call` records without attempt metadata.
- `agent/transcript/transcript.go:244-280` appends `api_call` records with monotonic transcript sequence numbers and never rewrites prior lines.
- `agent/session_model_call.go:375-410` writes one transcript `api_call` for the final `req`/`resp` currently visible to the session.

Chosen shape:
- Every future provider attempt record carries `attempt_group_id`, 1-based `attempt_index`, `attempt_count`, and `history_mode`.
- Non-terminal attempts may emit `attempt_count=0` and no `final_attempt_count`.
- The terminal attempt record carries `final_attempt_count=N`.
- Single-attempt groups use `attempt_index=1`, `attempt_count=1`, and `final_attempt_count=1`.
- This matches the current append-only transcript/API/raw log model and avoids a second group-summary record kind that would have to be correlated across three append-only logs.
```

- [ ] **Step 2: Verify proof line references still match current files**

Run:

```sh
for ref in \
  agent/session_lifecycle.go \
  agent/session_model_call.go \
  agent/session_queue.go \
  agent/session.go \
  llm/apilog.go \
  agent/transcript/transcript.go
do
  test -f "$ref" || exit 1
done
```

Expected: command exits 0.

- [ ] **Step 3: Commit proof artifact**

Run:

```sh
git status --short
git add docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0a.md
git commit -m "docs: record responses continuation phase 0a proof" -m "Record the Phase 0A proof artifact required by the continuation design. The artifact separates registry defaults, model-call serialization evidence with a reservation-required verdict, and the logging write-model decision that Phase 5A should implement terminal-attempt final_attempt_count."
```

## Task 4: Phase 0A Verification

**Files:**
- Read: `docs/superpowers/specs/2026-06-23-openai-responses-continuation-design.md`
- Read: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-0a-audits.md`

- [ ] **Step 1: Run focused tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestDefaultResponsesContinuationSupportRegistryDisabled|TestResponsesContinuationSupportForUnknownFamilyDisabled|TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory -count=1 -v
```

Expected: both commands PASS.

- [ ] **Step 2: Run adjacent regression tests from the minimal recovery slice**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestSession_ProcessInputRepairsOrphanedAssistantToolCallsBeforeModelRequest|TestResumeHistoryRepairsOrphanedAssistantToolCallsBeforeLaterUserInput' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestToResponsesInput_SanitizesMalformedHistoricalToolCallArguments|TestBuildChatCompletionsBody_SanitizesMalformedHistoricalToolCallArguments' -count=1 -v
```

Expected: both commands PASS.

- [ ] **Step 3: Check formatting and whitespace**

Run:

```sh
gofmt -w llm/responses_continuation.go llm/responses_continuation_test.go agent/session_openai_continuation_phase0a_test.go
git diff --check
```

Expected: `git diff --check` prints no errors.

- [ ] **Step 4: Confirm Phase 0A remains production-no-op**

Run:

```sh
rg -n 'DecideResponsesContinuation|DefaultResponsesContinuationSupportRegistry|ResponsesContinuationAuto|ResponsesContinuationOff' --glob '*.go'
```

Expected: matches are limited to `llm/responses_continuation.go`, `llm/responses_continuation_test.go`, and `agent/session_openai_continuation_phase0a_test.go`. No production session or adapter call site uses the new decision helper in Phase 0A.

- [ ] **Step 5: Commit verification-only plan checkbox updates if the plan was checked off during execution**

If the implementer updates checkbox state in this plan, run:

```sh
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-0a-audits.md
git commit -m "docs: update phase 0a execution checklist" -m "Record completed Phase 0A execution checklist state after focused tests, adjacent regression tests, gofmt, diff checks, and production no-op verification passed."
```

If the plan checkboxes remain unchecked, make no commit in this step.
