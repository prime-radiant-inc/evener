# Provider Registry Step 4: Cloud Live Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the registry's cloud rows against real endpoints (spec §13 "Live"), update the `[1m]` rows to GA, and close the two live-found issues (#714 minimal-budget floor, #715 empty-text extractions) — using only the credentials actually available on this machine, lightly, under an isolated fake homedir.

**Architecture:** No new subsystems. Live tests extend the existing `EVENER_LIVE_TESTS=1`-gated files (`cmd/evener/models_live_test.go`, `llm/integration_smoke_test.go`); every run uses a scoped fake `HOME` with explicit `EVENER_PROVIDERS_CONFIG`/`EVENER_CREDENTIALS_CONFIG`/`EVENER_STATE_DIR`; credentials come from `~/git/prime-radiant/serf/.env` (Anthropic, OpenAI, Gemini, OpenRouter, Moonshot) plus the store (kimi, lunaroute, openrouter) and the local AWS CLI. Code changes are data (overlay rows), one effort-floor fix, and whatever #715's diagnosis demands.

**Tech Stack:** Go 1.27 go.work workspace; `llm/registry` overlay + models.dev snapshot; the four protocol packages.

**Spec:** docs/superpowers/specs/2026-08-28-provider-registry-design.md — §9.2–9.4 (cloud transports), §13 "Live" (`[1m]`, Groq Responses, Bedrock `global.` routing, Kimi K3 thinking shape, MiniMax M3 budget object, one request per cloud transport), §15 (Bedrock bearer-only; Vertex/Bedrock counting estimate-only).

## Global Constraints

- **Light use**: one request per (instance, model, property) — smoke-sized prompts ("Reply with the single word: pong"); never loops of live calls; every live test skips (never fails) when its credential is absent.
- **Credentials never printed, logged, committed, or placed in argv**: keys load from `~/git/prime-radiant/serf/.env` / the store / `aws` into process env only; test output shows sources and header NAMES only (the existing `models_live_test.go` discipline).
- **Isolated fake homedir for every live run**: `HOME=$(mktemp -d)` with explicit `EVENER_PROVIDERS_CONFIG`, `EVENER_CREDENTIALS_CONFIG`, `EVENER_STATE_DIR`; the developer's real `~/.config/evener` and `~/.local/state/evener` are never read or written by live tests.
- Offline suites stay offline: nothing gated on `EVENER_LIVE_TESTS` may run in the normal gate; `make lint` 9/9; the full gate green apart from the accepted roster (agent/sandbox bwrap 2/7; root-fuzz appwire fixture test).
- Flag day (spec §14.1) still binds: no compat shims in any code change.
- Unavailable credentials (Azure, Groq, MiniMax-direct, Vertex-without-ADC) produce a SKIPPED row in the report naming exactly what is missing — never a silent gap.

## File Structure

- `cmd/evener/models_live_test.go` — extend `liveSmokeModels` + add per-property live tests ([1m], thinking shapes, budget objects, Bedrock global routing).
- `llm/registry/data/providers_overlay.toml` — `[1m]` GA rows; any Bedrock/vertex row corrections live testing exposes.
- `llm/types.go` (or wherever `ReasoningBudget` lives) + tests — #714 floor.
- `#715`: diagnosis decides (likely `llm/providers/chatcompletions` response folding); tests beside it.
- `docs/llm-providers.md`, the spec — [1m] GA language; anything live testing corrects.
- `.superpowers/sdd/<this plan>/live-report.md` — the per-row verification report (gitignored).

---

### Task 1: The live-run harness recipe and credential inventory

**Files:**
- Create: `scripts/live/with-live-env` (small sourcing wrapper, documented)
- Test: a dry run of the wrapper proving isolation

**Interfaces:**
- Produces: `scripts/live/with-live-env [--config <providers.toml>] -- <command…>` — builds a fake `HOME` (mktemp), exports `EVENER_STATE_DIR=$HOME/state`, `EVENER_PROVIDERS_CONFIG` (default: an absent path inside the fake home — the registry treats it as "user layer: none" and implicit instances come from the sourced env; `--config` copies a given providers.toml into the fake home instead), `EVENER_CREDENTIALS_CONFIG` pointing at the DEVELOPER's real store only when `--store` is passed, sources `~/git/prime-radiant/serf/.env` into the process env when present (never echoing values), then execs the command.

