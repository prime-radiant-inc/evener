# Liberal Output Caps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Always request the model's maximum output tokens (instead of a hardcoded 4096), and report length-truncated tool calls as truncation instead of a JSON syntax error.

**Architecture:** A catalog-lookup method on `llm.ModelCatalog` is the shared resolution primitive. The anthropic adapter uses it to replace its hardcoded 4096 default (fallback 32000 when the catalog is silent). The agent layer independently fills `Request.MaxTokens` from instance config then catalog (defense in depth). Separately, the round's finish reason is threaded into tool prevalidation so a `length`-stopped turn with unparseable tool args produces a truncation-specific error and never attempts JSON repair.

**Tech Stack:** Go. Repo is a go workspace (`go.work`); run builds/tests from the repo root `/Users/jesse/prime-radiant/toil-suite/serf`.

**Spec:** `docs/superpowers/specs/2026-08-05-liberal-output-caps-design.md`

## Global Constraints

- Cap precedence: explicit request `MaxTokens` → instance config `max_output_tokens` → embedded catalog `MaxOutputTokens` (with junk-data guard) → 32000 fallback (anthropic adapter only; the agent layer leaves `MaxTokens` nil when nothing resolves).
- Junk-data guard: a catalog `MaxOutputTokens` that equals or exceeds the model's `ContextWindow` is ignored (input and output share the window; such an entry is bad data — see `llm/providers/openaicompat/compat.go` `fillFromCatalog` for the precedent).
- Never repair length-truncated tool-call JSON. Truncated arguments must fail with the truncation message, not execute.
- TDD for every task: failing test first, then implementation.
- Match surrounding code style; comments explain WHAT/WHY, never history.

---

### Task 1: Catalog output-cap lookup

**Files:**
- Modify: `llm/model_catalog.go` (add method after `LookupModelInfo`, which ends near line 140)
- Test: `llm/model_catalog_test.go` (append; package `llm`, internal)

**Interfaces:**
- Consumes: `(*ModelCatalog).LookupModelInfo(modelID string) *ModelInfo` (existing), `ModelInfo.MaxOutputTokens *int`, `ModelInfo.ContextWindow int`.
- Produces: `func (c *ModelCatalog) MaxOutputTokensFor(model string) int` — returns the model's output cap, or 0 when the catalog is nil, the model is unknown, the entry has no cap, or the cap fails the junk-data guard. Tasks 2 and 3 call this exact name.

- [ ] **Step 1: Write the failing test**

Append to `llm/model_catalog_test.go` (the file already uses `parseLiteLLMCatalog` to build fixture catalogs — see `TestApplyOverrides_MaterializesSerfOnlyModel` around line 839 for the pattern):

```go
func TestMaxOutputTokensFor(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{
		"capped":   {"litellm_provider": "x", "max_input_tokens": 200000, "max_output_tokens": 64000},
		"junk-cap": {"litellm_provider": "x", "max_input_tokens": 8192, "max_output_tokens": 8192},
		"no-cap":   {"litellm_provider": "x", "max_input_tokens": 200000}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		model string
		want  int
	}{
		{"capped", 64000},
		{"junk-cap", 0}, // cap >= context window is junk data
		{"no-cap", 0},
		{"unknown-model", 0},
	}
	for _, tc := range cases {
		if got := cat.MaxOutputTokensFor(tc.model); got != tc.want {
			t.Errorf("MaxOutputTokensFor(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
	var nilCat *ModelCatalog
	if got := nilCat.MaxOutputTokensFor("capped"); got != 0 {
		t.Errorf("nil catalog: got %d, want 0", got)
	}
}
```

