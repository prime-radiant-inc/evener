# One rule for the wire image detail

Date: 2026-10-13
Issue: [#4509](https://github.com/prime-radiant-inc/evener/issues/4509)
Status: revised after spec review; awaiting implementation

## Problem

The image-detail precedence rule — the part's own detail when it names one, else
the row's `image_detail`; a row set to `omit` drops the field — is encoded in
three places, and two of them are copies:

- `llm/providers/responses/input.go` inlines it twice: the user-message image
  block and the tool-result image block (both `if imageDetail != "omit" { … }`).
- `llm/token_count.go` `effectiveImageDetail` re-encodes the Responses half.
- `llm.ChatImageDetail` holds the chat half (it also maps the chat wire's
  vocabulary).

A change to the rule must be made in every site, and the two Responses blocks can
drift apart. #4489 needed such a change, and the estimator lagged behind it until
a reviewer caught it.

## Scope

- `llm/image_detail.go`: add `ResponsesImageDetail` (the Responses rule, with its
  `high` default) and an unexported `imageDetail(protocol, part, cap)` dispatch
  for the estimator; `ChatImageDetail` stays as the chat branch, and each
  builder calls its own protocol's rule directly.
- `llm/providers/responses/request.go`: pass the raw `image_detail` cap through
  instead of pre-defaulting it to `high`.
- `llm/providers/responses/input.go`: both image blocks call
  `llm.ResponsesImageDetail`.
- `llm/token_count.go`: `effectiveImageDetail` returns
  `llm.ImageDetail(protocol, part, cap)`.
- `docs/llm-providers.md`: its `image_detail` sentence stays true.

## Non-goals

- No change to what any builder sends on the wire, case by case. There is one
  deliberate exception in the estimator, stated under Preservation.
- The chat wire vocabulary mapping stays in the chat branch; the Responses
  builder keeps carrying the part's and the row's values verbatim.
- The Anthropic and Google builders carry no detail field at all, before and
  after; only the estimator reads a value for them.
- No change to when a detail is sent: a Responses-lite row that carries
  `image_detail = "omit"` still drops the field.

## Behavior (single-sourced against the current code)

Wire detail, by protocol:

| protocol | row `image_detail` | part detail | wire |
|---|---|---|---|
| openai-chat | unset | none | omitted |
| openai-chat | `low` | none | `low` |
| openai-chat | `original` | none | `high` |
| openai-chat | `omit` | `low` | omitted |
| openai-chat | unset | `auto` / `low` / `high` | that value |
| openai-chat | unset | `original` | `high` |
| openai-chat | unset | anything else | omitted |
| openai-chat | `low` | `high` | `high` |
| openai-responses | unset | none | `high` |
| openai-responses | `low` | none | `low` |
| openai-responses | `original` | none | `original` |
| openai-responses | `omit` | any | omitted |
| openai-responses | `low` | `high` | `high` |
| anthropic / google | any | any | no detail field |

Normalization: the chat branch trims and lowercases both values and admits only
`auto|low|high` (mapping `original` to `high`). The Responses branch drops the
field when the row's value is exactly `omit`, and otherwise carries the part's
and the row's values verbatim, with no trimming.

The estimator resolves through the same function: `chat` and `responses` get
their branch, and any other protocol carries the part's own value and nothing
else (no row cap, no default).

## Preservation

Every wire outcome is unchanged. One estimate moves:

- A Responses row whose cap is `omit` *and* whose part names a detail was billed
  by the part's value (`low` billed 85) while the wire dropped the field (about
  765). The estimator now matches the wire. No production code sets a part
  detail (the vision side-channel stopped pinning one in #4489), so this is
  reachable only from tests — and it is the divergence the shared rule exists to
  remove.
- A Responses part whose detail is whitespace only no longer falls through to
  the row's cap: the builder always sent that whitespace value, and the
  estimator now reads it too, so a `low` cap alongside a whitespace part detail
  bills the full-detail formula instead of 85. Also test-only.
- A Responses row with no cap and no part detail resolved to `""` and now
  resolves to `high`; `estimateOpenAIImageTokens` treats every value but `low`
  identically, so no count moves.

## Test plan

- New `llm/image_detail_test.go`: `imageDetail` across the table, including the
  unknown-protocol branch, `omit` dropping even a part-named detail, and the
  chat vocabulary mapping.
- Builder wiring: the Responses tests keep pinning the user-message block
  (`high` baseline, `original`, `omit` dropped) and gain a tool-result image case
  (the second duplicated block); the chat tests keep pinning
  `TestToChatMessages_ImageDetailHonorsRowCap` and its invariant.
- Estimator: the chat test stays; a Responses `omit`-plus-part-detail case pins
  the moved count at the full-detail value.
- `go test ./llm/...`.

## Risks

- The Responses builder no longer pre-defaults `detail`; the default now lives in
  `ResponsesImageDetail`. A builder path that bypasses the resolver would send no
  detail at all, so both blocks must call it, and the new tool-result test is the
  guard.
