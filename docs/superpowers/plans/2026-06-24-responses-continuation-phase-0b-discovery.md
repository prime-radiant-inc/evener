# Responses Continuation Phase 0B Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add deterministic OpenAI Responses continuation discovery fixtures and an explicit opt-in live discovery gate for public OpenAI and Codex backend request shapes.

**Architecture:** Keep Phase 0B adapter-level and evidence-producing. Deterministic tests record the current wire shape and payload-size math without enabling runtime continuation; opt-in live tests probe provider acceptance and log findings only when the matching discovery env var is set. The proof artifact records deterministic facts immediately and treats missing or failed live discovery as a blocker before Phases 1A-11 become committed implementation work.

**Tech Stack:** Go tests in `llm/providers/openai`, local JSON body inspection through the existing OpenAI adapter, explicit `SERF_*_DISCOVERY_E2E=1` live gates, markdown proof artifact.

---

## File Structure

- Create `llm/providers/openai/responses_continuation_discovery_test.go`
  - Deterministic adapter-level request-shape matrix for public OpenAI and Codex backend.
  - Deterministic malformed-tool-call recovery payload-size probe.
- Create `llm/providers/openai/responses_continuation_discovery_e2e_test.go`
  - Explicit opt-in live discovery tests for public OpenAI and Codex backend.
  - Skips unless a discovery env var is set; provider credentials alone never run it.
- Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0b.md`
  - Records deterministic fixture results, live discovery commands, live status, `SystemPromptAsUser` inventory, rough eligible-hit-rate blockers, and the go/no-go state for continuing to Phases 1A-11.

## Task 1: Deterministic Request-Shape Matrix

**Files:**
- Create: `llm/providers/openai/responses_continuation_discovery_test.go`

- [ ] **Step 1: Write the request-shape matrix tests**

Create `llm/providers/openai/responses_continuation_discovery_test.go` with this initial content:

```go
package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestResponsesContinuationDiscovery_RequestShapeMatrix(t *testing.T) {
	storeTrue := true

	cases := []struct {
		name       string
		adapter    *Adapter
		req        llm.Request
		want       map[string]any
		wantAbsent []string
	}{
		{
			name:    "public default has store false and no provider state handles",
			adapter: &Adapter{},
			req: llm.Request{
				Model:    "gpt-5.2",
				Messages: []llm.Message{llm.User("hello")},
			},
			want: map[string]any{"store": false},
			wantAbsent: []string{
				"previous_response_id",
				"conversation",
			},
		},
		{
			name:    "public previous response only",
			adapter: &Adapter{},
			req: llm.Request{
				Model:              "gpt-5.2",
				Messages:           []llm.Message{llm.User("hello")},
				PreviousResponseID: " resp_public ",
			},
			want: map[string]any{
				"store":                false,
				"previous_response_id": "resp_public",
			},
			wantAbsent: []string{"conversation"},
		},
		{
			name:    "public conversation only",
			adapter: &Adapter{},
			req: llm.Request{
				Model:          "gpt-5.2",
				Messages:       []llm.Message{llm.User("hello")},
				ConversationID: " conv_public ",
			},
			want: map[string]any{
				"store":        false,
				"conversation": "conv_public",
			},
			wantAbsent: []string{"previous_response_id"},
		},
		{
			name:    "public previous response plus conversation plus explicit store true",
			adapter: &Adapter{},
			req: llm.Request{
				Model:              "gpt-5.2",
				Messages:           []llm.Message{llm.User("hello")},
				PreviousResponseID: " resp_public ",
				ConversationID:     " conv_public ",
				Store:              &storeTrue,
			},
			want: map[string]any{
				"store":                true,
				"previous_response_id": "resp_public",
				"conversation":         "conv_public",
			},
		},
		{
			name: "codex previous response only",
			adapter: &Adapter{
				ResponsesPath: "/backend-api/codex/responses",
			},
			req: llm.Request{
				Model:              "gpt-5.2",
				Messages:           []llm.Message{llm.User("hello")},
				PreviousResponseID: " resp_codex ",
			},
			want: map[string]any{
				"store":                false,
				"previous_response_id": "resp_codex",
				"stream":               true,
			},
			wantAbsent: []string{"conversation"},
		},
		{
			name: "codex conversation only",
			adapter: &Adapter{
				ResponsesPath: "/backend-api/codex/responses",
			},
			req: llm.Request{
				Model:          "gpt-5.2",
				Messages:       []llm.Message{llm.User("hello")},
				ConversationID: " conv_codex ",
			},
			want: map[string]any{
				"store":        false,
				"conversation": "conv_codex",
				"stream":       true,
			},
			wantAbsent: []string{"previous_response_id"},
		},
		{
			name: "codex previous response plus conversation plus explicit store true",
			adapter: &Adapter{
				ResponsesPath: "/backend-api/codex/responses",
			},
			req: llm.Request{
				Model:              "gpt-5.2",
				Messages:           []llm.Message{llm.User("hello")},
				PreviousResponseID: " resp_codex ",
				ConversationID:     " conv_codex ",
				Store:              &storeTrue,
			},
			want: map[string]any{
				"store":                true,
				"previous_response_id": "resp_codex",
				"conversation":         "conv_codex",
				"stream":               true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := tc.adapter.buildRequestBody(tc.req)
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			for key, want := range tc.want {
				if got := body[key]; got != want {
					t.Fatalf("%s = %#v, want %#v in body %#v", key, got, want, body)
				}
			}
			for _, key := range tc.wantAbsent {
				if _, ok := body[key]; ok {
					t.Fatalf("%s present in body %#v", key, body)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run request-shape tests and verify they pass or fail only on true current-wire mismatches**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestResponsesContinuationDiscovery_RequestShapeMatrix -count=1 -v
```

Expected: PASS. If it fails because the current adapter emits a different field, inspect `llm/providers/openai/responses.go` and update the expected matrix only when the observed wire shape is intentional and documented in the test name.

- [ ] **Step 3: Commit request-shape matrix**

Run:

```sh
git status --short
git add llm/providers/openai/responses_continuation_discovery_test.go
git commit -m "test(openai): record responses continuation request-shape matrix" -m "Add Phase 0B deterministic adapter fixtures for public OpenAI and Codex Responses request shapes. The matrix records previous_response_id, conversation, store, and Codex stream behavior without enabling session runtime continuation or relying on provider credentials."
```

## Task 2: Deterministic Payload-Size Probe

**Files:**
- Modify: `llm/providers/openai/responses_continuation_discovery_test.go`

- [ ] **Step 1: Add payload-size probe helpers and test**

Append this code to `llm/providers/openai/responses_continuation_discovery_test.go`:

```go
func TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe(t *testing.T) {
	storeTrue := true
	adapter := &Adapter{}
	malformedCall := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_bad",
				Name:      "my_strict_tool",
				Arguments: json.RawMessage(`{"value": broken`),
				Type:      "function",
			},
		}},
	}
	errorResult := llm.ToolResultNamed(
		"call_bad",
		"my_strict_tool",
		map[string]any{
			"is_error": true,
			"message":  "invalid tool arguments JSON",
		},
		true,
	)

	fullBody, err := adapter.buildRequestBody(llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{
			llm.System("stable system prompt"),
			llm.User(strings.Repeat("prior context marker ", 40)),
			malformedCall,
			errorResult,
			llm.User("recover now"),
		},
	})
	if err != nil {
		t.Fatalf("full buildRequestBody: %v", err)
	}
	deltaBody, err := adapter.buildRequestBody(llm.Request{
		Model:              "gpt-5.2",
		Messages:           []llm.Message{errorResult},
		PreviousResponseID: "resp_bad",
		Store:              &storeTrue,
	})
	if err != nil {
		t.Fatalf("delta buildRequestBody: %v", err)
	}

	fullInput := discoveryInputItems(t, fullBody)
	deltaInput := discoveryInputItems(t, deltaBody)
	if discoveryFindItem(fullInput, "function_call", "call_id", "call_bad") == nil {
		t.Fatalf("full-history probe missing historical function_call: %#v", fullInput)
	}
	if discoveryFindItem(deltaInput, "function_call", "call_id", "call_bad") != nil {
		t.Fatalf("delta probe must omit historical function_call: %#v", deltaInput)
	}
	if discoveryFindItem(deltaInput, "function_call_output", "call_id", "call_bad") == nil {
		t.Fatalf("delta probe missing tool output for call_bad: %#v", deltaInput)
	}
	if deltaBody["previous_response_id"] != "resp_bad" {
		t.Fatalf("delta previous_response_id = %#v", deltaBody["previous_response_id"])
	}

	result := discoveryPayloadSizeResult(t, fullBody, deltaBody)
	if result.GrossOmittedHistoricalItemBytes <= 0 {
		t.Fatalf("gross omitted historical item bytes = %d, want positive", result.GrossOmittedHistoricalItemBytes)
	}
	if result.AddedContinuationOverheadBytes <= 0 {
		t.Fatalf("added continuation overhead bytes = %d, want positive", result.AddedContinuationOverheadBytes)
	}
	if result.NetBodySizeDeltaBytes <= 0 {
		t.Fatalf("net body-size delta = %d, want positive; result=%+v", result.NetBodySizeDeltaBytes, result)
	}
	t.Logf("phase0b payload_size_result=%+v", result)
}

type discoveryPayloadSize struct {
	FullHistoryBytes                 int
	ResponsesDeltaBytes              int
	GrossOmittedHistoricalItemBytes   int
	AddedContinuationOverheadBytes    int
	NetBodySizeDeltaBytes             int
}

func discoveryPayloadSizeResult(t *testing.T, fullBody, deltaBody map[string]any) discoveryPayloadSize {
	t.Helper()
	fullBytes := len(discoveryJSON(t, fullBody))
	deltaBytes := len(discoveryJSON(t, deltaBody))
	omitted := 0
	for _, item := range discoveryInputItems(t, fullBody) {
		if m, ok := item.(map[string]any); ok && (m["type"] == "function_call" || discoveryItemContainsText(m, "prior context marker")) {
			omitted += len(discoveryJSON(t, m))
		}
	}
	added := len(discoveryJSON(t, map[string]any{
		"previous_response_id": deltaBody["previous_response_id"],
		"store":                deltaBody["store"],
	}))
	return discoveryPayloadSize{
		FullHistoryBytes:               fullBytes,
		ResponsesDeltaBytes:            deltaBytes,
		GrossOmittedHistoricalItemBytes: omitted,
		AddedContinuationOverheadBytes:  added,
		NetBodySizeDeltaBytes:           fullBytes - deltaBytes,
	}
}

func discoveryInputItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	input, ok := body["input"].([]map[string]any)
	if ok {
		items := make([]any, len(input))
		for i := range input {
			items[i] = input[i]
		}
		return items
	}
	anyInput, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want slice", body["input"])
	}
	return anyInput
}

