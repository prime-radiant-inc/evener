# Transcript as the Only History Read Model

Status: proposed, revision 4 (2026-09-25). Supersedes the eviction approach in
the closed PR #2251.

Revision history:
- Revision 2 replaced a sequence watermark with idempotent merges.
- Revision 3 stopped minting history identity before an entry exists. History
  became the projection of persisted entries, and live state became an overlay.
- Revision 4 fixes gaps design review found inside that model:
  - read bound: reads use the recorded length, not the synced length
  - status: legacy and non-execution turns
  - communicate calls: no item removal needed
  - the projector's bounded incremental state
  - impure projection inputs
  - client-mutation turn IDs
  - retry streams
  - writer failure
  - phase-2 reader updates
  - an early latency gate

## Problem

`evener serve` keeps a full projected copy of every turn it has ever shown:
`Server.appTurns` for the root thread and one `appTurnSnapshot` per delegate in
`Server.appDescendants` (`server/server.go:317-349`,
`server/appwire_turns.go:126-152`). The copy holds item text, tool output,
`Raw` tool state and images. It costs about 1.1× the transcript it mirrors and
is never released while the root identity lives. Measured on real sessions: 83
MB for a 75 MB root transcript, and 200 MB for 260 finished delegates.

The copy exists because the live view and the transcript disagree about
identity. A live item and its persisted form have different turn IDs, item IDs,
positions and turn boundaries, so the transcript cannot answer a read for a live
session. This also breaks the governing spec
(`docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`:
"Transcript key: stable item identity reproduced by live event projection and
later file projection"). Restarting a daemon already changes what clients see.

## End state

1. **History is the projection of recorded entries, and nothing else.** One
   projector, shared by daemon and hub, turns transcript entries into turns and
   items. The daemon produces live history notifications by projecting each
   entry after it is recorded. A live history item and its reloaded form are the
   same by construction.
2. **Everything not yet recorded is a live overlay.** Streaming assistant text
   and reasoning, running tool state, communicate previews and ephemeral notices
   live in memory, separate from history. They never claim history identity.
3. **Merges are versioned and upsert-only.** Every history item and turn carries
   a version: the highest `Seq` of the entries that contributed to it. The
   higher version wins, so applying a fact twice changes nothing, and neither
   does a file read that is ahead of the live stream. History items are never
   removed.
4. **Memory holds only the overlay and a bounded projection state.** Memory per
   thread no longer depends on its history.

What is deleted:
- the per-thread turn snapshot's history
- delegate snapshots and their seeding
- prelude shifting
- incarnation rotation on prelude
- the projector's ID counters
- client-minted steering IDs
- the server's separate prepared-transcript cache
- the "no file I/O inside the cut" rule

## Design

### Recorded length, the read bound

Today the writer has three doors:
- `Append` is buffered and fsyncs at most once per `SyncInterval`, which is 1s
  for sessions (`agent/transcript/transcript.go:521-524, 680`;
  `agent/session_init.go:616`).
- `AppendSynced` fsyncs.
- `AppendDurable` fsyncs, and truncates the record if the fsync fails
  (`transcript.go:655-677, 888-911`).

A record is *recorded* once its whole line is written. Retained records count:
the line was written but the fsync failed (`transcript.go:729-735`).

- After every append that records a line, through any door, the writer publishes
  its **recorded length** under its lock. The entry is handed to the server only
  after that.
- Only the durable door rolls back, and it does so before publishing. So the
  recorded length never includes rolled-back bytes.
- A rolled-back append still consumes its `Seq`, so a `Seq` is never reused
  within one writer's life. Today a rollback spends no Seq (`transcript.go:590-594`).
- Every writer in the process registers its file's recorded length in one
  registry, cold writers included. In-process reads never go past it.
- **Other processes.** A reader with no writer in its process reads to end of
  file and drops an incomplete last line, as today. For example, the hub reading
  a file written elsewhere. A write rolled back by another process can therefore
  be seen briefly. Rollback needs an fsync failure, so this is rare, and the
  `Seq` is never reused.
- **Crash or power loss.** A crash can lose buffered entries whose
  notifications clients already have. Reopening sets `nextSeq` from what
  survived (`transcript.go:1056-1061`). The daemon has a new incarnation, so
  clients replace their history state on reconnect to a new incarnation instead
  of merging it.

### Persisted identity

**Turn IDs.** A new dedicated `TurnID` field is written on every entry that
belongs to a turn. `StableTurnID` keeps its current meaning. On steering entries
it is the steer mutation's own ID and is never a turn ID
(`internal/appprojector/appwire_projection.go:972-979`).
- A turn admitted from a client mutation keeps its reserved ID, `turn_m<N>`.
  That is the ID `turn/start` already returns (`appwire/types.go:1353-1370,
  1750-1753`; `agent/session_client_mutation.go:433-440`), so client and history
  agree.
- Other turns get `t_<ulid>`, minted by the session. This namespace is disjoint
  from legacy `turn_<n>`.
- The session publishes the running turn ID through `SetProcessingTurn` before
  the server marks it processing (`server/server.go:880-895`). This replaces the
  server's own reservation in `SetProcessing(true)` (`server/server.go:864-927`;
  `cmd/evener/serve.go:1709, 1729`).

