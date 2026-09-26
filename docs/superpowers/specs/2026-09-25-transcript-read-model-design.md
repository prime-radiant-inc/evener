# Transcript as the Only History Read Model

Status: accepted for implementation, revision 8 (2026-09-26). Supersedes the eviction approach in
the closed PR #2251.

Revision history:
- **Revision 8** matches the contract to the phase 3 implementation
  (`wip/trm-phase3`), settling points the last review round raised: the three
  locks (projection serialization, append lock, queue mutex) nest in one fixed
  order; COMMUNICATE and completion entries use the synced door; a queue
  overflow that never catches up counts toward the three-rebuild failed-state
  cap; client request generations are monotonic across boot changes and a
  descendant served by its root's daemon carries a qualified generation token;
  the index header persists the covered entry count; a single entry that fails
  to decode is quarantined as one visible item rather than failing the whole
  thread; `delivery` covers session-owned async writes as well as cold
  writers; the update log is bounded to 10,000 records; a failed thread's read
  attempts recovery before returning the error; a closing history publishes
  what it has and a recreated history's epoch never goes backwards; the tool
  completion ordinal field is named `completedAtEntry`.
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
- **Revision 6** followed the final review round, which found the model
  converged. It fixed the remaining local gaps:
  - positions and keys use the entry ordinal, because real transcripts contain
    duplicate `Seq`s
  - the registry serializes appends
  - the index records item contributors and turn summaries
  - replacement is scoped to a window and a snapshot
  - communicate previews, `roundID`, resume startup and async writes get exact
    rules
  - resync delivery failure ends the subscription
  - boundary tests gate activation
- **Revision 7** closed consistency gaps from review of revision 6:
  - ordinals are assigned only to recorded lines
  - the index catches up to the recorded length before a read uses it
  - a file that stops extending rotates the index incarnation
  - a latest-window read is authoritative to the end
  - the turn summary tracks reopen markers
  - async writes pick their turn inside the append lock
  - round coverage stops only the covered streams
  - communicate records before it emits
  - the index has one extender at a time and publishes its covered length last
  - projection and its rebuild are serialized per thread; stale epochs are
    discarded
  - tool items and their execution overlay have a display rule
  - the running turn ID lives in the registry entry
  - notices before any entry have an anchor
  - the registry entry holds the running and open gap turn IDs from admission
  - COMMUNICATE records its call ID, and the index can find a round's calls
  - removal happens only by replacement; the registry is a prerequisite, not
    part of phase 2
  - daemonless reads return later completions of held items
  - one incarnation rule: stale cursors are rejected, new incarnations replace
  - a projection that keeps failing puts the thread in a failed history state
  - index extension truncates to the covered record counts first
  - reads drop tool execution state that their history completes
  - legacy status latches in the turn summary; a retry after failure is a new
    turn

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

1. **History is the projection of recorded entries and the transcript header,
   and nothing else.** One projector, shared by daemon and hub, turns them into
   turns and items. Prelude items come from the header. Live history notifications come from projecting each entry after it is
   recorded, so a live history item and its reloaded form are the same by
   construction.
2. **Everything not yet recorded is a live overlay.** Streaming text and
   reasoning, running tool state, communicate previews and ephemeral notices
   live in memory, separate from history, and never claim history identity.
3. **Merges are versioned and upsert-only.** Every history item and turn carries
   a version: the highest entry ordinal (below) among the entries that
   contributed to it. The
   higher version wins.
   - Applying a fact twice changes nothing.
   - A file read that is ahead of the live stream changes nothing.
   - A merge never removes a history item. Only a replacement does. A higher
     boot generation, a new incarnation on a latest-window read, a newer
     resync epoch, and an authoritative daemonless read all trigger one; so
     does a boot-generation token whose form or owner differs from the one
     the client holds (see Descendants, under Crash/boot generation), even
     when its counter is numerically lower. This list is a summary; the
     authoritative rule for each trigger is under Crash/boot generation and
     Reads.
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

### Recorded length, entry ordinals and Seq

The writer has three doors today:
- **`Append`** is buffered. It fsyncs at most once per `SyncInterval`, which is 1s
  for sessions (`agent/transcript/transcript.go:521-524, 680`;
  `agent/session_init.go:616`).
- **`AppendSynced`** fsyncs every record.
- **`AppendDurable`** fsyncs, and truncates the record if the fsync fails
  (`transcript.go:655-677, 888-911`).

COMMUNICATE and completion entries always go through the synced door
(`agent/session_execution.go:101, 393-397, 426`), so a delivered message or a
turn's terminal status survives a crash rather than waiting on the buffered
door's interval.

A record is *recorded* once its whole line is written. That includes a retained
record: the line was written but its fsync failed (`transcript.go:729-735`).

- **Publishing the recorded length.** After every append that records a line,
  through any door, the writer publishes its recorded length under its lock. The
  entry is handed to the server only after that. The durable door rolls back
  before publishing, so the recorded length never includes rolled-back bytes.
- **One process writes a transcript.** A session's transcript is written only by
  the process that owns the session, and the per-session ownership lock enforces
  it (`llm/apilog.go`, `ReserveSession`). The hub and the TUI never write
  transcripts. So serializing appends within one process is sufficient.
- **The per-process registry serializes appends.** Every writer in the process
  appends to a file through one registry entry for that file, cold writers and
  mid-session reopens included. The registry entry owns the append lock, the next
  `Seq`, the recorded length and the next entry ordinal. So two writers can never
  append at a stale offset or with a stale counter.

  Real transcripts written before this change contain duplicate `Seq`s. For
  example, lines 9391–9394 of a local 134 MB coordinator transcript carry seq
  9390, 9391, 9390, 9391. Two writers were open on one file without `O_APPEND`,
  and the buffered door never seeks (`agent/transcript/transcript.go:647-653,
  962, 981`). The registry fix ships ahead of phase 1 as its own PR.
  - In-process reads never go past the recorded length.
  - A rolled-back append still consumes its `Seq`.
