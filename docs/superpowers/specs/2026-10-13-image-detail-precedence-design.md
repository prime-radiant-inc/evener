# One rule for the wire image detail

Date: 2026-10-13
Issue: [#4509](https://github.com/prime-radiant-inc/evener/issues/4509)
Status: awaiting review

## Problem

The image-detail precedence rule — the part's own detail when it names one, else
the row's `image_detail`; a row set to `omit` drops the field — is encoded in
three places, and two of them are copies:

- `llm/providers/responses/input.go` inlines it twice: the user-message image
  block and the tool-result image block (both `if imageDetail != "omit" { … }`).
- `llm/token_count.go` `effectiveImageDetail` re-encodes the Responses half so
  the estimate matches the wire.
- `llm.ChatImageDetail` holds the chat half (it also maps the chat wire's
  vocabulary).

A change to the rule must be made in every site, and the two Responses blocks
can already drift apart. #4489 needed exactly such a change, and the estimator
lagged behind it until a reviewer caught it.

## Scope

- `llm/image_detail.go`: add `ResponsesImageDetail` (the Responses rule, with
  its `high` default) and `ImageDetail(protocol, part, cap)` as the protocol
  dispatch; `ChatImageDetail` stays as the chat branch.
- `llm/providers/responses/request.go`: pass the raw `image_detail` cap through
  instead of pre-defaulting it to `high`.
- `llm/providers/responses/input.go`: both image blocks call
  `llm.ResponsesImageDetail`.
- `llm/token_count.go`: `effectiveImageDetail` returns
  `llm.ImageDetail(protocol, part, cap)`.
- `docs/llm-providers.md`: its `image_detail` sentence stays true (both builders
  send the part's own detail, else the row's value; `omit` drops the field; the
  chat vocabulary maps `original` to `high`).

## Non-goals

- No behavior change. Every per-protocol outcome stays exactly as it is.
- The chat wire vocabulary mapping stays in the chat branch; the Responses
  builder keeps carrying its row's value verbatim.
- Anthropic and Google keep carrying the part's own detail and nothing else.
- No change to *when* a detail is sent: a Responses-lite row that carries
  `image_detail = "omit"` still drops the field.

## Behavior (single-sourced, unchanged)

| protocol | row `image_detail` | part detail | wire |
|---|---|---|---|
| openai-chat | unset | none | omitted |
| openai-chat | `low` | none | `low` |
| openai-chat | `original` | none | `high` |
| openai-chat | `omit` | `low` | omitted |
| openai-chat | any | `auto`/`low`/`high` | that value |
| openai-chat | any | anything else | omitted |
| openai-responses | unset | none | `high` |
| openai-responses | `low` | none | `low` |
| openai-responses | `omit` | any | omitted |
| openai-responses | any other | `low` | `low` |
| anthropic / google | any | `low` | `low` |

## Preservation

- The token estimator's `effectiveImageDetail` now returns the resolver's
  string. For a Responses row with no cap and no part detail this changes from
  `""` to `"high"`; `estimateOpenAIImageTokens` treats every value but `low`
  identically, so no billed count moves and the function is unexported.

## Test plan

- New `llm/image_detail_test.go`: `ImageDetail` across the table above,
  including that `omit` drops even a part-named detail and that other protocols
  carry the part's value alone.
- The Responses builder tests keep their own outcomes:
  `TestBuildBody_*` detail assertions (`high` baseline, `original`, `omit`
  dropped) stay green.
- The estimator tests stay green.
- `go test ./llm/...`.

## Risks

- The Responses builder no longer pre-defaults `detail`; the default now lives
  in `ResponsesImageDetail`. A builder path that bypasses the resolver would
  send no detail at all, so both blocks must call it.
