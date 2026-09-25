# Transcript as the Only History Read Model

Status: proposed, revision 2 (2026-09-25). Supersedes the eviction approach in
the closed PR #2251. Revision 2 replaces revision 1's sequence watermark with
idempotent, monotonic merges after a design review showed the watermark could
not give a gap-free, duplicate-free boundary.

## Problem

`evener serve` keeps a full projected copy of every turn it has ever shown:
`Server.appTurns` for the root thread and one `appTurnSnapshot` per delegate in
`Server.appDescendants` (`server/server.go:317-349`,
`server/appwire_turns.go:126-152`). The copy holds item text, tool output,
`Raw` tool state and images, costs about 1.1× the transcript it mirrors, and is
never released while the root identity lives. Measured on real sessions: 83 MB
for a 75 MB root transcript, and 200 MB for 260 finished delegates.

The copy exists because the live view and the transcript disagree about
identity. A live item and its persisted form have different turn IDs, item
IDs, positions and turn boundaries, so the transcript cannot answer a read for
a live session. Attempts to bound the copy by evicting and rebuilding it from
the transcript have to paper over that disagreement.

The code also breaks the governing spec
(`docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`:
"Transcript key: stable item identity reproduced by live event projection and
later file projection"). Restarting a daemon already changes what clients see.

## End state

The transcript is the only history. The daemon keeps only state that is not yet
durable.

1. **One identity.** Every history item has one key, fixed when the item is
   first announced and persisted with it. Live notifications, a file
   projection, and a restart all produce the same `id` and `transcriptKey` for
   the same item.
2. **Monotonic, idempotent merges.** Every history notification is an upsert by
   key. An item moves one way, from streaming to completed; nothing regresses a
   completed item. Applying the same fact twice, or applying a file read that
   is ahead of the live stream, changes nothing.
3. **One read path.** History is read from the transcript through the on-disk
   item index, with one projector shared by daemon and hub.
4. **Memory holds only non-durable state.** Per thread: items still streaming,
   a bounded buffer of ephemeral items, and small metadata. An item leaves
   memory as soon as its entry is persisted, even mid-turn.

Consequences: memory per thread no longer depends on its history; restart
changes nothing a client sees for sessions written in the new format; delegate
snapshots, seeding, prelude shifting, incarnation rotation on prelude, and the
"no file I/O inside the cut" rule all disappear.

## Design

### Identity

Identity and ordering are separate.

- **Turn IDs.** The session mints every turn ID and persists it in a new
  dedicated `TurnID` field on every entry that belongs to a turn. The projector
  adopts it and never mints one; `nextTurn`, `SeedPersistedTurns` and the
  projector's reservation counter are deleted. `StableTurnID` keeps its current
  meaning (on steering entries it is the steer mutation's own ID and must not be
  adopted as a turn, `internal/appprojector/appwire_projection.go:972-979`).
  - The server's early reservation in `SetProcessing(true)`
    (`server/server.go:864-927`, used by notification wakes and continuations,
    `cmd/evener/serve.go:1709, 1729`) is replaced by the session minting the
    running turn ID and publishing it before the server marks the session
    processing.
  - **Gap turns.** Standalone entries recorded between real turns (hooks, model
    switch, environment, persisted announcements) carry a shared gap-turn ID the
    session mints for the run of entries between two real turns, preserving
    today's "fold a burst of announcements into one turn" behavior
    (`internal/appprojector/appwire_projection.go:2160-2198`). Startup entries
    use the prelude turn ID.
- **Turn membership is explicit.** `TurnID` on every in-turn entry replaces
  grouping by entry-kind adjacency, so a mid-turn HOOK_COMPLETED, MODEL_SWITCH,
  CHECKPOINT or SUMMARY no longer splits a turn in the file.
- **Item keys.** `transcriptKey = apptranscript-item-v2:<turnID>:<itemID>`.
  `itemID` is minted by the session when the item is first announced and is
  persisted in the entry:
  - assistant text and reasoning: `<round>.<part>` (round ordinal within the
    turn, part index within the round), persisted on the ASSISTANT entry;
  - tool call and its result: `tool.<callID>`, carried by both the ASSISTANT
    entry and the TOOL_RESULTS entry, so the file projection merges them into
    one item keyed by the call (fixing `mergeAppThreadItems` keeping the
    incoming ID, `internal/apptranscript/logical_turn.go:314-316`);
  - every other item: minted at announcement, persisted on its entry.

  Item `id` derives from the key and keeps today's kind prefixes (`item_tool_`,
  `item_tool_result_`, …) so client tool folding is unaffected.
