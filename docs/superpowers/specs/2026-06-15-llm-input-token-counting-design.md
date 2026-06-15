# LLM Input Token Counting Design

## Purpose

Move Serf's input-token estimation into `llm/` so every caller uses the same request-aware logic. The implementation must keep normal context-pressure checks deterministic, while exposing exact provider counters for callers that explicitly want a network preflight.

## Requirements

- Provide a pure local estimator for `llm.Request` and `[]llm.Message`.
- Never count inline media bytes or base64 JSON as text tokens.
- Use provider/model-aware media formulas where they are stable and documented.
- Use bounded fallback media estimates when provider rules are unknown or image dimensions are unavailable.
- Add an adapter-level exact counter interface and implement it for Anthropic and Google/Gemini.
- Keep exact counters optional. Missing adapter support returns a local estimate instead of forcing every provider into one abstraction.
- Keep dependency direction clean: `agent` may import `llm`; `llm` must not import `agent`.

## Provider Basis

- Google Gemini exposes `models.countTokens` and documents that the request can include either `contents` or a full `generateContentRequest`.
- Anthropic exposes a Messages count-tokens API that accepts the same message-shaped request payload and returns `input_tokens`.
- Local formulas are best-effort because providers differ. Exact provider responses and normal post-call usage remain authoritative.

## Architecture

`llm/token_count.go` owns the public API:

- `type InputTokenCount` records token count, provider, model, source, exactness, and optional raw provider data.
- `type InputTokenCounter` is the optional adapter interface implemented by providers that support exact preflight counting.
- `func EstimateInputTokens(req Request) InputTokenCount` performs a pure local estimate.
- `func EstimateMessagesInputTokens(messages []Message) InputTokenCount` supports callers that only have messages.
- `func (c *Client) CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error)` resolves the provider like `Complete`, calls the adapter exact counter when present, and otherwise returns the local estimate.

Provider adapters stay isolated:

- Anthropic adds `CountInputTokens` next to `Complete`/`Stream`, reusing `buildRequestBody` and posting to `/v1/messages/count_tokens`.
- Google adds `CountInputTokens`, reusing `toGeminiContents` and `buildRequestBody`, then posting `{"generateContentRequest": <body>}` to `:countTokens`.

Agent context management should use the local estimator, not exact network counters, because warnings and compaction pressure run frequently and must not add latency or fail due to network/auth issues.

## Local Estimation Rules

- Text: `len(text) / 4`.
- Tool calls/results/thinking: preserve existing char-count behavior.
- Request tools: JSON-marshal tool definitions and count chars/4.
- Images:
  - Decode inline or local-file dimensions with `image.DecodeConfig` when possible.
  - Google/Gemini local estimate: `258 * tiles`, where tiles are `1` for images <=384x384 and otherwise `ceil(width/768) * ceil(height/768)`.
  - Anthropic local estimate: `ceil(width/28) * ceil(height/28)` when dimensions are known.
  - OpenAI local estimate: implement documented low-detail and high-detail tile behavior when possible; otherwise bounded fallback.
  - Unknown dimensions or unsupported providers: bounded media placeholder.
- Audio/documents: bounded placeholder unless exact provider counters are used.

## Error Handling

- Exact counter HTTP failures use the same provider error classification helpers as generation paths.
- `Client.CountInputTokens` returns adapter exact-counter errors. Callers that want fallback can call `EstimateInputTokens` themselves after an error.
- If the target adapter lacks `InputTokenCounter`, `Client.CountInputTokens` returns a local estimate with `Exact=false`.

## Testing

- Unit tests for local image estimates verify raw byte length does not affect counts.
- Unit tests cover provider-specific local media estimates for Anthropic and Google.
- Client tests verify exact-counter routing and local fallback when an adapter does not implement the interface.
- Anthropic and Google adapter tests verify endpoint paths, request shape reuse, returned token parsing, raw metadata, and HTTP error handling.
- Agent tests verify context-window warnings still do not fire for large image bytes.
