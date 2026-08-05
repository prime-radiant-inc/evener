# Recoverable Provider Failures Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep a Serf session open and model-switchable after a terminal provider failure, including a non-retryable quota response.

**Architecture:** Make provider errors terminal to the current turn and any active goal, but not to the session. Preserve the existing failed-turn and idle-boundary event flow so AppWire derives `changeModel=true`; prove the real daemon path and existing web control consume that recovery without production protocol or frontend changes.

**Tech Stack:** Go session engine and AppWire daemon integration tests; TypeScript, React Testing Library, Vitest, and Biome for the web regression.

## Global Constraints

- Follow `docs/testing.md`; default tests must use scripted providers and require no credentials, quota, model access, or network.
- Do not change AppWire methods, fields, codes, or notification shapes.
- Preserve existing retry counts, provider error text/unwrap behavior, failed-turn transcript/API-log evidence, and active-turn model-switch conflict behavior.
- Do not add provider-specific or quota-specific parsing.
- Explicit shutdown and engine/session-integrity failures outside the provider-call path remain session-fatal.
- No production server, AppWire, web, or TUI change should be necessary; add regressions against their existing behavior.
- Before the frontend gate, run `npx biome check --write` on every touched frontend file.
- Leave the pre-existing untracked `docs/superpowers/roborev-failed-reports-4295-plus.md` untouched.

## File Structure

- Modify `agent/session_model_call.go`: separate provider request terminality from session lifecycle; terminate an active goal, emit the failure, and settle the turn idle without closing the session.
- Modify `agent/session_model_test.go`: add the deterministic 403/quota regression that proves one provider attempt, failed-turn evidence, idle state, and a successful next operation.
- Modify `agent/session_provenance_test.go`: rename and strengthen the non-retryable failure test for idle-boundary provenance cleanup instead of close-time cleanup.
- Modify `agent/fuzz_mc_classify_model_error_test.go`: remove the obsolete non-retryable/close input and assert that the pure classifier only chooses cancel, content-filter recovery, warning, or terminal-turn outcomes.
- Modify `agent/lifecycle_seqfuzz_test.go`: update the auth-fault lifecycle model and comments so provider failures no longer predict a closed session.
- Modify `agent/session_goal.go`: correct the lifecycle comment to describe provider failures as goal-terminal rather than session-terminal.
- Modify `cmd/serf/scripted_provider_test.go`: extend the external provider test double with error-returning steps and a multi-provider installer while preserving all existing response-only fixtures.
- Modify `cmd/serf/serve_model_switch_test.go`: add a real daemon/AppWire regression covering failed turn, idle capabilities, `thread/read`, model switch, and a next turn on the switched model.
- Modify `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx`: prove rerendering from unavailable to restored `changeModel` re-enables the existing picker and sends the qualified model-set request.

---

### Task 1: Recover the agent session after provider failure

**Files:**
- Modify: `agent/session_model_call.go:469-574`
- Modify: `agent/session_model_test.go:1543-1602`
- Modify: `agent/session_provenance_test.go:83-97`
- Modify: `agent/fuzz_mc_classify_model_error_test.go:11-67`
- Modify: `agent/lifecycle_seqfuzz_test.go:299-333,465-478,681-697`
- Modify: `agent/session_goal.go:265-272`

**Interfaces:**
- Consumes: `Session.terminateGoalOnError(context.Context, error)`, `Session.finishProcessingAtBoundary(context.Context, SessionState)`, `llm.Kind(error)`, and existing turn-failure event/transcript projection.
- Produces: terminal provider failures return `fmt.Errorf("provider error: %w", err)`, block any active goal, and leave `Session.State()==SessionIdle`; `classifyModelError(isCancellation bool, kind llm.ErrorKind, contentFilterAlreadyRetried bool, haveContextMgr bool) modelErrorDecision` no longer accepts provider retryability or returns a close decision.

- [ ] **Step 1: Write the failing agent regression**

Extend the existing provider-error test area in `agent/session_model_test.go` with a focused test using `llm.ErrorFromHTTPStatus("kimi-anthropic", http.StatusForbidden, "billing-cycle quota exhausted", nil, nil)` and a zero-retry `fakeErrAdapter`. Capture events while the session remains open, then assert:

