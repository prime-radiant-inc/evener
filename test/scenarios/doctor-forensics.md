# doctor-forensics: serf-doctor inspects settled state and the doctor agent diagnoses it

**What this covers**: the serf doctoring system end-to-end. (a) The
`serf-doctor` forensic tools — `locate`, `watches`, `transcript --count`,
`apilog`, `tree`, `jobs` — read settled on-disk state (transcript / private API log /
meta / jobs.jsonl) through
serf's own folds and types, and report the numbers a hand-parser got wrong:
distinct deliveries after coalescing collapse, the watch breaker verdict read
from the runtime's own stamps (`max_self_influence_depth`/`runaway_drops`,
`agent/doctor/watches.go:46-49` — never re-derived from the provenance
`Chain`), the state of the job each watch was watching, and the structural
tool-call count vs assistant-prose mentions. (b) The `doctor` agent type
loads the `doctoring-serf` skill, runs the tools, and emits structured Findings
— **healthy ⇒ zero findings**, a real defect ⇒ exactly one. The watch/provenance
mechanics under test are produced by `job-watch-actually-monty-python-injection.md`
and `job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh binaries from the branch under test: `make build build-doctor` →
  `./serf` and `./serf-doctor`. Put the worktree on `PATH` so the doctor agent's
  shell tool resolves `serf-doctor` (`PATH="$PWD:$PATH"`).
- A credentialed model (e.g. `--model kimi/kimi-for-coding`).
- An **observer-watch session** to inspect. Either reuse an existing one (run
  `job-watch-actually-monty-python-injection.md` first and capture its caller
  `SID`), or pick any session whose `jobs.jsonl` has `watch_registered` +
  `watch_send_delivered` events. Below, `$SID` is that caller session id and the
  default state dir holds it.

## Steps

### 1 — locate resolves the on-disk layout (the find-tax killer)

```
serf-doctor locate $SID
```

Assert the output names: `transcript:` = `<bucket>/sessions/$SID.transcript.jsonl`,
`api log:` = `<bucket>/sessions/$SID.api.jsonl`, `meta:` =
`<bucket>/sessions/$SID.meta.json`, and **`jobs:` =
`<bucket>/sessions/$SID/jobs.jsonl`** (the per-session SUBDIR form — NOT a flat
`$SID.jobs.jsonl`). `--json` emits `{transcript_ref, transcript_path, meta_path,
jobs_path, api_log_path, project_id}`.

### 2 — watches collapses coalescing and reads the breaker verdict

```
serf-doctor watches $SID
serf-doctor watches $SID --self-loops
serf-doctor watches $SID --json | jq '.watches[0] | {pending_lines, distinct_deliveries, delivered, dropped, evicted, max_self_influence_depth, runaway_drops}'
serf-doctor watches $SID --json | jq '.watches[] | {watch_id, target, target_job: (.target_job | if . == null then null else {status, reason, exit_code, output_bytes} end), target_job_missing}'
```

Assert, for the observer watch:
- `distinct_deliveries` < `pending_lines` with the `[latest-wins coalescing
  collapsed — expected]` note (e.g. `5 distinct … from 10 pending lines`). A bare
  `grep -c watch_send_pending` would have reported the inflated pending count.
- every delivery `delivered` (0 dropped, 0 evicted), each `trigger=caller/…` —
  caller-caused, not a feedback loop.
- `runaway_drops == 0`, and the human line reads `breaker: bounded
  self-influence, max depth N (no runaway)` or `breaker: no self-influence`
  (`agent/doctor/watches.go:263-267`). There is no `self_loop` field on the
  report at all: bounded self-influence is normal under the inform+breaker
  policy, so what the verdict turns on is whether the depth fuse fired, read
  from the runtime's stamps rather than re-derived from the provenance `Chain`.
- `serf-doctor watches $SID --self-loops` returns **no watches**, with the
  message `no watches where the runaway fuse fired (self-influence is
  bounded)` (`watches.go:315`) — distinct from `no watches recorded` (`:319`),
  which would mean the session never registered one and the fixture is wrong.
- a watch on a **job** carries that job's own state on the row: `target job:
  status=…  reason=…  exit=…  output_bytes=…  ended=…`, joined from the same
  `jobs.jsonl` fold (`agent/doctor/watches.go:206-221`), and `target_job` in
  the JSON. Pivot to §6 for the full job row. A watch whose target names a job
  this log never recorded reads `target job: not recorded in this session's
  jobs.jsonl` / `target_job_missing: true` instead of going quiet — the guess
  the flag exists to refuse. A watch on the session rather than a job (a target
  that is not a `job_` id) carries neither field, which is not a defect.

### 3 — transcript --count separates calls from mentions

```
serf-doctor transcript $SID --count communicate
serf-doctor transcript $SID --count delegate_send
```

Assert `communicate` reports the real structural call count and `delegate_send`
reports **`0 calls`**. If assistant prose mentions `delegate_send`, the doctor
reports that separately as text rather than treating it as an invocation.

### 4 — apilog preserves structured failure and settlement evidence without bodies

```
serf-doctor apilog $SID
serf-doctor apilog $SID --errors
serf-doctor apilog $SID --json | jq '.calls[] | {attempt_id, attempt_group_id, status_code, error_class, outcome, final_attempt_count}'
serf-doctor apilog $SID --json | jq '.settlements | {total, truncated, records: [.records[] | {attempt_group_id, final_attempt_id, final_attempt_count, outcome, forensic_incomplete}]}'
```

Assert the unfiltered human table has distinct `status`, `error_class`, and
`outcome` columns. The separate `--errors` invocation returns only failed call
rows while preserving the settlement collection. The JSON call projection
retains `attempt_id`, `attempt_group_id`, `status_code`, `error_class`, `outcome`,
and settlement-derived `final_attempt_count`; neither human nor JSON output
prints provider body-derived `error_message` text. The bounded
`settlements.records` collection remains present under call filters, exposes
`forensic_incomplete`, and can represent a `final_attempt_count: 0` settlement
without fabricating a provider call row.

### 5 — tree links parent ↔ delegate/observer across buckets

```
serf-doctor tree $SID --observers
```

Assert the caller session roots a tree with its delegate child (`delegate <SID>
(agent_type) status → proj:<project-id>:<sid>`) and, when the worker is observed, an
`observer <SID>` edge — children resolved by their transcript ref so a
cross-project child still links.

### 6 — jobs answers "what did this session run, and how did each end"

```
serf-doctor jobs $SID
serf-doctor jobs $SID --job <a-job-id-from-the-list>
serf-doctor jobs $SID --json | jq '.jobs[] | {job_id, type, status, reason, exit_code, output_bytes, terminal_notification_state}'
```

Assert every job the session ran appears in the log's own append order, each
leading with `job <id>  (<status>)` — or `(<status>: <reason>)` when a reason
produced it, e.g. `(stopped: run_timeout)` — over a
`type=  exit=  output_bytes=  notify=` line and a `started=  ended=` line. This
is settled-disk data: before `jobs` existed the same numbers were reachable
only from a live daemon's `/status`, so a session that had already exited could
not be asked at all. If §2's watch row named a `target job`, that job must
appear here with the same `status`/`reason`/`exit_code`/`output_bytes` — that
is the join, seen from the other side. `--job <id>` scopes to one job; an id
this session never ran
prints `job <id> not found in this session` and exits 0, NOT `no jobs
recorded`, which would wrongly say the session ran nothing
(`agent/doctor/jobs.go:209-214`).

### 7 — the doctor agent diagnoses a HEALTHY session (zero findings)

```
PATH="$PWD:$PATH" ./serf --model kimi/kimi-for-coding --agent doctor \
  --skills-dir "$PWD/docs/skills" --dir "$(mktemp -d)" --max-rounds 40 \
  "Diagnose serf session $SID using the serf-doctor tools. Run the
   watch-delivery-health and observer-self-loop checks; report distinct
   deliveries, the breaker verdict, the communicate/delegate_send counts, and
   any Findings (healthy ⇒ zero)."
