# OpenAI TUI Auth And Stream Cleanup Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clean up OpenAI Responses streamed tool-call handling and add first-class OpenAI OAuth login/logout/status UX inside `serf-tui`.

**Architecture:** Keep the existing OpenAI auth service and storage model, and move TUI auth into a native interactive flow that calls the branch-local `internal/auth/openai.Service` directly. Use the existing slash-command and picker patterns in `serf-tui`, and recreate the embedded OpenAI session after login/logout so live requests pick up the new auth state without requiring a full TUI restart. Refactor the OpenAI streaming adapter so one logical backend function call always maps to one canonical Serf tool call keyed by `call_id`, with `item_id` used only as an internal alias.

**Tech Stack:** Go, Bubble Tea, existing `internal/auth/openai` service, OpenAI Responses SSE adapter tests.

---

## Chunk 1: Stream Tool-Call Tracker Cleanup

### Task 1: Extract the protocol rules into focused adapter logic

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Test: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Write the failing adapter test for `item_id` and `call_id` aliasing**

Add a focused test in `llm/providers/openai/adapter_test.go` that streams:

- `response.output_item.added` with `item.id=fc_1`, `call_id=call_1`, `name=task_list`
- multiple `response.function_call_arguments.delta` events keyed only by `item_id=fc_1`
- `response.function_call_arguments.done` keyed by `item_id=fc_1`
- `response.output_item.done` keyed by both ids
- `response.completed` with `output: []`

Assert:

- exactly one tool call is returned
- the tool call id is `call_1`
- the tool call name is `task_list`
- the arguments equal the authoritative final JSON

- [ ] **Step 2: Run the focused adapter test and capture the baseline behavior**

Run:

```bash
go test ./llm/providers/openai -run 'TestAdapter_Complete_OAuthTransportTracksItemIDAndFragmentedToolArguments'
```

Expected:

- if run before refactor on the broken baseline, FAIL with duplicate or malformed tool-call behavior
- if run after a hotfix branch already passes, treat the passing result as proof that the extracted tracker must preserve current live behavior exactly

- [ ] **Step 3: Introduce a focused tool-call tracker inside the OpenAI adapter**

In `llm/providers/openai/adapter.go`, refactor the stream parsing logic into a small private helper or cohesive local type with methods equivalent to:

```go
type toolCallTracker struct {
    byCallID map[string]*toolState
    byItemID map[string]*toolState
}
```

The tracker must:

- register `output_item.added`
- resolve delta events via `item_id`
- canonicalize public ids to `call_id`
- store fragment arguments
- finalize with authoritative end-of-call arguments

- [ ] **Step 4: Emit canonical stream events only**

Update stream emission so:

- `ToolCallStart` uses canonical `call_id`
- `ToolCallDelta` contains only the newly received fragment
- `ToolCallEnd` contains final canonical name and final authoritative arguments

Do not emit any tool call keyed only by `item_id` when a `call_id` exists.

- [ ] **Step 5: Run the focused adapter tests**

Run:

```bash
go test ./llm/providers/openai -run 'TestAdapter_Complete_OAuthTransportPreservesStreamedToolCallsWhenCompletedOutputIsEmpty|TestAdapter_Complete_OAuthTransportTracksItemIDAndFragmentedToolArguments'
```

Expected:

- PASS

- [ ] **Step 6: Commit the adapter cleanup**

```bash
git add llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go
git commit -m "refactor: clean up OpenAI streamed tool call tracking"
```

## Chunk 2: Accumulator Semantics Hardening

### Task 2: Make tool-call end semantics explicit and provider-safe

**Files:**
- Modify: `llm/stream_accumulator.go`
- Test: `llm/stream_accumulator_test.go`

- [ ] **Step 1: Write the failing accumulator test for authoritative end payloads**

Add a focused test in `llm/stream_accumulator_test.go` where:

- `ToolCallStart` has no name
- `ToolCallDelta` emits fragmented JSON
- `ToolCallEnd` provides the final name and full arguments

Assert:

- the final response contains one tool call
- the final tool call name comes from `ToolCallEnd`
- the final arguments equal the authoritative final payload, not concatenated duplicate snapshots

- [ ] **Step 2: Run the focused accumulator test and capture the baseline behavior**

Run:

```bash
go test ./llm -run 'TestStreamAccumulator_ToolCallEndOverridesAccumulatedArgumentsAndName'
```

Expected:

- if run before the semantic fix, FAIL because final name/arguments are not authoritative
- if the branch already passes, use the passing result as the regression target while simplifying the implementation

- [ ] **Step 3: Update accumulator `ToolCallEnd` handling**

Modify `llm/stream_accumulator.go` so `ToolCallEnd`:

- fills in missing name and type
- replaces accumulated arguments with the final authoritative payload when present

Keep `ToolCallDelta` append-only.

- [ ] **Step 4: Run the focused accumulator test**

Run:

```bash
go test ./llm -run 'TestStreamAccumulator_ToolCallEndOverridesAccumulatedArgumentsAndName|TestStreamAccumulator_ToolCallEvents_AccumulatedInResponse'
```

Expected:

- PASS

- [ ] **Step 5: Commit the accumulator cleanup**

```bash
git add llm/stream_accumulator.go llm/stream_accumulator_test.go
git commit -m "fix: preserve authoritative streamed tool call payloads"
```

## Chunk 3: TUI OpenAI Auth Controller

### Task 3: Add TUI-native auth orchestration

**Files:**
- Create: `cmd/serf-tui/openai_auth.go`
- Modify: `cmd/serf-tui/model.go`
- Modify: `cmd/serf-tui/input.go`
- Modify: `cmd/serf-tui/model_picker.go`
- Test: `cmd/serf-tui/*_test.go`

- [ ] **Step 1: Inspect current TUI command and state patterns**

Read and reuse patterns from:

- `cmd/serf-tui/model.go`
- `cmd/serf-tui/message.go`
- `cmd/serf-tui/input.go`
- `cmd/serf-tui/model_picker.go`

Confirm where temporary modal/input states belong before writing code.

- [ ] **Step 2: Verify the branch-local OpenAI auth seam and exact reuse points**

Confirm this work will reuse:

- `internal/auth/openai/service.go`
- `cmd/serf/openai_login.go`
- `cmd/serf/openai_logout.go`
- `cmd/serf/openai_status.go`

Do not duplicate token exchange or storage logic in `cmd/serf-tui`.

- [ ] **Step 3: Write the failing TUI auth flow test**

Add a focused test that exercises:

- invoking the OpenAI login action
- surfacing an auth URL in system output
- entering a temporary redirect paste mode
- submitting a valid redirect URL
- appending a success message with signed-in email

Use a fake `authopenai.Service` seam or injectable function to avoid live OAuth in tests.

- [ ] **Step 4: Create `cmd/serf-tui/openai_auth.go`**

Add a focused controller with responsibilities like:

- load OpenAI auth status for the model’s `stateDir`
- start login with browser opener and manual redirect reader hooks
- perform logout
- format compact status strings/messages

Keep it TUI-local. Do not move generic auth logic out of `internal/auth/openai`.

- [ ] **Step 5: Add auth UI state to the TUI model**

Modify `cmd/serf-tui/model.go` to track:

- whether auth input mode is active
- the temporary redirect input prompt/state
- any pending auth action
- cached OpenAI auth status when provider is OpenAI

Use a narrow state shape. Avoid introducing a generic modal framework.

- [ ] **Step 6: Add `/openai` slash command and picker-backed action sheet**

Extend the existing TUI slash-command flow so:

- `/openai` appears in `slashCommandHelp()`
- `/openai` opens one picker-backed action sheet
- the picker choices are exactly:
  - `Sign in with OpenAI`
  - `Show OpenAI auth status`
  - `Sign out of OpenAI`
- if the active provider is not OpenAI, `/openai` appends a concise system message instead of opening the picker

Reuse the current picker model; do not add a second action-sheet framework.

- [ ] **Step 7: Wire the login action**

The login action should:

- append a system message like “Opening OpenAI sign-in…”
- always show the auth URL in the transcript for copy/paste
- switch input into redirect-paste mode immediately

Do not block on browser success.

- [ ] **Step 8: Implement redirect-paste mode**

While auth mode is active:

- the input placeholder should change to indicate redirect paste
- Enter should submit the redirect URL to the waiting auth flow
- Esc should cancel auth mode cleanly

On success:

- exit auth mode
- append a compact success message

On failure:

- remain usable
- append a clear error message

- [ ] **Step 9: Add explicit status and logout actions**

Wire the same auth action sheet to:

- show current OpenAI auth status in a system message
- sign out locally using the existing auth service

Cover auth precedence text for:

- env only
- OAuth only
- env plus stored OAuth
- signed out

- [ ] **Step 10: Add startup status refresh and live-session reload behavior**

When the embedded session is OpenAI-backed:

- load auth status on startup
- if signed out, append a one-time system message that points the user at `/openai`
- after successful login or logout, recreate the embedded client/session so live requests pick up the new auth state

Keep the TUI shell alive while refreshing the embedded runtime.

- [ ] **Step 11: Add focused failure-path tests**

Add tests for:

- browser-open failure still surfacing the URL
- invalid pasted redirect URL or state mismatch
- cancelling redirect-paste mode
- attempting login while login is already in progress
- logout when already signed out
- quitting the program while login is pending cancels the in-flight auth context

Keep these controller-focused and offline.

- [ ] **Step 12: Run the focused TUI auth tests**

Run:

```bash
go test ./cmd/serf-tui -run 'Test.*OpenAI.*'
```

Expected:

- PASS

- [ ] **Step 13: Commit the TUI auth controller**

```bash
git add cmd/serf-tui/openai_auth.go cmd/serf-tui/model.go cmd/serf-tui/input.go cmd/serf-tui/model_picker.go cmd/serf-tui/*_test.go
git commit -m "feat: add OpenAI login flow to serf-tui"
```

## Chunk 4: TUI Status And UX Polish

### Task 4: Surface OpenAI auth state cleanly

**Files:**
- Modify: `cmd/serf-tui/statusbar.go`
- Modify: `cmd/serf-tui/model.go`
- Modify: `cmd/serf-tui/message.go`
- Test: `cmd/serf-tui/*_test.go`

- [ ] **Step 1: Write the failing status rendering test**

Add tests for OpenAI-active sessions showing:

- signed-in OAuth state
- env-key state
- missing login state

The rendering should stay compact and not overwhelm the existing status bar.

- [ ] **Step 2: Add compact status-bar auth hints**

Update `statusbar.go` so when the active provider/model is OpenAI, the right side can include a compact auth hint such as:

- `oa: oauth`
- `oa: env`
- `oa: login`

Keep this subordinate to model/turn/context information.

- [ ] **Step 3: Add transcript-level auth feedback**

Use `message.go` and existing system-message patterns so auth operations append concise, human-readable feedback:

- login started
- URL ready
- login success
- login cancelled
- logout success
- status summary

- [ ] **Step 4: Verify the auth action sheet and input routing**

Confirm the chosen v1 UX model is implemented exactly:

- one OpenAI auth action sheet
- no separate modal stack
- redirect paste uses the main textarea/input path

Keep `model.go` responsible only for state transitions and command dispatch. Put auth orchestration in `openai_auth.go`, and put any picker reuse helpers in `model_picker.go` if needed so `model.go` does not grow substantially beyond its current size.

- [ ] **Step 5: Run targeted TUI UX tests**

Run:

```bash
go test ./cmd/serf-tui -run 'Test.*Status.*|Test.*OpenAI.*'
```

Expected:

- PASS

- [ ] **Step 6: Commit the UX polish**

```bash
git add cmd/serf-tui/statusbar.go cmd/serf-tui/model.go cmd/serf-tui/message.go cmd/serf-tui/*_test.go
git commit -m "feat: surface OpenAI auth status in serf-tui"
```

## Chunk 5: End-To-End Verification

### Task 5: Verify the integrated behavior

**Files:**
- Modify as needed based on failures from prior tasks
- Test: existing package suites

- [ ] **Step 1: Run touched-package tests**

Run:

```bash
go test ./internal/auth/openai ./llm ./llm/providers/openai ./cmd/serf ./cmd/serf-tui
```

Expected:

- PASS

- [ ] **Step 2: Build exact local binaries for manual verification**

Run:

```bash
go build -o ./serf-tui ./cmd/serf-tui
go build -o ./serf ./cmd/serf
```

Expected:

- both binaries are produced at repo root with no compile errors

- [ ] **Step 3: Manually exercise deterministic local TUI auth UX first**

Run:

```bash
./serf-tui --provider openai --model gpt-5.5
```

Verify:

- `/help` shows `/openai`
- `/openai` opens the auth action sheet
- signed-out OpenAI startup state is obvious before auth is triggered
- cancel paths work without crashing

- [ ] **Step 4: Manually exercise live local TUI login flow**

Verify:

- OpenAI login action is discoverable
- browser URL is shown and usable
- manual pasteback works
- success state updates in the TUI
- an OpenAI prompt can complete successfully after login

- [ ] **Step 5: Check for regressions in CLI auth**

Run:

```bash
./serf openai status
./serf openai login --help
./serf openai logout --help
```

Expected:

- CLI surface still behaves as before

- [ ] **Step 6: Final commit for integration fixes if needed**

```bash
git add <exact files changed during verification>
git commit -m "fix: finalize OpenAI TUI auth integration"
```

- [ ] **Step 7: Summarize outcomes and residual risks**

Document in the final handoff:

- that the stream root cause was protocol identity confusion between `item_id` and `call_id`
- that `serf-tui` now owns an in-app OpenAI login flow
- any remaining rough edges, such as future opportunities to share auth UI patterns across providers