Before finalizing the test, check how `parseLiteLLMCatalog` maps `max_input_tokens`/`max_output_tokens` into `ContextWindow`/`MaxOutputTokens` (see `llm/model_catalog.go` around lines 270–285) and adjust the fixture keys so `junk-cap` genuinely has cap ≥ context window and `capped` genuinely has cap < context window.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./llm/ -run TestMaxOutputTokensFor -v`
Expected: FAIL — `cat.MaxOutputTokensFor undefined`.

- [ ] **Step 3: Implement the method**

In `llm/model_catalog.go`, after `LookupModelInfo`:

```go
// MaxOutputTokensFor returns the model's output-token cap from the catalog,
// or 0 when unknown. A claimed cap that equals or exceeds the context window
// is junk data — input and output share the window, so the cap can't coexist
// with any prompt — and reports 0 (mirrors openaicompat's fillFromCatalog
// guard). Nil-receiver safe so callers can use EmbeddedModelCatalog()
// directly without a nil check.
func (c *ModelCatalog) MaxOutputTokensFor(model string) int {
	if c == nil {
		return 0
	}
	mi := c.LookupModelInfo(model)
	if mi == nil || mi.MaxOutputTokens == nil || *mi.MaxOutputTokens <= 0 {
		return 0
	}
	if mi.ContextWindow > 0 && *mi.MaxOutputTokens >= mi.ContextWindow {
		return 0
	}
	return *mi.MaxOutputTokens
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./llm/ -run TestMaxOutputTokensFor -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add llm/model_catalog.go llm/model_catalog_test.go
git commit -m "feat(llm): catalog lookup for a model's max output tokens"
```

---

### Task 2: Anthropic adapter — catalog-driven max_tokens default

**Files:**
- Modify: `llm/providers/anthropic/request.go:28-42` (default resolution) and the thinking-budget reconciliation around lines 188-203 (clamp)
- Test: `llm/providers/anthropic/request_maxtokens_test.go` (create; package `anthropic`, internal)

**Interfaces:**
- Consumes: `llm.EmbeddedModelCatalog()` (may return nil — safe, the method is nil-receiver safe) and `(*ModelCatalog).MaxOutputTokensFor(model string) int` from Task 1.
- Produces: no new exported API. Behavior change only: `buildRequestBody` defaults `max_tokens` from the catalog instead of 4096.

- [ ] **Step 1: Write the failing test**

Create `llm/providers/anthropic/request_maxtokens_test.go`. Look at `adapter_test.go` first for how existing tests call `buildRequestBody` (adapter construction, message fixtures) and match that style. The behaviors to pin:

```go
package anthropic

import (
	"testing"

	"primeradiant.com/serf/llm"
)

func buildBodyForModel(t *testing.T, model string, maxTokens *int) map[string]any {
	t.Helper()
	a := &Adapter{}
	req := llm.Request{
		Model:     model,
		Messages:  []llm.Message{llm.User("hi")},
		MaxTokens: maxTokens,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	return body
}

// Catalog-known model: the default cap is the catalog's, not 4096.
func TestBuildRequest_MaxTokensDefaultsFromCatalog(t *testing.T) {
	const model = "claude-sonnet-4-5"
	want := llm.EmbeddedModelCatalog().MaxOutputTokensFor(model)
	if want <= 0 {
		t.Fatalf("embedded catalog has no output cap for %s; pick a model it covers", model)
	}
	body := buildBodyForModel(t, model, nil)
	if got := body["max_tokens"].(int); got != want {
		t.Errorf("max_tokens = %d, want catalog cap %d", got, want)
	}
}

// Catalog-unknown model: liberal 32000 fallback, not 4096.
func TestBuildRequest_MaxTokensFallback32000(t *testing.T) {
	body := buildBodyForModel(t, "no-such-model-xyz", nil)
	if got := body["max_tokens"].(int); got != 32000 {
		t.Errorf("max_tokens = %d, want 32000", got)
	}
}

// Explicit request cap always wins.
func TestBuildRequest_ExplicitMaxTokensWins(t *testing.T) {
	mt := 512
	body := buildBodyForModel(t, "claude-sonnet-4-5", &mt)
	if got := body["max_tokens"].(int); got != 512 {
		t.Errorf("max_tokens = %d, want 512", got)
	}
}
```

Also add a test for the thinking-budget clamp (see Step 3 for the behavior). Read the reconciliation block at `request.go:188-203` first, then write the test so it exercises: catalog-known model + thinking budget such that `budget + maxTokens` exceeds the catalog cap → final `max_tokens` equals the catalog cap (not the sum). How thinking gets enabled in a request is visible in `adapter_test.go` / `claude5_test.go` (reasoning effort → thinking budget); follow that pattern. If enabling thinking in a unit test proves impractical through the public request surface, test the clamp through whatever seam the reconciliation uses — but the assertion must be on the final `body["max_tokens"]`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./llm/providers/anthropic/ -run TestBuildRequest_MaxTokens -v`
Expected: FAIL — defaults land at 4096, not catalog/32000.

- [ ] **Step 3: Implement**

In `buildRequestBody`, the current code is:

```go
	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	// Strip the [1m] suffix — it's a client-side convention, not an API model ID.
	apiModel := strings.TrimSuffix(req.Model, "[1m]")
```

Replace with (note `apiModel` must move above the resolution so the catalog sees the real model ID):

```go
	// Strip the [1m] suffix — it's a client-side convention, not an API model ID.
	apiModel := strings.TrimSuffix(req.Model, "[1m]")

	// Output cap: explicit request value, else the model's real maximum from
	// the catalog, else a liberal fallback. An arbitrary small default silently
	// truncates large tool calls mid-stream (stop_reason max_tokens), which
	// surfaces downstream as unparseable tool JSON.
	catalogMax := llm.EmbeddedModelCatalog().MaxOutputTokensFor(apiModel)
	maxTokens := catalogMax
	if maxTokens == 0 {
		maxTokens = fallbackMaxTokens
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
```

Add the package constant near the top of `request.go`:

```go
// fallbackMaxTokens is the output cap requested for models the catalog does
// not cover. Liberal on purpose: a model that can't honor it fails loudly
// with a 400 (a catalog gap to fix) instead of silently truncating output.
const fallbackMaxTokens = 32000
```

Then the thinking-budget reconciliation (currently `body["max_tokens"] = thinkingBudget + out` when `out <= thinkingBudget`): after computing the sum, clamp to `catalogMax` when `catalogMax` is known and the sum exceeds it and `catalogMax` still strictly exceeds the budget (the API requires `max_tokens > thinking.budget_tokens`):

```go
		if out <= thinkingBudget {
			raised := thinkingBudget + out
			// With max_tokens now defaulting to the model's real maximum,
			// budget + max can exceed the model ceiling and 400. Clamp to the
			// catalog cap when that still satisfies max_tokens > budget.
			if catalogMax > thinkingBudget && raised > catalogMax {
				raised = catalogMax
			}
			body["max_tokens"] = raised
		}
```

(`catalogMax` is in scope from the resolution above; keep the existing surrounding comments intact.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./llm/providers/anthropic/ -run TestBuildRequest -v`
Expected: PASS, including pre-existing `TestBuildRequest*` tests. If a pre-existing test asserted the 4096 default, update its expectation to the new resolution — that assertion described the bug.

- [ ] **Step 5: Run the whole package**

Run: `go test ./llm/providers/anthropic/`
Expected: PASS (fuzz seed corpora run in normal test mode; if a fuzz-derived test pinned 4096, fix its expectation the same way).

- [ ] **Step 6: Commit**

```bash
git add llm/providers/anthropic/request.go llm/providers/anthropic/request_maxtokens_test.go llm/providers/anthropic/adapter_test.go
git commit -m "fix(llm/anthropic): default max_tokens to the model's real maximum, not 4096"
```

(Only add `adapter_test.go` if you actually changed it.)

---

### Task 3: Agent layer — fill MaxTokens from profile

**Files:**
- Modify: `agent/provider/profile.go` (new accessor near `ContextWindowSize`, line ~306)
- Modify: `agent/session_model_call.go` (in `prepareModelRequest`'s request construction, line ~704: fill `req.MaxTokens` after the `llm.Request{...}` literal, next to the `ProviderOptions` fill)
- Test: `agent/provider/profile_maxoutput_test.go` (create; package `provider`, internal — unexported fields are constructible here, matching `profile_overrides_test.go`)

**Interfaces:**
- Consumes: `Profile.instModels map[string]providercfg.ModelConfig` (existing field), `providercfg.ModelConfig.MaxOutputTokens int` (existing), `llm.EmbeddedModelCatalog().MaxOutputTokensFor` from Task 1.
- Produces: `func (p *Profile) MaxOutputTokens() int` — instance config cap if configured, else catalog cap, else 0. The agent fill in `prepareModelRequest` relies on it.

- [ ] **Step 1: Write the failing test**

Create `agent/provider/profile_maxoutput_test.go`:

```go
package provider

import (
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// Instance config wins over the catalog; catalog covers unconfigured models;
// unknown models report 0 so the adapter's own default governs.
func TestProfileMaxOutputTokens(t *testing.T) {
	catalogModel := "claude-sonnet-4-5"
	catalogCap := llm.EmbeddedModelCatalog().MaxOutputTokensFor(catalogModel)
	if catalogCap <= 0 {
		t.Fatalf("embedded catalog has no output cap for %s; pick a model it covers", catalogModel)
	}

	cases := []struct {
		name string
		p    *Profile
		want int
	}{
		{"instance config wins", &Profile{model: catalogModel, instModels: map[string]providercfg.ModelConfig{
			catalogModel: {MaxOutputTokens: 9000},
		}}, 9000},
		{"catalog fallback", &Profile{model: catalogModel}, catalogCap},
		{"unknown model", &Profile{model: "no-such-model-xyz"}, 0},
	}
	for _, tc := range cases {
		if got := tc.p.MaxOutputTokens(); got != tc.want {
			t.Errorf("%s: MaxOutputTokens() = %d, want %d", tc.name, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/provider/ -run TestProfileMaxOutputTokens -v`
Expected: FAIL — `p.MaxOutputTokens undefined`.

- [ ] **Step 3: Implement the accessor**

In `agent/provider/profile.go`, next to `ContextWindowSize` (line ~306):

```go
// MaxOutputTokens is the model's output-token cap: the instance's
// providers.toml max_output_tokens when configured, else the embedded
// catalog's, else 0 (unknown — the provider adapter's own default governs).
func (p *Profile) MaxOutputTokens() int {
	if mc, ok := p.instModels[p.model]; ok && mc.MaxOutputTokens > 0 {
		return mc.MaxOutputTokens
	}
	return llm.EmbeddedModelCatalog().MaxOutputTokensFor(p.model)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/provider/ -run TestProfileMaxOutputTokens -v`
Expected: PASS

- [ ] **Step 5: Wire the agent fill**

In `agent/session_model_call.go`, `prepareModelRequest` builds the request at line ~704:

```go
	if opts := profile.ProviderOptions(); opts != nil {
		req.ProviderOptions = opts
	}
```

Immediately after that block, add:

```go
	// Request the model's full output budget. Adapters default liberally too
	// (defense in depth), but filling it here makes the cap uniform across
	// providers and immune to any one adapter's default.
	if mt := profile.MaxOutputTokens(); mt > 0 {
		req.MaxTokens = &mt
	}
```

- [ ] **Step 6: Build and run the agent package tests**

Run: `go build ./... && go test ./agent/provider/ ./agent/`
Expected: PASS. If an agent test snapshots outgoing requests and now sees `MaxTokens` set, update the expectation — the new field is the intended behavior.

- [ ] **Step 7: Commit**

```bash
git add agent/provider/profile.go agent/provider/profile_maxoutput_test.go agent/session_model_call.go
git commit -m "feat(agent): fill request MaxTokens from instance config or catalog"
```

---

### Task 4: Truncation-aware tool prevalidation

**Files:**
- Modify: `agent/internal/tool/repair/explain.go` (new message renderer)
- Modify: `agent/session_tool_repair.go` (`prepareToolCall` gains a finish-reason parameter)
- Modify: `agent/session_tools.go:307-318` (`execTool` gains the parameter, passes it through)
- Modify: `agent/session_tool_round.go:130` (`execToolBatch` gains the parameter; four internal `s.execTool(...)` call sites forward it)
- Modify: `agent/session_lifecycle.go:1151` (pass `resp.Finish.Reason` into `execToolBatch`)
- Modify existing callers/tests: `agent/session_tool_repair_test.go` (four call sites), `agent/session_tools_aux_exact_fuzz_test.go:101`, plus any other `execTool`/`execToolBatch` caller the compiler flags — the compiler is the completeness net, run `go build ./...`
- Test: `agent/internal/tool/repair/explain_test.go` (append), `agent/session_tool_repair_test.go` (append)

**Interfaces:**
- Consumes: `llm.FinishReasonLength` (existing constant, `llm/types.go:372`); `resp.Finish.Reason` in scope at `session_lifecycle.go:1151`.
- Produces: `func ExplainTruncatedCall(toolName string) string` in package `repair`; new signatures `prepareToolCall(call llm.ToolCallData, t *tool.RegisteredTool, visibleNames []string, requestedVisible, finishReason string) prepareResult`, `execTool(ctx context.Context, call llm.ToolCallData, finishReason string) tool.ExecResult`, `execToolBatch(ctx context.Context, calls []llm.ToolCallData, profile *provider.Profile, finishReason string) ([]tool.ExecResult, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `agent/internal/tool/repair/explain_test.go`:

```go
func TestExplainTruncatedCall(t *testing.T) {
	msg := ExplainTruncatedCall("write_file")
	for _, want := range []string{"write_file", "truncated", "output-token limit", "NOT executed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}
```

Append to `agent/session_tool_repair_test.go`:

```go
// A length-stopped turn with unparseable args is truncation, not a JSON
// syntax problem: the error must say so, and no repair may run (a "healed"
// truncated write would silently write a truncated file).
func TestPrepareToolCall_TruncatedByLength(t *testing.T) {
	et := editTool(t)
	truncated := json.RawMessage(`{"file_path":"/x","old_string":"a","new_string":"unterminat`)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file", Arguments: truncated},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonLength)
	if res.PrevalErr == "" || !strings.Contains(res.PrevalErr, "truncated") {
		t.Fatalf("want truncation error, got: %q", res.PrevalErr)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("no repair may run on truncated args, got changes: %v", res.Changes)
	}
}

// Valid JSON on a length-stopped turn executes normally — the truncation may
// have landed after this tool call closed.
func TestPrepareToolCall_LengthStopWithValidArgs(t *testing.T) {
	et := editTool(t)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"/x","old_string":"a","new_string":"b"}`)},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonLength)
	if res.PrevalErr != "" {
		t.Fatalf("valid args must execute: %q", res.PrevalErr)
	}
}

// A non-length stop keeps the existing invalid-JSON coaching path.
func TestPrepareToolCall_BrokenJSONNonLengthStop(t *testing.T) {
	et := editTool(t)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path": nope}`)},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonStop)
	if res.PrevalErr == "" || !strings.Contains(res.PrevalErr, "not valid JSON") {
		t.Fatalf("want invalid-JSON error, got: %q", res.PrevalErr)
	}
}
```

Add `"strings"` to the test file's imports. The existing four `prepareToolCall` call sites in this file gain a trailing `""` argument (they model non-length stops); same for `session_tools_aux_exact_fuzz_test.go:101`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/internal/tool/repair/ -run TestExplainTruncatedCall -v; go test ./agent/ -run TestPrepareToolCall -v`
Expected: FAIL — `ExplainTruncatedCall` undefined; wrong argument count for `prepareToolCall`.

- [ ] **Step 3: Implement the message renderer**

In `agent/internal/tool/repair/explain.go`, after `ExplainJSONError`:

```go
// ExplainTruncatedCall renders the prevalidation error for a tool call whose
// argument stream was cut off because the response hit the output-token
// limit. Distinct from ExplainJSONError on purpose: the JSON is incomplete,
// not malformed, and coaching the model about syntax sends it debugging a
// problem it doesn't have.
func ExplainTruncatedCall(toolName string) string {
	return toolName + ": tool call truncated — the response hit the output-token limit " +
		"before the arguments finished streaming. The call was NOT executed and the lost " +
		"content cannot be recovered. Re-issue the work in smaller pieces (e.g. write the " +
		"file in sections across multiple calls)."
}
```

- [ ] **Step 4: Thread the finish reason**

`agent/session_tool_repair.go` — new signature and the truncation branch (replacing the current unmarshal-failure block):

```go
func prepareToolCall(call llm.ToolCallData, t *tool.RegisteredTool, visibleNames []string, requestedVisible, finishReason string) prepareResult {
```

```go
	args := map[string]any{}
	if len(res.Call.Arguments) > 0 { // raw len, mirroring ExecuteCall (no TrimSpace)
		if err := json.Unmarshal(res.Call.Arguments, &args); err != nil {
			// A length-stopped turn cut the argument stream mid-JSON. Never
			// repair that: closing the open string would execute a silently
			// truncated call (e.g. write half a file). Fail with the real
			// diagnosis instead.
			if finishReason == llm.FinishReasonLength {
				res.PrevalErr = repair.ExplainTruncatedCall(requestedVisible)
				return res
			}
			repaired, c := repair.RepairJSON(res.Call.Arguments)
			res.Changes = append(res.Changes, c...)
			args = map[string]any{}
			if err2 := json.Unmarshal(repaired, &args); err2 != nil {
				// Show the model's own bytes and the original parse error
				// (its offset refers to them, not the repaired form).
				res.PrevalErr = repair.ExplainJSONError(requestedVisible, t.Definition.Parameters, err, res.Call.Arguments)
				return res
			}
		}
	}
```

`agent/session_tools.go` — `execTool` signature gains `finishReason string`; pass it to `prepareToolCall`:

```go
func (s *Session) execTool(ctx context.Context, call llm.ToolCallData, finishReason string) tool.ExecResult {
```
```go
	prep := prepareToolCall(call, s.reg.Get(call.Name), visibleNames, requestedVisible, finishReason)
```

`agent/session_tool_round.go` — `execToolBatch` gains `finishReason string` and forwards it at all four `s.execTool(...)` sites:

```go
func (s *Session) execToolBatch(ctx context.Context, calls []llm.ToolCallData, profile *provider.Profile, finishReason string) ([]tool.ExecResult, error) {
```

`agent/session_lifecycle.go:1151`:

```go
		results, execErr := s.execToolBatch(ctx, calls, profile, resp.Finish.Reason)
```

Run `go build ./...` and fix every caller the compiler flags (tests included) by passing `""` where no model response is in play, and the real finish reason where one is.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./agent/internal/tool/repair/ ./agent/ -run 'TestExplainTruncatedCall|TestPrepareToolCall'`
Expected: PASS

- [ ] **Step 6: Full gate**

Run: `go build ./... && go test ./llm/... ./agent/...`
Expected: PASS, exit code 0.

- [ ] **Step 7: Commit**

```bash
git add agent/internal/tool/repair/explain.go agent/internal/tool/repair/explain_test.go agent/session_tool_repair.go agent/session_tool_repair_test.go agent/session_tools.go agent/session_tool_round.go agent/session_lifecycle.go agent/session_tools_aux_exact_fuzz_test.go
git commit -m "fix(agent): report length-truncated tool calls as truncation, never repair them"
```
