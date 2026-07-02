# Serf Doctoring Tools — Design

Date: 2026-06-19
Status: draft for Jesse review
Builds on: `docs/agentic-testing.md`, `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md`

## Motivation — the concrete pain

We just shipped observer-watch causal provenance and ran live e2e tests. Verifying
what actually happened meant hand-parsing serf's on-disk JSONL, and it broke in
specific, recurring ways. Each of these is a real failure from that session, not a
hypothetical:

1. **Schema guessing breaks.** A hand-written transcript parser returned
   `0 communicate calls` / `0 steering entries` because it guessed the JSONL shape
   wrong. It looked for top-level `communicate`/`STEERING` keys. In reality the
   transcript writes `transcript.Entry{Kind:"entry", Seq, Turn: schema.Turn{...}}`
   (one JSON object per line; see `agent/transcript/transcript.go:54`), a
   `communicate` call is a *tool call* nested at
   `entry.turn.message.content[].tool_call` with `name == "communicate"` (the
   default result-tool name, `cmd/serf/main.go:170`), and a steering turn is
   `entry.turn.kind == "STEERING"` — one level deeper than the parser looked.
   Any external parser rots the instant the schema shifts. **This is lesson #1:
   doctoring tools must read serf's OWN canonical types, never re-guess them.**

2. **`jobs.jsonl` is hard to read by hand.** The watch-send log writes one
   `watch_send_pending` event per *coalescing update*, not per delivery: 8 pending
   lines can be 4 real deliveries because pending frames coalesce latest-wins by
   `WatchSendKey` + `UpdateSeq` (`FoldWatchSends`, `agent/internal/jobstore/fold.go:199`).
   A raw `grep -c watch_send_pending` overcounts deliveries. Distinguishing distinct
   deliveries, reading the per-delivery provenance chain, and reconciling the
   lifecycle (pending → delivered / dropped / evicted) all require replaying the fold.
   A `pending != delivered` gap *looks* like a dropped delivery but is usually
   expected coalescing.

3. **Causality / self-loops.** "Was this watch delivery caused by the caller (legit)
   or by the observer it feeds (a feedback loop)?" required reading
   `provenance.chain[].session_id` by hand and comparing it to the delivery target.
   The data is right there (`provenance.Causal.Chain`, `agent/provenance/provenance.go:22`)
   but reconstructing the answer per-delivery by eye is error-prone. Detecting a
   self-loop should be one command.

4. **Tool-call vs mention.** `grep -c delegate_send` over a transcript returned 5,
   but the actual number of `delegate_send` *tool invocations* was 0 — the other
   matches were the tool list in an api_call request log and a "do not call
   delegate_send" instruction in assistant text. Counting real invocations means
   walking `content[]` and counting `kind == "tool_call"` blocks, not grepping lines.

5. **Cross-session trees.** A parent session and its observer/delegate sub-sessions
   are different SIDs, and live in *different* project-hash buckets under
   `~/.local/state/serf/projects/<hash>/sessions/`. Linking them (who delegated
   whom, which job maps to which child transcript) was manual cross-referencing of
   `delegate_created` events, `transcript_ref`s, and `observed_by` meta.

6. **Repeated e2e assertions.** "watch fired N times", "no self-loop", "no dropped
   delivery", "observer made 0 delegate_sends", "frame contains message X" — these
   got re-hand-rolled every run, each a fresh chance to mis-parse.

7. **Just finding the files.** Every inspection started with a `find
   ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`. The
   project-hash indirection is a tax on every single lookup.

The throughline: serf already has typed structs and fold logic that produce the
*exact* answers we were reconstructing by hand. The tools should call that code.

## Form-factor decision

**Recommendation: a first-class `serf doctor <subcommand>` CLI, implemented as a
thin `cmd/serf` dispatch over a new exported facade in the `agent` package. Not
standalone scripts.**

Three options were on the table:

| Option | Verdict |
| --- | --- |
| **Standalone scripts** (Python/Go that re-parse JSONL) | **Rejected.** This is exactly the drift that bit us (pains #1, #2, #4). A script that re-declares the JSONL shape silently produces wrong numbers the moment a field moves, and nothing fails loudly. |
| **First-class `serf doctor` CLI importing canonical types** | **Chosen.** It calls `jobstore.Fold*`, walks `llm.ContentPart`, reads `provenance.Causal` — so a schema change either updates the tool automatically or *fails to compile*. It also already has the file-locator logic (`resolveTranscript`, `enumerateBuckets`) sitting in `agent/`. |
| **Go package + separate tiny CLI** | Folded into the chosen option: the package is `agent` (which already owns the readers) and the CLI is a `serf` subcommand. No new top-level package needed. |

### The hard constraint that decides the wiring

`agent/internal/jobstore` is an **`internal/` package**. Go's rule: it is importable
only by code rooted at `agent/` (the directory containing `internal/`). `cmd/serf`
is rooted at the module root, **not** under `agent/`, so **`cmd/serf` cannot import
`jobstore` directly** (verified: no file under `cmd/` or `server/` imports it today).

This is load-bearing, not a footnote. It means the doctoring logic *must* live inside
package `agent` (or a sibling under `agent/`) and be reached through **exported**
functions; `cmd/serf` only does flag parsing and calls them. Concretely:

- New file `agent/doctor.go` (package `agent`) holds the exported entry points:
  `DoctorTranscript(...)`, `DoctorWatches(...)`, `DoctorTree(...)`, `DoctorLocate(...)`.
  These reuse the existing in-package readers — `renderTranscript`/`renderMarkdown`
  (`agent/transcript_render.go`), the outline renderer (`agent/session_outline.go`),
  `resolveTranscript`/`enumerateBuckets` (`agent/transcript_lookup.go`), and
  `jobstore.Store.Load*()` (which package `agent` is already allowed to call).
- New file `cmd/serf/doctor.go` adds a `doctor` case to `dispatchCLICommand`
  (`cmd/serf/main.go:248`), with one `flag.FlagSet` per subcommand — matching the
  existing `serve` / `launch-check` / `openai` dispatch pattern exactly.

This keeps the canonical fold/parse logic on one side of the `internal` wall and the
CLI shell on the other, with the boundary enforced by the compiler.

## The tool set

YAGNI: this is the minimum that would have made the observer-watch e2e verification
and live debugging easy. Four subcommands, plus a thin assertion mode folded into
the first two. Every one traces to a pain above.

Shared conventions for all subcommands:
- First positional arg is a **session selector** in the form `resolveTranscript`
  already accepts: `""`/`current`, `local:<SID>`, `proj:<hash>:<SID>`, or a bare
  `<SID>` (searched across buckets, ambiguity reported with candidate refs). This
  reuse means the locator semantics are identical to what the running agent's
  `read_session_transcript` tool already uses — no second dialect.
- `--json` emits the underlying typed struct as JSON for machine consumers; default
  is a human summary (see Output discipline).
- `--state-dir` overrides the XDG-computed root (mirrors `serf --state-dir`), so the
  tools work against an e2e scratch root.

### 1. `serf doctor locate <selector>`

- **Purpose:** resolve a session selector to its absolute on-disk file paths. No more
  `find`.
- **Inputs:** selector; `--all-buckets` to list every bucket the SID appears in.
- **Output:** the resolved `transcript.jsonl`, `meta.json`, and `jobs.jsonl` paths
  (and project-hash bucket). `--json` returns `{transcript_ref, transcript_path,
  meta_path, jobs_path, bucket_hash}`.
- **When to use:** the first call in any inspection; also to feed paths to ad-hoc
  tools when a one-off really is warranted.
- **Pain solved:** #7 (file-finding), #5 (bucket indirection).
- **Reuses:** `resolveTranscript`, `transcriptPath`, `stateHomeFor`,
  `enumerateBuckets` (`agent/transcript_lookup.go`); jobs path is
  `filepath.Join(bucketDir, "sessions", … )` — note jobs.jsonl lives beside the
  transcript per `agent/jobs.go:267` (`jobstore.Open(filepath.Join(dir, "jobs.jsonl"))`).
  Canonical layout it resolves to:
  `<base>/serf/projects/<sha256(originURL|workDir)[:8]>/sessions/<SID>.{transcript.jsonl,meta.json,jobs.jsonl}`,
  where `<base>` = `--state-dir` › `SERF_STATE_HOME`/`SERF_STATE_DIR` › `$XDG_STATE_HOME`
  › `$HOME/.local/state`, and the bucket hash is 16 hex chars
  (`agent/runtime_dir.go`). The locator *resolves* via the existing selector logic and
  cross-bucket glob — it must not recompute the hash itself (the running session already
  knows its bucket, and a fresh `serf doctor` would have to recompute it from the right
  origin/cwd to match; resolving by glob avoids that whole failure mode).

### 2. `serf doctor transcript <selector>`

- **Purpose:** render a session's logical turns — user/steering/assistant/tool-calls/
  communicates — instead of raw JSONL, and answer "how many real X calls" structurally.
- **Inputs:** selector; `--range last:N|start:N|A-B` (the grammar `parseRange` already
  implements); `--format outline|markdown` (default `markdown`); `--count <tool>` to
  print the structural invocation count of a tool name (e.g. `--count delegate_send`)
  and exit.
- **Output (default):** the existing markdown render — conversation-grouped, tool
  calls condensed into ID-paired cards, `communicate` surfaced as assistant text,
  results head+tail truncated — with the honest provenance footer
  (`turns_total`/`turns_rendered`/`elided`). `--format outline` prints the turn map.
  `--count delegate_send` prints e.g. `delegate_send: 0 calls (5 textual mentions in
  api_call logs / instructions)` — the exact disambiguation pain #4 needed.
- **When to use:** "what did this session actually do?"; "did the observer call
  `delegate_send`?"; "what was the communicated final message?".
- **Pain solved:** #1 (communicate/steering live where the renderer already looks),
  #4 (tool-call vs mention), #6 (the per-run "frame contains X" / "0 delegate_sends"
  assertions become `--count` + grep on rendered text).
