# W8 completion: honest thread-level cost — adversarial review

**Range:** `183271f0e..abcf669f3` (branch `w8-cost`, 4 commits).
**Verdict: APPROVED.**
**Deviation verdict: ENDORSE** (with one Minor honesty-disclosure note).

## Gate summary (all re-run by the reviewer, all green)
`go build ./...` OK · `go test ./internal/appprojector/... ./internal/apptranscript/... ./appwire/... ./cmd/serf-hub/... ./server/...` OK · `make lint` PASS (7 modules) · `npx tsc --noEmit` OK · `npx vitest run` **243 files / 3482 tests passed** (exact match to expected) · `npm run lint` (biome, 667 files) clean · `npm run build` OK, `dist/PLACEHOLDER` restored · `make generate` **zero drift** · working tree clean.

---

## Deviation analysis (P1) — the central question

The brief said "sum the settled turns' costs from the transcript." The implementer stamped `Cost = appwire.EstimateCost(model, SerfThread.Usage)` at the two thread producers instead. **Every leg of the implementer's evidence is independently verified true**, and the evidence is decisive:

**(a) workMillis genuinely is a producer-stamped session cumulative, not a transcript sum.** Verified: the past producer stamps `WorkMillis: entry.Meta.WorkMillis` (a persisted session metric) and the live producer stamps it from `wmfn()` (`server/appwire_runtime.go`). Nothing sums per-turn durations. Mirroring workMillis "exactly" (which the brief instructed) therefore *means* producer-stamping a session cumulative — a per-turn transcript sum would itself have deviated from the precedent.

**(b) The legacy session cost really was `EstimateCost(model, cumulativeUsage)`.** Verified at `cmd/serf-hub/web_workspace.go:346`: `tokensAndCostRows(meta.Model, serfUsageFromCumulative(meta.CumulativeUsage))` → `appwire.EstimateCost(model, usage)` (line 254). The new past stamp `EstimateCost(entry.Meta.Model, serfUsageFromCumulative(entry.Meta.CumulativeUsage))` is **byte-for-byte the same computation** the legacy details panel performed. This change re-instates the exact dropped behavior, now wire-backed. The deviation is only against the brief's *wording*, never against legacy behavior.

**(c) `llm.EstimateCost` is linear in tokens.** Verified at `llm/pricing.go`: `in·InputPerM/1e6 + cacheRead·rate/1e6 + out·OutputPerM/1e6`. For a fixed price, `Σ EstimateCost(turnᵢ) == EstimateCost(Σ turnᵢ)`. Two further facts make the deviation *equal-or-better* than the literal brief for the persisted path, and clarify the one place it is weaker:

- **Past transcript sum would be numerically identical.** The only past per-turn cost stamp available (`app_threadread.go:293`; `stampPastTurnCosts` line 332) prices *every* turn at the single `entry.Meta.Model`. By linearity, summing those equals `EstimateCost(entry.Meta.Model, Σ turnᵢ.usage)` = the implementer's number — and strictly *avoids* the per-turn rounding under-count (100×$0.004 → "$0.00" summed to "$0.00" vs the true $0.40). So for past threads the deviation is provably equal-or-better than the literal brief.
- **The data model forbids anything more honest at the aggregate.** `schema.CumulativeUsage` (`agent/schema/snapshot.go:111`) is a **flat** total — `InputTokens/OutputTokens/CacheReadTokens/TotalTokens`, **no per-model breakdown**. Combined with a single `Meta.Model`/`status.Model`, there is no data from which to price a model-switched session per-model at the thread aggregate. The honest per-model figure is simply not representable from the persisted state.

**Mid-session model switch — stated honestly, with severity (see Minor-1).** The live producer prices the flat cumulative usage at `status.Model` (the *current* model). The live *per-turn* stamps, by contrast, use `p.activeTurnModel` (`internal/appprojector/appwire_projection.go:949`) — genuinely per-turn. So after a live mid-session switch, the aggregate prices old-model tokens at the new model's rate, and a hypothetical *live* per-turn sum would be more faithful. This mispricing is real and can be large (Opus↔Haiku ≈ 10–15×). It is nonetheless **not a code defect**: it exactly reproduces legacy behavior (no regression), it is estimate-marked ("~"), fixing it in code would break coherence with the adjacent token chip (which shows the same flat cumulative) *and* still be impossible for the persisted/past path, and the alternative the brief named cannot help past threads at all. The one legitimate ding is disclosure, not code — see Minor-1.

