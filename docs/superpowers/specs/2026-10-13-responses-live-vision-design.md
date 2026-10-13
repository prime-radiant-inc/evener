# Responses-protocol `/models` vision and modality facts

Date: 2026-10-13
Issue: [#4508](https://github.com/prime-radiant-inc/evener/issues/4508)
Status: implemented; both review rounds applied

## Problem

The openai-chat live `/models` reader maps a gateway's advertised modality facts
to image input (#4489). The openai-responses reader maps neither: its platform
entry, `openAIModelListEntry` (`llm/providers/responses/models.go`), parses no
`architecture` and no `capabilities` object, so a gateway that `evener providers
probe` records as `openai-responses` resolves its models image-blind. That also
makes the live-layer statement in `docs/llm-providers.md` ("`InputModalities`
comes from OpenRouter's `architecture.input_modalities`, or … `capabilities.vision`")
false for the Responses reader.

Evidence: `grep -n "capabilities\|Vision\|input_modalities"
llm/providers/responses/models.go` finds only the Codex entry's
`input_modalities`; a `{"data":[{"id":"x","capabilities":{"vision":true}}]}`
listing yields no image input.

## Scope

- `llm/providers/internal/requestutil/gateway.go` (new): `GatewayArchitecture`,
  `GatewayCapabilities`, and `GatewayInputModalities`, which holds the one
  precedence rule. `requestutil` is the seam both readers already import.
- `llm/providers/responses/models.go`: the platform entry reads both objects.
- `llm/providers/chatcompletions/models.go`: uses the shared types and function;
  behavior unchanged.
- `docs/llm-providers.md`: names every source the live-layer modality statement
  covers, including the Codex listing's own list.

## Non-goals

- The Codex entry reads its own `input_modalities`, but routes the list through
  the same normalizer as the other two sources, so a raw or empty list cannot
  merge wholesale over a curated one.
- Only `vision` is read from the capability object; its other keys (tools,
  reasoning) do not affect resolution today.
- No lenient parsing. A non-boolean `vision` fails the whole listing, exactly as
  it already does for the chat reader; see Risks.

## Behavior

- A model row's advertised input modalities are `architecture.input_modalities`
  when that list is non-empty, else `["text", "image"]` when
  `capabilities.vision` is true, else none. Both readers now apply that rule.
- A row that advertises nothing leaves `InputModalities` unset.

## Precedence and preservation

- The live layer still never overrides a field the user layer set, and an unset
  modality list overrides nothing (`llm/registry/merge.go`).
- The precedence lives in one function, so the two readers cannot disagree.

## Test plan

- Red first, `llm/providers/responses/models_test.go`: a platform row with
  `capabilities.vision` true yields image input; `false` and an absent object
  yield none; and `architecture.input_modalities: ["text"]` beside
  `capabilities.vision: true` yields `["text"]` (architecture wins).
- `llm/providers/chatcompletions/models_test.go`: add the both-present
  precedence fixture, so the shared-type refactor cannot invert it.
- `go test ./llm/...`.

## Risks

- A gateway that advertises vision for a model that cannot see images marks it
  image-capable; this is the risk #4489's chat fix already carries.
- A malformed (non-boolean) `vision` value, or a `capabilities`/`architecture`
  value that is not an object, fails the whole `/models` parse, so the instance
  falls back to its catalog facts. That is pre-existing chat behavior, kept for
  consistency rather than adding a lenient parse.
