# Data model — what is on disk, and the Go type that owns it

This is the conceptual reference the HARD GATE points at. Read it before reading
any artifact. It is the map; the cited Go types are the territory (the
the `doctor_evener` data plane `import`s them, so they cannot drift). Citations are to Go
**symbols**, never `file:line`.

Every section ends with **read it via:** the `doctor_evener` command (the
in-process `evener doctor` data plane) that exposes it — you should rarely
open these files by hand.

---

## The big picture

A evener session's durable state lives under a per-project **bucket** directory.
Up to six artifacts per root/session tree:

- the **transcript** — the append-only semantic conversation/lifecycle log.
- the **API log** — exact provider attempts and outer-call settlements.
- the **meta** — session metadata (model, config, lineage, observers).
- **jobs.jsonl** — an append-only event log for shell jobs and watches; the
  records you reason about are folded from it. It is never delegate lifecycle
  authority.
- **delegates.jsonl** — the root-owned append-only stable delegate-tree journal.
  Its `delegatestore` fold is the only durable delegate lifecycle authority.
- the **client-mutation store** — the journal of every client mutation the
  daemon accepted AND every one it rejected, plus the durable input queue.

"Durable" state (these files) is distinct from the **live** event stream
(`events.SessionEvent` over appwire → tui/hub). doctor_evener reads settled
durable state only; it is not a live monitor.

---

## State-dir layout

The base resolves with evener's precedence. For `doctor_evener` (the primary
path): an explicit `state_dir` argument › the session's own state root › the
doctor default chain `EVENER_STATE_DIR` env › `$XDG_STATE_HOME` ›
`~/.local/state`. For the CLI: `--state-dir` flag › `EVENER_STATE_DIR` env ›
`$XDG_STATE_HOME` › `~/.local/state` (there is **no** `EVENER_STATE_HOME` —
it was never read). Under an XDG home the layout is:

```
<stateHome>/evener/projects/<bucket>/sessions/
    <SID>.transcript.jsonl      ← transcript   (flat, SID-prefixed)
    <SID>.api.jsonl             ← API log      (flat, SID-prefixed, private)
    <SID>.meta.json             ← meta         (flat, SID-prefixed)
    <SID>/jobs.jsonl            ← jobs         (per-session SUBDIR)
    <ROOT-SID>/delegates.jsonl  ← stable delegate tree (root session SUBDIR)
<stateHome>/evener/projects/<bucket>/mutations/
    <SID>.json                  ← client mutations (SIBLING of sessions/)
```

- The **bucket** is `hexHash(key)` where `key` is the git origin URL, or the
  working directory when there is no origin (one OR the other, not concatenated);
  `hexHash` = `hex(sha256(key)[:8])` = **16 hex chars** (`RuntimeDir` / `hexHash`).
- **transcript, API log, and meta are flat files** named
  `<SID>.transcript.jsonl` / `<SID>.api.jsonl` / `<SID>.meta.json` directly
  under `sessions/` (`transcriptPath`, `APILogPath`).
- **jobs.jsonl is in a per-session SUBDIR**: `<bucket>/sessions/<SID>/jobs.jsonl`
  (`jobsDir` → `filepath.Join(dir, "jobs.jsonl")`). It is **not** a flat
  `<SID>.jobs.jsonl` beside the transcript — a recurring mistake.
- **delegates.jsonl is in the root session's SUBDIR**:
  `<bucket>/sessions/<ROOT-SID>/delegates.jsonl`. Descendant sessions do not own
  competing delegate journals; their stable rows fold from this root journal.
- **the client-mutation store is a third shape again**: a flat `<SID>.json` in a
  bucket-level `mutations/` dir that is a **sibling** of `sessions/`, not a file
  under it (`clientMutationFilePath`).
- When `EVENER_STATE_DIR` / the tool's `state_dir` argument is set, that path
  **is** the bucket
  (sessions sit directly under it — no `evener/projects/<hash>` layer). This is the
  E2E / scratch-root shape.

Parent, observer, and delegate sub-sessions are different SIDs and frequently
live in **different buckets**. Don't assume one bucket.

**Read it via:** `doctor_evener` `locate <selector>` (resolves all six paths +
bucket hash; never recompute the hash by hand — resolve by glob).

---

## The transcript (`transcript.Entry` + `schema.Turn`)

JSONL, one record per line, discriminated by `"kind"`:

- `"header"` (`transcript.Header`) — first line: `SessionID`, `ParentSessionID`,
  `ParentToolCallID`, `Model`, `Depth`, etc.
