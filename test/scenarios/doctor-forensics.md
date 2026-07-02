# doctor-forensics: serf-doctor inspects settled state and the doctor agent diagnoses it

**What this covers**: the serf doctoring system end-to-end. (a) The
`serf-doctor` forensic tools — `locate`, `watches`, `transcript --count`,
`tree` — read settled on-disk state (transcript / meta / jobs.jsonl) through
serf's own folds and types, and report the numbers a hand-parser got wrong:
distinct deliveries after coalescing collapse, the watch self-loop verdict from
the provenance `Chain` (not the always-present `WatchKeys` stamp), and the
structural tool-call count vs textual mentions. (b) The `doctor` agent type
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
`meta:` = `<bucket>/sessions/$SID.meta.json`, and **`jobs:` =
`<bucket>/sessions/$SID/jobs.jsonl`** (the per-session SUBDIR form — NOT a flat
`$SID.jobs.jsonl`). `--json` emits `{transcript_ref, transcript_path, meta_path,
jobs_path, bucket_hash}`.

### 2 — watches collapses coalescing and reads the self-loop verdict

```
serf-doctor watches $SID
serf-doctor watches $SID --self-loops
serf-doctor watches $SID --json | jq '.watches[0] | {pending_lines, distinct_deliveries, delivered, dropped, evicted, max_self_influence_depth, runaway_drops}'
```

Assert, for the observer watch:
- `distinct_deliveries` < `pending_lines` with the `[latest-wins coalescing
  collapsed — expected]` note (e.g. `5 distinct … from 10 pending lines`). A bare
  `grep -c watch_send_pending` would have reported the inflated pending count.
- every delivery `delivered` (0 dropped, 0 evicted), each `trigger=caller/…` —
  caller-caused, not a feedback loop.
- `self_loop.detected == false`. `serf-doctor watches $SID --self-loops` prints
  **no watches** (empty list) — the verdict comes from the absence of a
  same-`watch_id` prior `Chain` hop, and the always-present `WatchKeys` stamp is
  explicitly NOT treated as the signal.

### 3 — transcript --count separates calls from mentions

```
serf-doctor transcript $SID --count communicate
serf-doctor transcript $SID --count delegate_send
```

Assert `communicate` reports the real structural call count, and `delegate_send`
reports **`0 calls`** with a non-zero "textual mention(s) in api_call payloads"
count — the exact disambiguation `grep -c delegate_send` got wrong (it counted a
tool list + an instruction as calls).

### 4 — tree links parent ↔ delegate/observer across buckets

```
serf-doctor tree $SID --observers
```

Assert the caller session roots a tree with its delegate child (`delegate <SID>
(agent_type) status → proj:<hash>:<sid>`) and, when the worker is observed, an
`observer <SID>` edge — children resolved by their transcript ref so a
cross-bucket child still links.

### 5 — the doctor agent diagnoses a HEALTHY session (zero findings)

```
PATH="$PWD:$PATH" ./serf --model kimi/kimi-for-coding --agent doctor \
  --skills-dir "$PWD/docs/skills" --dir "$(mktemp -d)" --max-rounds 40 \
  "Diagnose serf session $SID using the serf-doctor tools. Run the
   watch-delivery-health and observer-self-loop checks; report distinct
   deliveries, the self-loop verdict, the communicate/delegate_send counts, and
   any Findings (healthy ⇒ zero)."
```

Assert the run log shows: `agent:doctor` loaded, `[skill] activated
doctoring-serf`, `shell` calls to `serf-doctor watches/transcript`, and a final
`communicate` reporting the session is **healthy with zero findings**, citing the
distinct-delivery count and `self_loop: none`. The doctor must NOT emit a
finding for the expected coalescing (that is visibility, not a violation).

### 6 — the doctor agent emits ONE finding on a BROKEN session

Synthesize a self-loop in a scratch state dir (a `watch_send_delivered` whose
provenance `Chain` carries a prior hop of the same `watch_id` with a different
`delivery_id`), then:

```
serf-doctor watches $BROKEN_SID --state-dir "$SCRATCH" --self-loops   # flags the loop
PATH="$PWD:$PATH" ./serf --model kimi/kimi-for-coding --agent doctor \
  --skills-dir "$PWD/docs/skills" --dir "$(mktemp -d)" --max-rounds 40 \
  "Diagnose session $BROKEN_SID with serf-doctor (use --state-dir $SCRATCH).
   Report whether it is healthy and emit a Finding for any confirmed problem."
```

Assert `serf-doctor watches … --self-loops` lists the watch with
`SELF-LOOP: 1 deliver(ies) …`, and the doctor emits exactly one structured
Finding with `category: watch_self_loop`, `severity: high`, a stable
`signature` keyed to the session+watch, a `doctorCommand` in `evidence`, and
`suggestedFix.type: diagnosis`.

## Out of scope

`serf-doctor` is read-only forensics over settled state — not a live monitor
(that is appwire + tui + hub), not a mutator (mutation is the hub REST surface),
and not a test harness (it exposes numbers; scenarios assert them with `jq`).
The doctor's repair authority is graduated (`repair-guardrails.md`): diagnosis is
autonomous; core-skill / Go-tool repair is propose-only behind review + a gate.
