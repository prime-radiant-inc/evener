# Atomic Transcript Item Paging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace turn-count pagination with stable, opaque, backward pagination over the newest or previous 40 atomic projected `ThreadItem`s, while preserving partial turns, live state, tool pairs, and a soft 1 MiB final-result limit.

**Architecture:** Add an explicit item-paging mode to the existing AppWire thread read/list methods while retaining legacy turn mode unchanged. A shared paging package owns versioned cursor fences, absolute `(entry,item)` positions, stable transcript keys, atomic candidate selection, and turn-fragment regrouping. Indexed transcripts, live snapshots, and provider adapters return positioned candidates through an internal source contract; the hub enriches and packs the final typed result to the 40-item/1-MiB limits. The frontend merges fragments by turn ID and transcript key and replaces bounded state after a typed item-cursor failure.

**Tech Stack:** Go, JSON-RPC/AppWire, append-only transcript indexes, React, TypeScript, Zustand, Vitest, Chrome DevTools Protocol browser guards.

**Spec:** `docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`

## Global Constraints

- Work in a worktree created with `superpowers:using-git-worktrees`; do not implement on the planning branch.
- Apply strict TDD to every task: add the named failing test, run the exact focused command and observe the intended failure, implement the minimum production change, then rerun the focused command to green.
- Never weaken or delete an existing assertion. Keep all existing projection, notification, subscription-cut, transcript-index, and frontend live-update behavior outside this feature.
- The counted unit is one final projected `ThreadItem`, not a raw transcript delta, source turn, AppWire turn, or transcript entry.
- The default and maximum public page request is 40 items. A page may contain fewer items only at history exhaustion or after the hub applies the soft 1 MiB result target.
- Items remain chronological within a page. `olderCursor` and `nextCursor` point exclusively before the oldest returned item. Empty cursor means no older item exists.
- A page may split a turn and may split the items projected from one transcript entry. The wire still returns chronological `[]Turn`; a split turn is a normal fragment with the same turn ID and only that page's items.
- Cursor payloads are opaque, URL-safe, versioned, and fenced by thread reference, transcript incarnation, item-projection version, and an absolute `(entry,item)` position. Malformed, mismatched, future, or missing-boundary item cursors use the stable typed item-cursor invalid-params reason and trigger a fresh read; they are never clamped.
- Appends retain a transcript generation. Rebuilds, rewrites, item deletion, late front insertion, and any non-tail structural mutation rotate it. A projection-version change invalidates all old cursors.
- Never perform transcript file I/O while the daemon holds the subscription capture boundary. `PrepareAppIdentity` must seed all indexed state needed by live reads.
- The final size check is `json.Marshal` of the enriched typed outgoing RPC result; no outer JSON-RPC envelope or internal source-candidate state is counted. Keep at least one item even when that one-item result exceeds 1 MiB.
- Backfill, live notifications, and reconnect hydration must not duplicate, lose, reorder, or downgrade an item. A settled item never returns to `inProgress`, and current live content wins over older backfill for the same item ID.
- Tool calls and results may be on different pages. Preserve both halves until the frontend can reconcile them by `callId`; never discard an unmatched result.
- PR 757 is not a dependency. A separately validated `PrefixEntryCount` may inform absolute entry numbering, but `PrefixTurnCount` and resume-sidecar completeness must never gate item paging.
- Tests must be deterministic, use scripted boundaries, and contain no sleeps. Browser gates require Chrome.
- Before each Go task's green run and commit, run `gofmt -w` on that task's exact named `.go` paths only.
- Before frontend gates, run Biome on every named touched path under `frontend/src/`. Do not include `frontend/scripts/` in explicit Biome invocations; that directory is outside the enforced scope documented in `AGENTS.md`.
- After each task, stage only the exact named paths shown in that task.

## File Structure

### New files

- `internal/appitempaging/cursor.go` — versioned fence tokens, opaque cursors, absolute positions, and typed decode failures.
- `internal/appitempaging/cursor_test.go` — cursor round trips, append stability, malformed input, and stale-fence tests.
- `internal/appitempaging/page.go` — positioned candidate selection, partial-turn regrouping, completeness flags, and cursor construction.
- `internal/appitempaging/page_test.go` — exact 40-item, split-turn, split-entry, exclusive-boundary, and no-loss tests.
- `internal/apptranscript/item_paging.go` — indexed newest/previous item reads without projecting the historical prefix.
- `internal/apptranscript/item_paging_test.go` — index generation, partial-entry, append, rebuild, and cross-page tool tests.
- `cmd/evener-hub/internal/appsource/codex_item_paging.go` — adaptation from Codex native turn pages to AppWire atomic item pages.
- `cmd/evener-hub/internal/appsource/codex_item_paging_test.go` — Codex scan/cache/generation and partial-turn tests.
- `cmd/evener-hub/app_item_page_fit.go` — shared post-enrichment candidate packing, result measurement, and cursor construction.
- `cmd/evener-hub/app_item_page_fit_test.go` — exact byte-boundary and oversized-single-item tests.
- `cmd/evener-hub/app_rpc_item_paging_test.go` — protocol-level live, past, stale, and no-loss regressions.

### Modified files

