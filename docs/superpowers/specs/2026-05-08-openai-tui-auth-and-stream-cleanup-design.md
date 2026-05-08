# OpenAI TUI Auth And Stream Cleanup Design

## Summary

Serf already has working OpenAI OAuth login in the CLI and working OpenAI model execution in the common runtime, but the current OpenAI Responses streaming adapter needed defensive aliasing after a real protocol bug, and `serf-tui` has no first-class OpenAI login UX. This design cleans up the stream tool-call state machine and adds a TUI-native OpenAI login/logout/status flow that reuses the existing `internal/auth/openai` service instead of shelling out to the CLI.

The scope is intentionally narrow:

- make the OpenAI streamed tool-call state machine explicit and canonical
- preserve the existing CLI auth commands and storage format
- add clean `serf-tui` login UX for OpenAI-backed sessions
- avoid adding a generic multi-provider TUI auth framework

## Problem Statement

Two independent issues exist:

1. The OpenAI Responses SSE adapter currently works, but the internal implementation is harder to reason about than it should be. The backend uses both `item_id` and `call_id` for the same logical function call. A prior bug treated them as separate calls, which produced phantom tool calls with empty names and invalid replay input. The live fix is correct, but the logic should be made clearer and harder to regress.
2. `serf-tui` requires the user to leave the TUI and run `serf openai login` in the shell. That is functional but poor UX for an interactive client that already owns the session state dir, status bar, and event loop.

## Goals

- Model OpenAI streamed tool calls with one canonical in-memory state object per logical call.
- Keep `item_id` as an internal stream alias only; never let it leak as a user-visible tool-call id.
- Make final tool-call identity, name, and arguments come from authoritative end-of-call events.
- Add TUI-native OpenAI login, logout, and status entry points.
- Support both browser-open and manual pasteback from inside the TUI.
- Surface auth state clearly when the active provider is OpenAI.

## Non-Goals

- No new auth provider abstraction for Anthropic, Google, or others.
- No OS keychain integration in this change.
- No deep redesign of the embedded server or session lifecycle.
- No attempt to remove the existing CLI OpenAI auth commands.

## Approaches Considered

### Approach A: Keep the current inline alias logic and only add TUI login

This is the smallest immediate change. It avoids touching the now-working stream adapter and focuses only on the TUI.

Trade-offs:

- lowest implementation cost
- leaves a subtle protocol state machine in a long `switch`
- future regressions would still be easy to introduce

This is not recommended because the stream root cause is now understood well enough to encode cleanly.

### Approach B: Extract a small OpenAI stream tool-call tracker and reuse existing auth service in the TUI

Add a focused helper inside the OpenAI adapter that owns:

- canonical tool-call state keyed by `call_id`
- alias lookup from `item_id`
- fragment accumulation
- authoritative finalize behavior

Then add a small TUI auth controller that wraps `internal/auth/openai.Service` for login, logout, and status.

Trade-offs:

- slightly more code than patching inline
- much clearer ownership and invariants
- easiest to test against the real SSE shape we observed

This is the recommended approach.

### Approach C: Build a generic TUI auth framework first

Create provider-agnostic TUI auth menus, status models, and pluggable login controllers before wiring OpenAI.

Trade-offs:

- architecturally broad
- slower and riskier
- violates YAGNI for the current problem

This is not recommended.

## Recommended Design

Use Approach B.

### 1. OpenAI stream tool-call tracker

Introduce a small helper local to the OpenAI adapter, either as a private type in `llm/providers/openai/adapter.go` or a focused sibling file in the same package.

Responsibilities:

- register a new function-call item when `response.output_item.added` arrives
- maintain `item_id -> state` aliasing for incremental events
- expose canonical id as `call_id`
- append argument fragments exactly as received
- replace accumulated arguments with authoritative final arguments when `response.function_call_arguments.done` or `response.output_item.done` provides them
- emit exactly one logical tool call to the stream consumer

Invariants:

- there is one logical tool-call state per backend function call
- `item_id` is never emitted as the tool-call id if `call_id` exists
- `ToolCallEnd` is authoritative for name and arguments
- finish events without assistant content must not overwrite accumulated tool-call content

### 2. Stream accumulator semantics

Keep the generic accumulator provider-agnostic, but preserve two explicit rules:

- `ToolCallDelta` appends fragments
- `ToolCallEnd` can fill missing name/type and replace arguments with the authoritative final payload

This keeps the adapter responsible for protocol translation and the accumulator responsible for assembly semantics.

### 3. TUI OpenAI auth controller

Add a TUI-specific auth surface that wraps the existing OpenAI auth service.

Responsibilities:

- inspect current OpenAI auth status using the TUI’s resolved `stateDir`
- start login with browser-open and manual paste fallback
- persist the resulting auth using the existing storage path
- clear auth on logout
- report success/failure back into the TUI message stream

