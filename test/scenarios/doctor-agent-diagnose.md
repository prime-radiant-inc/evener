# doctor-agent-diagnose: the `doctor` agent type runs a real LLM-driven diagnosis end-to-end

**What this covers**: the `doctor` agent type (`internal/bundled/agents/doctor.md`) driving
a live diagnosis loop — loading the `doctoring-serf` skill, invoking the
`serf-doctor` tools via the shell tool, classifying results, and emitting (or
withholding) structured Findings. This is the full collapse the design bets on:
**serf is the agentic loop; the doctor is a persona + a skill + compiled tools.**

Two halves, both verified live against `openai/gpt-5.4-mini`:

1. **Healthy ⇒ zero findings** (the discipline) on a real session.
2. **A real defect ⇒ exactly one schema-correct Finding** (catches the problem).

Unit coverage: `TestBuiltinAgents_LoadsDoctor` (the persona parses, registers,
carries the skill + shell/edit tools). This card proves the *behavior*.

## Pre-state

- Built `serf` and `serf-doctor` (`make build && make build-doctor`), with
  `serf-doctor` reachable from the doctor's shell (on `PATH` or in cwd).
- A working provider (e.g. `openai/gpt-5.4-mini`).
- Run from the serf repo root with a Serf binary that includes the bundled
  `doctoring-serf` skill.

## Steps

### A. Healthy real session ⇒ zero findings

Pick a real session with watch deliveries (coalescing is fine — it is *expected*,
not a fault). Run the doctor persona directly:

```bash
PATH="$PWD:$PATH" serf --agent doctor --model openai/gpt-5.4-mini run \
  --prompt "Diagnose serf session <SID> for watch self-loops and delivery health.
            Use the serf-doctor tools and the doctoring-serf skill. Report findings;
            a healthy run emits zero findings."
```

ASSERT the doctor:
- invokes `serf-doctor watches <SID> --json` (and/or `--self-loops`) via the shell
  tool — it does NOT hand-parse `jobs.jsonl`;
- reports **zero findings**, and explicitly treats coalescing
  (`pending_lines > distinct_deliveries`) as expected, NOT a finding;
- emits a structured result with `findings: []`.

Observed (real run, session `01KVF40N0MV1R492KM4QJY7QN0`): the doctor ran
`serf-doctor watches … --json`, then reported *"No findings: watch self-loops
were not detected … 8 pending lines coalesced into 4 distinct deliveries"* and
`{"findings":[]}`. **PASS.**

### B. Defective session (self-loop) ⇒ one Finding

Synthesize a scratch state dir with a watch whose delivery's provenance `Chain`
carries a same-`watch_id` **prior** hop (a self-loop that escaped suppression):

```bash
SCR=$(mktemp -d); BSID=01BROKENLOOPAAAAAAAAAAAAAAA; mkdir -p "$SCR/sessions/$BSID"
printf '{"kind":"header","session_id":"%s"}\n' "$BSID" > "$SCR/sessions/$BSID.transcript.jsonl"
printf '{"id":"%s"}' "$BSID" > "$SCR/sessions/$BSID.meta.json"
cat > "$SCR/sessions/$BSID/jobs.jsonl" <<'JOBS'
{"kind":"watch_registered","seq":1,"watch_id":"wLOOP","watch":{"generation":"g1","target":"caller","send_to":"obs"}}
{"kind":"watch_send_delivered","seq":2,"watch_id":"wLOOP","watch_send":{"key":{"watch_id":"wLOOP"},"delivery_id":"dl2","provenance":{"watch_keys":[{"watch_id":"wLOOP","watch_generation":"g1"}],"chain":[{"kind":"watch","watch_id":"wLOOP","delivery_id":"dl1"},{"kind":"watch","watch_id":"wLOOP","delivery_id":"dl2"}]}}}
JOBS

PATH="$PWD:$PATH" serf --agent doctor --model openai/gpt-5.4-mini run \
  --prompt "Diagnose serf session $BSID for watch self-loops. The state dir is $SCR
            (pass it via --state-dir). Use the observer-self-loop runbook. Report findings."
```

ASSERT the doctor emits **exactly one** Finding conforming to the contract:
- `category: watch_self_loop`, `severity: high`;
- `signature: watch_self_loop:<SID>:wLOOP` (the structural-defect signature format);
- `evidence.deliveryIds == ["dl2"]` and `evidence.doctorCommand` is the
  `serf-doctor watches … --self-loops --json` invocation it actually ran;
- `suggestedFix.type: diagnosis` (a self-loop that escaped suppression is a bug in
  **serf**, not the doctor's machinery — report-only, per the finding contract's
  accepted scope limit).

Observed (real run): the doctor ran `serf-doctor watches --state-dir $SCR $BSID
--self-loops --json`, caught `dl2`, and emitted the Finding above verbatim
(`signature: watch_self_loop:01BROKENLOOPAAAAAAAAAAAAAAA:wLOOP`,
`suggestedFix.type: diagnosis`). **PASS.**

## Falsifiable failure modes

- If half A emitted a finding for coalescing, the doctor would be miscalibrated
  (visibility ≠ violation).
- If half B emitted zero findings, the self-loop verdict (Chain walk) or the
  runbook's CLASSIFY step would be broken.
- If the doctor read `jobs.jsonl` by hand instead of running `serf-doctor`, it
  would be violating the HARD GATE.