- `appwire/types.go` — additive page-unit, item-limit, position/key, and turn-fragment fields while retaining turn mode.
- `appwire/errors.go`, `appwire/errors_test.go` — stable invalid-params item-cursor discriminator.
- `appwire/paging.go`, `appwire/paging_test.go` — item-mode validation and 40-item constants alongside unchanged turn pagination.
- `appwire/protocol.go` — additive item-mode catalog roots and migration documentation.
- `docs/appwire-protocol.md` — generated additive item-mode contract.
- `cmd/evener-hub/frontend/src/protocol/types.gen.ts` — generated additive TypeScript types.
- `internal/apptranscript/apptranscript.go`, `internal/apptranscript/apptranscript_test.go` — emit stable transcript keys and positioned projected candidates.
- `internal/apptranscript/turn_index.go`, `internal/apptranscript/turn_index_test.go` — index v9/journal v3 item counts and persisted generation.
- `server/appwire_turns.go`, `server/appwire_turns_paging_test.go` — positioned live snapshot and item paging.
- `server/appwire_runtime.go`, `server/appwire_runtime_test.go` — v4 read/list handlers and prepared paging identity.
- `cmd/evener-hub/app_threadread.go`, `cmd/evener-hub/app_threadread_test.go` — ended local-session item paging.
- `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/app_rpc_test.go` — enrichment followed by final-result fitting.
- `cmd/evener-hub/internal/appsource/local_daemon.go`, `cmd/evener-hub/internal/appsource/transport_seams_test.go` — explicit item-mode local-daemon forwarding while preserving turn mode.
- `cmd/evener-hub/internal/appsource/codex_source.go`, `cmd/evener-hub/internal/appsource/coverage_completion_test.go` — invoke the Codex atomic adapter instead of forwarding native turn cursors.
- `cmd/evener-hub/frontend/src/protocol/errors.ts` — item-cursor invalid-params discriminator.
- `cmd/evener-hub/frontend/src/protocol/model.ts` — transcript-key and position identity on normalized item state.
- `cmd/evener-hub/frontend/src/protocol/reducer.ts`, `cmd/evener-hub/frontend/src/protocol/reducer.test.ts` — partial-turn and cross-page tool reconciliation.
- `cmd/evener-hub/frontend/src/stores/threads.ts`, `cmd/evener-hub/frontend/src/stores/threads.test.ts` — 40-item requests, stale refresh, and in-flight generation fences.
- `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx` — automatic older-page integration coverage.
- `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx`, `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs` — real-browser paging and scroll-anchor regression.

## Task 1: Add Item Mode Without Breaking Turn Mode

**Files:**
- Modify: `appwire/types.go`
- Modify: `appwire/errors.go`
- Modify: `appwire/errors_test.go`
- Modify: `appwire/paging.go`
- Modify: `appwire/paging_test.go`
- Modify: `appwire/protocol.go`
- Modify: `docs/appwire-protocol.md`
- Modify: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Keeps: legacy `ThreadReadParams.TurnLimit`, `ThreadTurnsListParams.Limit`, numeric turn cursors, and turn-mode response behavior.
- Adds: explicit `pageUnit: "item"`, `itemLimit`, positioned/keyed items, fragment metadata, and a stable item-cursor invalid-params reason.

- [ ] **Step 1: Write failing additive compatibility tests**

Test all of the following:

1. A legacy read with `turnLimit`, or legacy list with `limit`, and no item page unit marshals exactly as before and normalizes with `WindowTurns`/`PageTurns` unchanged.
2. Item mode requires `pageUnit: "item"`, accepts `itemLimit` from 1 through 40, defaults omitted/non-positive item limit to 40, and rejects a value over 40.
3. Supplying both the applicable legacy limit and `itemLimit`, or `itemLimit` outside item mode, is invalid params.
4. Item responses carry `pageUnit: "item"`; legacy responses carry or imply turn mode.
5. Item-mode responses require every item to have a non-empty `transcriptKey` and position and every turn to have `itemsView: "fragment"`.
6. A stale or unreconstructible item cursor has `CodeInvalidParams`, stable `evenerErrorInfo: "transcriptItemCursorStale"`, and automatic fresh-read disposition without echoing cursor bytes.

```go
func TestNormalizeTranscriptItemLimit(t *testing.T) {
    for _, tc := range []struct { in, want int }{{0, 40}, {-1, 40}, {7, 7}, {40, 40}} {
        if got, err := NormalizeTranscriptItemLimit(tc.in); err != nil || got != tc.want {
            t.Fatalf("NormalizeTranscriptItemLimit(%d) = %d, %v; want %d", tc.in, got, err, tc.want)
        }
    }
    if _, err := NormalizeTranscriptItemLimit(41); err == nil {
        t.Fatal("item limit above 40 accepted")
    }
}
```

Run:

```bash
go test ./appwire -run 'Test(ThreadPagingMigration|NormalizeTranscriptItemLimit|TranscriptItemCursorError|ThreadItemModeJSON)' -count=1
```

Expected: FAIL because item-mode types and validation do not exist; the legacy fixtures remain green.

- [ ] **Step 2: Define the exact additive wire vocabulary**

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

Add `PageUnit TranscriptPageUnit` and `ItemLimit int` to `ThreadReadParams` and `ThreadTurnsListParams`; retain `ThreadReadParams.TurnLimit` and `ThreadTurnsListParams.Limit` exactly for legacy mode. Add `PageUnit` to both response envelopes. Add `TranscriptKey string` and `Position *ThreadItemPosition` to `ThreadItem`. Change the existing `Turn.ItemsView string` field to `Turn.ItemsView TurnItemsView`, then add `HasEarlierItems` and `HasLaterItems`. Use `omitempty` only on new fields; preserve existing legacy JSON tags and asserted output.

