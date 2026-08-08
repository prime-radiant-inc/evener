# Provider-Failure Feedback and Retry Resilience

**Date:** 2026-08-07
**Status:** Draft — awaiting review

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
3. Retries are byte-identical, so a turn whose completion cannot fit under an
   upstream transport cap can never succeed by retrying.
4. Persisted `TurnFailure` markers are presentational only and are never sent
   to the model (`agent/session_model_call.go:1147`). On resume, the model has
   no idea its previous response was cut off, so it repeats the same doomed
   behavior.

This spec addresses all four. The unifying idea for 3 and 4: when a try
fails, tell the *model* what went wrong — transiently during retries, and
persistently at turn failure — so the agent can change behavior.

## Out of scope

- Salvaging partial output as continuation state (assistant prefill).
  Rejected: prefill returns 400 on all current Anthropic models, thinking
  blocks cannot be modified or fabricated, and OpenAI-compatible reasoning
  deltas are write-only. There is no provider-general resume mechanism.
- Cross-session or cross-turn provider health tracking (circuit breaker at
  the profile level). The early stop in component 2 is per attempt group.
- Fixing lunarouter's ~300s stream cap or mid-stream stalls (upstream issue,
  tracked separately).

## Shared piece: the failure explanation composer

One function turns an attempt group's failure state into a short, factual
explanation string. Inputs: error class, attempt count, elapsed time, and
per-attempt stream stats (bytes streamed, duration — already captured on the
api.jsonl attempt records; the composer takes them from in-memory attempt
state, not by re-reading the log). Output is honest and minimal, e.g.:

> Provider stream failures interrupted this turn: 11 attempts over 20m, all
> ending before completion. The longest attempt streamed ~5 minutes of output
> before the transport cut it off.

No speculation, no instructions beyond what the failure class supports. When
the failure class is truncation-after-substantial-output, the composer appends
the actionable guidance:

> If you were producing a long response, produce shorter responses and
> continue the work across multiple rounds.

Both component 3 and component 4 render their text through this composer so
the retry reminder and the settlement steering never drift apart.

### Partial-output fold-in rule

The composer embeds the cut-off output itself. The governing fact: the
partial was never persisted anywhere — the tool call it was headed for never
ran — so the fold-in is the **only surviving copy** of the work. Truncating
it destroys generated output we already paid for and forces the model to
re-derive it. With the full text in context, the model transcribes it back
out in small chunks (no re-reasoning, no divergence) and generates fresh
content only from the cut point onward.

1. **No truncation.** The full accumulated assistant text of the selected
   partial is included, verbatim, wrapped in a clearly delimited block. It
   was produced as a single response, so by construction it fits within the
   conversation's budget as input.
2. **Material.** Text content blocks from the stream accumulator only.
   Never reasoning/thinking deltas (write-only output on every provider;
   replaying them invites confusion). Never partial tool-call argument JSON
   (broken by construction; out of scope for v1).
3. **Selection.** The *largest* partial across the attempt group, not the
   most recent — a 2.9 MB attempt must not be shadowed by a later 4 KB
   stall. The fold-in is recomputed as attempts complete and the single
   reminder is replaced, never stacked.
4. **Accompanying guidance:** "Your previous response was cut off by the
   transport. Its content up to the cutoff is preserved below — reuse it
   rather than regenerating it (for example, write it out in smaller
   chunks), then continue from where it ends."

Size thresholds do **not** bound the fold-in. They have exactly one job:
the ≥ 60s-of-streaming / ≥ 8 KB-of-text signal distinguishes a
transport-cap truncation (where the produce-shorter-responses advice
applies, and the retry-time reminder fires) from a stall (where no behavior
change helps and no retry-time reminder fires).

Plumbing note: today `consumeModelStream` (`agent/session_stream.go`) drops
the accumulator on error. The attempt closure must return the accumulator's
partial response alongside the error so `callModel` can retain per-group
partial state.

## Component 1: sticky, cumulative retry liveness

**Now:** `MODEL_RETRY` fires only in `OnRetry` (before the backoff sleep);
both UIs clear the chip on the first delta (`cmd/serf-tui/hub_notifications.go:580`
and the web LivenessLine equivalent). Nothing is cumulative.

**Change:**

- `events.ModelRetryData` gains `GroupElapsedMS` (since the group's first
  attempt) and `AttemptGroupID`.
- A companion event fires when a retry attempt actually starts (after the
  sleep), carrying the same identity, so the UI can flip from "retrying in
  32s" to "attempt 9/11 in progress" instead of going blank.
- Projection forwards both on the existing `serf/thread/modelRetry`
  notification (a `Phase` field: `waiting` | `in_progress`) — one
  notification shape, no new protocol method.
