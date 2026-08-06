# Writing runbooks

A serf runbook is a **self-contained markdown audit definition** the doctor
persona executes. It is not code — serf loads markdown by path, so a runbook is
*registered by living in `runbooks/`* and being named to the doctor. There is no
`index.ts`, no registry call.

A runbook encodes three questions (adapted from meta-doctor's
HEALTHY/INSPECT/CLASSIFY), each a heading:

## The three sections

1. **HEALTHY** — what steady state looks like, in terms of `serf-doctor` output.
   Be precise about the *signal* (e.g. "no watch has a `runaway` drop"), and call
   out any look-alike that is **not** the signal (e.g. a bounded self-influenced
   delivery — normal, not a runaway). A reader must be able to tell PASS from FAIL
   without guessing.
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

The seed `observer-self-loop.md` is the canonical example: HEALTHY = no watch has
a `runaway` drop (bounded self-influence is normal); INSPECT = `serf-doctor
watches <selector> --self-loops --json`; CLASSIFY = each watch with
`runaway_drops > 0` → one `watch_runaway` Finding, empty output → PASS. Read it
before authoring a new one.

## The `audit:` block — mechanical checks `serf-doctor audit` drives

CLASSIFY's INSPECT/prose form is written for an LLM operator's judgment.
`serf-doctor audit --runbook NAME (--sessions <sel,...> | --since DUR)`
is the **batch** driver: it runs a runbook's threshold checks over a whole
session set mechanically, dedups the results into Findings by signature
(one Finding, every affected session listed in its evidence), and prints a
pattern × session-count summary table. It only executes checks — any other
CLASSIFY bullet is prose for a human/LLM operator, and the driver reports it
under `manual`, never silently drops it.

A runbook opts into batch driving by adding one fenced YAML block, inside
CLASSIFY, whose top-level key is `audit:` — a list of checks. The block
**must** live inside CLASSIFY: `ParseRunbook` only looks for it there, and
errors loudly if it finds one anywhere else (HEALTHY, INSPECT, or before any
heading) rather than accepting it silently — a misplaced block usually means
the author meant to gate it on a section that no longer runs as CLASSIFY
prose.

```yaml
audit:
  - title: "Run-timeout jobs wasting budget"
    severity: high
    category: timeout
    metric: jobs.run_timeout
    op: ">="
    value: 5
  - title: "Long identical-error tool-call run"
    severity: medium
    category: provider_error
    all:
      - metric: longest_identical_run.errors
        op: "=="
        value: true
      - metric: longest_identical_run.length
        op: ">="
        value: 3
```

### Check fields

| Field | Req | Notes |
|---|---|---|
| `title` | yes | one-line label; becomes the Finding's `title` and (slugified) part of its `signature`. Must be unique per `category` within the runbook — `ParseRunbook` rejects a duplicate `(category, title)` pair at parse time, because `auditSignature` keys on exactly that pair and a collision would silently merge two different checks' evidence into one Finding. |
| `severity` | yes | `low` \| `medium` \| `high` — becomes the Finding's `severity`. |
| `category` | yes | a `finding-contract.md` category (mint a new one only for a genuinely new forensic shape, per that doc's own allowance) — becomes the Finding's `category`. |
| `suggested_fix` | no | `diagnosis` \| `runbook` \| `skill`, per the contract's routing table. Default: `diagnosis`. |
| `metric`, `op`, `value` | one condition | a single threshold: session metric `op` value. |
| `all` | compound condition | a list of `{metric, op, value}` clauses, ANDed. Use this **instead of** the top-level `metric`/`op`/`value` (never both) when a check needs more than one clause to hold — e.g. "the longest identical run was all-errors AND at least 3 calls long". |

`op` is one of `>=`, `>`, `<=`, `<`, `==`, `!=` (`==`/`!=` only for a boolean
or string metric).

A session "trips" a check when every one of its conditions holds. Every
session that trips the same check in one run collapses into **one** Finding
— that check's `title`/`category`/`signature` — with every tripped session
listed in `evidence.sessionRefs`. `evidence.doctorCommand` is the
`serf-doctor audit --runbook <name> --sessions <ref,...>` invocation that
reproduces it, scoped to exactly the affected sessions.

### Metric namespace

Metrics are dotted paths resolved against Task 2's `transcript --health`
result (`agent/doctor/health.go`'s `HealthResult`) and, only when a check
references one, the session's `apilog` summary totals and/or its Task 4
`apilog --health` verdict (`agent/doctor/apilog.go`'s `APILogTotals` and
`APIHealthResult` — each decode is skipped entirely for a runbook that never
needs it):

| Metric path | Reads |
|---|---|
| `jobs.<reason>` | `HealthResult.Jobs.ByTerminalReason[<reason>]` — e.g. `jobs.run_timeout`. An absent reason reads as 0, not an error. |
| `jobs.zero_output_terminal` | `HealthResult.Jobs.ZeroOutputTerminal` |
| `longest_identical_run.length` | `HealthResult.LongestIdenticalRun.Length` |
| `longest_identical_run.errors` (alias `.all_errors`) | `HealthResult.LongestIdenticalRun.AllErrors` (boolean) |
| `longest_identical_run.tool` | `HealthResult.LongestIdenticalRun.Tool` (string) |
| `truncation_warnings` | `HealthResult.TruncationWarnings` |
| `steering.<kind>` | `HealthResult.Steering[<kind>]` — the `events.SteeringKind*` vocabulary |
| `stale_notifications` | `HealthResult.StaleNotifications` |
| `user_corrections` | `HealthResult.UserCorrections` (a proxy metric — see health.go's own caveat) |
| `tool_calls.<tool>` | `HealthResult.ToolCalls[<tool>]` |
| `tool_errors.<tool>.<class>` | `HealthResult.ToolErrors[<tool>][<class>]` |
| `apilog.calls` / `.empties` / `.errors` / `.avg_latency_ms` | the session's `APILogTotals` (`serf-doctor apilog --summary`'s fields) |
| `apilog.recorded_empty` / `.retry_storm_groups` / `.unsettled_groups` | the session's `APIHealthResult` (`serf-doctor apilog --health`'s fields) — `retry_storm_groups` counts attempt groups with 3+ recorded attempts, `unsettled_groups` counts groups with no settlement record |
| `apilog.errors_by_class.<class>` | `APIHealthResult.ErrorsByClass[<class>]` — `<class>` is one of `quota`, `permanent`, `retryable` (see `agent/doctor/apilog.go`'s `classifyAPIErrorClass` for the recorded-field mapping) |

An unknown namespace, or a malformed path (e.g. `jobs` with no reason), is a
loud parse/eval error — never a silent zero.

### Worked example

The checks above are the fixture `agent/doctor/audit_test.go`'s
`fixtureRunbookMD` exercises end to end: `jobs.run_timeout >= 5` (high,
`category: timeout`) flags sessions that burned budget on jobs the runtime
had to kill for running too long; `longest_identical_run.errors &&
longest_identical_run.length >= 3` (medium, `category: provider_error`)
flags a stuck retry loop the runtime's own loop detector would also have
caught. A standing `error-loop.md` runbook built on this pattern is planned
(WS9 Task 5) — read the fixture for the complete, tested block in the
meantime.

## When you are *extending* (authoring a brand-new runbook)

Authoring a new runbook is the **Extend** capability — autonomous (it adds a
runbook, not the doctor's own machinery), subject to this contract. Repairing an
*existing* runbook, or anything deeper, is gated: see `repair-guardrails.md`.
