# Serf Debugging Docs — Audit & Proposal

Date: 2026-06-19
Status: draft for Jesse review (UNCOMMITTED)
Sibling: `docs/superpowers/specs/2026-06-19-serf-doctoring-tools.md` (the TOOLS half).
This is the DOCS half: are serf's debugging docs good and evergreen, and how do we make them so. It is an audit + proposal, not a docs rewrite and not tool design. It assumes the `serf doctor` tools from the sibling spec **will exist** and references them; it does not design them.

## TL;DR

Serf's debugging knowledge is **real and mostly accurate, but neither consolidated nor evergreen.** There is one good central runbook (`docs/agentic-testing.md`) and several scattered, single-purpose diagnostic docs. The dominant rot pattern — *re-declaring serf's on-disk JSONL shape in hand-rolled `grep`/`python` recipes* — is not confined to one doc; it is replicated across **~34 `test/scenarios/*.md`** plus `agentic-testing.md`, `performance-profiling.md`, `terminal-bench.md`, and a benchmark `tools-reference.md`. That is the exact failure the doctoring-tools spec was born from, multiplied across the corpus. The biggest *gap* is a conceptual **data-model reference** (what a transcript / `jobs.jsonl` / meta line actually contains) — the load-bearing knowledge an ad-hoc parser lacks. There is no failure-mode taxonomy and no symptom→tool index.

---

## 1. Inventory — docs found, with per-doc verdict

"Debugging-relevant" = teaches how to *diagnose* serf behavior (not feature specs, design plans, or UI mockups). Two distinct debugging axes turned up and should not be conflated:

- **Runtime forensics** — "what did this running session/job/watch actually do, and why is it stuck/looping/silent?" (the doctoring-tools domain).
- **Eval-task-failure diagnosis** — "why did this agent fail the task?" (decision-point reconstruction + interrogation).

### Primary debugging docs

| Doc | Good? | Evergreen? | Stale now? |
| --- | --- | --- | --- |
| `docs/agentic-testing.md` | **Yes** — the central, genuinely useful E2E runbook (setup, hermetic workdir, web/TUI driving, on-disk inspection, falsification debugging, rebuild matrix, over-spec trap). | **Partly.** Durable bones (the 6 rebuild points, falsification loop, over-spec trap) wrapped in drift-prone specifics: hand-rolled `grep -c '"kind":"STEERING"'` recipes, `find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`, three `kata wymv` transient refs, named source files in probe examples. | **Yes — 2 confirmed.** See §2. `internal/appwire/` rebuild path is wrong; the falsification/over-spec source-file citations point at moved files. |
| `test/scenarios/README.md` | Yes — clean explanation of the scenario format + how-to-run. | Mostly. Leans on `~/go/bin/kata` and "file a kata" (live tooling, fine). | No. Accurate. |
| `docs/tools/transcripts.md` | **Yes — excellent.** The canonical design of `find_session_transcripts`/`read_session_transcript`; explains turn-numbering, refs, buckets, scope, the result-tool. This is *the* conceptual onramp to reading a transcript. | **Yes.** Describes concepts and tool contracts, not line numbers or recipes. The single most evergreen debugging-adjacent doc. | No. Cross-checked against `agent/transcript/transcript.go` + renderer; matches. |
| `agent/prompts/sections/transcripts.md` | Yes — the agent-facing steer for the same two tools. | Yes — parameter/format vocabulary only. | No. |
| `docs/openai-spend-diagnostics.md` | Yes — clear metric interpretation for `tools/api-log-analyze.py`. | **Mostly**, but framed around a transient delivery ("This branch adds conservative OpenAI prompt-cache defaults…", §"OpenAI Prompt Cache Defaults"). That section is changelog prose in an evergreen doc. | Not verified field-by-field; the `<state-dir>/api.jsonl` + per-session `api_call` model is consistent with code. |
| `docs/performance-profiling.md` | Yes — useful round-timing/latency/tool-duration recipes. | **No.** Hand-parses the transcript JSONL in Python (`obj['turn']`, `turn.get('kind')`, `p.get('tool_result')`). | **Yes — see §2.** Its tool-duration snippet keys on `kind == 'TOOL_RESULTS'`, the **deprecated** TOOL-turn shape; pairs results positionally, which the renderer explicitly does not do. |
| `docs/hooks.md` | Yes — strong hook-troubleshooting reference (matcher gotchas: Claude tool names vs serf internal names; exit-code semantics; I/O JSON contract). | Yes — contract-level, no line numbers. | Not deeply verified; reads current. |
| `docs/llm-provider-config-and-launch.md` | Yes — credential-resolution precedence + env-var table; the right doc for "why did my provider/model/creds fail to launch." | Yes — mostly tabular contract. | Not deeply verified. |
| `docs/ollama.md` | Yes — a real symptom→cause troubleshooting section (connection refused, model-not-found, bare-text output, truncation). | Yes — procedural, no line numbers. The closest thing in the repo to a failure-mode list, but provider-scoped. | Not verified; reads current. |
| `docs/subagent-management/10-runtime-contracts.md` | Yes for *expected* diagnostic behavior (diagnostic source/field/severity, redaction, error categories, event ordering). | Yes — contracts, not recipes. | Evergreen by construction (lifted from specs); not re-verified here. |