**Turn kind.** The first entry of each turn persists a `TurnKind`:
- `execution`: a real run of the model.
- `gap`: standalone entries recorded between executions (hooks, model switch,
  environment, persisted notices). The session mints one gap turn per run of
  such entries, which keeps today's grouping of a burst of announcements into
  one turn (`appwire_projection.go:2160-2198`). Startup entries use the prelude
  turn.
- `delivery`: a cold writer's entry. Attention delivery, watch sends and shell
  repair have no session (`agent/session_attention.go:351-397`,
  `agent/delegate_delivery.go:106`, `agent/delegate_tree_watch.go:478`,
  `agent/delegate_shell_repair.go:109`). Each cold write is its own turn with a
  fresh ID, which is how they project today.

**Turn membership** comes from `TurnID`, not from entry-kind adjacency.

**Item key.** `transcriptKey = apptranscript-item-v2:<turnID>:<Seq>:<part>`.
`Seq` is the entry that opens the item, and `part` is the item's index in that
entry's projection.
- An item that spans entries keeps its opener's key. A tool call and its result
  are keyed by the ASSISTANT entry that holds the call.
- This fixes `mergeAppThreadItems` keeping the incoming ID
  (`internal/apptranscript/logical_turn.go:314-316`).
- It also makes a reused provider call ID harmless.
- Item `id` derives from the key and keeps today's kind prefixes.

**Position.** `{entry: Seq + 1, item: part}` for every entry, legacy and new.
Header-derived prelude items get `{entry: 0}`.

**Format marker.** Every new entry carries an explicit format version, so the
projector never infers the format from which fields are present. `StableTurnID`
and `OwningTurnID` already appear on legacy steering entries
(`agent/schema/turn.go:233-251`).

**Legacy entries** (no format marker):
- They keep today's turn IDs (`turn_<entryIndex>`), adjacency grouping, and
  inferred status: completed by default, failed or interrupted from their
  entries (`internal/apptranscript/logical_turn.go:185-218`).
- Their positions move to the `Seq` scheme. Cursors go stale once, at the
  projection version bump.
- A resumed legacy session is self-consistent: new entries carry new identity,
  and legacy turns are never rewritten.

### Turn status

Status is derived from persisted facts:
- A `gap`, `delivery` or prelude turn is always complete.
- An `execution` turn with a completion entry is completed, failed or
  interrupted, as recorded.
- An `execution` turn with no completion entry is **open**.
- A legacy turn keeps today's inferred status.

On resume, the session writes an interrupted completion only for new-format
`execution` turns that it finds open.

Whether an open turn is running is overlay state. A client shows an open turn as
running while the overlay says it is running, and as interrupted otherwise. That
second case is a display rule, never stored or merged as a fact, so a later
`turn/started` in the overlay simply shows the turn as running. A daemonless
past session has an empty overlay, so a turn left open by a crash displays as
interrupted, which is what the hub shows today.

### The projector

