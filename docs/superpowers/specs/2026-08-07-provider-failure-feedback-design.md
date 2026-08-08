# Provider-Failure Feedback and Retry Resilience

**Date:** 2026-08-07
**Status:** Draft v2 — revised after adversarial review; awaiting review

## Context

Session `0343Ek5InpsrSp2nI4L7xh` (kimi-k3 via the lunarouter profile) burned
11 identical retry attempts over 20 minutes on a turn whose completion needed
~300s of streaming against an upstream that caps streams at ~300s, then did it
again after the user typed "continue". The forensics exposed four gaps:

1. The retry chip (`serf/thread/modelRetry`) exists but clears on the first
   delta and shows no cumulative state, so 20 minutes of failing retries read
   as "stuck thinking".
2. Transient failures get 10 identical retries (`llm.DefaultRetryPolicy`)
   with no early stop, even when every attempt fails the same way.
3. Partial output from a cut-off stream is discarded everywhere the model
   could see it: `consumeModelStream` drops the accumulator on error, and the
   UI partial is reset on retry. A turn that streamed ~10k tokens of real
   output five times leaves nothing behind.
4. Persisted `TurnFailure` markers are presentational only and are never sent
   to the model (`agent/session_model_call.go:1147`). On resume, the model has
   no idea its previous response was cut off, so it repeats the same doomed
   behavior.

The unifying fix for 3 and 4: when a turn fails, persist what the model
actually produced as the assistant turn it was, followed by a model-visible
steering turn that says what happened — so on resume the model continues
from the cut point instead of starting over blind.

## Design history (why not the alternatives)

- **Prefill/continuation salvage** (send the partial back as a trailing
  assistant message for the API to continue): impossible. Assistant prefill
  returns 400 on all current Anthropic models; thinking blocks cannot be
  modified; OpenAI-compatible reasoning deltas are write-only.
- **Transient retry-time fold-in** (v1 of this spec): fatally flawed. The
  reminder died with the retry group, so multi-round recovery lost the
  remainder; and re-emitting the whole partial in one response takes as long
  as the original generation and hits the same transport cap.
- **File-spill** (write the partial to a file, steer the model to read it
  back): works, but strictly worse than persisting it as history — it adds a
  file lifecycle and read-tool round trips, and the model still re-emits
  everything it reads.
- **Persist as history** (this spec): the partial rides in the transcript at
  exactly the cost it would have had if the turn succeeded, survives rounds
  and restarts for free, and for plain-text output the model continues from
  the cut point with no re-emission at all. This is also the
  provider-documented replacement for continuation prefill ("Your previous
  response was interrupted and ended with […]. Continue from there.").

## Out of scope

- Cross-session or cross-turn provider health tracking (circuit breaker at
  the profile level). The early stop in component 2 is per retry group.
- Automatic re-issue of the failed round after settlement. V1 settles the
  turn; the user resumes.
- Fixing lunarouter's ~300s stream cap or mid-stream stalls (upstream issue,
  tracked separately).
- Salvaging reasoning/thinking output. It is write-only on every provider
  and replaying it invites confusion; it is never persisted or replayed.

## Component 1: sticky, cumulative retry liveness

**Now:** `MODEL_RETRY` fires only in `OnRetry` (before the backoff sleep);
both UIs clear the chip on the first delta
(`cmd/serf-tui/hub_notifications.go:580` and the web LivenessLine
equivalent). Nothing is cumulative.

**Change (minimal):**

- `events.ModelRetryData` gains `GroupElapsedMS` — elapsed time since the
  retry group's first attempt. No new event kind, no phase field, no group
  ID on the wire: clients already receive `DelayMS` and can derive
  waiting-vs-in-progress locally (delay expired, or a delta arrived).
- UI clearing rule changes: the retry state survives deltas and clears only
  on round completion or turn settlement (`NotifyItemCompleted`,
  `NotifyTurnCompleted`, `NotifyTurnStarted`). While a retry attempt is
  streaming, the chip renders "in progress" instead of vanishing.
- Rendering: `provider error · attempt 9/11 · retrying in 32s · 14m on this
  call` (waiting) / `… · in progress · 14m on this call` (streaming). The
  elapsed label is per retry group (one model call), not per turn — a
  multi-round turn restarts the clock each round.

## Component 2: failure-streak early stop

**Now:** `llm.RetryStream` (`llm/stream_retry.go`) runs the full budget
(10 retries) regardless of failure pattern.