func discoveryFindItem(items []any, itemType, key, value string) map[string]any {
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == itemType && item[key] == value {
			return item
		}
	}
	return nil
}

func discoveryItemContainsText(item map[string]any, needle string) bool {
	content, ok := item["content"].([]map[string]any)
	if ok {
		for _, part := range content {
			if text, _ := part["text"].(string); strings.Contains(text, needle) {
				return true
			}
		}
	}
	contentAny, ok := item["content"].([]any)
	if !ok {
		return false
	}
	for _, partAny := range contentAny {
		part, ok := partAny.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := part["text"].(string); strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func discoveryJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal discovery JSON: %v", err)
	}
	return data
}
```

- [ ] **Step 2: Run the payload-size probe**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe -count=1 -v
```

Expected: PASS and a `phase0b payload_size_result=...` log line with positive gross omitted bytes, positive continuation overhead, and positive net body-size delta.

- [ ] **Step 3: Commit payload-size probe**

Run:

```sh
git status --short
git add llm/providers/openai/responses_continuation_discovery_test.go
git commit -m "test(openai): record responses continuation payload-size probe" -m "Add the deterministic Phase 0B malformed-tool-call recovery payload-size fixture. The probe compares full-history replay against a delta-shaped request, proves historical function_call items are omitted from the delta body, and reports gross omitted bytes, added continuation overhead, and net body-size delta."
```