- **Ordering.** `position = {entry: Seq, item: part}` is assigned when the item's
  entry is persisted, using the writer's own `Seq` (no reservation; the writer
  keeps assigning `Seq` under its lock, `agent/transcript/transcript.go:635-664`).
  A streaming item has no position; clients order it after all positioned items
  of its turn, which is where it is. Positions are ordered and unique but not
  dense.
- **Per-part items.** Live emits one agentMessage per text part and
  communicate call and one reasoning item per thinking part, matching the file.
  Where live text and persisted text differ today (reasoning summaries vs
  thinking parts, communicate echo suppression, private-evidence tool results
  shown live but persisted as a placeholder, `agent/session_tools.go:1054-1057`)
  the persisted form is authoritative, and the completion upsert replaces the
  streamed text. A client therefore sees the persisted text once the item
  completes, both live and on reload.
- **Format marker.** Entries written in the new format carry an explicit format
  version. The projection uses the new rules only for such entries and keeps the
  current rules (`turn_<entryIndex>`, adjacency grouping, dense positions) for
  older entries, so a resumed legacy session is self-consistent: its legacy
  prefix is always read from the file with legacy identity, and new entries get
  new identity.

### Notifications and merge rules

- **Every history write is announced.** Every entry the session persists emits
  a history notification after the write succeeds, including the writes that
  today emit nothing: delegate attention and delivery steering to a live parent
  (`agent/session_attention.go:482-560`), background-shell attention
  (`agent/jobs.go:2155-2176`), watch sends (`agent/job_watch.go:4541`),
  retained attention turns (`agent/session_attention.go:1184-1207`), and
  NOTES_CONTEXT and TURN_FAILURE items that today exist only in the file.
- **Steering gets server-side identity.** `evener/steering/injected` carries the
  item's `id`, `transcriptKey` and turn ID. The client stops minting
  `item_steering_live_*` IDs (`appwire-client/typescript/reducer.ts:3264-3267`).
- **Upserts, not appends.** `item/started`, `item/completed` and steering are
  upserts by key. Deltas apply only to an item in the streaming state.
- **Monotonic.** A completed item is immutable: `item/started` for an existing
  completed item and deltas for a completed item are ignored. A completion
  replaces the streamed content with the persisted content. Turn status is
  monotonic the same way (completed or interrupted beats in progress).
- **Where emit precedes persist.** Streamed items (assistant text, reasoning,
  tool output, and per-call tool completion) are announced before their entry
  exists; they are streaming items held in memory. When the entry persists, a
  completion upsert carries the final content and the position. Per-call tool
  completion stays per call (TOOL_CALL_END), since TOOL_RESULTS is written once
  per round after every call (`agent/session_tools.go:876`, `:1052-1071`); the
  item stays in memory until TOOL_RESULTS persists.
- **When an item leaves memory.** The server drops a streaming item from its
  in-memory state when it commits that item's completion upsert (inside
  `CommitProjection`), not when the agent writes the entry. At any cut, an item
  is therefore either still in memory or its completion has already been
  committed, which requires its entry to be on disk.
- The three emit-before-persist sites for non-streamed history items flip to
  persist first: goal continuation (`agent/session_lifecycle.go:2804/2808`),
  HOOK_COMPLETED (`agent/session_events.go:329/346`), TURN_FAILURE
  (`:266/313`).

### Reads