```go
func TestSession_NonRetryableProviderErrorLeavesSessionIdle(t *testing.T) {
    t.Parallel()
    adapter := &fakeErrAdapter{
        name: "kimi-anthropic",
        steps: []func(llm.Request) (llm.Response, error){
            func(llm.Request) (llm.Response, error) {
                return llm.Response{}, llm.ErrorFromHTTPStatus(
                    "kimi-anthropic", http.StatusForbidden,
                    "billing-cycle quota exhausted", nil, nil,
                )
            },
            func(req llm.Request) (llm.Response, error) {
                return toolCallResponse(communicateCall("after_failure", "recovered")), nil
            },
        },
    }
    client := llm.NewClient()
    client.Register(adapter)
    policy := llm.RetryPolicy{MaxRetries: 0}
    sess, err := NewSession(
        client,
        NewOpenAIProfile("k3"),
        execenv.NewLocalExecutionEnvironment(t.TempDir()),
        SessionConfig{LLMRetryPolicy: &policy},
    )
    if err != nil {
        t.Fatalf("NewSession: %v", err)
    }
    sess.getOrCreateGoalStore().Set("recover from provider failure", sess.sclock().Now())

    var evs []events.SessionEvent
    done := make(chan struct{})
    go func() {
        defer close(done)
        for ev := range sess.Events() {
            evs = append(evs, ev)
        }
    }()

    _, processErr := sess.ProcessInput(context.Background(), "trigger quota failure", nil)
    if processErr == nil || !strings.Contains(processErr.Error(), "billing-cycle quota exhausted") {
        t.Fatalf("ProcessInput error = %v, want quota detail", processErr)
    }
    var providerErr llm.Error
    if !errors.As(processErr, &providerErr) {
        t.Fatalf("ProcessInput error does not unwrap to llm.Error: %v", processErr)
    }
    if got := len(adapter.Requests()); got != 1 {
        t.Fatalf("provider requests after failure = %d, want 1", got)
    }
    if got := sess.State(); got != SessionIdle {
        t.Fatalf("state after provider failure = %q, want %q", got, SessionIdle)
    }
    snap, ok := sess.getOrCreateGoalStore().Snapshot()
    if !ok || snap.Status != goal.StatusBlocked {
        t.Fatalf("goal after provider failure = %+v, want blocked", snap)
    }

    out, secondErr := sess.ProcessInput(context.Background(), "try again", nil)
    if secondErr != nil || out != "recovered" {
        t.Fatalf("second ProcessInput = (%q, %v), want (%q, nil)", out, secondErr, "recovered")
    }
    sess.Close()
    <-done
    // Count EventError and EventTurnEnded in evs and require at least one of each.
}
```

Use `llm.NewClient` plus `Register` directly if no `clientWithAdapter` helper exists; the committed test must contain the concrete construction. Replace the final event-count comment with explicit loops and fatal assertions. Do not close the session until after the second operation.

- [ ] **Step 2: Add failing goal and provenance assertions**

In the same regression, activate a goal without scheduling an autonomous kick by calling `sess.getOrCreateGoalStore().Set(...)` through the existing goal-store test pattern (or use the narrowest existing helper). After the provider failure, assert its snapshot is `goal.StatusBlocked` and the session is still idle. Rename `TestNonRetryableModelErrorClearsActiveProvenanceBeforeClose` to `TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary`, keep the active/completed provenance checks, and add:

```go
if got := s.State(); got != SessionIdle {
    t.Fatalf("state after non-retryable provider error = %q, want %q", got, SessionIdle)
}
```

- [ ] **Step 3: Run the focused tests and verify the old behavior fails**

Run:

```bash
go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary' -count=1
```

Expected before implementation: FAIL because the 403 path closes the session (and the second operation cannot run).

- [ ] **Step 4: Remove session closure from the model-error decision**

In `agent/session_model_call.go`, change `modelErrorDecision` and `classifyModelError` to:

```go
type modelErrorDecision struct {
    Action             modelErrorAction
    EmitContextLenWarn bool
}

func classifyModelError(
    isCancellation bool,
    kind llm.ErrorKind,
    contentFilterAlreadyRetried bool,
    haveContextMgr bool,
) modelErrorDecision {
    if isCancellation {
        return modelErrorDecision{Action: modelErrorCancel}
    }
    if kind == llm.KindContentFilter && !contentFilterAlreadyRetried && haveContextMgr {
        return modelErrorDecision{Action: modelErrorContentFilterRetry}
    }
    return modelErrorDecision{
        Action:             modelErrorTerminal,
        EmitContextLenWarn: kind == llm.KindContextLength,
    }
}
```

Update `handleModelError` to stop calculating `llmErrNonRetryable`, pass the four remaining arguments, and replace the `dec.CloseSession` block with unconditional terminal-goal handling before the idle boundary:

```go
s.terminateGoalOnError(ctx, err)
s.finishProcessingAtBoundary(ctx, SessionIdle)
return false, fmt.Errorf("provider error: %w", err)
```

Keep cancellation and one-time content-filter recovery unchanged. Do not call `finishActiveProvenance` directly; the idle boundary already owns it. Rewrite the function comments to say retryability governs request retries elsewhere, while this function's terminal path fails the turn and returns the open session to idle.

- [ ] **Step 5: Update the classifier fuzz contract**

Remove `llmErrNonRetryable` from the fuzz seed shape, callback, classifier calls, and invariants in `agent/fuzz_mc_classify_model_error_test.go`. Replace close-oriented assertions with:

```go
if dec.Action == modelErrorCancel && dec.EmitContextLenWarn {
    t.Fatalf("cancel must not warn: %+v", dec)
}
if dec.EmitContextLenWarn && (dec.Action != modelErrorTerminal || kind != llm.KindContextLength) {
    t.Fatalf("context warning requires terminal context-length error: kind=%v dec=%+v", kind, dec)
}
```

Retain seeds for cancellation, content-filter retry, repeated content-filter terminality, context-length warning, and unknown errors.

- [ ] **Step 6: Update lifecycle/property-test semantics and comments**

In `agent/lifecycle_seqfuzz_test.go`, change the auth fault comments from “non-retryable close” to “non-retryable terminal turn.” Do not mark `lifecycleModel.closed` because of `faultAuth`; only `opClose`/actual `SessionClosed` may make closure monotonic. Preserve the boundary oracle accepting idle/awaiting/closed so the fuzz harness now exercises operations after a 401 instead of treating the session as dead. In `agent/session_goal.go`, change “non-retryable provider error routes to blocked” to clarify that the goal blocks while the session remains available.

- [ ] **Step 7: Run agent unit and deterministic fuzz gates**

Run:

```bash
gofmt -w agent/session_model_call.go agent/session_model_test.go agent/session_provenance_test.go agent/fuzz_mc_classify_model_error_test.go agent/lifecycle_seqfuzz_test.go agent/session_goal.go
go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestSession_ProvideErrorReturnsErrorToCaller|TestProviderErrorEmitsStructuredCause|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary|TestGoalErrorBlockIsPersisted' -count=1
go test -tags serffuzz ./agent -run '^FuzzMcClassifyModelError$' -count=1
```

Expected: PASS; the regression observes exactly one request before the failure, the active goal is blocked, the session is idle, and a second operation succeeds.

- [ ] **Step 8: Commit the agent behavior**

```bash
git add agent/session_model_call.go agent/session_model_test.go agent/session_provenance_test.go agent/fuzz_mc_classify_model_error_test.go agent/lifecycle_seqfuzz_test.go agent/session_goal.go
git commit -m "fix: keep sessions open after provider failures"
```

### Task 2: Prove AppWire restores model switching end to end

**Files:**
- Modify: `cmd/serf/scripted_provider_test.go:26-68`
- Modify: `cmd/serf/serve_model_switch_test.go:1-121`

**Interfaces:**
- Consumes: Task 1's provider-failure contract, `appwire.Client.TurnStart`, `Client.Notifications`, `Client.ThreadRead`, and `Client.ThreadModelSet`.
- Produces: an integration regression proving a failed turn is followed by idle `ThreadStatusChangedParams` with `Capabilities.ChangeModel=true`, `thread/read` agrees, and the next request uses the selected provider/model.

- [ ] **Step 1: Extend the scripted provider boundary to return errors and install multiple providers**

