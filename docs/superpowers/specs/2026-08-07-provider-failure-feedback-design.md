# Provider-Failure Feedback and Retry Resilience

**Date:** 2026-08-07
**Status:** Draft v3 — revised after two adversarial review rounds; awaiting review

## Context

Session `0343Ek5InpsrSp2nI4L7xh` (kimi-k3 via the lunarouter profile) burned
11 retry attempts over 20 minutes on a turn, then did it again after the user
typed "continue". Two distinct failure shapes: mid-stream stalls killed by
serf's 30s `StreamRead` timeout (~32s attempts, trickle output), and an
upstream ~300s stream cap that cut attempts after minutes of real output
(~295s attempts, ~10k tokens streamed). The forensics exposed four gaps:

1. The retry chip (`serf/thread/modelRetry`) exists but clears on the first
   delta and shows no cumulative state, so 20 minutes of failing retries read
   as "stuck thinking".
2. Transient failures get 10 identical retries (`llm.DefaultRetryPolicy`)
   with no early stop, even when every attempt fails the same way.
3. Partial output from a cut-off stream is discarded everywhere the model
   could see it: `consumeModelStream` drops the accumulator on error, and the
   UI partial is reset on retry.
4. Persisted `TurnFailure` markers are presentational only and are never sent
   to the model (`agent/session_model_call.go:1147`). On resume, the model has
   no idea its previous response was cut off, so it repeats the same doomed
   behavior.

The unifying fix for 3 and 4: when a turn fails, persist what the model
actually produced as the assistant turn it was, followed by a model-visible
steering turn that says what happened — so on resume the model continues
from its own draft instead of starting over blind.

## Design history (why not the alternatives)

- **Prefill/continuation salvage** (send the partial back as a trailing
  assistant message for the API to continue): impossible. Assistant prefill
  returns 400 on all current Anthropic models; thinking blocks cannot be
  modified; OpenAI-compatible reasoning deltas are write-only.
- **Transient retry-time fold-in** (v1): fatally flawed. The reminder died
  with the retry group, so multi-round recovery lost the remainder; and
  re-emitting the whole partial in one response takes as long as the
  original generation and hits the same transport cap.
- **File-spill** (write the partial to a file, steer the model to read it
  back): works, but adds a file lifecycle and read-tool round trips, and
  puts the draft outside the model's context until it spends rounds
  fetching it.
- **Persist as history** (this spec): the partial rides in the transcript at
  the cost it would have had if the turn succeeded, survives rounds and
  restarts for free, and sits in context as a draft. Note an earlier claim
  here was wrong: serf forces `tool_choice: required` and rejects bare-text
  responses (`agent/session_tool_round.go` `decideNoToolCalls`), so
  user-facing continuation must be re-emitted through the result tool —
  persisting as history does **not** avoid re-emission. Its advantages over
  file-spill are the absent file lifecycle, no read round trips, and the
  draft being in context so the model reuses rather than re-derives.

## Out of scope

- Cross-session or cross-turn provider health tracking (circuit breaker at
  the profile level). The early stop in component 2 is per retry group.
- Automatic re-issue of the failed round after settlement. V1 settles the
  turn; the user resumes.
- Fixing lunarouter's ~300s stream cap or mid-stream stalls (upstream issue,
  tracked separately).
- Salvaging reasoning/thinking output. It is write-only on every provider
  and replaying it invites confusion; it is never persisted or replayed.

## Shared infrastructure: the round salvage recorder

Everything downstream needs data that today dies inside
`consumeModelStream`. One agent-owned recorder fixes that:

- `consumeModelStream` returns its accumulator's partial response and
  attempt stats (wall-clock duration, accumulated text bytes, open-phase vs
  consume-phase failure) alongside the error, instead of an empty response.
- `callModel` records, per retry group: every attempt's stats, and a
  best-partial snapshot (captured before each retry, since `OnReset` wipes
  the UI copy) — "best" = largest total salvaged bytes.
- `callModelWithFallback` aggregates recorders across the round's groups
  (primary, continuation fallback, each model fallback) into round-scoped
  state the settlement path can read. Selection for salvage spans **all**
  groups in the round — a fallback group that fails with a trickle must not
  shadow the primary group's 10k-token partial (each salvaged turn notes
  which model produced it). Steering stats describe the group that
  ultimately failed.