- `thread/read` captures, inside `CaptureSubscription`, the thread's in-memory
  state (streaming items, ephemeral buffer, turn metadata) and the subscription
  cut. After releasing the cut it reads history from the transcript to its
  current end and merges the two with the monotonic rules.
- Reading the file after the cut can see entries whose notifications are
  delivered after the response. Because those notifications are idempotent
  upserts and never regress a completed item, applying them again changes
  nothing. Nothing announced before the cut is missing from the file, because
  every non-streamed history item is persisted before it is announced, and
  every streamed item is in the captured memory state until its entry persists.
  This replaces the "no file I/O inside the cut" rule and the test that pins it
  (`server/appwire_rejoin_test.go:309-418`), which guarded against duplicates
  that non-idempotent live identity caused.
- The projection stamps a turn completed only when its completion is persisted
  (see Timing). A turn without one is in progress while the thread's runtime
  runs it, and interrupted otherwise. Legacy groups keep today's completed
  status.
- `thread/turns/list` pages are read from the file; the same merge applies to
  the open turn's page.
- A thread with no runtime (a released delegate, a daemonless session) is read
  the same way; its memory state is empty. Writes by cold writers are visible on
  the next read. No attach/release signal is needed.

### Ephemeral items

Items that are not persisted (`round_timings`, `prompt_loaded`,
`plugin_loaded`, `loop_detection`, `context_compaction`, `fork_summary`,
`warning`) live in a per-thread ring buffer of the last 200 ephemeral items. Each
carries an anchor position (`{entry: Seq of the preceding persisted entry, item:
sub-index}`) so it keeps its place in the turn across a refresh or page merge
instead of sorting to the end (`reducer.ts:969-984`). They survive a browser
refresh while the daemon lives and disappear on restart, which is today's
behavior. Streamed content from a round that never persists
(`agent/session_events.go:679-707`: closing session, content filter, empty
salvage) moves into this buffer marked interrupted; salvage that does persist
keeps the item's minted ID, so it is not shown twice.

Persisted as presentational entries instead: `tool_repair`, `goal_ended`,
`turn_limit` (unless an existing TURN_FAILURE covers it), standalone
`skill_activated`.

### Timing

Turn `CompletedAt` and `DurationMS`, and tool `DurationMS`, are persisted: a
turn completion is written as its own entry (carrying the turn ID and timing)
when the turn ends. Cost is recomputed from persisted usage.

### Persist failures

If the transcript writer is missing or poisoned (`agent/session_init.go:607-614`,
`agent/transcript/transcript.go:612-632`), history cannot be durable. Items that
fail to persist are emitted into the ephemeral buffer, and the thread carries a
visible "history not saved" diagnostic, so clients see the same thing live and
on refresh while the daemon lives. Turns recorded before the writer attaches
(`agent/session.go:2195-2200`) are announced when they persist at attach.

### Compaction and fork

- The file stays append-only. A fold's re-appended copies of entries
  (`agent/session_compaction.go:146-154`) carry an `OriginalSeq` field; the
  projection skips a copy whose original is present. `Seq` stays unique.
- Fork's `sourceTurnId` (a 1-based entry index today, `appwire/types.go:1706-1715`,
  parsed by the hub at `cmd/evener-hub/app_threadlifecycle.go:1558-1568`)
  becomes a `Seq` resolved to the original entry. This is a protocol change,
  made in the identity phase with its clients and docs.

### Index

