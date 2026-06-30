# Plan 12 — Dramatically increasing fuzzing coverage of the whole codebase

**Status: PROPOSED (2026-06-30).** Author: Bot, with Jesse. Builds on Phases 0–11
(`docs/design/plans/01..11`, `docs/fuzzing.md`). Point-in-time design doc — describes
intent as of its date, not current code.

## Goal

Dramatically increase fuzzing coverage of the *entire* codebase. "Coverage" is three
different things; we are in three different places on each, and this plan attacks the two
that actually move the needle.

| Meaning | Where we are | This plan |
| --- | --- | --- |
| **Breadth** — every package has a target | Done (gap gate enforces it; 91 targets) | maintain, don't re-litigate |
| **Depth** — the fuzzer drives real *behavior*, not just a parser | Partial — only ~24/91 are behavioral; a long tail sits at 1–48% focus coverage | **Phase B** |
| **Detection** — reached code actually *catches* bugs | The real ceiling (Phase 8: 10× fuzz-time found nothing; both real bugs came from differential oracles) | **Phase A corpus + Phase C oracles** |

The diagnosis from Phases 8–9 stands: the limiter is **harnessability and oracle
strength**, not search time. So we do **not** buy more fuzz-hours or add more shallow
decode targets. We (A) feed real traffic into the corpus, (B) promote decode-only targets
to behavioral ones, (C) give every reached path a real oracle, (D) make whole-codebase
coverage *measurable*, and (E) keep the corpus fresh.

Jesse's ordering: **Phase A first** (record locally, drive kimi + gpt-5.4-mini, build a
real corpus), then **B/C/D in parallel** via fanned-out subagents.

## What already exists (verified, do not rebuild)

- **Three recorders, all default-off, all reading `os.Getenv` directly** (no central
  default): `SERF_RECORD_APPWIRE` → `<state>/appwire-frames.jsonl`
  (`appwire/frame_recorder.go:newEnvFrameRecorder`, attached in `appwire/ws_transport.go`);
  `SERF_RECORD_HTTP` → `<state>/hub-http.jsonl` (`cmd/serf-hub/http_recorder.go`, middleware
  in `cmd/serf-hub/web.go`); **`SERF_LOG_RAW_HTTP` → `<state>/api-raw.jsonl`** capturing
  per-call provider `request_body` + streaming `response_body`
  (`llm/apilog.go:EnableRawLogging`, attached via `cmdutil.AttachAPILogger`). All registered
  in `envvars/envvars.go` with `Visibility: Tooling`.
- **The provider-traffic harvest path is already complete end-to-end.**
  `serf-fuzz-harvest --surface sse` reads `api-raw.jsonl`, takes streaming `ResponseBody`,
  and routes by provider into the per-provider metamorphic seed dirs
  (`cmd/serf-fuzz-harvest/raw.go:harvestSSE`, `main.go:112`). Surfaces: `sse, toolargs,
  appwire, http, jobs`.
- **A robust sanitization chokepoint** (`cmd/serf-fuzz-harvest/sanitize.go`): shape-scrub by
  default (structure/enums kept, free-text length-bucketed, numbers zeroed, timestamps
  fixed), nine high-confidence secret regexes + entropy gate, `--keep-values` gated behind
  `SERF_FUZZ_CAPTURE_ENV`, personal `~/.serf` forced to shape-scrub, plus a write-time
  `gitleaks` gate. Corpora are **not** path-allowlisted in `.gitleaks.toml` — they must be
  genuinely scrubbed to commit.
- **The headless driving surface:** `serf --model <provider/model> [--verbose]
  [--max-rounds N] [--reasoning-effort L] [--fast-cheap-model <m>] "<prompt>"` (prompt
  positional or stdin via `cmd/serf/main.go:111 cliprompt.Read`; `--verbose` emits NDJSON
  events to stderr; flags at `cmd/serf/main.go:166-195`). Kimi: `type = "kimi-anthropic"`,
  keys `KIMI_CODING_API_KEY` / `KIMI_CODING_BASE_URL`. OpenAI: `OPENAI_API_KEY`,
  model `gpt-5.4-mini`. Configurable via `~/.serf/providers.toml` `[instances.*]`.

**The one missing piece for Phase A is a *driver*** — there is no bulk prompt runner; the
harvester only consumes *recorded* traffic. So Phase A = make recording the local default +
build the driver + run it.