### Eval-failure debugging (separate axis)

| Doc | Verdict |
| --- | --- |
| `docs/skills/benchmark-driven-improvement/root-cause-task-failure.md` | **Excellent, durable methodology** — decision-point reconstruction, blameless agent interrogation, a 6-category missing-support taxonomy. Genuinely evergreen *as method*. Larded with transient anchors: `## Session 21 Additions` / `## Session 22 Additions` headings and inline `subagents.go:237` line cites. The method is gold; the session-log accretions and line numbers are the rot. |
| `docs/skills/benchmark-driven-improvement/tools-reference.md` | Reference for eval tooling (`interrogate_session.py`, `read_transcript.py`, etc.). Useful but re-declares transcript field paths in snippets (drift-prone). Eval-harness-scoped. |
| `docs/skills/benchmark-driven-improvement/root-cause-prompt.md`, `SKILL.md`, `infrastructure.md` | Meta/skill scaffolding; not standalone debugging guides. |

### Architecture (debugging-load-bearing, but not framed as debugging)

| Doc | Verdict |
| --- | --- |
| `docs/architecture.md` | **The durable substrate a failure-mode taxonomy needs but doesn't yet reference.** §"Ownership and mailboxes" + §"Drive-down" precisely explain the mailbox invariant, the three queues, the watch-deadlock that motivated it, and level-by-level drive-down — i.e. the mechanics behind watch loops, stuck turns, undelivered notifications. But it is written as *architecture* ("how a turn flows"), not as *diagnosis* ("a deaf coordinator looks like X; check Y"). It also carries many line-number cites (`agent/session_lifecycle.go:821`, `agent/job_watch.go:2560`, `agent/subagents.go:683`, …) — more defensible in an explanatory architecture doc than in a runbook, but still drift-prone. Notably it never documents the on-disk data model conceptually: `transcript` appears only as "JSONL writer/reader" — *what a line contains* is nowhere. |

### Job-control reference

| Doc | Verdict |
| --- | --- |
| `docs/job-control.md` | Evergreen *contract* for the job/watch/delegate model (statuses, reasons, wait primitives, coalescing semantics, the self-loop boundary). Not a debugging runbook, but it is the **vocabulary source** a failure-mode taxonomy should link to (e.g. `runtime_lost`, `stop_pending`, latest-frame-wins coalescing). Keep as-is; reference from the debugging docs. |

### Bench / ops (peripheral)

- `docs/terminal-bench.md` — benchmark infra reference; hand-parses transcript JSONL over SSH (drift-prone), infra-host-specific.
- `docs/serf-hub-remote-operations.md`, `cmd/serf-hub/README.md` — ops runbooks (health checks, restarts, OAuth). Useful for *ops* debugging; mostly evergreen; not session/job forensics.
- `docs/testing.md` — provider-E2E setup only; **no** inspection/diagnosis content (confirmed). Not a debugging doc.