Add an optional error-returning script while retaining the existing `steps` API used by other tests:

```go
type scriptedProvider struct {
    name string

    mu         sync.Mutex
    requests   []llm.Request
    steps      []func(llm.Request) llm.Response
    errorSteps []func(llm.Request) (llm.Response, error)
    i          int
}
```

In `Complete`, append the request once, consume `errorSteps` when non-empty, otherwise preserve the current `steps` behavior and default `scriptedCommunicate("done")`. Apply provider/model/finish defaults only when there is no returned error.

Add `installServeScriptedProviders(t, adapters...)` that registers every adapter in one `llm.Client` and returns a `providercfg.Config` whose `Instances` contains one `{Name: adapter.Name(), Type: "openai"}` entry per adapter. Keep `installServeScriptedProvider` as a one-adapter wrapper so existing tests do not change.

- [ ] **Step 2: Write the failing real-daemon regression**

Add `TestServeModelSwitch_ProviderFailureRestoresCapability` to `cmd/serf/serve_model_switch_test.go`. Install two scripted instances: `kimi-anthropic` returns the 403, and `openai` returns `scriptedCommunicate("switched provider recovered")`. Start the daemon with `--model kimi-anthropic/k3`:

```go
installServeScriptedProviders(t,
    &scriptedProvider{
        name: "kimi-anthropic",
        errorSteps: []func(llm.Request) (llm.Response, error){
            func(llm.Request) (llm.Response, error) {
                return llm.Response{}, llm.ErrorFromHTTPStatus(
                    "kimi-anthropic", http.StatusForbidden,
                    "billing-cycle quota exhausted", nil, nil,
                )
            },
        },
    },
    &scriptedProvider{
        name: "openai",
        errorSteps: []func(llm.Request) (llm.Response, error){
            func(llm.Request) (llm.Response, error) {
                return scriptedCommunicate("switched provider recovered"), nil
            },
        },
    },
)
```

Boot `runServe`, initialize and subscribe with `ThreadRead(..., Subscribe: true)`, then start the first turn with `Client.TurnStart`. Consume notifications in one goroutine and record ordered milestones:

```go
case appwire.NotifyTurnCompleted:
    var params appwire.TurnCompletedParams
    if params.Turn.Status == appwire.TurnStatusFailed {
        failedTurnOnce.Do(func() { close(failedTurn) })
    }
case appwire.NotifyThreadStatusChanged:
    var params appwire.ThreadStatusChangedParams
    if json.Unmarshal(notification.Params, &params) == nil &&
        params.Status.Type == appwire.ThreadStatusIdle &&
        params.Capabilities != nil && params.Capabilities.ChangeModel {
        idleCapOnce.Do(func() { close(idleCapability) })
    }
```

Use channels tied to these structured frames instead of sleeps or polling.

- [ ] **Step 3: Assert snapshot and switching behavior**

After receiving the failed-turn and idle-capability milestones, assert:

```go
read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
// read.Thread.Status.Type == appwire.ThreadStatusIdle
// read.Thread.Serf.Capabilities.ChangeModel == true

err = client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
    Ref: ref, ModelProvider: "openai", Model: "gpt-5.6-sol",
})
```

Start a second turn and wait for its successful `turn/completed`. Assert the scripted provider recorded exactly two requests and that `requests[1].Provider == "openai"` and `requests[1].Model == "gpt-5.6-sol"`. Shut down the daemon through `/shutdown` and join it as the existing test does.

- [ ] **Step 4: Run the integration test against the old behavior**

Before landing Task 1's implementation (or by temporarily restoring the old close block for mutation verification), run:

```bash
go test ./cmd/serf -run '^TestServeModelSwitch_ProviderFailureRestoresCapability$' -count=1
```

Expected under old behavior: FAIL because the status/capability path closes the session and `thread/model/set` cannot execute. Restore Task 1 immediately after this mutation check.

- [ ] **Step 5: Run the focused daemon regressions**

Run:

```bash
gofmt -w cmd/serf/scripted_provider_test.go cmd/serf/serve_model_switch_test.go
go test ./cmd/serf -run '^TestServeModelSwitch_' -count=1
```

Expected: PASS; no production server/AppWire code changes are present.

