# Transcript as the Only History Read Model

Status: proposed, revision 5 (2026-09-25). Supersedes the eviction approach in
the closed PR #2251.

Revision history:
- **Revision 2** replaced a sequence watermark with idempotent merges.
- **Revision 3** made history the projection of recorded entries, with live
  state as an overlay.
- **Revision 4** set the read bound at the recorded length and added turn kinds
  and bounded projector state.
- **Revision 5** fixed the remaining local rules:
  - communicate is recorded at delivery
  - a recovered turn reopens and the last completion wins
  - forked turn IDs no longer collide
  - streams are scoped per attempt and covered per round
  - `Seq` is never reused in-process
  - cross-process reads replace state instead of merging
  - stale recovery is pushed
  - the index is seekable on disk
  - the read switch is folded into activation
  - the new entry kinds stay out of in-memory history

## Problem

`evener serve` keeps a full projected copy of every turn it has ever shown:
`Server.appTurns` for the root thread and one `appTurnSnapshot` per delegate in
`Server.appDescendants` (`server/server.go:317-349`,
`server/appwire_turns.go:126-152`). The copy holds item text, tool output,
`Raw` tool state and images, costs about 1.1× the transcript it mirrors, and is
never released while the root identity lives. Measured on real sessions: 83 MB
for a 75 MB root transcript, and 200 MB for 260 finished delegates.

The copy exists because the live view and the transcript disagree about
identity. A live item and its persisted form have different turn IDs, item IDs,
positions and turn boundaries, so the transcript cannot answer a read for a live
session.