### Not debugging docs (scoped out)
`README.md`, `ABOUT.md`, `docs/serf-hub-web-routing.md`, `docs/resume-model-context-fix-plan.md`, the `docs/web-ui/**` mockups, and the `docs/superpowers/{plans,research,specs}/**` dated design/plan corpus (history, not runbooks).

---

## 2. Concrete staleness & drift findings

Cross-checked load-bearing claims against current code. Findings, worst first.

### STALE — `docs/agentic-testing.md:322` names a non-existent package path
Rebuild point #5 says: *"**AppWire types** — `internal/appwire/`. Both daemon and hub statically link these; rebuild both."* The package is at **`appwire/`** (module-root, top-level). There is no `internal/appwire/` (verified: `./appwire` exists, `internal/appwire` does not). Anyone following the rebuild recipe gets "no such package." This is the headline example of why inlined paths rot.

### STALE — `docs/agentic-testing.md` falsification & over-spec examples cite moved files
- The falsification example (lines ~330-336) shows the probe *"In `cmd/serf-tui/hub_model.go` `applyHubNotification`"*. `applyHubNotification` and `notificationMatchesCurrentSession` now live in **`cmd/serf-tui/hub_notifications.go`**.
- The over-specification trap (line ~382) says *"`handleSessionForceSteer` in `cmd/serf-tui/hub_model.go` early-returns…"*. `handleSessionForceSteer` now lives in **`cmd/serf-tui/hub_session_keys.go`**.
The *behavior* described is still correct; the file names are wrong. The TUI was split into per-concern files (`hub_notifications.go`, `hub_session_keys.go`, `hub_update.go`, …) and the doc didn't move with it — textbook line/path drift.

### STALE — `docs/performance-profiling.md` parses the deprecated transcript shape
The tool-duration snippet (lines ~91-98) does:
```python
if turn.get('kind') != 'TOOL_RESULTS': continue
...
tr = p.get('tool_result', {})
```
`TOOL_RESULTS` is the **deprecated** TOOL turn kind — `docs/tools/transcripts.md` (§markdown rules) explicitly calls out *"deprecated `TOOL` turns"* and states results pair to calls **by call ID, never by adjacency**. So this snippet (a) keys on a turn kind the current renderer treats as legacy, and (b) reads results positionally. It is a hand-parser already drifting from the schema — the canonical pain #1/#4 from the doctoring spec, sitting live in a "how to profile" doc.

### DRIFT (systemic, not yet "wrong") — re-declared JSONL shape across the corpus
The same `grep -c '"kind":"…"'` / `find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"` / inline-python parse pattern appears in **~34 `test/scenarios/*.md`** (e.g. `job-watch-sidecar-observer.md` does `find … -path "*sessions/$SID/jobs.jsonl"` and asserts on raw `watch_send_pending`/`watch_send_delivered` lines; `job-list-and-recovery.md`, `meta-flush-on-completion.md`, `job-shell-lifecycle.md`, … all hand-`find` the state dir) **plus** `agentic-testing.md`, `performance-profiling.md`, `terminal-bench.md`, and `docs/skills/benchmark-driven-improvement/tools-reference.md`. Each is an independent copy of serf's on-disk shape. None fails loudly when the schema shifts — it just silently returns the wrong number. The `job-watch-sidecar-observer.md` raw-`watch_send_pending`-counting is *already* the over-count bug the doctoring spec calls out (pending lines ≠ deliveries because of latest-wins coalescing in `FoldWatchSends`).

### DRIFT — `kata wymv` transient references in the runbook
`docs/agentic-testing.md` cites `kata wymv` three times (lines 99, 200, 325) as the debug-session that produced an example. `kata` is the live tracker (`.kata.toml` present), but `wymv` is one closed issue — a dead anchor that means nothing to a future reader. `kata`-style transient refs are exactly the class flagged as non-evergreen.

### DRIFT — `docs/openai-spend-diagnostics.md` carries changelog prose
The §"OpenAI Prompt Cache Defaults" opens *"This branch adds conservative OpenAI prompt-cache defaults…"*. "This branch adds…" is release-note language; in an evergreen diagnostics doc it should read as the present-tense behavior, with the history (if needed) in a dated spec.

