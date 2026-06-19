# Writing runbooks

A serf runbook is a **self-contained markdown audit definition** the doctor
persona executes. It is not code — serf loads markdown by path, so a runbook is
*registered by living in `runbooks/`* and being named to the doctor. There is no
`index.ts`, no registry call.

A runbook encodes three questions (adapted from meta-doctor's
HEALTHY/INSPECT/CLASSIFY), each a heading:

## The three sections

1. **HEALTHY** — what steady state looks like, in terms of `serf-doctor` output.
   Be precise about the *signal* (e.g. "no settled delivery's `Chain` has a
   same-`watch_id` prior hop"), and call out any look-alike that is **not** the
   signal (e.g. the always-present `WatchKeys` stamp). A reader must be able to
   tell PASS from FAIL without guessing.
2. **INSPECT** — the exact `serf-doctor <cmd>` invocations to run. **Pull live
   state first; never hardcode a session id, a watch id, or a threshold** — take
   them from the target selector the runbook is invoked with. Prefer `--json` so
   CLASSIFY keys on fields, not on parsing prose.
3. **CLASSIFY** — which results are PASS-with-a-note and which **emit a Finding**.
   Name the exact `category`, `severity`, `signature` format, and
   `suggestedFix.type` for each emit (see `finding-contract.md`). State plainly:
   **a healthy run emits zero findings.**

## Authoring checklist

- [ ] **Parameterized off the target.** No literal SIDs/hashes/thresholds; read
      them from the selector / config.
- [ ] **Violations separated from visibility.** A count that is *expected* (e.g.
      coalescing: "8 pending lines → 4 deliveries") is a note, not a finding.
- [ ] **Stable signature per emit.** Deterministic across runs so dedup works
      (`{shape}:{sessionID}:{watchID|turn}` or `{runbook}:{category}:{bucket}`).
- [ ] **Zero-on-healthy.** A runbook that fires on every healthy session is
      miscalibrated, not thorough. If the HEALTHY case would emit, fix the
      predicate.
- [ ] **Confirm through the tools.** Every INSPECT step is a `serf-doctor`
      invocation, never an ad-hoc grep/jq over raw JSONL.
- [ ] **Registered by path.** Drop it in `runbooks/`, name it to the doctor.

## Skeleton

```markdown
# Runbook: <short audit name>

**Question:** <the one diagnostic question this run answers>

## HEALTHY
- <the precise PASS signal, in serf-doctor terms>
- Note: <any look-alike that is NOT the signal>

## INSPECT
Take the target session id from the runbook invocation — never hardcode one.
\`\`\`
serf-doctor <cmd> <selector> [--flag] --json
\`\`\`

## CLASSIFY
- <result that emits> → Finding: category=<…>, severity=<…>,
  signature=<format>, evidence={doctorCommand, …}, suggestedFix.type=<…>
- <expected/PASS result> → emit nothing.
A run that finds nothing is the expected, correct outcome.
```

## Worked sketch

The seed `observer-self-loop.md` is the canonical example: HEALTHY = no
same-`watch_id` prior `Chain` hop; INSPECT = `serf-doctor watches <selector>
--self-loops --json`; CLASSIFY = each `self_loop.detected == true` → one
`watch_self_loop` Finding, empty output → PASS. Read it before authoring a new
one.

## When you are *extending* (authoring a brand-new runbook)

Authoring a new runbook is the **Extend** capability — autonomous (it adds a
runbook, not the doctor's own machinery), subject to this contract. Repairing an
*existing* runbook, or anything deeper, is gated: see `repair-guardrails.md`.