---

## Decisions (resolve before/while building)

**D1 — RESOLVED: master env var.** Recording captures real prompts, code, and provider
responses to disk, so it must be **off by default in the shipped binary, in CI, and in any
non-dev environment**, and **on by default for local dev**. Mechanism: a single master env
var **`SERF_FUZZ_RECORD`** that flips all three recorders on unless a per-recorder var
(`SERF_RECORD_APPWIRE`, `SERF_RECORD_HTTP`, `SERF_LOG_RAW_HTTP`) explicitly disables it; set
it once in the dev shell profile / dev bootstrap. Absent → all off (safe everywhere it isn't
set). Ensure `./.serf/` is gitignored (the repo-local state fallback) so raw recordings never
land in a commit; raw `*.jsonl` stays on the dev box, only *scrubbed seeds* are committed.

**D2 — Driver drives the real `serf` CLI as a subprocess (recommended), not the Go Session
API.** Subprocessing `serf --model ... "task"` exercises the real one-shot path (itself a
surface), needs no test seams, and naturally produces `api-raw.jsonl` (provider SSE),
transcripts (toolargs), and `jobs.jsonl`. The Go API (`agent.NewSession` →
`sess.ProcessInput`) is tighter but bypasses the CLI wiring and the recorders' attach points.
Subprocess wins. (Appwire/HTTP frames flow only over the hub WS path, not the one-shot CLI —
so a *hub-mode* driver is a Phase A follow-on, not the first cut.)

**D3 — RESOLVED: auto-PR via fuzz-triage.** The driver runs **locally, on-demand**
(consistent with the standing "no scheduled CI" constraint). It harvests → scrubs →
gitleaks-gates → **opens a PR automatically via the existing `fuzz-triage.sh` machinery**
(flake-guard + dedup + PR + ledger) with the new seeds. The gitleaks gate is the hard
safety boundary before any PR; a leak aborts with non-zero exit and no PR.

---

## Phase A — Real-traffic corpus bootstrap (FIRST)

Highest cheap leverage: real inputs reach states random generation never will. The harvest
path exists; we need the local-record default and a driver.

**A1 — Local-record master switch (D1).** Add `envvars.SERFFuzzRecord`; thread it into the
three recorders' enable checks (`appwire/frame_recorder.go`, `cmd/serf-hub/http_recorder.go`,
`llm/apilog.go:rawHTTPLogEnabled`) as "per-recorder var OR (master ∧ not explicitly off)".
Document in dev onboarding. Gitignore `./.serf/`. *~40 LoC + tests asserting off-by-default
when master unset.*

**A2 — The driver (`cmd/serf-fuzz-drive`, or `scripts/fuzz-drive.sh`).** Runs a corpus of
varied coding tasks headless against each configured provider, recorders on, in throwaway
sandboxed workdirs, then invokes the harvester. Requirements:
- **Task corpus**: a directory of prompt files spanning the tool/behavior surface — file
  create/edit/multi-edit, search/grep, shell, read large files, multi-turn refactor,
  intentional-error-then-fix, web fetch, MCP use, delegation/subagents, long outputs. Variety
  is the point (drives diverse provider SSE shapes + tool-arg shapes). Seed with ~20–30 tasks;
  extensible.
- **Providers**: `kimi` (`kimi-anthropic`/`kimi-for-coding`) and `gpt-5.4-mini` first;
  table-driven so more providers/models drop in.
- **Isolation + safety**: each run in its own `t.TempDir`-style scratch repo; `--max-rounds`
  capped; per-provider rate-limit backoff; a cost ceiling (mini is cheap — surface est. token
  spend from `--verbose` events).
- **Pipeline**: after the batch, run `serf-fuzz-harvest` (shape-scrub default) → gitleaks gate
  → stage seeds (D3). Wrap under `scripts/run-capped.sh`.
- *~250–400 LoC. This is the one genuinely new build in Phase A; do it serial/first.*

**A3 — First harvest + measure.** Run A2 against kimi + gpt-5.4-mini; harvest; commit the
first real-traffic corpus. Measure coverage delta on the provider decoders (`FuzzParseSSE`,
the five `*Metamorphic` targets) and `FuzzToolArgsValidate` via `make fuzz-coverage`; record
the before/after in `fuzz/README.md`. Any oracle trip during harvest-replay = a real bug →
TDD fix.

