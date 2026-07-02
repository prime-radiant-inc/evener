# Doctoring-Serf Skill — Doc Adoption Audit

Date: 2026-06-19
Status: audit + adoption catalog for Jesse review (UNCOMMITTED)
Siblings:
- `docs/superpowers/specs/2026-06-19-serf-doctoring-tools.md` — the `serf doctor` CLI data-plane (the tools the skill references).
- `docs/superpowers/specs/2026-06-19-serf-debugging-docs.md` — the earlier DEBUGGING-doc audit (staleness/gaps/principles). This audit is BROADER (all of `docs/`) and ADOPTION-focused. It **extends** that one; it does not repeat its scenario-recipe drift findings.

## What this is

We are building a **`doctoring-serf` skill** under `docs/skills/doctoring-serf/`: a small `SKILL.md` + on-demand `references/`, loaded into serf at runtime, used by an on-demand **`doctor`** agent type to diagnose sessions/health, emit structured Findings, and maintain runbooks. Planned references: **data-model.md**, **failure-modes.md**, **finding-contract.md**, **writing-runbooks.md**, **repair-guardrails.md** (plus runbooks/ and the agent-type system prompt).

This catalog says, for each existing doc: evergreen vs transient, doctoring-relevant?, and — for the relevant evergreen ones — an **adoption mode** and **which skill reference it feeds**. It does NOT rewrite or edit any docs.

Adoption modes:
- **adopt-as-reference** — durable + clean; link/include mostly wholesale.
- **extract-the-evergreen-core** — transient/mixed doc with a durable nugget worth pulling out (section cited); leave the rest.
- **cite-as-source-of-truth** — point the skill at the doc (or the Go types) rather than copy.

The model skill already in-repo is `docs/skills/benchmark-driven-improvement/` (`SKILL.md` + `references`-style siblings) — same shape we are cloning. `docs/skills/doctoring-serf/` does not exist yet; this is greenfield.

---

## 1. Doc inventory with per-doc verdict

`docs/` splits cleanly: **top-level `docs/*.md`** are durable architecture/contracts/runbooks; **`docs/superpowers/{specs,plans,research}/`** (~190 files) are dated single-change artifacts (transient by construction); `docs/web-ui/**`, `docs/design/**` are UI/eval result history (transient). Only the doctoring-relevant rows are enumerated; the dated corpus is summarized in bulk at the end of the table.