- `"entry"` (`transcript.Entry{Kind, Seq, Turn}`) — one conversation turn.

No other record kind is accepted. Provider request and response records are not
part of the transcript.

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

**Read it via:** `doctor_evener` `transcript <selector>` (turn map / conversation
render); `transcript <selector>` with `count: <tool>` for the **structural**
invocation count — distinct from textual mentions in assistant prose.

---

## The API log (`apilog.APILogRecord`)

`<SID>.api.jsonl` is the canonical private provider-forensics stream. Each
completed transport attempt is appended immediately as an
`apilog.APIAttemptRecord` (`kind: "api_attempt"`) with exact non-credential
headers, request body, raw response body, endpoint, timing, identity, and
outcome. Once the outer logical model call settles, an
`apilog.APIAttemptGroupSettlement` (`kind: "attempt_group_settlement"`) records
the final attempt and count. A clean EOF after an attempt without its settlement
is an explicitly unsettled group; a partial tail has unknown finality.

**Read it via:** `doctor_evener` `apilog <selector>` for attempt metadata and
aggregates. The model-facing `read_transcript` tool does not accept API-log
selectors or expose request/response bodies. Credential values are excluded.

---

## jobs.jsonl — a folded event log (`jobstore`)

Each line is a `jobstore.Event` (a flat union; the present fields apply to the
record). You never read raw events for answers — you read the **folds**:

| Fold | Produces | Reads events |
|---|---|---|
| `Fold` / `FoldOrdered` | shell `JobRecord`s | job_started/output/finished/notification… |
| `FoldWatches` | `WatchRecord`s (the watch registry) | `watch_registered` / `watch_cleared` |
| `FoldWatchSends` | **pending** `WatchSendState`s only | `watch_send_*` |

### Jobs

A `JobRecord` is the folded state of one job: `Status` (running / completed /
failed / cancelled / stopped / exhausted), the `Reason` that produced it (e.g.
`run_timeout`), `ExitCode`, `OutputBytes`, `StartedAt` / `EndedAt`, the
`NotifyState` (`terminal_notification_state` on disk — a terminal job still
`pending` never told its caller; `delivered` was rendered into the caller's own
notification turn, and `consumed` means the caller read the terminal
`job_status` itself, settling the notification without a turn), and the links to
pivot on (`TranscriptRef`, shell-to-shell `ParentJobID`, and typed
`ParentDelegateID` for a delegate-owned shell). A JobRecord never represents a
delegate. Note what the durable log does **not** carry: `Background` and `Phase`
live only on the shell runtime's in-memory record, so no folded record can say
whether a shell job ran in the background.

**Read it via:** `doctor_evener` `jobs <selector>` (every job in durable append
order), or `jobs <selector>` with `job_id` for one job's state.

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

A watch has a typed public source: `self`, a granted `parent`, a concrete shell
`job_...`, or a stable delegate `dlg_...`. The watch journal remains the sole
registration/pending/delivery authority. Its stable source/receiver bindings are
derived from the delegate controller and do not create a second delegate
lifecycle fold. For a shell source, the watch row is only half the story: join
the target shell's folded state before diagnosing a missing match.

**Read it via:** `doctor_evener` `watches <selector>` (distinct deliveries vs pending
lines, per-delivery terminal + reason + provenance, self-influence/breaker
telemetry, and the joined `target job:` state — the `target_job` /
`target_job_missing` fields).

### delegates.jsonl → the stable session tree

`delegatestore.Fold` produces one `Aggregate` per `dlg_...`, rooted in the
selected root session. Its immutable `Descriptor` carries child session and
transcript identity, parent delegate, task/agent/model configuration,
`ParentWatchGranted`, allowance, sandbox/worktree, and resumability. Run events
carry private generations, phases, terminal packets/outcomes, subtree-stop
state, and delivery acknowledgements; they never mint an activation `job_...`.
Observer links remain stamped on the worker's `SessionMeta.ObservedBy[]`.

Doctor commands read both journals through `ReadEvents` plus pure folds. They do
not call append-capable `Open`, repair a tail, migrate state, construct a Session,
or invoke a provider. A retired delegate JobRecord fails closed as
`legacy_delegate_state`; a watch addressed through that retired activation
fails as `legacy_delegate_watch_state`.

**Read it via:** `doctor_evener` `tree <selector>` (with `observers: true` for observer edges).

---

## Provenance (`provenance.Causal`) — and the runaway breaker

Watch-send deliveries and some job events carry causal provenance:

```
provenance.Causal{
    WatchKeys      []WatchKey   // deduped SET of (WatchID, WatchGeneration) — LOAD-BEARING
    Chain          []Entry      // ordered diagnostic trail — TRUNCATABLE
    ChainTruncated bool
}
```

- **There is no echo suppression.** Watches **always deliver**. A self-influenced
  delivery — one whose triggering event already carries this watch's key
  (`ContainsWatch(p, watchID, generation)`) — is **delivered and recorded**, not
  dropped; the runtime injects a depth-gradient `<system-reminder>` line so the
  sidecar can self-regulate (inform+breaker). So self-influence is **normal**, and
  a watch records self-influenced deliveries by design — that is not a defect.
- `WatchKeys` / the `Chain` are **diagnostic provenance**, not a suppression set.
  `WatchKeys` is the deduped `(watch_id, watch_generation)` set carried for causal
  reasoning; the `Chain` is the ordered diagnostic trail. A recorded delivery is
  stamped with its own watch's key at delivery time (`watchSendSnapshot` →
  `WithWatch`), so `ContainsWatch` over a recorded delivery is **vacuously true** —
  it is not a verdict on anything.
- **The breaker signal is the recorded `SelfInfluenceDepth`** (an int the runtime
  stamps on each `WatchSendState`) plus the **`runaway` drops**. The runtime
  computes a coalescing-aware self-influence depth — distinct *delivered* prior
  deliveries of this watch in the lineage (a coalesced-away pending that never
  independently delivered does **not** count) — sizes the gradient line from it,
  and at depth **8** (`runawaySelfInfluenceDepth`) drops the send with
  `DiagnosticReason = "runaway"`. The volume breaker (`watchDeliveryBudget = 50`)
  is the floor that bounds the no-send notification path.
- The doctor **reads** these recorded facts (the stamped `SelfInfluenceDepth` and
  the `runaway` drop reason); it does **not** re-derive a loop from the `Chain`. A
  forensic tool observes recorded reality, it does not re-simulate the runtime.
- Caveat: `Chain` is truncatable (`maxDiagnosticChain = 16`, sets
  `ChainTruncated`), so a deep lineage can lose middle hops. Truncation sharpens
  the gradient line but does not drive the fuse — the runaway is bounded by the
  recorded depth and, ultimately, by the volume breaker.

**Read it via:** `doctor_evener` `watches <selector>` with `self_loops: true` (now reads breaker
telemetry: per-watch `max_self_influence_depth` and `runaway_drops`, surfacing
only watches whose fuse fired).

---

## The client-mutation store (`clientMutationSnapshot`)

`<bucket>/mutations/<SID>.json` is the daemon's durable record of what clients
asked it to do. It is the artifact that settles **"did the user's input ever
reach the daemon?"** — the journal holds every client mutation the daemon
accepted AND every one it rejected (`executeAtomic` writes a rejected record via
`rejectClientMutation`, with `operation_state: "rejected"` and the wire
rejection). **Absence from the journal means the request never arrived**; a
present record shows exactly what happened to it. That distinction is what
separates a client-side wedge (a browser outbox that never sent) from a
server-side one.

Fields that carry a diagnosis:

- `journal` — per client-mutation-ID: `method` (`turn/start`, `turn/queue`,
  `turn/steer`, …), `operation_state` (`inFlight` / `applied` / `rejected` /
  `terminal`), `execution_state`, `stable_turn_id`, and on refused records the
  `rejection` code + message.
- `input_queue` — inputs durably queued and still waiting for a turn to drain
  them, in drain order.
- `pending_executions` — mutations the daemon is still executing.
- `accepted_turns`, `queue_revision` — the counters the optimistic client
  reconciles against.

The client mutation ID is the join key to the client's own outbox: a reply the
browser holds but the journal does not know about never left the browser.

**Read it via:** `doctor_evener` `mutations <selector>`. A session with no store
reports that cleanly (it accepted no client mutations); a file the reader cannot
decode is an error naming the file, never an empty journal.

---

## Meta (`schema.SessionMeta`)

`schema.LoadSessionMeta(bucketDir, sid)` / `schema.ListSessionMetas(bucketDir)`.
Key fields: `ID`, `Model`, `Config` (incl. `ResultToolName`), `ParentSessionID`
(fork lineage), `IsSubagent`, and `ObservedBy[]` (the observer sessions watching
this worker — the durable basis for the session tree's observer edges).

**Read it via:** `doctor_evener` `locate` (path), `tree` with `observers: true`
(observer edges), `transcript` (result-tool resolution).
