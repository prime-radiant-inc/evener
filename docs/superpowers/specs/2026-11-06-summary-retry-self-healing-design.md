# Summary retry self-healing — design

**Date:** 2026-11-06
**Status:** Approved by Jesse 2026-11-06; PAR-resolved the same day (two
independent reviewers; findings addressed: honest per-call cost model covering
condensation batches and the fork's per-round cost, per-route attempt budget
with reworded latch claim, per-route-deduplicated `Previous`, explicit
cancellation rule, diagnostic type-contract comment in scope, attempt-group
test pin named).
**Owning guide:** `docs/product/history-content.md` § Summary failure diagnostics
**Subsystem rows:** `docs/product/subsystems.md` S09 (session execution), S21 (diagnosis, unchanged contract)

## Problem

A session summarizing on `lunaroute/deepseek-4.1-flash-abliterated` failed with:

```
Summary failed for session model lunaroute/deepseek-4.1-flash-abliterated: rejected_output, empty_reply
The response could not safely replace the conversation. Retry or choose a more capable summary model.
```

An empty reply is a *successful* API call whose content fails local validation, so
none of evener's existing recovery reaches it: the LLM client retries only
transport-class errors (rate limits, 5xx, mid-stream truncations), the cheap-model
caller falls back only on provider *refusals*, and `completeSummarization`'s one
recovery move is a single attempt on the other route — which, when no separate
cheap model is configured, is the route that just returned empty. One bad sample
fails the fold and the warning tells the user to do what the product should do
itself. Abliterated and small flash models return empty or non-summary replies
often enough that this is a recurring failure, not a rare one.

What already works and stays: a failed fold leaves history untouched, the
deterministic checkpoint stands, the turn continues, and the next eligible
compaction retries the whole fold.

## Ruling (Jesse, 2026-11-06)

- Up to **two same-route retries** (three attempts per route) for locally
  rejected summarizer replies, before the existing route fallback.
- The **session-log fork summary** (`forkSummarize`) gets the same treatment in
  this change, not a follow-up.

## Scope

1. **`completeSummarization`** (`agent/internal/contextmgr/summarize_route.go`):
   a bounded retry loop per route rung.
   - Retryable rejections — failures of a *sample*, where a fresh sample
     plausibly passes: `empty_reply`, `missing_summary_section`, `empty_batch`.
   - Non-retryable rejections keep today's single attempt per route:
     `output_limit` (the reply does not fit the model's own output cap; a
     resample hits the same wall — the existing rung fallback and hint advice
     stay the remedy), `uncondensable_partner_messages`, `missing_model`.
   - Retry = re-issue the same request to the same route, immediately, no sleep.
     Rung 1 retries by calling `CompleteConfigured` again; rung 2 retries
     `cm.client.Complete` on the session route again.
   - The attempt budget is **per resolved route**, not per rung: each distinct
     (provider, model) route answers at most three attempts inside one
     `completeSummarization` call. A refusal latch landing between rung-1
     retries (rejections never latch; a concurrent caller through the same
     session-scoped `Caller` can) moves the remaining budget to the session
     route exactly as a first-attempt refusal would — the reroute is the
     cheap caller's own fallback, and `ranSessionModel` still suppresses the
     separate session rung.
   - Rung transitions otherwise unchanged: a rejection the route could not
     recover from falls to the next rung as today; permanent
     invalid_request/not_found/access_denied API errors still trigger the
     session rung; transient API errors still end the rung (client-level
     retries already apply to each attempt); `ranSessionModel` still suppresses
     the second rung.
   - API-attempt settlement is unchanged: `SettleResult` receives the last
     attempt's `apiErr` only, never a local rejection.
2. **Diagnostics** (`summary_failure.go`, `diagnostic/summary.go`): failed
   attempts feed `SummaryFailure.Previous` with **at most one entry per
   distinct route — the last failed attempt on it** — still capped at two
   entries; `Terminal` is the last attempt. Per-route deduplication preserves
   what the cap exists for: on a total failure the operator still sees that
   the cheap route was tried (recording every retry instead would let the
   two-newest cap push every cheap attempt out of view). Field sets and the
   wire shape are unchanged; `Previous`'s meaning is "prior failed routes,
   latest attempt each", and the type's contract comment in
   `diagnostic/summary.go` says so after this change.
3. **Hint copy** (`diagnostic/summary.go`, `rejected_output` case): no longer
   tells the user to retry — evener has. States that evener retried
   automatically and that a more capable summary model helps if it repeats.
   Tests keep asserting structured fields, never this copy.
4. **`forkSummarize`** (`agent/internal/contextmgr/fork_summarize.go`): up to
   three attempts on a reply that is empty or fails JSON parsing (the same
   sample-variance class). API errors keep today's single attempt (client
   retries and the cheap caller's refusal fallback already apply). The
   retry-exhausted error is bounded: it does not embed the raw reply — this
   error surfaces as a warning via AfterAction, and warnings never carry
   conversation-adjacent text. This replaces today's
   `failed to parse JSON ... (raw: %s)` construction, which does leak the reply
   into warnings; noted in the PR description.