The on-disk item index is keyed by `(Seq, part)` and serves windows by
searching positions, instead of today's per-group dense rank arithmetic
(`internal/apptranscript/item_paging.go:176-234`). Append validation changes
from a full prefix rehash on each read after an append
(`internal/apptranscript/turn_index.go:571-584`) to the identity, size and
trailing-bytes check proven by the attention fold cursor (PR #2254). Daemon and
hub share one projector and `ProjectionID`, and the hub-only post-stamps
(`cmd/evener-hub/app_threadread.go:221-226`) move into a shared read helper, so
neither side's read rebuilds the other's index.

### Schema compatibility

Transcript entries decode strictly (`agent/transcript/transcript.go:283-307`);
an older hub cannot read a transcript containing new fields. All new entry
fields (format marker, `TurnID`, item IDs, `OriginalSeq`, the turn completion
entry, presentational entries) land in one release (phase 2), so the version
skew happens once. The release notes tell users to restart the hub with the
daemon.

### What is deleted

- `appTurnSnapshot`'s history: `Seed`, history turns, `itemPositions`,
  `turnIndex`, `turnEntries`, the paging index, prelude insert and position
  shifting, incarnation rotation on prelude (roughly 350–450 production lines).
- `appDescendants[*].turns` and delegate seeding.
- Projector counters, the `SetProcessing` reservation, and client-minted
  steering IDs.
- The server's separate prepared-transcript cache and projector.

### Out of scope

The client-mutation journal, the notifier replay ring (count-bounded, no
production reader), the task store, and the projector's small `delegates` map.

## Acceptance criteria

- **Memory.** On a resumed copy of the 260-delegate coordinator session, the
  daemon's retained turn-history memory (heap attributable to turn projection
  after GC) is under 5 MB at idle, down from about 283 MB (root 83 MB plus
  delegates 200 MB). During a long turn it is bounded by the items still
  streaming plus the ephemeral buffer.
- **Latency.** `thread/read` with turns on the 95 MB root, warm index: p99 no
  worse than 2× today's in-memory read, and under 50 ms. Measured before phase
  5 and after.
- **Parity.** The parity harness reports zero divergences for sessions written
  in the new format, including across a daemon restart.

## Migration

Each phase ships on its own and keeps main green.

1. **Parity harness.** A differential test driving a scripted multi-round
   session (user input, assistant text, reasoning, tool calls and results with
   images, steering, hooks, model switch, compaction, failure and retry, goal
   continuation, delegate attention, a restart) and comparing the live view with
   the file projection item by item. It passes: known divergences are listed in
   an explicit table, each tagged with the phase that removes it; the test fails
   on a new divergence and on a listed divergence that no longer occurs, so
   fixes must update the table.
2. **Schema and writer (file side).** All new entry fields in one release:
   format marker, `TurnID` (including gap and prelude turn IDs), item IDs,
   `OriginalSeq`, the turn completion entry, presentational entries. Writers
   write them; the file projection uses them for new-format entries. Flip the
   three emit-before-persist sites. Announce every history write. Fixes today's
   restart divergences for new sessions.
3. **Live identity and merges.** The live projector adopts persisted identity;
   notifications carry keys; steering identity; completion upserts carry
   position and persisted content; monotonic merge rules in the shared client
   reducer; the ephemeral buffer with anchors; the session mints the running
   turn ID; fork by `Seq`; projection version 2 (old cursors go stale, clients
   re-read).
4. **Index and shared projection.** Position-keyed index, tail validation, one
   projector for hub and daemon.
5. **Read switch.** Reads as described; delete snapshot history, delegate
   snapshots, the prepared cache, and the cut I/O rule.
6. **Cleanup and docs.** Remove dead code and tests; amend the atomic paging
   spec and `docs/appwire-protocol.md`.

Phases 1–2 are planned in detail in
`docs/superpowers/plans/2026-09-25-transcript-read-model-phase1-2.md`. Phases
3–6 each get their own plan when the previous phase has landed.

## Risks

- **Old clients.** An old client does not apply the monotonic rules or
  server-side steering identity. The protocol version bump in phase 3 makes old
  clients re-read; the web and mobile clients ship the reducer change in the
  same phase.
- **Client-visible ID change.** Old cursors go stale once (clients re-read on
  `TranscriptItemCursorStale`); a stored web "seen" watermark or mobile reader
  anchor misses once and degrades to its fallback.
- **Read latency moves to disk.** Bounded by the acceptance criteria.
- **Legacy sessions** keep file-fallback identity for their old prefix.
- **Completion replaces streamed text.** Where persisted text differs from what
  streamed, the user sees it change once, at completion. Today they see it
  change at restart instead.
