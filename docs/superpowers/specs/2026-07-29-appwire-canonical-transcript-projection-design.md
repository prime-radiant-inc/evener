# AppWire Canonical Transcript Projection Design

**Status:** Superseded

**Date:** 2026-07-29

**Superseded by:**
`2026-07-29-appwire-authoritative-rejoin-design.md`. The replacement keeps
the existing transcript and wire identities, materializes one daemon-owned
snapshot, and makes client hydration self-healing without adding transcript
format v3 or public stream epochs.

**Scope:** Serf daemon transcript identity, live AppWire projection, transcript
paging, and atomic subscribing hydration.

## Decision

AppWire v2 will use one durable projection identity and one canonical
materialized projector for both reload and live delivery.

Every semantic transcript entry carries the logical AppWire turn ID and the
item IDs needed to project that entry. The same session-owned identity state
stamps live `SessionEvent` envelopes before they leave the agent. Transcript
projection groups entries by the persisted logical turn ID and uses the
persisted item IDs. The live projector consumes the supplied IDs instead of
minting a second namespace.

The daemon seeds its canonical materialized AppWire state from the grouped
transcript at identity installation, then advances that state only with
committed live notifications. Notification replay retention remains a bounded
transport concern and never defines snapshot history or identity. A
subscribing `thread/read` clones this canonical state under the same
appserver projection boundary that records its notification cut.

This is a flag-day change. There is no compatibility reader, identity
synthesis, or migration path for older transcripts.

## Problem

The current daemon has two projection authorities:

1. transcript replay assigns one `turn_N` per transcript entry and derives
   item IDs from entry/content indexes; and
2. the live event projector assigns one `turn_N` per conversation turn and
   allocates item IDs from its own counters.

The subscribing response cut is defined by the live notifier, while the
snapshot may be chosen from the independently advanced transcript. This
creates three proven failures:

- the transcript can already contain a completed assistant response while its
  live delta/completion remains after the cut, so hydration displays the answer
  twice under different IDs;
- an idle transcript snapshot can contain the prior assistant at `turn_2`,
  after which the next live conversation also starts `turn_2` and replaces the
  prior answer in the frontend; and
- choosing the live projection during an active turn makes bounded notifier
  retention accidentally define history, dropping older transcript turns and
  returning an empty paging cursor.

Stable frontend deduplication cannot repair these failures. The producer must
provide one identity domain and a snapshot whose authority is aligned with its
notification cut.

## Invariants

1. **One logical identity.** A renderable transcript entry has exactly one
   non-empty logical turn ID. Every projected item has a non-empty item ID.
   IDs are stable across transcript replay, daemon restart, live streaming,
   retry-safe mutation replay, and forked-session prefix copying.
2. **One conversation turn.** User input, assistant responses, tool calls,
   tool results, steering incorporated into that turn, and terminal failure
   group under the same logical turn ID. Transcript pages never split that
   group.
3. **One item identity.** A tool call and its later result update the same item
   ID. Assistant/reasoning reset allocates a replacement item ID and the final
   transcript entry persists the surviving ID.
4. **One materialized authority.** The daemon's `appTurnSnapshot` is seeded
   from grouped transcript state and then updated from committed live
   notifications. Subscribing hydration never chooses between a transcript
   snapshot and a notifier-derived snapshot.
5. **Transport retention is not state retention.** Evicting notifier replay
   records cannot remove turns or change the snapshot cursor.
6. **One response cut.** `CaptureSubscription` clones the canonical materialized
   state and records the notifier sequence under the same projection commit
   boundary. State represented by the snapshot is not replayed after the
   response; later notifications are delivered once in producer order.
7. **Paging is logical-turn paging.** Latest-window and older-page cursors count
   grouped logical turns, not transcript entries. Appending another entry to
   the current logical turn does not move older-page boundaries.
8. **No fallback namespace.** Missing required projection identity is invalid
   format/state. The v3 reader and live projector do not synthesize entry-index
   IDs or silently fall back to local counters.

## Durable format

Transcript `format_version` becomes `3`.

`schema.Turn` gains:

```go
type ProjectionIdentity struct {
	TurnID  string   `json:"turn_id"`
	ItemIDs []string `json:"item_ids,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Text    string   `json:"text,omitempty"`
}

