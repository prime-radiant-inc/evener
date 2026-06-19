# Repair guardrails — graduated by blast radius

Read this before **any** repair. The doctor is itself made of skills, a persona,
and its own Go tools, so repairing them is a **bootstrapping hazard**: a bad edit
to the doctor's own machinery corrupts the thing doing the diagnosis. Authority
is therefore graduated by how far a change can blast.

The discipline is adopted wholesale from meta-doctor's repair pipeline:
**propose, validate, never silently apply** load-bearing changes, with
**self-consistency** (N candidates, majority agreement) for code.

| Capability | Blast radius | Authority |
|---|---|---|
| **Diagnose** — run runbooks → Findings | read-only | **Autonomous.** No mutation. |
| **Extend** — author a *new* runbook | adds a runbook, not the doctor's own machinery | **Autonomous authoring**, subject to the runbook contract (zero-on-healthy, stable signature). This is also how the corpus stays evergreen. |
| **Heal — runbook repair** — fix an *existing* runbook | one runbook | **Propose + light validation.** Re-run the runbook against a **known-healthy** and a **known-broken** target; the repair must emit **zero** findings on healthy and **catch** the broken case before it lands. |
| **Heal — core-skill / doctor-tool repair** — the `doctoring-serf` skill, the persona, a reference, **or the serf-doctor Go tools** (`agent/doctor` + `cmd/serf-doctor`) | the doctor's own foundation | **Propose-only + review + a validation gate — NEVER silently applied.** A human (or an explicitly-authorized higher-authority agent) approves before it is written. |

## The one-line rule

> Diagnosis and runbook *authoring* may be autonomous. Runbook *repair* is
> propose-plus-validate. Core-skill and doctor-tool repair are **propose-only
> behind review + a validation gate** — that tier can corrupt the doctor itself.

## Core-skill / doctor-tool repair — the two gates

**Go-tool repair** (the `agent/doctor` package + the `cmd/serf-doctor` main):
1. **Self-consistency first.** Generate **N candidate edits** independently and
   require a **majority (≥2) agreeing by byte-identical normalized source** (this
   is meta-doctor's winner-selection — it bites on *code*) **before the proposal
   is even surfaced.** A lone candidate is not a proposal.
2. **Then the concrete validation gate**, all required:
   - it **compiles** (`go build ./...`);
   - **`go test`** passes (the tools' goldens — `agent/doctor` + the cmd test);
   - **`data-model.md` is updated in the same change** if the on-disk format note
     moved, so the conceptual reference tracks the code.

**Prose repair** (a reference, the persona, the skill text): the ≥2 byte-majority
**does not apply** — independent prose rewrites are essentially never
byte-identical. These go through **review + the consult/validation gate** (does it
still match the verified §8 facts? does it still describe the real
`serf-doctor` surface?), not a byte-vote.

## Why this shape

This mirrors meta-doctor's "every load-bearing patch goes through N-candidate
self-consistency and is validated before it lands," **tightened** at the
core-skill / doctor-tool tier because that tier — including the Go tools the data
plane is built from — can corrupt the doctor itself. Diagnosis is the value;
repair is the hazard, so repair is deliberately the most constrained capability.

A finding that is a genuine bug in **serf itself** (not the doctor's machinery)
has no repair route here at all — it routes to `diagnosis` (report-only). The
doctor's Heal authority covers its *own* runbooks, tools, and skills, never
serf's product code.