Item-mode validation makes key/position and fragment metadata mandatory. A fragment says `itemsView: "fragment"` even when it currently contains every known item in that turn; only legacy or explicit complete snapshots say `full`.

- [ ] **Step 3: Add the typed item-cursor invalid-params reason**

```go
const ErrorTranscriptItemCursorStale ErrorInfo = "transcriptItemCursorStale"

func TranscriptItemCursorStale() WireError {
    return WireError{
        Code:    CodeInvalidParams,
        Message: "transcript item cursor is stale; refresh the thread",
        Data: ErrorData{
            EvenerErrorInfo:  ErrorTranscriptItemCursorStale,
            RetryDisposition: RetryDispositionAutomatic,
        },
    }
}
```

Malformed, mismatched, future, or no-longer-reconstructible item cursors all use this stable non-echoing reason. Ordinary malformed non-item parameters keep `ErrorInvalidParams`.

- [ ] **Step 4: Generate and verify both modes**

```bash
make generate
go test ./appwire -count=1
make lint-generated
```

Expected: PASS; generated docs/types contain both `turnLimit` and `itemLimit`, and the protocol version is not bumped solely for this additive mode.

- [ ] **Step 5: Commit named paths**

```bash
git add -- appwire/types.go appwire/errors.go appwire/errors_test.go appwire/paging.go appwire/paging_test.go appwire/protocol.go docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(appwire): add atomic transcript item mode"
```

## Task 2: Implement Opaque Cursors and Atomic Turn Fragments

**Files:**
- Create: `internal/appitempaging/cursor.go`
- Create: `internal/appitempaging/cursor_test.go`
- Create: `internal/appitempaging/page.go`
- Create: `internal/appitempaging/page_test.go`

**Interfaces:**
- Produces: an opaque, URL-safe v1 cursor bound to thread reference, transcript incarnation, projection version, and exclusive item position.
- Produces: deterministic positioned candidate selection and fragment regrouping without transport enrichment.

- [ ] **Step 1: Write failing cursor and page tests**

Cover round trip, append stability, wrong thread/incarnation/projection, malformed/future/unreconstructible positions, unknown JSON fields, 40-of-45 selection, 5-item backfill, a turn split across three pages, an entry split inside its projected items, empty projections, and correct earlier/later fragment flags.

```bash
go test ./internal/appitempaging -run 'Test(Cursor|SelectCandidates|RegroupTurnFragments)' -count=1
```

Expected: FAIL because the package has tests but no implementation.

- [ ] **Step 2: Implement the exact cursor identity**

```go
const CursorVersion uint8 = 1

type CursorIdentity struct {
    ThreadRef         string
    Incarnation       string
    ProjectionVersion uint16
}

type transcriptItemCursorV1 struct {
    Version           uint8                      `json:"version"`
    ThreadRef         string                     `json:"threadRef"`
    Incarnation       string                     `json:"incarnation"`
    ProjectionVersion uint16                     `json:"projectionVersion"`
    Before            appwire.ThreadItemPosition `json:"before"`
}

func EncodeCursor(identity CursorIdentity, before appwire.ThreadItemPosition) (string, error)
func DecodeCursor(encoded string, want CursorIdentity) (appwire.ThreadItemPosition, error)
func RebaseCursor(encoded string, before appwire.ThreadItemPosition) (string, error)
```

Encode canonical JSON with `base64.RawURLEncoding`. Decode with `json.Decoder.DisallowUnknownFields`, reject a second JSON value, validate lengths and coordinates, and never log or echo the token. `DecodeCursor` must require every identity field to match and must let the caller validate that `Before` is not future and is still reconstructible. Every failure maps to `TranscriptItemCursorStale`.

- [ ] **Step 3: Implement candidate selection and fragment regrouping**

Use the spec's core candidate types:

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

func SelectCandidates(
    candidates []TranscriptItemCandidate,
    before *appwire.ThreadItemPosition,
    limit int,
) ([]TranscriptItemCandidate, bool, error)
func RegroupTurnFragments([]TranscriptItemCandidate) ([]appwire.Turn, error)
```

Require strictly increasing lexicographic positions and unique non-empty transcript keys. Select strictly before `before`, retain the newest normalized limit, return chronological candidates, and compute whether any older candidate exists. Regroup adjacent candidates by turn ID, copying scalar turn state but replacing items and setting `itemsView: "fragment"` plus accurate earlier/later flags. Never claim `full` in item mode.

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/appitempaging -count=1
go test ./appwire ./internal/appitempaging -count=1
git add -- internal/appitempaging/cursor.go internal/appitempaging/cursor_test.go internal/appitempaging/page.go internal/appitempaging/page_test.go
git commit -m "feat(transcript): add fenced atomic item pager"
```

## Task 3: Add Indexed Historical Item Paging

**Files:**
- Create: `internal/apptranscript/item_paging.go`
- Create: `internal/apptranscript/item_paging_test.go`
- Modify: `internal/apptranscript/apptranscript.go`
- Modify: `internal/apptranscript/apptranscript_test.go`
- Modify: `internal/apptranscript/turn_index.go`
- Modify: `internal/apptranscript/turn_index_test.go`

- [ ] **Step 1: Write failing indexed-paging tests**

Build deterministic transcript fixtures with:

- one assistant entry projecting 45 non-empty text/reasoning/tool items with stable transcript keys;
- empty and suppressed `communicate` entries between visible entries;
- a tool call in an older page and its result in the newest page;
- an append after the first cursor is minted;
- a transcript rewrite and an index-sidecar deletion.