- **Entry ordinal.** Identity and order use the **entry ordinal**, not `Seq`. The
  entry ordinal is the 0-based index of an entry line in the file, excluding the
  header. It is unique and increasing by construction, including across legacy
  duplicate `Seq`s. The registry knows it for every append in-process. A reader
  counts lines. `Seq` keeps its existing uses and nothing new depends on it being
  unique.
  - Only a recorded line gets an ordinal. A rolled-back append consumes its
    `Seq` but not an ordinal. Its entry was never announced in-process, so the
    next record to take that ordinal aliases nothing a client got from this
    process. A reader in another process that saw the rolled-back line sees the
    file stop extending, which rotates the index incarnation (see Reads).
- **Reads from another process.** A reader with no writer for the file in its
  process reads to end of file and drops an incomplete last line, as today. The
  hub reading a daemonless session is the main case. Such reads are
  authoritative for what they return. There is no live stream to merge with. The
  exact scope of replacement is defined under Reads: a returned range, a snapshot
  identity, and incarnation handling. A record that another process later rolls
  back is gone on the next read.
- **Crash or power loss** can lose buffered entries whose notifications clients
  already have. Every daemon start mints a **boot generation**, which increases
  monotonically. It is a per-session counter persisted in the session's own
  state, incremented under that session's ownership lock, and fsynced before the
  daemon serves any read or notification. Every read
  response and every `history/updated` carries it. A client that sees a boot
  generation higher than the one it holds marks that thread invalid. The client
  applies no update for that thread until a fresh latest-window read at the new
  generation replaces its whole history, and it issues that read at once. A read
  still in flight from the old generation is ignored when it returns. Anything
  carrying a lower generation is ignored.
  - **Daemonless reads.** The hub has no running daemon for the session. It
    stamps the distinguished generation `daemonless` instead of a number.
  - **Descendants.** A delegate served by its root's daemon carries a
    **qualified** token, `<n>@<rootSessionID>`, where `n` is the root's boot
    counter. Comparing two tokens of the same form and the same owner (both
    unqualified, or qualified with the same root) is the numeric comparison
    above. Any other difference — an unqualified token against a qualified
    one, two qualified tokens with different owners, or either token against
    `daemonless` — replaces rather than ignores, even when the incoming
    counter is numerically lower: a delegate later served by its own daemon
    (unqualified) replaces a qualified token from its former root, and vice
    versa.
  - **Which response wins.** The client keeps a **per-thread** request-generation
    counter that is monotonic across boot changes: it is never reset when a
    thread is marked invalid. Ordinarily the client has one latest-window
    read per thread outstanding at a time. Marking a thread invalid can leave
    a second one outstanding: the read already in flight from before the
    invalidating event, alongside the fresh read the invalidation issues. The
    guarantee does not depend on how many are outstanding — it depends only on
    the counter and comparing each response's echoed generation against the
    newest one issued for that thread: a response whose generation is lower
    than the newest issued is discarded, and so is one issued before the
    thread's last invalidation. So the pre-invalidation read's response, which
    always carries a lower generation than the invalidating read that
    followed it, is discarded, never applied after the newer read's response,
    and a delayed response from any generation cannot overtake a newer one.
  - **Replace or merge.** The generation token decides only that.
    - A latest-window response whose token differs from the one the client holds
      replaces the thread's whole history. That covers numeric to `daemonless`,
      `daemonless` to numeric, a higher number of the same form and owner, and
      (per Descendants, above) a qualified token whose owner differs from the
      held one, or a switch between an unqualified and a qualified token —
      each of these replaces even when the qualified token's numeric part is
      lower.
    - Within the same `daemonless` token and incarnation, a daemonless response
      follows the scoped rules under Reads.
    - Updates carrying a lower numeric generation, of the same form and owner
      as the held token, are ignored.
  - **The state machine.** One state machine covers reads and updates alike,
    stated fully by `CompareBootGeneration` (Descendants, above): same token,
    apply; same form and owner with a lower number, ignore; anything else —
    a higher number of the same form and owner, or any difference in form or
    owner — replaces:
    - **Same token as held:** apply normally.
    - **Lower numeric token, same form and owner:** ignore.
    - **Any other token** (a higher number of the same form and owner; a
      switch between numeric and `daemonless`; a qualified token with a
      different owner; or a switch between an unqualified and a qualified
      token, regardless of which number is larger):
      1. Mark the thread invalid and issue a fresh subscribing latest-window
         read.
      2. Drop updates while invalid. This is safe because the replacing read is
         taken under `CaptureSubscription`: every update after its cut is
         delivered after its response, and every update before its cut is
         already in it. No gap can form.
      3. Replace the whole history with that read.

    This takes precedence over the index incarnation, the epoch, and the
    recorded length. So entries lost in a crash never survive on a client,
  even when the truncated file happens to match the index's covered length.
  Resync epochs are per boot generation. They start again at zero on each boot
  and are compared only within one.

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

**Turn kind.** The first entry of each turn persists a `TurnKind` field (JSON
`turn_kind`), typed `schema.TurnSpanKind` — a distinct type from the existing
entry-kind field `schema.TurnKind`/`Kind` (`USER_INPUT`, `ASSISTANT`,
`TOOL_RESULTS`, and so on). The two are orthogonal: this one names the role of
the turn the entry opens, not the entry's own kind, and no consumer that
switches on the existing `Kind` field changes behavior because of it. Its
values:
- **`execution`**: a real run of the model.
- **`gap`**: standalone entries recorded between executions, such as hooks,
  model switch, environment and persisted notices. The session mints one gap turn
  for each run of such entries. This keeps today's grouping of a burst of
  announcements into one turn (`appwire_projection.go:2160-2198`).