This also breaks the governing spec
(`docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`:
"Transcript key: stable item identity reproduced by live event projection and
later file projection"). Restarting a daemon already changes what clients see.

## End state

1. **History is the projection of recorded entries, and nothing else.** One
   projector, shared by daemon and hub, turns transcript entries into turns and
   items. Live history notifications come from projecting each entry after it is
   recorded, so a live history item and its reloaded form are the same by
   construction.
2. **Everything not yet recorded is a live overlay.** Streaming text and
   reasoning, running tool state, communicate previews and ephemeral notices
   live in memory, separate from history, and never claim history identity.
3. **Merges are versioned and upsert-only.** Every history item and turn carries
   a version: the highest `Seq` of the entries that contributed to it. The
   higher version wins.
   - Applying a fact twice changes nothing.
   - A file read that is ahead of the live stream changes nothing.
   - History items are never removed.
4. **Memory holds only the overlay and bounded per-thread state.** Memory per
   thread no longer depends on the size of its history.

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

### Recorded length and Seq

The writer has three doors today:
- **`Append`** is buffered. It fsyncs at most once per `SyncInterval`, which is 1s
  for sessions (`agent/transcript/transcript.go:521-524, 680`;
  `agent/session_init.go:616`).
- **`AppendSynced`** fsyncs every record.
- **`AppendDurable`** fsyncs, and truncates the record if the fsync fails
  (`transcript.go:655-677, 888-911`).

A record is *recorded* once its whole line is written. That includes a retained
record: the line was written but its fsync failed (`transcript.go:729-735`).

- **Publishing the recorded length.** After every append that records a line,
  through any door, the writer publishes its recorded length under its lock. The
  entry is handed to the server only after that. The durable door rolls back
  before publishing, so the recorded length never includes rolled-back bytes.
- **The per-process registry.** Every writer in the process registers its file
  in one registry, cold writers and mid-session reopens included. The registry
  holds the file's recorded length and its next `Seq`.
  - In-process reads never go past the recorded length.
  - A writer opening a file takes `nextSeq = max(file max Seq + 1, registry
    nextSeq)`, which covers reopens such as `reopenAttentionTranscriptDurably`
    and `stabilizeAttentionForStop` (`agent/session_attention.go:1401-1446`).
  - A rolled-back append still consumes its `Seq`.
  - So a `Seq` is never reused within one daemon run.
- **Reads from another process.** A reader with no writer for the file in its
  process reads to end of file and drops an incomplete last line, as today. The
  hub reading a daemonless session is the main case. Such reads are
  **authoritative replacements**: there is no live stream to merge with, so each
  read replaces the thread's history on the client instead of merging into it. A
  record that another process later rolls back is therefore gone on the next
  read.
- **Crash or power loss** can lose buffered entries whose notifications clients
  already have. The restarted daemon has a new incarnation, and clients replace
  their history state when they reconnect to a new incarnation.

### Persisted identity

**Turn IDs.** A new dedicated `TurnID` field is written on every entry that
belongs to a turn. `StableTurnID` keeps its current meaning: on steering entries
it is the steer mutation's own ID and is never a turn ID
(`internal/appprojector/appwire_projection.go:972-979`).

- **Client-mutation turns.** A turn admitted from a client mutation keeps its
  reserved ID `turn_m<N>`, the ID `turn/start` already returns
  (`appwire/types.go:1353-1370, 1750-1753`;
  `agent/session_client_mutation.go:433-440`).
- **Other turns** get `t_<ulid>`, minted by the session. This namespace is
  disjoint from legacy `turn_<n>` IDs.
- **Fork.** A fork child's mutation sequence starts above the highest `turn_m`
  in the prefix it copies (`agent/fork.go:296-298`), so no new turn reuses a
  copied turn's ID.
- **Legacy-spelled IDs.** A turn recovered with a legacy `turn_<n>` spelling
  (`agent/session_client_mutation.go:456-463`) runs again under a fresh ID. The
  old turn stays as it was recorded.
- **Publishing the running turn.** The session publishes the running turn ID
  through `SetProcessingTurn` before the server marks it processing
  (`server/server.go:880-895`). This replaces the server's own reservation in
  `SetProcessing(true)` (`server/server.go:864-927`;
  `cmd/evener/serve.go:1709, 1729`).

**Turn kind.** The first entry of each turn persists a `TurnKind`:
- **`execution`**: a real run of the model.
- **`gap`**: standalone entries recorded between executions, such as hooks,
  model switch, environment and persisted notices. The session mints one gap turn
  for each run of such entries. This keeps today's grouping of a burst of
  announcements into one turn (`appwire_projection.go:2160-2198`). Startup
  entries use the prelude turn.
- **`delivery`**: an entry from a cold writer. Attention delivery, watch sends
  and shell repair have no session (`agent/session_attention.go:351-397`,
  `agent/delegate_delivery.go:106`, `agent/delegate_tree_watch.go:478`,
  `agent/delegate_shell_repair.go:109`). Each cold write is its own turn with a
  fresh ID.

  Today these STEERING entries have no owner and join the previous open group in
  the file projection (`internal/apptranscript/logical_turn.go:47-53, 121-122`).
  Live never shows them at all. Showing each delivery as its own turn is an
  intended, visible change for new-format sessions.

**Turn membership** comes from `TurnID`, not from entry-kind adjacency.

**Item key.** `transcriptKey = apptranscript-item-v2:<turnID>:<Seq>:<part>`.
- `Seq` is the entry that opens the item.
- `part` is the index of the entry content part the item comes from, counted
  over the entry's content parts, not over the items it projects. Hidden or
  omitted parts still occupy their index, so the same part always gets the same
  key.
- An item that spans entries keeps its opener's key. A tool call and its result
  are keyed by the ASSISTANT entry that holds the call. This also fixes
  `mergeAppThreadItems` keeping the incoming ID
  (`internal/apptranscript/logical_turn.go:314-316`) and makes a reused provider
  call ID harmless.
- Item `id` derives from the key and keeps today's kind prefixes.

**Position.** Every entry, legacy and new, uses `{entry: Seq + 1, item: part}`.
Header-derived prelude items use `{entry: 0}`.

**Format marker.** Every new entry carries an explicit format version, so the
projector never infers the format from which fields are present. `StableTurnID`
and `OwningTurnID` already appear on legacy steering entries
(`agent/schema/turn.go:233-251`).

**Legacy entries** (no format marker):
- They keep today's turn IDs (`turn_<entryIndex>`), adjacency grouping and
  inferred status: completed by default, failed or interrupted from their entries
  (`logical_turn.go:185-218`).
- Their positions move to the `Seq` scheme. Cursors go stale once, at the
  projection version bump.
- A resumed legacy session is self-consistent. New entries carry new identity,
  and legacy turns are never rewritten.

### Turn status

Status is derived from persisted facts:
- **Gap, delivery and prelude turns** are always complete.
- **An execution turn's status is its latest completion entry:** completed,
  failed or interrupted, as recorded. If entries of the same turn follow its
  latest completion, the turn is open again until the next completion. This
  covers a turn that recovery reclaims and re-runs under the same ID
  (`agent/session.go:530-548`; `agent/session_client_mutation.go:408-415,
  575-599`).
- **An execution turn with no completion** is open.
- **A legacy turn** keeps today's inferred status.

**On resume**, the session writes an interrupted completion for each new-format
execution turn it finds open, except a turn it is about to reclaim and re-run.

**Running state lives in the overlay.** A client shows an open turn as running
while the overlay says it is running, and as interrupted otherwise. That is a
display rule: it is never stored or merged as a fact. A daemonless past session
has an empty overlay, so a turn that a crash left open displays as interrupted,
which is what the hub shows today.

### The projector

The projector is one pure function over (entries, header). It has one
`ProjectionID`, shared by daemon and hub.

**Inputs that are not in the transcript stay out of it.**
- Cost is computed at egress from persisted usage and model. The model is
  recorded on each entry.
- File-backed output images are resolved at egress from persisted image
  references (`cmd/evener-hub/output_images.go`).
- Egress enrichments are not part of the versioned item. A client replaces them
  with what it received most recently.

**It also fixes places where the file projection loses what live shows today.**
- It projects `Thinking.Summary` when `Thinking.Text` is empty
  (`llm/providers/responses/response.go:75-94`,
  `internal/apptranscript/apptranscript.go:521-530`).
- It projects one agentMessage per assistant text run and one reasoning item per
  entry, matching how live displays them today.
- **Communicate.** A new presentational COMMUNICATE entry is recorded when
  `communicate` delivers its message. Every failure path in `communicate`
  (missing `end_turn`, empty message, abort) happens before delivery, so a
  recorded COMMUNICATE entry means the message was delivered
  (`agent/session_tools_communicate.go:73-77`).
  - The communicate item is projected from the COMMUNICATE entry, so it is in
    history at delivery, whether or not the round's TOOL_RESULTS is ever written
    (`agent/session_tools.go:1021-1090`).
  - The ASSISTANT entry's communicate call part is hidden, but it still occupies
    its part index.
  - No history item is ever removed.
- **Fold copies.** A fold re-appends entries after its markers
  (`agent/session_compaction.go:146-154`). The copies carry `OriginalSeq`, and
  they are neither projected nor announced.

**Incremental use.** For live notifications, the daemon keeps a projection state
for each thread:
- the open turn's accumulators (usage sums, earliest timestamp)
- the open round's call records (names and arguments, needed to emit a full tool
  item when TOOL_RESULTS lands, `apptranscript.go:598-603`)