The recorder exists for every terminal error, not only the fail-fast path —
settlements via permanent mid-stream errors or a too-long Retry-After abort
compose from the same state. (`ProviderUnhealthyError` carries a reference
to its group's stats; it is not the stats' only carrier.)

## Component 1: sticky, cumulative retry liveness

**Now:** `MODEL_RETRY` fires only in `OnRetry` (before the backoff sleep);
both UIs clear the chip on the first delta
(`cmd/serf-tui/hub_notifications.go:580`, web reducer clear-on-any-frame).

**Change (minimal):**

- `events.ModelRetryData` gains `GroupElapsedMS` — elapsed time since the
  retry group's first attempt. No new event kind, no phase field: clients
  already receive `DelayMS` and can derive waiting-vs-in-progress locally
  (delay expired, or a delta arrived).
- UI clearing rule changes: the retry state survives deltas and clears only
  on round completion or turn settlement (`NotifyItemCompleted`,
  `NotifyTurnCompleted`, `NotifyTurnStarted` — a failed turn does emit
  `NotifyTurnCompleted`, so failure clears it too).
- Rendering: `provider error · attempt 9/11 · retrying in 32s · 14m on this
  call` (waiting) / `… · in progress · 14m on this call` (streaming). The
  elapsed label is per retry group (one model call), not per turn.

## Component 2: stream-failure early stop

**Now:** `llm.RetryStream` (`llm/stream_retry.go`) runs the full budget
(10 retries) regardless of failure pattern.

**Change:** the attempt closure reports whether each failure was
**open-phase** (request rejected before or at stream open: 429, 5xx, auth)
or **consume-phase** (the stream opened and then died: stall timeout,
truncation). This is positional knowledge the closure already has — no
error-class taxonomy needed, which is why v2's `llm.Kind` keying (dropped:
stall → `KindTimeout`, cap-cut → `KindUnknown`) isn't resurrected.

Early-stop rules, both scoped to consume-phase failures only:

- **Streak:** after `FailFastAfter` (serf passes 4) consecutive
  consume-phase failures, stop with `llm.ProviderUnhealthyError`.
- **Cap detection:** after 2 consecutive consume-phase failures that each
  ended after substantial streaming (≥60s), stop immediately — two long
  streams cut in a row is strong evidence of a hard transport cap that
  identical retries cannot beat, and each such attempt costs minutes.

Open-phase failures (rate limits with Retry-After, 5xx bursts) never count:
riding those out on the full budget is existing, documented, correct
behavior (`llm/retry.go:30-33`) and stays untouched.

**`ProviderUnhealthyError` is NOT fallback-eligible.** v2 said the
opposite; review killed it: serf rejects cross-provider fallbacks
(`agent/session_init.go`), so every fallback model sits behind the same
endpoint and transport that was just declared unhealthy — fallback would
multiply the grind (N+1 groups) for no route out. The error settles the
round immediately, preserving the existing invariant that retryable-class
failures never trigger the model-fallback chain.

**Honest bounds:** the ~2-minute failure applies to the stall shape
(4 × ~32s + short backoffs). Cap-shaped groups are bounded by the
cap-detection rule at 2 long attempts (~10 minutes worst case), not 2
minutes — better than today's ~20, and each attempt was streaming real
output that becomes salvage.

## Component 3: partial-preserving settlement

Settlement persists model-visible turns only when the terminal error is
`ProviderUnhealthyError` **or** the round's recorder shows at least one
consume-phase failure. Explicitly excluded: user interrupts and
cancellations; context-length failures (appending turns to an overflowing
history makes it worse); and open-phase request rejections (auth, 4xx,
quota — "the provider connection failed" would be false, and stacking
steering on a misconfiguration helps nothing).

When it fires, up to two turns are persisted, in order:

1. **The salvaged assistant turn** (only when salvageable text exists). A
   normal `TurnAssistant` carrying the round's best partial (cross-group
   selection per the recorder):
   - Text content blocks from the accumulator, verbatim and whole. At most
     one response's worth of tokens — the cost success would have had.
   - Content cut off inside a tool-call argument: extract **all top-level
     string-valued fields** from the partial JSON (generalizing
     `partialJSONStringField`), rendered labeled by field name under a
     marker naming the tool call and stating it never executed. Broken
     `tool_use` blocks themselves are never persisted (invalid history on
     several providers). "Largest" compares total salvaged bytes across
     text blocks + extracted fields.
   - Reasoning deltas: never included.