type Turn struct {
	// Existing semantic fields remain.
	Projection ProjectionIdentity `json:"projection"`
}
```

`ItemIDs` is aligned with the ordered semantic item keys produced for the
entry. The shared identity package defines those keys; transcript and live
projection do not independently guess the alignment. Reusing an item ID across
entries is permitted and required when a tool result settles an earlier tool
call. IDs within one entry must be non-empty and unique unless the shared item
key reducer explicitly describes an update to an existing item.

`Projection.Kind` and `Projection.Text` carry the minimum presentational
semantic needed when the model-facing message intentionally differs from the
human-facing item. The first required case is goal continuation: the transcript
keeps the full model prompt in `Turn.Message`, while projection keeps the
compact marker in `Projection.Text`. This avoids persisting an AppWire payload
or exposing the full continuation scaffolding.

The existing `ClientMutationID` and `StableTurnID` remain mutation bookkeeping.
For client-authored user/steering entries, `StableTurnID` must equal
`Projection.TurnID`.

The writer validates v3 identity before appending. `DecodeEntry` rejects a
missing turn ID, malformed item IDs, and client-mutation turn-ID disagreement.
The reader does not accept v1/v2 as v3 and does not synthesize identity for
them.

## Session-owned identity

A new `agent/projection_identity.go` owns:

- monotonically increasing turn and item sequences;
- the active logical turn;
- the surviving assistant and reasoning item identities;
- semantic item-key to item-ID correlation;
- tool-call-ID to item-ID correlation; and
- the current standalone announcement/gap turn.

It is seeded from v3 transcript entries and durable client-mutation
reservations during session creation/restore. Numeric allocation advances
above every persisted or reserved ID before the session emits startup/live
events.

The two central transcript doors, `writeTranscript` and
`writeTranscriptDurable`, identify a turn before holding or appending it.
`Session.sendEvent` stamps `SessionEvent.TurnID` and `SessionEvent.ItemID` from
the same state before delivery. Ordering-sensitive pairs—assistant start and
append, tool start and result, error and failure entry, steering event and
entry—therefore reuse one allocation.

Retry-safe client mutation acceptance pre-reserves a canonical turn ID before
entering the mutation-store transaction and passes the value into the atomic
record update. Rejected operations may consume an ID; monotonic gaps are valid.
This avoids acquiring the projection-identity mutex while the mutation store
owns its serializer and avoids the reverse lock order during session
incorporation.

Forking preserves identities for an unchanged copied prefix. A replacement or
new child-local entry receives a new identity allocated above the copied
prefix.

## Transcript projection and paging

`internal/apptranscript` consumes persisted identities:

- `ProjectTurn` uses the persisted item IDs and shared semantic item-key order;
- consecutive visible entries with the same turn ID fold into one
  `appwire.Turn`;
- items upsert by item ID, allowing tool results and terminal state to settle
  existing items; and
- timestamps, usage, status, and failure metadata combine into the grouped
  turn.

The turn index stores each visible entry's logical turn ID and logical group
rank. Appending another entry to the current final group appends an index
record but does not increment the logical turn count. A requested page expands
the selected logical ranks to all entries in those groups before projecting.
No page begins or ends inside a logical turn.

The index version changes with the grouping contract. Existing index sidecars
are invalidated and rebuilt; this is cache invalidation, not transcript
compatibility. The transcript turn cache stores grouped turns and invalidates
the prior index version.

## Canonical daemon projection

`appTurnSnapshot` becomes state storage rather than a replay-window cache:

- `Seed([]appwire.Turn)` installs grouped transcript state;
- `Apply([]SequencedNotification)` incrementally upserts canonical turn/item
  state; and
- `Snapshot()` returns a deep clone.

It no longer stores notifier records, retained lower bounds, or a replay-size
limit, and it never rebuilds itself from `Notifier.RetainedWindow`.

Identity installation and transcript-path installation converge on one seed
operation under `appserver.CommitProjection`. The operation validates the
transcript belongs to the current thread, loads grouped turns, seeds
`appTurnSnapshot`, and installs a live projector that consumes supplied
event identities. Clear/replacement increments the identity generation so an
older seed cannot publish into a newer session.

`RecordAppEvent` projects the identity-stamped event, records its notifications
in the notifier, and applies those same notifications to the canonical
snapshot inside one projection commit. There is no transcript-vs-notification
authority heuristic.

Subscribing `thread/read` always windows the canonical snapshot captured by
`CaptureSubscription`. Non-subscribing reads and `thread/turns/list` use the
same grouped transcript projection and logical cursors. Bounded notifier
eviction affects only reconnect replay outside the atomic snapshot contract.

## Canonical edge behavior

- **Goal continuation:** the live event and transcript entry share the logical
  turn/item IDs. `Projection.Kind="goal_continuation"` and
  `Projection.Text` carry the compact marker; the full model prompt remains in
  `Turn.Message`.
- **Turn failure:** live `EventError` emits the same failed system item that
  transcript replay projects, then completes the same logical turn. Reload
  does not invent an extra item shape.
- **Assistant retry/reset:** a reset retires the partial assistant item and
  allocates a new item ID. Only the surviving identity is persisted with the
  assistant transcript entry.
- **Tool calls/results:** call ID is the semantic correlation key; start,
  output, completion, and transcript result all address the same persisted
  item ID.
- **Standalone markers and hooks:** the session allocator assigns a standalone
  logical turn and persists/emits its item identity. The live projector has no
  private counter namespace.
- **Missing transcript:** a fresh session seeds an empty projector. A resumed
  session whose transcript is absent/unsupported continues to fail through
  the existing restore boundary; hydration does not fabricate state.

## Deleted compatibility surface

`cmd/serf-transcript-v2-upgrade` is removed. It cannot produce required v3
projection identity from v1 data without inventing grouping and item history,
which would be a migration/compatibility project explicitly outside this
flag-day change.

Documentation and tool descriptions that promise transcript v2 are updated to
v3. AppWire generated types do not change: `Turn.ID`, `ThreadItem.ID`, and
`ThreadItem.TurnID` already carry the canonical IDs.

## Verification

The implementation must preserve three deterministic WebSocket RED cases:

1. transcript persistence ahead of assistant delta/end cannot duplicate the
   answer across the response cut;
2. idle hydration followed by the next live turn cannot collide with or
   replace the prior assistant answer; and
3. active hydration after notifier eviction returns the complete latest
   logical-turn window and a cursor into older transcript history.

Additional gates cover:

- event/transcript identity parity for user, assistant, reasoning, parallel
  tools/results, steering, failure, goal continuation, model switch,
  compaction, and hooks;
- retry-safe mutation replay and restore without counter collisions;
- forked-prefix preservation and child-local allocation;
- grouped full-read/latest/page equivalence and pages that never split turns;
- append-to-current-group index/cache behavior;
- notifier eviction with unchanged canonical snapshot state;
- identity replacement/clear without stale seed publication;
- response-before-post-cut-notification ordering;
- focused stress and race repetition; and
- full Go, frontend, generation-drift, lint, and build checks.