Assert that newest plus previous pages contain every transcript key exactly once, the 45-item entry is split 5/40, the tool halves retain the same `callId`, append preserves the item-index incarnation, and rewrite/item-index rebuild makes the old cursor stale while a missing/corrupt resume sidecar does not disable paging. Install the existing read observer and assert the newest page does not project the unselected historical prefix.

Run:

```bash
go test ./internal/apptranscript -run 'TestIndexedItemPaging|TestProjectedItemPositions|TestItemPagingGeneration|TestItemPagingToolPair'
```

Expected: FAIL because item counts, generation, and item-page APIs do not exist.

- [ ] **Step 2: Give every projected item a stable key and position**

Keep the existing public projection behavior, but add a positioned-candidate path after filtering. Assign the absolute decoded-entry ordinal and the index in that entry's final visible projected slice, using `uint64`/`uint32` with checked conversion. Generate `TranscriptKey` from stable transcript identity and projection coordinates so started/completed live forms and later saved projection reproduce the same key. Prelude items use a reserved projection-versioned coordinate range and never collide with transcript entries.

Do not expose implementation-only entry indexes as a second wire coordinate. The wire `ThreadItem.Position` is the sole item-mode coordinate, and legacy projection omits it.

- [ ] **Step 3: Upgrade the rebuildable index**

Bump:

```go
const (
	turnIndexVersion        = 9
	turnIndexJournalVersion = 3
	ItemCursorProjectionVersion uint16 = 1
	itemIndexProjectionID          = "apptranscript-items-v1"
)
```

Use `ItemCursorProjectionVersion` in `CursorIdentity.ProjectionVersion`. Store and validate the separately named string `itemIndexProjectionID` only as the rebuildable index-format/projection fingerprint; never assign that string to the numeric cursor field.

Extend the disk and journal records:

```go
type turnIndexDisk struct {
	Incarnation string `json:"incarnation"`
}

type turnIndexJournalFrame struct {
	Incarnation string `json:"incarnation"`
}

type indexedTurn struct {
	ItemCount uint32 `json:"item_count"`
}
```

These fields are additions to the existing structures, not replacements for offsets, anchors, visibility counts, tool seeds, integrity stamps, or journal chaining. During a scan, project each entry once, record the final visible item count, and keep the existing tool-name seed/change machinery. Preserve `Incarnation` only when the existing item-index candidate independently validates and the change is append-only. A v8 item index, missing item index, bad item-index journal, rewrite, anchor mismatch, or projection-ID mismatch rebuilds v9 and creates a new persistent random incarnation with `crypto/rand`.

Do not read `PrefixTurnCount`. Do not require a resume sidecar. If a validated prefix-entry offset is introduced independently, add it only to the raw entry index before assigning positions.

- [ ] **Step 4: Implement indexed newest/previous candidate windows**

Use this API:

```go
type ItemWindowOptions struct {
    ThreadRef string
    Cursor    string
    Limit     int
}

func (c *TurnCache) LatestItemWindowFromFile(
    path string,
    maxLineBytes int,
    options ItemWindowOptions,
    project BoundedEntryProjector,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error)

func (c *TurnCache) PreviousItemWindowFromFile(
    path string,
    maxLineBytes int,
    options ItemWindowOptions,
    project BoundedEntryProjector,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error)
```

Walk item cardinalities backward to find only records intersecting the requested candidate batch. Reconstruct tool state at the earliest selected record with existing seed/change data, project those records, slice the boundary entry by stable ordinal, and return positioned candidates. Validate that the cursor boundary is not future and is still reconstructible; otherwise return the typed item-cursor invalid-params error. Zero-item records do not consume quota.

The index owns and persists the cursor incarnation. Appends preserve it after the existing independent append validation; rewrites, rebuilds, projection changes, and index identity failures rotate it. Missing, corrupt, incomplete, or oversized resume sidecars are irrelevant to this index and cannot disable paging.

- [ ] **Step 5: Verify**

```bash
go test ./internal/apptranscript -run 'TestIndexedItemPaging|TestProjectedItemPositions|TestItemPagingGeneration|TestItemPagingToolPair'
go test ./internal/apptranscript
```

Expected: PASS, including all existing v8-rebuild, append-journal, projection, usage, and failure-count tests after their expected version values are updated to v9/v3.

- [ ] **Step 6: Commit the named paths**

```bash
git add internal/apptranscript/item_paging.go internal/apptranscript/item_paging_test.go internal/apptranscript/apptranscript.go internal/apptranscript/apptranscript_test.go internal/apptranscript/turn_index.go internal/apptranscript/turn_index_test.go
git commit -m "feat(transcript): page indexed history by projected item"
```

## Task 4: Page the Live Daemon Snapshot by Stable Item Position

**Files:**
- Modify: `server/appwire_turns.go`
- Modify: `server/appwire_turns_paging_test.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/appwire_runtime_test.go`

- [ ] **Step 1: Write failing live-snapshot tests**

Add tests proving:

- a seeded turn with 45 items yields a 40-item latest fragment and a 5-item previous fragment;
- two pages can share the same turn ID without duplicate item IDs;
- deltas and `item/completed` updates keep the original item position;
- a tail-appended live item does not stale an existing cursor;
- a reset deletion and a late prelude insertion rotate generation and stale an existing cursor;
- reading with `Subscribe:true` still captures one coherent snapshot/notification cut and performs no transcript file read;
- the runtime maps `ItemLimit`, returns positioned item fragments, and propagates typed stale errors.