- [ ] **Step 6: Commit the daemon proof**

```bash
git add cmd/serf/scripted_provider_test.go cmd/serf/serve_model_switch_test.go
git commit -m "test: cover model switch after provider failure"
```

### Task 3: Prove the web control re-enables and sends the switch

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx:127-201`

**Interfaces:**
- Consumes: existing `ModelSwitch` props, `ThreadModel.capabilities.changeModel`, `FakeClient`, and the existing `model/list` and `thread/model/set` calls.
- Produces: a UI regression showing an updated idle model snapshot re-enables the existing control and sends `{ref, modelProvider, model}` without reload.

- [ ] **Step 1: Add the capability-recovery regression**

Near the existing disabled-trigger test, add:

```tsx
test("a failed turn's idle capabilities re-enable model switching without reload", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());
  let called: unknown;
  fake.on("thread/model/set", (params) => {
    called = params;
    return {};
  });

  const view = render(
    <ModelSwitch
      sessionRef="ref_a"
      model={testModel({
        status: { type: "active" },
        activeTurnId: "turn_failed",
        capabilities: { ...CAPABILITIES, changeModel: false },
      })}
    />,
  );
  expect(trigger().disabled).toBe(true);

  view.rerender(
    <ModelSwitch
      sessionRef="ref_a"
      model={testModel({
        status: { type: "idle" },
        activeTurnId: undefined,
        capabilities: { ...CAPABILITIES, changeModel: true },
      })}
    />,
  );
  expect(trigger().disabled).toBe(false);

  await user.click(trigger());
  const combobox = await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  await user.clear(combobox);
  await user.keyboard("gpt-5.5");
  await user.click(await screen.findByRole("option", { name: /openai\/gpt-5\.5/i }));
  await waitFor(() =>
    expect(called).toEqual({ ref: "ref_a", modelProvider: "openai", model: "gpt-5.5" }),
  );
});
```

If `ThreadModel.activeTurnId` requires `null` rather than `undefined`, use the repository type's exact value. Do not change `ModelSwitch.tsx`; the test pins existing capability-driven behavior.

- [ ] **Step 2: Run formatting on the touched frontend test**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/chrome/ModelSwitch.test.tsx
```

Expected: exit 0; review any formatter changes before proceeding.

- [ ] **Step 3: Run the focused Vitest file**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/ModelSwitch.test.tsx
```

Expected: PASS, including the new disabled-to-enabled rerender and qualified `thread/model/set` assertion.

- [ ] **Step 4: Commit the web regression**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx
git commit -m "test: cover model switch recovery in web"
```

### Task 4: Verify the complete change

**Files:**
- Verify only; modify only if a gate identifies a root-cause defect in the files above.

**Interfaces:**
- Consumes: all prior task outputs.
- Produces: fresh repository-wide evidence that the lifecycle change preserves agent, daemon, frontend, lint, build, and deterministic test contracts.

- [ ] **Step 1: Confirm the diff contains no unintended production surface changes**

```bash
git status --short
git diff --check
git diff HEAD~3 -- agent cmd/serf cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx
```

Expected: only the planned agent production change and planned tests/helpers; the pre-existing untracked review report remains unmodified.

- [ ] **Step 2: Run focused Go packages**

```bash
go test ./agent ./server ./cmd/serf -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the canonical frontend gate**

```bash
make test-web
```

Expected: PASS for typecheck, Vitest, and Biome.

- [ ] **Step 4: Run the browser gate on this Chrome-capable host**

```bash
make test-web-browser
```

Expected: PASS for layout, overflow, and spawn guards. If Chrome itself is unavailable, report that prerequisite failure; do not call it a pass.

- [ ] **Step 5: Run deterministic fuzz replay**

```bash
make fuzz
```

Expected: PASS, including the updated classifier and lifecycle sequence targets.

- [ ] **Step 6: Run the canonical merge approval gate**

```bash
make merge-approval-gate
```

Expected: PASS for `make lint`, `make build`, and `ROOT_FULL=1 make test`.

- [ ] **Step 7: Review final repository state**

```bash
git status --short
git log -4 --oneline
```

Expected: no unstaged implementation changes or generated drift; only the known pre-existing untracked report may remain. Record exact test commands and outcomes for the final report.