| Doc | Evergreen? | Doctoring-relevant? | Adoption mode | Feeds reference |
| --- | --- | --- | --- | --- |
| `docs/architecture.md` | **Evergreen** (self-declares "canonical, living") | **High** — mailbox invariant, three-queues, drive-down, watch-deadlock mechanics, module layout | **cite-as-source-of-truth** (link §"Ownership and mailboxes", §"Drive-down"; do not restate) | **failure-modes** (watch loops, stuck turns, deaf coordinator, single-hop forwarding); **data-model** (state-dir/module shape) |
| `docs/job-control.md` | **Evergreen** (self-declares "Evergreen contract") | **High** — statuses/reasons, coalescing semantics, self-loop boundary, watch/delegate/grant vocabulary | **cite-as-source-of-truth** (the status/reason/coalescing vocabulary the taxonomy links to) | **failure-modes** (status×reason table, dropped-vs-coalesced, `runtime_lost`/`supervision_lost`/`stop_pending`); **data-model** (job record fields) |
| `docs/tools/transcripts.md` | **Evergreen** (concepts + tool contracts, code-verified) | **High** — THE transcript-reading onramp: turn numbering, refs, buckets, scope, result-tool | **adopt-as-reference** (link wholesale; it is the cleanest evergreen doc in the repo) | **data-model** (transcript grammar, refs, buckets, kind=root/subagent/fork); the agent-type prompt |
| `agent/prompts/sections/transcripts.md` | **Evergreen** (agent-facing steer, param vocab only) | **Medium** — the in-prompt vocabulary for the same two tools | **cite-as-source-of-truth** | the agent-type system prompt |
| `docs/subagent-management/10-runtime-contracts.md` | **Evergreen** ("Proposed evergreen spec"; contracts not recipes) | **High** — diagnostic field/severity/category contract, event-ordering, lineage invariants, helper isolation | **extract-the-evergreen-core** (Contract 3 diagnostics → finding-contract; Contract 2 ordering + Contract 6 lineage → failure-modes; it is a "proposed/target" doc so cite as target-contract, verify before quoting as shipped) | **finding-contract** (Diagnostic DTO, severity, `kind` categories, redaction); **failure-modes** (event-order, lineage/provenance gaps) |
| `docs/subagent-management/09-session-tree-history-assessment.md` | **Evergreen-ish** (explicitly "not a committed feature spec"; an assessment) | **Medium** — fork/subagent lineage fields + the "derive tree from metadata, don't index" stance | **extract-the-evergreen-core** (the lineage-field inventory + `ListSessionMetas`/`children_of` tree-derivation §§; ignore the API-decision body) | **data-model** (lineage fields); **failure-modes** (orphaned-parent diagnosis) |
| `docs/hooks.md` | **Evergreen** ("how serf's hooks work today"; contract-level) | **Medium** — hook matcher/tool-name/exit-code/IO contract; `hook_blocked`/`hook_failed` failure surface | **cite-as-source-of-truth** | **failure-modes** (hook-blocked/failed entry); the agent-type prompt (hook authoring) |
| `docs/llm-provider-config-and-launch.md` | **Evergreen** (credential-resolution precedence, mostly tabular) | **Medium** — "why did my provider/model/creds fail to launch"; credential store locations | **cite-as-source-of-truth** | **failure-modes** (launch/credential failures); **data-model** (where credentials live, hub vs daemon) |
| `docs/llm-providers.md` | **Evergreen** (provider routing architecture) | **Low–Medium** — provider/model routing, two-identity (name vs behavior-tag) model | **cite-as-source-of-truth** | **failure-modes** (provider-error class), peripheral |
| `docs/ollama.md` | **Evergreen** (procedural, no line numbers) | **Medium** — the closest thing in-repo to a symptom→cause list (provider-scoped) | **adopt-as-reference** (model the failure-modes ENTRY SHAPE on its symptom→cause section) | **failure-modes** (provider-scoped template); peripheral as content |
| `docs/conventions/naming.md` | **Evergreen** (canonical, linter-enforced) | **Medium** — JSON=snake_case / TOML=snake_case / CLI=kebab; the on-disk-grammar casing a parser needs | **cite-as-source-of-truth** (anchor the "why a field is snake_case on disk" claim) | **data-model** (on-disk JSON casing); has its own drift — see §3 |
| `docs/performance-profiling.md` | **Mixed** — durable interpretation, drift-prone snippets | **Low** — `api.jsonl` + `ROUND_TIMINGS`/`duration_ms` model is durable; the Python parser is stale | **extract-the-evergreen-core** (the `api.jsonl`/`ROUND_TIMINGS` field model only; NOT the parser) | **data-model** (api.jsonl, ROUND_TIMINGS); has a confirmed STALE parser — see §3 |
| `docs/agentic-testing.md` | **Mixed** — durable method bones, drift-prone recipes | **Medium** — falsification loop, rebuild-point map, over-spec trap, on-disk inspection | **extract-the-evergreen-core** (method bones only; shed the `grep '"kind"'`/`find` recipes → `serf doctor`; two confirmed stale paths in §3) | **writing-runbooks** + **repair-guardrails** (escalation ladder); methodology for the agent-type prompt |
| `docs/skills/benchmark-driven-improvement/root-cause-task-failure.md` | **Evergreen as METHOD** (larded with `## Session NN` accretions + `file:line`) | **High (different axis)** — eval-task-failure diagnosis: decision-point reconstruction, blameless interrogation, 6-category missing-support taxonomy | **extract-the-evergreen-core** (the method + the 6-category taxonomy; strip `## Session 21/22 Additions` + `subagents.go:237` cites) | **failure-modes** (the eval-failure half of the taxonomy); **finding-contract** (the report schema in Step 5) |
| `docs/skills/benchmark-driven-improvement/SKILL.md` | Evergreen-as-method | **Medium** — the SKILL.md SHAPE precedent (HARD GATE framing, tool reference, anti-pattern table) | **adopt-as-reference** (structural model for our `SKILL.md`, not content) | the skill's own `SKILL.md` authoring |
| `docs/skills/benchmark-driven-improvement/tools-reference.md` | Mixed | Low — eval-harness tool reference; re-declares transcript field paths | **cite-as-source-of-truth** (eval-scoped; not the doctoring data plane) | — |
| `docs/specs/2026-06-12-job-control-watch-deadlock-design.md` | **Transient** (dated note) **with a durable "Resolution" tail** | **High (as narrative)** — the canonical motivation for the mailbox invariant; the deadlock reproduction | **extract-the-evergreen-core** (the §"Root Cause" cycle + §"Resolution (implemented)" — but note `architecture.md` already absorbed the rule; cite that, use this only for the worked example) | **failure-modes** (watch-self-loop worked example, "why the boundary exists") |
| `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md` | **Transient** (dated design) **with a durable data-model nugget** | **High (the provenance model)** — `CausalProvenance`/`WatchKey` set, same-watch suppression invariant, propagation rules | **extract-the-evergreen-core** (the causal-provenance model + suppression rule) **but the type names DRIFTED on impl** — see §3; cite `agent/provenance` types, not this doc's names | **failure-modes** (provenance gaps, self-loop detection); **data-model** (provenance chain) |
| `docs/superpowers/research/2026-06-18-observer-sidecar-use-cases.md` | **Transient** (research inventory) | **Low** — 49 use-case cards + a frame/permission synthesis; aspirational, no shipped contract | **cite-as-source-of-truth** only if writing-runbooks wants observer-runbook seeds; otherwise leave | **writing-runbooks** (optional seed for observer audit definitions) |
| `docs/superpowers/specs/2026-06-19-observer-provenance-remediation-spec.md` | **Transient** (remediation spec for THIS branch) | **Low** — useful "Reviewer Checklist" of provenance-gap questions | **extract-the-evergreen-core** (only the Reviewer Checklist as failure-mode probes) | **failure-modes** (provenance-rail probe checklist) |
| `docs/superpowers/specs/2026-05-12-transcript-tool-grouping-contract.md` | **Evergreen contract** (small) | **Low** — appwire-projection rendering; the LIVE stream, an explicit doctoring non-goal | **cite-as-source-of-truth** (only if the skill ever touches live appwire) | peripheral (rendering, not forensics) |
| `docs/testing.md` | **Evergreen** (provider-E2E setup) | **None** — no inspection/diagnosis content (confirmed) | — | — |
| `docs/original-attractor-specs/{attractor,coding-agent-loop,unified-llm}-spec.md` | **Evergreen-as-idealized-spec** (from-scratch, language-agnostic) | **Low** — describes an idealized build, NOT serf's current on-disk reality; predates the actual schema | **cite-as-source-of-truth** only for "the intended loop", never for on-disk shape | peripheral |
| `docs/serf-hub-remote-operations.md`, `cmd/serf-hub/README.md` | Evergreen (ops runbooks) | Low — ops/health/restart/OAuth, not session/job forensics | cite-as-source-of-truth | **writing-runbooks**/**repair-guardrails** (ops-repair boundary) |
| `docs/terminal-bench.md` | Mixed | Low — hand-parses transcript over SSH (drift-prone), infra-host-specific | leave | — |
| `docs/superpowers/{plans,research,specs}/**` (the dated corpus, ~190 files), `docs/web-ui/**`, `docs/design/**`, `docs/resume-model-context-fix-plan.md`, `docs/serf-hub-web-routing.md`, `docs/openai-spend-diagnostics.md` | **Transient** (dated single-change artifacts; UI mockups; eval-result snapshots) | **None–Low** (history, not runbooks) | leave (history lives where it is) | — (exceptions individually rowed above where relevant) |

---

## 2. Canonical evergreen sources — what the skill anchors on

Five docs are the authoritative substrate. The skill should anchor on these and the Go types they point at, never re-derive:

1. **`docs/architecture.md`** — the runtime mechanics: the mailbox invariant ("Event observation records durable intent and wakes the owner. Only a session's own loop mutates that session."), the three queues (steering / job-notifications / watch-outbox), drive-down (parent drives children level-by-level), single-hop forwarding, the every-job-notifies-only-its-OWNER rule. **This is the failure-modes mechanics source.** (Memory hint CONFIRMED: §"Ownership and mailboxes" + §"Drive-down" carry exactly this. The watch-deadlock material is cross-linked, not inlined.)
2. **`docs/job-control.md`** — the status/reason/coalescing/self-loop **contract vocabulary**. The taxonomy's symptom rows must use these exact names (`running`/`completed`/`failed`/`cancelled`/`stopped` × `runtime_lost`/`supervision_lost`/`stop_pending`/`run_timeout`/`awaiting_permission`; latest-frame-wins coalescing; the at-most-one-watch-per-`(visible_session,target,send.to)` key).
3. **`docs/tools/transcripts.md`** — the transcript **concept onramp** (turn numbering is universal; refs `local:`/`proj:`; buckets keyed by git-origin hash; scope; result-tool = the communicate-named tool). Cleanest evergreen doc in the repo; code-verified by the prior audit and re-confirmed here.
4. **`docs/subagent-management/10-runtime-contracts.md`** — the **diagnostic/finding contract** (Diagnostic DTO with `source`/`kind`/`severity`/`field`/`message`; the `validation|policy_denied|unavailable|timeout|cancellation|provider_error|hook_blocked|hook_failed|transcript_unavailable` category set; "warnings only where displayed"; secret redaction) and the lineage invariants. This is the closest existing thing to **finding-contract.md**.
5. **The Go types as ultimate source of truth** (per the doctoring-tools spec's own rule — "read serf's OWN canonical types, never re-guess them"):
   - `agent/transcript/transcript.go` — header line (`Kind:"header"`), `Entry{Kind:"entry", Seq, Turn schema.Turn}`, interleaved `Kind:"api_call"` lines.
   - `agent/schema` — `schema.Turn` (the canonical turn; `Kind`, `Message`), `SessionMeta` (`ParentSessionID`/`DivergenceTurn`/`ForkLabel`/`IsSubagent`/`ObservedBy`), `ListSessionMetas`.
   - `agent/internal/jobstore` — `Fold`/`FoldOrdered`/`FoldWatches`/`FoldWatchSends`/`FoldDelegates`/`FoldGrants` and the matching `Store.Load`/`LoadOrdered`/`LoadWatches`/`LoadWatchSends`/`LoadDelegates`/`LoadGrants`; `WatchRecord`/`WatchSendState`/`WatchSendKey`/`DelegateRecord`; provenance rides `WatchSendState.Provenance`; event kinds `watch_send_pending|delivered|dropped|evicted` (`agent/internal/jobstore/event.go`). On-disk path: jobs live at `sessions/<SID>/jobs.jsonl` (a subdir), per §3.
   - `agent/provenance` — `Causal{WatchKeys []WatchKey, Chain []Entry, ChainTruncated}`, `WatchKey{WatchID, WatchGeneration}`, `Entry{Kind, WatchID, WatchGeneration, DeliveryID, SessionID, JobID}`, `ContainsWatch(p, watchID, watchGeneration)`.
   - `agent/runtime_dir.go` — `RuntimeDir`/`RuntimeDirWithStateHome`, `hexHash` (bucket = `sha256(originURL else workDir)[:8]` raw → 16 hex chars).

---

## 3. Staleness flags — what we'd inherit if we adopt naively

Cross-checked load-bearing claims against current code. These corrections must be applied **before** adoption, so the skill anchors on the corrected fact.

### WORST — the on-disk `jobs.jsonl` PATH in the doctoring-tools spec is wrong (verified against `agent/jobs.go`)
The sibling spec states jobs.jsonl "lives beside the transcript" and gives canonical layout `…/sessions/<SID>.{transcript.jsonl,meta.json,jobs.jsonl}`. **That is false in two ways, both verified in production code:**
- Transcript/meta ARE flat SID-prefixed files: `<bucket>/sessions/<SID>.transcript.jsonl` (`agent/transcript_lookup.go` `transcriptPath` → `sessionID+".transcript.jsonl"`).
- But jobs.jsonl is in a **per-session subdirectory**: `jobsDir(stateDir, sessionID)` = `<stateDir>/sessions/<id>/`, and the store opens `filepath.Join(dir, "jobs.jsonl")` → **`sessions/<SID>/jobs.jsonl`** (`agent/jobs.go` `jobsDir` + `jobstore.Open(filepath.Join(dir, "jobs.jsonl"))`).
- The scenario files that `find … -path "*sessions/$SID/jobs.jsonl"` (the subdir form) were **right all along** — the prior debugging-docs audit flagged their *delivery counting*, but their *path* is correct.
- **Adopt:** data-model.md states the split explicitly (transcript+meta flat, SID-prefixed; jobs in a `sessions/<SID>/` subdir). `serf doctor locate`'s `jobs_path` must resolve via `jobsDir`, NOT by appending `.jobs.jsonl` to the transcript path. If the doctor tool inherits the spec's "beside the transcript" claim it will emit a non-existent `jobs_path`.

### WORST-0 — the fold/load surface the spec cites is CORRECT (one earlier draft of this audit got this wrong; recorded so it isn't re-flagged)
For the record, having re-verified `agent/internal/jobstore/{fold.go,store.go}`: the spec's `Fold`/`FoldWatches`/`FoldWatchSends`/`FoldDelegates`/`FoldGrants` and `Store.Load*()` are **real and correct**. `Fold` returns `map[string]*JobRecord` (latest-wins job map) and `FoldOrdered` is its ordered-slice sibling; both exist, as do `Store.Load()`/`LoadOrdered()`/`LoadWatches()`/`LoadWatchSends()`/`LoadDelegates()`/`LoadGrants()`. **There is no missing/renamed fold function** — data-model.md may cite `Fold`/`FoldWatchSends`/etc. straight. (Use `FoldOrdered`/`LoadOrdered` only when you specifically need event/job order; the generic job-state read is `Fold`/`Load`.)

### WORST-2 — the provenance type names in the 06-18 observer design were RENAMED on implementation
The design doc (`2026-06-18-observer-watch-origin-loop-design.md`) defines `CausalProvenance`, `WatchProvenanceKey`, `ProvenanceEntry`, and puts `Provenance *CausalProvenance` on `events.SessionEvent`. The shipped code instead has a dedicated **`agent/provenance`** package with **`Causal`**, **`WatchKey`**, **`Entry`** (not `CausalProvenance`/`WatchProvenanceKey`/`ProvenanceEntry`). The doctoring-tools spec compounds this by citing `provenance.Causal.Chain` and `provenance.Chain[].SessionID` and framing the **diagnostic Chain** as the self-loop source. Two corrections for failure-modes.md:
- Cite `agent/provenance.Causal` / `provenance.WatchKey` / `provenance.Entry`, not the design doc's names.
- **The load-bearing structure is `WatchKeys` (the deduped `(watch_id, watch_generation)` set), and suppression is `provenance.ContainsWatch(p, watchID, watchGeneration)`.** `Chain` is explicitly diagnostic-and-truncatable (`maxDiagnosticChain = 16`; "truncation can never drop watch keys"). So "self-loop" detection keyed on `Chain[].SessionID == target` is the *diagnostic* read, not the canonical suppression path. The skill must not present the Chain as authoritative.

### CONFIRMED STALE — `docs/performance-profiling.md` hand-parses the transcript (and the prior audit mislabeled which kind is deprecated)
The tool-duration snippet keys on `turn.get('kind') != 'TOOL_RESULTS'` and reads `p.get('tool_result')` **positionally**. **Correction to the prior debugging-docs audit:** it called `TOOL_RESULTS` "the deprecated TOOL-turn shape" — but in `agent/schema/turn.go`, `TurnTool = "TOOL"` is the **Deprecated** one and **`TurnToolResults = "TOOL_RESULTS"` is the CURRENT** aggregated-results kind. So the snippet keys on the *right* (current) kind; the real bug is narrower but still real — it pairs `tool_result` parts **positionally** within the turn and re-declares the JSONL shape by hand, where the canonical rule (`docs/tools/transcripts.md`) is that results pair to calls **by call ID, never by adjacency**. Adopt only the `api.jsonl`/`ROUND_TIMINGS`/`duration_ms` field model from this doc; never the parser. data-model.md should list turn kinds with `TOOL` (deprecated) vs `TOOL_RESULTS` (current) stated correctly.

### CONFIRMED STALE — `docs/agentic-testing.md` rebuild point #5 + TUI cites
Rebuild #5 says AppWire types are at `internal/appwire/` — the package is top-level **`appwire/`** (no `internal/appwire/`). The falsification/over-spec examples cite `cmd/serf-tui/hub_model.go` for `applyHubNotification`/`handleSessionForceSteer`, now in `hub_notifications.go`/`hub_session_keys.go`. The METHOD (falsification loop, rebuild map, over-spec trap) is the durable extract; shed the stale paths and the inline `grep '"kind"'`/`find ~/.local/state` recipes (→ `serf doctor`).

### NEW — `docs/conventions/naming.md` itself cites the stale `internal/appwire/` path
The naming doc's surface map and Exceptions repeatedly name `internal/appwire/` (and `server/appwire_*.go`) for the camelCase carve-out. Per the agentic-testing finding the package is top-level `appwire/`. The naming RULE (snake-on-disk) is what data-model.md needs and is correct; just don't inherit the `internal/appwire/` path when citing the carve-out.

### NEW — `SERF_STATE_HOME` is invented; the real precedence is split across layers
The doctoring-tools spec describes base = `--state-dir › SERF_STATE_HOME/SERF_STATE_DIR › $XDG_STATE_HOME › $HOME/.local/state`. **`SERF_STATE_HOME` is read nowhere** (verified: no non-test Go file references it). The real, verified precedence is split across two layers:
- `agent/runtime_dir.go` reads **only** `XDG_STATE_HOME` (else `~/.local/state`); it knows nothing of `SERF_STATE_DIR`.
- `SERF_STATE_DIR` is resolved at the **cmd layer** (`cmdutil/statedir.go` `DefaultStateRoot`; `cmd/serf/run.go`/`serve.go`: `--state-dir` flag > `SERF_STATE_DIR` env > XDG) and passed *down* as `overrideDir`/`stateHome`.
- **Adopt:** precedence = `--state-dir` flag › `SERF_STATE_DIR` env › `$XDG_STATE_HOME` › `~/.local/state`; attribute the flag/env override to the launch/config layer, not to `RuntimeDir`. Also: the bucket key is `originURL` **else** `workDir` (one or the other; `agent/runtime_dir.go` `key := originURL; if key=="" { key = workDir }`), **not** a concatenation — the spec's `sha256(originURL|workDir)` shorthand reads as concat and should be corrected. Bucket = `hexHash(key)` = SHA256(key)[:8] → 16 hex chars.

### MINOR — `evicted` is a real fourth watch-send terminal kind
`job-control.md` and the doctoring spec describe pending/delivered/dropped; the code also emits **`watch_send_evicted`** (`EventWatchSendEvicted`). failure-modes.md's "dropped vs coalesced" entry should account for `evicted` as a third settled terminal so the distinct-delivery count is right.

### MINOR — `file:line` drift is endemic (re-confirmed)
The architecture/runtime-contracts/assessment docs and the doctoring spec all carry `file:line` cites that drift within days (the prior audit proved `main.go:170` etc.). **Adoption rule:** the skill cites **symbols/packages by name**, never `file:line` — a renamed symbol is a loud grep-miss, a moved line is a silent lie.

---

## 4. Per-skill-reference rollup — adopt / extract / write-fresh

### data-model.md — **strong existing sources; assemble + correct, don't invent**
- **Adopt-as-reference:** `docs/tools/transcripts.md` (transcript grammar, refs, buckets, scope, result-tool, kind=root/subagent/fork) — link wholesale.
- **Cite-as-source-of-truth:** the Go types (transcript.go / schema / jobstore / provenance / runtime_dir.go) per §2.5; `docs/architecture.md` for state-dir + module shape; `docs/conventions/naming.md` for on-disk JSON casing; `docs/job-control.md` for job-record fields.
- **Extract-the-evergreen-core:** the `api.jsonl`/`ROUND_TIMINGS`/`duration_ms` model from `performance-profiling.md`; the lineage-field inventory from `subagent-management/09`.
- **Write-fresh (thin glue only):** the single page that ties state-dir layout → transcript grammar → `jobs.jsonl`-is-a-folded-event-log → `meta.json` → "durable-vs-live appwire" into prose, **each artifact pointing at its Go type + `serf doctor` reader**. The pieces all exist; only the assembly + the §3 corrections are new. **The single biggest gap the prior audit named (a conceptual data-model reference) has strong source material — it is assembly, not authorship.**

### failure-modes.md — **mechanics exist; the taxonomy framing is the new work**
- **Cite-as-source-of-truth:** `docs/architecture.md` (watch loops, stuck turns, deaf coordinator, single-hop forwarding mechanics); `docs/job-control.md` (status×reason, dropped-vs-coalesced incl. `evicted`, `runtime_lost`/`supervision_lost`).
- **Extract-the-evergreen-core:** the provenance model + suppression invariant from the 06-18 observer design **with §3 type-name + WatchKeys-vs-Chain corrections**; the watch-self-loop worked example + "Resolution" from `2026-06-12-watch-deadlock-design`; the 6-category missing-support taxonomy from `root-cause-task-failure.md` (the eval-failure axis); hook-blocked/failed from `hooks.md`; provider-error from `llm-providers.md`; the provenance-rail probe checklist from `2026-06-19-observer-provenance-remediation-spec`.
- **Adopt the ENTRY SHAPE** from `docs/ollama.md`'s symptom→cause section.
- **Write-fresh:** the taxonomy STRUCTURE — one entry per shape: *symptom → what it actually is → confirm with (`serf doctor …` / which durable record) → mechanics link.* The raw material is fully present but scattered; nobody has assembled the recognize/diagnose table. This is the highest-leverage new authorship.

### finding-contract.md — **a real existing spine in runtime-contracts**
- **Extract-the-evergreen-core:** `subagent-management/10-runtime-contracts.md` Contract 3 (the Diagnostic DTO: `source`/`kind`/`severity`/`field`/`message`; the category enum; redaction; "warnings only where displayed"; structured-fields-over-string-matching) — this is most of the schema.
- **Cite-as-source-of-truth:** the Step-5 report schema in `root-cause-task-failure.md` (a worked Finding shape: DECISION POINT / REASONING / EVIDENCE).
- **Write-fresh:** the doctoring-specific bits — **stable signatures** (so the same defect dedupes run-to-run), the "**healthy run emits zero findings**" invariant, and the JSON envelope the `doctor` agent emits. No existing doc defines a stable-signature/zero-on-healthy Finding contract; that is genuinely new (built ON Contract 3, not from scratch).

### writing-runbooks.md — **MUST-WRITE-FRESH (weakest coverage)**
- **Extract-the-evergreen-core:** the escalation-ladder + rebuild-map METHOD from `agentic-testing.md` (drive-the-UI → read-the-transcript → add-a-probe), stripped of stale paths/recipes.
- **Optional seed:** observer audit-definitions from `2026-06-18-observer-sidecar-use-cases.md` (the 49 cards reduce to ~8 families).
- **Write-fresh:** the actual concept of a serf **runbook/audit-definition** (what a runbook IS in this skill, its schema, how a `doctor` run consumes one). **No existing doc describes "runbook authoring" for serf** — `agentic-testing.md` is E2E-driving, not audit-definition authoring. This reference has the thinnest existing source; treat as primarily new.

### repair-guardrails.md — **MUST-WRITE-FRESH (no existing source)**
- **Cite-as-source-of-truth (boundary anchors only):** `docs/job-control.md` ("Not a mutator" posture; the hub REST mutation surface is out-of-scope for reads) and the doctoring-tools spec's Non-goals (read-only forensics; mutation is the hub's `/api/spawn` etc.); `serf-hub-remote-operations.md` for the ops-repair boundary.
- **Write-fresh:** the entire graduated-repair model (runbooks vs core skills; what a `doctor` agent may auto-repair vs must escalate). **Zero existing coverage** — this is the topic with no source at all. Surprise: the repository has rich *diagnosis* material and essentially **no *repair-policy* material**, because everything shipped to date is read-only forensics; the guardrails are net-new design.

---

## 5. First-cut adoption list (build order)

1. **data-model.md** — assemble from `tools/transcripts.md` (adopt) + the Go types (cite) + `architecture.md`/`naming.md`/`job-control.md` (cite) + the `api.jsonl` extract; apply ALL §3 corrections (jobs.jsonl `sessions/<SID>/` subdir path, `provenance.Causal`/`WatchKeys`, runtime_dir env-layer attribution + 16-hex bucket, `TOOL` deprecated vs `TOOL_RESULTS` current, `evicted` 4th kind). Lowest authorship, highest leverage, closes the prior audit's #1 gap. Each artifact line ends with "read it via `serf doctor <sub>`".
2. **failure-modes.md** — write the taxonomy table fresh, every row citing `architecture.md`/`job-control.md` for mechanics and a `serf doctor` confirm command; extract the provenance + watch-self-loop + 6-category nuggets (corrected). Pairs with data-model.md; ship together (each references the other), exactly as the debugging-docs audit recommended.
3. **finding-contract.md** — lift Contract 3 from `subagent-management/10`; add the stable-signature + zero-on-healthy + JSON-envelope layer (new).
4. **writing-runbooks.md** — extract the escalation-ladder method from `agentic-testing.md`; author the runbook/audit-definition concept (mostly new).
5. **repair-guardrails.md** — author from scratch against the read-only/mutation boundary anchors (entirely new).
6. **SKILL.md** — model structure on `benchmark-driven-improvement/SKILL.md`; the agent-type system prompt cites `tools/transcripts.md` + `agent/prompts/sections/transcripts.md` + `hooks.md` for vocabulary.

**Net:** references 1–3 (data-model, failure-modes, finding-contract) have **strong existing sources** and are assembly-plus-correction; references 4–5 (writing-runbooks, repair-guardrails) are **must-write-fresh**, with repair-guardrails having literally no precedent. The corrections to inherit (from the code, never the spec prose) are, worst first: the **jobs.jsonl on-disk path** (`sessions/<SID>/jobs.jsonl` subdir, NOT beside the transcript — `serf doctor locate` is otherwise wrong); the **provenance type rename** (`CausalProvenance`/`WatchProvenanceKey`/`ProvenanceEntry` → `provenance.Causal`/`WatchKey`/`Entry`, with `WatchKeys` load-bearing and `Chain` diagnostic-only); the **state-dir env precedence layer** (`SERF_STATE_DIR`+`XDG_STATE_HOME` resolved at the cmd layer, `runtime_dir.go` knows only `XDG_STATE_HOME`); `TOOL`-vs-`TOOL_RESULTS` (TOOL is the deprecated one); and `evicted` as the 4th watch-send terminal. The fold/load surface the spec cites (`Fold`/`FoldWatchSends`/`Store.Load*`) is **correct** — not a drift.