## Task 3: Opt-In Live Discovery Harness

**Files:**
- Create: `llm/providers/openai/responses_continuation_discovery_e2e_test.go`

- [ ] **Step 1: Add live discovery tests guarded by explicit env vars**

Create `llm/providers/openai/responses_continuation_discovery_e2e_test.go` with this content:

```go
package openai

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/llm"
)

func TestAdapter_E2E_PublicResponsesContinuationDiscovery(t *testing.T) {
	if os.Getenv("SERF_OPENAI_RESPONSES_DISCOVERY_E2E") != "1" {
		t.Skip("set SERF_OPENAI_RESPONSES_DISCOVERY_E2E=1 to run live public OpenAI Responses continuation discovery")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is required for public OpenAI discovery")
	}
	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_RESPONSES_DISCOVERY_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}
	a := &Adapter{APIKey: apiKey}
	runResponsesContinuationDiscovery(t, a, model, "public_openai", true)
}

func TestAdapter_E2E_CodexResponsesContinuationDiscovery(t *testing.T) {
	if os.Getenv("SERF_OPENAI_CODEX_DISCOVERY_E2E") != "1" {
		t.Skip("set SERF_OPENAI_CODEX_DISCOVERY_E2E=1 to run live Codex Responses continuation discovery")
	}
	if testing.Short() {
		t.Skip("skipping live Codex discovery in short mode")
	}
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if !a.usesCodexBackend() {
		t.Skip("OpenAI env did not resolve to stored OAuth/Codex backend credentials")
	}
	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_CODEX_DISCOVERY_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}
	runResponsesContinuationDiscovery(t, a, model, "codex_backend", false)
}

func runResponsesContinuationDiscovery(t *testing.T, a *Adapter, model, endpointFamily string, requestStore bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	id := ulid.Make().String()
	store := requestStore
	anchorReq := llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.User("Reply exactly: serf continuation discovery anchor")},
		Store:    &store,
		Metadata: map[string]string{
			"serf_discovery_id": id,
		},
	}
	anchor, err := a.Complete(ctx, anchorReq)
	if err != nil {
		t.Fatalf("%s anchor request failed: %v", endpointFamily, err)
	}
	if strings.TrimSpace(anchor.ID) == "" {
		t.Fatalf("%s anchor response id is empty; raw=%#v", endpointFamily, anchor.Raw)
	}

	delta, err := a.Complete(ctx, llm.Request{
		Model:              model,
		Messages:           []llm.Message{llm.User("Reply exactly: serf continuation discovery delta")},
		PreviousResponseID: anchor.ID,
		Store:              &store,
	})
	if err != nil {
		t.Fatalf("%s valid previous_response_id request failed: %v", endpointFamily, err)
	}
	if strings.TrimSpace(delta.Text()) == "" {
		t.Fatalf("%s delta response was empty", endpointFamily)
	}

	branch, err := a.Complete(ctx, llm.Request{
		Model:              model,
		Messages:           []llm.Message{llm.User("Reply exactly: serf continuation discovery branch")},
		PreviousResponseID: anchor.ID,
		Store:              &store,
	})
	if err != nil {
		t.Fatalf("%s second branch from same previous_response_id failed: %v", endpointFamily, err)
	}
	if strings.TrimSpace(branch.Text()) == "" {
		t.Fatalf("%s branch response was empty", endpointFamily)
	}

	_, invalidErr := a.Complete(ctx, llm.Request{
		Model:              model,
		Messages:           []llm.Message{llm.User("This invalid anchor request should fail clearly.")},
		PreviousResponseID: "resp_serf_invalid_" + id,
		Store:              &store,
	})
	if invalidErr == nil {
		t.Fatalf("%s invalid previous_response_id was accepted; cannot enable continuation without silent-drop design", endpointFamily)
	}

	t.Logf("%s discovery: anchor_id=%q delta_text=%q branch_text=%q invalid_anchor_error=%q",
		endpointFamily,
		anchor.ID,
		strings.TrimSpace(delta.Text()),
		strings.TrimSpace(branch.Text()),
		fmt.Sprint(invalidErr),
	)
}
```