- [ ] **Step 1: Write the wrapper** with `set -euo pipefail`, a `--help`, and a comment block naming every variable it exports. It must `chmod 700` the fake home and delete it on exit unless `--keep`.
- [ ] **Step 2: Prove isolation**: `scripts/live/with-live-env -- sh -c 'echo $HOME'` prints a temp path; run it again with `EVENER_LIVE_TESTS=1 go test ./cmd/evener/ -run TestLiveSmoke -count=1` WITHOUT `-args -live-config` and confirm the suite skips cleanly (no config → skip), touching nothing under the real `~/.config/evener` (assert by mtime).
- [ ] **Step 3: Commit** (`git add scripts/live/with-live-env && git commit -m "test(live): scoped fake-home wrapper for live verification runs"`).

### Task 2: `[1m]` is GA — rows, docs, and the live pin

**Files:**
- Modify: `llm/registry/data/providers_overlay.toml` (the two `[1m]` rows at ~:107-117), `docs/llm-providers.md`, the spec's §13 live row (one clause)
- Test: `cmd/evener/models_live_test.go` (a `[1m]` property test), `llm/registry` goldens

**Interfaces:**
- Consumes: `ANTHROPIC_API_KEY` from the .env; the anthropic protocol.

- [ ] **Step 1 (investigate before editing):** with the Task-1 wrapper, send ONE live `/messages` request on `anthropic/claude-sonnet-4-5[1m]` as the rows stand (beta header on) and ONE with the header stripped (a local overlay override in the fake home's config), each asking for the single word pong with a `context_window`-sized prompt NOT sent (small prompt; the property under test is acceptance, not the window). Record both statuses.
- [ ] **Step 2:** if the headerless request is accepted (GA confirmed): update the overlay comment ("[1m] rows exist only where the 1M window is a beta" → GA wording), drop the `anthropic-beta` headers, keep the rows (the id spelling and 1M window facts remain useful), regenerate any goldens (`make fuzz-goldens` if a corpus seed covers the rows), and update `docs/llm-providers.md` + the spec §13 clause. If the header is still REQUIRED, keep it, correct nothing, and record the finding — the human partner's belief is then out of date and the report says so with the status line.
- [ ] **Step 3:** add `TestLiveOneMegaContextRowAccepted` beside `TestLiveSmoke` (gated the same way): resolve `anthropic/claude-sonnet-4-5[1m]`, assert the resolved window is 1_000_000+, send the one pong request, assert 200 and non-empty text.
- [ ] **Step 4:** offline gate for the touched packages + commit.

### Task 3: The direct-provider live matrix

**Files:**
- Modify: `cmd/evener/models_live_test.go` (`liveSmokeModels` additions + two property tests)

**Interfaces:**
- Consumes: ANTHROPIC/OPENAI/GEMINI/OPENROUTER/MOONSHOT keys from the .env; the store's kimi/lunaroute keys via `--store`.

- [ ] **Step 1:** extend `liveSmokeModels` so the smoke covers, one model each: `anthropic` (claude-haiku-4-5), `openai` (gpt-4.1-nano over Responses — assert the request went to `/responses`), `google` (gemini-2.5-flash-lite), `moonshotai` (kimi-k2.5), plus the already-proven openrouter/kimi/lunaroute rows.
- [ ] **Step 2:** add `TestLiveKimiK3ThinkingShape` (spec §13): one k3 request with a reasoning effort set; assert the response carries thinking content and the request body used the anthropic thinking shape the registry resolved (log the shape fields, not the payload).
- [ ] **Step 3:** run the matrix once under the wrapper with the .env + `--store`; every available row green; capture the per-row log into the live report.
- [ ] **Step 4:** commit.

### Task 4: Bedrock — token acquisition and `global.` routing

**Files:**
- Create: nothing new unless the investigation demands a row fix
- Test: `cmd/evener/models_live_test.go` (`TestLiveBedrockGlobalRouting`)

**Interfaces:**
- Consumes: the local AWS CLI (account verified reachable); `amazon-bedrock` rows (bearer via `AWS_BEARER_TOKEN_BEDROCK`, spec §9.3/§15 — bearer only, no SigV4).

- [ ] **Step 1 (investigate):** determine how to obtain a Bedrock bearer token from the local CLI credentials: check `aws bedrock` / `aws bedrock-runtime` CLI surface and AWS's documented bearer-token path for `bedrock-mantle` (the `aws-bedrock-token-generator` mechanism or a console-issued API key). Document the working command in the report. If NO bearer token is obtainable without console action, stop this task with a one-line ask for the human partner (a console-issued key pasted into the .env) and mark the row BLOCKED-ON-CREDENTIAL in the report — do not implement SigV4 (spec §15 forbids it).
- [ ] **Step 2 (with a token):** `TestLiveBedrockGlobalRouting`: resolve `bedrock/global.anthropic.claude-haiku-4-5` (or the cheapest global profile the catalog lists), assert the resolved base URL is the regional mantle host and the wire id keeps the `global.` prefix verbatim, send one pong request, assert 200. One more request on the in-region id (`anthropic.…`) for the non-global leg.
- [ ] **Step 3:** offline gate + commit.

### Task 5: Vertex, and the unavailable rows

**Files:**
- Test: `cmd/evener/models_live_test.go` (`TestLiveVertexOneRequest`, skip-gated on ADC)

**Interfaces:**
- Consumes: `gcp-adc` (gcloud present; ADC currently unconfigured — the test SKIPS with the exact remedy `gcloud auth application-default login`).

- [ ] **Step 1:** `TestLiveVertexOneRequest`: skip unless ADC resolves (probe via the tokenauth gcp-adc source, not gcloud exec); with ADC: resolve a `google-vertex-anthropic` claude row for the project/location taken from `GOOGLE_VERTEX_PROJECT`/`GOOGLE_VERTEX_LOCATION` env (skip naming them when unset), send one pong via `:rawPredict`, assert 200 and that `model` was omitted from the body (path-addressed).
- [ ] **Step 2:** the report's skip rows: `azure` (no `AZURE_API_KEY` anywhere on this machine), `groq` (no `GROQ_API_KEY` — spec §13's Groq-Responses row stays open), `minimax` direct (no `MINIMAX_API_KEY`; M3's budget object gets a partial check via `openrouter/minimax/minimax-m3` IF openrouter lists it — one attempted request, recorded either way), `vertex` when ADC is unconfigured. Each row names the exact missing credential.
- [ ] **Step 3:** commit.

### Task 6: #714 — the minimal-effort thinking budget floor

**Files:**
- Modify: the `ReasoningBudget` mapping in `llm` (locate via `grep -rn 'ReasoningBudget' llm/`), its unit tests
- Test: one live pin on `anthropic` (budget-shaped row)

- [ ] **Step 1 (RED):** unit test: `minimal` on a budget-shaped row yields a budget ≥ 1024 (Anthropic's floor); today it yields 512 — watch it fail.
- [ ] **Step 2:** clamp budget-shaped `minimal` to 1024 (the plainest fix; keep effort-shaped `minimal` untouched); doc comment cites Anthropic's minimum and #714.
- [ ] **Step 3 (live):** one request on `anthropic/claude-haiku-4-5` with `--reasoning-effort minimal` equivalents through the session path (`evener run` under the wrapper) — assert 200, no wire rejection.
- [ ] **Step 4:** offline gate + commit; note the fix on #714.

### Task 7: #715 — the empty-text 200s

**Files:**
- Diagnosis decides; likely `llm/providers/chatcompletions` response folding + a fixture from the captured live body

- [ ] **Step 1 (capture):** re-run the smoke rows that returned empty (`openrouter/minimax/minimax-m2.7`, `lunaroute/glm-5.2-vision`) once each with the raw body logged to the live report (bodies are provider OUTPUT — safe to capture; still grep them for header echoes before committing any excerpt).
- [ ] **Step 2 (classify):** if the text lives in a reasoning-only field the production `Response.Text()` also misses, that is a real folding gap: write the failing fixture test from the captured body (offline), fix the folding, re-run the live row (text non-empty). If production folds it fine and only the smoke's extractor was thin, fix the smoke's extractor and say so on #715.
- [ ] **Step 3:** commit; note findings on #715.

### Task 8: Report, docs sync, and issue closures

**Files:**
- Create: `.superpowers/sdd/2026-08-31-provider-registry-step4/live-report.md` (gitignored path)
- Modify: `docs/llm-providers.md` / the spec only where live results contradicted them

- [ ] **Step 1:** assemble the per-row report: every §13 live row → VERIFIED (with the one-line evidence) | SKIPPED (missing credential, named) | BLOCKED (Task 4's ask) | CORRECTED (what changed).
- [ ] **Step 2:** update #716 with the report summary; close/comment #714 and #715 per their outcomes.
- [ ] **Step 3:** full offline gate; commit anything outstanding.

## Spec coverage

| Spec §13 live row | Task |
|---|---|
| `[1m]` on Sonnet 4.5 / Opus 4.5 | 2 |
| Kimi K3 thinking shape | 3 |
| Bedrock `global.` routing | 4 |
| One request per cloud transport | 4 (bedrock), 5 (vertex; azure skipped-with-note) |
| Groq Responses | 5 (skip row — no credential) |
| MiniMax M3 budget object | 5 (partial via openrouter; direct key absent) |

## Rulings

- `[1m]` GA is the human partner's statement (2026-08-31); Task 2 verifies live before editing rows — evidence beats belief in both directions.
- Bedrock stays bearer-only (spec §15); a missing bearer is a credential ask, never a SigV4 implementation.
- Light use is a hard rule: a task that wants a second request per property must justify it in its report.