Run:

```bash
go test ./server -run 'TestAppTurnSnapshotItemPaging|TestAppTurnSnapshotCursorGeneration|TestAppWireItemPagingSubscriptionCut'
```

Expected: FAIL because `appTurnSnapshot` is still turn-indexed and calls decimal `WindowTurns`/`PageTurns`.

- [ ] **Step 2: Add position ownership to the snapshot**

Extend the snapshot with internal position state:

```go
type appTurnSnapshot struct {
	mu                   sync.Mutex
	threadRef            string
	transcriptIncarnation string
	incarnationEpoch     uint64
	turns                []appwire.Turn
	turnIndex            map[string]int
	itemPositions        map[string]appwire.ThreadItemPosition // keyed by transcript key
	nextLiveEntry        uint64
	activeTurnID         string
}

type appTurnSeed struct {
	Turns                   []appwire.Turn
	ThreadRef               string
	TranscriptIncarnation string
	NextEntry               uint64
}
```

Change `Seed` to accept `appTurnSeed`. `NextEntry` is the absolute count of all decoded transcript entries, including zero-item entries, so zero unambiguously means an empty transcript and the next live entry always receives its actual ordinal. Persisted and live transcript items both use `(absolute decoded-entry ordinal, final projected-item ordinal)`. Allocate a live item's position from `NextEntry` and the shared live/file projector's final visible-item ordering so later saved projection reproduces the same position and `TranscriptKey`. Reserve the special projection-versioned coordinate range only for prelude items. Completion, delta, reset, and tombstone update the started key in place.

When an existing transcript key is updated, retain its position. Tail insertion allocates one new position without rotating the incarnation. Deletion, front insertion, or reordering increments `incarnationEpoch` and rotates the transcript incarnation.

- [ ] **Step 3: Replace turn paging methods**

```go
func (s *appTurnSnapshot) LatestItemCandidates(limit int) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error)
func (s *appTurnSnapshot) PreviousItemCandidates(cursor string, limit int) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error)
```

Both methods must hold the snapshot mutex only while cloning positioned state, then select and encode outside the lock. The returned internal candidate window includes cursor identity for the outer packer; no private metadata is added to the browser wire response. Keep item update merging, active-turn placement, notification ordering, and deep-clone guarantees unchanged.

- [ ] **Step 4: Wire prepared identity and runtime handlers**

`PrepareAppIdentity` must obtain or create the item-index incarnation while it already reads and projects persisted history, then seed `appTurnSnapshot` with the resolved thread reference, transcript incarnation, absolute decoded-entry count, stable keys, and positions. Reads use only the prepared in-memory snapshot.

Dispatch item-mode `thread/read` and `thread/turns/list` to `NormalizeTranscriptItemLimit`, `LatestItemCandidates`, and `PreviousItemCandidates`; keep the legacy turn-mode methods and numeric cursors unchanged. Preserve the existing one-subscription capture cut and `ReplaceSubscription` behavior.

- [ ] **Step 5: Verify**

```bash
go test ./server -run 'TestAppTurnSnapshotItemPaging|TestAppTurnSnapshotCursorGeneration|TestAppWireItemPagingSubscriptionCut'
go test ./server
```

Expected: PASS.

- [ ] **Step 6: Commit the named paths**

```bash
git add server/appwire_turns.go server/appwire_turns_paging_test.go server/appwire_runtime.go server/appwire_runtime_test.go
git commit -m "feat(server): page live snapshots by atomic item"
```

## Task 5: Adapt Ended Local Sessions and Codex Sources

**Files:**
- Modify: `cmd/evener-hub/app_threadread.go`
- Modify: `cmd/evener-hub/app_threadread_test.go`
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/evener-hub/internal/appsource/transport_seams_test.go`
- Modify: `cmd/evener-hub/internal/appsource/codex_source.go`
- Create: `cmd/evener-hub/internal/appsource/codex_item_paging.go`
- Create: `cmd/evener-hub/internal/appsource/codex_item_paging_test.go`
- Modify: `cmd/evener-hub/internal/appsource/coverage_completion_test.go`

- [ ] **Step 1: Write failing source tests**

Add tests for:

- an ended local transcript whose newest 40 items split one turn and one entry;
- a local cursor remaining valid after append and becoming stale after item-index rebuild, regardless of resume-sidecar availability;
- `LocalDaemonSource` forwarding `pageUnit: "item"`, `itemLimit`, and opaque cursor unchanged while legacy reads still forward `ThreadReadParams.TurnLimit` and legacy list calls still forward `ThreadTurnsListParams.Limit`;
- a Codex native turn containing 45 items being adapted into 5/40 AppWire item pages;
- a Codex append retaining generation, while a changed prefix rotates generation;
- tool call/result halves crossing two adapted Codex pages without either half disappearing.

Run:

```bash
go test ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -run 'TestPastThreadItemPaging|TestLocalDaemonItemPaging|TestCodexItemPaging'
```

Expected: FAIL because past reads call turn-page APIs and Codex forwards native turn cursors.

- [ ] **Step 2: Switch ended local reads to the indexed item API**

Dispatch bounded item-mode paths to `LatestItemWindowFromFile` and `PreviousItemWindowFromFile`; keep legacy `pastEntryLatestTurns` and `pastEntryPageTurns` behavior available for turn mode. Use the canonical session ref as `ThreadRef` and the rebuildable item-index incarnation as cursor incarnation. Preserve existing image projection, cost stamps, derived totals, divergence boundaries, failed-tool counts, skill catalog, and turn reconciliation.

An omitted or non-positive `itemLimit` now means 40, not an unbounded transcript. `IncludeTurns:false` remains a metadata-only read.

- [ ] **Step 3: Forward explicit item mode through the local-daemon source**

`LocalDaemonSource.ListTurns` and `ReadThread` must send `pageUnit: "item"` and `itemLimit` for the new browser path while retaining the legacy turn-mode forwarding path and its assertions. Preserve item positions, transcript keys, turn-fragment flags, and opaque cursor unchanged for outer packing.

- [ ] **Step 4: Implement a correct Codex adapter**

Do not pass an AppWire cursor to Codex's native `thread/turns/list`. Add an adapter cache with this contract:

```go
type codexItemSnapshot struct {
	ThreadRef      string
	Incarnation    string
	Candidates     []appitempaging.TranscriptItemCandidate
	TranscriptKeys []string
}

