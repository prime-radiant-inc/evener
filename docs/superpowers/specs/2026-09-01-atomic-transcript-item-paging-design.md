# Atomic Transcript Item Paging

## Status

Approved for planning. This specification replaces the browser transcript's
turn-count paging contract with projected-item paging. It does not adopt or
depend on PR #757.

> **Flag-day correction (2026-09-05):** The approved
> [AppWire item-only flag-day design](2026-09-05-appwire-item-only-flag-day-design.md)
> supersedes the compatibility and rollout portions below. AppWire v4 is the
> sole browser/daemon contract: there is no turn-mode fallback, capability
> negotiation, downgrade, or dual-version rollout. The item semantics,
> cursor/fragment rules, identity guarantees, and byte budget in this
> specification remain requirements unless the v4 design states otherwise.

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
- Existing turn-based clients are outside the v4 contract; callers migrate in
  the same flag day and must use bounded item reads.

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

The v4 contract removes legacy `turnLimit`, transcript-list `limit`, numeric
turn cursors, and the `TranscriptPageUnit` selector. Requests and responses
are item-only; the protocol version rejects an incompatible daemon rather than
silently changing or negotiating a legacy request.

The v4 wire contract retains the item-fragment and position vocabulary:

```go
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

Item-only requests carry `itemLimit`; the browser sends 40. Item responses do
not carry a page-unit selector. Numeric legacy cursors and retired fields are
invalid at the request decoding boundary.

Each returned item carries:

```go
TranscriptKey string             `json:"transcriptKey,omitempty"`
Position      *ThreadItemPosition `json:"position,omitempty"`
```

The fields are mandatory in v4 item responses.
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

## Migration

The v4 flag day replaces the former compatibility rollout: generated types and
the strict request decoder remove the retired fields, then saved/live sources,
the browser, harnesses, and other callers switch together. There is no
turn-mode retention step or compatibility facade. The implementation order
remains useful as historical context: add positioned sources, the shared
post-enrichment packer, frontend fragment normalization, and stale-page
fencing, then use `itemLimit: 40` for initial and older reads.

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
