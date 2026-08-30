---
name: doctoring-evener
description: Use when diagnosing a evener session, job, watch, or the session tree — investigating watch runaways (self-influence depth + runaway-fuse drops), dropped/coalesced deliveries, "how many times did X actually get called", stuck turns, or parent↔delegate↔observer linkage. Covers the evener doctor forensic tools, the Finding contract, runbook authoring, and graduated repair.
---

# Doctoring evener

On-demand forensic diagnosis of evener sessions, jobs, watches, and the session
tree — reading canonical durable state through the `doctor_evener` tool (the
in-process `evener doctor` data plane).

**Core principle:** You diagnose by reading evener's own folds and types through
the `doctor_evener` tool, never by hand-parsing JSONL. The tool imports evener's
canonical code (`jobstore` folds, `provenance`, `transcript`, `apilog`), so the
numbers they report are the numbers the runtime computed. **A healthy run emits
zero findings.**

## HARD GATE: consult the data model, inspect through the tools, never hand-parse

Two coupled rules, both mandatory:

1. **Before reading any artifact, read `references/data-model.md`.** It is the
   conceptual map an ad-hoc parser lacks — what is on disk, what each field
   means, and which Go type is the source of truth. Hand-written parsers guessed
   the JSONL shape wrong and returned confident zeros (`0 communicate calls`,
   `0 steering entries`). Do not guess. Consult.
2. **Inspect through `doctor_evener`, never an ad-hoc one-off parser.** `grep -c
   watch_send_pending` overcounts deliveries (pending frames coalesce). `grep -c
   delegate_send` counted 5 where the real invocation count was 0 (the hits were
   a tool list and an instruction). The tools exist precisely because these
   reconstructions rot. Run the tool.

This mirrors evener's own `agent/prompts/sections/transcripts.md` rule: use the
transcript tools, do not read raw transcript files directly.

## The diagnose → findings loop

1. **Pick / load a runbook** (a markdown audit definition in `runbooks/`).
2. **INSPECT** by calling `doctor_evener` with the command and selector. Pull live
   state first — never hardcode session ids or thresholds.
3. **CLASSIFY** each result: PASS-with-a-note, or a confirmed, actionable
   problem.
4. **Emit** a structured Finding per confirmed problem (see the contract below).
5. **Report** back to the caller in natural language.

evener *is* the agentic loop — these are your steps, not a separate engine.

## The evener doctor tools

Call the `doctor_evener` tool — the in-process equivalent of the `evener
doctor` CLI, running against this session's own state root by default (no
shell, no PATH, no cwd dependence; the CLI shell-out failed with
`command not found` when the binary wasn't installed). Its `selector` argument
is the CLI's first positional: `local:<id>`, `proj:<hash>:<id>`, or a bare
`<id>` (searched across buckets). `state_dir` targets a different root.
Results are the CLI's `--json` struct shapes. The same commands also run as
`evener doctor <cmd>` for humans and scripts.

| Command | Answers | Key arguments |
|---|---|---|
| `locate <sel>` | where are this session's transcript / private API log / meta / jobs / client-mutation files? | — |
| `sessions` | enumerate sessions for a batch study — id, bucket, timestamps, model(s), turn count, transcript bytes, subagent/parent linkage, outcome hint | `since DUR`, `bucket B` |
| `transcript <sel>` | render the turns; **how many real `X` calls?** | `count`, `format`, `range`, `text_max`, `full_text` |
| `transcript <sel>` + `full_text` | **is one response repeating itself?** Turn text and tool-result previews both render capped at 200 bytes by default; a salvaged partial response carries its repeated tool calls as *text*, so no tool-call metric counts them and the cap hides the whole loop. Narrow with `range` first — one turn can run to tens of kilobytes. | `range`, `text_max` for a middle ground |
| `transcript <sel>` + `health` | one session's mechanical metrics in one shot — tool calls/errors by class, longest identical-call run (+ whether it's all errors), truncation warnings, steering counts, jobs by terminal reason, stale notifications, user corrections (proxy) | — |
| `apilog <sel>` | summarize canonical `sessions/<sid>.api.jsonl` attempt identity/grouping/finality, tokens/latency, **empty responses, errors, cache spikes**; `validate` strictly decodes offset zero..EOF and reports every corrupt/malformed/oversized/unsupported record with its offset (whole-file scan, explicit diagnostics only) | `empty`, `errors`, `cache_spikes` + `threshold`, `summary`, `validate`, `recompute` |
| `apilog <sel>` + `health` | one-line API-health verdict — attempts, recorded-empty count, retry-storm groups (≥3 attempts), unsettled groups, errors by class (quota/permanent/retryable) | — |
| `jobs <sel>` | **what jobs has this session run**, and **what state is job X in** — status, reason, exit code, output bytes, start/end times, delegate/transcript/parent links | `job_id` |
| `mutations <sel>` | **did the user's input reach the daemon?** — the journal of every client mutation the daemon accepted or rejected, plus the durable input queue, pending executions, accepted turns, and queue revision | — |
| `watches <sel>` | distinct deliveries (collapsing coalescing), provenance, **breaker telemetry (self-influence depth + runaway drops)**, and the **target job's state** — "why didn't my watch fire" | `watch_id`, `self_loops` |
| `tree <sel>` | parent ↔ delegate/observer tree across buckets | `depth`, `observers` |
| `turnids` | sweep every session under the state root for reserved turn ids minted inside the transcript's entry-index namespace — duplicate/overflow collisions | — |
| `audit` | run a runbook's mechanical `audit:` checks across a whole session set, deduped into contract-valid Findings, plus a pattern × session-count summary and every non-mechanical CLASSIFY step surfaced as `manual` (never silently skipped) | `runbook NAME`, `sessions <sel,...>` \| `since DUR` |
| `plugins` | plugin-store health — registry/disk drift, marketplace health, component validity, auto-upgrade sanity. Note: its store-writability probe creates and removes one temp file in the plugin store (not session state), and it reads the default plugin store root, not the session's state root. | — |