func (s *CodexSource) latestItemWindow(
	ctx context.Context,
	threadID string,
	limit int,
	itemsView string,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error)

func (s *CodexSource) previousItemWindow(
	ctx context.Context,
	threadID string,
	cursor string,
	limit int,
	itemsView string,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error)
```

Materialize native history by following native turn cursors to exhaustion, map every native turn, assign oldest-first absolute `(native turn ordinal,item ordinal)` positions, and cache the resulting transcript-key sequence. On refresh, retain the incarnation only when the old key sequence is an exact prefix of the new sequence. Any changed, removed, or reordered prefix rotates it. Use `codex:<threadID>` as the thread ref; a process-local incarnation is acceptable for the provider adapter because a restart intentionally makes old provider cursors stale, while append-only refresh within one process preserves it.

Use the adapter for both initial `ReadThread(IncludeTurns:true)` and `ListTurns`. Keep the Codex native wire version and native cursor semantics private to this adapter.

- [ ] **Step 5: Verify**

```bash
go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonItemPaging|TestCodexItemPaging'
go test ./cmd/evener-hub -run 'TestPastThreadItemPaging'
go test ./cmd/evener-hub/internal/appsource ./cmd/evener-hub
```

Expected: PASS.

- [ ] **Step 6: Commit the named paths**

```bash
git add cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_test.go cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/transport_seams_test.go cmd/evener-hub/internal/appsource/codex_source.go cmd/evener-hub/internal/appsource/codex_item_paging.go cmd/evener-hub/internal/appsource/codex_item_paging_test.go cmd/evener-hub/internal/appsource/coverage_completion_test.go
git commit -m "feat(hub): adapt transcript sources to atomic item pages"
```

## Task 6: Pack the Final Enriched Result to 40 Items and 1 MiB

**Files:**
- Create: `cmd/evener-hub/app_item_page_fit.go`
- Create: `cmd/evener-hub/app_item_page_fit_test.go`
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `cmd/evener-hub/app_rpc_test.go`
- Modify: `cmd/evener-hub/app_threadread.go`
- Modify: `cmd/evener-hub/internal/appsource/source.go`

**Interfaces:**
- Consumes: an internal source result containing positioned candidates, cursor identity, and history-exhaustion state.
- Produces: the public `ThreadReadResponse` or `ThreadTurnsListResponse` only after enrichment and exact typed-result measurement.

- [ ] **Step 1: Write failing count/byte-boundary tests**

Use deterministic text and image/document URL/cost enrichment to prove:

- 45 candidates pack as newest 40 with a cursor before the oldest returned item;
- an enriched result below `1_048_576` bytes remains unchanged;
- a result below the target before enrichment but above it afterward excludes nearest older candidates until it fits;
- exactly `1_048_576` bytes is accepted;
- two ordinary items whose combined result is oversized are not returned together;
- one oversized item is returned alone;
- excluded candidates appear exactly once on the next request;
- thread/read and thread/turns/list use identical packing rules;
- marshaling the public result exposes no internal cursor identity or candidate state.

```bash
go test ./cmd/evener-hub -run 'Test(PackTranscriptItemPage|AppRPCItemPageSoftLimit|AppRPCItemPageCountLimit)' -count=1
```

Expected: FAIL because source pages are currently turn-shaped and no shared final-result packer exists.

- [ ] **Step 2: Introduce one internal candidate-source result**

Keep cursor identity out of public AppWire JSON:

```go
type transcriptItemCandidateResult struct {
    Candidates appitempaging.TranscriptItemWindow
    Identity   appitempaging.CursorIdentity
    Exhausted  bool
}
```

Every saved, live-daemon, and provider adapter returns this internal shape to the hub. The local-daemon adapter reconstructs the identity only from authenticated thread/source metadata and the shared cursor codec; it never trusts browser input or emits identity separately to the browser. If a source cannot supply a coherent identity and exact positions, item mode fails closed rather than falling back to turn paging.

- [ ] **Step 3: Implement one exact outer packer**

```go
const transcriptRPCResultSoftLimit = 1_048_576
const transcriptItemPageLimit = 40

func packThreadReadItemCandidates(
    candidates transcriptItemCandidateResult,
    enrich func(appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error),
) (appwire.ThreadReadResponse, error)

