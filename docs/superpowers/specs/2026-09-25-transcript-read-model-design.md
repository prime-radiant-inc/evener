# Transcript as the Only History Read Model

Status: proposed (2026-09-25). Supersedes the eviction approach in the closed
PR #2251.

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
a live session. Every attempt to bound the copy (evict, then rebuild from the
transcript) has to paper over that disagreement: rebuilt keys differ, items
duplicate across the persist/emit window, and live-only items vanish.

The code also breaks the promise of the governing spec,
`docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`
("Transcript key: stable item identity reproduced by live event projection and
later file projection"). Restarting a daemon already changes what clients see.

## End state

The transcript is the only history. The daemon keeps only live state.

1. **One identity.** Every history item is identified by the transcript entry
   that persists it: `(turnID, entrySeq, partIndex)`. Live notifications and
   file projection produce the same `id`, `transcriptKey` and `position` for the
   same item. There are no ID counters in the projector.
2. **One read path.** All history reads (root, live delegates, finished
   delegates, past daemonless sessions) are served from the transcript through
   the on-disk item index (`apptranscript.TurnCache`, item paging), with one
   shared projector identity for daemon and hub.
3. **Memory holds only the open turn.** Per thread the daemon keeps the turn in
   flight (its streamed items) plus a watermark. When a turn completes it is
   already on disk and leaves memory.
4. **The cut needs no file I/O.** The transcript is append-only, so a read
   records the watermark inside the subscription cut and reads history up to
   it afterwards, outside the cut.

Consequences: memory per thread no longer depends on its history; restart
changes nothing a client sees; delegate snapshots, seeding, prelude shifting
and incarnation rotation on prelude all disappear.

## Design

### Identity

- **Turn IDs.** Every turn-opening entry (USER_INPUT, goal STEERING,
  notification/steering carriers, and every entry that opens a standalone turn)
  persists `StableTurnID`. The projector adopts the ID carried by the event and
  never mints one. `nextTurn`, `SeedPersistedTurns`, `ReserveTurnID`'s counter
  and gap turns are deleted. `turn_mN` client-mutation IDs keep working, since
  they are already stable IDs.
- **Turn membership is explicit.** Every entry written inside an open turn
  carries `OwningTurnID`; file grouping groups by it instead of inferring
  boundaries from entry-kind adjacency. This fixes today's splits (a mid-turn
  HOOK_COMPLETED, MODEL_SWITCH, CHECKPOINT or SUMMARY currently closes the file
  group, so later ASSISTANT/TOOL entries land in a stray `turn_<idx>` and a tool
  call can be separated from its result).
- **Item keys.** `position = {entry: entrySeq, item: partIndex}` and
  `transcriptKey = apptranscript-item-v2:<turnID>:<entrySeq>:<partIndex>`.
  Item `id`s derive from the same pair and keep today's kind prefixes
  (`item_tool_`, `item_tool_result_`, …) so client folding code is unaffected;
  a tool call and its result are one item keeping the call's ID (fixing
  `mergeAppThreadItems`, `internal/apptranscript/logical_turn.go:314-316`).
  `TranscriptItemProjectionVersion` bumps to 2.
- **Per-part items on both sides.** Live emits one agentMessage per text part
  and communicate call and one reasoning item per thinking part, matching the
  file.
- **Seq travels with events.** Each notification for a history item carries the
  entry `Seq`. Items streamed before their entry exists (assistant text,
  reasoning, tool output) use a `Seq` reserved from the transcript writer at
  round start. An aborted round leaves a gap in `Seq`, which is harmless
  because positions are only ordered, never dense.
- **Legacy transcripts.** Entries written before this change have no
  `StableTurnID`/`OwningTurnID`. The file projection keeps its current
  fallback (`turn_<entryIndex>`, kind-adjacency grouping) for entries that lack
  them. Because history is only ever read from the file after this change, a
  legacy prefix is self-consistent; only entries written after the upgrade
  carry live-identical identity.

### What is history and what is live-only

History is exactly what the transcript persists. Items that are not persisted
move to an **ephemeral lane**: notifications with no `transcriptKey` or
`position`, shown by live clients and never returned by a history read. Today
they already vanish on restart, so this makes existing behavior explicit.

| Item | Decision |
|---|---|
| `round_timings`, `prompt_loaded`, `plugin_loaded`, `loop_detection` (a `loop-detected` STEERING is already persisted), `context_compaction`, `fork_summary`, `warning` | ephemeral |
| `tool_repair`, `goal_ended`, `turn_limit` (unless an existing TURN_FAILURE covers it), standalone `skill_activated` | persist as presentational entries |
| turn `CompletedAt`, `DurationMS`, tool `DurationMS` | persist (turn/tool timing fields) |
| NOTES_CONTEXT, TURN_FAILURE item | emit live (currently file-only) |

### Ordering: persist before emit

Every history item's notification is committed only after its entry is
persisted. Four sites emit first today and flip: goal continuation
(`agent/session_lifecycle.go:2804/2808`), HOOK_COMPLETED
(`agent/session_events.go:329/346`), TURN_FAILURE (`:266/313`), and tool
results (TOOL_CALL_END before the TOOL_RESULTS write,
`agent/session_tools.go:877` vs ~1055-1071). Streamed items are covered by the
reserved `Seq`: their notifications may precede the write, but they belong to
the open turn, which lives in memory until the entry is written.