- **Reuses:** `renderTranscript` / `renderMarkdown` and the outline renderer
  *verbatim*. Counting walks `entry.Turn.Message.Content` for
  `Kind == llm.ContentToolCall && ToolCall.Name == <tool>` (the same predicate
  `toolCallIDs` already uses, `agent/transcript_render.go:528`) and, for the "mentions"
  number, counts substring hits in `api_call` request logs and assistant text so the
  two are never conflated again. Note `communicate` is the *default* result-tool name
  but is per-session overridable (`SessionConfig.ResultToolName`,
  `agent/session_config.go`); `--count` resolves the effective name via
  `effectiveResultToolName` (which already reads the meta/config,
  `agent/transcript_render.go:32`) rather than hard-coding the string, so an aliased
  result tool is still counted correctly.

### 3. `serf doctor watches <selector>`

- **Purpose:** the watch/job inspector. Collapse coalescing, show distinct deliveries
  with their provenance chains and lifecycle, and flag self-loops.
- **Inputs:** selector; `--watch <watch_id>` to scope to one watch; `--self-loops`
  to print only deliveries whose provenance implicates the delivery target.
- **Output (default), per watch (`watch_registered` → `watch_cleared`,
  via `FoldWatches`):**
  - registration: `watch_id`, target, `send_to`, condition, generation, active/ended
    (+ `end_reason`).
  - **distinct deliveries** — the count of *settled* `watch_send` keys
    (delivered/dropped/evicted), NOT the raw `watch_send_pending` line count — with a
    one-line note when they differ (`8 pending lines → 4 deliveries (latest-wins
    coalescing — expected)`), so the gap reads as normal, not as a bug.
  - per delivery: `delivery_id`, trigger identity/reason, terminal kind
    (`delivered`/`dropped`/`evicted`) + `diagnostic_reason` if dropped,
    `coalesced_count`, and the **provenance chain** as
    `kind:watch watch_id … session_id=…` hops.
  - **self-loop verdict:** SELF-LOOP when any `provenance.Chain[].SessionID` equals
    the delivery's resolved `send_to`/target session — i.e. the watch is being
    retriggered by the very session it feeds. This is the safety boundary the
    feature exists to enforce, so the inspector states it explicitly per delivery
    and in a summary line.
