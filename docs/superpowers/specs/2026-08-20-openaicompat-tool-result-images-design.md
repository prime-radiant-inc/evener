# OpenAI-compatible tool-result image handling

**Status**: approved design — implementation pending.

**Date**: 2026-08-20

## Summary

Evener must not send tool-result image bytes to a generic OpenAI-compatible
provider. These providers use the Chat Completions wire format, whose
`role: "tool"` messages carry text content and do not define OpenAI's
Responses API `input_image` content for function outputs.

The session will keep tool-result image bytes in durable history and the
transcript/UI, while removing them from the outbound request copy used by an
OpenAI-compatible model. The existing vision side-call will continue to turn
those bytes into textual steering when the configured model can analyze images.

The low-level OpenAI-compatible adapter will continue rejecting a direct
image-bearing tool-result request. That guard protects callers that bypass the
session and prevents silent data loss at the adapter boundary.

## Problem

A tool such as `read_file` can return text and image bytes together. The
session persists both in the tool-result content part. On the next model
request, the OpenAI-compatible adapter sees the image bytes and returns:

```text
configuration error: openai-compatible chat completions does not support tool-result images
```

The adapter is correct: its Chat Completions serializer can emit only the
textual part of a `role: "tool"` message. The session is incorrect because it
passes a provider-incompatible representation to that adapter even though it
already has a vision side-channel and durable image persistence.

OpenAI's first-party Responses path is different. It can encode a
`function_call_output` whose `output` contains `input_text` and `input_image`
parts. That behavior belongs to the Responses adapter and must not be assumed
for arbitrary OpenAI-compatible Chat Completions servers.

## Goals

- Keep ordinary OpenAI-compatible tool calls working after a tool returns an
  image.
- Preserve the tool's text output in the model request.
- Preserve the original image bytes and media type in durable history,
  transcript storage, and UI projections.
- Continue using the existing vision side-call and textual steering path.
- Keep the adapter-level rejection for direct, unsupported requests.
- Avoid adding a provider-specific wire extension that generic servers may not
  understand.

## Non-goals

- Do not change the OpenAI Responses wire format.
- Do not change Anthropic, Google, or other provider serialization.
- Do not claim that a generic OpenAI-compatible server received or understood
  the original image bytes.
- Do not add a new configuration flag or provider capability negotiation in
  this fix.
- Do not alter transcript retention, image rendering, or image persistence.

## Current flow

1. A tool returns `ExecResult.ImageData` and `ImageMediaType`.
2. `agent.Session.persistToolResults` stores those fields on the
   `llm.ToolResultData` history part.
3. The session calls `describeImage`, which sends the image as ordinary user
   multimodal input and injects a textual description as steering when the
   side-call succeeds.
4. `buildModelRequest` assembles the next request from history.
5. `llm/providers/openaicompat` serializes a tool result as a Chat Completions
   `role: "tool"` message. Its text-only representation cannot carry the
   stored image bytes, so `buildRequestBody` rejects the request rather than
   dropping them silently.

The fix belongs between steps 4 and 5. It must affect only the request copy,
not the durable history from which that copy was assembled.

## Design

### Request-only sanitization

When `buildModelRequest` receives a profile whose `BehaviorTag()` is
`"openai-compatible"`, it will make a copy of the assembled messages and
remove `ToolResult.ImageData` and `ToolResult.ImageMediaType` from each
image-bearing tool-result part in that copy.

The helper should be pure from the caller's perspective:

- No input `llm.Message`, `ContentPart`, or `ToolResultData` is mutated.
- Messages and content parts without image-bearing tool results are reused or
  copied consistently with existing session conventions.
- The returned request retains the tool call ID, tool name, text/content,
  error state, timing, and tool state.
- The original history continues to contain the image bytes and media type.

The sanitization runs after `SystemPromptAsUser` message arrangement and before
`llm.Request` is returned. This keeps all request construction paths covered,
including system-prompt-as-user mode, while leaving transcript history intact.

### Provider behavior matrix

| Request path | Tool result has image | Outbound behavior |
| --- | --- | --- |
| `openai-compatible` / Chat Completions | Yes | Send text-only `role: "tool"`; retain image in history/UI |
| `openai-compatible` / Chat Completions | No | Existing behavior |
| First-party `openai` / Responses | Yes | Existing `function_call_output` with `input_text` + `input_image` |
| Anthropic or supported Google model | Yes | Existing provider-specific multimodal behavior |
| Any direct call to `openaicompat.Adapter` with image bytes | Yes | Existing explicit `ConfigurationError` |

### Vision fallback

No new side-call is required. `persistToolResults` already invokes
`describeImage` and injects a textual description as steering. If that call
fails, its existing warning behavior remains; the main OpenAI-compatible model
request still proceeds with the tool's textual output and without the invalid
image fields.

This makes the loss boundary explicit: the generic Chat Completions model does
not receive the original image, but Evener does not lose the image from its
transcript or UI, and it uses the best available textual representation when
vision is available.

## Files

Expected implementation and test changes:

- `agent/session_model_call.go` — apply request-only sanitization while
  assembling requests.
- An agent session test file near the request-assembly tests — cover the
  compatible and non-compatible paths and prove the source history is not
  mutated.

No change is expected in:

- `llm/providers/openaicompat/request.go` — retain the adapter guard.
- Existing OpenAI Responses image serialization.
- Tool-result persistence or frontend image projection.

## Testing

The regression test must fail before the implementation because the generated
OpenAI-compatible request still contains image bytes and the adapter rejects
it. The test will then verify:

1. An OpenAI-compatible profile produces a request whose tool result retains
   text but has empty image fields.
2. The original history passed to request assembly still has the image bytes and
   media type.
3. A non-compatible profile does not undergo this sanitization.
4. Existing adapter tests continue to prove that a direct image-bearing
   OpenAI-compatible adapter request returns `ConfigurationError`.

Run the focused agent and provider tests first. Then run the applicable module
gate and inspect the final diff for unrelated changes. All tests must remain
deterministic and offline.

## Alternatives rejected

### Encode the image as a Chat Completions tool-message content array

Rejected because generic Chat Completions servers do not share a standard
image-bearing tool-result schema. Sending `input_image` or `image_url` there
would be a provider-specific guess and could produce a 400 or silently
misinterpreted content.

### Remove the adapter rejection and silently drop the image

Rejected because direct adapter callers would lose data without an explicit
signal. The session has enough context to make the loss boundary deliberate,
while the adapter should continue validating its own wire contract.

### Move the image into a synthetic user message

Rejected for this fix because it changes conversation roles and ordering and
would require a new provider-independent history transformation. The existing
vision side-call already supplies a textual user-facing representation without
altering the main conversation protocol.

### Add generic provider capability negotiation

Deferred. A capability field could support known vendor extensions later, but
this incident only requires the safe default for generic Chat Completions.

## Acceptance criteria

- A session using an arbitrary OpenAI-compatible Chat Completions server can
  continue after an image-bearing tool result instead of failing with the
  configuration error above.
- The main request contains the tool result's text and no unsupported image
  fields.
- Durable history and user-facing image projections remain unchanged.
- First-party OpenAI Responses behavior remains unchanged.
- Direct low-level OpenAI-compatible requests still fail explicitly when they
  contain tool-result image bytes.
- Focused tests and the relevant deterministic package gates pass.