- **The prelude turn** holds startup entries only before the first execution of
  a fresh session. Startup entries written on resume, such as held SessionStart
  hooks (`agent/session_events.go:340-345`), go to a gap turn, as they do live
  today (`appwire_projection.go:72-77, 2186-2191`).
- **`delivery`**: an entry with no running execution to own it. This covers two
  cases: an entry from a cold writer (attention delivery, watch sends and shell
  repair have no session — `agent/session_attention.go:351-397`,
  `agent/delegate_delivery.go:106`, `agent/delegate_tree_watch.go:478`,
  `agent/delegate_shell_repair.go:109`), and a session-owned asynchronous write
  that finds no execution running (see Asynchronous writes below). Each such
  write is its own turn with a fresh ID.

  Today these STEERING entries have no owner and join the previous open group in
  the file projection (`internal/apptranscript/logical_turn.go:47-53, 121-122`).
  Live never shows them at all. Showing each delivery as its own turn is an
  intended, visible change for new-format sessions.

**Asynchronous writes through the session's own writer.** These arrive from
delivery and stop goroutines at any time: model-bound attention STEERING
(`agent/session_attention.go:541-545`) and ATTENTION_RESOLUTION (`:1382, 1489,
1532`).
- If an execution is running, they take the running execution's `TurnID`.
- Otherwise they take a `delivery` turn of their own.
- They never take the ID of a turn that has already completed.
- A turn's completion entry is written as the last entry of its execution span.
- The choice is made inside the registry's append lock. The running execution's
  `TurnID` for the file is held in the registry entry and changes only under
  that lock: the session sets it when it admits the execution, before it
  publishes through `SetProcessingTurn`, and the completion append clears it.
  The open gap turn ID is held the same way: a standalone entry takes it, or
  mints one when none is open, and any execution, delivery or prelude entry
  closes it. An async write that loses the race to the completion therefore
  takes a delivery turn.
- **Lock order.** Three locks nest in one fixed order: the thread's projection
  serialization (held by its projection goroutine for each step, and by a read
  recovering a failed thread, for the same kind of step) is outermost, then
  the append lock, then the per-thread projection queue's mutex, a leaf. The
  recorded hook takes only the queue mutex. A rebuild — whether the
  goroutine's own, or a read's recovery of a failed thread — captures its
  boundary ordinal by briefly taking the append lock and then the queue mutex
  while it already holds the serialization, so the boundary and the queue's
  contents are one atomic snapshot; releasing both locks again before doing
  any I/O. Nothing takes the serialization while the append lock is held, and
  nothing takes the append lock while the queue mutex is held. The queue mutex
  is never held across I/O. The serialization *is* routinely held across file
  and index I/O — normal projection, a rebuild, and a read's recovery all read
  and write the transcript and its index while holding only the
  serialization, with the append lock and queue mutex both released — since
  serialization ordering, not lock-freedom during I/O, is what keeps one
  thread's projection steps from interleaving. That I/O never blocks an
  appender, because the append lock is never nested inside it.

**Turn membership** comes from `TurnID`, not from entry-kind adjacency.

**Item key.** `transcriptKey = apptranscript-item-v2:<turnID>:<ordinal>:<part>`.
- `ordinal` is the entry ordinal of the entry that opens the item.
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
- **Server-side steering identity.** A steering item's id is `item_steering_<entryIndex>`,
  where `entryIndex` is the entry's ordinal plus one — the same number as its
  position's `entry` and its transcript key's ordinal, so it is unique the same
  way every other entry-ordinal-keyed item's id is. The server derives it the
  same way live and on reload; the client never mints it. The item still
  carries the steer request's `ClientMutationID`, so a client that sent the
  steer matches its own request to the resulting history item without needing
  an id of its own. `StableTurnID` keeps meaning the steer mutation's own ID
  (see above) and never contributes to the item's id.

**Position.** Every entry, legacy and new, uses `{entry: ordinal + 1, item:
part}`. Header-derived prelude items use `{entry: 0, item: part}`, with key
`apptranscript-item-v2:prelude:header:<part>` and version 0. The header never
changes after creation, so their version never grows. A turn is displayed at the
position of its first item.

**Format marker.** Every new entry carries an explicit format version, so the
projector never infers the format from which fields are present. `StableTurnID`
and `OwningTurnID` already appear on legacy steering entries
(`agent/schema/turn.go:233-251`).

**Legacy entries** (no format marker):
- They keep today's turn IDs (`turn_<entryIndex>`), adjacency grouping and
  inferred status: completed by default, failed or interrupted from their entries
  (`logical_turn.go:185-218`).
- Their positions move to the entry-ordinal scheme. Cursors go stale once, at
  the projection version bump.
- A resumed legacy session is self-consistent. New entries carry new identity,
  and legacy turns are never rewritten.

### Turn status

Status is derived from persisted facts:
- **Gap, delivery and prelude turns** are always complete.
- **An execution turn's status is its latest completion entry:** completed,
  failed or interrupted, as recorded. A turn reopens only when recovery reclaims
  and re-runs it under the same ID. That case writes a reopen marker, and the
  turn is open until its next completion
  (`agent/session.go:530-548`; `agent/session_client_mutation.go:408-415,
  575-599`).