The projector is one pure function over (entries, header). It has one
`ProjectionID`, shared by daemon and hub. Inputs that are not in the transcript
stay out of it:
- Cost is computed from persisted usage and model at egress. The model is
  recorded per entry.
- File-backed output images are resolved from persisted image references at
  egress (`cmd/evener-hub/output_images.go`).
- Egress enrichments are not part of the versioned item. A client replaces them
  with whatever it received most recently.

The projector also fixes places where the file projection loses or mangles what
live shows today:
- It projects `Thinking.Summary` when `Thinking.Text` is empty
  (`llm/providers/responses/response.go:75-94`,
  `internal/apptranscript/apptranscript.go:521-530`).
- It projects one agentMessage per assistant text run and one reasoning item per
  entry, matching how live displays them today.
- **Communicate calls become history only when their result is known.** The
  ASSISTANT entry's projection omits communicate calls. The TOOL_RESULTS entry's
  projection adds each call that succeeded, keyed by the ASSISTANT entry. A
  failed one never appears. Until then the communicate preview is overlay. So no
  history item is ever removed.
- It skips fold copies. A fold re-appends entries after its markers
  (`agent/session_compaction.go:146-154`); the copies carry `OriginalSeq` and
  are neither projected nor announced.

**Incremental use.** For live notifications, the daemon keeps a projection state
per thread:
- accumulators for the open turn: usage sums, earliest timestamp
- the call records of the open round: names and arguments, needed to emit a full
  tool item when TOOL_RESULTS lands (`apptranscript.go:598-603`)

This state is released at round end and at turn end. It is bounded by one round
and not by history. The parity harness checks that incremental projection of
entries equals projection of the whole file.

### Live history notifications

After each recorded append, the server projects the entry with the incremental
state and emits `history/updated`, carrying the full current form and version of
every affected item and turn. Every history write is covered, including the
writes that emit nothing today:
- delegate attention and delivery to a live parent
  (`agent/session_attention.go:482-560`)
- shell attention (`agent/jobs.go:2155-2176`)
- watch sends (`agent/job_watch.go:4541`)
- retained attention turns (`agent/session_attention.go:1184-1207`)
- NOTES_CONTEXT and TURN_FAILURE, which today exist only in the file

Notifications are derived from recorded entries, so they never run ahead of the
file. If projecting or publishing fails after an append records, the daemon logs
it and marks the thread stale. The next read replaces the client's history for
that thread instead of merging, so a missed notification cannot hide recorded
history.

### The live overlay

The overlay is per thread and in memory. It holds four kinds of state.

**Streams.** Streaming text and reasoning belong to a `streamID`: one per model
call, the same across its retry attempts.
- The ASSISTANT entry written for that call records the `streamID`. Once that
  entry exists in history, the stream's overlay items are covered and dropped.
  This holds whether the entry arrives live or through a file read.
- The overlay has explicit `overlay/reset(streamID)`, sent when a retry discards
  partial output (`appwire_projection.go:549-575`).
- It also has `overlay/end(streamID)`, sent at round end.
- A stream that ended without an ASSISTANT entry becomes one interrupted notice
  and is dropped. That covers a reasoning-only round
  (`agent/session_lifecycle.go:958-960`), a closing session, the content filter,
  and empty salvage (`agent/session_events.go:679-707`).
- Salvage that is recorded, including from an earlier attempt
  (`session_events.go:692`), records the call's `streamID`.

**Tool execution state.** Tools run after the ASSISTANT entry holding their
calls is recorded (`agent/session_model_call.go:994-996`). Running output,
per-call completion (TOOL_CALL_END, `agent/session_tools.go:876`) and held
images (`appwire_projection.go:847-868`) attach to the call's history key.
- The history item wins once its version includes the TOOL_RESULTS entry.
- Running output keeps the last 256 KB per call. The full output arrives with
  TOOL_RESULTS.
- If TOOL_RESULTS is never recorded, the round's `overlay/end` turns the
  execution state into an interrupted notice.

**Running state.** The running turn ID and the thread status.