**Change:** `RetryStreamOptions` gains `FailFastAfter int` (0 = disabled;
serf's agent passes 4). When `FailFastAfter` consecutive attempts fail with
retryable stream-level failures, `RetryStream` returns early with a distinct
wrapper error (`llm.ProviderUnhealthyError`) carrying the last error plus
group stats (attempts, elapsed, per-attempt durations/bytes).

- **No error-class matching.** The incident's two failure modes classify
  differently (`StreamRead` stall → `KindTimeout`; cap truncation →
  `StreamError` → `KindUnknown`), so keying the streak on `llm.Kind` would
  reset it on exactly the mixed incidents it exists for. The streak is
  simply consecutive failed attempts in the group; any success ends the
  group anyway (`RetryStream` returns on success).
- **Fallback classification.** `ProviderUnhealthyError` is
  fallback-eligible in `callModelWithFallback`: the provider has been
  declared unhealthy, so a configured fallback model is the only remaining
  route. Each fallback model gets its own (fail-fast-bounded) group, so the
  worst case with N fallbacks is N+1 short groups, not N+1 full budgets.
- `emitTurnFailure` surfaces it as its own failure message ("provider
  unhealthy after 4 stream failures, 2m10s") so the incident's 20-minute
  silent grind becomes a clear failure in ~2 minutes.

**Scope note:** partial-salvage state and streak state are per
`callModel` invocation (one `RetryStream` group). `callModelWithFallback`
can run several groups per round (continuation fallback, model fallbacks);
each group's state is independent, and settlement reports the group that
ultimately failed.

## Component 3: partial-preserving settlement

When a turn fails with a provider-failure class (not user interrupts, not
cancellations), settlement persists up to two model-visible turns, in order:

1. **The salvaged assistant turn** (only when salvageable text exists). A
   normal `TurnAssistant` carrying what the model actually produced in the
   failed group's best attempt:
   - Text content blocks from the stream accumulator, verbatim and whole.
     The partial is at most one response's worth of tokens, so persisting
     it costs exactly what a successful response would have — no special
     budget or compaction treatment.
   - Content cut off inside a tool-call argument is extracted
     (`partialJSONStringField`, already used for communicate previews) and
     rendered into the turn as text under a marker noting the tool call
     that never executed. Broken tool calls themselves are never persisted
     — a `tool_use` without a result is invalid history on several
     providers.
   - Selection: the largest salvageable partial across the failed group's
     attempts (a 10k-token cap-cut must not be shadowed by a later stall's
     trickle).
   - Reasoning deltas: never included.
2. **The steering turn** (always). Model-visible, via the existing steering
   mechanism, composed from group stats:
   - What happened: "The provider connection failed before your response
     completed: N attempts over M minutes." Honest per-class detail (stall
     vs. transport cut) from the recorded attempt stats.
   - When a salvaged turn precedes it: "Before the connection failed, you
     sent the response above. Any tool calls in progress did not execute.
     Continue from where it ends — do not start over."
   - When the failure class is truncation-after-substantial-output (stream
     ran ≥60s or salvaged text ≥8 KB): "The transport cuts off long
     responses. Produce shorter responses and continue your work across
     multiple rounds."

The presentational `TurnFailure` marker is unchanged and remains
model-invisible. No dedupe machinery: the transcript is append-only, and
with component 2's early stop, consecutive failure settlements are rare,
bounded, and harmless to stack.

**Effect on resume:** the model's history reads as a conversation in which
it produced the partial, was told the connection failed, and was told to
continue — the documented interrupted-response continuation pattern. For
plain-text output it continues from the cut point with zero re-emission.
For tool-arg content the material is in context to write out in pieces.

## How the incident would have played out

1. Turn starts failing at 22:27. Attempts 1–4 fail (~32s stalls). Component
   2 stops the group at ~22:30 with "provider unhealthy: 4 stream
   failures". Component 1 showed live attempt state and elapsed time the
   whole way.
2. Component 3 persists the largest partial (the plan text already
   streamed) as an assistant turn plus the steering turn.
3. User types "continue". The model sees its own partial plan and the
   steering; it continues from the cut point in smaller pieces instead of
   regenerating 10k tokens into the same transport cap.

## Testing

TDD throughout; per repo policy, all functionality covered.

- **RetryStream:** `FailFastAfter` unit tests — streak counting across
  mixed error classes, disabled when 0, wrapper error carries stats;
  existing retry tests stay green.
- **Fallback interaction:** `callModelWithFallback` treats
  `ProviderUnhealthyError` as fallback-eligible; per-group state isolation
  across continuation/model-fallback groups.
- **Salvage:** accumulator partial survives `consumeModelStream` error
  returns; largest-partial selection; tool-arg text extraction with
  never-executed marker; no reasoning content; no broken tool calls in the
  persisted turn.
- **Settlement:** transcript tests — assistant turn + steering turn
  persisted in order, model-visible in `buildHistory`, absent for
  cancellations/interrupts; steering-only when nothing salvageable;
  `TurnFailure` stays model-invisible; resumed-session history includes
  both turns after restore.
- **Composer:** table-driven — stats rendering, per-class wording,
  threshold gating of the shorter-responses guidance.
- **Events/UI:** `GroupElapsedMS` plumbing; new clearing rules in TUI
  notification tests and web reducer tests; chip survives deltas and clears
  on settlement.
- **Integration:** one end-to-end fake-provider scenario reproducing the
  incident shape (stall streak → early stop → partial + steering persisted
  → resumed turn carries both in model history).