2. **The steering turn** (always, when settlement fires). Model-visible,
   via the existing `appendSteeringTurn` mechanism, composed from recorder
   stats:
   - What happened, per failure shape: stalls ("the provider repeatedly
     stopped responding mid-stream: N attempts over M minutes") vs cap
     ("the transport cut off your response after ~Xs of streaming, twice").
   - When a salvaged turn precedes it: "Before the connection failed, you
     produced the response above. Any tool calls in progress did not
     execute, and nothing was delivered or saved. Treat it as your draft —
     re-send user-facing content through [result tool] and re-issue file
     writes in smaller pieces, reusing the draft rather than regenerating
     it. Do not start over."
   - Cap shape adds: "The transport cannot sustain responses that long.
     Keep each response well under that size and continue your work across
     multiple rounds."

**Live-client consistency:** persisted turns must reach watching clients,
not just reloading ones. At settlement the session emits the corresponding
events (assistant-text reset, then the salvaged turn's content and item
completion, then the steering event) so the appwire projection renders
exactly what the transcript stores — the screen may hold the *last*
attempt's partial while the *largest* is persisted, and the reset+re-emit
reconciles that. The `TurnFailure` marker and its failure event are
unchanged and remain model-invisible.

No dedupe machinery: the transcript is append-only; with early stop,
consecutive failure settlements are rare, bounded, and harmless to stack.

## How the incident would have played out

1. **First failing turn (the stall group):** attempts 1–4 stall at ~32s
   each; the streak rule stops the group at ~2m10s with "provider
   unhealthy: repeated mid-stream stalls". The chip showed attempts and
   elapsed throughout. Salvage is a trickle, so settlement persists
   steering only: provider unstable, nothing was saved.
2. **User types "continue".** The retry group for the rebuilt round hits
   the other shape: two attempts stream ~5 minutes of plan text each and
   get cut at the ~300s cap. Cap detection stops the group after the
   second (~10 min instead of ~20), and settlement persists the larger
   ~10k-token partial as an assistant turn plus cap-shape steering.
3. **User types "continue" again.** The model sees its own draft and the
   instruction to work in smaller pieces; it re-issues the plan as several
   smaller `write_file` calls reusing the draft, each finishing well under
   the cap.

(v2's walkthrough claimed the first group would salvage "the plan text
already streamed" — wrong: that group's attempts were stalls with trickle
output; the large partials belonged to the resumed turn's group. This
version is consistent with the recorded attempt stats.)

## Testing

TDD throughout; per repo policy, all functionality covered.

- **Recorder:** partial + stats survive `consumeModelStream` error returns;
  per-attempt capture before `OnReset`; best-of-group and cross-group
  selection (fallback trickle never shadows primary partial); stats present
  for non-fail-fast terminal errors.
- **RetryStream:** open-phase failures never count toward either rule;
  streak at 4 consume-phase failures; cap detection at 2 substantial
  (≥60s) consume-phase failures; disabled when 0; wrapper error references
  group stats; existing retry tests stay green.
- **Fallback interaction:** `ProviderUnhealthyError` settles immediately —
  the model-fallback chain is not attempted.
- **Salvage content:** all top-level string fields extracted from partial
  tool args, labeled, under never-executed marker; no broken tool calls; no
  reasoning content; largest-by-total-bytes selection.
- **Settlement gating:** fires for consume-phase rounds; absent for
  interrupts, cancellations, context-length, and open-phase rejections;
  steering-only when nothing salvageable; wording matches failure shape.
- **History:** salvaged turn + steering model-visible in `buildHistory`, in
  order, including after restore; `TurnFailure` stays model-invisible; a
  text-only assistant turn followed by steering forms valid provider
  history.
- **Live consistency:** settlement event sequence produces identical
  content in the appwire projection and the reloaded transcript, including
  the last-attempt-on-screen vs largest-persisted case and previously
  shown-then-reset communicate text.
- **Events/UI:** `GroupElapsedMS` plumbing; new clearing rules in TUI and
  web reducer tests; chip survives deltas, clears on settlement.
- **Integration:** end-to-end fake-provider scenarios for both failure
  shapes (stall streak → fast settle, steering only; cap shape → 2-attempt
  stop, partial + steering persisted; resumed turn carries both in model
  history).