**Ephemeral notices.** These cover `round_timings`, `prompt_loaded`,
`plugin_loaded`, `loop_detection`, `context_compaction`, `fork_summary`,
`warning`, and interrupted-stream notices.
- They are kept in a per-thread ring: 50 `round_timings`, 50 of everything else,
  64 KB in total.
- A daemon-wide cap of 16 MB evicts the oldest first across threads.
- Each notice has an anchor, `{entry: Seq + 1 of the preceding entry, item: part
  count of that entry, sub: n}`. `sub` is a new position component, absent on
  history items, that orders a notice after the entry it followed.
- Notices survive a browser refresh while the daemon lives and vanish on
  restart, as today.
- A released delegate's ring is dropped with its runtime.

Some items are persisted as presentational entries instead of being ephemeral:
`tool_repair`, `goal_ended`, `turn_limit` (unless an existing TURN_FAILURE covers
it), and standalone `skill_activated`. Turn timing (`CompletedAt`, `DurationMS`)
is persisted in the completion entry, and tool `DurationMS` in TOOL_RESULTS.

### Writer failure

The writer API returns either `recorded Seq` or `not recorded`. Today `Append`
and `AppendDurable` return nil with Seq 0 for both a missing and a closed writer
(`agent/transcript/transcript.go:606-627`).

`evener serve` always has a state directory (`cmd/evener/serve.go:527`). A served
session whose transcript cannot be created, or whose writer is poisoned
(`transcript.go:724-728`), **fails closed**:
- it stops accepting input
- the thread shows one visible diagnostic
- nothing piles up in memory

Turns held before the writer attaches (`agent/session.go:2195-2200`) are
announced when they are recorded at attach.

### Reads

`thread/read` has two steps:
1. Inside `CaptureSubscription`, it captures the thread's overlay, its stale flag
   and the subscription cut.
2. After releasing the cut, it projects history up to the recorded length, then
   merges: history by version, overlay items minus covered streams.

Notifications delivered after the response merge by the same rules. So reading
the file ahead of the stream cannot produce a gap, a duplicate or a regression:
- a later `history/updated` at a lower or equal version is ignored
- overlay updates for a covered or reset stream are ignored

Nothing announced before the cut is missing from the read. An announced entry
has already been recorded, and the recorded length is published before the
entry is announced.

`thread/turns/list` pages come from the file, with the same merge applied to the
open turn's page. A thread with no runtime has an empty overlay and is read the
same way.

### Index

The on-disk item index is keyed by position and serves windows by searching
positions. This replaces today's per-group dense rank arithmetic
(`internal/apptranscript/item_paging.go:176-234`).

