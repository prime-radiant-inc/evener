# Provider-failure feedback: triaged follow-ups

Follow-up batch to the merged 13-task provider-failure-feedback plan.
Spec (normative): `docs/superpowers/specs/2026-08-07-provider-failure-feedback-design.md`
(v7 plus the post-merge amendments in commits b411e7f69 and 23104745c).

## Global Constraints

- TDD: write the failing test first, watch it fail for the right reason, then
  implement. No exceptions for "trivial" tasks.
- The spec is normative. Where this plan and the spec disagree, the spec wins —
  raise the conflict rather than guessing.
- Never edit anything under `docs/superpowers/specs/**`.
- Test output must be pristine. Expected error output must be captured and
  asserted, never allowed to leak into passing test logs.
- Never `git add -A`. Stage named paths only.
- Smallest reasonable change. No backward-compatibility shims.
- Go tests: `go test ./llm/... ./agent/ -count=1` must be green for Go tasks.
- Lint: `golangci-lint cache clean` before running `golangci-lint run` in a
  worktree (the cache is shared across worktrees).
- Each task is file-disjoint from its siblings. Do not touch files outside
  your task's stated scope.

---

## Task 1: trickle-stall fragment-salvage e2e

**Files:** `agent/provider_failure_e2e_test.go` (test only; no production change
expected)

`agent/provider_failure_e2e_test.go` already covers two silent-stall shapes:
zero salvage (steering-only, no salvaged assistant turn) and the cap shape.
It does not cover the shape from the real incident: the model streams a
*trickle* of content deltas, then stalls. That produces `PhaseConsume`
failures with a small but nonzero salvage.

Add an e2e test for that shape asserting:

1. The verdict is a `*llm.ProviderUnhealthyError`.
2. A salvaged assistant turn **is** persisted (nonzero salvage is salvage —
   the spec resolution at b411e7f69 says "nothing salvageable" means literally
   zero bytes, never "too small to bother").
3. The salvaged turn / steering text uses the FRAGMENT wording, i.e. it tells
   the user a small fragment of N bytes was produced and not delivered.
   Find the exact production wording in `agent/failure_steering.go` and assert
   against the real string — do not invent wording, and do not change
   production wording to match a test.
4. The steering text makes **no draft-reuse claim** for this shape (the
   fragment is too small to be a reusable draft; the zero-salvage and
   fragment shapes must not promise the model a draft it can continue from).

If the production code turns out not to produce the fragment wording for this
shape, that is a real bug: fix the production code, and say so in the report.

**Verify:** `go test ./agent/ -run 'ProviderFailure|Stall|Salvage' -count=1`
then the full `go test ./llm/... ./agent/ -count=1`.

---

## Task 2: continuation-recovery unhealthy guard

**Files:** `agent/session_model_call.go`, `agent/session_model_call_unhealthy_test.go`

Two related defects around `agent/session_model_call.go:766` (the
Responses-continuation full-history recovery re-call inside
`callModelWithFallback`):

**(a) The recovery re-call is not guarded by the provider-unhealthy check.**
The fallback-chain loop below it checks for `*llm.ProviderUnhealthyError`
(see `agent/session_model_call.go:892`) and stops — an unhealthy verdict means
`RetryStream` already decided the provider is sick, and re-calling it burns
another full retry group against a provider we just declared unhealthy. The
continuation-recovery re-call has no such guard. Add it: when the primary
group's error is a provider-unhealthy verdict, do not issue the
continuation-recovery re-call.

**(b) `ProviderUnhealthyError.Unwrap` lets `errors.As` match *through* the
verdict.** `llm/provider_unhealthy.go:29` returns `e.LastErr`. So an
`errors.As(err, &someOtherErrType)` performed on a wrapped attempt error can
succeed *through* an unhealthy verdict, and the caller then treats a verdict as
if it were the raw attempt error. Audit the `errors.As` call sites that run on
a terminal model-call error — at minimum
`agent/session_model_call.go`, `agent/failure_steering.go`,
`agent/session_init.go:1174` and whatever
`shouldRetryResponsesContinuationAsFullHistory` uses — and fix any site where
matching through the verdict produces wrong behavior. Report the sites you
audited and why each is or is not affected.

Do not change `Unwrap`'s semantics without saying why in the report;
`llm/provider_unhealthy_test.go` pins the current behavior.

**Tests:** in `agent/session_model_call_unhealthy_test.go`. A failing test
first: a scenario where the primary group ends in an unhealthy verdict and the
request is continuation-shaped, asserting exactly one model call is made (no
recovery re-call). Plus a test for whatever unwrap-leak site you find real.

**Verify:** `go test ./llm/... ./agent/ -count=1`.

---

## Task 3: accumulator quadratic partial rebuild

**Files:** `llm/stream_accumulator.go`, `llm/stream_accumulator_test.go`
(or a new `_test.go` in `llm/`)

`llm/stream_accumulator.go` rebuilds the entire partial `Response` on *every*
content delta — see the `a.partial = a.buildResponse()` calls at roughly
lines 65 (text delta), 97 (tool-call delta) and 119 (tool-call end), plus the
finish path. `buildResponse` copies the accumulated reasoning and text, so a
long reasoning stream costs O(n²) in total across a call.

Replace eager rebuild with a dirty flag plus lazy build inside
`PartialResponse()`: mutating paths mark dirty, `PartialResponse()` rebuilds
at most once per dirty round and caches. Cover **all** the mutation sites,
including the finish path where `a.partial` is set from `ev.Response`.

Semantics must not change: `PartialResponse()` returns what it returns today
for every event sequence. If `PartialResponse` currently hands out an
internal pointer that callers might mutate, preserve today's behavior — this
is a performance change only.