- **When to use:** verifying watch behavior after any watch/observer change; the
  single most-repeated thing we did by hand last session.
- **Pain solved:** #2 (coalescing, lifecycle accounting), #3 (self-loop in one
  command), #6 ("fired N times" / "no dropped" / "no self-loop" assertions).
- **Reuses:** `jobstore.Store.LoadWatches()`, `LoadWatchSends()`, `LoadOrdered()`.
  Crucially, distinct-delivery counting must mirror `FoldWatchSends`' settle rule
  (`terminalSeq` keyed by `WatchSendKey`, `agent/internal/jobstore/fold.go:204`) — do
  not re-derive it. Provenance comes straight off
  `WatchSendState.Provenance` (`record.go:161`) and `Causal.Chain`.

### 4. `serf doctor tree <selector>`

- **Purpose:** the session-tree view — parent ↔ delegates/observers across buckets.
- **Inputs:** selector (any node in the tree); `--depth N` to bound; `--observers`
  to include observer edges, not just delegate edges.
- **Output:** an indented tree: each node is `SID  (agent_type)  status  →
  transcript_ref`. Delegate edges come from `delegate_created` events
  (`DelegateRecord`, parent via `ParentDelegateID` / `OwnerSessionID`); observer
  edges come from the watch read-grant history — the worker's
  `meta.observed_by[]` (`SessionMeta`, `agent/schema/snapshot.go:67`) and
  `FoldGrants` (observer-session → watched-job, `fold.go:236`). Each job's child is
  reached via its `transcript_ref` so you can pivot straight into
  `serf doctor transcript <ref>`.
- **When to use:** "which child transcript is this job?"; "who is observing whom?";
  orienting before drilling into a specific session.
- **Pain solved:** #5 (cross-session linking).
- **Reuses:** `LoadDelegates()`, `LoadGrants()`, `LoadSessionMeta` /
  `ListSessionMetas` (`agent/schema/snapshot.go`), and the same cross-bucket
  enumeration the locator uses. Edges follow the *same* `transcript_ref` and
  `delegate_*` fields the runtime persists, so the tree can never disagree with what
  actually ran.

### On e2e assertion helpers (folded in, not a new tool)