- the round's last assistant text, for communicate echo suppression
  (`apptranscript.go:564-565`)

It is released at round end and turn end. When a turn reopens, or after a
projection failure, the state is rebuilt from the file, starting at the turn's
first entry through the index. The parity harness checks that incremental
projection equals whole-file projection.

### Live history notifications

After each recorded append, the server projects the entry with the incremental
state. It then emits `history/updated`, carrying the full current form and
version of every affected item and turn. This covers every history write,
including writes that emit nothing today:
- delegate attention and delivery to a live parent
  (`agent/session_attention.go:482-560`)
- shell attention (`agent/jobs.go:2155-2176`)
- watch sends (`agent/job_watch.go:4541`)
- retained attention turns (`agent/session_attention.go:1184-1207`)
- NOTES_CONTEXT and TURN_FAILURE, which today appear only in the file

Notifications are derived from recorded entries, so they never run ahead of the
file.

**When projecting or publishing fails** after an append has recorded:
1. The server bumps the thread's resync epoch.
2. It pushes `evener/thread/resync`, which already exists
   (`appwire/types.go:213, 2697-2700`), with the new epoch.
3. It rebuilds the projection state from the file.

A client that receives the resync, or that later reads and sees a newer epoch
than it holds, replaces that thread's history instead of merging. Every
subscriber recovers on its own, and the epoch never needs clearing.

