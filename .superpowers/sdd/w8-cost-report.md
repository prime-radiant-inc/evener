# W8 completion: honest thread-level cost — report

**Status: DONE_WITH_CONCERNS**
**Commits:** `73de910f4..2a650004d` (3 commits) on branch `w8-cost` off `183271f0e`.
**Gate summary:** `go build ./...` OK; `go test ./internal/appprojector/... ./internal/apptranscript/... ./appwire/... ./cmd/serf-hub/... (+ ./server/...)` OK; `make lint` OK (7 modules); frontend `tsc` clean, `vitest` 3482 pass / 243 files (baseline 3478 → +4), `biome ci` clean, `npm run build` OK, `dist/PLACEHOLDER` restored. Working tree clean.

---

## Headline design decision (deviation — read first)

The task's BUILD step 1 said "sum the settled turns' costs from the FULL transcript." I implemented the **session cost total as `appwire.EstimateCost(model, SerfThread.Usage)`** — pricing the authoritative full-session **cumulative usage** once — rather than iterating and summing per-turn transcript costs. The task's EVIDENCE clause explicitly licensed this: *"mirror that pattern [workMillis] exactly for cost unless the evidence argues otherwise (state why if you deviate)"* and *"Whether SerfThread.Usage … already has an honest home for a cost total — extend the existing shape if one fits."* The evidence argues decisively for it:

1. **The workMillis precedent IS a session-level cumulative metric, not a transcript sum.** `SerfThread.WorkMillis` is stamped at the producer from a session metric (`entry.Meta.WorkMillis` past; `wmfn()` live), never summed from per-turn durations. Mirroring workMillis *exactly* therefore means: stamp `Cost` at the producer from the session's cumulative `Usage`. Per-turn transcript summing would itself *deviate* from the workMillis precedent.
2. **Legacy precedent.** The legacy details panel computed session cost as `EstimateCost(model, cumulativeUsage)` — `cmd/serf-hub/web_workspace.go` `tokensAndCostRows` (calls `appwire.EstimateCost(model, serfUsageFromCumulative(meta.CumulativeUsage))`). The rewrite **dropped** dollar cost precisely because `SerfThread` had no `Cost` field: `.superpowers/sdd/w5-close-t6-parity-sweep.md:161-162` ("dollar cost dropped (Go-side `EstimateCost`, never crosses the wire)") and `progress.md:155` ("cost-omission verified vs Go (SerfThread has no Cost field)"). This task **re-instates the dropped legacy behavior**, now backed by a wire field.
3. **Numerically the correct sum, without a rounding lie.** `llm.EstimateCost` (`llm/pricing.go:87`) is **linear** in tokens (`input·rate + cacheRead·rate + output·rate`, all `/1e6`). For a fixed model, `Σ EstimateCost(turnᵢ.usage) == EstimateCost(Σ turnᵢ.usage)`. The existing per-turn *past* stamping already prices every turn at the single session model (`app_threadread.go:287` `stampPastTurnCosts` uses `entry.Meta.Model`), so summing per-turn == pricing cumulative usage. Meanwhile a **naïve sum of the per-turn `"~$X.XX"` strings would under-count**: 100 turns of $0.004 each round to "$0.00" → sum "$0.00" vs real $0.40. Pricing the cumulative usage once avoids that entirely.
4. **Coherence.** Deriving cost from `SerfThread.Usage` guarantees the cost chip agrees with the token chip beside it (same authoritative cumulative). A transcript sum could disagree with the displayed cumulative tokens.
5. **Minimal surface, pagination-proof by construction.** No transcript re-read, no float-sum accumulator, no "did any turn contribute" bookkeeping; it never touches client-loaded turns, so the pagination lie the task forbids is impossible.

This is the same anti-lie the task demanded ("never a frontend sum of only-loaded turns") achieved more robustly.

---

## Evidence receipts (read before edit)

