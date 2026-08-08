# Provider-Failure Feedback and Retry Resilience

**Date:** 2026-08-07
**Status:** Draft v7 — after fifth adversarial round; awaiting review

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
- Cross-provider fallbacks. Directed by Jesse, but adversarial review
  showed it is a spec-sized feature of its own (history re-projection,
  response-path tool-name mapping, state isolation, credentials, window
  disparity, meta-provider ref semantics). Requirements captured in
  `2026-08-07-cross-provider-fallbacks-requirements.md`; this spec only
  provides the eligibility extension point.
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
  which model produced it). Steering stats describe the
  **salvage-producing (or consume-phase) group**, not whichever group
  happened to fail last: when a chain walk ends on an open-phase fallback
  rejection (the likely shape — an unauthenticated fallback entry), the
  steering describes the consume-phase group's shape and provider, with
  one added clause noting the configured fallback also failed and how.

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
- UI clearing rule changes: the retry state survives deltas and clears
  only on turn boundaries (`NotifyTurnCompleted` — which failed turns do
  emit — and `NotifyTurnStarted`) or on completion of a **model-output
  item** (assistant message, reasoning, tool call). `NotifyItemCompleted`
  from systemMessage announcements or user-input items must NOT clear it:
  those arrive mid-grind (a user steering "are you stuck?" completes a
  systemMessage item), and clearing there re-creates the vanishing-chip
  bug this component exists to fix.
- Rendering: `provider error · attempt 3/4 · retrying in 32s · 14m on
  this call` (waiting) / `… · in progress · 14m on this call`
  (streaming). The elapsed label is per retry group (one model call), not
  per turn. Two honesty rules:
  - **The denominator must be the effective bound.** `ModelRetryData`
    gains `AttemptCap`: the policy budget while the group has only
    open-phase failures, dropping to `FailFastAfter` (or the cap-detection
    bound) once consume-phase failures are present. Rendering `9/11` when
    component 2 will stop the group at 4 promises patience the budget
    won't deliver.
  - **Model identity renders when it differs from the session's primary.**
    `ModelRetryData.Model` already rides the wire unrendered; a chain walk
    resets the chip's numbers ("attempt 1/4 · 5s") and without the model
    tag the user cannot distinguish "same model, still failing" from "now
    trying a different model" — the confusion class this component exists
    to eliminate.

## Component 2: stream-failure early stop

**Now:** `llm.RetryStream` (`llm/stream_retry.go`) runs the full budget
(10 retries) regardless of failure pattern.

**Change:** the attempt closure reports whether each failure was
**open-phase** (request rejected before or at stream open: 429, 5xx, auth)
or **consume-phase** (the stream opened and then died: stall timeout,
truncation). This is positional knowledge the closure already has — no
error-class taxonomy needed, which is why v2's `llm.Kind` keying (dropped:
stall → `KindTimeout`, cap-cut → `KindUnknown`) isn't resurrected.

**Adapter fix (standalone — ships first).** The openai-compat adapter
drops any SSE line that isn't a well-formed chat-completion chunk
(`llm/providers/openaicompat/adapter.go:595-599`), including in-band
`{"error": ...}` chunks — the standard way meta-providers (openrouter,
lunarouter) report upstream rejections after the 200 header is committed.
Today those rejections all degrade into a generic "stream ended without
completion", which is also why the incident's own api.jsonl was so
uninformative. The adapter learns to decode in-band error chunks into
typed llm errors carrying the payload's code/status, so a rate-limit,
quota, or moderation rejection classifies as what it is. **Landed:**
commit c3b80eb60 (integer and numeric-string codes map to their HTTP
class; string codes ride as `ErrorCode` on the retryable-unknown class;
in-band retry hints are **not** mapped to `RetryAfter()` — decoded 429s
keep normal backoff and cannot trigger the kata-r128 retry-after fallback
from an in-band hint).

The fix is code-independent but not behaviorally inert: failures that
previously degraded to a generic retryable stream error now classify
truthfully, so a decoded permanent-class in-band error (4xx int code)
walks the existing model-fallback chain where it previously retried in
place. That is correct behavior arriving early, and its classification
and fallback-eligibility cases belong in the standalone fix's tests.

**Phase classification of decoded and dropped-connection failures** is
positional, like everything else in this component:

- **"Content events"** for phase classification = text deltas, tool-call
  argument deltas, AND reasoning deltas — activity is activity, and the
  incident model (kimi-k3) streams reasoning first. Salvage selection and
  byte floors remain text + tool-arg only (reasoning is never salvaged).
- An in-band error with **content events before it** is a
  **content-bearing consume-phase failure**: it counts toward both
  early-stop rules and is salvage-eligible — a meta-provider reporting an
  upstream failure in-band after minutes of streaming is the cap shape
  with a chattier transport, not a request rejection. Steering wording
  draws on the decoded class.
