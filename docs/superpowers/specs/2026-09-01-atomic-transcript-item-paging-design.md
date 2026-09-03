# Atomic Transcript Item Paging

## Status

Approved for planning. This specification replaces the browser transcript's
turn-count paging contract with projected-item paging. It does not adopt or
depend on PR #757.

## Evidence and problem

The current browser path budgets transcript reads in whole turns. A turn may
contain one very large item or many items, so a small turn count can still
produce a multi-megabyte response. The trace investigation confirmed that
bulk transcript and navigation reads dominated recorded traffic and that a
16-turn transcript was returned in full because the limit was a turn count,
not an item or byte budget.

The storage and projection layers also use different units:

- a transcript JSONL entry can project zero, one, or many AppWire
  `ThreadItem`s;
- saved-transcript indexes currently record entry visibility, not projected
  item cardinality or an intra-entry boundary;
- daemon snapshots and browser state group items into turns and page the turn
  array;
- the frontend can destructively pair tool calls and results before separate
  pages have been joined.

Consequently, an entry count, visible-turn count, and atomic projected-item
count are not interchangeable.

## Goals

- Initial reads return the newest **at most 40 atomic projected items**.
- Backfill reads return the previous **at most 40 atomic projected items**.
- A page may split a turn and may split the items projected from one transcript
  entry.
- The cursor denotes the exclusive position immediately before the oldest item
  returned. Empty means no older projected item exists.
- A page's final serialized AppWire result targets at most **1 MiB** after all
  URL, image, and cost enrichment. If the nearest item alone exceeds 1 MiB,
  return that one item rather than an empty page.
- Historical fragments, live lifecycle updates, reload, reconnect, and resync
  merge without replay, omission, duplication, or state downgrade.
- Saved and live sources implement one cursor and page-result contract.
- Cursor and item identity remain stable under transcript append.
- Existing turn-based clients remain compatible during an explicit versioned
  migration.

## Non-goals

- Do not page raw model streaming deltas or internal transcript records.
  `ThreadItem` is the atomic browser-visible unit.
- Do not use PR #757's resume sidecar as the browser paging index.
- Do not couple paging availability to delegate-attention folds, delivery
  deduplication snapshots, `PrefixTurnCount`, or any resume optimization.
- Do not retain arbitrarily deep backfill across reconnect or resync. A fresh
  bounded read may replace the loaded history.
- Do not change transcript content, redaction, or tool-result semantics.
- Do not make a provider, credential, network service, quota, ambient clock, or
  live model part of the default test suite.

## Terminology

- **Atomic item:** one final projected AppWire `ThreadItem`, including command,
  tool call, tool result, user, assistant, diagnostic, or other supported item
  variants. A started item and its completed form are lifecycle versions of
  one atomic item, not two positions.
- **Entry ordinal:** the absolute zero-based position of a decoded transcript
  entry in its transcript incarnation.
- **Projected-item ordinal:** the zero-based item position emitted by the
  projection of one entry.
- **Item position:** `(entry ordinal, projected-item ordinal)`. Prelude items
  use a reserved, versioned coordinate range and never collide with transcript
  entries.
- **Transcript key:** stable item identity reproduced by live event projection
  and later file projection. Display IDs may remain separate.
- **Turn fragment:** a `Turn` carrying only the returned subset of that turn's
  items plus explicit completeness metadata.

## Protocol

### Version negotiation

Extend the existing thread read/list contracts rather than silently changing
legacy `turnLimit` and numeric cursors. The request selects item paging
explicitly, and the response identifies its unit.

The Go wire contract adds these named types and fields:

```go
type TranscriptPageUnit string

const (
    TranscriptPageUnitTurn TranscriptPageUnit = "turn"
    TranscriptPageUnitItem TranscriptPageUnit = "item"
)

type TurnItemsView string

const (
    TurnItemsViewFull     TurnItemsView = "full"
    TurnItemsViewFragment TurnItemsView = "fragment"
)

type ThreadItemPosition struct {
    Entry uint64 `json:"entry"`
    Item  uint32 `json:"item"`
}
```

Item-mode requests carry `itemLimit`; the browser sends 40. Legacy
`turnLimit` remains valid only in turn mode. Supplying both is invalid. Item
mode responses carry `pageUnit: "item"`; legacy responses carry or imply
`"turn"` during migration.

Each returned item carries:

```go
TranscriptKey string             `json:"transcriptKey,omitempty"`
Position      *ThreadItemPosition `json:"position,omitempty"`
```

The fields are mandatory in item mode and omitted only for legacy responses.
Each returned turn carries:

```go
ItemsView       TurnItemsView `json:"itemsView,omitempty"`
HasEarlierItems bool          `json:"hasEarlierItems,omitempty"`
HasLaterItems   bool          `json:"hasLaterItems,omitempty"`
```

`itemsView: "fragment"` is mandatory in item mode, including when the fragment
happens to contain every currently known item in that turn. Only a legacy or
explicit complete snapshot may say `"full"`.

### Cursor

The cursor is opaque to clients and versioned by the server. Its decoded form
binds all of:

```go
type transcriptItemCursorV1 struct {
    Version           uint8
    ThreadRef         string
    Incarnation       string
    ProjectionVersion uint16
    Before            ThreadItemPosition
}
```

`Before` is the exclusive position immediately before the oldest returned
item. The cursor must be stable when later entries/items append. It is invalid
when the thread reference, transcript incarnation, or projection version no
longer matches. A malformed, mismatched, future, or no-longer-reconstructible
cursor returns the existing structured invalid-params class with a stable
item-cursor reason; it must not clamp or silently restart from newest.

