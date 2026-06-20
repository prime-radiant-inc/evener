---
name: doctoring-serf
description: Use when diagnosing a serf session, job, watch, or the session tree — investigating watch self-loops, dropped/coalesced deliveries, "how many times did X actually get called", stuck turns, or parent↔delegate↔observer linkage. Covers the serf-doctor forensic tools, the Finding contract, runbook authoring, and graduated repair.
---

# Doctoring serf

On-demand forensic diagnosis of serf sessions, jobs, watches, and the session
tree — reading canonical durable state through the `serf-doctor` tools.

**Core principle:** You diagnose by reading serf's own folds and types through
the `serf-doctor` tools, never by hand-parsing JSONL. The tools import serf's
canonical code (`jobstore` folds, `provenance`, `transcript`), so the numbers
they report are the numbers the runtime computed. **A healthy run emits zero
findings.**

## HARD GATE: consult the data model, inspect through the tools, never hand-parse

Two coupled rules, both mandatory:

1. **Before reading any artifact, read `references/data-model.md`.** It is the
   conceptual map an ad-hoc parser lacks — what is on disk, what each field
   means, and which Go type is the source of truth. Hand-written parsers guessed
   the JSONL shape wrong and returned confident zeros (`0 communicate calls`,
   `0 steering entries`). Do not guess. Consult.
2. **Inspect through `serf-doctor`, never an ad-hoc one-off parser.** `grep -c
   watch_send_pending` overcounts deliveries (pending frames coalesce). `grep -c
   delegate_send` counted 5 where the real invocation count was 0 (the hits were
   a tool list and an instruction). The tools exist precisely because these
   reconstructions rot. Run the tool.

This mirrors serf's own `agent/prompts/sections/transcripts.md` rule: use the
transcript tools, do not read raw transcript files directly.

## The diagnose → findings loop

1. **Pick / load a runbook** (a markdown audit definition in `runbooks/`).
2. **INSPECT** by running `serf-doctor <cmd>` on the target selector. Pull live
   state first — never hardcode session ids or thresholds.
3. **CLASSIFY** each result: PASS-with-a-note, or a confirmed, actionable
   problem.
4. **Emit** a structured Finding per confirmed problem (see the contract below).
5. **Report** back to the caller in natural language.

serf *is* the agentic loop — these are your steps, not a separate engine.

## The serf-doctor tools

Run them via the shell tool. First positional arg is a session selector:
`local:<id>`, `proj:<hash>:<id>`, or a bare `<id>` (searched across buckets).
`--json` emits the struct; `--state-dir <path>` targets a scratch root.

| Command | Answers | Key flags |
|---|---|---|
| `serf-doctor locate <sel>` | where are this session's transcript / meta / jobs files? | `--all-buckets` |
| `serf-doctor transcript <sel>` | render the turns; **how many real `X` calls?** | `--count <tool>`, `--format outline\|markdown`, `--range last:N` |
| `serf-doctor apilog <sel>` | per-call tokens/latency, **empty responses, errors, cache spikes**, session token spend | `--empty`, `--errors`, `--cache-spikes [--threshold N]`, `--summary` |
| `serf-doctor watches <sel>` | distinct deliveries (collapsing coalescing), provenance, **self-loop verdict** | `--watch <id>`, `--self-loops` |
| `serf-doctor tree <sel>` | parent ↔ delegate/observer tree across buckets | `--depth N`, `--observers` |

Flag-level detail lives in each subcommand's `--help`, not here.

## The Finding contract (in brief)

A Finding is structured JSON with: `signature` (a stable dedup key), `severity`,
`category`, `title`, `description`, `evidence` (at least the `serf-doctor`
invocation that surfaces it), and `suggestedFix` routing (`diagnosis` /
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
| Hand-parse JSONL with grep/jq/python | Run `serf-doctor <cmd>` |
| Read an artifact before reading `data-model.md` | Consult the data model first |
| Count `watch_send_pending` lines as deliveries | Read distinct deliveries from `serf-doctor watches` |
| Treat a `delegate_send` text mention as a call | `serf-doctor transcript --count delegate_send` |
| Use `ContainsWatch` / `WatchKeys` as a self-loop verdict | Read the `Chain` for a same-`watch_id` **prior** hop (the recorded `WatchKeys` stamp is always present — vacuously true) |
| Emit a PASS / FYI / "looks fine" finding | Emit only confirmed, actionable problems; healthy ⇒ zero |
| Silently apply a core-skill or doctor-tool repair | Propose only, behind review + the validation gate (`repair-guardrails.md`) |