```

Assert the run log shows: `agent:doctor` loaded, `[skill] activated
doctoring-serf`, `shell` calls to `serf-doctor watches/transcript`, and a final
`communicate` reporting the session is **healthy with zero findings**, citing the
distinct-delivery count and zero runaway drops. The doctor must NOT emit a
finding for the expected coalescing, nor for bounded self-influence depth —
both are visibility, not violations.

### 8 — the doctor agent emits ONE finding on a BROKEN session

Synthesize a **fired breaker** in a scratch state dir — a `watch_send_dropped`
event carrying `"diagnostic_reason":"runaway"` and a deep
`self_influence_depth`, alongside the `watch_registered` and
`watch_send_delivered` events for the same `watch_id`
(`doctor-agent-diagnose.md`'s Part B has a ready-made three-line `jobs.jsonl`
fixture). A delivered send whose provenance `Chain` merely repeats the same
`watch_id` is **not** enough: bounded self-influence is expected under the
inform+breaker policy and `--self-loops` will not list it. Then:

```
serf-doctor watches $BROKEN_SID --state-dir "$SCRATCH" --self-loops   # flags the fired fuse
PATH="$PWD:$PATH" ./serf --model kimi/kimi-for-coding --agent doctor \
  --skills-dir "$PWD/docs/skills" --dir "$(mktemp -d)" --max-rounds 40 \
  "Diagnose session $BROKEN_SID with serf-doctor (use --state-dir $SCRATCH).
   Report whether it is healthy and emit a Finding for any confirmed problem."
```

Assert `serf-doctor watches … --self-loops` lists the watch with
`breaker: FIRED — 1 runaway drop(s) (depth fuse); max self-influence depth N`
(`agent/doctor/watches.go:263`), and the doctor emits exactly one structured
Finding with `category: watch_runaway`, `severity: high`,
`signature: watch_runaway:<sessionID>:<watchID>`, `evidence.deliveryIds`
naming the dropped send, a `doctorCommand` in `evidence`, and
`suggestedFix.type: diagnosis` — the vocabulary the doctoring-serf runbook
prescribes (`internal/bundled/skills/doctoring-serf/runbooks/observer-self-loop.md:34-45`).
There is no `watch_self_loop` category.

## Out of scope

`serf-doctor` is read-only forensics over settled state — not a live monitor
(that is appwire + tui + hub), not a mutator (mutation is the hub REST surface),
and not a test harness (it exposes numbers; scenarios assert them with `jq`).
The doctor's repair authority is graduated (`repair-guardrails.md`): diagnosis is
autonomous; core-skill / Go-tool repair is propose-only behind review + a gate.