- **An execution turn with no completion** is open.
- **A legacy turn** keeps today's inferred status.
- **Failure and retry.** A TURN_FAILURE ends the input
  (`agent/session_lifecycle.go:1700`), so in a new-format turn it is followed
  by the turn's completion entry, which records failed. Running the work again is a new admission and a
  new execution turn with its own ID. Model-call retries inside a round write no
  completion and stay in the running turn. Recovery re-running a reclaimed turn
  is the only way an interrupted turn's ID runs again, and it writes a reopen marker.

**On resume**, the session writes an interrupted completion for each new-format
execution turn it finds open, except a turn it is about to reclaim and re-run.

**Running state lives in the overlay.** A client shows an open turn as running
while the overlay says it is running, and as interrupted otherwise. That is a
display rule: it is never stored or merged as a fact. A daemonless past session
has an empty overlay, so a turn that a crash left open displays as interrupted.
Today the hub shows such a turn as completed (`logical_turn.go:188`), so this is
an intended visible change for new-format sessions.

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
  `communicate` delivers its message, which today is the `EventCommunicate`
  emit (`agent/session_tools_communicate.go:73-77`). The entry is appended
  first, and the event is emitted only once the entry is recorded. Every
  failure path in `communicate` (missing `end_turn`, empty message, abort)
  happens before that point. A recorded COMMUNICATE entry is therefore a
  message the user receives. It is live when the emit follows. If the daemon
  crashes between recording and emitting, it appears on the client's next
  read. It is never lost, and never shown for a failed call.
  - The communicate item is projected from the COMMUNICATE entry, so it is in
    history at delivery, whether or not the round's TOOL_RESULTS is ever written
    (`agent/session_tools.go:1021-1090`).
  - The ASSISTANT entry's communicate call part is hidden, but it still occupies
    its part index.
  - If the COMMUNICATE append is not recorded, nothing is emitted, and the
    session treats it as writer failure and fails closed (see Writer failure).
    A delivered message is never missing from history, apart from the crash
    loss every buffered entry shares (see Recorded length).
  - No history item is ever removed.
- **Fold copies.** A fold re-appends entries after its markers
  (`agent/session_compaction.go:146-154`). The copies carry the original's entry
  ordinal in `OriginalOrdinal`, and they are neither projected nor announced.

**Incremental use.** For live notifications, the daemon keeps a projection state
for each thread:
- the open turn's accumulators (usage sums, earliest timestamp)
- the open round's call records, as call ID to the opener entry's offset and
  length only. When TOOL_RESULTS lands, the drain goroutine reads the call's name
  and arguments back from that entry, outside any lock
  (`apptranscript.go:598-603`). Memory therefore stays bounded by the number of
  calls, not their argument size.
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

Projection is serialized per thread, in append order. The writer hands each
recorded entry to that thread's projection queue while it still holds the append
lock, so entries enter the queue in ordinal order no matter which goroutine
appended them. That includes async attention writes. One goroutine per thread
drains the queue.

The queue is bounded to 4 MB of queued entries per thread. An enqueue that would
exceed the bound does not block the append lock. It marks the thread for resync
and drops the queue. The drain goroutine then rebuilds through a boundary
ordinal, as described below, so a slow projector costs a resync, never unbounded
memory. If the queue overflows again before that rebuild catches up, the
goroutine restarts the rebuild through a new boundary ordinal, repeating until
the queue is covered; each such restart counts as one of the three consecutive
rebuild attempts below, so a projector too slow to ever catch up still reaches
the failed state instead of restarting forever. A boundary test appends from
two goroutines at once and checks that projection sees every ordinal in order.

**When projecting or publishing fails** after an append has recorded:
1. The server bumps the thread's resync epoch.
2. It pushes `evener/thread/resync`, which already exists
   (`appwire/types.go:213, 2697-2700`), with the new epoch.
3. It rebuilds the projection state from the file, inside the thread's
   projection serialization, as follows:
   - **Boundary.** It first captures a boundary ordinal `B`: the last entry
     recorded when the rebuild starts. `B` is taken under the append lock, which
     is also held for every enqueue. That makes `B` and the queue's contents an
     atomic snapshot. It rebuilds through exactly `B`.
   - **Queued entries.** It discards queued entries whose ordinal is `B` or
     less, because the rebuild already covers them, and projects only the
     queued suffix after `B`, in order.
   - **Result.** No entry is applied twice and none is skipped.

Every read response and `history/updated` carries the epoch. A client that
receives the resync, or that later reads and sees a newer epoch than it holds,
treats it the same way as a higher boot generation. It marks the thread invalid,
issues a fresh latest-window read, and replaces the thread's whole history with
that read, instead of merging. Until then it applies no updates for the thread.
A response or update with an older epoch than the client holds is discarded. The
rule that live responses merge applies only within one boot generation and
epoch. The epoch never needs clearing.

If a resync push cannot be delivered to a subscriber, the server ends that
subscription. The client's reconnect then starts from a fresh read with the
current epoch. Every subscriber recovers.

If three rebuild attempts in a row fail or are overrun by a queue overflow that
does not catch up, the thread's history enters a failed state. The server
stops projecting that thread, drops its queued entries, stops enqueueing new
ones for it, and pushes a resync. Nothing accumulates for a failed thread. This
failed state is reserved for rebuild and infrastructure failures — an index or
file I/O error, or a projector too slow to keep up — and for a builder error
applying an already-decoded entry, which a rebuild might still recover from a
clean incremental or file-projection state. It is never entered for one entry
that fails to decode (see Quarantine, below): that failure is a pure function
of the entry's own bytes, which do not change, so no rebuild or retry can ever
recover it, and quarantining it is strictly better than failing the whole
thread's history over it.