A client recovers from a stale cursor by issuing a fresh item-mode
`thread/read` and replacing its bounded transcript state.

### Page selection

For initial reads, walk projected positions backward from the end. For
backfill, walk backward from the decoded cursor. Zero-item entries consume no
quota. Select nearest older items until either:

1. 40 items have been selected; or
2. adding the next item would make the final serialized result exceed
   1,048,576 bytes.

Return selected items in chronological order. If the first selected item alone
exceeds 1,048,576 bytes, include it and stop. The cursor is computed from the
oldest returned item's position, not from a turn boundary or source entry
boundary.

### Serialized-size definition

The soft target covers the JSON serialization of the typed AppWire RPC
`result`, not source JSONL bytes and not the outer JSON-RPC envelope. Measure
the production serializer output after every response transformation that can
change bytes, including image URLs, document URLs, costs, and other hub
enrichment.

Sources return positioned candidates. A shared outer packer enriches and
packs the final result so saved and live paths cannot disagree about the byte
budget. The result is a target, not an item-truncation rule: item content is
never split or shortened to fit it.

## Saved-transcript indexing

Extend the saved index with projection-versioned item cardinality and enough
boundary data to seek to an entry and resume inside its projected item list.
The index remains rebuildable from the transcript and must fail closed on
identity, append-validation, or projection-version mismatch.

The core interface is:

```go
type TranscriptItemCandidate struct {
    TurnID          string
    Turn            appwire.Turn
    Item            appwire.ThreadItem
    Position        appwire.ThreadItemPosition
    HasEarlierItems bool
    HasLaterItems   bool
}

type TranscriptItemWindow struct {
    Candidates  []TranscriptItemCandidate
    OlderCursor string
}
```

Projection produces stable keys and positions before transport enrichment.
An entry that emits more than 40 items is resumable at every intra-entry
position.

PR #757's validated offset and `PrefixEntryCount` may be reused only as an
optional, already-validated way to establish absolute entry numbering during
index construction. `PrefixTurnCount` is legacy turn metadata and must not be
part of item cursors, item availability, or index validity. Missing, incomplete,
corrupt, or oversized resume sidecars leave item paging fully operational.

## Live snapshot and lifecycle rules

The daemon stores positioned items and supports the same item-window request as
the saved index. A started item receives its final `TranscriptKey`; completion,
delta, reset, and tombstone events update that key in place. The later saved
transcript projection must reproduce it.

The live source may return more positioned candidates than the final page can
hold. The hub enriches and packs them, requesting another candidate batch only
when needed to fill the 40-item/1-MiB page without skipping positions.

## Frontend model and merge rules

The browser normalizes loaded transcript state by `turnId` and
`transcriptKey`. It keeps one logical turn even when three pages contribute
three fragments.

- Prepending a historical fragment inserts only unseen older keys at their
  transcript positions.
- When a historical item collides with a live item, the live/newer lifecycle
  value wins.
- `item/started` is an upsert, not unconditional append.
- Completion, delta, reset, and tombstone events update the same key.
- `turn/completed` merges fragment scalars and items. Only
  `itemsView: "full"` may replace a turn's entire item list.
- Tool call/result pairing runs over the complete loaded raw-item set, not one
  page. A call and result on opposite page boundaries remain represented and
  pair exactly once.
- Display projection coalesces fragments before rendering. One logical turn
  produces one React/virtual-list key and one set of separators.

An in-flight older-page request captures its cursor and the thread hydration
incarnation. If reconnect, resync, release, or a newer accepted page changes
either fence, discard the late result.

## Reconnect and resync

A successful reconnect or `evener/thread/resync` performs a fresh item-mode
read and replaces bounded transcript history. Previously backfilled history may
be discarded. Live notifications that occur after the resync cut are then
applied normally. A response issued before the cut cannot prepend into the new
incarnation.

## Migration

1. Add item-mode wire types and generated TypeScript without changing legacy
   behavior.
2. Add saved and live positioned-candidate sources behind focused interfaces.
3. Add the shared post-enrichment page packer.
4. Add frontend fragment normalization and stale-page fencing.
5. Switch the browser's initial and older reads to item mode with `itemLimit:
   40`.
6. Retain turn mode for older protocol clients until catalog/support evidence
   shows no caller remains. Removal is a separate, explicit cleanup.

## Security and privacy

- Cursors contain no transcript content, prompt text, filesystem paths, raw
  titles, or secrets. They are encoded opaque state and validated as untrusted
  input.
- Errors identify the violated cursor contract without echoing cursor bytes.
- Logs and metrics record counts, serialized bytes, source kind, and fallback
  reason only; never item content or identities.
- Existing response authorization and redaction boundaries remain unchanged.

## Acceptance criteria

- Initial and backfill pages contain at most 40 atomic projected items, except
  that the byte rule may reduce the count.
- Two ordinary items whose final result would exceed 1 MiB are not returned
  together; one oversized item is returned alone.
- An entry that projects 41 items can page at every intra-entry boundary with
  no replay or omission.
- A turn split across at least three pages renders as one turn with stable item
  order and no duplicate keys/separators.
- A tool call/result split across pages pairs exactly once and neither source
  item is lost.
- A live completion followed by backfill/reload does not duplicate or downgrade
  the item.
- A pre-resync older-page response is discarded.
- Appending live entries does not change an existing cursor's meaning.
- Stale projection/incarnation cursors return a typed error and recover through
  a fresh read.
- Failure or absence of any resume sidecar does not disable saved or live item
  paging.
- Deterministic focused tests, generated-output checks, `make lint`, `make vet`,
  `make test`, `make test-web`, and Chrome-capable browser guards pass.
