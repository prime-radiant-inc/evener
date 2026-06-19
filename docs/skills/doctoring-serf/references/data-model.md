# Data model — what is on disk, and the Go type that owns it

This is the conceptual reference the HARD GATE points at. Read it before reading
any artifact. It is the map; the cited Go types are the territory (the
`serf-doctor` tools `import` them, so they cannot drift). Citations are to Go
**symbols**, never `file:line`.

Every section ends with **read it via:** the `serf-doctor` command that exposes
it — you should rarely open these files by hand.

---

## The big picture

A serf session's durable state lives under a per-project **bucket** directory.
Three artifacts per session:

- the **transcript** — the append-only conversation log (turns + api calls).
- the **meta** — session metadata (model, config, lineage, observers).
- **jobs.jsonl** — an append-only *event log* for jobs, watches, delegates, and
  grants; the records you reason about are *folded* from it.

"Durable" state (these files) is distinct from the **live** event stream
(`events.SessionEvent` over appwire → tui/hub). serf-doctor reads settled
durable state only; it is not a live monitor.

---

## State-dir layout

The base resolves with serf's precedence: `--state-dir` flag › `SERF_STATE_DIR`
env › `$XDG_STATE_HOME` › `~/.local/state` (there is **no** `SERF_STATE_HOME` —
it was never read). Under an XDG home the layout is:

```
<stateHome>/serf/projects/<bucket>/sessions/
    <SID>.transcript.jsonl      ← transcript   (flat, SID-prefixed)
    <SID>.meta.json             ← meta         (flat, SID-prefixed)
    <SID>/jobs.jsonl            ← jobs         (per-session SUBDIR)
```

- The **bucket** is `hexHash(key)` where `key` is the git origin URL, or the
  working directory when there is no origin (one OR the other, not concatenated);
  `hexHash` = `hex(sha256(key)[:8])` = **16 hex chars** (`RuntimeDir` / `hexHash`).
- **transcript and meta are flat files** named `<SID>.transcript.jsonl` /
  `<SID>.meta.json` directly under `sessions/` (`transcriptPath`).
- **jobs.jsonl is in a per-session SUBDIR**: `<bucket>/sessions/<SID>/jobs.jsonl`
  (`jobsDir` → `filepath.Join(dir, "jobs.jsonl")`). It is **not** a flat
  `<SID>.jobs.jsonl` beside the transcript — a recurring mistake.
- When `SERF_STATE_DIR` / `--state-dir` is set, that path **is** the bucket
  (sessions sit directly under it — no `serf/projects/<hash>` layer). This is the
  E2E / scratch-root shape.

Parent, observer, and delegate sub-sessions are different SIDs and frequently
live in **different buckets**. Don't assume one bucket.

**Read it via:** `serf-doctor locate <selector>` (resolves all three paths +
bucket hash; never recompute the hash by hand — resolve by glob).

---

## The transcript (`transcript.Entry` + `schema.Turn`)

JSONL, one record per line, discriminated by `"kind"`:

- `"header"` (`transcript.Header`) — first line: `SessionID`, `ParentSessionID`,
  `ParentToolCallID`, `Model`, `Depth`, etc.
- `"entry"` (`transcript.Entry{Kind, Seq, Turn}`) — one conversation turn.
- `"api_call"` (`transcript.APICall`) — one LLM round (the request payload +
  latency). This is **in the transcript**; it is NOT the separate per-call
  latency log at `<state-dir>/api.jsonl` (`llm.APILogger`).

A turn is `schema.Turn{Kind, Message, Timestamp, Usage}`. `Turn.Kind`
(`schema.TurnKind`):

- `USER_INPUT` (`TurnUserInput`) — note: not `USER`.
- `STEERING` — mid-turn user input.
- `ASSISTANT`, `TOOL_RESULTS` (`TurnToolResults`, the current aggregated-results
  kind), `SYSTEM`, `CHECKPOINT`, `SUMMARY`.
- `TOOL` (`TurnTool`) is **deprecated** — do not key on it for new logic.

A **tool call** is a content part: `Turn.Message.Content[]` is `[]llm.ContentPart`;
a call is a part with `Kind == llm.ContentToolCall` and the name at
`ToolCall.Name` (`llm.ToolCallData.Name`). Tool **results** pair to calls by
`ToolResult.ToolCallID`, **never by adjacency**.

The session's **result tool** (how the agent "speaks" its answer) follows
`effectiveResultToolName`: `meta.Config.ResultToolName` if set, else
`"communicate"`. A `communicate` call is the result-tool call, not an ordinary
tool — resolve the effective name from meta rather than hard-coding it.

**Read it via:** `serf-doctor transcript <selector>` (turn map / conversation
render); `serf-doctor transcript <selector> --count <tool>` for the **structural**
invocation count — distinct from textual mentions in api_call payloads or
assistant prose (the `delegate_send`: 0 calls / 5 mentions disambiguation).

---

## jobs.jsonl — a folded event log (`jobstore`)