### The live overlay

The overlay is per thread and in memory. It holds four kinds of state.

**Streams.**
- Each model response attempt has its own `streamID`, carrying `roundID` and an
  attempt number. `roundID` is one per model round, across retries and fallback
  groups.
- `overlay/reset(streamID)` discards one attempt when it is retried
  (`appwire_projection.go:549-575`). The next attempt has a new `streamID`, so
  its output is never mistaken for the discarded one.
- The ASSISTANT entry, or the salvage entry, written for the round records its
  `roundID`. When that entry is in history, all of the round's streams are
  covered and dropped, whichever attempt or fallback group
  (`agent/session_model_call.go:1215-1230, 1295-1306`) the recorded content came
  from. This covers `BestSalvage` picking an earlier group
  (`agent/session_events.go:692`).
- `overlay/end(roundID)` is sent at round end. A round that ended with nothing
  recorded collapses into one interrupted notice. That happens after a
  reasoning-only round (`agent/session_lifecycle.go:958-960`), a closing session,
  the content filter, or empty salvage (`agent/session_events.go:679-707`).
- A communicate preview is a stream item. It is covered by the COMMUNICATE entry
  when the message is delivered, and dropped by the round's `overlay/end` if the
  call failed.
- Stream content is bounded by the provider's maximum output for one response.

**Tool execution state.** Tools run after the ASSISTANT entry holding their
calls is recorded (`agent/session_model_call.go:994-996`).
- Running output, per-call completion (TOOL_CALL_END,
  `agent/session_tools.go:876`) and held images (`appwire_projection.go:847-868`)
  attach to the call's history key.
- The history item wins once its version includes the TOOL_RESULTS entry.
- Running output keeps the last 256 KB per call. Held images keep the existing
  per-result image limits.
- If TOOL_RESULTS is never recorded, `overlay/end` turns the execution state into
  an interrupted notice.

**Running state.** The running turn ID and the thread status.

**Ephemeral notices.**
- They cover `round_timings`, `prompt_loaded`, `plugin_loaded`,
  `loop_detection`, `context_compaction`, `fork_summary`, `warning`, and
  interrupted notices.
- Each thread keeps a ring of 50 `round_timings`, 50 of everything else, and
  64 KB in total. A daemon-wide cap of 16 MB evicts the oldest first.
- Each notice is anchored at `{entry: Seq + 1 of the preceding entry, item:
  1<<30, sub: n}`. The item value sits past every real part index, and `sub` is a
  new position component that orders notices among themselves.
- Notices survive a browser refresh while the daemon lives and vanish on
  restart, as they do today.
- A released delegate's ring is dropped with its runtime.

**Persisted instead of ephemeral.**
- These become presentational entries: `tool_repair`, `goal_ended`,
  `turn_limit` (unless an existing TURN_FAILURE covers it), and standalone
  `skill_activated`.
- Turn timing (`CompletedAt`, `DurationMS`) is persisted in the completion entry.
- Tool `DurationMS` is persisted in TOOL_RESULTS.

### New entry kinds stay out of model history

The new entry kinds are the completion entry, COMMUNICATE, and the
presentational entries. They are written to the transcript only. They never
enter the session's in-memory history, and resume skips them.

So these consumers never see them:
- the model wire builder
- orphan repair
- Responses continuation eligibility
- outline and find
- the model-facing transcript tool

