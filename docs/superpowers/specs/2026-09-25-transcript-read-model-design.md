# Transcript as the Only History Read Model

Status: proposed, revision 3 (2026-09-25). Supersedes the eviction approach in
the closed PR #2251.

- Revision 2 replaced revision 1's sequence watermark with idempotent merges.
- Revision 3 stops minting history identity before an entry exists. History is
  now only the projection of persisted entries, and everything not yet durable
  is a separate live overlay. Design review showed pre-persist identity cannot
  be minted reliably (no stream part identity, repeating round numbers,
  two-stage tool completion, rolled-back writes).

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
session. This also breaks the governing spec
(`docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`:
"Transcript key: stable item identity reproduced by live event projection and
later file projection"). Restarting a daemon already changes what clients see.

## End state

1. **History is the projection of persisted entries, and nothing else.** One
   projector, shared by daemon and hub, turns transcript entries into turns and
   items. Live history notifications are produced by running that projector on
   each entry after it is written. A live history item and its reloaded form are
   therefore the same by construction.
2. **Everything not yet durable is a live overlay.** Streaming assistant text
   and reasoning, running tool state, and ephemeral notices live in memory,
   separate from history, and never claim history identity.
3. **Merges are versioned.** Every history item and turn carries a version: the
   highest `Seq` of the entries that contributed to it. A client or server keeps
   the higher version. Applying the same fact twice, or a file read that is
   ahead of the live stream, changes nothing.
4. **Memory holds only the overlay.** An item leaves memory once the entry that
   covers it is written. Memory per thread no longer depends on its history.

What is deleted: the per-thread turn snapshot's history, delegate snapshots and
seeding, prelude shifting, incarnation rotation on prelude, the projector's ID
counters, client-minted steering IDs, the server's separate prepared-transcript
cache, and the "no file I/O inside the cut" rule.

## Design

### Persisted identity

- **Turn IDs.** A new dedicated `TurnID` field is written on every entry that
  belongs to a turn. `StableTurnID` keeps its current meaning; on steering entries
  it is the steer mutation's own ID and is never a turn ID
  (`internal/appprojector/appwire_projection.go:972-979`).
  - The session mints turn IDs as `t_<ulid>`, a namespace disjoint from legacy
    `turn_<n>` IDs, so the two can never collide (the collision
    `SeedPersistedTurns` guards against, `appwire_projection.go:177-197`).
  - The session publishes the running turn ID before the server marks it
    processing. This replaces the server's own reservation in
    `SetProcessing(true)` (`server/server.go:864-927`; wakes and continuations
    at `cmd/evener/serve.go:1709, 1729`).
  - **Gap turns.** Standalone entries recorded between real turns (hooks, model
    switch, environment, persisted notices) share one gap-turn ID, minted by the
    session for that run of entries. This keeps today's grouping of a burst of
    announcements into one turn (`appwire_projection.go:2160-2198`).
  - **Cold writers** have no session: attention delivery, watch sends and shell
    repair (`agent/session_attention.go:351-397`, `agent/delegate_delivery.go:106`,
    `agent/delegate_tree_watch.go:478`, `agent/delegate_shell_repair.go:109`).
    They mint a fresh turn ID per entry, so each cold write is its own turn,
    which is how they project today.
- **Turn membership** comes from `TurnID`, not from entry-kind adjacency. A
  mid-turn HOOK_COMPLETED, MODEL_SWITCH, CHECKPOINT or SUMMARY no longer splits
  a turn.
- **Item key.** `transcriptKey = apptranscript-item-v2:<turnID>:<Seq>:<part>`,
  where `Seq` is the entry that opens the item and `part` is the item's index in
  that entry's projection. Items that span entries keep the opener's key: a tool
  call and its result are keyed by the ASSISTANT entry that holds the call. This
  also fixes `mergeAppThreadItems` keeping the incoming ID
  (`internal/apptranscript/logical_turn.go:314-316`) and makes a reused provider
  call ID harmless. Item `id` derives from the key and keeps today's kind
  prefixes (`item_tool_`, `item_tool_result_`, …).
- **Position.** `{entry: Seq + 1, item: part}` for every entry, legacy and new,
  and `{entry: 0}` for header-derived prelude items. Positions are unique and
  ordered, but not dense.
- **Legacy entries.** Entries written before this change have no format marker
  and no `TurnID`.
  - Their turn IDs keep the current fallback (`turn_<entryIndex>`, adjacency
    grouping).
  - Their positions move to the `Seq` scheme. Cursors go stale once, at the
    projection version bump.
  - A resumed legacy session is self-consistent: new entries carry new identity.
- **Format marker.** Every new entry carries an explicit format version, so the
  projector never infers format from which fields are present. `StableTurnID`
  and `OwningTurnID` already appear on legacy steering entries
  (`agent/schema/turn.go:233-251`, `agent/session_queue.go:1267-1277`).

### The projector

One pure function projects a prefix of entries (plus the header) into turns and
items. It has one `ProjectionID` shared by daemon and hub. The hub-only
post-stamps (`cmd/evener-hub/app_threadread.go:221-226`: image URLs, cost,
file-backed output images, derived totals) move into it.

It also fixes the places where the file projection loses or mangles what live
shows today:
- It projects `Thinking.Summary` when `Thinking.Text` is empty. Otherwise OpenAI
  reasoning summaries would disappear
  (`llm/providers/responses/response.go:75-94`,
  `internal/apptranscript/apptranscript.go:521-530`).
- It hides a communicate call whose result failed, matching live's reset
  (`internal/appprojector/appwire_projection.go:753-760` versus
  `apptranscript.go:564-575, 606-609`).
- It projects one agentMessage per assistant text run and one reasoning item per
  entry, matching how live displays them today, instead of one item per content
  part.
- It skips fold copies. A fold re-appends entries after its markers
  (`agent/session_compaction.go:146-154`); the copies now carry `OriginalSeq`.
  They are neither projected nor announced.

A turn's status comes only from persisted facts, never from the runtime:
- A turn with a completion entry is completed, failed or interrupted, as
  recorded.
- Any other turn is open.
- Gap and prelude turns are not executions and are always complete.
- On resume, the session writes an interrupted completion for any turn it finds
  open, so a crash cannot leave a turn open forever once the session runs again.
- Whether an open turn is currently running is overlay state (below).

### Live history notifications

After every successful append, the session hands the entry to the server, which
runs the projector on it and emits `history/updated` notifications. Each one
carries the full current form of every affected item and turn, with its version.
This covers every history write:
- the writes that emit nothing today: delegate attention and delivery to a live
  parent (`agent/session_attention.go:482-560`), shell attention
  (`agent/jobs.go:2155-2176`), watch sends (`agent/job_watch.go:4541`), retained
  attention turns (`agent/session_attention.go:1184-1207`);
- file-only items: NOTES_CONTEXT and TURN_FAILURE.

Items that span entries are re-emitted at the higher version when a later entry
contributes. A tool item gets version `Seq(TOOL_RESULTS)` when its results land.

Because notifications are derived from persisted entries, they can never run
ahead of the file. There is no persist-before-emit ordering to maintain per item
kind.

### The live overlay

The overlay is per thread and in memory. It holds:
- **Streams.** A streaming assistant text or reasoning item has an overlay ID and
  a `streamID`, one per model response, minted by the session. The ASSISTANT
  entry written for that response records its `streamID`. When that entry's
  history notification exists, whether live or in a file read, the overlay items
  with that `streamID` are covered and dropped. Clients apply the same rule.
- **Tool execution state.** Tools run after the ASSISTANT entry holding their
  calls is written (`agent/session_model_call.go:994-996`), so running output,
  per-call completion (TOOL_CALL_END, `agent/session_tools.go:876`) and held
  images (`appwire_projection.go:847-868`) attach to the call's history key. The
  history item wins once its version includes the TOOL_RESULTS entry.
- **Running state.** The running turn ID and the thread status.
- **Ephemeral notices.** `round_timings`, `prompt_loaded`, `plugin_loaded`,
  `loop_detection`, `context_compaction`, `fork_summary` and `warning` are kept
  in a per-thread ring bounded per kind: 50 `round_timings`, 50 of everything
  else, and 64 KB total per thread.
  - A daemon-wide cap of 16 MB evicts oldest-first across threads.
  - Each notice has an anchor, `{entry: Seq + 1 of the preceding entry, item:
    part count of that entry, sub: n}`. The new `sub` component, absent on
    history items, orders a notice after the entry it followed.
  - Notices survive a browser refresh while the daemon lives and vanish on
    restart, as today.
  - A released delegate's ring is dropped with its runtime. That is an accepted
    loss: they are notices, not history.
- **Content that never persists.** Streamed content from a round that is never
  written (`agent/session_events.go:679-707`: closing session, content filter,
  empty salvage) becomes an interrupted ephemeral notice. Salvage that is
  written records the same `streamID`, so it covers its stream and is not shown
  twice.

`tool_repair`, `goal_ended`, `turn_limit` (unless an existing TURN_FAILURE
covers it) and standalone `skill_activated` are persisted as presentational
entries instead of being ephemeral. Turn timing (`CompletedAt`, `DurationMS`)
and tool `DurationMS` are persisted in the completion entry and the TOOL_RESULTS
entry. Cost is recomputed from persisted usage.

### Reads

`thread/read` works in two steps:
1. Inside `CaptureSubscription`, capture the thread's overlay and the
   subscription cut.
2. After releasing the cut, project history from the transcript, then merge:
   history by version, and overlay items minus those covered by a `streamID`
   present in history.

Notifications delivered after the response merge by the same rules, so reading
the file ahead of the stream cannot cause a gap, a duplicate or a regression:
- A later `history/updated` at a lower or equal version is ignored.
- Overlay updates for a covered stream are ignored.

**Bounded to durable data.** Reads use the durable length the writer publishes
after each successful synced append, never raw end of file. So a write that is
later rolled back (`agent/transcript/transcript.go:666-683, 888-911`) is never
read. Every writer in the process publishes into one per-file registry, which
covers cold writers too. With no writer in the process, a read goes to end of
file and drops an incomplete last line, as today.

`thread/turns/list` pages come from the file, with the same merge applied to the
open turn's page. A thread with no runtime has an empty overlay and is read the
same way.

**Write results are explicit.** The writer API returns `recorded Seq` or
`not recorded`. Today `Append`/`AppendDurable` return nil with Seq 0 for both a
missing and a closed writer (`agent/transcript/transcript.go:606-627`).
- A missing, closed or poisoned writer yields `not recorded`. The item stays in
  the overlay as a notice, and the thread shows a visible "history not saved"
  diagnostic. Persist-failure notices are never evicted.
- Turns held before the writer attaches (`agent/session.go:2195-2200`) are
  announced when they are written at attach.

### Index

The on-disk item index is keyed by position and serves windows by searching
positions. This replaces today's per-group dense rank arithmetic
(`internal/apptranscript/item_paging.go:176-234`).

Append validation changes too. Today every read after an append rehashes the
whole prefix (`internal/apptranscript/turn_index.go:571-584`). Instead, the
index is checked by file identity, durable length and trailing bytes, the same
check the attention fold cursor uses (PR #2254). The index is bounded by the
same durable length as reads.

### Fork

`thread/fork` names its source by item `transcriptKey`, and the server resolves
it to the entry. This works for legacy and new items alike. It replaces the
1-based entry index in `sourceTurnId` (`appwire/types.go:1706-1715`; hub parser
at `cmd/evener-hub/app_threadlifecycle.go:1558-1568`; fork lookup at
`agent/fork.go:61-69`).

### Protocol and clients

The switch changes the protocol:
- `history/updated`
- versions on items and turns
- overlay `streamID` and `sub`
- server-side steering identity
- fork by key

So it bumps the AppWire protocol major version. A client that announces an
older version gets an explicit "upgrade required" error instead of a degraded
session. Its reducer could not apply versioned merges or stream coverage. The web
client ships with the hub, so it is always current. The mobile app must update.
The shared reducer (`appwire-client/typescript/reducer.ts`) implements:
- merge by version
- stream coverage
- anchors with `sub`
- no client-minted steering IDs

### Schema compatibility

Transcript entries decode strictly (`agent/transcript/transcript.go:283-307`),
so an older hub cannot read a transcript that contains new fields. All the new
entry fields ship in one release (phase 2):
- the format marker
- `TurnID`
- `streamID`
- `OriginalSeq`
- the completion entry
- presentational entries
- timing fields

The version skew therefore happens once. The release notes say to restart the
hub along with the daemon.

### Out of scope

- the client-mutation journal
- the notifier replay ring (count-bounded, with no production reader)
- the task store
- the projector's small `delegates` map

## Acceptance criteria

- **Memory.** Measured on a long-lived daemon running a copy of the
  260-delegate coordinator session after replaying its activity (not after a
  cold restart):
  - Retained memory for history (heap attributable to turn projection after GC)
    is 0.
  - Overlay memory is within its caps: 64 KB per thread and 16 MB daemon-wide.
  - Today the same measurement is about 283 MB (root 83 MB plus delegates
    200 MB).
- **Latency.** `thread/read` of the latest window (the default page size) on the
  95 MB root transcript with a warm index, on the dev machine's local SSD:
  - 200 samples, idle writer: p99 under 50 ms and no worse than 2× today's
    in-memory read.
  - A second run while the session appends one entry per 100 ms: p99 under
    100 ms.
- **Parity.** The parity harness reports zero divergences for sessions written
  in the new format, live against reload, including across a daemon restart.

## Migration

Each phase ships on its own and keeps main green.

1. **Parity harness.** A differential test drives a scripted multi-round session
   and compares the live view with the file projection, item by item. The
   session covers:
   - user input, assistant text and reasoning
   - tool calls and results with images
   - steering and hooks
   - model switch and compaction
   - failure and retry, and goal continuation
   - delegate attention
   - a restart

   It passes. Known divergences are listed in an explicit table, each tagged
   with the phase that removes it. The test fails on a new divergence, and on a
   listed divergence that no longer occurs.
2. **Write the new fields (no behavior change).** Writers write every new field;
   readers accept them and ignore them. This is split into small tasks: schema
   and decoding; `TurnID`, gap and prelude turn IDs, and cold-writer turn IDs;
   `streamID`; `OriginalSeq`; completion and timing entries; presentational
   entries; the explicit write result; and the durable-length registry. Every
   projection rule and every client-visible behavior stays as it is today.
3. **Activation.** One PR, built as a stack of reviewable commits, that switches
   everything together:
   - the shared projector's new rules and positions
   - the position-keyed index with tail validation
   - live history from projected entries
   - the overlay
   - status from persisted facts, with interrupted completions written on resume
   - `history/updated` with versions
   - server-side steering identity
   - fork by key
   - the protocol major bump
   - the reducer changes
   - projection version 2

   It ships as one unit because the file side and the live side must switch
   together. Neither half is correct alone.
4. **Read switch.** Reads work as described above. Delete snapshot history,
   delegate snapshots, the prepared cache and the cut I/O rule. Measure the
   acceptance criteria.
5. **Cleanup and docs.** Remove dead code and tests. Amend the atomic paging
   spec and `docs/appwire-protocol.md`.

Phases 1–2 are planned in detail in
`docs/superpowers/plans/2026-09-25-transcript-read-model-phase1-2.md`. Phases
3–5 each get their own plan once the previous phase has landed.

## Risks

- **Mobile must update.** After phase 3 an old mobile build gets "upgrade
  required".
- **Old cursors and stored anchors.** Old cursors go stale once, and clients
  re-read. A stored web "seen" watermark or mobile reader anchor misses once and
  falls back.
- **Read latency moves to disk.** The acceptance criteria bound it.
- **Legacy sessions keep fallback turn IDs** for their old prefix.
- **Phase 3 is large.** It is kept reviewable as a stack of commits, and the
  parity harness gates it.