**Quarantine.** A single entry that fails to decode — a line whose bytes are
not a well-formed entry — does not fail the thread. It is quarantined: the
entry projects as its own turn holding one visible "unreadable entry" item
naming its ordinal, and the thread's history continues past it, live and on
reload alike. Quarantine applies only to a decode failure, never to a builder
error over an entry that did decode; a builder error goes through the rebuild
and failed-state path above instead, so a failure that a whole-file rebuild
might recover from is never permanently masked as one quarantined item. A quarantined
entry never triggers a resync, a rebuild, or the failed-history state.

A history read of a failed thread first attempts recovery: it rebuilds inside
the thread's projection serialization, the same rebuild the goroutine runs.
If that succeeds, the thread is un-failed, its epoch bumps, one resync is
pushed, and the read returns data at the new epoch. If it fails, the read
returns the error `ErrorTranscriptHistoryFailed`, which names the entry
ordinal that fails to project, and pushes nothing. The response carries no
items and no snapshot identity, and it carries the boot generation and epoch
unchanged. A client that receives it keeps the history it already holds
unchanged, marks the thread's history as failed, and shows one visible
diagnostic. It neither replaces nor merges anything until a later read
succeeds. The overlay keeps updating live.

The session keeps running, and its entries keep being recorded. A daemon
restart, or any later read that recovers as above, projects from the file
again and replaces history under the rules above.

### The live overlay

The overlay is per thread and in memory. It holds four kinds of state.

**Streams.**
- Each model response attempt has its own `streamID`, carrying `roundID` and an
  attempt number.
- **What a `roundID` spans.** A `roundID` covers the model requests that can
  record one ASSISTANT or salvage entry: the attempts, retries and fallback groups
  up to the first recorded ASSISTANT or salvage entry.
  - Any model call after that point gets a new `roundID`. That includes the
    pause_turn continuation and the bare-text retry, which record an ASSISTANT
    entry and then call the model again (`agent/session_lifecycle.go:2338-2345,
    2384-2392`).
  - Projected ASSISTANT and salvage items carry their `roundID` on the wire.
  - Once a round's entry is recorded, the server emits no further text or
    reasoning deltas for that round. Tool execution state, communicate previews
    and the round's `overlay/end` are still delivered after that point.
- `overlay/reset(streamID)` discards one attempt when it is retried
  (`appwire_projection.go:549-575`). The next attempt has a new `streamID`, so
  its output is never mistaken for the discarded one.
- The ASSISTANT entry, or the salvage entry, written for the round records its
  `roundID`. When that entry is in history, the round's text and reasoning
  streams are covered and dropped, whichever attempt or fallback group
  (`agent/session_model_call.go:1215-1230, 1295-1306`) the recorded content came
  from. This covers `BestSalvage` picking an earlier group
  (`agent/session_events.go:692`).
- `overlay/end(roundID)` is sent at round end. A round that ended with nothing
  recorded collapses into one interrupted notice. That happens after a
  reasoning-only round (`agent/session_lifecycle.go:958-960`), a closing session,
  the content filter, or empty salvage (`agent/session_events.go:679-707`).
- **Communicate previews are exempt from round coverage.** A preview stays on
  screen after the ASSISTANT entry is recorded, through hooks and slow sibling
  tools, as it does today (`appwire_projection.go:644-656, 685-690`). It is
  covered only by its COMMUNICATE entry. The preview is keyed by its call ID,
  and the COMMUNICATE entry records that call ID
  (`agent/session_tools_communicate.go:73-77` already emits it). If the call
  failed, the round's `overlay/end` drops it.
- Stream content is bounded by the provider's maximum output for one response.

**Tool execution state.** Tools run after the ASSISTANT entry holding their
calls is recorded (`agent/session_model_call.go:994-996`).
- Running output, per-call completion (TOOL_CALL_END,
  `agent/session_tools.go:876`) and held images (`appwire_projection.go:847-868`)
  attach to the call's history key.
- Until then, the client shows the history item with the overlay's execution
  fields (running output, per-call status, held images) laid over it. The
  overlay never changes the item's identity or version.
- The history item wins once its version includes the TOOL_RESULTS entry, and
  the overlay's state for that key is dropped.
- Running output keeps the last 256 KB per call. Held images keep the existing
  per-result image limits.
- If TOOL_RESULTS is never recorded, `overlay/end` turns the execution state into
  an interrupted notice.
- **Derived interrupted state.** The projector derives an interrupted state for
  any tool call whose execution turn has a completion but no TOOL_RESULTS for
  that call. That completion is the interrupted completion that resume writes
  after a crash. So a reload, a restart and a daemonless read all show the call
  as interrupted, not as running forever.

**Running state.** The running turn ID and the thread status.

**Ephemeral notices.**
- They cover `round_timings`, `prompt_loaded`, `plugin_loaded`,
  `loop_detection`, `context_compaction`, `fork_summary`, `warning`, and
  interrupted notices.
- Each thread keeps a ring of 50 `round_timings`, 50 of everything else, and
  64 KB in total. A daemon-wide cap of 16 MB evicts the oldest first.
- Each notice is anchored at `{entry: ordinal + 1 of the preceding entry, item:
  1<<30, sub: n}`. The item value sits past every real part index, and `sub` is a
  new position component that orders notices among themselves. A notice with no
  preceding entry is anchored at `{entry: 0, item: 1<<30, sub: n}`, after the
  header-derived prelude items.
- Notices survive a browser refresh while the daemon lives and vanish on
  restart, as they do today.
- A released delegate's ring is dropped with its runtime.
- **Closing.** A closing thread history (a delegate release, or an identity
  replacement) first projects and publishes every entry recorded up to its
  recorded length, so entries recorded just before the close still reach
  clients as `history/updated`, then stops. A later read of the same thread id
  (a released delegate read again) recreates its history lazily. Its epoch
  never goes backwards: the recreated history starts at least at the epoch its
  predecessor ended with, for the life of the boot generation.

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