**Exit:** recorders default-on locally; a repeatable driver; a committed real-traffic corpus;
measured coverage lift on provider + tool-arg targets.

---

## Phase B — Promote decode-only targets to behavioral (parallel fan-out)

Lever #2. The shallow tail (`scripts/fuzzcov-floors.txt`, all <50%). For each: feed the
decoded value into the production logic that consumes it. Worklist (verified file:symbol):

| Target (floor) | Decoded type | Drive into | Seam |
| --- | --- | --- | --- |
| `FuzzWireTypes` (1.0%) | all 99 wire types | RPC dispatch → typed handlers (`cmd/serf-hub/app_rpc.go`, `internal/appserver/router.go:Dispatch`) | sandbox sources/registry — **reuse the existing `FuzzAppWireDispatch` sandbox**; first coverage-diff against it to avoid duplication |
| `FuzzMethodParams` (1.0%) | 46 method Params | same dispatch path, per-method | same sandbox |
| `FuzzLaunchConfigDecode` (1.5%) | `LaunchConfigLayer` | `launchconfig.Resolve` + `validateAndExpandRepoLayer` (`cmd/serf-hub/internal/launchconfig/resolver.go`) | fs/trust-store seam (temp dir + fake trust) |
| `FuzzApplyEdit` (24%) | `LaunchConfigLayer` edits | `launch_settings_panel.go:applyEdit` path → `SetLayer` (`cmd/serf-tui/internal/launchconfig`) | path-validation seam |
| `FuzzMCPConfigLoad` (48%) | MCP server map | already calls `LoadFile`; extend to `mcpstatus.ProbeMCPStatus` (`cmd/serf-hub/web_settings.go`) | probe stub (no real subprocess) |
| `FuzzCodexItemDecode` | `ThreadItem`/`Turn` | `server/appwire_turns.go:appTurnsFromNotifications` (pure) | none — just route the decoded value in |

**Already behavioral — enrich seeds only, don't rebuild:** `FuzzProject`
(`internal/appprojector` — pure state machine), `FuzzTranscriptWriterRoundTrip`
(`agent/transcript` — full write/reopen). Note their floors are low because the *focus file*
has little executable code, not because they're shallow (same Go-coverage caveat documented
in `fuzz-mutation-score.sh`).

Each promotion **ships a real oracle** (Phase C), not a bare never-panic.

---

## Phase C — Oracle strengthening (parallel, rides on B + A)

Lever #5 — the actual bug-finding ceiling. Every behavioral target from B and every
provider target re-seeded in A gets a differential or internal invariant:
- **Provider decoders** (A): the real-traffic corpus flows through the existing
  cross-provider + stream-vs-non-stream differentials (`llm/providers/difftest/`); confirm the
  new shapes don't diverge, and add a golden-conformance fixture (`make fuzz-goldens`) for any
  new SSE shape kimi/gpt-5.4-mini produce that we don't already pin.
- **Dispatch promotions** (B `FuzzWireTypes`/`FuzzMethodParams`): internal invariants under
  `serffuzz` on handler post-conditions (no partial mutation on error, response frame shape).
- **Config promotions** (B): round-trip + resolve-is-deterministic + no-path-escape invariants.
- Audit each new oracle with `scripts/fuzz-oracle-audit.sh` (prove it reddens on its bug
  class) before declaring it done.

---

## Phase D — Whole-codebase coverage measurement + global ratchet (parallel, infra)

Lever #4 — makes "the entire codebase" measurable instead of a hand-picked focus set. Today
`cmd/serf-fuzzcov` + `scripts/fuzzcov-floors.txt` track a focus set per target. Add:
- A **global fuzz-reachable coverage number**: run the full seed corpus (`go test -run '^Fuzz'`)
  with `-coverpkg=./...` per module, merge profiles, report total % per module + repo.
- A **global ratchet** (`scripts/fuzzcov-floors.txt` sibling) that fails CI if the global
  number drops — so new code without a target visibly lowers it.
- A **gap report by reachable-but-uncovered package**, so the next promotion worklist
  generates itself. Honest accounting: log what's excluded (TUI render, CLI glue) and why.

---

## Phase E — Keep the corpus fresh (continuous, local)