### MINOR — the runbook hardcodes `~/.serf/auth-token` as absolute
Lines 34/402/407 state the token is at `~/.serf/auth-token`. In code the hub resolves it at `$hubStateRoot/auth-token` (`LoadOrCreateAuthToken(hubStateRoot)`, `cmd/serf-hub/main.go:106` → `hubStateRoot := cfg.HubStateRoot`). `~/.serf/auth-token` is correct only under the default config. A doc that points at `serf --state-dir` / the hub's configured root (or `serf doctor locate`) instead of a fixed `~/.serf` path won't lie when someone sets a custom state root.

### MINOR — even the doctoring spec already carries drifting line numbers
The sibling spec cites `cmd/serf/main.go:170` for the result-tool default (actual flag is at **:158**) and `dispatchCLICommand` at `:248` (actual **:243**, `case "serve"` at :249). Its *type/path* cites that matter most (`agent/jobs.go:267`, `agent/schema/snapshot.go:67`, `fold.go` symbols) are accurate today. The point for *this* audit: even a careful, well-aimed spec rots at the line-number granularity within days. That is the evidence base for §4's principle — cite symbols and packages, not `file:line`.

---

## 3. Gaps — durable knowledge that should be documented but isn't

1. **A serf data-model reference (the #1 gap).** Nowhere does a doc say, conceptually, *what is on disk*:
   - the state-dir layout (`$XDG_STATE_HOME/serf/projects/<origin-hash>/sessions/`, the bucket-per-project indirection, the `--state-dir` override, the flat-dir degenerate case);
   - the **transcript** line grammar — header line, then `transcript.Entry{Kind:"entry", Seq, Turn}` per turn, with `api_call` lines interleaved; a turn's `kind` (`USER`/`STEERING`/`ASSISTANT`/`SUMMARY`/…), `message.content[]` parts, and that a tool call is `kind == tool_call` inside `content[]` (not a top-level key), and a "communicate"/result call is a tool call whose name is the session's result-tool name;
   - **`jobs.jsonl`** — an append-only *event* log folded into state, *not* a row-per-job file; one `watch_send_pending` per coalescing update (latest-wins), so raw counts mislead;
   - **`meta.json`** — `SessionMeta` (title, model, `observed_by[]`, …), derived `kind` (root/subagent/fork);
   - the firm line between **durable transcript/jobs state** (forensics) and the **live `events.SessionEvent` appwire stream** (rendering) — different sources, different jobs.
   This is precisely the knowledge a hand-parser lacks when it returns wrong counts. It should exist as prose that **points at the Go types as the source of truth** and at `serf doctor` as the way to read them, *not* as a copyable parser.

2. **A failure-mode taxonomy.** No doc enumerates the recurring shapes of serf misbehavior with a recognize/diagnose recipe per shape. The raw material exists but is scattered (`architecture.md` mailbox/drive-down, `job-control.md` statuses, ` collection of scenarios`). Candidate entries (each → which `serf doctor`/where-to-look):
   - **Watch self-loop / feedback loop** — observer retriggers the watch that feeds it → `doctor watches --self-loops`.
   - **Dropped vs coalesced delivery** — `pending != delivered` usually = latest-wins coalescing (expected), occasionally a real drop → `doctor watches` (distinct-delivery count vs raw pending lines).
   - **Stuck "processing" / deaf coordinator** — a child with undelivered mailbox attention not being driven down; the drive-down invariant in `architecture.md`. (There is already a `state-stuck-processing-display.md` and `recursion-deaf-coordinator-drivedown.md` scenario — symptoms exist, the *taxonomy entry* doesn't.)
   - **Provenance gaps** — a delivery whose `Causal.Chain` is empty/truncated → `doctor watches` provenance hops.
   - **Orphaned / lost runtimes after restart** — `stopped` + `runtime_lost`; `supervision_lost`; resumability preflight (`job-control.md` covers the contract; nothing says "here's how to recognize it in the wild").
   - **Tool-call vs textual mention** — `grep delegate_send` over-counts; real invocations are `content[]` tool_call blocks → `doctor transcript --count`.

3. **A consolidated debugging methodology.** The falsification-probe loop (add stderr probe → rebuild the right layer → rerun → grep) and the **rebuild-point map** live only inside `agentic-testing.md`, entangled with E2E-driving mechanics. The durable core — *where the seams are, how to bisect a failure across daemon/hub/web/TUI/appwire, when to drop from "drive the UI" to "read the transcript" to "add a probe"* — deserves to be stated as method, decoupled from the specific tmux/browser recipes and (critically) from named `file:line` probe sites.

4. **A symptom → tool/where-to-look index.** There is no single "I observe X → run Y / read Z" table. With `serf doctor` landing, this is the highest-leverage *new* artifact: it turns the whole debugging surface into a lookup keyed on what you actually see.

---

## 4. Evergreen principles for serf debugging docs

1. **Reference canonical types and tools; never inline a parser.** A debugging doc must not contain a `grep`/`python`/`jq` recipe that re-declares serf's on-disk shape. It either (a) points at `serf doctor <sub>` (which reads the real structs/folds and fails to compile if the schema moves), or (b) names the Go type as the schema source of truth (`transcript.Entry`, `schema.Turn`, `jobstore.WatchSendState`, `provenance.Causal`, `schema.SessionMeta`). The doctoring-tools spec's own rule — *"doctoring tools must read serf's OWN canonical types, never re-guess them"* — applies verbatim to the docs.

2. **Cite symbols and packages, not `file:line`.** Line numbers and file *names* drift within days (proven in §2: the runbook's TUI cites, the spec's own `main.go:170`). Cite `package agent`’s `drainPendingWatchSends`, or `agent/internal/jobstore` `FoldWatchSends`, by **name**. A reader greps the symbol; a renamed symbol is a loud miss, a moved line is a silent lie.

3. **Separate durable method from transient recipe.** Architecture, data model, failure taxonomy, methodology, and the symptom index belong in durable docs. Exact CLI invocations, flag spellings, per-tool output shapes, and copy-paste commands belong in tool `--help`, in tests, or in clearly-dated scenario/runbook files — the places that are *supposed* to track the code.

4. **No transient anchors in evergreen docs.** No `kata wymv`, no `## Session 22 Additions`, no "this branch adds…". A specific past debug session can motivate an example, but the example must stand without the dead reference. Histories live in dated `docs/superpowers/specs|plans|research/`.

5. **Point at state, don't hardcode its path.** Prefer `serf doctor locate <selector>` / "the hub's configured state root" over `~/.serf/...` and `~/.local/state/serf/projects/<hash>/...`. Hardcoded absolute paths are correct only under default config and silently wrong under `--state-dir`.

6. **An evergreen doc that must teach a recipe teaches the *tool*, not the bytes.** "How many real `delegate_send` calls?" → `serf doctor transcript --count delegate_send`, not a `content[]`-walking snippet. The doctoring tools exist precisely so the docs can stop shipping parsers.

---

## 5. Recommended structure

Target a small, durable debugging set, with `docs/agentic-testing.md` refactored (not deleted) and everything keyed to the `serf doctor` tools.

### Keep / refactor existing
- **`docs/agentic-testing.md` → split.** It currently fuses two things: (a) *how to drive serf E2E* (hub setup, REST shim, AGENTS.md pacing, tmux/browser conventions, optimistic-rendering assertion shape, cleanup) — which is genuinely useful and should **stay** as the E2E-driving runbook; and (b) *how to diagnose serf* (on-disk inspection, falsification loop, rebuild matrix, over-spec trap) — which should **lift out** into the durable debugging methodology doc and shed its inlined greps/paths in the process. Fix the two confirmed staleness bugs (§2) regardless of the split, since the runbook is in active use.
- **`docs/tools/transcripts.md`** — keep as-is; it is the evergreen transcript-reading onramp. Link to it from the data-model reference.
- **`docs/job-control.md`** — keep as-is; it is the status/reason/coalescing vocabulary the failure taxonomy links to.
- **`docs/architecture.md`** — keep; have the failure-taxonomy doc *reference* its mailbox/drive-down sections rather than restate them. (Optionally, downgrade some of its `file:line` cites to symbol names on its next edit.)
- **`docs/performance-profiling.md`** — keep the *interpretation*; replace the hand-parse snippets with `serf doctor`/api-log-analyze invocations once available, and at minimum fix the `TOOL_RESULTS`/positional-pairing bug now.
- **`docs/openai-spend-diagnostics.md`, `docs/hooks.md`, `docs/llm-provider-config-and-launch.md`, `docs/ollama.md`** — keep; de-changelog the spend doc; these are fine domain-specific troubleshooting refs and the symptom index should point *into* them.

### New durable docs (small set)
1. **`docs/debugging/data-model.md`** — the conceptual on-disk reference (gap #1). Prose + a diagram, citing Go types by name, with a one-line "to read this, run `serf doctor …`" per artifact. The thing my ad-hoc parser needed.
2. **`docs/debugging/failure-modes.md`** — the taxonomy (gap #2). One entry per failure shape: *symptom → what it actually is → how to confirm (`serf doctor …` / which durable record) → links to `job-control.md`/`architecture.md` for the mechanics.*
3. **`docs/debugging/methodology.md`** — the falsification loop + rebuild-point map + "escalation ladder" (drive-the-UI → read-the-transcript → add-a-probe), lifted from `agentic-testing.md`, symbol-cited, recipe-free. The symptom→tool index (gap #4) can be a top section here or its own short page.

(Folder name `docs/debugging/` is a suggestion; could equally be three top-level `docs/*.md`. The point is the three durable artifacts, not the path.)

### Relationship to the doctoring tools
The new docs are the **prose around** `serf doctor`: the tools answer "what happened" with canonical numbers; the docs say "what to ask, what the answer means, where to look next." Symbiotic — every recipe a doc would have hand-rolled becomes a `doctor` invocation, and `doctor --help` carries the flag-level transient detail the docs must not.

### Salvage vs cut
- **Salvage:** the falsification loop, rebuild matrix, over-spec trap, and on-disk-inspection *concepts* from `agentic-testing.md`; the entire interrogation method from `root-cause-task-failure.md`; the mailbox/drive-down mechanics from `architecture.md`; the metric interpretation from the spend/profiling docs.
- **Cut / replace:** every inlined `grep '"kind"…'` and `find ~/.local/state/...` recipe (→ `serf doctor`); `kata wymv` and `## Session NN Additions` anchors; "this branch adds…" prose; `file:line` probe-site citations (→ symbol names); the `TOOL_RESULTS`/positional transcript parse.

---

## 6. First cut — the 1-2 highest-value changes

**Fix #1 (do first): create `docs/debugging/data-model.md` and `docs/debugging/failure-modes.md`, anchored to `serf doctor`.** These two close the two biggest gaps (the data model my parser lacked; the taxonomy that turns "weird behavior" into "known shape + confirming command"). They are durable by construction — concepts + type names + tool pointers, zero recipes — so they don't re-enter the rot. Land them as the prose companion to the doctoring tools (they can ship in the same change, each referencing the other). This is where the leverage is: it converts the scattered, drift-prone tribal knowledge into two evergreen pages.

**Fix #2 (cheap, do alongside): de-rot `docs/agentic-testing.md` in place.** Even before the full split, two confirmed bugs make the runbook actively misleading and are ~10-line fixes:
- correct rebuild point #5 `internal/appwire/` → `appwire/`;
- correct the TUI source-file cites (`hub_model.go` → `hub_notifications.go` / `hub_session_keys.go`), or better, replace them with symbol names so they stop drifting;
- while there: drop the three `kata wymv` anchors and swap the hand-rolled `grep`/`find` inspection block for a "use `serf doctor` (see `docs/debugging/...`)" pointer.

Everything else — the full `agentic-testing.md` split, `methodology.md`, the symptom index, mass-replacing the ~34 scenario recipes — follows once the data-model + taxonomy + doctoring tools give them a canonical thing to point at.