Flag-level detail lives in the CLI's per-subcommand `--help` and in the
`doctor_evener` tool's argument descriptions, not here.

**apilog empties caveat:** `recorded_empty` (and `apilog --health`'s
`errors_by_class`) reflect the compact fields recorded at call time, not a
re-decode of the response body. Logs written before the
Responses-API-decoder fix (WS1, merged to main 2026-08-06 as `812eb5c15`)
can under-record non-empty responses as empty; call `doctor_evener` with
command `apilog` and `recompute: true` to re-extract text/tool-call counts
from the stored body for those rows (adds `recomputed_txt`/`recomputed_tools`
columns and a `recomputed_nonempty` total) before trusting an empty-response
verdict on pre-fix logs.

## The Finding contract (in brief)

A Finding is structured JSON with: `signature` (a stable dedup key), `severity`,
`category`, `title`, `description`, `evidence` (at least the `doctor_evener`
call that surfaces it), and `suggestedFix` routing (`diagnosis` /
`runbook` / `skill`). **Every finding is actionable or it is not emitted** — no
FYI/PASS noise. **Healthy ⇒ zero findings.** Full schema:
`references/finding-contract.md` (read it before you emit).

## When to pull each reference

- "What is on disk / what does this field mean?" → **`references/data-model.md`** (ALWAYS, before reading any artifact)
- "I see weird behavior X — what is it?" → `references/failure-modes.md`
- "I'm about to emit a finding" → `references/finding-contract.md`
- "I'm writing or registering a runbook" → `references/writing-runbooks.md`
- "I want to repair a runbook, a doctor tool, or a core skill" → **`references/repair-guardrails.md`** (ALWAYS, before any repair)

## Anti-patterns

| Don't | Do |
|---|---|
| Hand-parse JSONL with grep/jq/python | Call `doctor_evener` |
| Read an artifact before reading `data-model.md` | Consult the data model first |
| Count `watch_send_pending` lines as deliveries | Read distinct deliveries from `doctor_evener` `watches` |
| Read a watch with zero deliveries as broken delivery machinery | Read the `target job:` line on the same row — a target already terminal, or one that produced zero output bytes, could never match the condition |
| `jq` the client-mutation store to see whether a message arrived | `doctor_evener` `mutations` — absence from the journal is the "it never arrived" verdict |
| Treat a `delegate_send` text mention as a call | `doctor_evener` transcript with `count: delegate_send` |
| Read a turn that ends in `…` as the whole response, or conclude "no loop" from `longest_identical_run: length=0` | Both are blind to repetition *inside* one response: a salvaged partial response's repeated calls are text, not tool calls. Re-read the turn with `doctor_evener` `transcript`, `range` set to that turn and `full_text: true`, before ruling a loop out |
| Treat any self-influenced delivery as a bug, or re-derive a loop from the `Chain` | Self-influence is normal; flag only a runaway — read the recorded breaker telemetry (`max_self_influence_depth`, `runaway_drops`) via `doctor_evener` `watches` with `self_loops: true` |
| Emit a PASS / FYI / "looks fine" finding | Emit only confirmed, actionable problems; healthy ⇒ zero |
| Silently apply a core-skill or doctor-tool repair | Propose only, behind review + the validation gate (`repair-guardrails.md`) |