Fold the A2 driver into the existing local loop: `scripts/fuzz-continuous.sh` gains a
periodic "drive providers → harvest → dedup-merge new seeds" turn (on-demand, not scheduled
CI). Crashers route through `fuzz-triage.sh` (flake-guard + dedup + PR) as today. This is
what turns Phase A from a one-shot into a standing corpus refresh.

---

## Execution: subagent fan-out

Per the hard-won fan-out rules (memory + plan 10 §): **non-isolated agents on disjoint
packages only; one lane per package per batch; lanes edit only their own `_test.go` + report
the `run-fuzz.sh` registry line for the parent to add (lanes never touch `scripts/` or
`go.mod` concurrently); parent runs full `make lint` + gate + commits; never `git checkout`
an uncommitted file; verify each lane scoped while siblings still edit.**

- **Batch 0 (serial, foundational):** A1 (record switch) then A2 (driver). A2 spans
  multiple modules' enable checks + a new `cmd/`; do it as one focused lane to avoid churn,
  then A3 (run + harvest + measure) by the parent.
- **Batch 1 (parallel, after A lands):** fan out B + C + D. Lane assignment by disjoint
  package:
  - Lane 1 (root / appwire+dispatch): `FuzzWireTypes` + `FuzzMethodParams` promotion + their
    invariants (coverage-diff vs `FuzzAppWireDispatch` *first*; may collapse to enrichment).
  - Lane 2 (root / launchconfig×2): `FuzzLaunchConfigDecode` + `FuzzApplyEdit`.
  - Lane 3 (agent / mcpconfig): `FuzzMCPConfigLoad` → probe.
  - Lane 4 (llm / difftest): Phase C provider oracles + goldens from the A corpus.
  - Lane 5 (infra): Phase D global coverage + ratchet (touches `cmd/serf-fuzzcov` + scripts —
    parent-adjacent; run it as a parent-supervised lane so it doesn't race scripts edits).

  Note most B targets live in the **root module** — parallelism there is by disjoint
  *package* (different dirs, no shared file), with the parent serializing every
  `scripts/run-fuzz.sh` and `go.mod` edit.
- **Batch 2 (serial):** E (continuous refresh wiring) once the corpus + targets are in.

Per-batch gate: `make fuzz` / `test` / `lint` / `fuzz-gap-check` (capped, `-tags serffuzz`),
then `--no-ff` merge → push.

## Risks / honest caveats

- **Provider cost + flakiness** (A): live calls cost money and rate-limit. Mitigate with the
  cheap models Jesse named, backoff, a cost ceiling, and on-demand (not continuous-by-default)
  runs.
- **Secret leakage** (A): the scrub + gitleaks chokepoint is strong, but raw `api-raw.jsonl`
  on disk contains real content — keep it local + gitignored; only scrubbed seeds commit.
- **`FuzzWireTypes`/`FuzzMethodParams` may overlap `FuzzAppWireDispatch`** — coverage-diff
  first; if redundant, downgrade to seed enrichment rather than a duplicate target.
- **Diminishing returns past depth**: don't gold-plate TUI/CLI glue; Phase D's gap report
  should say plainly what we deliberately don't fuzz and why.

## Anchors (verified)

- Recorders: `appwire/frame_recorder.go`, `cmd/serf-hub/http_recorder.go`,
  `llm/apilog.go` (`EnableRawLogging`, `rawHTTPLogEnabled`), `envvars/envvars.go`.
- Harvest: `cmd/serf-fuzz-harvest/{main.go,raw.go,sanitize.go,discover.go,emit.go}`,
  `scripts/gitleaks-scan.sh`, `.gitleaks.toml`.
- Driving: `cmd/serf/main.go:166-195` (flags), `:111` (prompt), `cmd/serf/run.go`.
- Targets/coverage: `scripts/run-fuzz.sh` (registry), `scripts/fuzzcov-floors.txt`,
  `cmd/serf-fuzzcov`, `scripts/fuzz-mutation-score.sh` (coverage-artifact caveat).
- Promotion consumers: `internal/appserver/router.go:Dispatch`, `cmd/serf-hub/app_rpc.go`,
  `cmd/serf-hub/internal/launchconfig/resolver.go:Resolve`, `agent/mcpconfig/config.go:LoadFile`,
  `server/appwire_turns.go:appTurnsFromNotifications`.