func packThreadTurnsItemCandidates(
    candidates transcriptItemCandidateResult,
    enrich func(appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error),
) (appwire.ThreadTurnsListResponse, error)
```

Walk candidates newest-to-oldest, tentatively include the next nearest older atomic item, regroup fragments, run every production enrichment, and `json.Marshal` the typed AppWire `result`. Stop before item 41 or before an added item would exceed the byte target. If the first/nearest item alone is oversized, return it. Never split or truncate item content.

Build the outward cursor from the final oldest returned position and internal cursor identity when older candidates remain or candidates were excluded by count/bytes; otherwise return an empty cursor. This avoids needing relay-only wire metadata and guarantees byte trimming cannot skip history. The packer may request another bounded candidate batch only when needed to fill the page; it must advance by exact position and detect a source that repeats or skips a boundary.

- [ ] **Step 4: Place packing after every byte-changing transform**

Route both initial and backfill item-mode paths through the same packer. Preserve production order for reconciliation, image/document URL stamping, costs, and output-image enrichment, and add no transform after the final measurement. Legacy turn mode bypasses this packer unchanged.

Return an internal error if candidate keys/positions, cursor identity, or source continuation disagree; silently emitting a wrong cursor would lose history.

- [ ] **Step 5: Verify and commit**

```bash
go test ./cmd/evener-hub -run 'Test(PackTranscriptItemPage|AppRPCItemPageSoftLimit|AppRPCItemPageCountLimit)' -count=1
go test ./cmd/evener-hub ./cmd/evener-hub/internal/appsource -count=1
git add -- cmd/evener-hub/app_item_page_fit.go cmd/evener-hub/app_item_page_fit_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/internal/appsource/source.go
git commit -m "feat(hub): pack enriched transcript item pages"
```

## Task 7: Merge Partial Pages and Recover Stale Cursors in the Frontend

**Files:**
- Modify: `cmd/evener-hub/frontend/src/protocol/errors.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/model.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/threads.test.ts`

- [ ] **Step 1: Write failing reducer tests**

Add tests which start with a newest page, apply an older page, and assert:

- two fragments with the same turn ID become one turn;
- every transcript key appears once and in transcript order, including when historical and live display IDs differ;
- a duplicate older item cannot replace newer text, observed timings, reasoning chunks, output images, or settled status;
- a repeated `item/started` for an existing transcript key upserts rather than appends;
- an unmatched tool result remains visible;
- loading an older tool call later folds the call/result into one settled item;
- a result-only turn is removed only after its result was actually folded into a matching call.

Run:

```bash
cd cmd/evener-hub/frontend && npm test -- src/protocol/reducer.test.ts
```

Expected: FAIL because `prependOlderTurns` concatenates duplicate turns and `mergeToolCallsByCallId` drops unmatched results.

- [ ] **Step 2: Replace concatenation with identity-aware merging**

Export:

```ts
export function mergeOlderItemPage(
  model: ThreadModel,
  resp: ThreadTurnsListResponse,
): ThreadModel
```

Use these precedence rules:

```ts
const statusRank: Record<string, number> = {
  inProgress: 0,
  completed: 1,
  failed: 1,
};

function mergePageItem(older: ItemModel, newer: ItemModel): ItemModel {
  return {
    ...older,
    ...newer,
    argumentsJSON: newer.argumentsJSON ?? older.argumentsJSON,
    outputImages: newer.outputImages ?? older.outputImages,
    reasoningSummaries: newer.reasoningSummaries ?? older.reasoningSummaries,
    observedStartedAt: newer.observedStartedAt ?? older.observedStartedAt,
    observedCompletedAt: newer.observedCompletedAt ?? older.observedCompletedAt,
    status:
      statusRank[newer.status] >= statusRank[older.status]
        ? newer.status
        : older.status,
  };
}
```

Add `transcriptKey` and `position` to `ItemModel`, and copy them in `wireItemToModel`. Build final turn order as older-page turn IDs followed by current turn IDs not already present. For a shared turn in item mode, merge by `transcriptKey` with older position order first and current-only items afterward; current/live items are the `newer` argument even when their display IDs differ. Keep ID matching only for legacy items that have no transcript key. Change `item/started` to the same transcript-key upsert rule. Current turn scalar state wins over backfill.

Fix tool reconciliation by first collecting both call IDs and result IDs. Skip a result item only when a matching call exists in the aggregate turn list. Run tool reconciliation after combining the complete loaded model, not independently on each page.

- [ ] **Step 3: Write failing store tests for item limits and stale recovery**

Assert that:

- initial `thread/read` and older `thread/turns/list` both send `pageUnit: "item"` and `itemLimit: 40`;
- a `WireError` with `evenerErrorInfo: "transcriptItemCursorStale"` triggers one fresh `thread/read`, replaces the model, and does not display an older-page error;
- an ordinary list failure still rejects for the existing inline retry UI;
- a page response is discarded if release, reconnect epoch change, fresh hydration, or another successful older-page request changed the captured cursor while the request was in flight;
- a live notification arriving during the request survives the merge.
- reconnect and `evener/thread/resync` each perform one fresh subscribed item-mode read, replace bounded history, and reject a pre-cut older-page completion.

Run:

```bash
cd cmd/evener-hub/frontend && npm test -- src/stores/threads.test.ts
```

Expected: FAIL because requests still use turn limits and stale errors are not classified.

- [ ] **Step 4: Implement stale-cursor detection and guarded publication**

```ts
export function isStaleCursorError(error: unknown): boolean {
  return error instanceof WireError && error.evenerErrorInfo === "transcriptItemCursorStale";
}
```

Replace both old constants with:

```ts
const TRANSCRIPT_ITEM_PAGE_SIZE = 40;
```

`threadReadParams` and `olderItemsParams` both send `pageUnit: "item"` and `itemLimit: TRANSCRIPT_ITEM_PAGE_SIZE`. Legacy callers continue to send turn mode.

In `loadOlderTurns`, capture the client, ready epoch, and exact cursor. On stale error, call the existing targeted `refreshTrackedThread(client, epoch, ref, true)` path so the established subscription-cut and hydration-generation fences perform a fresh read. Do not retry the stale cursor. Before publishing a successful older page, re-read the current model and require the same client, epoch, tracked ref, and `current.olderCursor === capturedCursor`; then call `mergeOlderItemPage(current, response)`.

- [ ] **Step 5: Verify frontend behavior**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/protocol/errors.ts src/protocol/model.ts src/protocol/reducer.ts src/protocol/reducer.test.ts src/stores/threads.ts src/stores/threads.test.ts
npm test -- src/protocol/reducer.test.ts src/stores/threads.test.ts
cd ../../.. && make test-web
```