Those consumers are `agent/session_model_call.go:1718-1726`,
`agent/history_repair.go:79-88`,
`agent/responses_continuation_eligibility.go:83-93`,
`agent/session_outline.go:187,368`, `agent/session_tools_find.go:483,548`, and
`agent/session_tools_transcript.go:1037-1047`.

File consumers learn to skip them:
- resume (`agent/transcript_read.go:162-190`)
- delegate fork context (`agent/delegate_fork_context.go:25,67`)
- `agent/session_init.go:2361`
- grouping (`logical_turn.go:30-35, 94-104`)
- the attention fold
- derived totals
- eval probes (`agent/eval_probes.go:128`)
- renderers

### Writer failure

The writer API returns either `recorded Seq` or `not recorded`. Today `Append`
and `AppendDurable` return nil with Seq 0 for both a missing and a closed writer
(`agent/transcript/transcript.go:606-627`).

`evener serve` always has a state directory (`cmd/evener/serve.go:527`). A served
session fails closed when its transcript cannot be created or its writer is
poisoned (`transcript.go:724-728`). Failing closed means:
- a running execution is interrupted
- `overlay/end` turns its streams and tool state into notices
- projection state is dropped
- the session stops accepting input
- the thread shows one visible diagnostic
- nothing accumulates

Turns held before the writer attaches (`agent/session.go:2195-2200`) are
announced when they are recorded at attach.

### Reads

`thread/read` is served by the daemon for a live session:
1. Inside `CaptureSubscription`, capture the thread's overlay, its resync epoch
   and the subscription cut.
2. After releasing the cut, project history up to the recorded length and merge
   it: history by version, and overlay items minus covered streams.

Notifications delivered after the response merge by the same rules. A later
`history/updated` at a lower or equal version is ignored, and so are overlay
updates for a covered or reset stream. So reading the file ahead of the stream
cannot produce a gap, a duplicate or a regression.

Nothing announced before the cut is missing from the read. An announced entry is
already recorded, and the recorded length is published before the entry is
announced.

`thread/turns/list` pages come from the file, and the same merge applies to the
open turn's page. A thread with no runtime has an empty overlay. The hub serves a
daemonless session from the file as an authoritative replacement.

### Index

The index is a seekable on-disk file of fixed-size records sorted by position.
Each record holds the position, the key, the turn, and the entry's byte offset
and length. A window read binary-searches the records with `ReadAt` and projects
only the entries it needs. There is no full decode per read.

This replaces the JSON index that today is unmarshalled whole or held in a cache
(`internal/apptranscript/turn_index.go:91-119, 551-553`;
`server/appwire_runtime.go:80`). It also replaces the per-group rank arithmetic
in `item_paging.go:176-234`.

