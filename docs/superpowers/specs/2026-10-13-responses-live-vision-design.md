# Responses-protocol `/models` vision capability

Date: 2026-10-13
Issue: [#4508](https://github.com/prime-radiant-inc/evener/issues/4508)
Status: awaiting implementation

## Problem

The openai-chat live `/models` reader maps a gateway's `capabilities.vision`
object to image input (#4489). The openai-responses reader does not:
`openAIModelListEntry` (`llm/providers/responses/models.go`) parses no
capability object, so a gateway that `evener providers probe` records as
`openai-responses` resolves its models image-blind — the same failure #4489
fixed for chat.

`grep -n "capabilities\|Vision" llm/providers/responses/models.go` finds
nothing, and a `{"data":[{"id":"x","capabilities":{"vision":true}}]}` listing
yields no image input.

## Scope

- `llm/gateway_capabilities.go` (new): the shared capability object and its
  modality mapping.
- `llm/providers/responses/models.go`: read it in the platform entry.
- `llm/providers/chatcompletions/models.go`: reuse the shared type (no
  behavior change).
- `docs/llm-providers.md`: the live-layer statement already names
  `capabilities.vision`; keep it true for both readers.

## Non-goals

- No change to the Codex entry: the Codex backend states `input_modalities`
  directly.
- No change to layer precedence or to which listing shape a protocol parses.
- No other capability keys (tools, reasoning) are read from the object; only
  vision affects resolution today.

## Behavior

- A platform `/models` row advertising `capabilities.vision: true` yields
  `InputModalities = ["text", "image"]`; `vision: false` or an absent object
  yields none.
- The chat reader's behavior is unchanged: `architecture.input_modalities`
  wins when present, else `capabilities.vision`.

## Precedence and preservation

- The live layer still never overrides a field the user layer set.
- Both readers map through one shared type, so they cannot drift.

## Test plan

- Red first: `TestListModelsPlatformVisionCapability` in
  `llm/providers/responses/models_test.go`.
- The chat reader's `TestListModelsReadsGatewayVisionCapability` stays green
  through the shared-type refactor.
- `go test ./llm/...`.