Expected: PASS.

- [ ] **Step 6: Commit the named paths**

```bash
git add -- cmd/evener-hub/frontend/src/protocol/errors.ts cmd/evener-hub/frontend/src/protocol/model.ts cmd/evener-hub/frontend/src/protocol/reducer.ts cmd/evener-hub/frontend/src/protocol/reducer.test.ts cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.test.ts
git commit -m "feat(web): merge atomic transcript item pages"
```

## Task 8: Add Protocol-Level and Real-Browser Regression Gates

**Files:**
- Create: `cmd/evener-hub/app_rpc_item_paging_test.go`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs`

- [ ] **Step 1: Write failing protocol-level integration tests**

Drive the real hub RPC handler with scripted live-daemon, ended-local, and Codex sources. For each source, fetch the initial page and every older page, flatten the responses, and compare against an independently projected full transcript. Assert:

- page counts are 40 except exhaustion or byte fitting;
- one turn and one entry can cross a page boundary;
- every expected item ID occurs once;
- cursors are opaque and never decimal indexes;
- a cursor from a replaced thread incarnation returns typed stale;
- a tool call/result split across pages becomes complete after all pages load;
- internal candidate/cursor identity state is absent from the JSON result sent to the client, while public item keys/positions and fragment flags are present.

Run:

```bash
go test ./cmd/evener-hub -run 'TestAppRPCAtomicItemPagingEndToEnd'
```

Expected: FAIL until all three source paths and the shared packer agree.

- [ ] **Step 2: Add the React integration regression**

In `Session.test.tsx`, script an initial result-only partial turn and an older page containing the call plus an earlier fragment of the same turn. Let the existing visible paging sentinel trigger without a click. Assert the DOM contains each message once, one settled tool row with call arguments and result output, no empty separator from the folded result turn, and no inline error. Add a stale-list variant which returns `transcriptItemCursorStale` and then a fresh read; assert the stale content is replaced and no retry row appears.

Run:

```bash
cd cmd/evener-hub/frontend && npm test -- src/panes/session/Session.test.tsx
```

Expected: PASS after Task 7; if it fails, fix the reducer/store boundary rather than weakening the component assertion.

- [ ] **Step 3: Extend the real-browser harness**

Add `?paging=1` mode to `overflowharness-entry.tsx`. Seed the real `Session` tree through the real reducer with a 40-item newest page, script `thread/turns/list` to return an older fragment sharing the first visible turn, and expose:

```ts
declare global {
  interface Window {
    verifyItemPaging: () => Promise<{
      duplicateItemIds: string[];
      missingItemIds: string[];
      toolRows: number;
      anchorDelta: number;
    }>;
  }
}
```

`verifyItemPaging` records the first visible row's ID and top coordinate, triggers the real store's `loadOlderTurns`, waits for two animation frames and virtualizer settlement, then reports duplicates, missing fixture IDs, settled tool-row count, and anchor displacement.

In `overflowguard/run.mjs`, navigate one Chrome page to the paging mode and fail unless:

```js
result.duplicateItemIds.length === 0
result.missingItemIds.length === 0
result.toolRows === 1
Math.abs(result.anchorDelta) <= 1
```

The test must use the existing CDP startup deadline and frame/font settling helpers; add no sleeps.

- [ ] **Step 4: Run focused and complete gates**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/panes/session/Session.test.tsx src/dev/overflowharness-entry.tsx
cd ../../..
make lint-gofmt
go test ./appwire ./internal/appitempaging ./internal/apptranscript ./server ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -count=1
make generate
make lint-generated
make lint
make vet
make test
make test-web
make test-web-browser
make merge-approval-gate
git diff --check
```

Every command must exit zero. `make test-web-browser` requires Chrome; absence of Chrome is incomplete verification, not a pass. After `make generate`, inspect `git diff` and commit any deterministic generated change rather than leaving the worktree dirty.

- [ ] **Step 5: Commit the named paths**

```bash
git add cmd/evener-hub/app_rpc_item_paging_test.go cmd/evener-hub/frontend/src/panes/session/Session.test.tsx cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx cmd/evener-hub/frontend/scripts/overflowguard/run.mjs docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "test(paging): cover atomic transcript pages end to end"
```

## Completion Criteria

Implementation is complete only when all eight task commits exist, the worktree is clean, every focused red test was observed failing before implementation, every focused test is green afterward, generated AppWire artifacts are current, the real-browser guard proves stable partial-turn backfill, `make lint`, `make vet`, `make test`, `make test-web`, `make merge-approval-gate`, and the Chrome-capable browser guard all exit zero.
