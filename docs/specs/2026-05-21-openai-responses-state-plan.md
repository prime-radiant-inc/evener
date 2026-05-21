# OpenAI Responses State Round-Trip Plan

Date: 2026-05-21
Branch: `codex-backend-parity`
Worktree: `/tmp/serf-codex-backend-parity`

## Context

Serf now sends Codex-shaped session metadata to the ChatGPT/Codex Responses backend:

- `prompt_cache_key`
- install metadata in `client_metadata`
- `session-id`, `thread-id`, and `x-client-request-id`
- default `originator` and `User-Agent`
- `include: ["reasoning.encrypted_content"]` when reasoning is enabled

The remaining correctness gap is that requesting encrypted reasoning is not enough. OpenAI returns encrypted reasoning as response output state, and stateless clients must pass it back in later Responses API requests. Serf currently ignores `type: "reasoning"` response items, so the encrypted state is dropped before the next turn.

## Goals

- Round-trip OpenAI `reasoning.encrypted_content` through Serf history without displaying it as user-visible reasoning text.
- Expose easy Responses API fields in `llm.Request` and high-level generation options:
  - `previous_response_id`
  - `conversation`
  - `service_tier`
  - `safety_identifier`
  - `prompt_cache_retention`
  - `truncation`
  - `max_tool_calls`
  - `background`
  - explicit `store`
- Keep Serf agent sessions stateless by default. Do not enable `previous_response_id` or `conversation` automatically.
- Preserve current default `store:false` unless a caller explicitly sets `Store`.
- Keep Chat Completions fallback tolerant by ignoring Responses-only state there.

## Non-Goals

- No switch to server-side conversation state by default.
- No migration of historical transcripts beyond naturally preserving the new fields in existing JSON structures.
- No provider-neutral abstraction for every OpenAI Responses field.
- No UI display of encrypted reasoning.
- No attempt to decrypt, inspect, summarize, or redact encrypted reasoning.

## Design

### Core request fields

Extend `llm.Request` and `llm.GenerateOptions`:

```go
PreviousResponseID   string
ConversationID       string
ServiceTier          string
SafetyIdentifier     string
PromptCacheRetention string
Truncation           string
MaxToolCalls         *int
Background           *bool
Store                *bool
```

`ConversationID` is intentionally string-only for the first pass. If a future caller needs the object shape, it can use `ProviderOptions["openai"]` or a small typed struct later.

### Reasoning state representation

Extend `llm.ThinkingData`:

```go
EncryptedContent string `json:"encrypted_content,omitempty"`
```

`Response.ReasoningText()` remains unchanged and only returns visible `ThinkingData.Text`.

### OpenAI response parsing

In `fromResponses`, parse output items:

```json
{"type":"reasoning","encrypted_content":"..."}
```

Append:

```go
llm.ContentPart{
    Kind: llm.ContentThinking,
    Thinking: &llm.ThinkingData{EncryptedContent: enc},
}
```

If summary text is present in a known OpenAI summary shape, preserve it as visible thinking text only when non-empty. Encrypted-only items must not affect `ReasoningText()`.

### OpenAI request serialization

In `toResponsesInput`, serialize assistant `ContentThinking` parts that contain `EncryptedContent` as top-level Responses input items:

```json
{"type":"reasoning","encrypted_content":"..."}
```

Do not serialize encrypted thinking into assistant `message.content`.

Ordering:

- Preserve the order in `Message.Content` as much as the current serializer permits.
- It is acceptable to emit assistant message text groups separately and then top-level reasoning/tool items in message content order.
- Tool calls and tool outputs must keep their current behavior.

### Responses field serialization

In `buildRequestBody`, serialize non-empty request fields:

- `previous_response_id`
- `conversation`
- `service_tier`
- `safety_identifier`
- `prompt_cache_retention`
- `truncation`
- `max_tool_calls`
- `background`
- `store`

`Store` uses pointer semantics:

- nil: current adapter default (`store:false`)
- true: emit `store:true`
- false: emit `store:false`

### Session behavior

The existing session history append path already appends `resp.Message` after model responses. Once `fromResponses` preserves encrypted reasoning in the assistant message, normal Serf history and transcript persistence should carry it forward.

Agent sessions should not set `PreviousResponseID` or `ConversationID` by default. Those fields are explicit controls for callers that choose server-side state.

## Implementation Tasks

### Task 1: Core fields

Files:

- `llm/types.go`
- `llm/generate.go`
- `llm/stream_generate.go`

Steps:

- Add request/generation fields listed above.
- Pass them through both `Generate` and `StreamGenerate`.
- Keep zero values omitted.

Tests:

- Existing compile coverage is enough for pass-through.
- Add direct adapter tests in Task 3 for body shape.

### Task 2: Encrypted reasoning round-trip

Files:

- `llm/types.go`
- `llm/providers/openai/adapter.go`
- `llm/providers/openai/adapter_test.go`

Steps:

- Add `ThinkingData.EncryptedContent`.
- Parse `reasoning.encrypted_content` from Responses output.
- Serialize encrypted thinking back to Responses input.
- Ensure `ReasoningText()` remains empty for encrypted-only items.

Tests:

- `fromResponses` preserves encrypted reasoning.
- `toResponsesInput` emits a top-level reasoning item.
- `ReasoningText()` ignores encrypted-only reasoning.

### Task 3: Easy Responses controls

Files:

- `llm/providers/openai/adapter.go`
- `llm/providers/openai/adapter_test.go`

Steps:

- Serialize the easy request fields in `buildRequestBody`.
- Preserve default `store:false`.
- Allow explicit `Store` to override default.

Tests:

- Body contains all set fields with expected names.
- Explicit `Store:true` emits `store:true`.
- Nil `Store` still emits `store:false`.

### Task 4: Session integration guard

Files:

- `agent/session_dod_test.go` or a narrower session test

Steps:

- Verify assistant encrypted reasoning captured in a model response is present in the next model request history.
- Use the fake adapter to return an assistant message with encrypted thinking and a tool call, then inspect the second request.

Tests:

- Second request includes the prior assistant `ContentThinking.EncryptedContent`.

### Task 5: Verification

Run focused tests:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'EncryptedReasoning|ResponsesControls|MapsToResponsesAPI|OAuthTransport' -count=1
GOCACHE=/tmp/serf-gocache go test ./agent -run 'EncryptedReasoning|PopulatesModelRequestMetadata|ReasoningEffort' -count=1
GOCACHE=/tmp/serf-gocache go test ./llm -run 'Generate|StreamGenerate|ReasoningText' -count=1
git diff --check
```

Broader package tests may hit live integration credentials or existing naming-call assumptions; report those separately if they fail for unrelated reasons.

## Subagent Split

- Worker A owns llm core fields and OpenAI request body serialization.
- Worker B owns encrypted reasoning parse/serialize tests and implementation.
- Main agent owns session integration test, merge review, focused verification, and commit.