We do **not** add a separate assertion framework (that would be a framework, against
YAGNI, and overlaps test infra). Instead the two inspectors expose the exact numbers
an assertion needs as first-class fields:
- `--count <tool>` on `doctor transcript` (pain #4/#6),
- `--json` on `doctor watches` carrying `{distinct_deliveries, dropped, self_loop:
  bool}` (pain #3/#6),
so a scenario asserts with `jq` over `--json`, or a grep over `--count`. The
falsifiable number is produced by canonical code; the scenario just compares it.

## Output discipline

These tools serve BOTH the e2e driver / controller AI agents and humans. Per serf's
automation philosophy — named tools, good `--help`, context-managed output — each
subcommand is **summary-by-default, detail-on-demand**:

- **Summary by default.** `doctor watches` prints per-watch headers, distinct-delivery
  counts, and the self-loop verdict — not every coalesced pending frame. `doctor
  transcript` reuses the renderer's existing budgets (24k char conversation budget,
  200k hard cap, head+tail result truncation; `agent/transcript_render.go:129`) so it
  cannot dump a 10k-line transcript by accident.
- **Drill-down flags.** `--watch <id>`, `--range`, `--format markdown`, `--depth`,
  `--self-loops` narrow to the part you need. The full raw JSONL is always one
  `cat`/`read_session_transcript(format=jsonl)` away — the tools point at it rather
  than reprinting it.
- **`--json` for machines.** Emits the typed struct (`WatchRecord`, `WatchSendState`,
  `DelegateRecord`, the render meta envelope) directly, so the consumer parses
  serf's own shape — closing the loop on pain #1 for downstream code too.
- **Honest counts.** Every elision is reported (the renderer's
  `turns_rendered + elided == turns_total` invariant; the
  `pending-lines vs distinct-deliveries` note). The tools never silently hide
  evidence; a dropped delivery and a coalesced one are visibly different.

## Anti-drift

The single most important property, and the reason for the form-factor choice:

- **Reuse the fold, never re-parse.** Watch/job state comes only from
  `jobstore.Store.Load*()` → `Fold` / `FoldWatches` / `FoldWatchSends` /
  `FoldDelegates` / `FoldGrants`. Transcript rendering and tool-call counting come
  only from the in-package readers and `llm.ContentPart` walking. No subcommand
  declares its own copy of any on-disk shape.
- **A schema change updates the tools, or breaks the build.** Because the entry points
  live in package `agent` and consume the real structs, renaming a field
  (`WatchSendState.DeliveryID`, `Causal.Chain`, `transcript.Entry.Turn`) updates the
  tool automatically or fails to compile loudly — the opposite of a script silently
  printing `0`.
- **The `internal` wall is the enforcement mechanism.** Keeping `jobstore` internal and
  reaching it only through exported `agent` functions means there is *no* supported
  path for an external re-parser to creep back in.

## Non-goals / out of scope

- **Not a live TUI / monitor.** These read settled on-disk state after (or during) a
  run. Live observation is the appwire stream + `serf-tui` + the web hub; this is
  post-hoc forensics, not a replacement for them.
- **Not a replacement for the appwire projection.** The hub/TUI consume the live
  `events.SessionEvent` stream for rendering; doctoring reads the durable transcript
  and `jobs.jsonl`. Different sources, different jobs.
- **Not test infrastructure.** No assertion DSL, no fixtures, no harness. The
  inspectors *expose numbers* that `test/scenarios` can assert against with `jq`/grep;
  they do not run or own tests.
- **Not a mutator.** Read-only. No subcommand sends, steers, shuts down, repairs, or
  edits anything. (Mutation is the hub REST surface — `/api/spawn`, `/s/<id>/send`,
  `/s/<id>/shutdown` — and out of scope here.)
- **Not a hub client.** Everything works directly against the state dir, so it
  functions with the hub down and against an e2e scratch root.

## First cut — build these first

Highest value for the least surface, mapped to the pains that actually cost us time:

1. **`serf doctor watches`** — the watch/job inspector (coalescing collapse +
   provenance chain + self-loop verdict). This is the single biggest repeated
   hand-parse from the observer-watch session (pains #2, #3, #6) and is the riskiest
   to get wrong by eye. It is also where reusing `FoldWatchSends` pays off most.
2. **`serf doctor transcript`** with `--count` — turns/communicates rendered from the
   existing renderer, plus structural tool-call counting (pains #1, #4). The renderer
   already exists; this is mostly the exported facade + the `--count` predicate.
3. **`serf doctor locate`** — trivial to build on `resolveTranscript`, removes the
   `find` tax (pain #7), and the other two can shell out to it / share its resolver.

`serf doctor tree` (pain #5) is valuable but the least urgent — cross-session linking
was annoying but not the thing that produced *wrong* numbers. Build it third-or-fourth,
once 1–3 prove the facade shape.
