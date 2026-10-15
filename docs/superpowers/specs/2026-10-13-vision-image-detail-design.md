# Vision image detail and live catalog vision capability

Date: 2026-10-13
Status: implemented in the `vision-image-detail` lane, awaiting review
Issue: [#4489](https://github.com/prime-radiant-inc/evener/issues/4489)

## Problem

An image read through `read_file` reported *"vision is unavailable … because the
vision provider failed"* on a session whose model does have vision
(`lunaroute/deepseek-4.1-flash`, an `openai-chat` row; a direct request to the
provider read the test image's text and colors exactly). Two independent
defects:

1. **The vision side-channel pinned an invalid image detail.**
   `Session.describeImageCall` (`agent/session_tools.go`) built the
   user-message image with `Detail: "original"`, and the Chat Completions
   builder (`llm/providers/chatcompletions/messages.go`) wrote that value
   verbatim onto `image_url.detail`. The chat wire accepts only OpenAI's
   `auto|low|high`; a strict OpenAI-compatible endpoint rejects anything else
   with HTTP 400. Because the side-channel's part always named a detail, a
   row's `image_detail` cap never reached the chat wire at all.
2. **The live listing reader dropped a gateway's vision capability.** The
   openai-chat `/models` reader (`llm/providers/chatcompletions/models.go`)
   read image input only from OpenRouter's `architecture.input_modalities`.
   `lunaroute` advertises `capabilities.vision: true` instead, so the live
   layer contributed no image input and the model resolved as image-blind.

## Scope

- `agent/session_tools.go` — the side-channel's image part.
- `llm/image_detail.go` — the shared `ChatImageDetail` normalization, called by
  the builder and the token estimator.
- `llm/providers/chatcompletions/messages.go` — chat wire image detail.
- `llm/providers/chatcompletions/models.go` — live `/models` reading.
- `llm/token_count.go` — the estimator mirror.
- `docs/llm-providers.md` — the owning documentation.

## Non-goals

- No change to the Responses builder's code: it still sends the part's own
  detail, else the row's value (default `high`), and drops the field for
  `omit`; its tests pin that shape. Because the side-channel's part no longer
  names a detail, a Responses-route description now carries the builder's
  default `high` where it previously carried the literal `original`.
- No change to models.dev conversion, the curated overlay, or the user config
  schema.
- No change to *when* the side-channel runs: it is still skipped only for
  explorer agents, a configured `vision_model`, or a session chain that
  receives tool-result media natively.
- No new `image_detail` values.

## Behavior

- A chat image part's wire detail is the part's own value when it names one,
  otherwise the row's `image_detail`. `original` from either source maps to
  `high` (the chat wire accepts only `auto|low|high`); a row set to `omit`
  drops the field even when the part names a detail; an unset value drops it;
  `auto`, `low` and `high` pass through. One shared `llm.ChatImageDetail`
  produces this for both the builder and the token estimator, so the billed
  detail matches the sent one.
- The side-channel's image part names no detail, so the row cap (and each
  builder's own default) governs.
- The live listing maps `capabilities.vision: true` to
  `InputModalities = ["text", "image"]` when `architecture.input_modalities`
  is absent; `vision: false` and an absent `capabilities` object leave the
  modalities unset.

## Precedence and preservation

- Layer order is unchanged: live facts still never override a field the user
  layer set (`docs/llm-providers.md`, "Layers").
- A chat row that names its own part detail keeps it; only the literal
  `original` is normalized.
- Responses rows and non-vision chat rows keep their existing wire shape.

## Test plan

- Test-first unit tests (each watched red, then green):
  `TestS3Cov_DescribeImage_DoesNotPinDetail` (the side-channel's part names no
  detail), `TestToChatMessages_ImageDetailHonorsRowCap` and
  `TestToChatMessages_NeverEmitsOriginalImageDetail` (the chat detail mapping,
  including the #4489 invariant that no cap/part combination emits `original`),
  `TestListModelsReadsGatewayVisionCapability` (the live listing), and the
  estimator test that the billed detail matches the wire.
- Journey coverage. The request spans two layers: the side-channel builds the
  image part in `agent`, and the protocol builder serializes it in
  `llm/providers/chatcompletions`. No single test drives both, because a
  session test reaches the builder through the protocol adapter it replaces
  with a fake; each leg is covered by the tests above. End-to-end behavior is
  confirmed against the real gateway (manual, evidenced): the same image with
  `detail:"high"` returns HTTP 200 and a correct transcription, and with
  `detail:"original"` returns HTTP 400 naming `image_url.detail`.
- Package suites: `go test ./llm/...`; `go test ./agent/` alone with
  `-timeout 20m`.

## Risks and rejected alternatives

- Defaulting a chat part with no detail to `high` was rejected: it would
  change every chat image request and contradict "keep existing behavior for
  other rows".
- Normalizing only inside the side-channel was rejected: the cap would still
  never reach the chat wire.

## Record

- Base: `ea31304e2e` (`main`). Lane: `vision-image-detail`.
- Task-owned scratch: this session's `EVENER_SCRATCH_DIR`.