- [ ] **Step 2: Verify default tests skip live discovery**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_PublicResponsesContinuationDiscovery|TestAdapter_E2E_CodexResponsesContinuationDiscovery' -count=1 -v
```

Expected: PASS with both tests skipped unless the explicit discovery env vars are set.

- [ ] **Step 3: Commit live discovery harness**

Run:

```sh
git status --short
git add llm/providers/openai/responses_continuation_discovery_e2e_test.go
git commit -m "test(openai): add opt-in responses continuation discovery e2e" -m "Add explicit opt-in Phase 0B live discovery probes for public OpenAI and Codex Responses continuation. Provider credentials alone do not run the tests; each endpoint family requires its own SERF_OPENAI_*_DISCOVERY_E2E gate."
```

## Task 4: Phase 0B Proof Artifact

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0b.md`

- [ ] **Step 1: Run the payload probe and capture exact values**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe -count=1 -v | tee /tmp/serf-phase0b-payload-size.log
```

Expected: PASS and one `phase0b payload_size_result={...}` log line. Read the five integer fields from that log line before creating the proof artifact.

- [ ] **Step 2: Create proof artifact with deterministic findings and live gate status**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0b.md` with this content. In the `Payload-size result` block, write the exact integer values from `/tmp/serf-phase0b-payload-size.log`; do not write symbolic values.

```markdown
# Responses Continuation Phase 0B Discovery Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 0B-discovery

## Deterministic Request-Shape Matrix

Checkable line: public OpenAI and Codex backend adapter fixtures cover `previous_response_id` only, `conversation` only, and both handles present.

Evidence:
- `llm/providers/openai/responses_continuation_discovery_test.go:TestResponsesContinuationDiscovery_RequestShapeMatrix`

Current deterministic matrix:
- Public OpenAI default emits `store:false` and no provider-state handles.
- Public OpenAI serializes trimmed `previous_response_id`.
- Public OpenAI serializes trimmed `conversation`.
- Public OpenAI serializes both handles together and preserves explicit `store:true`.
- Codex backend serializes trimmed `previous_response_id`, trimmed `conversation`, and both handles together.
- Codex backend preserves explicit `store:true` in deterministic adapter body construction; live discovery must decide whether that shape is accepted by the backend.
- Codex backend sets `stream:true`.

## Deterministic Payload-Size Probe

Checkable line: scripted malformed-tool-call recovery delta body omits the historical `function_call`, includes the linked `function_call_output`, includes `previous_response_id`, and has positive net body-size reduction in the deterministic fixture.

Evidence:
- `llm/providers/openai/responses_continuation_discovery_test.go:TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe`

Payload-size result:
- `full_history_bytes`: exact integer from `FullHistoryBytes`
- `responses_delta_bytes`: exact integer from `ResponsesDeltaBytes`
- `gross_omitted_historical_item_bytes`: exact integer from `GrossOmittedHistoricalItemBytes`
- `added_continuation_overhead_bytes`: exact integer from `AddedContinuationOverheadBytes`
- `net_body_size_delta_bytes`: exact integer from `NetBodySizeDeltaBytes`

## Live Discovery Status

Checkable line: live discovery is explicit opt-in and blocks treating Phases 1A-11 as committed implementation work for a target endpoint family until the target endpoint family has accepted valid anchors, rejected invalid anchors clearly, resolved co-present `previous_response_id` plus `conversation`, and shown net request-payload reduction on the scripted probe.

Commands:
- Public OpenAI: `SERF_OPENAI_RESPONSES_DISCOVERY_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestAdapter_E2E_PublicResponsesContinuationDiscovery -count=1 -v`
- Codex backend: `SERF_OPENAI_CODEX_DISCOVERY_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestAdapter_E2E_CodexResponsesContinuationDiscovery -count=1 -v`

Observed status:
- Public OpenAI: not run in default test suite.
- Codex backend: not run in default test suite.

Go/no-go:
- Public OpenAI remains blocked for runtime enablement until its live discovery command is run and this artifact records accepted valid anchor behavior plus clear invalid-anchor behavior.
- Codex backend remains blocked for runtime enablement until its live discovery command is run and this artifact records accepted valid anchor behavior plus clear invalid-anchor behavior.

## SystemPromptAsUser Inventory

Checkable line: `SessionConfig.SystemPromptAsUser` is a runtime/session setting, not a property set by `NewOpenAIProfile`; continuation must remain full-history for any launch path that sets it true.

Evidence:
- `agent/session_model_call.go` branches on `s.cfg.SystemPromptAsUser` while building model messages.
- `agent/provider/profile.go` profiles do not carry a `SystemPromptAsUser` field.

Current inventory:
- Static code inventory found no OpenAI Responses profile constructor that forces `SystemPromptAsUser=true`.
- Real launch/profile usage for intended V1-public traffic is not measured by default tests. Runtime enablement remains blocked until the intended rollout path records whether `SystemPromptAsUser=true` is prevalent.

## Rough Eligible-Hit-Rate Blockers

Checkable line: broad runtime enablement is not committed until rollout traffic has an eligible-hit-rate expectation that clears the future Phase 12A floor, or Jesse explicitly accepts a parity-first narrow rollout.

Known blockers from the design:
- `SystemPromptAsUser=true`
- date-boundary prompt changes from `Today`
- unsupported item kinds such as media, provider-hosted files, reasoning items, or web-search inputs
- model changes
- storage-scope mismatches
- missing or expired anchor metadata

Current Phase 0B verdict:
- Deterministic adapter fixtures can land.
- Runtime continuation remains disabled.
- Phases 1A-11 must not be treated as a committed implementation path for an endpoint family until that endpoint family's live discovery findings are recorded here.
```

