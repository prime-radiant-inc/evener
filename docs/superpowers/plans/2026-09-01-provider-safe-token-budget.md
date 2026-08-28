# Provider-Safe Token Budget Admission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent every request whose known total/input/output caps and conservative token calculation prove invalid from reaching a provider.

**Architecture:** Registry metadata first preserves total context, max input, and max output independently. A pure `llm` budget evaluator then constrains output and returns a typed local error for input/no-headroom failures; `Client.Complete` and `Client.Stream` invoke it at the non-bypassable dispatch boundary. The agent invokes the same evaluator earlier to compact/rebuild and to recompute continuation and fallback requests.

**Tech Stack:** Go, existing registry/profile/request abstractions, scripted provider tests.

**Spec:** `docs/superpowers/specs/2026-09-01-provider-safe-token-budget-design.md`

## Global Constraints

- Preserve full model output when it fits; shrink only to satisfy a known limit.
- Unknown limits remain unknown; do not invent them.
- Safety reserve is `max(1_024, ceil(1% of the smallest applicable known input/total limit))`.
- No production change precedes a failing test.
- No provider/network access in default tests.
- Do not change context-strategy thresholds.
- A local admission failure performs no adapter/transport call.
- Compaction and provider-context recovery are bounded to one retry.
- Do not commit; the user requested implementation and verification, not repository delivery.

## File Structure

- `llm/registry/types.go`: add `Caps.MaxInputTokens`.
- `llm/registry/modelsdev.go`, `llm/providers/{responses,google,chatcompletions}/models.go`: preserve source cap semantics.
- `llm/registry/{load,resolve,derive,schema}.go`: clone, inherit, validate, and derive the new cap correctly.
- `appwire/types.go`, `cmd/evener/models.go`, `agent/provider/profile.go`: expose the independent input cap.
- `llm/token_budget.go`: pure calculation, result metadata, and typed local error.
- `llm/registry_shape.go`, `llm/client.go`: constrain output and enforce admission immediately before dispatch.
- `llm/providers/{chatcompletions,responses,anthropic,google}/*request*.go`: prevent wire builders from raising admitted output.
- `agent/session_model_call.go`, `agent/session_lifecycle.go`: full-history budgeting, bounded compaction recovery, anchor/fallback recomputation, and provider-context recovery.
- Adjacent `_test.go` files: deterministic behavior coverage.

---

### Task 1: Preserve total, input, and output caps independently

**Files:**
- Modify: `llm/registry/types.go`
- Modify: `llm/registry/modelsdev.go`
- Modify: `llm/registry/load.go`
- Modify: `llm/registry/resolve.go`
- Modify: `llm/registry/derive.go`
- Modify: `llm/registry/schema.go`
- Modify: `llm/providers/responses/models.go`
- Modify: `llm/providers/google/protocol_transport.go`
- Modify: `agent/provider/profile.go`
- Modify: `appwire/types.go`
- Modify: `cmd/evener/models.go`
- Test: adjacent registry/provider/appwire tests

**Interfaces:**
- Produces: `registry.Caps.MaxInputTokens *int`.
- Produces: `func (p *Profile) MaxInputTokens() int` returning a positive cap or zero when unknown.
- Preserves: `ContextWindow` as total input-plus-output only.

- [ ] **Step 1: Write failing registry conversion and schema tests**

Add tests with a source row whose limits are deliberately distinct:

```go
contextLimit, inputLimit, outputLimit := 1_050_000, 922_000, 128_000
// Resolve the fixture and assert all three pointers preserve these exact values.
```

Add a user TOML validation table asserting each configured
`context_window = 0`, `max_input_tokens = 0`, and `max_output_tokens = 0`
fails with a field-specific positive-integer error.

- [ ] **Step 2: Run the focused registry tests and verify RED**

Run:

```sh
go test ./llm/registry -run 'Test.*(Limits|TokenCaps|Positive)' -count=1
```

Expected: compile failure for missing `MaxInputTokens` or a behavioral failure
showing `ContextWindow == 922000` rather than `1050000`.

- [ ] **Step 3: Implement the registry field and propagation**

Add to `Caps` beside the existing model facts:

```go
ContextWindow   *int `toml:"context_window" json:"context_window,omitempty"`
MaxInputTokens  *int `toml:"max_input_tokens" json:"max_input_tokens,omitempty"`
MaxOutputTokens *int `toml:"max_output_tokens" json:"max_output_tokens,omitempty"`
```

Map models.dev `Limit.Context`, `Limit.Input`, and `Limit.Output` one-to-one;
clone `MaxInputTokens`; include it in alias seeding; validate every configured
non-nil token limit is positive. Retain the junk-output check only against
`ContextWindow`, never `MaxInputTokens`.

- [ ] **Step 4: Run registry tests and verify GREEN**

Run:

```sh
go test ./llm/registry -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing live-metadata and public-descriptor tests**

Cover these source shapes:

```json
{"context_window":1050000,"max_input_tokens":922000,"max_output_tokens":128000}
```

and Google:

```json
{"inputTokenLimit":922000,"outputTokenLimit":128000}
```

Responses must populate all three caps; Google must populate independent input
and output while leaving total context unknown. Extend the appwire descriptor
round-trip assertion with `maxInputTokens`.

- [ ] **Step 6: Run focused tests and verify RED**

Run:

```sh
go test ./llm/providers/responses ./llm/providers/google ./appwire ./cmd/evener -run 'Test.*(Models|Descriptor|Metadata)' -count=1
```

Expected: missing field or wrong mapping failures.

- [ ] **Step 7: Implement live mappings and accessors**

Responses parsing must keep total aliases separate from input aliases. Google
must set `MaxInputTokens` from `inputTokenLimit`. Add the profile accessor and
model descriptor projection without changing existing JSON field names.

- [ ] **Step 8: Run affected packages and verify GREEN**

Run:

```sh
go test ./llm/providers/responses ./llm/providers/google ./appwire ./cmd/evener ./agent/provider -count=1
```

Expected: PASS.

---

### Task 2: Add the pure budget evaluator and hard client guard

**Files:**
- Create: `llm/token_budget.go`
- Create: `llm/token_budget_test.go`
- Modify: `llm/registry_shape.go`
- Modify: `llm/client.go`
- Modify: `llm/errors.go`
- Test: `llm/client_registry_test.go` or a focused new client admission test

**Interfaces:**
- Produces:

```go
type TokenBudget struct {
    InputTokens     int
    SafetyTokens    int
    RequestedOutput int
    AdmittedOutput  int
    LimitedOutput   bool
}

func ApplyTokenBudget(req Request, res registry.Resolved) (Request, TokenBudget, error)

type ContextBudgetError struct {
    Provider, Model, Limit string
    InputTokens, OutputTokens, Maximum int
}
```

- `ContextBudgetError` is non-retryable and classifies as `KindContextLength`.

- [ ] **Step 1: Write the failing pure regression test**

Use the exact incident:

```go
req := Request{
    Model: "glm-5.2-vision",
    Messages: []Message{User("x")},
    InputTokensEstimate: 393_217,
    MaxTokens: new(131_072),
}
res := registry.Resolved{Caps: registry.Caps{
    ContextWindow: new(524_288),
    MaxInputTokens: new(393_216),
    MaxOutputTokens: new(131_072),
}}
```

Assert the evaluator returns a local max-input `ContextBudgetError`; then use
`MaxInputTokens=nil` and assert output is reduced so
`budget.InputTokens + budget.AdmittedOutput <= 524288`.

- [ ] **Step 2: Run the pure test and verify RED**

Run:

```sh
go test ./llm -run 'TestApplyTokenBudget' -count=1
```

Expected: compile failure because the evaluator does not exist.

- [ ] **Step 3: Implement minimal pure calculation**

Calculate effective input as the maximum of local request estimate,
`InputTokensEstimate`, and `FullHistoryInputTokensEstimate`; add the named
safety reserve once. Clamp positive output by known output and total limits.
Return a typed error for max-input overflow or no positive total headroom. Use
subtraction only after ordered comparisons to avoid integer overflow.

- [ ] **Step 4: Add and run table cases**

Add cases for: no caps, output-only, input-only, total-only with unset output,
all caps, exact boundary, one-token overflow, non-positive request max,
continuation shadow larger than delta, and very large integers. Run:

```sh
go test ./llm -run 'TestApplyTokenBudget' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing shaping and no-dispatch tests**