The writer API returns a record: either `recorded (ordinal, Seq)`, or `not
recorded` together with the door's error. When a record is not recorded, the
writer's `Poisoned()` and `Closed()` state tells the caller why: missing,
closed, poisoned, or a clean durable rollback that leaves the writer usable.
The fail-closed rule below branches on that state. Today `Append`
and `AppendDurable` return nil with Seq 0 for both a missing and a closed writer
(`agent/transcript/transcript.go:606-627`).

`evener serve` always has a state directory (`cmd/evener/serve.go:527`). There is
one rule for when a served session fails closed. It fails closed when:
- its transcript cannot be created, or its writer is poisoned
  (`transcript.go:724-728`)
- a COMMUNICATE append is not recorded. The message is emitted only after its
  entry records (see The projector). An unrecorded COMMUNICATE therefore means
  `communicate` cannot deliver it, and the session fails closed instead of
  dropping the message silently or delivering it without history.
- a completion entry is not recorded. A turn's terminal status must never be
  lost.

A writer is closed only when its session closes, and a closed session accepts no
input, so a closed writer cannot lose history while input continues. A missing
writer in a served session is the "cannot be created" case above.

The only unrecorded append that leaves the writer usable is a clean durable
rollback of an entry that is neither COMMUNICATE nor a completion. A poisoned,
missing or never-created writer always fails the session closed, whatever the
entry kind. The caller's existing error handling applies,
and nothing was announced, so no history is missing. Failing closed means:
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
   it: history by version, and overlay items minus the streams and previews
   that this projected history covers. Tool execution state for a key is
   dropped when the projected item's version includes its TOOL_RESULTS entry,
   the same rule notifications use.
3. Deliver the response. The subscription stays buffered until the response
   enters the connection's send queue. This is today's `releaseHydration`
   (`internal/appserver/server.go:1290-1310`), which releases only records past
   the cut. No notification after the cut can reach the client before the
   response.

Notifications delivered after the response merge by the same rules. A later
`history/updated` at a lower or equal version is ignored, and so are overlay
updates for a covered or reset stream. So reading the file ahead of the stream
cannot produce a gap, a duplicate or a regression.

Nothing announced before the cut is missing from the read. An announced entry is
already recorded, and the recorded length is published before the entry is
announced.

`thread/turns/list` pages come from the file, and the same merge applies to the
open turn's page. A thread with no runtime has an empty overlay.

**Authoritative replacement is scoped.** The hub serves a daemonless session from
the file. Every successful read response, live or daemonless, carries its
**snapshot identity**: the index incarnation plus the recorded length it read.
Error responses, including `ErrorTranscriptHistoryFailed`, carry the boot
generation and epoch but no snapshot identity. A client never adopts a
generation or epoch from an error response, and never replaces anything with
one. A thread's failed-history state lasts on the client until a read succeeds. A daemonless
response is authoritative for the position range it returned:
- The client replaces its items in that range and keeps pages outside it.
- Pages from the same snapshot accumulate.
- A daemonless latest-window response is authoritative from its first position
  to the end of history. The client drops any item it holds past the returned
  window. This scoped, position-range drop is a daemonless-only rule: within
  one boot generation, epoch and incarnation, a live response never drops
  items this way, and merges by version instead. It is still subject to the
  whole-history replacement triggers above (a higher boot generation, a newer
  resync epoch, or a different incarnation), which a live latest-window
  response can carry too — an index rebuild rotates the incarnation. Those
  triggers replace the whole history rather than dropping a range; they are
  not the position-scoped rule this bullet states.
- **Later completions of held items.** A daemonless latest-window request
  carries the snapshot the client holds. The response also returns every item
  and turn outside the window whose version grew since that snapshot, found
  through the index's update log (see Index). The lookup is scoped to the held
  snapshot's incarnation. If the incarnation differs, the response is a full
  latest-window replacement with no update-log deltas. A tool call whose TOOL_RESULTS
  lands after the client cached its page therefore reaches the client.

**Index incarnation.** The index gets a new incarnation whenever the file is no
longer an extension of what the index covers: shorter than its indexed length,
or with different trailing bytes (see Validation). Within one incarnation the
recorded length only grows, so snapshots of one incarnation order by length.
Incarnations themselves are not ordered, so every **latest-window** read request
carries a **request generation**. The client increments it for each latest-window
read it issues, and the response echoes it.
- **Ordering.** The client applies a latest-window response only if no response
  to a later generation has been applied. So a slow response from an old sidecar
  can never overwrite history from a newer one.
- **Backfill pages** carry no generation. They accumulate within their snapshot,
  in any arrival order. A page is dropped when its snapshot cannot be the
  current one: a different incarnation (incarnations compare by equality
  only, never by age), or the same incarnation with a shorter recorded length
  than the client already holds. Only the current
incarnation is valid. Each rule applies on one side:
- **The server rejects.** A backfill request whose cursor names an incarnation
  other than the current one gets `TranscriptItemCursorStale`, and the client
  re-reads the latest window.
- **The client replaces.** A successful response always carries the current
  incarnation. If a *latest-window* response carries an incarnation different
  from the one the client holds, the client replaces the thread's whole history
  with it. A backfill page from a different incarnation never replaces anything.
  The server rejects its stale cursor, and the client re-reads the latest window.
- **The client discards.** A response from the client's incarnation with a
  shorter recorded length than the client already holds arrived out of order.
  The client discards it and re-reads the latest window.

The hub already
rotates its cursor incarnation when a snapshot does not extend the previous one
(`cmd/evener-hub/internal/appsource/source.go:120, 604`), and this keeps that
rule for the seekable index.