**Test:** prove a single rebuild per snapshot round — e.g. a counter on the
build path (test-visible), or a Go benchmark showing the flattening. A test
that asserts "N deltas then one PartialResponse() call performs exactly one
build" is the minimum.

**Verify:** `go test ./llm/... -count=1`.

---

## Task 4: TUI retry chip

**Files:** `cmd/evener-tui/` only (`composer_render.go` and its tests; other
`cmd/evener-tui/` files only if the chip logic lives elsewhere)

The spec (as amended by Jesse in commit 23104745c, spec line ~122) renders
the retry chip with **em-dash** separators on both surfaces:

```
provider error — attempt 3/4 — retrying in 32s — 14m on this call
provider error — attempt 3/4 — in progress — 14m on this call
```

Three changes to the TUI chip:

1. Switch chip separators from `·` to `—` (em-dash).
2. `AttemptCap == 0` fallback: render the attempt without a denominator
   (`attempt 3`), never `attempt 3/0`.
3. Add the streaming-state variant: `retrying in <d>` while waiting out the
   backoff, `in progress` once the delay has expired or a delta has arrived.
   The client derives this locally from `DelayMS` — see the spec's
   Component 1 section.

Update the existing tests and add cases for the new behavior. Do not touch
files outside `cmd/evener-tui/`.

**Verify:** `go test ./cmd/evener-tui/... -count=1`.

---

## Task 5: web retry chip alignment

**Files:** `cmd/evener-hub/frontend/` only

The web `LivenessLine` already uses em-dashes. Verify it matches the spec's
string **exactly** — field order, wording, and the streaming variant:

```
provider error — attempt 3/4 — retrying in 32s — 14m on this call
provider error — attempt 3/4 — in progress — 14m on this call
```

Also confirm the `AttemptCap == 0` case renders without a denominator, and
that the model-identity tag renders when the retrying model differs from the
session's primary (spec Component 1).

Fix every divergence you find; where it already matches, add or tighten the
test that pins the exact string so it cannot drift. Do not touch files
outside `cmd/evener-hub/frontend/`.

**Verify:** the frontend test command from `cmd/evener-hub/frontend/package.json`.

---

## Task 6: openaicompat in-band error dedupe

**Files:** `llm/providers/openaicompat/response.go` (and whatever in that
package references the private type)

`llm/providers/openaicompat/response.go:144` defines a private
`chatInbandError` with a `statusCode()` method. The same payload shape is now
exported as `InbandError` with `StatusCode()` in
`llm/providers/internal/openaichat/openaichat.go:112` (added by the adapter
fleet; already used by the openai provider).

Delete the private copy and use the exported type. Behavior must be
identical — `llm/providers/openaicompat/adapter.go:410` matches on the
`interface{ StatusCode() int }` shape, so the exported method name is what
that path already expects.

`llm/providers/openaicompat/inband_error_test.go` must stay green **without
being modified**. If it needs modification, stop and report — that means
behavior changed.

**Verify:** `go test ./llm/providers/... -count=1`, and confirm
`git diff --stat` shows no change to `inband_error_test.go`.

---

## Task 7: agent/ hygiene and hardening (Wave B — runs after Tasks 1-6 merge)

**Files:** `agent/session_stream.go`, `agent/round_recorder.go`,
`agent/round_recorder_test.go`, `agent/salvage.go`, `agent/salvage_test.go`,
`agent/jobs.go`, `agent/session_events.go`,
`cmd/evener-hub/frontend/…/reducer.test.ts`

Four independent sub-items. Commit them separately.

**(a) FailFastAfter constant.** `agent/session_stream.go` has the value 4 in
two places — `modelRetryFailFastAfter` (line ~60) and the literal in the
`callModel` retry-policy construction (line ~144 area). Verify whether they
are genuinely two literals; if so, collapse to the one shared const so the
chip denominator cannot drift from the actual budget. If they already share
the const, say so and skip.

**(b) round_recorder hardening.**
- Pin the case where the round aborts *before* any model call, so the round's
  recorder is EMPTY: assert the settlement path handles it (no salvage, no
  panic, no phantom assistant turn). Test in `agent/round_recorder_test.go`.
- `BestSalvage` / the steering-group accessor return an interior pointer into
  the recorder's `Groups` slice. A later `append` can relocate the backing
  array under a caller holding that pointer. The existing call sites append
  whole groups and don't hold across appends, so a **comment** documenting
  the constraint is the fix — no restructure.

**(c) salvage scanner tests.** Add two cases to `agent/salvage_test.go`:
a nested brace *inside a JSON string* (`{"a":"}{"}` — the scanner must not
treat the braces in the string as structure), and a duplicate top-level key.
Assert the real current behavior; if the scanner gets either wrong, that is a
bug — fix it and say so.

There is an organizational inversion: `agent/session_stream.go`'s older
function calls into the newer `agent/salvage.go` helper. A comment noting
where the salvage logic actually lives is an acceptable fix; do not
restructure.

**(d) comment sweep.**
- `agent/jobs.go:245` names a latch that has since been renamed — fix the name.
- `agent/session_events.go:549` may carry a stale old-method comment; check
  and fix, or report it already correct.
- The `reducer.test.ts` comment at ~line 3617 narrates history ("used to…")
  instead of describing behavior — rewrite it to state what the test asserts.
- Document on the `contentWindowClock` seam **why** it is deliberately not
  the session's `clock.Clock`: fuzz harnesses inject a fake clock, and using
  it here would make every attempt read as cap-shaped.

Line numbers are as-of the branch head; find the real sites by name, not by
line number.

**Verify:** `go test ./llm/... ./agent/ -count=1` plus the frontend test for
the reducer file.
