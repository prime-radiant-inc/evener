# The Finding contract

A Finding is the atomic output of a doctor audit — structured, emitted as JSON.
It adapts meta-doctor's finding contract and serf's own diagnostic DTO
(`subagent-management/10-runtime-contracts.md` Contract 3) to forensic
diagnosis. Read this before you emit.

**The one rule above all:** a healthy run emits **zero** findings. A finding is
emitted only for a real, confirmed, actionable problem. No PASS findings, no
informational metrics, no "looks fine" notes. When in doubt, do not emit — a
missed note costs nothing; a spurious finding makes a human triage noise.

---

## Schema

| Field | Type | Req | Notes |
|---|---|---|---|
| `signature` | string | yes | **stable dedup key**, deterministic for a given root cause across runs (formats below). |
| `severity` | `low` \| `medium` \| `high` | yes | impact level. |
| `category` | enum | yes | classification (below) — consumers key on this, never on parsing `description`. |
| `title` | string | yes | one-line label. |
| `description` | string | yes | what was observed + why it is a problem. |
| `evidence` | object | yes | ≥1 sub-field populated. Always include `doctorCommand`. |
| `suggestedFix` | object | yes | the **routing** directive (below). |

`evidence` sub-fields: `sessionRefs[]`, `watchIds[]`, `deliveryIds[]`,
`transcriptTurns[]`, `doctorCommand` (the exact `serf-doctor <cmd> …` that
surfaces it — so a human can reproduce), `logSnippets[]`. Redact secrets from
evidence.

### Category

Adopt the diagnostic categories from Contract 3 where they apply — `validation`,
`policy_denied`, `unavailable`, `timeout`, `cancellation`, `provider_error`,
`hook_blocked`, `hook_failed`, `transcript_unavailable` — plus the forensic
shapes this skill adds: `watch_runaway`, `dropped_delivery`, `provenance_gap`,
`stuck_processing`, `orphaned_runtime`. The category routes and groups.

### Signature formats

Stable and deterministic so dedup can suppress repeats:

- structural defect: `{shape}:{sessionID}:{watchID|turn}` — e.g.
  `watch_runaway:01J…:w_42`.
- recurring audit finding: `{runbook}:{category}:{bucket}` where bucket is an ISO
  week/date/month — e.g. `watch-delivery-health:dropped_delivery:2026-W25`.

When unsure, **bucket broader** — a false dedup is cheaper than a duplicate
finding. If two signatures would point a human at the same fix, use one.

### suggestedFix routing

This is where meta-doctor's PR/ticket/heal-script triage collapses into serf's
three capabilities:

| `type` | Means | Capability |
|---|---|---|
| `diagnosis` | report-only — the durable record explains it; no change | (none — just report) |
| `runbook` | propose/extend a runbook to catch this class henceforth | Extend |
| `skill` | propose a core-skill / persona / doctor-tool repair | Heal (gated — see `repair-guardrails.md`) |

Optional: `fileHint`, `symbolHint`.

**Accepted scope limit (not a clean equivalence).** meta-doctor has a fourth
route the collapse drops: "repair the *target system* under audit." serf's doctor
has no such route — a finding that is a genuine bug in **serf itself** can only
route to `diagnosis` (report-only), because the doctor's Heal authority covers
its *own* machinery (runbooks / its Go tools / core skills), not serf's product
code. That is an honest limit of an on-demand forensic doctor.

---

## JSON example

```json
{
  "signature": "watch_runaway:01JABCWORKER:w_42",
  "severity": "high",
  "category": "watch_runaway",
  "title": "Watch w_42 self-influence went unbounded — runaway fuse fired",
  "description": "Watch w_42 reached max_self_influence_depth 8 and the machinery cut it: 3 sends were dropped with diagnostic_reason \"runaway\" (runaway_drops=3). Self-influence is normal, but this watch kept reacting to its own influence without backing off until the depth fuse had to terminate the loop — a sidecar/watch-topology problem to investigate.",
  "evidence": {
    "sessionRefs": ["proj:6f…:01JABCWORKER"],
    "watchIds": ["w_42"],
    "deliveryIds": ["dl_9"],
    "doctorCommand": "serf-doctor watches proj:6f…:01JABCWORKER --self-loops"
  },
  "suggestedFix": { "type": "diagnosis" }
}
```

---

## Dedup & structured-fields discipline

- **Dedup** by `signature` (stem-matched), applied per `suggestedFix.type`.
  Within a single run, the first occurrence of a signature stem wins.
- **Structured fields over string-matching**: consumers key on `category` /
  `signature`, never on parsing `description`. Keep machine-meaningful facts in
  fields, not prose.
- **Secret redaction**: never put credentials/tokens in `evidence` or
  `description`.

## When NOT to emit

- A watch reacting to its own influence at a **bounded** depth (the fuse never
  fired, `runaway_drops == 0`) — self-influence is normal under inform+breaker;
  that is HEALTHY, emit nothing.
- Informational / visibility metrics the runbook itself labels non-violations
  (e.g. expected coalescing: "8 pending lines → 4 deliveries").
- Clean scorecards, healthy funnels, expected baselines.

Absence of a problem is not a finding.