So backfill never discards newer pages. A record that another process rolled back
disappears on the next read: the rollback rotates the incarnation, and the
client replaces the thread's history.

### Index

The index is a derived sidecar file. It is rebuilt whenever validation fails, and
it holds three tables of fixed-size records.

- **Item records**, sorted by position. Each one holds:
  - the position and key
  - a slot reference to its turn's summary record
  - its contributors: the byte offset and length of the entry that opens the
    item, and of the entry that completes it (for a tool item, the TOOL_RESULTS
    entry)
  - its version
- **Turn summary records.** Each one holds:
  - the first entry's offset
  - the latest lifecycle entry's offset and the status it sets: the recorded
    status for a completion entry, open for a reopen marker
  - for a legacy turn, today's inferred status as a latch instead: extension
    sets failed on any TURN_FAILURE entry and interrupted on interrupted
    steering unless failed, and later entries never clear it
    (`logical_turn.go:199-217`)
  - usage totals and timestamps
  - the offset of the latest ASSISTANT entry whose tool calls still await their
    TOOL_RESULTS. Extending over a TOOL_RESULTS entry decodes that one entry to
    map each result's call ID to its part index, and so to its item record.
  - the turn's version
- **Update log records**, in append order. Each in-place update (a completer
  filled in, a turn summary rewritten) appends one record with the slot it
  changed and the byte offset of the entry that caused it. A request holding a
  snapshot at recorded length `L` binary-searches the log for the first record
  at or past `L` and returns the items and turns it names. The log is bounded
  to the newest 10,000 records: an extension that would grow it past that
  cuts the oldest ones, and the header persists the byte offset the cut
  entries' updates started from as the log's retained floor. A request whose
  held length is below that floor predates what the log still retains — the
  binary search alone cannot tell that case apart from "no updates yet," so
  the floor is what makes it decidable — and gets a full latest-window
  replacement with no update-log deltas, the same response an incarnation
  mismatch gets.

**Extension.** Index updates are not part of the append. The index header
records the length it covers. A read captures the recorded length once, extends
the index from the covered length to it, and projects to that same length.
Extending over an entry fills in a completer or rewrites a turn summary, both
at fixed offsets, and appends new item, turn and update log records.
- One extender at a time holds an exclusive lock on the sidecar; readers hold a
  shared lock while they read records. The lock is a file lock, because the hub
  and a daemon can open the same sidecar. Extending and projecting are two
  separate acquisitions, not one lock held across both or downgraded between
  them: a read extends under the exclusive lock, releases it, then projects
  under the shared lock, entirely from the records its own handle just wrote
  and now holds decoded in memory. So the two steps describe the same build by
  construction, whether or not another handle takes the exclusive lock in
  between: that handle's extension, if any, changes the file, not the
  in-memory records this read already extended and is about to project from.