- An in-band error with **zero content events** before it, or any other
  zero-content failure that resolved **fast**, is open-phase-equivalent:
  four fast rejections must never produce "the provider repeatedly
  stopped responding mid-stream" steering.
- A zero-content failure that ends in the **stall timeout**
  (`ErrSSEReadTimeout`, or elapsed at the ~30s `StreamRead` bound) is a
  **consume-phase stall**: the provider accepted the request and sent
  nothing. It counts toward the streak — accept-then-silence is the
  incident's stall shape and must not ride the full budget — with its own
  steering wording ("the provider accepted requests but streamed
  nothing").

Early-stop rules (consume-phase failures only, per the classification
above — consume-phase stalls count toward the streak):

- **Streak:** after `FailFastAfter` (serf passes 4) consecutive
  consume-phase failures, stop with `llm.ProviderUnhealthyError`.
- **Cap detection:** after 2 consecutive consume-phase failures that each
  ended after substantial streaming (content-event window ≥ 60s — twice
  the 30s `StreamRead` stall bound, so a stream productive that long is
  demonstrably not a stall; no byte floor, since a reasoning-heavy stream
  cut at a cap is cap-shaped regardless of how little text it emitted),
  stop immediately — two long streams cut in a
  row is strong evidence of a hard transport cap that identical retries
  cannot beat, and each such attempt costs minutes. Cap detection **also
  raises `llm.ProviderUnhealthyError`** (wrapping the last attempt
  error), so both early stops share the settlement and mid-chain-abort
  semantics — a fallback group tripping cap detection aborts the chain
  exactly like a streak stop.

**Measurement.** "Substantial streaming" is measured on the
**content-event window** — first content event to last content event —
recorded by the agent-side closure (which sees the events), not by
`RetryStream` (whose attempt contract carries no stats). Wall-clock is explicitly not the measure: a
20s-stream-then-30s-stall attempt is stall-shaped, and SSE keep-alive
comments reset the read timer (`llm/sse.go:169-170`) so an attempt can run
minutes with zero output. A stall tail is discriminated by the existing
`ErrSSEReadTimeout` marker rather than inferred from duration. The
attempt-closure contract (or an options callback) is extended to carry
phase + stats to `RetryStream`; both early-stop rules are disabled
together when `FailFastAfter` is 0, so non-agent `RetryStream` users see
no behavior change and existing retry tests stay green.

**Interleaving.** Open-phase failures (rate limits with Retry-After, 5xx
bursts) are **transparent** to both rules: they neither count toward nor
reset either streak. Riding them out on the full budget is existing,
documented, correct behavior (`llm/retry.go:30-33`) and stays untouched —
and a provider alternating stall/429 must still trip the streak on its
4th stall rather than grinding the full budget.

**`ProviderUnhealthyError` settles the round immediately — from any
group.** An unhealthy verdict indicts the provider's endpoint and
transport, and serf's fallback chain cannot currently route around it:
same-provider fallback entries share the transport, and cross-provider
entries are rejected at session init. Three mechanics make "immediately"
real:

- **Classification is explicit.** `modelFallbackEligible` gains a
  dedicated arm returning false for `ProviderUnhealthyError` — the
  verdict's class must not be derived by `llm.Classify` walking the
  wrapped attempt error (which would land retryable and leave settlement
  riding on a coincidence).
- **Mid-chain verdicts abort the walk.** The eligibility gate runs once at
  chain entry, but a fallback group (reachable via permanent-class or
  retry-after-declined chain walks) runs the same early-stop machinery and
  can produce its own unhealthy verdict. Any group ending in
  `ProviderUnhealthyError` aborts the remaining chain, and the verdict is
  preserved as the round's terminal error rather than being replaced by
  last-error-wins.
- **The extension point is a per-entry filter.** When cross-provider
  fallbacks land (separate spec:
  `2026-08-07-cross-provider-fallbacks-requirements.md`), eligibility for
  this error is a filter evaluated per entry inside the chain loop with
  round state (which providers are already unhealthy this round) — not a
  new arm in the one-shot boolean gate, which structurally cannot host
  per-entry, per-round-state predicates.

Everything else preserves existing behavior exactly: permanent-class
errors walk the chain as today, and retryable-class errors keep their
current eligibility — including the existing retry-after-declined
exception (kata r128, `agent/session_init.go:1158-1190`), which does walk
the chain today and must not regress.

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
steering on a misconfiguration helps nothing). Fast zero-content failures
(component 2's classification) count as open-phase here; consume-phase
stalls and content-bearing in-band failures do not. Two further
carve-outs:

- **Content-filter/moderation-class failures never persist salvage.** A
  compaction-atomic, recent-tail-protected turn carrying the content that
  triggered the filter would pin it in history and defeat the existing
  ForceCompact content-filter recovery
  (`agent/session_model_call.go:490-505`), which exists to remove it.
  Steering-only, with filter-appropriate wording.
- **Interrupts persist salvage but not failure steering.** When a user
  interrupt lands while the round's recorder holds a nonzero
  consume-phase partial, discarding it wastes exactly the work this
  component preserves — and component 1's honest chip makes interrupting
  a visible grind *more* likely. The salvaged turn is persisted with an
  interrupt-specific one-line steering ("this response was interrupted;
  the content above was produced before the interruption and was not
  delivered") that makes no provider-failure claim and does not push
  "continue".

**No salvage floor:** any nonzero salvageable text persists — a byte
threshold would discard data to fix a sentence. The steering wording
scales with size instead: a substantial partial gets "treat it as your
draft… do not start over"; a small fragment gets "a small fragment (N
bytes) was produced and not delivered", with no draft-reuse instruction.
("Nothing salvageable" in the gating rules means literally zero bytes.)

**Precedence for mixed rounds.** The exclusions key on the class of the
**salvage-producing (or consume-phase) group's** failure — never on the
round's last error. A chain walk can end on an open-phase fallback
rejection or a fallback-induced context-length error while the primary
group holds consume-phase failures and a salvageable partial
(last-error-wins, `agent/session_model_call.go:768-775`); that round
settles WITH salvage and consume-phase steering. A round whose only
failures are excluded classes persists nothing.

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
   - Metadata: model/provider provenance only. The turn must **never**
     carry Responses-continuation metadata — continuation anchoring scans
     backward to the most recent assistant turn
     (`agent/responses_continuation_eligibility.go:36-55`), and stamping
     IDs from a response the provider never finalized poisons every
     subsequent round with `previous_response_not_found`. Post-settlement
     rounds take the full-history path by design.
2. **The steering turn** (always, when settlement fires). Model-visible,
   via the existing `appendSteeringTurn` mechanism, composed from recorder
   stats:
   - What happened, from one generic consume-phase template parameterized
     by attempt count and shape — singular and plural must both read
     truthfully (a permanent mid-stream error settles after ONE attempt;
     "repeatedly" would be false). Shapes: stall ("the provider stopped
     responding mid-stream[, N times over M minutes]"), silent stall
     ("the provider accepted requests but streamed nothing"), cap ("the
     transport cut off your response after ~Xs of streaming[, twice]"),
     decoded in-band ("the provider reported: <decoded message>"). Every
     terminal-error class that can reach settlement maps to exactly one
     template.
   - When a salvaged turn precedes it: "Before the connection failed, you
     produced the response above. Any tool calls in progress did not
     execute, and nothing was delivered or saved. Treat it as your draft —
     re-send user-facing content through [result tool] and re-issue file
     writes in smaller pieces, reusing the draft rather than regenerating
     it. Do not start over."
   - Cap shape adds: "The transport cannot sustain responses that long.
     Keep each response well under that size and continue your work across
     multiple rounds."

**Group-transition reset:** `OnReset` fires only between attempts within
one group; nothing resets the screen between *groups*. A chain walk after
a partial-delivering group leaves the primary's dangling partial rendered
above the fallback's output (the projector's text-start handler abandons
the item without emitting a reset, unlike the reset handler). Before any
subsequent group streams, if a prior group in the round delivered partial
output, the session emits the same assistant-text reset the retry path
uses — this covers the chain-walk-then-success path, which never reaches
settlement.

**Delegate scope:** components 1–3 operate per session, children
included — a delegate child's failing turn salvages and steers in its own
transcript. Two parent-facing additions make that reachable: the child's
retry/unhealthy state surfaces into its job activity phase (today a
child's 10-minute grind reads as `awaiting model`, the exact opacity
component 1 kills for attached sessions), and a failed delegate result
notes when a salvaged draft exists ("partial draft salvaged in the child
transcript — resume it with delegate_send rather than re-dispatching"),
since re-dispatching a fresh child regenerates everything the child
already paid for.

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

**Compaction atomicity:** the salvaged turn + steering pair is
compaction-atomic — kept or folded together, never split across a
checkpoint boundary — and is protected as recent-tail for the immediately
following turn, since the cap-failure scenario correlates with maximal
context pressure and the steering's "the response above" must not point at
content compaction just summarized away.

## How the incident would have played out

1. **First failing turn (the stall group):** attempts 1–4 stall at ~32s
   each; the streak rule stops the group at ~2m10s with "provider
   unhealthy: repeated mid-stream stalls". The chip showed attempts and
   elapsed throughout. The salvaged text is a small fragment, so the
   persisted turn is tiny and the steering uses the fragment wording:
   provider unstable, a small fragment was produced, nothing delivered.
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

Once cross-provider fallbacks exist (separate spec), a configured escape
provider changes steps 1 and 2: the round continues there instead of
settling. Until then, fast settlement + salvage + steering is the whole
story.

## Testing

TDD throughout; per repo policy, all functionality covered.

- **Recorder:** partial + stats survive `consumeModelStream` error returns;
  per-attempt capture before `OnReset`; best-of-group and cross-group
  selection (fallback trickle never shadows primary partial); stats present
  for non-fail-fast terminal errors.
- **RetryStream:** open-phase failures are transparent (neither count nor
  reset — alternating stall/429 still trips at the 4th stall); streak at 4
  consume-phase failures including silent stalls; fast zero-content
  failures (decoded in-band rejections) treated as open-phase-equivalent;
  cap detection at 2 substantial consume-phase failures measured on the
  ≥60s content-event window (keep-alive-only minutes and
  stream-then-stall attempts do not qualify; `ErrSSEReadTimeout`
  discriminates the stall tail; a reasoning-heavy 5-minute stream cut at
  the cap qualifies despite little text); both rules disabled when
  `FailFastAfter` is 0; wrapper error references group stats; existing
  retry tests stay green.
- **Adapter (standalone, first):** openai-compat decodes in-band
  `{"error": ...}` chunks into typed llm errors carrying the payload's
  code/status; existing well-formed-stream and line-noise handling
  unchanged; captured meta-provider fixtures
  (`llm/providers/openaicompat/testdata/`) extended with an in-band error
  stream.
- **Fallback interaction:** `ProviderUnhealthyError` has an explicit
  non-eligible classification arm; a mid-chain unhealthy verdict aborts
  the remaining chain and survives as the terminal error; permanent-class
  and retry-after-declined eligibility unchanged — existing kata r128
  tests stay green.
- **Salvage content:** all top-level string fields extracted from partial
  tool args, labeled, under never-executed marker; no broken tool calls; no
  reasoning content; largest-by-total-bytes selection.
- **Classification:** reasoning deltas count as content events for phase
  classification but never for salvage; in-band-after-content is
  consume-phase and salvage-eligible; fast zero-content is
  open-phase-equivalent; zero-content stall (`ErrSSEReadTimeout`) counts
  toward the streak.
- **Settlement gating:** fires for consume-phase rounds; absent for
  cancellations, context-length, open-phase rejections, and
  content-filter classes (steering-only wording for the filter case);
  interrupts persist any nonzero salvage with interrupt-specific steering
  and no failure claim; no salvage floor — steering wording scales with
  fragment size instead;
  mixed-round precedence — primary consume-phase salvage survives a chain
  walk ending in an open-phase or context-length fallback error, and
  steering describes the consume-phase group with a fallback-also-failed
  clause; steering-only when nothing salvageable; wording matches failure
  shape.
- **Composer templates:** every terminal-error class that can reach
  settlement maps to exactly one template; singular/plural truthfulness
  (a one-attempt settlement never says "repeatedly"); fragment-vs-draft
  wording scales with salvage size.
- **Group transitions:** assistant-text reset emitted before a subsequent
  group streams when a prior group delivered partial output — including
  the chain-walk-then-success path; chip renders the `AttemptCap`
  denominator and a model tag on fallback groups.
- **Delegates:** child retry/unhealthy state visible in job activity; a
  failed delegate result notes when a salvaged draft exists.
- **Continuation:** the salvaged turn carries no continuation metadata;
  post-settlement rounds take the full-history path cleanly.
- **Compaction:** the salvaged turn + steering pair survives compaction
  atomically and is retained for the immediately following turn.
- **History:** salvaged turn + steering model-visible in `buildHistory`, in
  order, including after restore; `TurnFailure` stays model-invisible; a
  text-only assistant turn followed by steering forms valid provider
  history.
- **Live consistency:** settlement event sequence produces identical
  content in the appwire projection and the reloaded transcript, including
  the last-attempt-on-screen vs largest-persisted case and previously
  shown-then-reset communicate text.
- **Events/UI:** `GroupElapsedMS` plumbing; new clearing rules in TUI and
  web reducer tests; chip survives deltas AND systemMessage/user-item
  completions (a mid-grind steering injection must not clear it), clears
  on turn boundaries and model-output item completion.
- **Integration:** end-to-end fake-provider scenarios for both failure
  shapes (stall streak → fast settle, steering only; cap shape → 2-attempt
  stop, partial + steering persisted; resumed turn carries both in model
  history).
