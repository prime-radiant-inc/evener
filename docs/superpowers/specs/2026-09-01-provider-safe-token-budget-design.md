# Provider-Safe Token Budget Admission

## Problem

Evener can currently issue a request whose known input allocation and requested
output allocation exceed a model's known context window. The observed failure
used a 524,288-token total context window, a 131,072-token requested output,
and at least 393,217 input tokens. The provider correctly rejected the
524,289-token total.

The failure has three related causes:

1. Request construction asks for the full model output cap without subtracting
   it from the remaining total context.
2. Context compaction uses an input-pressure percentage only; it is not a hard
   request-admission boundary.
3. Capability ingestion conflates total context and maximum input even though
   upstream and live model metadata can expose total, input, and output as
   separate limits.

Retries and alternate request paths widen the gap: Responses continuation
requests send a small delta while retaining a larger provider-side context,
anchor recovery restores full history without another context-management pass,
and model fallback currently carries the primary model's `MaxTokens` into the
fallback.

## Goals

- Never contact a provider with a request that Evener's known caps and
  conservative input calculation already prove invalid.
- Preserve the model's full output allocation when it fits; reduce it only to
  satisfy a known hard limit.
- Treat total context, maximum input, and maximum output as independent facts.
- Apply the rule to primary calls, transport retries, Responses continuations,
  anchor recovery, model fallbacks, side calls, and direct `llm.Client` users.
- Recover once through compaction when input cannot fit, then fail locally and
  diagnostically rather than retrying forever.

## Non-goals and limits

- Evener cannot guarantee against a provider limit that neither live/catalog
  metadata nor user configuration supplies. Unknown limits remain unknown.
- A local token estimator cannot exactly reproduce every provider tokenizer.
  Admission therefore uses conservative accounting and retains one bounded
  recovery for a provider-reported context disagreement.
- This change does not alter the existing context-strategy thresholds. Those
  remain proactive quality controls, not correctness gates.
- This change does not parse numeric limits from provider error prose.

## Capability semantics

`registry.Caps` carries three optional positive integer limits:

- `ContextWindow`: maximum effective input plus allocated output.
- `MaxInputTokens`: maximum effective input independent of output.
- `MaxOutputTokens`: maximum allocated output independent of input.

A nil pointer means unknown. User-authored zero or negative limits are invalid
configuration. Catalog or live non-positive values are ignored as absent.

Catalog and live adapters preserve the source distinction:

- models.dev `limit.context`, `limit.input`, and `limit.output` map one-to-one.
- Responses total-context aliases map to `ContextWindow`; input aliases map to
  `MaxInputTokens`.
- Google `inputTokenLimit` and `outputTokenLimit` map to the independent input
  and output limits; no total window is invented.
- OpenAI-compatible `context_length` remains total context and
  `max_completion_tokens` remains maximum output.

The existing junk-cap derivation only rejects an output cap greater than or
 equal to a known *total* context window. It must not compare output against an
independent max-input value.

## Budget calculation

One pure `llm` evaluator owns the arithmetic. Its inputs are the shaped request,
the resolved caps, and the best available effective-input estimate.

The effective input is the maximum of:

1. the full request's deterministic local estimate;
2. a caller-supplied input estimate, when present; and
3. `FullHistoryInputTokensEstimate` for a Responses continuation delta.

The agent supplies its stronger provider-reported-baseline-plus-delta estimate
through the request when available. Generic client callers still receive the
local estimate.

Because provider tokenization and request framing can exceed the local
estimate, calculated input includes a safety reserve equal to the greater of
1,024 tokens or 1% of the applicable known input/total limit. The reserve is
part of every hard input and total-context comparison.

For calculated input `I`, output allocation `O`, total window `W`, max input
`Imax`, and max output `Omax`, every known constraint must hold:

```text
I <= Imax
O <= Omax
I + O <= W
O >= 1 when an explicit output allocation is required
```

The evaluator selects output as follows:

1. Start with a positive request `MaxTokens`, otherwise the known model
   `MaxOutputTokens`.
2. Clamp it to the known `MaxOutputTokens`.
3. Clamp it to `ContextWindow - I` when total context is known.
4. When total context is known but no output allocation is known, set an
   explicit allocation to the remaining positive headroom so an unknown
   protocol default cannot exceed the known total window.
5. If max input is exceeded or no positive total-context headroom remains,
   return a typed local `ContextBudgetError` and do not dispatch.

Unknown constraints add no restriction. All arithmetic is overflow-safe.

## Enforcement boundaries

### LLM client: hard invariant

`Client.Complete` and `Client.Stream` apply request shaping and admission inside
their innermost handler, immediately before the adapter override or protocol
call. This location sees middleware changes and runs once per transport retry,
so no normal client path bypasses the check.

`ShapeRequest` changes from "fill a missing output cap" to "constrain output to
the known cap": an excessive positive caller value is clamped, and a missing
value receives the known cap.

Protocol builders may not increase the admitted request allocation. Provider
option/body overlays are reconciled back to the admitted cap before HTTP
submission. Anthropic reasoning reconciliation must return a local error when
`max_tokens > thinking.budget_tokens` cannot be satisfied within both the
admitted allocation and known output cap; it must never raise `max_tokens` past
the admitted value.

### Agent: recovery and request-path correctness

The agent evaluates the full-history primary request before Responses anchor
selection. That chosen output allocation and conservative full-history estimate
travel with a delta request, because the provider's stored anchor still consumes
context even though the wire delta is small.

When input exceeds a max-input cap or no positive total-context output remains,
the agent force-compacts once, rebuilds the request from current history, and
evaluates again. A second failure is returned locally.

Anchor recovery re-evaluates the rebuilt full-history request. Every model
fallback clears the primary allocation, applies the fallback profile's output
cap, recomputes input against the fallback provider/model, and evaluates the
fallback's own input and total-context limits.

A provider-reported context-length error, which means metadata or tokenization
disagreed with admission, earns one force-compaction/rebuild retry. A second
context error remains terminal.

## Errors and observability

`ContextBudgetError` is non-retryable at the transport layer and records the
known model/provider, calculated input (including reserve), requested/admitted
output, and whichever known limit failed. Its message must state that Evener
blocked the request before provider dispatch.

The agent emits one warning when it reduces output, compacts for admission, or
receives a provider context disagreement. It does not emit repeated warnings
for ordinary transport retries of the same admitted request.

Existing context metrics continue to report actual total-context pressure;
`MaxInputTokens` and output reservation do not redefine `ContextWindow`.

## Testing

Deterministic tests use scripted providers/transports only.

- Registry tests cover one-to-one total/input/output conversion, merge,
  cloning, alias inheritance, live Responses and Google mapping, user-config
  rejection of non-positive limits, and model descriptor serialization.
- Pure budget tests cover every known/unknown cap combination, output clamping,
  independent max input, no positive headroom, overflow safety, safety reserve,
  and the exact `393217 + 131072 > 524288` regression.
- Client tests prove Complete and Stream do not call an adapter when admission
  fails and that middleware cannot introduce a bypass.
- Protocol tests prove Chat, Responses, Anthropic, and Google never serialize
  an output allocation above the admitted request or known output cap.
- Agent tests cover full-history primary requests, Responses delta shadow
  accounting, full-history anchor recovery, per-fallback recomputation, one
  compaction/rebuild recovery, local terminal failure, and one provider
  context-error recovery.

Required gates are focused package tests during TDD, then `make lint`,
`make vet`, and `make test`.
