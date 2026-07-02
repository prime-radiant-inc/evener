# Failure modes — symptom → what it is → confirm with → mechanics

A recognize/diagnose table. Each row: the **symptom** you notice, **what it
actually is**, the exact **`serf-doctor`** invocation (and durable record) that
confirms it, and the **mechanics** behind it. The doctor persona's "known
gotchas" are seeded from this. Cite Go **symbols**, never `file:line`.

---

## Watch / delivery shapes (the ones that produced wrong numbers)

### Watch runaway (fuse fired)
- **Symptom:** an observer/watch keeps reacting to its own influence and climbs
  instead of disengaging; a delivery count that won't settle until it stops cold.
- **What it is:** self-influence that went **unbounded** — the watch kept
  delivering on its own causal descendants until the depth fuse had to cut it.
  (Self-influence itself is **normal**: watches always deliver, and a
  self-influenced delivery is informed by a depth-gradient line, not dropped. The
  failure is only the *unbounded* case the fuse had to terminate.)
- **Confirm:** `serf-doctor watches <sel> --self-loops` — it surfaces only watches
  whose fuse fired. A watch with `runaway_drops > 0` is confirmed; its
  `max_self_influence_depth` reached the fuse. A merely self-influenced but
  **bounded** watch (`runaway_drops == 0`) is healthy and is **not** listed.
- **Mechanics:** the runtime computes a coalescing-aware self-influence depth
  (distinct *delivered* prior deliveries of this watch in the lineage; a
  coalesced-away pending that never independently delivered does not count),
  stamps it as `WatchSendState.SelfInfluenceDepth`, and at depth **8**
  (`runawaySelfInfluenceDepth`) drops the send with `DiagnosticReason = "runaway"`.
  The volume breaker (`watchDeliveryBudget = 50`) is the floor behind it. The
  doctor **reads** the stamped depth and the `runaway` drop reason — it does not
  re-derive a loop from the `Chain`.

### Pending lines counted as deliveries (overcount)
- **Symptom:** `grep -c watch_send_pending jobs.jsonl` says N; the real delivery
  count is lower.
- **What it is:** pending frames **coalesce latest-wins** into one delivery.
- **Confirm:** `serf-doctor watches <sel>` — the `distinct deliveries (… ) from N
  pending lines` line, with `[latest-wins coalescing collapsed — expected]` when
  they differ.
- **Mechanics:** `jobstore.FoldWatchSends` coalesces by `WatchSendKey` +
  `UpdateSeq` (latest wins, terminals evict pending). Counting `watch_send_pending`
  events counts *frames*, not deliveries.

### Dropped vs evicted vs delivered
- **Symptom:** a "missing" delivery, or unsure why a frame never arrived.
- **What it is:** a settled frame has one of **four** terminal states — pending is
  not terminal; the three terminals are delivered, dropped, **evicted**. A drop is
  a real non-delivery (carries a `DiagnosticReason`); an eviction is a coalesced-out
  pending frame.
- **Confirm:** `serf-doctor watches <sel>` shows each settled delivery's terminal
  + `reason=`; the per-watch line breaks out `(X delivered, Y dropped, Z evicted)`.
- **Mechanics:** `jobstore.EventWatchSendDelivered` / `…Dropped` / `…Evicted`.
  `FoldWatchSends` returns only **pending** state (discards terminal payloads), so
  `serf-doctor watches` reads terminals from a raw event scan
  (`Event.WatchSend *WatchSendState`), deduped by `DeliveryID`.

---

## Transcript shapes

### Textual mention counted as a call
- **Symptom:** "the agent called `delegate_send` 5 times" — but it never did.
- **What it is:** the tool **name appears as text** (a tool list in an `api_call`
  request, or a "do not call delegate_send" instruction in assistant prose) — not
  a structural invocation.
- **Confirm:** `serf-doctor transcript <sel> --count delegate_send` →
  `delegate_send: 0 calls (5 textual mention(s) in api_call payloads, 1 in
  assistant text — not invocations)`.
- **Mechanics:** a real call is a content part with `Kind == llm.ContentToolCall`
  and `ToolCall.Name == <tool>` (`writeAssistantContent`'s predicate). Substring
  hits in `api_call` lines / `llm.ContentText` are mentions, tracked separately.

### Guessed-the-shape zeros
- **Symptom:** a hand parser reports `0 communicate calls` / `0 steering entries`
  on a session that clearly has them.
- **What it is:** the parser guessed the JSONL shape (looked for top-level keys; a
  tool call is nested at `entry.Turn.Message.Content[].ToolCall.Name`; a steering
  turn is `schema.TurnSteering`).
- **Confirm:** `serf-doctor transcript <sel> --format outline` (turn map) and
  `--count <tool>`. The result tool resolves via `effectiveResultToolName`
  (`meta.Config.ResultToolName` else `communicate`).
- **Mechanics:** `transcript.Entry{Kind, Seq, Turn}` wraps a `schema.Turn`; the
  turn kinds are `USER_INPUT`/`STEERING`/`ASSISTANT`/`TOOL_RESULTS` (current,
  `TOOL` is deprecated)/`SUMMARY`. Read via the tool; never re-parse.

---

## Linkage shapes

### "Where did the sub-session go" / cross-bucket confusion
- **Symptom:** a parent, its delegate, and its observer can't be found in one
  place; `find` across one bucket misses them.
- **What it is:** parent / delegate / observer sessions live in **different
  project-hash buckets** (origin/cwd differ).
- **Confirm:** `serf-doctor tree <sel> --observers` links them — delegate edges
  from `jobstore.FoldDelegates` (each child resolved by its transcript ref, so a
  cross-bucket child links), observer edges from `schema.SessionMeta.ObservedBy`.
- **Mechanics:** bucket = `hexHash(originURL else workDir)`. A child's
  `DelegateRecord.TranscriptRef` carries `proj:<hash>:<sid>`, which the tree
  resolves directly.

---

## Mechanics shapes (architecture.md / job-control.md)

These are real failure modes; map each to the durable record that confirms it,
and be honest where `serf-doctor` cannot yet confirm one directly.

| Symptom | What it is | Confirm with |
|---|---|---|
| A coordinator stops reacting ("deaf coordinator"); turns stall | the agent loop is wedged or a watch-outbox drain stalled (`docs/architecture.md` level-by-level coordinator / single-hop forwarding) | `serf-doctor transcript <sel> --format outline` (last turns) + `serf-doctor watches <sel>` (undrained pending); root-cause is partly live-only — say so |
| A job shows `runtime_lost` / `supervision_lost` | the runtime supervising the job vanished (`docs/job-control.md` status×reason) | the `JobRecord` status/reason via the jobs log (a future `serf-doctor jobs` view; today, `serf-doctor locate` + read the record) |
| A hook blocked or failed a tool | `hook_blocked` / `hook_failed` (`docs/hooks.md`) | not yet a `serf-doctor` view — read the transcript turn; emit category `hook_blocked`/`hook_failed` |
| A provider error stalled a call | `provider_error` (`docs/llm-providers.md`) | the `api_call` line's `error`/`source` (`transcript.APICall`); `serf-doctor transcript` surfaces the turn, not yet the api_call error detail — say so |

Where a shape has no first-class `serf-doctor` confirm path yet, that gap is a
candidate for the next subcommand or runbook — note it in the Finding rather than
pretending the tool covers it.