- UI clearing rule changes: the retry state survives deltas and clears only
  on turn settlement or round completion (`NotifyItemCompleted`,
  `NotifyTurnCompleted`, `NotifyTurnStarted`). Rendering becomes:
  `provider error · attempt 9/11 · retrying in 32s · 14m on this turn`
  (waiting) / `provider error · attempt 9/11 · in progress · 14m on this
  turn` (streaming).

## Component 2: failure-streak early stop

**Now:** `llm.RetryStream` (`llm/stream_retry.go`) runs the full budget
(10 retries) regardless of failure pattern.

**Change:** `RetryStreamOptions` gains `FailFastAfter int` (0 = disabled;
serf's agent passes 4). When `FailFastAfter` consecutive attempts fail with
the same error class (`llm.Kind`) and no attempt in the group has succeeded,
`RetryStream` returns early with a distinct wrapper error
(`llm.ProviderUnhealthyError`) carrying the last error plus group stats
(attempts, elapsed, dominant class). `emitTurnFailure` surfaces it as its own
failure message so the user sees "provider unhealthy after 4 identical
failures (2m10s)" in ~2 minutes instead of a generic error after 20.

A retry that follows a component-3 reminder injection is no longer
byte-identical to its predecessor, but the streak still counts by error
class — the point is to stop hammering an unhealthy provider, not to detect
identical requests.

## Component 3: retry-time steering injection (transient)

When a retry follows a truncation-class failure with substantial output
(per the fold-in rule's threshold), `callModel` appends one system-reminder
message to the retry request's messages: the composer's explanation, the
full partial fold-in, and the produce-shorter-responses guidance.
Properties:

- **Transient.** Request-level only; never written to the transcript. The
  transcript records at most the settlement steering (component 4).
- **Single instance.** Each retry carries exactly one reminder reflecting
  the latest group state; it replaces the previous retry's reminder.
- **Message shape.** The same system-reminder convention the harness already
  uses for steering content, appended after the existing history so provider
  prompt-cache prefixes are preserved.
- **Scope.** Only truncation-class failures past the substantial-output
  threshold. Stall-class failures get no injection (nothing actionable) —
  they are component 2's job.

## Component 4: settlement steering (persistent, model-visible)

When a turn fails with a provider-failure class (not user interrupts, not
cancellations), `recordTurnFailure` (`agent/session_events.go:223`)
additionally persists a **steering turn** — the mechanism that already
renders to the model — carrying the composer's explanation, and the full
partial fold-in when any accumulated assistant text exists. After
settlement, the transcript is the only place the cut-off content can
survive into the resumed session, so it is persisted whole. The
presentational `TurnFailure` marker is unchanged.

On resume ("continue"), the model now sees what happened and the guidance to
work in smaller pieces, instead of a bare `write_file result → "continue"`
history.

**Dedupe:** if the immediately preceding model-visible turn is already a
failure-steering turn (consecutive failures with no other input between),
the new one replaces it rather than stacking.

**Scope:** all provider-failure classes persist the explanation — even for
stalls, "the provider is unstable; your previous response did not complete
and was not saved" is honest, useful context on resume. The
shorter-responses guidance appears only past the substantial-output
threshold; the fold-in appears whenever there is any text to preserve.

## How the incident would have played out

1. Turn starts failing at 22:27. Attempts 1–4 stall (~32s each). Component 2
   stops the group at ~22:30 with "provider unhealthy: 4 stream failures,
   same class". Component 1 showed live attempt state the whole time.
2. Component 4 persists a steering turn: stream failures, nothing saved.
3. User types "continue". The model, seeing the steering, writes the plan in
   smaller write_file chunks. If an attempt still gets cut at ~295s after
   streaming 2 MB, component 3 injects the explanation plus the full
   preserved partial on the next retry, and the model writes the preserved
   content out in short pieces under the cap, then continues from where it
   ended.

## Testing

TDD throughout; per repo policy, all functionality covered.

- **Composer:** table-driven unit tests — failure classes × threshold
  gating × fold-in selection (largest partial wins, full text verbatim, no
  reasoning content or tool-call fragments ever included).
- **RetryStream:** `FailFastAfter` unit tests — streak counting by class,
  reset on success and on class change, disabled when 0, wrapper error
  carries stats. Existing retry tests stay green.
- **callModel:** fake-provider tests that the retry request carries exactly
  one reminder, replaced across retries, absent for stalls; partial state
  survives `consumeModelStream` error returns.
- **Settlement:** transcript tests that the steering turn persists with the
  right content, is model-visible in history projection
  (`buildHistory`), dedupes consecutively, and never fires for
  cancellations. `TurnFailure` stays model-invisible.
- **Events/projection/UI:** `ModelRetryData` field plumbing, phase
  transitions, and the new clearing rules in the TUI notification tests and
  web reducer tests.
- **Integration:** one end-to-end fake-provider scenario reproducing the
  incident shape (stall streak → early stop → steering persisted → resumed
  turn carries steering in model history).