Assert `ShapeRequest` clamps an explicit request of 8192 to a known output cap
of 4096. Register a recording adapter under a resolvable instance, issue both
Complete and Stream requests that exceed max input, and assert the adapter call
count remains zero. Add middleware that replaces a safe request with an unsafe
one and assert the innermost guard still prevents dispatch.

- [ ] **Step 6: Run focused client tests and verify RED**

Run:

```sh
go test ./llm -run 'Test(ShapeRequest.*Cap|Client.*TokenBudget)' -count=1
```

Expected: explicit max is not clamped and/or the recording adapter is called.

- [ ] **Step 7: Implement final client admission**

Make `ShapeRequest` clamp known output. In Complete and Stream, re-shape and
call `ApplyTokenBudget` inside the innermost base handler immediately before the
override/protocol invocation. Ensure `Kind` recognizes `ContextBudgetError` as
context length and provider rewriting does not make it look like a remote HTTP
failure.

- [ ] **Step 8: Run the complete llm package and verify GREEN**

Run:

```sh
go test ./llm -count=1
```

Expected: PASS.

---

### Task 3: Prevent protocol builders from raising admitted output

**Files:**
- Modify: `llm/providers/chatcompletions/request.go`
- Modify: `llm/providers/responses/request.go`
- Modify: `llm/providers/google/request.go`
- Modify: `llm/providers/anthropic/protocol_request.go`
- Modify: `llm/providers/anthropic/request.go`
- Test: adjacent protocol request tests

**Interfaces:**
- Consumes: admitted positive `Request.MaxTokens`.
- Guarantee: the final body output allocation is never greater than admitted
  `Request.MaxTokens` or known `MaxOutputTokens`.

- [ ] **Step 1: Write failing provider-option override tests**

For each protocol, build a request with `MaxTokens=100` and provider options
attempting to set the protocol's output field to 1000. Assert the final body
contains at most 100.

For Anthropic, add a budget-thinking case where `budget_tokens=1024` and the
admitted max is 1000. Assert body construction returns a local error rather
than raising `max_tokens` above 1000.

- [ ] **Step 2: Run protocol tests and verify RED**

Run:

```sh
go test ./llm/providers/chatcompletions ./llm/providers/responses ./llm/providers/google ./llm/providers/anthropic -run 'Test.*Admitted.*Max|Test.*ProviderOption.*Max' -count=1
```

Expected: body allocations exceed 100 or Anthropic silently raises its value.

- [ ] **Step 3: Implement post-overlay reconciliation**

After provider options/body overlays, normalize each protocol's output field to
`min(positive wire value, positive req.MaxTokens, positive caps.MaxOutputTokens)`.
If Anthropic cannot satisfy `max_tokens > thinking.budget_tokens` inside that
ceiling, return `ContextBudgetError` (or a wrapped local validation error that
classifies identically) before HTTP dispatch.

- [ ] **Step 4: Run all four provider packages and verify GREEN**

Run:

```sh
go test ./llm/providers/chatcompletions ./llm/providers/responses ./llm/providers/google ./llm/providers/anthropic -count=1
```

Expected: PASS.

---

### Task 4: Integrate bounded agent recovery, continuations, and fallbacks

