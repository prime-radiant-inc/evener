# Atomic Transcript Item Paging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace turn-count pagination with stable, opaque, backward pagination over the newest or previous 40 atomic projected `ThreadItem`s, while preserving partial turns, live state, tool pairs, and a soft 1 MiB final-result limit.

**Architecture:** AppWire v4 exposes one item-only contract for the existing thread read/list methods. A shared paging package owns versioned cursor fences, absolute `(entry,item)` positions, stable transcript keys, atomic candidate selection, and turn-fragment regrouping. Indexed transcripts, live snapshots, and provider adapters return positioned candidates through an internal source contract; the hub enriches and packs the final typed result to the 40-item/1-MiB limits. The frontend merges fragments by turn ID and transcript key and replaces bounded state after a typed item-cursor failure.

**Tech Stack:** Go, JSON-RPC/AppWire, append-only transcript indexes, React, TypeScript, Zustand, Vitest, Chrome DevTools Protocol browser guards.

**Spec:** `docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`

> **Flag-day correction (2026-09-05):** This historical plan was written for
> an additive item-mode rollout. The approved v4 item-only design supersedes
> its legacy compatibility promises: do not retain turn mode, `pageUnit`,
> `turnLimit`, transcript-list `limit`, numeric cursors, fallback, or
> capability negotiation. Execute only the still-applicable item paging,
> fragment, identity, and byte-budget work; migrate all callers together.

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

### Modified files

- `appwire/types.go` — item-limit, position/key, and turn-fragment fields for the v4 item-only contract.
- `appwire/errors.go`, `appwire/errors_test.go` — stable invalid-params item-cursor discriminator.
- `appwire/paging.go`, `appwire/paging_test.go` — v4 item-only validation and 40-item constants.
- `appwire/protocol.go` — v4 item-only catalog roots and protocol documentation.
- `docs/appwire-protocol.md` — generated v4 item-only contract.
- `cmd/evener-hub/frontend/src/protocol/types.gen.ts` — generated v4 TypeScript types.

## Task 1: Publish the Item-Only v4 Contract

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
- Removes: `ThreadReadParams.TurnLimit`, `ThreadTurnsListParams.Limit`, numeric turn cursors, and the `TranscriptPageUnit` selector.
- Keeps: `itemLimit` (default/maximum 40), positioned/keyed items, fragment metadata, and a stable item-cursor invalid-params reason.

- [ ] **Step 1: Write failing flag-day contract tests**

Test all of the following:

1. Requests carrying retired `turnLimit`, transcript-list `limit`, numeric cursors, or `pageUnit` are rejected at decoding, with no compatibility path.
2. Item-only reads accept `itemLimit` from 1 through 40, default omitted/non-positive item limit to 40, and reject a value over 40.
3. Item responses omit the retired page-unit selector and carry positioned/keyed fragments.
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

Expected: FAIL because the v4 item-only types and validation do not exist.

- [ ] **Step 2: Define the exact item-only wire vocabulary**

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

Add `ItemLimit int` to `ThreadReadParams` and `ThreadTurnsListParams`; reject `PageUnit`, `ThreadReadParams.TurnLimit`, and `ThreadTurnsListParams.Limit` at decoding. Add `TranscriptKey string` and `Position *ThreadItemPosition` to `ThreadItem`. Change the existing `Turn.ItemsView string` field to `TurnItemsView`, then add `HasEarlierItems` and `HasLaterItems`. Use `omitempty` only on new fields and preserve the required v4 JSON tags and asserted output.

Item-mode validation makes key/position and fragment metadata mandatory. A fragment says `itemsView: "fragment"` even when it currently contains every known item in that turn; complete snapshots say `full`.

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

Expected: PASS; generated docs/types contain only the v4 item-only fields and the protocol version is `evener-appwire-v4`.

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