- The header holds the covered length, the covered entry count (the next
  entry's ordinal) and each table's record count, all published in the same
  header write. The covered entry count lets an extender in a fresh process —
  a restart, or another process extending the same sidecar — resume at the
  first uncovered entry's ordinal without rescanning the covered prefix to
  count lines. The extender writes records first and the header last, so a
  reader never trusts a record past the counts.
- An extender first truncates each table to the header's record count. That
  removes whatever a crashed extension left past the counts. In-place writes are
  a function of the entries alone, so redoing one after a crash writes the same
  bytes.
- A rebuild writes a new sidecar and renames it into place. A reader holding
  the old file sees the incarnation change on its next validation and reopens.

A window read binary-searches item records with `ReadAt`. It projects each item
from its contributor entries and stamps each turn from its summary record. It
never decodes a whole turn or the whole file. Today's index projects whole groups
instead (`turn_index.go:1134-1165`; `item_paging.go:302-352`), and one real turn
spans 72 MB and 8,882 entries.

This replaces the JSON index that today is unmarshalled whole or held in a cache
(`internal/apptranscript/turn_index.go:91-119, 551-553`;
`server/appwire_runtime.go:80`). It also replaces the per-group rank arithmetic
in `item_paging.go:176-234`.

**Validation.** The transcript is still an extension of what the index covers
when, in order: the file's identity is the one the index recorded (device and
inode, or the platform's equivalent; empty when the platform exposes neither,
which then falls through to the rest of the check), the file is at least as
long as the covered length, and the last 4 KB of the covered prefix hashes
(SHA-256) to what the index recorded for it. Any failure rebuilds. This is the
check the attention fold cursor already proved (PR #2254). The full prefix
rehash (`turn_index.go:571-584`) goes away.

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
- `roundID` (on the ASSISTANT and salvage entries)
- `OriginalOrdinal`
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

## Implementation decisions

These came from the last spec review round and are pinned as tests in the
phase 3 plan.

- **Incarnations** compare by equality only. A client normally issues one
  latest-window read per thread at a time; an invalidation can leave an older
  one still outstanding alongside the fresh read it issues. Either way the
  client applies only the response whose request generation is the newest
  issued so far for that thread (see Which response wins, under Crash/boot
  generation).
- **After an incarnation change**, the latest-window response replaces the whole
  history. The client then backfills older pages again as it needs them.
- **Queue overflow during a rebuild** restarts the rebuild through a new boundary
  ordinal. It repeats until the queue is covered, up to the same three-attempt
  cap that bounds plain rebuild failures; a projector that never catches up
  enters the failed state instead of restarting forever.
- **Completion entries.** An unrecorded completion entry fails the session
  closed, the same as COMMUNICATE. A turn's terminal status is never silently
  lost.
- **Tool items** carry the ordinal of their completing TOOL_RESULTS entry, as a
  separate `completedAtEntry` metadata field (`ThreadItem.completedAt` already
  carries the completion timestamp, so the ordinal takes a distinct name). Key
  and position stay tied to the opener, so an item never moves when it
  completes. The field makes the rule for dropping overlay execution state
  decidable from wire data.
- **Backfill pages during an incarnation change.** A client that has seen a new
  incarnation defers any backfill page from the new incarnation until the
  latest-window replacement for it has applied. It discards backfill pages from
  any other incarnation.
- **The prelude** has its own `TurnKind` value, `prelude`.

## Acceptance criteria

**Memory.** Measure on a long-lived daemon running a copy of the 260-delegate
coordinator session, after replaying its activity rather than after a cold
restart:
- Retained history memory is 0, beyond the projection state (the open turn's
  accumulators plus the open round's call records) and the index handle cache
  (64 handles).
- Ephemeral notices stay within 64 KB per thread and 16 MB daemon-wide.
- Running state (streams, running tool output, held images) is bounded per
  response and per call. It is released at round end, so at idle it is zero.
- For comparison, the same measurement is about 283 MB today.

**Latency.** `thread/read` of the latest window at the default page size, on the
95 MB root transcript, on the dev machine's local SSD, 200 samples:
- idle writer: p99 under 50 ms, and no worse than 2× today's full-page read
  after a notification (the read an active client gets)
- appending one entry per 100 ms: p99 under 100 ms

This is prototyped and measured in phase 1, before any irreversible step.

*Amended after the phase 1 measurement (PR #2303).* The original relative
criterion compared against today's cached read. That read reuses a paging index
the server rebuilds after every applied notification, so an active session
rarely hits the cache.

Phase 1 measured p99 on real 101–134 MB transcripts, 40 items per page:

| Read | Index | Today, cached | Today, after a notification |
|---|---|---|---|
| Full page | 5.6–8.7 ms | 2.1–5.9 ms | 8.3–26.3 ms |
| Page while appending | 1.4–12.3 ms | n/a | n/a |

Both absolute limits are met with a wide margin. Against the cached read the
index is 1.2–3× slower. Against the read an active client actually gets, it is
equal or faster.

The remaining cost is decoding large tool state in the returned entries. Five
`task_list` results of about 500 KB each dominate one window. That is a data-size
problem to fix at its source, not by caching decoded entries.

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
     - failure and retry, both inside a round and as a new turn after
       TURN_FAILURE, on legacy and new-format transcripts
     - goal continuation
     - delegate attention
     - a restart
   - It passes by listing known divergences in a table, each tagged with the
     phase that removes it. It fails on a new divergence, and on a listed
     divergence that no longer occurs.
   - A prototype of the seekable index measures file-window reads on a copy of
     the 95 MB root transcript and on the 134 MB coordinator transcript, against
     the latency criteria. It has to build complete turns and items (status,
     usage, tool results) that equal the whole-file projection before any timing
     counts. *Done in PR #2303. The results and the amended criterion are under
     Acceptance criteria.*
2. **Write the new fields.**
   - Writers write every new field and entry kind. The consumers listed above
     skip the new kinds, and the new kinds stay out of in-memory history. Every
     projection rule and client-visible behavior stays as it is today.
   - `Seq` on rollback, the explicit write result and fork sequence seeding
     also land here. The registry has already shipped ahead of phase 1.
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

   Deterministic boundary tests are activation prerequisites, alongside
   final-state parity. Each one pauses the system at a synchronization boundary:
   - a read between the ASSISTANT and the COMMUNICATE entry
   - a paginated read that overlaps a cross-process rollback
   - a failed history publication, and a failed resync delivery
   - a crash that loses buffered entries: clients replace on the higher boot
     generation and never keep the lost entries, including when the truncated
     file matches the index's covered length
   - an unrecorded append on a poisoned writer and on a missing writer: the
     session fails closed
   - an index extension or rebuild killed between record writes and the header
     commit, including while another process holds the shared lock: the next
     read redoes it to a correct projection, with no duplicate or torn records
   - a projection queue overflow: the thread resyncs and no entry is lost
   - three consecutive rebuild failures: the thread enters the failed state,
     recording continues, reads return `ErrorTranscriptHistoryFailed` while
     clients keep their history, and a restart recovers
   - a read that overlaps a retry reset and the next attempt's deltas
   - a turn that recovery reclaims
   - a read whose cut is captured before a TOOL_RESULTS append and whose
     projection runs after it
   - a daemonless client holding a tool call's page when its TOOL_RESULTS lands
   - an async attention write after a turn's completion
   - a delegate moving between an unqualified and a qualified boot-generation
     token, and between two qualified tokens with different owners: each
     replaces even when the incoming counter is numerically lower
   - an entry that fails to decode is quarantined and history continues past
     it, live and on reload, with no resync and no failed-history state; a
     builder error over a decodable entry instead takes the rebuild and
     failed-state path
   - a request whose held length predates what the bounded update log still
     retains gets a full latest-window replacement, not a stale "no updates"
     answer
4. **Cleanup and docs.** Remove dead code and tests. Amend the atomic paging spec
   and `docs/appwire-protocol.md`.

Each phase gets its own implementation plan once this design is accepted and the
previous phase has landed.

## Risks

- **The mobile app must update** after phase 3.
- **Old cursors and stored anchors** go stale or miss once.
- **Duplicate `Seq`s in existing transcripts** are handled by the entry ordinal.
  Nothing new depends on `Seq` uniqueness.
- **Read latency moves to disk.** The phase 1 gate bounds it before anything
  irreversible ships.
- **Phase 3 is large.** It stays reviewable as a stack of commits, and the parity
  harness and the acceptance criteria gate it.
- **Delivery turns** display attention deliveries as their own turns, which
  changes how delegate-attention sessions look.