5. **Docs** (same change): `history-content.md` § Summary failure diagnostics —
   the "retry counts unchanged" sentence becomes the new retry contract, the
   hint advice sentence updates, and `Previous` is described as prior failed
   attempts; `subsystems.md` S09 — "with no new retry policy" is replaced by the
   bounded rejected-reply retry. S21's contract (bounded records, no provider
   bodies) is untouched and still holds.

## Non-goals

- No change to the LLM client's transport retry policy, the cheap-model refusal
  latch, route order, or API-error fallback eligibility.
- No cross-turn backoff or warning deduplication: a persistently broken summary
  model still warns on each failed compaction, and the next eligible compaction
  still retries the whole fold. Repeated-failure visibility is a separate
  product decision if it ever hurts.
- No new configuration surface for retry counts.
- No wire-schema or client-renderer changes; the recorded
  `summaryfailurewire` fixture is regenerated only if its producer scenario's
  output actually changes (verify; its failure may be an API error, which this
  change does not alter).

## Cost and interaction notes

- The retry budget is per **summarizer call**, not per fold. A fold's summarizer
  calls are: partner-message condensation, which runs up to 4 rounds over
  batches (one `completeSummarization` per batch), then the main summary. Each
  such call now makes at most 6 attempts (3 per route) where it made at most 2.
  Worst-case fold cost is therefore 6 × (batch calls across condensation
  rounds) + 6 calls, not a flat six; success paths still make exactly one call
  per summarizer call per route.
- `forkSummarize` runs from `SessionLogStrategy.AfterAction` after every
  completed tool round: a persistently junk-replying model now costs up to 3 ×
  `AdapterTimeout` per round in the degraded case, where today costs 1 ×. The
  retry only fires on a bad sample, so a healthy model's rounds are unchanged.
- Each attempt carries the same `AdapterTimeout`; a pathological summarizer
  call spends attempts × timeout before failing. The turn then continues as
  today.
- No retry once the context is cancelled (`ctx.Err() != nil`), so a cancelled
  run unwinds within one in-flight attempt, matching the client's own
  fail-fast on cancellation.
- A retry that succeeds recovers silently: no warning, no user action.

## Test plan (red-first)

Package: `agent/internal/contextmgr` (primary), `agent/diagnostic` (copy path only).

1. New `completeSummarization`/`summarizeFold` behavior tests via scripted
   adapters, asserting call sequences and stored results:
   - empty reply ×2 then valid → success, exactly 3 calls on one route, none on
     the other (Jesse's single-route config).
   - empty ×3 on the cheap route → session rung runs; session valid → stored;
     call order cheap×3 then session×1.
   - empty ×3 on both routes → failure; `Previous` holds exactly one entry per
     earlier route (the last failed attempt on it), so the cheap route stays
     visible; `Terminal` is `rejected_output`/`empty_reply` on the session
     route; 6 calls total.
   - single-route config exhausting its attempts → `Previous` holds that
     route's own prior attempt, showing the retry happened.
   - `missing_summary_section` and `empty_batch` retried the same way;
     `output_limit` (`FinishReasonLength`) makes exactly one call per route and
     still falls through (pins the non-retryable boundary).
   - valid first reply → exactly one call (no retry tax on success).
   - a cancelled context between attempts makes no further attempts.
2. Update existing pins to the new attempt structure:
   `TestSummarizeWithLLM_RejectedCheapReplyFallsBackToSessionModel` (cheap
   rejection now retries before the session call), `TestSummarizeWithLLM_RejectsNonSummaryReplies`
   (scripted single replies become steady responders; assertion stays
   "fold fails"), `TestSummaryFailureValidationPreservesAPISuccess` (six
   attempts in one logical group; `assertContextManagerLogicalGroup` gains an
   attempt-count parameter while pinning the one-group and
   success-settlement invariants), and the `TestSummaryFailureRoutingAndRecovery`
   call-order/Previous pins.
3. `condensePartnerMessages`: an empty batch reply twice then a valid one
   completes the condensation within one batch's retry budget.
4. `forkSummarize`: "not JSON" twice then valid JSON → entry parsed, 3 calls;
   all attempts bad → error is present and does not contain a raw-reply canary
   (privacy boundary assertion, not a copy oracle).
5. Docs: updated sections present; no test asserts their prose.
6. Gates: `go test ./agent/internal/contextmgr ./agent/internal/cheapmodel
   ./agent/diagnostic ./agent/events -count=1` (plus `-race` on the touched
   new tests), `make vet`, lint on touched files; CI owns the full suite.

## Acceptance

- A summary model that intermittently returns empty or non-summary replies
  recovers inside the same compaction pass with no warning and no user action;
  a single-route (no cheap model) config recovers on the second or third
  sample.
- A failure that survives all retries produces the same bounded
  `summary_failure` warning as today, with the last failed attempt of each
  earlier route visible in `Previous`.
- `output_limit`, `uncondensable_partner_messages`, `missing_model`, and every
  API-error path behave exactly as before this change.
- Success paths make no additional calls.
- `forkSummarize` parses a reply that succeeds after bad samples and never
  embeds the raw reply in its error.
- Product docs state the new contract.