### Watermark and the cut

- Each thread's server state tracks `W`, the highest entry `Seq` whose entry is
  persisted and whose notification has committed. `W` advances inside
  `CommitProjection`, so it is ordered with notification sequence numbers.
- `thread/read` inside `CaptureSubscription` captures `W`, a copy of the open
  turn, and the subscription cut. After releasing the cut it reads history
  items with `Seq ≤ W` from the transcript index. Entries past `W` (written but
  not yet announced) are excluded; their notifications arrive after the cut.
  No gap, no duplicate, no file I/O inside the cut.
- A thread with no live runtime (a finished delegate after idle release, a
  daemonless session) has no event stream to race; its reads go to end of file.
  Cold writers that append without events (delegate delivery to a released
  parent, attention resolutions, watch sends, shell repair) are therefore
  visible on the next read, which matches the persisted truth.
- `thread/turns/list` pages are read entirely from the file with the shared
  cursor identity.

### Shared projection and index cost

- The daemon and the hub use one projector with one `ProjectionID`. Hub-only
  post-stamps (image URLs, cost, file-backed output images, derived totals,
  `cmd/evener-hub/app_threadread.go:221-226`) move into a shared read helper so
  both sides return the same items. Today their different projection IDs make
  each side's read rebuild the shared sidecar index and rotate the cursor
  incarnation.
- Append validation of the index changes from a full prefix rehash on every
  read after an append (`internal/apptranscript/turn_index.go:571-584`) to the
  identity/size/trailing-bytes check proven by the attention fold cursor
  (PR #2254). A live session's reads then cost O(new bytes).

### Compaction and fork

- The file stays append-only. The fold's re-appended copies of entries
  (`agent/session_compaction.go:146-154`) carry the original `Seq`, and the
  projection skips a copy whose original is already present.
- Fork's `sourceTurnId` becomes the entry `Seq` instead of
  `len(s.history)+1`, which is wrong after compaction or resume
  (`agent/session_lifecycle.go:2663`).

### What is deleted

- `appTurnSnapshot`'s history: `Seed`, history turns, `itemPositions`,
  `turnIndex`, `turnEntries`, the paging index, prelude insert and position
  shifting, incarnation rotation on prelude (roughly 350–450 production lines in
  `server/appwire_turns.go` and `server/appwire_runtime.go`).
- `appDescendants[*].turns` and delegate seeding.
- Projector counters and gap turns.
- The server's separate prepared-transcript cache and projector.

### Out of scope

The client-mutation journal, the notifier replay ring (count-bounded; it has
no production reader today), the task store, and the projector's small
`delegates` map.

## Migration

Each phase ships on its own, keeps main green, and is valuable alone.

1. **Parity harness.** A differential test that drives a scripted multi-round
   session (user input, assistant text, reasoning, tool calls and results with
   images, steering, hooks, model switch, compaction, failure and retry, goal
   continuation, a delegate) and compares the live snapshot with the file
   projection item by item. It fails today and lists every divergence; each
   later phase turns rows green. Replaces the partial parity tests that filter
   out tool and system items.
2. **Transcript fixes (file side).** `OwningTurnID` on in-turn entries and
   grouping by it; persist the items and timing listed above; flip the four
   emit-first sites; fold-copy dedup; fork by `Seq`. These fix restart
   divergence bugs that exist today.
3. **Identity.** `StableTurnID` on every opener; `Seq` on events and writer
   reservation for streamed rounds; live projector keys by `(Seq, part)`;
   per-part live items; the ephemeral lane; delete counters, gap turns and the
   prelude shift; projection version 2.
4. **Shared projection and index cost.** One projector and `ProjectionID` for
   hub and daemon; shared read post-stamps; tail-based append validation.
5. **Read-model switch.** Watermark in the cut; root and delegate history reads
   from the transcript; delete snapshot history and delegate snapshots.
6. **Cleanup and docs.** Remove dead code and tests; amend the atomic paging
   spec and `docs/appwire-protocol.md`.

Phases 1–2 are planned in detail in
`docs/superpowers/plans/2026-09-25-transcript-read-model-phase1-2.md`. Phases
3–6 each get their own plan when the previous phase has landed, because each
phase changes the code the next one edits.

## Risks

- **Client-visible ID change.** Old cursors go stale (clients already re-read on
  `TranscriptItemCursorStale`); a web "seen" watermark or mobile reader anchor
  stored before the change misses once and degrades to its fallback.
- **Read latency moves to disk.** Mitigated by the index and tail validation;
  measure `thread/read` p50/p99 on a large root before and after phase 5.
- **Streamed-item identity before the write.** Relies on writer `Seq`
  reservation; an aborted round must never reuse a reserved `Seq`.
- **Legacy sessions** keep file-fallback IDs for their old prefix, forever.
- **Text differences** between live and persisted forms (reasoning summaries vs
  thinking parts, communicate echo suppression, private-evidence tool results)
  must be resolved in phase 3 so a completed turn reads back exactly as it
  streamed.