**1. Where per-turn cost lives on the wire / the existing formatter (REUSE, don't rewrite):**
- `appwire/types.go:503-510` — `Turn.Usage *SerfUsage` + `Turn.Cost string` (`"~$X.XX"`), nil/empty when not computable.
- `appwire/cost.go:31-47` — `EstimateCost(model, usage) string` returns `"~$X.XX"` via `llm.DefaultPrice`, **`""` when usage nil or model uncataloged** (the absent-vs-zero rule, reused verbatim).
- Live per-turn stamp: `internal/appprojector/appwire_projection.go:943-950` (`stampTurnUsage` → `EstimateCost(p.activeTurnModel, usage)`). Past per-turn stamp: `cmd/serf-hub/app_threadread.go:285-289, 323-329`.
- **Frontend "cost formatter" is a passthrough, not a function**: `panes/session/transcript/messages/turnMeta.ts:52-53` (`parts.cost = turn.cost`) and `reducer.ts:219` (`cost: turn.cost`) — the cost string is produced *server-side* and displayed verbatim. The status chip does the same; **no new formatter written**.

**2. The `workMillis` thread-aggregate flow (mirrored):**
- Field: `appwire/types.go:258-269` — `Usage`/`WorkMillis`/`ActiveTurnStartedAt` are "read on demand from the session via a pull callback rather than pushed on every event."
- Stamped at exactly two `SerfThread` producers: **live** `server/appwire_runtime.go:874-875` (`Usage:/WorkMillis:` from `wmfn()`), **past** `cmd/serf-hub/app_threadread.go:236-237` (`WorkMillis: entry.Meta.WorkMillis`, `Usage: serfUsageFromCumulative(entry.Meta.CumulativeUsage)`). (`appprojector` builds `SerfThread` only at `EventSessionStart` with Ref+Profile — it does NOT stamp metrics; `server_handlers.go:315-320` fills the separate legacy `StatusInfo`, not appwire.)
- Hydrate mapping: `reducer.ts:265` `workMillis: thread.serf.workMillis ?? 0`. **No dedicated live-update handler** — every reducer case preserves it via `...model` spread; the live clock is client-extrapolated (`statusFormat.ts:58-63 totalWorkMillis`). Cost mirrors this exactly (hydrate map + spread preserve; no per-token push, snapshot-refreshes on re-hydrate).

**3. Honest home for the total:** `SerfThread.Usage` (`types.go:300-309`) is the session's cumulative self-only token total (`serfUsageFromCumulative`, `web_workspace.go:562-576`; live `wmfn` = `server/server.go:192`). Cost is the deterministic `EstimateCost(model, Usage)` of it. Added `Cost string` as the **sibling of `Usage`**, mirroring `Turn.Usage`/`Turn.Cost` (they are siblings, not cost-inside-usage), rather than polluting the token-only `SerfUsage` struct.

---

## What was built

**Wire (`appwire`, sanctioned):** `SerfThread.Cost string \`json:"cost,omitempty"\`` (`types.go`) with a WHAT/WHY doc comment stating the absent-vs-zero semantics and pagination-proof rationale. `make generate` regenerated `cmd/serf-hub/frontend/src/protocol/types.gen.ts` (SerfThread gains `cost?: string`, mirroring `Turn.cost`). `docs/appwire-protocol.md` unchanged (it documents methods/params, not `SerfThread` fields — verified not stale via `git diff --exit-code`). **Generated twin committed.**

**Producers (stamp beside `Usage`):**
- Past: `cmd/serf-hub/app_threadread.go` `pastEntryThread` — hoisted `cumulativeUsage`, `Cost: appwire.EstimateCost(entry.Meta.Model, cumulativeUsage)`.
- Live: `server/appwire_runtime.go` `appThread` — `Cost: appwire.EstimateCost(status.Model, usage)`, refreshed each snapshot from the `wmfn` pull exactly like `Usage`/`WorkMillis`.

**Frontend model/reducer (sanctioned):** `model.ts` `cost?: string | null` (optional — see scope note) with a WHAT/WHY comment; `reducer.ts` `hydrateThread` maps `cost: thread.serf.cost ?? null`; `...model` spread preserves it elsewhere (workMillis treatment).

**StatusRow (sanctioned):** cost chip after the token/usage chip showing `model.cost` verbatim, `title={\`session cost ${model.cost}\`}` (the row's LocationCluster "key value" tooltip convention, `LocationCluster.tsx:54`), guarded `{model.cost && …}` so a falsy cost renders nothing. Reused the existing `.item` CSS class (no CSS change). **Replaced the now-false "Dollar cost is deliberately NOT shown" header block** (StatusRow.tsx:8-16) with the wire-backed rationale.

## Absent-vs-zero semantics (stated)
`Cost` is **absent** (`""` on the wire → omitted → `null` in the model → **no chip**) exactly when `EstimateCost` returns `""`: `Usage` is nil (no token data — old daemon / Codex / zero-usage) **or** the model is uncataloged. It is **present** only when there is real priced usage; the only `"~$0.00"` a user can ever see is a genuinely sub-cent priced session — an honest zero, never a fabricated one for an unknown.

## RED evidence
- appwire: `go test ./appwire/ -run TestSerfThreadCost` → `unknown field Cost in struct literal of type SerfThread` (compile-RED) → green after field.
- past producer: `TestPastEntryThread_CarriesCostTotal` → `thread.Serf.Cost = "", want non-empty "~$1.00"` → green after stamp.
- live producer: `TestServerAppWireThreadReadIncludesCostTotal` → `cost="", want non-empty "~$1.00"` → green after stamp.
- reducer: `tsc` → `Property 'cost' does not exist on type 'ThreadModel'` (RED) → green after model field + mapping.
- StatusRow: `getByTestId("status-row-cost")` → `Unable to find an element` (RED) → green after chip.

## Mutation proofs
- **Absence net (StatusRow):** mutating the guard `{model.cost && …}` → `{true && …}` fails **both** absence tests ("no cost chip when … null", "… undefined"); reverted. Proves the honest-absence guard is load-bearing.
- **Present-value net:** `TestPastEntryThread_CarriesCostTotal` / `…IncludesCostTotal` assert `Cost == EstimateCost(model, Usage) && != ""` AND the uncataloged-model case asserts `Cost == ""` — a stamp that dropped the model arg or ignored the price would fail.
- **Omit net:** `TestSerfThreadMetricsOmitEmpty` extended to ban `"cost"` on a zero `SerfThread`.

## Concerns
1. **Design deviation (documented, licensed):** cost = `EstimateCost(model, cumulativeUsage)` (legacy + workMillis-mirror) rather than literal per-turn transcript summing. Justified above; numerically equivalent for a fixed model, without the per-turn-rounding under-count. Flagging because BUILD step 1's wording differs — trivially redirectable if the controller wants transcript iteration.
2. **Scope stretch (necessary):** `SerfThread.Cost` can only be stamped where `Usage`/`WorkMillis` are — `cmd/serf-hub/app_threadread.go` (in the `go test ./cmd/serf-hub/...` gate) and `server/appwire_runtime.go` (NOT in the enumerated sanctioned set nor the Go test gate). Both edits are one-line additive stamps mirroring the adjacent `Usage:` line, plus focused RED-first tests; `./server/...` was gated manually (green). These are the actual appwire.Thread producers the "Go producer files" intent points at. `model.ts cost` is **optional** (`cost?: string | null`, like `activeTurnStartedAt?`) so no non-sanctioned ThreadModel constructor breaks — making it required would have forced edits across ~17 out-of-scope test files.
3. **Live refresh cadence:** cost, like usage/workMillis, is snapshot-authoritative — it refreshes on the next `thread/read` (re-hydrate), not per token. This is the exact existing usage/workMillis behavior (mirrored precedent), not a regression; there is no live-push wire candidate for these thread aggregates yet.