**Validation.** An append is validated by file identity, recorded length and
trailing bytes, the check proven by the attention fold cursor (PR #2254). The
full prefix rehash (`turn_index.go:571-584`) goes away.

**Memory.** A bounded cache of 64 open index handles plus their headers is the
only index memory.

### Fork

`thread/fork` names its source by item `transcriptKey`, and the server resolves
it to the entry. This works for legacy and new items alike. It replaces the
1-based entry index in `sourceTurnId` (`appwire/types.go:1706-1715`; hub parser
`cmd/evener-hub/app_threadlifecycle.go:1558-1568`; `agent/fork.go:61-69`).

### Protocol and clients

The protocol gains:
- `history/updated`
- versions on items and turns
- overlay streams with `roundID`, reset and end
- the position `sub`
- server-side steering identity
- resync epochs
- replace-on-new-incarnation and replace-on-authoritative-read
- fork by key

These bump the AppWire protocol major version. A client announcing an older
version gets an explicit "upgrade required" error. The web client ships with the
hub, but the mobile app must update.

The shared reducer (`appwire-client/typescript/reducer.ts`) implements:
- merging by version
- stream coverage, reset and end
- anchors
- the display rule for open turns
- resync replacement
- no client-minted steering IDs

### Schema compatibility

Transcript entries decode strictly (`agent/transcript/transcript.go:283-307`), so
an older hub cannot read a transcript containing new fields. All new entry
fields therefore ship in one release (phase 2):
- the format marker
- `TurnID` and `TurnKind`
- `roundID`
- `OriginalSeq`
- the per-entry model
- the completion, COMMUNICATE and presentational entries
- timing fields

The version skew then happens once. The release notes tell users to restart the
hub along with the daemon.

### Out of scope

- the client-mutation journal
- the notifier replay ring (count-bounded; no production reader)
- the task store
- the projector's small `delegates` map

## Acceptance criteria

**Memory.** Measure on a long-lived daemon running a copy of the 260-delegate
coordinator session, after replaying its activity rather than after a cold
restart:
- Retained history memory is 0, beyond the projection state (the open turn's
  accumulators plus the open round's call records) and the index handle cache
  (64 handles).
- Ephemeral notices stay within 64 KB per thread and 16 MB daemon-wide.
- For comparison, the same measurement is about 283 MB today.

**Latency.** `thread/read` of the latest window at the default page size, on the
95 MB root transcript, on the dev machine's local SSD, 200 samples:
- idle writer: p99 under 50 ms, and no worse than 2× today's in-memory read
- appending one entry per 100 ms: p99 under 100 ms

This is prototyped and measured in phase 1, before any irreversible step.

**Parity.** The parity harness reports zero divergences for new-format sessions:
- live against reload, including across a daemon restart and a reclaimed turn
- incremental projection against whole-file projection

## Migration

Each phase ships on its own and keeps main green.

1. **Parity harness and latency gate.**
   - A differential test drives a scripted multi-round session and compares the
     live view with the file projection, item by item. The session covers:
     - user input
     - assistant text and reasoning
     - tool calls and results with images
     - communicate
     - steering and hooks
     - model switch and compaction
     - failure and retry
     - goal continuation
     - delegate attention
     - a restart
   - It passes by listing known divergences in a table, each tagged with the
     phase that removes it. It fails on a new divergence, and on a listed
     divergence that no longer occurs.
   - A prototype of the seekable index measures file-window reads on a copy of
     the 95 MB root transcript against the latency criteria. If the criteria are
     not met, stop and redesign the index before phase 2.
2. **Write the new fields.**
   - Writers write every new field and entry kind. The consumers listed above
     skip the new kinds, and the new kinds stay out of in-memory history. Every
     projection rule and client-visible behavior stays as it is today.
   - The registry, `Seq` on rollback, the explicit write result and fork
     sequence seeding also land here.
   - The one intended behavior change in this phase: a served session fails
     closed on writer failure.
   - Each consumer is its own small task, with a test showing that a transcript
     containing the new kinds resumes, projects, repairs and renders exactly as
     one without them.
3. **Activation and read switch.** One PR, built as a stack of reviewable
   commits, switches everything together:
   - the projector's new rules, positions and incremental state
   - the seekable index
   - live history from projected entries
   - the overlay
   - status from persisted facts
   - `history/updated`
   - resync epochs
   - server-side steering identity
   - fork by key
   - the protocol major bump
   - the reducer
   - reads as described
   - deletion of snapshot history, delegate snapshots, the prepared cache and the
     cut I/O rule

   It ships as one unit, because the file side, the live side and the read path
   must switch together. Keeping snapshot reads in between would need a
   throwaway versioned snapshot. The acceptance criteria are measured before
   merge.
4. **Cleanup and docs.** Remove dead code and tests. Amend the atomic paging spec
   and `docs/appwire-protocol.md`.

Each phase gets its own implementation plan once this design is accepted and the
previous phase has landed.

## Risks

- **The mobile app must update** after phase 3.
- **Old cursors and stored anchors** go stale or miss once.
- **Read latency moves to disk.** The phase 1 gate bounds it before anything
  irreversible ships.
- **Phase 3 is large.** It stays reviewable as a stack of commits, and the parity
  harness and the acceptance criteria gate it.
- **Delivery turns** display attention deliveries as their own turns, which
  changes how delegate-attention sessions look.