**ENDORSE.** Producer-stamped cumulative is the correct call: it is what "mirror workMillis exactly" and "re-instate the legacy formula" both require, it is coherent-by-construction with the token chip (same `usage` value feeds both within one snapshot), it is pagination-proof, and for the persisted path it is provably equal-or-better than the literal transcript sum. The implementer flagged the deviation loudly and it is redirectable; it should not be redirected.

---

## Probe results

- **P1 (deviation):** ENDORSE. (a)/(b)/(c) all verified true; the flat `CumulativeUsage` + single-model data model makes the deviation equal-or-better than the literal brief for past threads and forecloses any more-honest aggregate. Model-switch mispricing is real but inherent, not-a-regression, estimate-marked → Minor-1 (disclosure only).
- **P2 (producers):** PASS. Exactly two producers stamp a real `Usage` — past `app_threadread.go:242/243` and live `appwire_runtime.go:874/875` — and each stamps `Cost` on the very next line from the same usage value. Swept all nine `SerfThread{}` literals: the other seven (appprojector session-start, local_daemon roster, codex, four lifecycle stubs) carry no Usage, so correctly no Cost. RED-first tests present at both producers; `go test ./server/...` re-run green.
- **P3 (absent-vs-zero):** PASS. Guard is `{model.cost && …}`; chip absent when cost is null/undefined/"". Re-ran the mutation `{true && …}` live — **both** absence tests fail, and the failure output shows exactly what the guard suppresses: `title="session cost null"` / `title="session cost undefined"`. Tree restored clean. The only "~$0.00" reachable is a genuinely sub-cent priced session.
- **P4 (wire hygiene):** PASS. `make generate` zero-drift; `types.gen.ts` twin (`cost?: string`) committed. `SerfThread.Cost` doc-commented with format/currency ("estimated dollar total", "~$X.XX") per EstimateCost's contract. Reducer maps `cost: thread.serf.cost ?? null` (wire-true). `cost?: string | null` optionality cannot render "undefined"/"null" — the guard short-circuits before the title string is built (proven by the P3 mutation). Hydrate with usage-present/cost-absent renders the token chip + no cost chip — the intended uncataloged-model honesty, not a masked stamp. `docs/appwire-protocol.md` correctly unchanged (it documents methods/params; it does not enumerate SerfThread fields — grep confirms no usage/workMillis/cost entries).
- **P5 (refresh cadence):** PASS. `model.ts` comment states "Snapshot-only like usage/workMillis … refreshes on the next thread/read." Matches the precedent exactly: both `usage` and `cost` are set only in `hydrateThread` (adjacent lines) and preserved everywhere else via the `...model` spread; no live push for either. Chip and token gauge read the same SerfThread snapshot, so they go stale and refresh together.
- **P6 (design system):** PASS. Reuses `CLASS.item` (no CSS change), no new formatter (server string shown verbatim), `requireClass` retained, title follows the LocationCluster "key value" convention (`session cost ~$1.23`). The now-false "Dollar cost is deliberately NOT shown" header block is replaced with the wire-backed truth. (Note: the cost chip carries a title where the sibling usage/work chips do not — a defensible scope-disambiguation, consistent with the LocationCluster pattern; not a defect.)
- **P7 (gates):** PASS — see gate summary above; all re-run by the reviewer.

---

## Findings

### Critical
None.

### Important
None.

### Minor
1. **Model-switch mispricing is undisclosed in the honesty framing.** The `SerfThread.Cost` doc comment and the report call this "the honest full-session total" and "an honest 'unknown' … never misleading," but neither discloses that a mid-session model switch prices the entire flat cumulative usage at one model — the single scenario where the number is materially inexact. The value itself is correct-as-designed (legacy parity, estimate-marked, unfixable at the aggregate given the flat `CumulativeUsage`), so this warrants a one-line caveat in the doc comment, **not** a code change. Recommend adding to the `SerfThread.Cost` comment a note like: "priced at the thread's current model; a mid-session model switch prices all cumulative tokens at that one model (an estimate, matching the legacy details panel)." Non-blocking.

## Conclusion
The implementation is correct, minimal, well-tested (RED-first at both producers, live-verified mutation on the absence guard), coherent with the token chip by construction, and pagination-proof. The producer invariant "wherever Usage is stamped, Cost is stamped from the same value" holds across the whole codebase. All gates green. The deviation from the brief's literal wording is the right architectural choice and is endorsed. APPROVED.
