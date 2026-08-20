# OpenAI-compatible tool-result image handling

**Status**: approved design — implementation pending.

**Date**: 2026-08-20

## Summary

Evener must not send tool-result image bytes to a generic OpenAI-compatible
provider. These providers use the Chat Completions wire format, whose
`role: "tool"` messages carry text content and do not define OpenAI's
Responses API `input_image` content for function outputs.

The session will keep tool-result image bytes in durable history and the
transcript/UI. The OpenAI-compatible adapter will preserve those bytes when it
tries a Responses endpoint, and remove them only from the Chat Completions
request copy where the wire format cannot represent them. The existing vision
side-call will continue to turn those bytes into textual steering when the
configured model can analyze images.

The exported strict Chat Completions body builder will continue rejecting a
direct image-bearing tool-result request. The adapter's endpoint dispatcher
owns the safe conversion for its own Chat path, so adaptive Responses requests
are not degraded and Chat fallback requests do not fail before dispatch.

## Problem

A tool such as `read_file` can return text and image bytes together. The
session persists both in the tool-result content part. On the next model
request, the OpenAI-compatible adapter sees the image bytes and returns:

```text
configuration error: openai-compatible chat completions does not support tool-result images
```

The adapter is correct on its Chat Completions path: its serializer can emit
only the textual part of a `role: "tool"` message. The session is incorrect
only when that request reaches the Chat path without endpoint-aware handling,
even though it already has a vision side-channel and durable image
persistence.

OpenAI's first-party Responses path is different. It can encode a
`function_call_output` whose `output` contains `input_text` and `input_image`
parts. The generic adapter can use that format when its adaptive Responses
attempt succeeds, but must sanitize a copy before falling back to Chat
Completions. The same Chat-only sanitization applies to non-adaptive
OpenAI-compatible instances.

The policy is based on the actual request builder path, not a single behavior
tag. `openai-compatible`, `kimi`, `glm`, `zai`, `deepseek`, `together`,
`ollama`, and `openrouter` profiles share the compatibility-family behavior,
but some retain provider-specific behavior tags.

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
4. `buildModelRequest` assembles the next request from history, retaining the
   complete tool-result image.
5. The OpenAI-compatible adapter selects its endpoint. Responses receives the
   original request; Chat receives a sanitized copy because its `role: "tool"`
   representation is text-only.

### Provider behavior matrix

| Request path | Tool result has image | Outbound behavior |
| --- | --- | --- |
| Non-adaptive compat adapter / Chat Completions | Yes | Sanitize only the dispatched copy; send text-only `role: "tool"` |
| Adaptive compat adapter / Responses succeeds | Yes | Preserve image as `input_image` inside `function_call_output.output` |
| Adaptive compat adapter / Responses falls back to Chat | Yes | Sanitize only the fallback copy; send text-only `role: "tool"` |
| Any compat-family adapter without image | No | Existing behavior |
| First-party `openai` / Responses | Yes | Existing `function_call_output` with `input_text` + `input_image` |
| First-party OpenAI Chat fallback | Yes | Existing explicit unsupported-image behavior |
| Anthropic or supported Google model | Yes | Existing provider-specific multimodal behavior |
| Direct `ChatCompletionsBody` call with image bytes | Yes | Existing explicit `ConfigurationError` |

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

- `llm/providers/openaicompat/request.go` or a focused neighboring file — add
  the pure request-copy sanitizer used only by the adapter's Chat dispatch
  path.
- `llm/providers/openaicompat/adapter.go` — sanitize non-adaptive Chat calls
  and adaptive Chat fallback calls, while leaving adaptive Responses attempts
  untouched.
- `llm/providers/openaicompat/adapter_test.go` and/or focused request tests —
  cover the endpoint split, copy-on-write behavior, and direct strict body
  validation.

No change is expected in:

- `agent/session_model_call.go` — the session request remains complete.
- Existing OpenAI Responses image serialization.
- Tool-result persistence or frontend image projection.
- The strict `ChatCompletionsBody`/`buildRequestBody` rejection guard.

## Testing

The regression tests must fail before the implementation because the Chat path
still receives image bytes and the adapter rejects them. The tests will then
verify:

1. A non-adaptive Chat `Complete` request dispatches the tool's text and omits
   image fields from the wire body.
2. A non-adaptive Chat `Stream` request has the same behavior.
3. An adaptive Responses request sends the original image as
   `function_call_output.output` content when Responses succeeds.
4. An adaptive Responses failure eligible for fallback sends a sanitized
   text-only Chat request.
5. The input request remains unchanged after sanitization.
6. The strict `ChatCompletionsBody`/`buildRequestBody` helper still returns
   `ConfigurationError` for a direct image-bearing request.
7. Configured compat-family providers (`openai-compatible`, `kimi`, `glm`,
   `zai`, `deepseek`, `together`, `ollama`, and `openrouter`) are covered by
   the adapter-path tests or a table-driven sanitizer test, so behavior tags do
   not accidentally become the policy boundary.

Run the focused provider tests first. Then run the applicable module gate and
inspect the final diff for unrelated changes. All tests must remain
deterministic and offline.

## Alternatives rejected

### Encode the image as a Chat Completions tool-message content array

Rejected because generic Chat Completions servers do not share a standard
image-bearing tool-result schema. Sending `input_image` or `image_url` there
would be a provider-specific guess and could produce a 400 or silently
misinterpreted content.

### Sanitize in the session using one behavior tag

Rejected because the behavior tag does not uniquely identify the request path.
Several providers share the compatibility-family serializer under their own
tags, and the environment-created compatibility adapter may try Responses
before Chat. The adapter is the only layer that knows which endpoint is being
used for the current attempt.

### Remove the adapter rejection everywhere and silently drop the image

Rejected because the exported strict Chat body builder should still signal an
unsupported direct representation. The adapter's own dispatch path may make a
deliberate copy for Chat, but callers that explicitly ask the body builder to
serialize an image-bearing tool result must not lose data without an error.

### Move the image into a synthetic user message

Rejected because it changes conversation roles and ordering and would require a
new provider-independent history transformation. The existing vision
side-call already supplies a textual user-facing representation, while the
Responses path can carry the image in its native function-output content.

### Add generic provider capability negotiation

Deferred. The adapter's endpoint choice is sufficient for this fix. A
capability field could support known vendor extensions later, but generic
compatibility should not guess a non-standard tool-message image schema.

## Acceptance criteria

- A non-adaptive OpenAI-compatible Chat session can continue after an
  image-bearing tool result instead of failing with the configuration error
  above.
- An adaptive compatibility adapter preserves the image when its Responses
  attempt succeeds.
- An adaptive compatibility adapter sanitizes only the Chat fallback copy when
  Responses is unavailable or rejects the request in a fallback-eligible way.
- The Chat wire body contains the tool result's text and no unsupported image
  fields.
- The original request, durable history, and user-facing image projections
  remain unchanged.
- First-party OpenAI Responses behavior remains unchanged.
- The strict direct Chat body builder still fails explicitly for an
  image-bearing tool result.
- Focused tests and the relevant deterministic package gates pass.