- [ ] **Step 3: Verify proof artifact records integers and has no template markers**

Run:

```sh
rg -n 'PENDING_VALUE|REPLACE_ME|TBA_VALUE' docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0b.md
```

Expected: no output.

- [ ] **Step 4: Commit proof artifact**

Run:

```sh
git status --short
git add docs/superpowers/proofs/2026-06-24-responses-continuation-phase-0b.md
git commit -m "docs: record responses continuation phase 0b discovery" -m "Record Phase 0B deterministic request-shape and payload-size discovery, explicit live discovery commands, SystemPromptAsUser inventory, eligible-hit-rate blockers, and the go/no-go state that keeps runtime continuation disabled until live endpoint findings are recorded."
```

## Task 5: Phase 0B Verification

**Files:**
- Read: `docs/superpowers/specs/2026-06-23-openai-responses-continuation-design.md`
- Read: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-0b-discovery.md`

- [ ] **Step 1: Run deterministic discovery tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestResponsesContinuationDiscovery_RequestShapeMatrix|TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe' -count=1 -v
```

Expected: PASS.

- [ ] **Step 2: Verify live discovery tests are skipped by default**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_PublicResponsesContinuationDiscovery|TestAdapter_E2E_CodexResponsesContinuationDiscovery' -count=1 -v
```

Expected: PASS with skip messages unless explicit discovery env vars are set.

- [ ] **Step 3: Verify continuation remains runtime-disabled**

Run:

```sh
rg -n 'DefaultResponsesContinuationSupportRegistry|DecideResponsesContinuation|ResponsesContinuationAuto|ResponsesContinuationOff' --glob '*.go'
```

Expected: references are limited to `llm/responses_continuation.go`, tests, and discovery fixtures. No production session or adapter runtime path uses the helper.

- [ ] **Step 4: Format and whitespace checks**

Run:

```sh
gofmt -w llm/providers/openai/responses_continuation_discovery_test.go llm/providers/openai/responses_continuation_discovery_e2e_test.go
git diff --check
```

Expected: `git diff --check` prints no errors.