The TUI must not shell out to `serf openai login`. It should call `internal/auth/openai.Service` directly with TUI-provided browser and manual-input hooks.

### 4. Clean UX in `serf-tui`

User-visible behavior:

- if provider is OpenAI and no API key or OAuth session is available, the TUI should make the missing auth obvious instead of failing opaquely
- add an explicit OpenAI auth entry point from the TUI keyboard flow
- show OpenAI auth state in a compact way when OpenAI is active
- keep the happy path fast:
  - start login
  - print/open the URL
  - show a paste field or modal for the redirect URL
  - confirm signed-in email on success

Recommended UX shape:

- keybinding to open an OpenAI auth menu or action sheet
- actions:
  - `Sign in with OpenAI`
  - `Sign out of OpenAI`
  - `Show OpenAI auth status`
- login flow:
  - append a system message explaining that the browser will open and that the URL is also available for remote/manual flow
  - display the URL in the transcript for copy/paste
  - transition input into a temporary “Paste redirect URL” mode
  - on success, append a system confirmation with email/account summary

This keeps the UX consistent with the rest of the TUI: transcript-driven, keyboard-first, no extra windows beyond the optional browser open.

## Architecture

### Files likely affected

- `llm/providers/openai/adapter.go`
  - extract or simplify stream tool-call state handling
- `llm/providers/openai/adapter_test.go`
  - cover real `item_id`/`call_id` sequences and authoritative finalize behavior
- `llm/stream_accumulator.go`
  - keep end-of-call overwrite semantics explicit
- `llm/stream_accumulator_test.go`
  - cover provider-independent end-of-call replacement semantics
- `cmd/serf-tui/model.go`
  - add auth UI state and key handling
- `cmd/serf-tui/main.go`
  - wire new commands or startup status checks if needed
- `cmd/serf-tui/statusbar.go`
  - surface compact OpenAI auth status when relevant
- `cmd/serf-tui/message.go`
  - reuse or extend transcript/system message rendering for auth prompts
- `cmd/serf-tui/input.go`
  - add temporary redirect-paste mode if that code owns input behavior
- `cmd/serf-tui/*_test.go`
  - add focused tests for key flow, auth mode transitions, and status rendering

If the TUI auth orchestration grows beyond a few methods, create a small focused file such as `cmd/serf-tui/openai_auth.go` rather than bloating `model.go`.

## Data Flow

### Stream cleanup

1. OpenAI SSE event arrives.
2. Adapter passes function-call events through a tracker.
3. Tracker resolves `item_id` to canonical call state.
4. Tracker emits stream events using canonical `call_id`.
5. Accumulator assembles fragments and authoritative end data into one tool call.
6. Subsequent history serialization sends exactly one valid `function_call` item back to OpenAI.

### TUI auth flow

1. User triggers OpenAI login action in `serf-tui`.
2. TUI creates `authopenai.Service` with:
   - browser opener callback that both opens and surfaces the URL in the transcript
   - manual redirect reader bound to a temporary paste-input mode
3. Service runs the existing callback + manual race.
4. TUI receives success/failure and appends a system message.
5. Future OpenAI model requests use the same stored auth via `llm.NewFromEnv(llm.WithStateDir(...))`.

## Error Handling

### Stream cleanup

- unknown function-call event without resolvable id should remain a provider event, not a fake tool call
- end events should reconcile earlier partial state, not create duplicates
- malformed final arguments should fail at schema/use sites, but the adapter should preserve the provider payload faithfully

### TUI auth

- browser-open failure is non-fatal; still show the URL and continue
- callback timeout or inability to reach localhost should not abort manual pasteback
- invalid pasted URL or state mismatch should produce a clear transcript error and keep the TUI usable
- logout should be idempotent
- auth status should clearly distinguish:
  - env API key
  - stored OAuth session
  - signed out

## Testing Strategy

### Stream cleanup tests

- `output_item.added` + delta fragments keyed by `item_id` + final done keyed by `call_id` becomes one tool call
- no phantom `item_id` tool call is emitted
- authoritative final arguments replace fragment assembly
- finish with empty provider `output` still preserves streamed tool calls

### TUI auth tests

- login action enters auth mode and surfaces URL
- manual pasteback path completes and appends success message
- invalid pasteback appends error and leaves TUI responsive
- logout action clears stored auth and updates status
- status bar changes when OpenAI auth is signed in or missing

## Rollout Notes

- Preserve current CLI behavior and tests.
- Land the stream cleanup first or in the same change, because the TUI flow depends on reliable OpenAI tool-call handling during real sessions.
- Keep docs minimal; user-facing CLI/TUI auth usage can be summarized in README only if needed by existing repo norms.