**Files:**
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_lifecycle.go`
- Modify: focused agent continuation/fallback/error tests

**Interfaces:**
- Consumes: `llm.ApplyTokenBudget` and `llm.ContextBudgetError`.
- Produces: one force-compaction/rebuild retry for local or provider context
  overflow, then terminal local/provider error.

- [ ] **Step 1: Write a failing primary request budget test**

Construct a scripted provider/profile with total 524,288 and output 131,072,
build a full-history request whose supplied estimate crosses the combined
boundary, and assert the scripted provider sees a reduced positive MaxTokens
satisfying the total invariant.

- [ ] **Step 2: Run focused test and verify RED**

Run:

```sh
go test ./agent -run 'TestSession.*TokenBudget.*Primary' -count=1
```

Expected: provider sees the full 131,072 allocation.

- [ ] **Step 3: Budget full history before continuation planning**

After `buildModelRequest`, attach the larger context-manager estimate, call the
shared evaluator against `profile.Resolved()`, and only then call Responses
continuation planning. Preserve the full-history estimate on delta requests.
Return the typed error to a bounded prepare/compact/rebuild wrapper when input
cannot fit.

- [ ] **Step 4: Write and run failing continuation tests**

Add one test whose delta is tiny but full-history shadow exceeds max input, and
one anchor-rejection test whose restored full history requires a smaller output
allocation. Assert no unsafe scripted-provider call occurs. Run:

```sh
go test ./agent -run 'TestSession.*(Continuation|Anchor).*TokenBudget' -count=1
```

Expected: unsafe delta or full-history recovery reaches the provider.

- [ ] **Step 5: Implement continuation re-evaluation**

Use `FullHistoryInputTokensEstimate` for the delta. Re-apply budgeting to
`responsesContinuationFullHistoryFallbackRequest` before its call.

- [ ] **Step 6: Write and run a failing fallback-cap test**

Primary cap: 131,072. Fallback cap: 8,192 with its own smaller input/total
limits. Force a fallback-eligible primary error and assert the fallback sees
8,192 or less, never 131,072. Run:

```sh
go test ./agent -run 'TestSession.*Fallback.*TokenBudget' -count=1
```

Expected: fallback inherits 131,072.

- [ ] **Step 7: Implement per-fallback reset and budgeting**

Build the fallback request from full history, clear `MaxTokens`, apply the
fallback profile's known output cap, recompute its input estimate after changing
provider/model, and call the shared evaluator before dispatch. Skip an unsafe
fallback without a transport call and continue the configured chain.

- [ ] **Step 8: Write failing bounded recovery tests**

Test local no-headroom: first preparation fails, force compaction changes
history, second preparation succeeds, and provider call count is one. Test no
progress: both preparations fail and provider call count is zero. Test a remote
context-length error: exactly one compacted retry occurs; a second context
error is terminal.

- [ ] **Step 9: Run recovery tests and verify RED**

Run:

```sh
go test ./agent -run 'TestSession.*ContextBudget.*(Compact|Retry|Terminal)' -count=1
```

Expected: no local recovery and no remote context retry under current behavior.

- [ ] **Step 10: Implement bounded recovery**

Add per-input booleans for local admission compaction and provider context
recovery. Rebuild from session history after compaction; do not reuse a stale
request. A second failure follows the existing terminal settlement path.

- [ ] **Step 11: Run the agent package and verify GREEN**

Run:

```sh
go test ./agent -count=1
```

Expected: PASS.

---

### Task 5: Simplify and verify

**Files:**
- Review: the complete working-tree diff
- Modify: only changed files, with no behavior changes

- [ ] **Step 1: Run gofmt and focused tests**

Run:

```sh
gofmt -w <all touched .go files>
go test ./llm/registry ./llm ./llm/providers/chatcompletions ./llm/providers/responses ./llm/providers/google ./llm/providers/anthropic ./agent/provider ./agent ./appwire ./cmd/evener -count=1
```

Expected: PASS with no warnings.

- [ ] **Step 2: Run the requested simplify-code workflow**

Gather `git diff HEAD`, dispatch four read-only reviewers in parallel for
reuse, simplification, efficiency, and altitude, deduplicate findings, and
apply only behavior-preserving fixes within the diff.

- [ ] **Step 3: Re-run focused tests after simplification**

Run the exact Step 1 test command again. If simplification causes a failure,
revert that simplification rather than changing tests or behavior.

- [ ] **Step 4: Run required repository gates**

Run:

```sh
make lint
make vet
make test
```

Expected: all commands exit zero. Investigate and fix every failure at its root.

- [ ] **Step 5: Verify deliverables and workspace**

Run:

```sh
git status --short
git diff --check
git diff --stat
```

Confirm the spec, plan, production changes, and regression tests are present;
no scratch files or generated drift remain.