Each line is a `jobstore.Event` (a flat union; the present fields apply to the
record). You never read raw events for answers — you read the **folds**:

| Fold | Produces | Reads events |
|---|---|---|
| `Fold` / `FoldOrdered` | `JobRecord`s | job_started/session_assigned/finished/… |
| `FoldWatches` | `WatchRecord`s (the watch registry) | `watch_registered` / `watch_cleared` |
| `FoldWatchSends` | **pending** `WatchSendState`s only | `watch_send_*` |
| `FoldDelegates` | `DelegateRecord`s | `delegate_created` / job_started / … |
| `FoldGrants` | observer→watched-job grants | `watch_read_grant` |

### Watches and the four watch-send terminals

A watch fires and emits a `watch_send_pending` frame keyed by `WatchSendKey`
(`{VisibleSessionID, WatchID, WatchTarget, ResolvedWatchedIdentity,
ResolvedSendTo, WatchGeneration}`). Updates to the same key **coalesce
latest-wins** by `UpdateSeq` — so the count of `watch_send_pending` lines
**overcounts deliveries**. Each slot settles to one of **four terminals**:

- `watch_send_delivered` — delivered.
- `watch_send_dropped` — dropped (carries a `DiagnosticReason`).
- `watch_send_evicted` — `EventWatchSendEvicted`, a **real fourth terminal**.
- (`watch_send_pending` is not terminal.)

A distinct delivery = a settled terminal (deduped by `DeliveryID`), **not** a
pending line. Crucial subtlety: `FoldWatchSends` returns only the still-**pending**
frames (`WatchSendRecord.Pending`) and discards terminal payloads, so the settled
deliveries (terminal kind + `DiagnosticReason` + provenance) are read by a **raw
scan** of the `watch_send_delivered/dropped/evicted` events. The doctor tool does
this for you.

**Read it via:** `serf-doctor watches <selector>` (distinct deliveries vs pending
lines, per-delivery terminal + reason + provenance, self-loop verdict).

### Delegates and grants → the session tree

`DelegateRecord` carries `ChildSessionID`, `AgentType`, `Status`, and a
`TranscriptRef` that may point into another bucket. Observer links are stamped on
the **worker's** `SessionMeta.ObservedBy[]` and mirrored by `FoldGrants`.

**Read it via:** `serf-doctor tree <selector> [--observers]`.

---

## Provenance (`provenance.Causal`) — and the self-loop trap

Watch-send deliveries and some job events carry causal provenance:

```
provenance.Causal{
    WatchKeys      []WatchKey   // deduped SET of (WatchID, WatchGeneration) — LOAD-BEARING
    Chain          []Entry      // ordered diagnostic trail — TRUNCATABLE
    ChainTruncated bool
}
```

- `WatchKeys` is the **runtime suppression** set: `shouldSuppressWatch` →
  `ContainsWatch(p, watchID, generation)` is applied to an *incoming* event's
  provenance **pre-stamp**; if the triggering event already carries the key, the
  delivery is suppressed and **never recorded**. So a healthy watch records
  **zero** self-loop deliveries by construction.
- Therefore `ContainsWatch` / `WatchKeys` is **useless as a post-hoc verdict**: a
  *recorded* delivery is stamped with its own watch's key at delivery time
  (`watchSendSnapshot` → `WithWatch`), so `ContainsWatch` over it is **vacuously
  true for every delivery**.
- The forensic self-loop verdict is the **`Chain`**: a delivery whose `Chain`
  contains a **prior hop of the same `watch_id`** (a hop with a *different*
  `delivery_id` than its own stamp) was caused, transitively, by an earlier
  delivery of this watch — a loop.
- Caveat: `Chain` is truncatable (`maxDiagnosticChain = 16`, sets
  `ChainTruncated`), so a deep loop can lose middle hops — a positive verdict is
  a real signal, its absence is not a completeness guarantee.
- Nuance: the Chain-hop check keys on `watch_id` alone while suppression keys on
  `watch_id`+`watch_generation`, so a re-arm / generation bump escapes suppression
  but is exactly the loop the `Chain` still catches.

There is **no** durable "count of legitimate external triggers" to compare
against — the triggering `events.SessionEvent`s are ephemeral and never
persisted — so a "deliveries ≤ external triggers" invariant is uncomputable from
durable state. The Chain is the signal.

**Read it via:** `serf-doctor watches <selector> --self-loops`.

---

## Meta (`schema.SessionMeta`)

`schema.LoadSessionMeta(bucketDir, sid)` / `schema.ListSessionMetas(bucketDir)`.
Key fields: `ID`, `Model`, `Config` (incl. `ResultToolName`), `ParentSessionID`
(fork lineage), `IsSubagent`, and `ObservedBy[]` (the observer sessions watching
this worker — the durable basis for the session tree's observer edges).

**Read it via:** `serf-doctor locate` (path), `serf-doctor tree --observers`
(observer edges), `serf-doctor transcript` (result-tool resolution).