Append validation changes from a full prefix rehash on each read after an append
(`internal/apptranscript/turn_index.go:571-584`) to a check of file identity,
recorded length and trailing bytes. That check is proven by the attention fold
cursor (PR #2254). The index is bounded by the same recorded length as reads.

### Fork

`thread/fork` names its source by item `transcriptKey`, and the server resolves
it to the entry. This works for legacy and new items alike. It replaces the
1-based entry index in `sourceTurnId` (`appwire/types.go:1706-1715`; hub parser
`cmd/evener-hub/app_threadlifecycle.go:1558-1568`; `agent/fork.go:61-69`).

### Protocol and clients

The switch changes the protocol:
- `history/updated`
- versions on items and turns
- overlay `streamID`, reset and end
- position `sub`
- server-side steering identity
- the replace-on-new-incarnation and stale-thread rules
- fork by key

So it bumps the AppWire protocol major version. A client that announces an older
version gets an explicit "upgrade required" error. The web client ships with the
hub. The mobile app must update.

The shared reducer (`appwire-client/typescript/reducer.ts`) implements:
- merging by version
- stream coverage, reset and end
- anchors with `sub`
- the display rule for open turns
- no client-minted steering IDs

### Schema compatibility

Transcript entries decode strictly (`agent/transcript/transcript.go:283-307`), so
an older hub cannot read a transcript that contains new fields. All new entry
fields ship in one release (phase 2):
- the format marker
- `TurnID` and `TurnKind`
- `streamID`
- `OriginalSeq`
- the per-entry model
- the completion entry
- presentational entries
- timing fields

The version skew therefore happens once. The release notes say to restart the
hub along with the daemon.

### Out of scope

- the client-mutation journal
- the notifier replay ring (count-bounded; no production reader)
- the task store
- the projector's small `delegates` map

## Acceptance criteria

**Memory.** Measured on a long-lived daemon running a copy of the 260-delegate
coordinator session, after replaying its activity (not after a cold restart):
- Retained history memory is 0 beyond the projection state. That state is the
  open turn's accumulators plus the open round's call records.
- Ephemeral notices are within 64 KB per thread and 16 MB across the daemon.
- For comparison, the same measurement today is about 283 MB.

**Latency.** Measured for `thread/read` of the latest window at the default page
size, on the 95 MB root transcript with a warm index, on the dev machine's local
SSD, 200 samples:
- idle writer: p99 under 50 ms, and no worse than 2× today's in-memory read
- while appending one entry per 100 ms: p99 under 100 ms

This is prototyped and measured in phase 1, before any irreversible step.

**Parity.** The parity harness reports zero divergences for new-format sessions:
- live against reload, including across a daemon restart
- incremental projection against whole-file projection

## Migration

Each phase ships on its own and keeps main green.

1. **Parity harness and latency gate.**
   - A differential test drives a scripted multi-round session and compares the
     live view with the file projection, item by item. The session covers:
     - user input, assistant text and reasoning
     - tool calls and results with images
     - communicate
     - steering and hooks
     - model switch and compaction
     - failure and retry, and goal continuation
     - delegate attention
     - a restart
   - The test passes. Known divergences are listed in a table, each tagged with
     the phase that removes it. The test fails on a new divergence, and on a
     listed divergence that no longer occurs.
   - A benchmark measures file-window reads on a copy of the 95 MB root
     transcript against the latency criteria. If they are not met, stop and
     redesign the index before phase 2.
2. **Write the new fields, no behavior change.** Writers write every new field,
   and every transcript consumer is taught to skip the new entry kinds (the
   completion entry and the presentational entries). Consumers to cover:
   - resume (`agent/transcript_read.go:162-190`)
   - orphan repair (`agent/history_repair.go:79-88`)
   - the model wire builder (`agent/session_model_call.go:1718-1726`)
   - delegate fork context (`agent/delegate_fork_context.go:25,67`)
   - `agent/session_init.go:2361`
   - grouping and the index (`internal/apptranscript/logical_turn.go:30-35,
     94-104`)
   - the attention fold
   - derived totals
   - renderers

   Each consumer is a small task with a test proving a transcript that contains
   the new kinds resumes, projects, repairs and renders exactly as one without
   them. Other small tasks cover the recorded-length registry and `Seq` on
   rollback, the explicit write result, and failing closed on writer failure.
   Every projection rule and client-visible behavior stays as it is today.
3. **Activation.** One PR, built as a stack of reviewable commits, switches the
   following together:
   - the projector's new rules, positions and incremental state
   - the position-keyed index with tail validation
   - live history from projected entries
   - the overlay
   - status from persisted facts, with resume completions
   - `history/updated` with versions
   - server-side steering identity
   - fork by key
   - the protocol major bump
   - the reducer
   - projection version 2

   It ships as one unit because the file side and the live side must switch
   together.
4. **Read switch.** Reads as described. Delete snapshot history, delegate
   snapshots, the prepared cache and the cut I/O rule. Measure the acceptance
   criteria.
5. **Cleanup and docs.** Remove dead code and tests. Amend the atomic paging
   spec and `docs/appwire-protocol.md`.

Each phase gets its own implementation plan once this design is accepted and the
previous phase has landed.

## Risks

- **Mobile must update** after phase 3.
- **Old cursors and stored anchors** go stale or miss once.
- **Read latency moves to disk.** The phase 1 gate bounds it before anything
  irreversible ships.
- **Cross-process rollback** can briefly show a record the other process then
  rolls back. This needs an fsync failure in that process.
- **Phase 3 is large.** It stays reviewable as a stack of commits, and the
  parity harness gates it.
