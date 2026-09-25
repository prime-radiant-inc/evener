# Transcript Read Model Phase 3 Implementation Plan: Activation and Read Switch

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the transcript the only history read model: history is the projection of recorded entries (live and reloaded alike), everything unrecorded is a per-thread live overlay, merges are versioned upserts, and the daemon keeps no copy of history in memory.

**Architecture:** The seekable transcript index (`internal/transcriptindex`) learns the new-format projection rules (turn membership by `TurnID`, status from completion entries, COMMUNICATE and NOTICE items, round ids) and becomes the projection state: the daemon's per-thread projection goroutine extends the index to each recorded length handed to it under the append lock and publishes `history/updated` with the changed items and turns. A new overlay model (`internal/appoverlay`) replaces the live projector's history emission with streams keyed by round and attempt, previews, tool execution state keyed by the call's history key, running state and bounded notice rings. Reads project history from the index after the subscription cut, merge the overlay captured inside it, and carry epoch, snapshot identity and request generation; the hub serves daemonless sessions from the same index. The protocol goes to `evener-appwire-v6`; the shared TypeScript reducer, the web store and components, the mobile store and the TUI switch together; snapshot history is then deleted.

**Tech Stack:** Go (module `primeradiant.com/evener`), TypeScript (appwire-client, React frontend, React Native mobile), vitest, biome.

**Spec:** `docs/superpowers/specs/2026-09-25-transcript-read-model-design.md` (revision 7 + amended latency criterion). Also `docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`; phase 1 and 2 plans (`docs/superpowers/plans/2026-09-25-transcript-read-model-phase1.md`, `-phase2.md`).

## Global Constraints

- Default tests are deterministic: scripted provider at the LLM boundary, no network, credentials or ambient state; no sleeps; await structured completions (AGENTS.md, `docs/developing-evener/testing.md`). Per-test ceiling 3 s (`testing-budget.json`); `./agent` runs need `-timeout 30m`.
- Real-transcript measurement is opt-in only (`EVENER_TRANSCRIPT_INDEX_REAL`, `EVENER_TRM_SESSION_DIR`); copies live in `/tmp/claude-1000/trm-data/`. Never read Jesse's state directory from a test.
- Entry ordinal: "the 0-based index of an entry line in the file, excluding the header." Position: `{entry: ordinal + 1, item: part}`; header prelude items `{entry: 0, item: i}`; notices `{entry: <preceding ordinal + 1 or 0>, item: 1<<30, sub: n}`.
- Item key: `apptranscript-item-v2:<turnID>:<ordinal>:<part>` (`transcriptindex.ItemKey`).
- Version: "the highest entry ordinal (below) among the entries that contributed to it" — stored and sent as ordinal + 1 so 0 means "no history version" (overlay). "The higher version wins." "A merge never removes a history item. Only a replacement does: a new incarnation, a resync epoch, or an authoritative daemonless read."
- Legacy entries (no format marker) "keep today's turn IDs (`turn_<entryIndex>`), adjacency grouping and inferred status". New entries follow the new rules.
- The append lock is a leaf: the recorded hook takes only leaf locks (the thread's queue mutex, the overlay mutex) and never appends, never blocks, never does I/O.
- "Running output keeps the last 256 KB per call." Notice rings: "50 `round_timings`, 50 of everything else, and 64 KB in total"; "A daemon-wide cap of 16 MB evicts the oldest first." Index handle cache: 64.
- "If the rebuild itself fails three times in a row, the thread's history enters a failed state."
- Protocol: `evener-appwire-v6`; an older client gets an explicit "upgrade required" error.
- Frontend gate rules (AGENTS.md): never `npx biome` at the repo root; use `make test-web`; before the gate run `cd cmd/evener-hub/frontend && npx biome check --write <touched paths>`; never `npm ci` through a symlinked `node_modules`.
- Generated files: `make generate` regenerates `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md`; `make lint` fails on drift.
- zsh: quote globs (`--include='*.go'`), `set -o pipefail` in every piped verification chain.
- Commit messages end with `Claude-Session: https://claude.ai/code/session_01ALoA3J8ymsuJuXoApTZqLJ`. Never skip hooks. No bare `git stash`.

## Baselines measured before any change (2026-09-25, this worktree at `d37e40057c`)

| Measure | root101.jsonl (the "95 MB root", now 101 MB) | coord134.jsonl |
|---|---|---|
| Today, page after a notification (`baseline-page-invalidated`) p50 / p99 | 5.09 ms / 7.81 ms | 15.71 ms / 23.39 ms |
| Today, cached page p50 / p99 | 1.00 ms / 1.82 ms | 1.26 ms / 2.97 ms |

Latency limits therefore: idle p99 < 50 ms and ≤ 15.6 ms (root101) / ≤ 46.8 ms (coord134); appending p99 < 100 ms.

Memory: today's server holds 334.4 MB of turn snapshots for the 261 transcripts of the coordinator session `034N8nMIUxWLIw7RrX7Kgg` (copy in `/tmp/claude-1000/trm-data/coord-session/`), seeded cold. The spec's "about 283 MB" was measured when the session was smaller.

## Spec issues found

Each takes the most conservative reading; each is pinned by a test named in its task.

1. **The index is the incremental projection state.** The spec describes an in-memory incremental projector (open turn accumulators, open round call records) rebuilt "from the file ... through the index". The index already holds exactly that state on disk (turn summaries with usage sums and timestamps, the awaiting ASSISTANT entry, item contributors), updated by one implementation. The daemon therefore projects each recorded entry by extending the index to it and reading back what changed. This keeps one implementation of the rules (no incremental/whole-file drift by construction) and holds even less memory than the spec allows. "Incremental projection against whole-file projection" becomes: an index extended entry by entry (live) against one built in one pass, and both against the independent reference projection in `internal/transcriptindex` tests (Tasks 3, 18).
2. **Round end.** The spec sends `overlay/end(roundID)` "at round end" and delivers tool state and previews after the round's ASSISTANT entry. The session knows a round has ended once the next round opens or the execution completes, which is after the round's tools and hooks ran. `EventRoundEnded` is emitted at those two points (Task 5).
3. **Stream attempt numbers.** Events carry the round id; the overlay counts attempts itself (a reset ends an attempt, the next output starts attempt n+1). `streamID = <roundID>/<attempt>` (Task 6).
4. **"Before the server marks it processing."** `serve` calls `SetProcessing(true)` before the session has minted a plain turn's id. `SetProcessing(true)` stops publishing anything (no reservation, no status); `SetProcessingTurn(turnID)`, called by the session from `beginExecution` before its first entry is recorded, publishes `active` with `activeTurnId` in one commit. So the turn id is always published before, or with, the processing state clients see (Task 11).
5. **Ending a subscription whose resync cannot be delivered.** The appserver already answers a full outbound buffer during a commit by evicting the connection (`evictSlowConsumer`), which ends every subscription on it; the client reconnects and re-reads. The resync push goes through that path and a test pins it (Task 8).
6. **Delegates' running state.** Children are not served, so nothing calls `SetProcessingTurn` for them. Their running turn comes from `EventExecutionStarted`/`EventExecutionEnded` (Task 5, Task 6).
7. **Errors.** A turn failure is recorded as TURN_FAILURE plus a failed completion, so history shows it. An `EventError` whose error was not recorded (for example the fail-closed diagnostic) becomes an ephemeral `error` notice. `events.ErrorData` gains `Recorded bool`, set where the session emits an error it recorded as TURN_FAILURE (Task 5).
8. **Warnings** are listed among the spec's ephemeral notices, so the `warning` notification becomes an overlay notice and the reducer stops minting warning ids (Tasks 6, 15).
9. **"One agentMessage per assistant text run."** A run is a maximal sequence of consecutive text parts; any other part (a thinking part, a tool call, a hidden communicate call) ends it. The run's item is keyed by its first part. **"One reasoning item per entry"** is keyed by the first thinking part; its text joins every thinking part's `Text`, or `Summary` when `Text` is empty, with blank lines (Task 2).
10. **Communicate echo suppression** needs the round's last assistant text. The index decides at extension time, reading the turn's awaiting ASSISTANT entry: a COMMUNICATE whose message echoes it projects no item. Deterministic from entries (Task 3).
11. **A new-format `TurnID` equal to a legacy turn id** (a resumed legacy session whose legacy `StableTurnID` was `turn_mN`) would give two turn records one id. Fork and client mutation sequences are seeded above every `turn_m` in the file (phase 2 Task 26), so this cannot arise from Evener's writers; the index does not try to merge them.
12. **Where a stream is displayed.** The spec anchors notices; it does not anchor streams. Streams and previews display at the end of their turn (`TurnID` on the overlay item), after every history item of that turn.

---

## Work units

| Unit | Tasks | State after the unit |
|---|---|---|
| A. New projection rules and wire vocabulary | 1-4 | additive: new types, new index rules, nothing wired |
| B. Session facts the daemon needs | 5 | additive events and hooks; nothing consumes them |
| C. New daemon machinery beside the old | 6-10 | overlay model, thread history, reads, all tested but not wired to `serve` |
| D. The switch | 11-17 | daemon, hub, fork, TypeScript reducer, web, mobile, TUI all on v6 |
| E. Proof | 18-19 | parity at zero, boundary tests, acceptance numbers |
| F. Deletion | 20 | snapshot history and every other deleted piece gone |
| G. Gates and PR | 21 | one PR, roborev |

Every task ends with the whole build green: `go build ./... && go vet ./...` and the packages it touches pass. The product is only coherent again after Task 17; the PR ships the stack together, as the spec requires.

## Review Focus

1. **A read that races a live append**: its cut captured before an entry is recorded and its projection run after it. Neither a duplicate nor a gap: merge by version drops the duplicate, and the notification after the cut delivers the rest. Pinned in Task 18 (`TestReadCutBeforeToolResultsProjectedAfter`).
2. **A client holding a stale page after the daemon restarted** (new incarnation) or after a cross-process rollback. A backfill with an old cursor gets `TranscriptItemCursorStale`, and the next latest-window read replaces the history. Pinned in Task 9 and Task 18.
3. **A running turn whose daemon died.** On the daemonless read it shows as interrupted (display rule), not completed. Pinned in Task 15 (reducer display rule) and Task 12 (hub read of an open execution turn).
4. **A tool that produces megabytes of output while running.** Only the last 256 KB are kept per call, and notices stay within their caps even when a session loops on warnings. Pinned in Task 6.
5. **A delegate that finished long ago and is read again.** It is served from its transcript through the index handle cache with no retained history; evicting handles past 64 closes them. Pinned in Task 4 (cache) and Task 10 (delegate read).

---

## Unit A: new projection rules and wire vocabulary

### Task 1: Wire vocabulary for versioned history, the overlay and read identity (additive)

**Files:**
- Modify: `appwire/types.go`, `appwire/validate*.go` (whatever validates `ThreadReadParams`/responses; find with `grep -n "func ValidateThreadRead" appwire/*.go`)
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md` via `make generate`
- Test: `appwire/history_types_test.go`

**Interfaces — Produces (exact Go, JSON keys matter for TS):**

```go
// ThreadItemPosition gains Sub, which orders notices anchored at one place.
type ThreadItemPosition struct {
	Entry uint64 `json:"entry"`
	Item  uint32 `json:"item"`
	Sub   uint32 `json:"sub,omitempty"`
}

// NoticeAnchorItem is the Item value of every notice anchor: past every real
// content part index.
const NoticeAnchorItem uint32 = 1 << 30

// On ThreadItem (keep existing fields; add):
//	Version uint64 `json:"version,omitempty"` // highest contributing entry ordinal + 1; 0 on overlay items
//	RoundID string `json:"roundId,omitempty"` // ASSISTANT-projected items
// On Turn (add):
//	Version uint64 `json:"version,omitempty"`

// SnapshotIdentity names what a history read or update was projected from.
type SnapshotIdentity struct {
	Incarnation string `json:"incarnation"`
	Length      int64  `json:"length"`
}

const NotifyHistoryUpdated = "history/updated"

// HistoryUpdatedParams carries the full current form of every item and turn
// that recorded entries changed. Turns carry no Items.
type HistoryUpdatedParams struct {
	ThreadID string           `json:"threadId"`
	Ref      string           `json:"ref,omitempty"`
	Epoch    uint64           `json:"epoch"`
	Snapshot SnapshotIdentity `json:"snapshot"`
	Turns    []Turn           `json:"turns,omitempty"`
	Items    []ThreadItem     `json:"items,omitempty"`
}

type OverlayKind string

const (
	OverlayStream  OverlayKind = "stream"
	OverlayPreview OverlayKind = "preview"
	OverlayTool    OverlayKind = "tool"
	OverlayNotice  OverlayKind = "notice"
)

// OverlayItem is one piece of live state that is not recorded history.
type OverlayItem struct {
	Key        string              `json:"key"` // "stream:<streamId>:<agentMessage|reasoning>", "preview:<callId>", "tool:<historyKey>", "notice:<n>"
	Kind       OverlayKind         `json:"kind"`
	TurnID     string              `json:"turnId,omitempty"`
	RoundID    string              `json:"roundId,omitempty"`
	StreamID   string              `json:"streamId,omitempty"`
	CallID     string              `json:"callId,omitempty"`
	HistoryKey string              `json:"historyKey,omitempty"` // tool: the call item's transcriptKey
	Anchor     *ThreadItemPosition `json:"anchor,omitempty"`     // notice
	Item       ThreadItem          `json:"item"`                 // the display form
}

const (
	NotifyOverlayUpserted = "overlay/upserted" // OverlayUpsertedParams
	NotifyOverlayDelta    = "overlay/delta"    // OverlayDeltaParams
	NotifyOverlayReset    = "overlay/reset"    // OverlayResetParams
	NotifyOverlayEnd      = "overlay/end"      // OverlayEndParams
)

type OverlayUpsertedParams struct {
	ThreadID string      `json:"threadId"`
	Ref      string      `json:"ref,omitempty"`
	Item     OverlayItem `json:"item"`
}
type OverlayDeltaField string
const (
	OverlayDeltaText   OverlayDeltaField = "text"
	OverlayDeltaOutput OverlayDeltaField = "output"
)
type OverlayDeltaParams struct {
	ThreadID string            `json:"threadId"`
	Ref      string            `json:"ref,omitempty"`
	Key      string            `json:"key"`
	Field    OverlayDeltaField `json:"field"`
	Delta    string            `json:"delta"`
}
type OverlayResetParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref,omitempty"`
	StreamID string `json:"streamId"`
}
type OverlayEndParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref,omitempty"`
	RoundID  string `json:"roundId"`
}

// HistoryChanges are items and turns outside a daemonless latest window whose
// version grew since the snapshot the client held.
type HistoryChanges struct {
	Turns []Turn       `json:"turns,omitempty"`
	Items []ThreadItem `json:"items,omitempty"`
}
```

Additions to existing request/response types (all `omitempty` except where noted):
- `ThreadReadParams`: `RequestGeneration uint64 "requestGeneration"`, `HeldSnapshot *SnapshotIdentity "heldSnapshot"`.
- `ThreadReadResponse`: `RequestGeneration uint64 "requestGeneration"`, `Epoch uint64 "epoch"`, `Snapshot *SnapshotIdentity "snapshot"`, `Overlay []OverlayItem "overlay"`, `Authoritative bool "authoritative"`, `Changes *HistoryChanges "changes"`.
- `ThreadTurnsListParams`: `RequestGeneration uint64`. `ThreadTurnsListResponse`: `RequestGeneration`, `Epoch`, `Snapshot *SnapshotIdentity`, `Authoritative bool`.
- `ThreadResyncParams`: `Epoch uint64 "epoch,omitempty"`.
- `ThreadStatusChangedParams`: `ActiveTurnID string "activeTurnId,omitempty"` (the running turn; empty when none).
- `ThreadForkParams`: `SourceItemKey string "sourceItemKey,omitempty"` (additive here; `SourceTurnID` goes in Task 14).
- `appwire.AnyNotification` / the notification method registry (find where `NotifyAgentMessageDelta` is registered for typed decoding and TS generation, `grep -rn "NotifyAgentMessageDelta" appwire internal/appwirets`) gets the five new methods with their params types.

- [ ] **Step 1: Write the failing test** (`appwire/history_types_test.go`): JSON round trip of `HistoryUpdatedParams` with one turn and one item carrying `Version`, `RoundID`, `Position{Entry: 3, Item: NoticeAnchorItem, Sub: 2}`; a zero `Sub` is omitted from the encoding (`{"entry":3,"item":1}` exactly); an `OverlayItem` round trip for each kind; `ThreadReadResponse` with `Snapshot`, `Epoch`, `RequestGeneration`, `Overlay`, `Authoritative`, `Changes` round trips and still validates through the existing response validator.
- [ ] **Step 2: Run** `go test ./appwire -run 'History|Overlay' -count=1` → FAIL (`undefined: HistoryUpdatedParams`).
- [ ] **Step 3: Implement** the types with doc comments, register the methods, run `make generate`.
- [ ] **Step 4: Run** `go test ./appwire ./internal/appwirets -count=1`, `go build ./...`, then `make test-web` (types.gen.ts is additive; typecheck must pass). Expected PASS.
- [ ] **Step 5: Commit** — `feat(appwire): versioned history, overlay and read identity vocabulary`

### Task 2: New-format per-entry projection

**Files:**
- Modify: `internal/apptranscript/apptranscript.go` (`projectTurn`, ~:345-680)
- Test: `internal/apptranscript/new_format_projection_test.go`

**Interfaces — Produces:** `ProjectTurnParts` keeps its signature and, for an entry with `Format == schema.TurnFormatIdentity`, applies the new rules below; legacy entries (`Format == 0`) project exactly as today. Also exported: `func CommunicateItem(turnID string, entryIndex int, entry schema.Turn) (appwire.ThreadItem, bool)` and `func NoticeItem(turnID string, entryIndex int, entry schema.Turn) (appwire.ThreadItem, bool)` used by the switch below and by the overlay's notice text reuse.

New-format rules (the spec's "The projector" section; Spec issues 9):
- An entry with `OriginalOrdinal != nil` (a fold copy) projects nothing.
- `ASSISTANT`:
  - Text runs: one `agentMessage` per maximal run of consecutive text parts, part index = the run's first part, text = the run's texts concatenated. Empty runs project nothing.
  - Reasoning: one `reasoning` item per entry, part index = the first thinking or redacted-thinking part, text = each part's `Thinking.Text`, or `Thinking.Summary` when `Text` is empty, joined with `"\n\n"`; nothing when all are empty.
  - A `communicate` tool call part projects nothing (hidden; the COMMUNICATE entry projects the message).
  - Other tool calls and web search project as today.
  - Every item gets `RoundID = entry.RoundID`.
- `TOOL`/`TOOL_RESULTS`: as today (the index folds results into their call items).
- `COMMUNICATE`: one `agentMessage` at part 0, `ID "item_assistant_<entryIndex>_0"`, `Text = Communicate.Message`, `CallID = Communicate.CallID`, `Status completed`, `StartedAt` from the timestamp. Echo suppression is the index's job (Task 3), not this function's.
- `NOTICE`: one `systemMessage` at part 0 with `EventKind` and description matching today's live announcement for the same event (`appwire.ThreadItemEventKind` values `tool_repair`, `goal_ended`, `turn_limit`, `skill_activated`; reuse the text builders the live projector uses today in `internal/appprojector/appwire_projection.go` `systemAnnouncement*` for those four — move the shared formatting into apptranscript so both call one function).
- `TURN_COMPLETION`, `TURN_REOPEN`: nothing (they set turn status).
- Every other kind: as today.

- [ ] **Step 1: Write the failing tests.** For each rule, an entry built with `schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", ...}`: (a) text, text, thinking, text, communicate call, read_file call → items `[agentMessage part 0 "ab", reasoning part 2, agentMessage part 3, commandExecution part 5]`, all with `RoundID "r_1"`; (b) thinking with empty Text and Summary "sum" → reasoning text "sum"; two thinking parts → one item joined; (c) the same entry with `Format 0` projects exactly `ProjectTurnParts` of today (compare against a copy of the legacy expectation from `project_turn_parts_test.go`); (d) COMMUNICATE projects the agentMessage with call ID; (e) each NOTICE kind projects its systemMessage whose `EventKind` and `Text` equal what `appprojector` produced for the equivalent event before this change (assert on `EventKind` and non-empty text; do not pin prose — AGENTS/testing.md "Prompt Prose Is Not a Test Oracle"); (f) TURN_COMPLETION, TURN_REOPEN and a fold copy project nothing and report no parts.
- [ ] **Step 2: Run** `go test ./internal/apptranscript -run NewFormat -count=1` → FAIL.
- [ ] **Step 3: Implement** in `projectTurn`, branching on `turn.Format == schema.TurnFormatIdentity` at the top; move the four notice text builders from appprojector into apptranscript and call them from both.
- [ ] **Step 4: Run** `go test ./internal/apptranscript ./internal/appprojector ./internal/transcriptindex -count=1` → PASS (legacy fixtures unchanged; the phase 1 index tests still pass because their fixtures are legacy).
- [ ] **Step 5: Commit** — `feat(apptranscript): project new-format entries by the read model's rules`

### Task 3: The index applies the new-format turn rules

**Files:**
- Modify: `internal/transcriptindex/build.go`, `records.go`, `window.go`, `index.go` (format/projection constants, restoreBuilder)
- Modify: `internal/transcriptindex/reference_test.go`, `fixture_test.go` (new-format fixtures and the reference's new rules)
- Test: `internal/transcriptindex/new_format_test.go`

**Interfaces — Produces:**
- `formatVersion = 4`, `projectionID = "transcript-read-model-v3"`; `appitempaging.TranscriptItemProjectionVersion` bumps to 2 (cursors go stale once, as the spec says).
- Turn record (fixed size; grow `turnRecordSize` to 160 and keep it a multiple of 8):

```go
type turnRecord struct {
	ID              strRef
	Kind            uint32 // turnKindLegacy, turnKindExecution, turnKindGap, turnKindDelivery, turnKindPrelude
	FirstOffset     int64
	FirstOrdinal    uint64
	Status          uint32 // statusCompleted, statusFailed, statusInterrupted, statusOpen
	FailureOffset   int64  // latest TURN_FAILURE: the diagnostic for a failed status
	FailureLength   uint32
	CompletedAt     int64 // unix ms, from the latest completion entry; 0 when none
	DurationMS      int64
	HasCompletion   bool
	Started         bool
	StartedAt       int64
	Usage           [4]int64
	Model           strRef // latest entry's model, for cost at egress
	AwaitingOffset  int64  // latest ASSISTANT entry with tool calls in this turn
	AwaitingLength  uint32
	AwaitingOrdinal uint64
	Version         uint64
}
```

- Item record: unchanged layout; for new-format call items `Completer` is the TOOL_RESULTS entry.
- Window turns (`reader.turn`) stamp: `ID`, `ItemsView full`, `Status` (open → `appwire.TurnStatusInProgress`), `Error` (from the failure entry when failed), `StartedAt`, `CompletedAt`, `DurationMS`, `Usage`, `Version = record.Version`. Items get `Version = record.Version`. Model is returned for cost: `appitempaging.TranscriptItemCandidate` gains `Model string` (the turn's model), which the server's egress uses (Task 10).

Rules (new-format entries; legacy entries keep phase 1 behavior exactly, through `TurnGrouper`):
- **Membership by `TurnID`.** The first entry of an unknown `TurnID` appends a turn record with `Kind` from `TurnKind` (execution when absent on a reopen of a turn the builder cannot find: see below). A later entry of the same `TurnID` joins that record. The builder keeps `open map[string]uint64` (turn id → slot) for turns that can still take entries: executions whose status is open, the latest gap turn and the prelude turn; a completion removes its turn from the map. An entry naming a turn not in the map searches the turn table backward for the id (`findTurn(id)`; rare: a reopen or resume's interrupted completion of an older turn) and re-adds it. `restoreBuilder` rebuilds `open` by scanning turn records whose status is open or whose kind is gap/prelude (the last ones only).
- Any new-format entry closes the legacy grouper (`TurnGrouper{}`), so a legacy continuation after a new-format entry opens its own legacy group.
- **Status.** gap/delivery/prelude: completed. execution: open until a `TURN_COMPLETION`, which sets its status (completed/failed/interrupted), `CompletedAt`, `DurationMS`, `HasCompletion`; a `TURN_REOPEN` sets open again. A `TURN_FAILURE` records `FailureOffset/Length` and never sets status by itself for new-format turns. Legacy turns keep the latch.
- **Tool results.** An ASSISTANT entry with tool calls sets the turn's `Awaiting*`. A `TOOL_RESULTS`/`TOOL` entry in a new-format turn reads the awaiting ASSISTANT entry (`ReadAt` + `transcript.DecodeValidatedEntry`), maps each result's `ToolCallID` to the call's part index, finds the call item's slot by position `{AwaitingOrdinal+1, part}` (binary search, as `rank` does), and adds the result entry as that item's completer. The contributor's `Name` is the call's tool name when the result carries none. A result with no matching call in the awaiting entry projects as its own item (today's orphan shape) in the result's turn.
- **COMMUNICATE.** Projects its item unless its message echoes the awaiting ASSISTANT entry's last text run (`apptranscript.EchoesAssistantText`, the rule today's projectors use).
- **Fold copies** (`OriginalOrdinal != nil`) consume their ordinal and nothing else.
- Every accounted entry rewrites its turn's summary and bumps `Version` (as today), logging an update.

Reference (test code, independent of the builder): `referenceTurns` gains the same rules, implemented directly over the whole entry list in memory (group by `TurnID` in first-appearance order; status from the latest completion/reopen; fold results into the latest preceding ASSISTANT call of the same turn with that call id; drop echoing COMMUNICATEs; drop fold copies). Fixtures gain: (10) a new-format session: prelude entries, an execution with user input, assistant text+reasoning+tool call+communicate call, TOOL_RESULTS, COMMUNICATE, completion; (11) a gap turn of two HOOK_COMPLETED entries and a MODEL_SWITCH; (12) a delivery STEERING recorded between an execution's ASSISTANT and TOOL_RESULTS; (13) a failed execution (TURN_FAILURE then failed completion) and a retry as a new execution; (14) an execution with no completion (open), then a reopen marker and a completion of the same turn (reclaimed); (15) a legacy prefix followed by new-format entries; (16) a fold copy of an earlier entry; (17) an echoing COMMUNICATE and a non-echoing one; (18) a NOTICE of each kind.

- [ ] **Step 1: Write the failing tests** — add fixtures 10-18 and the reference rules; the existing `TestWindowsEqualTheReferenceOnEveryBoundary`, `TestAppendEntryByEntryMatchesTheReference`, `TestReplayAfterCrashIsIdempotent`, the sharing tests and the update-log test all run over them. Add `new_format_test.go` pinning directly: the open execution displays `inProgress` with no `CompletedAt`; after the reopen marker it is open again; the reclaimed turn's final status and `DurationMS` come from its last completion; the delivery turn inside an execution span has its own turn id and the execution's TOOL_RESULTS still completes the call item; items and turns carry `Version`.
- [ ] **Step 2: Run** `go test ./internal/transcriptindex -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/transcriptindex ./internal/apptranscript ./internal/appitempaging -count=1` → PASS; each test under 3 s.
- [ ] **Step 5: Commit** — `feat(transcriptindex): turn membership by TurnID, status from completions, results by awaiting call`

### Task 4: Index changes-since for live projection, entry errors, sidecar location, handle cache

**Files:**
- Modify: `internal/transcriptindex/window.go`, `index.go`, `build.go`
- Create: `internal/transcriptindex/cache.go`, `internal/transcriptindex/dir.go`
- Test: `internal/transcriptindex/changes_test.go`, `cache_test.go`

**Interfaces — Produces:**

```go
// DirFor is the sidecar directory the daemon and the hub share for a transcript.
func DirFor(transcriptPath string) string { return transcriptPath + ".index" }

// ChangedSince (existing) now also returns every item and turn whose record an
// entry at or past length CREATED, not only those it updated in place: the
// full set of what entries in [length, covered) changed. Items are in position
// order, turns in slot order, each once.
func (x *Index) ChangedSince(length int64) (Changes, error)

// EntryError reports the entry the index could not apply.
type EntryError struct {
	Ordinal uint64
	Err     error
}
func (e *EntryError) Error() string
func (e *EntryError) Unwrap() error

// Rebuild discards the current build and indexes the transcript again, up to
// length, under a new incarnation.
func (x *Index) Rebuild(length int64) error

// Cache holds up to capacity open indexes, least recently used first out. A
// handle in use is never closed; Release returns it.
type Cache struct{ /* mu, capacity, entries by path, lru list */ }
func NewCache(capacity int) *Cache
func (c *Cache) Acquire(path string) (*Index, error) // Open(path, DirFor(path)) on a miss
func (c *Cache) Release(x *Index)
func (c *Cache) Forget(path string) // close when unused; for a deleted session
func (c *Cache) Close() error
const DefaultCacheCapacity = 64
```

- `ChangedSince`: items created at or past `length` are the item records whose `Opener.Offset >= length` (records are in offset order: binary search the first); turns created are the turn records whose `FirstOffset >= length` (same). Merge with the update log's slots.
- `EntryError`: `scan` and `extend` wrap any decode or apply failure of an entry in `*EntryError` with its ordinal.

- [ ] **Step 1: Write the failing tests.** `changes_test.go`: over every fixture, for every prefix length `L` at an entry boundary, extend to the end and assert `ChangedSince(L)` equals exactly the reference items and turns that any entry past `L` contributed to (created or updated), with versions. A corrupt entry line (valid JSON, strict-decode failure: an unknown field) yields `*EntryError` with its ordinal. `cache_test.go`: capacity 2, acquire three paths, the least recently used released handle is closed; an acquired handle is never closed until released; `Forget` closes an unused handle; `Acquire` after `Close` errors. Run the cache test with `-race` across goroutines.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/transcriptindex -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(transcriptindex): changes since a length, entry errors, shared sidecar location and a handle cache`

---

## Unit B: session facts the daemon needs

### Task 5: Round, execution and recorded-entry events and hooks

**Files:**
- Modify: `agent/events/events.go`, `agent/events/payloads.go`
- Modify: `agent/session_execution.go` (round open/end, execution start/end), `agent/session.go` (writer attach and the recorded hook install), `agent/session_attention.go` (reinstall the hook when attention recovery reopens the writer, ~:1444, :1519), `agent/delegate_runtime.go` / `agent/session_init.go` (children inherit the descendant recorded func like `descendantEvent`, `session_init.go:428, 1113`), `agent/session_events.go` (`ErrorData.Recorded` where the error was recorded as TURN_FAILURE)
- Test: `agent/session_round_events_test.go`, `agent/session_recorded_hook_test.go`

**Interfaces — Produces:**

```go
// events
const (
	EventRoundStarted     EventKind = "ROUND_STARTED"
	EventRoundEnded       EventKind = "ROUND_ENDED"
	EventExecutionStarted EventKind = "EXECUTION_STARTED"
	EventExecutionEnded   EventKind = "EXECUTION_ENDED"
)
type RoundData struct{ RoundID string }
type ExecutionData struct {
	TurnID string
	Status string // on EXECUTION_ENDED: completed, failed or interrupted
}
// ErrorData gains: Recorded bool // the error is recorded as a TURN_FAILURE entry

// Session
// SetExecutionStartedFunc installs a callback run in beginExecution before the
// execution's first entry is recorded, with the execution's TurnID. serve wires
// it to Server.SetProcessingTurn. Install it before the session runs input.
func (s *Session) SetExecutionStartedFunc(fn func(turnID string))
// SetTranscriptRecordedFunc installs fn as the transcript's recorded-entry hook
// (transcript.Writer.OnRecorded) now and on every writer the session attaches
// or reopens. fn runs under the append lock: see OnRecorded.
func (s *Session) SetTranscriptRecordedFunc(fn func(transcript.Record))
// TranscriptRecordedLength is the recorded length of the session's transcript,
// 0 when it has none.
func (s *Session) TranscriptRecordedLength() int64
// SetDescendantRecordedFunc installs fn on every descendant session this
// session spawns, now and later (inherited like the descendant event func):
// fn(sessionID, record) under that child's append lock.
func (s *Session) SetDescendantRecordedFunc(fn func(sessionID string, rec transcript.Record))
```

- `roundIDForModelCall` emits `EventRoundStarted{RoundID}` when it mints a round, after emitting `EventRoundEnded{previous}` for the previous round when one was opened in this execution and not yet ended; `completeExecution` emits `EventRoundEnded` for the last open-or-closed round not yet ended, then `EventExecutionEnded{TurnID, Status}` after the completion entry is recorded (Status is the recorded status, or the requested status when nothing was recorded). `beginExecution` calls the execution-started func, then emits `EventExecutionStarted{TurnID}`, before `BeginExecution`. Keep the session's "ended" bookkeeping as `lastRoundID` + `lastRoundEnded bool` under `s.mu`.

- [ ] **Step 1: Write the failing tests** (scripted provider, a real session with a state dir, events via `ConsumeEventsLossless`): (a) a turn of two rounds (tool call, then communicate) emits `EXECUTION_STARTED`, `ROUND_STARTED r1`, …, `ROUND_ENDED r1` before `ROUND_STARTED r2`, …, `ROUND_ENDED r2`, then `EXECUTION_ENDED{completed}`, and the ASSISTANT entries' `RoundID`s equal r1 and r2; (b) a round retried after a 503 emits one `ROUND_STARTED`; (c) the execution-started func is called with the TurnID later found on the USER_INPUT entry, before that entry is in the file (the func reads the file and finds no entry with that TurnID); (d) a TURN_FAILURE turn's `EventError` has `Recorded: true`; the fail-closed diagnostic has `Recorded: false`; (e) the recorded func sees every entry the session records, in ordinal order, with `Offset+Length` equal to `TranscriptRecordedLength()` after the last, including an attention write after attention recovery reopened the writer; (f) a delegate child's entries reach the descendant recorded func with the child's session id.
- [ ] **Step 2: Run** `go test ./agent -run 'RoundEvents|RecordedHook' -count=1 -timeout 30m` → FAIL.
- [ ] **Step 3: Implement.** Every existing consumer of events must ignore the four new kinds (check `switch ev.Kind` sites with `grep -rn "case events.Event" --include='*.go' . | grep -v _test | cut -d: -f1 | sort -u`; the projector's default case already ignores unknown kinds).
- [ ] **Step 4: Run** the new tests and `go test ./agent -count=1 -timeout 30m` and `go test ./server ./cmd/evener -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(agent): round and execution events, the recorded-entry hook, recorded errors`

---

## Unit C: new daemon machinery beside the old

### Task 6: The overlay model

**Files:**
- Create: `internal/appoverlay/overlay.go`, `internal/appoverlay/notices.go`, `internal/appoverlay/budget.go`
- Test: `internal/appoverlay/overlay_test.go`, `notices_test.go`

**Interfaces — Produces:**

```go
package appoverlay

// Budget is the daemon-wide notice byte cap shared by every thread's ring.
type Budget struct{ /* mu, used, limit, threads */ }
func NewBudget(limit int) *Budget // DefaultBudgetBytes = 16 << 20

// Overlay is one thread's live state that is not recorded history. It is
// safe for concurrent use; its mutex is a leaf (Recorded runs under the
// transcript append lock).
type Overlay struct{ /* ... */ }
func New(budget *Budget) *Overlay
func (o *Overlay) Close() // releases its notices from the budget

// Change is one notification the caller commits, in order.
type Change struct {
	Method string // appwire.NotifyOverlay*
	Params any    // the Overlay*Params without ThreadID/Ref (the server stamps them)
}

// Event applies one session event and returns the notifications it causes.
func (o *Overlay) Event(ev events.SessionEvent) []Change
// Recorded applies one recorded entry: covers streams of its round, the
// preview of its communicate call, the tool state of its results; learns
// call history keys from ASSISTANT entries; advances the notice anchor.
// It returns nothing: clients apply the same coverage from history.
func (o *Overlay) Recorded(rec transcript.Record)
// Snapshot is the overlay's current items, for a read's cut.
func (o *Overlay) Snapshot() []appwire.OverlayItem
// Contains reports whether key is still in the overlay (a read drops captured
// items a recorded entry covered between the cut and the projection).
func (o *Overlay) Contains(key string) bool
// RunningTurnID is the running execution for a thread nothing calls
// SetProcessingTurn for (a delegate).
func (o *Overlay) RunningTurnID() string
```

Rules (spec "The live overlay"):
- **Streams.** `EventRoundStarted` sets the current round and attempt 1. Text deltas (`EventAssistantTextDelta`) and reasoning deltas (`EventReasoningSummaryDelta`) go to `stream:<roundID>/<attempt>:agentMessage` / `:reasoning`: the first delta upserts the item (`Kind stream`, `TurnID` = running turn, `RoundID`, `StreamID`, `Item{Type, ID: key, Status inProgress}`), later ones emit `overlay/delta{Field text}`. `EventAssistantTextReset` emits `overlay/reset{StreamID}` for the current attempt, drops it and its previews, and moves to attempt+1. A delta for a covered round is dropped (the round's entry is recorded: "the server emits no further text or reasoning deltas for that round").
- **Previews.** `EventCommunicatePreviewStart/Delta` → `preview:<callID>` (`Kind preview`, `CallID`, `RoundID`, `StreamID`), deltas as text deltas; `EventCommunicatePreviewReset` removes it (no notification: the attempt's `overlay/reset` or the round's `overlay/end` tells clients). Covered only by a recorded COMMUNICATE with that call id.
- **Tool execution state.** `EventToolCallStart` (not for `communicate`) upserts `tool:<historyKey>` where `historyKey` is learned from `Recorded` on the round's ASSISTANT entry (`transcriptindex.ItemKey(turnID, {ordinal+1, part})` per tool call part); a call whose key is unknown (no transcript) uses `tool:call:<callID>`. `EventToolCallOutputDelta` appends to `Item.Output`, keeping the last 256 KB (`maxRunningOutputBytes = 256 << 10`; trimming at a UTF-8 boundary), and emits `overlay/delta{Field output}`; when a trim happens the next change is a full upsert instead of a delta. `EventToolCallEnd` upserts status (`appwire.SettledToolStatus`-equivalent), error, and held images (`EventToolResultImagesPersisted` later upserts them). Covered by a recorded TOOL_RESULTS naming the call.
- **Round end.** `EventRoundEnded{r}`: when streams of `r` hold content and `r` was not covered, or tool states of `r` were not covered, append one `interrupted` notice; then emit `overlay/end{RoundID}` and drop every stream, preview and tool state of `r`.
- **Execution.** `EventExecutionStarted/Ended` set/clear `RunningTurnID`.
- **Notices.** `round_timings`, `prompt_loaded`, `plugin_loaded`, `loop_detection`, `context_compaction`, `fork_summary`, `warning`, `error` (unrecorded `EventError`, Spec issue 7) and `interrupted` become `notice:<n>` items (`n` a per-overlay counter) with `Anchor{Entry: lastOrdinal+1 or 0, Item: NoticeAnchorItem, Sub: n}` and `Item` the systemMessage the live projector builds today for that event (reuse its builders; move them if needed). Ring: at most 50 `round_timings`, 50 others, 64 KB of encoded items per thread; the budget evicts the oldest notice daemon-wide past 16 MB. Evictions emit nothing (clients keep what they got; a later read omits them).
- Everything else returns no changes.

- [ ] **Step 1: Write the failing tests** — pure unit tests over synthetic events and `transcript.Record`s: stream upsert then deltas; reset then next attempt's deltas go to a new stream id; coverage by an ASSISTANT record with the round id drops the stream and later deltas; preview survives the round's ASSISTANT record and is covered by the COMMUNICATE record; tool state keyed by the history key learned from the ASSISTANT record, output capped at 256 KB (feed 1 MB in 4 KB deltas; the item holds the last 256 KB and valid UTF-8), covered by TOOL_RESULTS; round end with uncovered content yields exactly one interrupted notice then `overlay/end`; round end with everything covered yields only `overlay/end`; notice anchors follow the last recorded ordinal (and `{0, 1<<30, n}` before any entry); ring caps 50/50/64 KB; the budget evicts across two overlays past its limit; `Close` returns bytes to the budget; `Snapshot` returns streams, previews, tools and notices; `-race` with `Recorded` from one goroutine and `Event` from another.
- [ ] **Step 2: Run** `go test ./internal/appoverlay -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/appoverlay -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(appoverlay): the live overlay: streams by round and attempt, previews, tool state, notice rings`

### Task 7: Thread history: projection queue, publication, epochs, failure state

**Files:**
- Create: `server/thread_history.go`
- Test: `server/thread_history_test.go`

**Interfaces — Produces:**

```go
// threadHistory is one thread's history projection: the recorded entries the
// append hook hands it, projected by extending the transcript index in order,
// and published as history/updated. It holds no history.
type threadHistory struct {
	threadID, path string
	cache   *transcriptindex.Cache
	overlay *appoverlay.Overlay
	publish func(appwire.HistoryUpdatedParams) error // commits one history/updated
	resync  func(epoch uint64)                        // commits one evener/thread/resync

	mu        sync.Mutex // leaf: taken by the append hook
	pending   int64      // recorded length handed to it, not yet projected
	recorded  int64      // latest recorded length seen by the hook
	published int64      // length published
	epoch     uint64
	failures  int
	failed    *transcriptindex.EntryError
	wake      chan struct{} // 1-buffered
	done      chan struct{}
}

func newThreadHistory(cfg threadHistoryConfig) *threadHistory
// recorded is the append hook: it runs under the transcript append lock.
func (h *threadHistory) recorded(rec transcript.Record)
// RecordedLength is the length reads project to: the hook's latest, or the
// file's size when no entry was recorded in this process.
func (h *threadHistory) RecordedLength() (int64, error)
func (h *threadHistory) Epoch() uint64
func (h *threadHistory) Failed() error // non-nil once failed: names the ordinal
func (h *threadHistory) close()
```

Behavior (spec "Live history notifications"):
- `recorded` takes `h.mu`, sets `recorded`/`pending`, calls `overlay.Recorded(rec)`, releases, and wakes the goroutine without blocking.
- One goroutine per thread: `idx := cache.Acquire(path)`; `idx.CatchUpTo(pending)`; `changes := idx.ChangedSince(published)`; when non-empty, `publish(HistoryUpdatedParams{Epoch, Snapshot{changes.Incarnation, changes.Length}, Turns, Items})` with each item `TurnID` set and positions/keys from the index; `published = changes.Length`; release. A changed incarnation (the index rotated) also counts as a failure.
- On any error (projection or publish): `epoch++`, `resync(epoch)`, `idx.Rebuild(pending)`; success resets `failures` and sets `published = pending`; the third consecutive failed rebuild sets `failed` (an `*EntryError` from the index, or `&EntryError{Ordinal: <last attempted>}`), pushes one more resync, and stops projecting. The session keeps running; nothing accumulates (the goroutine returns; `recorded` only updates lengths).
- Test seams (package-level funcs, nil in production): `threadHistoryPublishHook func(threadID string) error` called before publish.

- [ ] **Step 1: Write the failing tests** — a real `transcript.Writer` on a temp file with `OnRecorded(h.recorded)`, a recording `publish`/`resync`: (a) twenty appends from two goroutines publish updates whose item and turn versions cover every ordinal in order, each entry's item exactly once at its final version (the spec's two-goroutine boundary test through projection); (b) a publish that fails once bumps the epoch, pushes resync with it, rebuilds, and the next append publishes with the new epoch; (c) three consecutive failures put the thread in the failed state, `Failed()` names the ordinal, further appends publish nothing, and the writer keeps recording; (d) `RecordedLength` before any append equals the file size; (e) closing stops the goroutine (goleak-free: the test waits on `done`).
- [ ] **Step 2: Run** `go test ./server -run ThreadHistory -count=1 -race` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** — `feat(server): per-thread history projection from recorded entries`

### Task 8: A resync that cannot be delivered ends the subscription

**Files:**
- Test: `internal/appserver/resync_delivery_test.go`; modify `internal/appserver/server.go` only if the test fails.

- [ ] **Step 1: Write the test**: a connection subscribed to a thread whose outbound buffer is full (the existing slow-consumer test helper in `internal/appserver/server_test.go`; find `evictSlowConsumer` tests) receives a committed `evener/thread/resync`: the connection is evicted (unregistered), its subscriptions are gone (`Subscriptions.Threads(conn) == nil`), and a new connection that subscribes and reads gets the current epoch (the test's snapshot func returns it).
- [ ] **Step 2: Run** → if it passes, the behavior already holds (Spec issue 5); otherwise implement the eviction for that path and re-run.
- [ ] **Step 3: Commit** — `test(appserver): an undeliverable resync ends the subscription`

### Task 9: History reads: latest window with the overlay cut, pages, request generations

**Files:**
- Create: `server/history_read.go`
- Test: `server/history_read_test.go`

**Interfaces — Produces:**

```go
// historyRead is what a thread/read with turns returns from the transcript:
// captured inside the subscription cut (overlay, epoch), projected after it.
type historyCapture struct {
	epoch   uint64
	overlay []appwire.OverlayItem
}
func (h *threadHistory) capture() historyCapture // inside the cut: no I/O
// latest projects after the cut: RecordedLength, CatchUpTo, Latest(limit),
// regroup, and the captured overlay minus items the overlay no longer contains.
func (h *threadHistory) latest(c historyCapture, threadRef string, limit int) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, overlay []appwire.OverlayItem, err error)
// before pages from the transcript: a cursor naming another incarnation is
// appwire.TranscriptItemCursorStale().
func (h *threadHistory) before(threadRef, cursor string, limit int) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, err error)
```

- Cursor identity: `appitempaging.CursorIdentity{ThreadRef, Incarnation: window.Incarnation, ProjectionVersion: appitempaging.TranscriptItemProjectionVersion}`.
- A failed thread returns an error whose message names the entry ordinal (`appwire` internal error class; add `appwire.HistoryFailed(ordinal uint64) error` with `evenerErrorInfo "historyFailed"`).
- Cost at egress: turns get `Cost` from the server's cost lookup applied to `Usage` and the candidate's `Model` (a func passed in; the server's `installCostLookup` source).

- [ ] **Step 1: Write the failing tests** (a real writer + `threadHistory` + overlay): latest window equals `transcriptindex` Latest regrouped; a stream captured in the cut whose round's ASSISTANT is recorded before the projection is dropped from the response; the response snapshot length equals the recorded length at projection; a `before` with a cursor from a previous incarnation (rebuild in between) is stale; a failed thread's read errors naming the ordinal.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./server -run HistoryRead -count=1 -race` → PASS.
- [ ] **Step 5: Commit** — `feat(server): history reads from the transcript index with the overlay cut`

### Task 10: Thread history for delegates and the per-server registry

**Files:**
- Create: `server/thread_histories.go`
- Test: `server/thread_histories_test.go`

**Interfaces — Produces:**

```go
// threadHistories owns every thread's history on this server: the root's and
// each delegate's, with one index handle cache and one notice budget.
type threadHistories struct {
	mu      sync.Mutex
	cache   *transcriptindex.Cache
	budget  *appoverlay.Budget
	threads map[string]*threadHistory
}
func (r *threadHistories) ensure(threadID, path string, publish, resync ...) *threadHistory
func (r *threadHistories) get(threadID string) *threadHistory
func (r *threadHistories) drop(threadID string) // closes it and its overlay
func (r *threadHistories) close()
```

- [ ] **Step 1: Failing tests**: two delegates' histories share one cache; reading a delegate that recorded nothing in this process projects its whole file; `drop` closes its overlay (budget bytes returned) and its goroutine; 70 delegates read in turn leave at most 64 open handles.
- [ ] **Step 2-4:** run → FAIL, implement, `go test ./server -run ThreadHistories -race -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(server): a registry of thread histories with one index cache and notice budget`

---

## Unit D: the switch

### Task 11: The daemon switches to transcript history and the overlay

**Files:**
- Modify: `server/server.go` (fields; `SetProcessing`, `SetProcessingTurn`, `setProcessingLocked`), `server/appwire_runtime.go` (`RecordAppEvent`, `finishProcessing`, `RecordDescendantAppEvent`, `ReplaceAppIdentity`, `PrepareAppIdentity*`, read handlers, `appDescendantProjection`), `server/bridge.go` (hand-off unchanged), `internal/appprojector/appwire_projection.go`, `cmd/evener/serve.go` (wire `SetExecutionStartedFunc`, `SetTranscriptRecordedFunc`, `SetDescendantRecordedFunc`)
- Modify: `appwire/types.go` (`ProtocolVersion = "evener-appwire-v6"` and its history comment), `internal/appserver/server.go` (`initialize`: a client announcing an `evener-appwire-v<N>` with N < 6 gets `appwire.UpgradeRequired(...)`: `CodeInvalidRequest` data `evenerErrorInfo "upgradeRequired"`, message naming both versions; add the constructor and TS error mapping in `appwire-client/typescript/errors.ts`)
- Modify tests across `server/` and `internal/appprojector/` that assert today's history notifications (list with `grep -rln "NotifyItemCompleted\|NotifyTurnStarted\|NotifyTurnCompleted\|NotifyItemStarted\|NotifyAgentMessageDelta\|NotifyEvenerSteeringInjected\|appTurnSnapshotForID" server internal/appprojector cmd/evener --include='*_test.go'`); each assertion is rewritten against `history/updated`/overlay notifications or the read, never deleted without an equivalent.
- Test: `server/history_switch_test.go`, `internal/appserver/upgrade_required_test.go`

What changes:
- `AppEventProjector` stops emitting `turn/started`, `turn/completed`, `item/*` and `evener/steering/injected`, and stops minting turn and item ids (`nextTurn`, `nextItem`, `ReserveTurnID`, `ReserveStableTurnID`, `ReleaseReservedTurnID`, `SeedPersistedTurns`, `activeTurnID`, `pendingNotifications` for turns). It keeps thread-level notifications (`thread/started`, `thread/status/changed`, queue, goal, notes, urls, tasks, delegates, model/effort/vision changes, jobs, escalations, name, modelRetry, `thread/closed`). Status transitions it derived from turn carriers now come from `EventExecutionStarted`/`Ended` (active/idle) and `SessionEnd` as today. Delete the dead code in the same task (it has no caller).
- `RecordAppEvent` runs `overlay.Event(ev)` for the root thread and commits its changes (stamping thread id/ref) in the same projection commit as the projector's thread notifications; `RecordDescendantAppEvent` does the same for the delegate's overlay.
- `ReplaceAppIdentity` installs the root `threadHistory` (dropping the previous root's and every delegate's) instead of seeding a snapshot; `PreparedAppIdentity` carries source/thread/ref/projector and the transcript path only (no turns).
- Delegates: `RecordDescendantAppEvent` ensures the delegate's `threadHistory` (path from `appDescendantTranscriptPathFunc`) with no file I/O in the commit (the goroutine does the I/O). `SetDescendantRecordedFunc` routes records to `threadHistories.get(sessionID).recorded` (ensuring it). A released delegate drops its history (find the release path: `grep -n "appDescendants\[" server/*.go`).
- `SetProcessing(true)` sets `processing` only. `SetProcessingTurn(turnID)` commits `thread/status/changed{active, ActiveTurnID: turnID}` and sets `appActiveTurnID`. `finishProcessing` publishes idle with empty `ActiveTurnID`. Every `thread/status/changed` carries `ActiveTurnID`.
- `thread/read` with turns: `CaptureSubscription`'s snapshot func captures the envelope and `historyCapture`; after it returns, `latest` projects; the response carries `Epoch`, `Snapshot`, `Overlay`, `RequestGeneration` (echoed), `OlderCursor`. `thread/turns/list` uses `before`. The "no file I/O inside the cut" doc comments are rewritten to the new rule (I/O happens after the cut, projected to the recorded length).
- `evener/thread/resync` carries `Epoch` (root clear keeps its resync; history failure resyncs come from Task 7).

- [ ] **Step 1: Write the failing tests** (`server/history_switch_test.go`, a real scripted session bridged as in `server/transcript_parity_test.go`'s `bridgeParitySession`, with the new serve wiring reproduced by a helper `wireTranscriptHistory(sess, srv)` that the parity harness will also use): a turn with text, a tool call and communicate produces `history/updated` notifications whose reduced items (a tiny Go reducer in the test: map by key, keep the higher version) equal a fresh `thread/read` of the latest window; no `turn/*`, `item/*` or `evener/steering/injected` notification is ever committed; the overlay notifications for the turn's round end with `overlay/end`; `thread/status/changed` carries the execution's TurnID from `SetProcessingTurn` before any `history/updated` for that turn; a client steer's steering item arrives in history with its `ClientMutationID` and a server-derived id; `internal/appserver`: `initialize` with `evener-appwire-v5` is `upgradeRequired`, with v6 succeeds.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement**, then fix every server/appprojector/cmd/evener test that asserted the removed notifications (rewrite to the new vocabulary; keep each test's intent).
- [ ] **Step 4: Run** `set -o pipefail; go test ./server ./internal/appprojector ./internal/appserver ./cmd/evener -count=1 -race 2>&1 | tail -30` and `go test ./agent -count=1 -timeout 30m` → PASS. `make vet`.
- [ ] **Step 5: Commit** — `feat(server): live history from recorded entries, the overlay, reads from the transcript; AppWire v6`

### Task 12: The hub serves daemonless history from the index and relays the new vocabulary

**Files:**
- Modify: `cmd/evener-hub/app_threadread.go` (`pastEntryLatestItems`, `pastEntryPageItems`, `pastThreadItemReadResponse`, `pastThreadTurnsList`, `pastTranscriptCache` use), `cmd/evener-hub/app_rpc.go` (thread/read and turns/list: echo `RequestGeneration`, pass `Epoch`/`Snapshot`/`Overlay`/`Authoritative`/`Changes` through), `cmd/evener-hub/app_relay.go` (`publishTarget`: enrich `history/updated` and `overlay/upserted` items with output images and image URLs as `item/completed` was), `cmd/evener-hub/output_images.go` (`enrichOutputImageNotification` for the new methods), `cmd/evener-hub/internal/appsource/source.go`, `local_daemon.go`, `remote_hub_source.go` (carry the new response fields; keep the incarnation-rotation rule; `ItemCandidateResult` carries the daemon's snapshot and epoch), relay give-up path (`app_relay.go` ~:1810-1840: instead of synthesizing `turn/completed`, publish `thread/status/changed` idle and `evener/thread/resync`)
- Test: `cmd/evener-hub/app_threadread_index_test.go`, plus updates to hub tests asserting old notifications or `TurnCache` reads

Rules:
- Daemonless `thread/read`: `transcriptindex.Cache` (hub-wide, 64) `Acquire(path)`, `CatchUp()` (to end of file; "drops an incomplete last line"), `Latest(limit)`; `Authoritative: true`; `Snapshot{Incarnation, Length}`; `Epoch: 0`; and when `params.HeldSnapshot` names the current incarnation, `Changes` = `ChangedSince(HeldSnapshot.Length)` minus the items/turns inside the returned window. `thread/turns/list`: `Before(cursor)`, `Authoritative: true`.
- Cost stamping and image enrichment run on the new turns/items as they did on the old ones (`stampItemPageTurns`).
- `DerivedTotalsFromFile`/`FailedToolCallsFromFile`/`ItemTurnsFromFile` callers stay as they are in this task (they read totals, not history pages); the subagent preview (`evener/subagentPreview`, full saved transcript) switches to walking `Before` pages from the index.

- [ ] **Step 1: Write the failing tests**: a daemonless read of a new-format transcript equals the index's latest window, is authoritative and carries the snapshot; a read holding a snapshot at length L after a TOOL_RESULTS landed past L (outside the window: make the window 1 item) returns the completed tool item in `Changes` (the spec's daemonless held-page boundary test); an open execution turn (no completion; daemon gone) reads `inProgress` with no running turn; a relayed `history/updated` carrying a `write_file` call item gets its output image descriptors as `item/completed` did; a `thread/read` response echoes `requestGeneration`.
- [ ] **Step 2-4:** FAIL → implement → `set -o pipefail; go test ./cmd/evener-hub/... -count=1 -race 2>&1 | tail -30` PASS.
- [ ] **Step 5: Commit** — `feat(hub): daemonless history from the transcript index; relay history and overlay`

### Task 13: Server-side steering identity and the running turn in every thread

**Files:** `server/appwire_runtime.go`, `server/thread_envelope.go`; Test `server/steering_identity_test.go`

- [ ] Tests: two steers in one turn produce two history steering items whose ids derive from their keys (`item_steering_<entryIndex>`) and whose `ClientMutationID`s match the steer requests; a delegate's `thread/read` carries `ActiveTurnID` from its overlay's running turn while it runs and none after `EXECUTION_ENDED`. Run → FAIL where behavior is missing; implement; `go test ./server -count=1 -race`; commit `feat(server): steering and running state come from the daemon`.

(If Task 11 already made these pass, this task is the tests alone, committed as `test(server): ...`.)

### Task 14: Fork by transcript key

**Files:**
- Modify: `appwire/types.go` (`ThreadForkParams`: remove `SourceTurnID`, `SourceItemKey` required unless `Aside`), `cmd/evener-hub/app_threadlifecycle.go` (`validateThreadForkParams`, `parseSourceTurnID` → `parseSourceItemKey(key) (entryIndex int, err error)`), `internal/transcriptindex/key.go` (`func ParseItemKey(key string) (turnID string, position appwire.ThreadItemPosition, err error)`), `cmd/evener-tui/hub_commands.go` (~:1296), `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx` (~:79) and any other `sourceTurnId` sender (`grep -rn "sourceTurnId\|SourceTurnID" --include='*.go' --include='*.ts' --include='*.tsx' .`), `make generate`
- Test: `cmd/evener-hub/app_threadlifecycle_fork_key_test.go` (replacing `app_threadlifecycle_fork_turn_id_test.go`), `internal/transcriptindex/key_test.go`

- [ ] Tests: `ParseItemKey` round-trips `ItemKey` for header and entry positions and rejects malformed keys; a fork naming a user message's key forks at that entry (the child holds exactly the entries before it) for a legacy and a new-format transcript; a key naming a non-USER_INPUT entry, a header key and an unknown ordinal are invalid params. Run → FAIL; implement; run the hub and index tests plus `make test-web` (types); commit `feat: fork names its source by transcript key`.

### Task 15: The shared reducer: versioned history, overlay, display rules

**Files:**
- Modify: `appwire-client/typescript/reducer.ts`, `model.ts`, `itemFailure.ts` (display of open turns), `index.ts` (exports)
- Test: `appwire-client/typescript/reducer.history.test.ts`, updates to `reducer.test.ts` / `reducer.edge.test.ts`

**Interfaces — Produces (TypeScript):**

```ts
// model.ts additions
export interface HistoryState {
  epoch: number;
  incarnation?: string;
  length: number;
  appliedGeneration: number; // highest request generation whose response was applied
}
export interface ThreadModel {
  // ...existing
  history: HistoryState;
  overlay: Record<string, OverlayItem>; // by OverlayItem.key
  runningTurnId?: string;
}
export interface ItemModel {
  // ...existing
  version?: number;
  roundId?: string;
  overlayKey?: string; // display items that come from the overlay
}
export interface TurnModel {
  // ...existing
  version?: number;
}

// reducer.ts
export function hydrateThread(resp: ThreadReadResponse, ref: string, now: number): ThreadModel;
export function applyReadResponse(model: ThreadModel, resp: ThreadReadResponse, now: number): ThreadModel; // generation/incarnation/epoch/authoritative rules
export function mergeOlderItemPage(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel; // versioned
export function displayTurnStatus(turn: TurnModel, model: ThreadModel): TurnStatus; // open-turn display rule
```

Rules (spec "Protocol and clients", "Reads", "The live overlay"):
- **Merge by version.** An incoming item replaces the held item with the same `transcriptKey` only when its `version` is higher (or the held item has no version); equal or lower is ignored. Turns likewise by `id`. `history/updated` and read pages use the same function. Merges never remove items.
- **Epoch.** A `history/updated` or response with `epoch` lower than `model.history.epoch` is discarded; a higher one on a read response replaces the thread's history (the store re-reads on a resync push).
- **Request generation.** A response whose `requestGeneration` is lower than `appliedGeneration` is discarded; applying one records its generation.
- **Incarnation.** A response whose `snapshot.incarnation` differs from the held one replaces the whole history. Same incarnation and shorter `length` → discarded (the store re-reads the latest window).
- **Authoritative (daemonless) responses** replace items in the returned position range; a latest-window response also drops every held item past its last position; `changes` are merged by version.
- **Overlay.** `overlay/upserted` stores the item; `overlay/delta` appends to `item.text` or `item.output`; `overlay/reset` drops every overlay item with that `streamId`; `overlay/end` drops every stream, preview and tool item with that `roundId`. A stream item is dropped (and later deltas ignored) once history holds an item with its `roundId`; a preview once history holds an agentMessage with its `callId`; a tool item once the history item with `transcriptKey === historyKey` has a settled status. Read responses replace the overlay with `resp.overlay`.
- **Display.** `model.turns` stays the display structure every consumer reads: history turns ordered by their first item's position; items in a turn by position (entry, item, sub); a tool overlay's `output`, `status` and images are laid over its history item while that item is in progress (identity and version unchanged); stream and preview items appended at the end of their `turnId` (a placeholder turn when history has none yet); notices placed by `anchor` among history items across turns (the turn containing the preceding entry). `displayTurnStatus`: `inProgress` shows as running only while `turn.id === model.runningTurnId`, otherwise as `interrupted`.
- **Running state.** `thread/status/changed` sets `runningTurnId` from `activeTurnId` (cleared on idle).
- **Removed.** The `turn/started`, `turn/completed`, `item/*` and `evener/steering/injected` cases, the `warning` item minting, and the tool call/result fold and coverage machinery that only served unversioned merges (`mergeToolCallsByCallId`, `ToolItemMergeContext`, the `olderItemAddsCoverage` family, `mergeTurnHistoryWithContext`), once no caller outside the reducer needs them (mobile's callers move to the new functions in Task 17; keep `mergeTurnHistory`/`mergeTurnHistoryWithFolds` exported with versioned semantics and the same result shape until Task 17 removes their last use, then delete them there).

- [ ] **Step 1: Write the failing tests** (`reducer.history.test.ts`), one per rule above, each with minimal wire fixtures: a lower-version update is ignored and an equal one is a no-op; a history update never removes an item; an older-epoch update is discarded; a stale-generation response is discarded after a newer one applied (the spec's "stale incarnation response ordering by request generation" client half); a new incarnation replaces; a same-incarnation shorter response is discarded; an authoritative latest window drops items past it and keeps older pages; `changes` refresh a held tool item; stream delta/reset/next attempt; coverage by roundId ignores late deltas; preview survives the ASSISTANT item and goes with the COMMUNICATE item; tool overlay laid over an in-progress item and dropped when it settles; notice anchors order among items of two turns by `sub`; open-turn display rule; steering items only from history (no minted ids).
- [ ] **Step 2: Run** `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/reducer.history.test.ts` → FAIL.
- [ ] **Step 3: Implement**; rewrite `reducer.test.ts` cases that asserted removed notifications to the new vocabulary, keeping their intent.
- [ ] **Step 4: Run** `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/reducer.ts ../../../appwire-client/typescript/model.ts ../../../appwire-client/typescript/reducer.history.test.ts` then `make test-web` → the appwire-client tests pass (frontend failures are Task 16's).
- [ ] **Step 5: Commit** — `feat(appwire-client): versioned history, the overlay and the open-turn display rule in the reducer`

### Task 16: The web store and components

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` (request generations on every `thread/read` and `thread/turns/list`, passing `requestGeneration` and, for daemonless refs, `heldSnapshot`; `applyReadResponse`; resync with epoch; discard rules), components that read turn/item status or the removed notifications (`panes/session/transcript/**`, `panes/session/chrome/statusFormat.ts`, `composer/queue/pendingTurnsStore.ts`, `shell/palette/*`, `flow/useTranscriptScroll.ts` prepend/resync detection), `transcriptProjector.ts` (anchors by position, not array index, where it assigns `sourceIndex`)
- Test: existing frontend tests updated; new `stores/threads.history.test.ts`

- [ ] Tests: a slow response to an older request generation arriving after a newer one does not overwrite it; a resync push with a higher epoch re-reads and replaces; a daemonless backfill page keeps newer pages; an open turn with no running turn renders as interrupted (component test on `TurnBlock`/`TurnSeparator`); notices render at their anchors. Run `make test-web` → FAIL; implement; `cd cmd/evener-hub/frontend && npx biome check --write <touched>`; `make test-web` PASS; on a Chrome-capable host `make test-web-browser`. Commit `feat(web): the frontend reads versioned history and the overlay`.

### Task 17: Mobile and the TUI

**Files:**
- Modify: `mobile/src/state/conversation.ts`, `mobile/src/services/conversation.ts`, `mobile/src/conversation/project.ts`, `mobile-native/src/*` files that read turns (`projectedRows.ts`, `timeline.ts`, `readerPosition.ts`, `transcriptPresentation.ts`, `screens.tsx`, `activityRetention.ts`), `appwire-client/typescript/reducer.ts` (delete `mergeTurnHistory*` once unused)
- Modify: `cmd/evener-tui/hub_notifications.go` (~:94-230, :373-387, :593, :924), `cmd/evener-tui/hub_session_keys.go`, TUI transcript rendering of reads
- Test: mobile tests under `mobile/src` and `mobile-native/src`; TUI tests `cmd/evener-tui/*_test.go` asserting the removed notifications

- [ ] Mobile: `rehydrate` and `loadOlder` use `applyReadResponse`/`mergeOlderItemPage`; the retained-turn bound and compacted skeletons keep working (they operate on `model.turns`); the "gap" rule (a transcript frame that leaves `turns` unchanged by reference) is rewritten for `history/updated` (a history update whose epoch is newer than held triggers a re-read); request generations as in Task 16. Tests: the mobile gap/resync tests rewritten to the new vocabulary; a stale-generation response is discarded. Run `make test-native` → FAIL, implement, PASS.
- [ ] TUI: render history from `history/updated` (merge by version into its transcript model) and the overlay (streams, tool output, notices); pending-send matching (`hub_notifications.go:360-387`) matches history steering/user items by `ClientMutationID`. Run `go test ./cmd/evener-tui/... -count=1` → FAIL, implement, PASS.
- [ ] Commit — `feat(mobile,tui): read versioned history and the overlay`

---

## Unit E: proof

### Task 18: The parity harness at zero, and the boundary tests

**Files:**
- Modify: `server/transcript_parity_test.go`, `server/transcript_parity_diff_test.go`
- Create: `server/history_boundary_test.go`

Parity (spec "Acceptance criteria — Parity"): "live" is the reduced `history/updated` stream (Go test reducer: by key, higher version wins; turns by id) plus nothing else; "reload" is a fresh `thread/read` walked back through every `thread/turns/list` page; "whole-file" is a fresh index built in a new sidecar directory over the transcript at the end, walked the same way. The scenario gains a crash-and-restore of an open execution (write a transcript whose last execution has no completion, restore) and a reclaimed client-mutation turn (the phase 2 Task 23 seam). Checks at each point: live = reload = whole-file with **zero** divergences for new-format entries. The legacy tables are replaced by one table for the legacy-prefix scenario only (a transcript written in the legacy format, then resumed): every row must cite the spec's "Legacy entries" rule; there must be no row for a new-format entry. Rows from phase 1 whose phase was 3 are all removed.

Boundary tests (`history_boundary_test.go`; each pauses at a synchronization boundary with channels, no sleeps; scripted provider):
1. `TestReadBetweenAssistantAndCommunicate` — hold the COMMUNICATE append (a recorded hook that blocks when it sees the ASSISTANT entry's round until released) and read: the read has the ASSISTANT items and the preview in the overlay; after release the COMMUNICATE item arrives by `history/updated` and the reduced client state holds the message once.
2. `TestPaginatedReadOverlappingCrossProcessRollback` — a second process's write is simulated by appending a line to the file and truncating it back (the index sees the file stop extending); a backfill with the old cursor is stale; the next latest read has a new incarnation and the client replaces.
3. `TestFailedHistoryPublication` — `threadHistoryPublishHook` fails once: resync with a new epoch, the client re-reads, no item missing. `TestFailedResyncDelivery` — the connection with a full buffer is evicted and its reconnect read carries the new epoch.
4. `TestReadOverlappingRetryResetAndNextAttempt` — pause after the reset of attempt 1 and the first delta of attempt 2 are committed, read: the overlay holds only attempt 2's stream; then the client applies later deltas once.
5. `TestReclaimedTurn` — the reclaimed turn reopens (history status `inProgress` while running), then completes; the client shows it running only while `ActiveTurnID` names it.
6. `TestReadCutBeforeToolResultsProjectedAfter` — capture the cut, then append TOOL_RESULTS before the projection runs (hook in `threadHistory.latest` between capture and projection): the read contains the completed tool item and the tool overlay is gone; the later `history/updated` is a no-op at equal version.
7. `TestDaemonlessClientHoldingPageWhenToolResultsLands` — at the hub (Task 12's harness): covered there; this file references it in a comment only if the hub test is the single owner.
8. `TestAsyncAttentionWriteAfterCompletion` — an attention delivery released after the completion is recorded appears in history as its own delivery turn, never inside the completed turn.
9. `TestConcurrentAppendsProjectInOrdinalOrder` — two goroutines append through the session writer and a cold writer on the same file; the published versions cover every ordinal in order (with Task 7's unit test, this pins the carried-over finding end to end).
10. `TestStaleIncarnationResponseOrdering` — server half: a read served before a rebuild and one served after carry their request generations and incarnations; the TS half is Task 15's test.

- [ ] **Step 1: Write the tests.** **Step 2: Run** `go test ./server -run 'TestTranscriptParity|Boundary|TestRead|TestPaginated|TestFailed|TestReclaimed|TestAsync|TestConcurrent|TestStale' -count=1 -race -v 2>&1 | tail -60` → every divergence or failure is a bug to fix in the owning code (not a table row). **Step 3: Fix.** **Step 4: Run** 20 times (`-count=20`) to prove stability. **Step 5: Commit** — `test(server): parity at zero divergences and the activation boundary tests`

### Task 19: Acceptance measurements

**Files:**
- Create: `server/history_acceptance_test.go` (opt-in), `scripts/measure-transcript-read-model.sh`
- Delete: `server/appwire_turns_latency_test.go` (its baseline is recorded above)

- `TestRealSessionRetainedHistoryMemory` (skip unless `EVENER_TRM_SESSION_DIR`): for each `*.transcript.jsonl` in the dir, copy the header to a fresh file in `t.TempDir()`, register a thread history for it on one `Server` (root = the file whose header has no parent), then replay its activity: append every entry line through a `transcript.Writer` (`PlaceVerbatim`) with the thread's recorded hook installed, and feed the overlay a synthetic `round_timings` notice per 20 entries; wait until every thread published its final length; close writers; `runtime.GC()` twice; report `retained = HeapAlloc(after) - HeapAlloc(before replay)`, the index cache's open handle count, the overlay budget's bytes and each ring's max bytes. Pass: retained history memory is only the handle cache and projection goroutine state (report the figure; the criterion is judged from it: no term grows with history size — also run with the 20 smallest transcripts and compare per-thread figures), notice bytes ≤ 64 KB per thread and ≤ 16 MB total.
- `TestRealTranscriptHistoryReadLatency` (skip unless `EVENER_TRANSCRIPT_INDEX_REAL`): a `Server` with one thread history on a copy of the file; 200 samples of the full `thread/read` handler path (capture + latest + regroup + JSON encoding) at the default page size, idle; then 200 while a goroutine appends one entry line every 100 ms. Log `latency history-read-idle` and `history-read-appending` p50/p99 in the phase 1 quantile rule.
- The script runs both with `-v`, keeps full logs in a temp dir it prints, prints only the result lines and PASS/FAIL against: idle p99 < 50 ms and ≤ 2× the baseline table above; appending p99 < 100 ms.

- [ ] Write the tests and script; run each with the env unset (skip) and set (the real copies); record the numbers in this plan's "Acceptance results" section below and in the PR body. If a criterion fails, do not change the criterion: record it, and the PR goes up as a draft (brief).
- [ ] Commit — `test(server): opt-in acceptance measurements for memory and read latency`

---

## Unit F: deletion

### Task 20: Delete snapshot history and everything that only served it

Delete, after Tasks 11-19 are green:
- `server/appwire_turns.go`: `appTurnSnapshot` and its reducer, paging, clones, merge, `Seed`, `appTurnSeed`, prelude shifting, `rotateIncarnationLocked`, `appTurnsFromNotifications`, the projection helpers (`appTurnsFromTranscriptFile`, `appTurnProjection*`, `positionAppItems`, `positionMissingAppItems`) — the file goes.
- `server/appwire_runtime.go`: the prepared transcript cache (`preparedTranscriptItemCache`, `preparedItemProjector`, `preparedItemIndexIncarnation`), `fromTranscriptFile`'s projection, delegate seeding, `snapshot` fields on `pendingAppNotification`/`taskCarrierTarget` and `finishProcessing`'s snapshot identity filter, `appAllTurns`, `appTurnSnapshotForID`, `appLatestItemTurns`.
- `server/server.go`: `appTurns`, `appDescendantProjection.turns`, `appProcessingReservedTurnID`, `appReservedTurnID`, `appPendingStableTurnID` and what only they served.
- The cut I/O rule's tests that pinned "no file reads under the cut" (`TestServerAppWireInstalledSnapshotNeedsNoTranscriptReads`, `TestAtomicRejoinDoesNotReadTranscriptAheadOfBlockedEvent`) and `apptranscript.InstallReadObserverForTesting`; keep `TestServerAppWireReadCutTakesTheSnapshotInsideTheSubscription`, `TestThreadReadUnderTheCutReachesNoSession` and the appserver `TestSnapshotCut*` tests, rewritten to the new capture if needed (they pin the cut itself, which stays).
- The snapshot-only test files: `server/appwire_turns_paging_test.go`, `appwire_turns_differential_test.go`, `appwire_turns_prelude_test.go`, `appwire_turns_images_test.go`, `appwire_turns_typed_delta_test.go`, `appwire_turns_fuzz_test.go`, `appwire_turns_catalog_types_test.go`, `transcript_only_seed_test.go`, `appwire_turn_sequence_parity_test.go` — each checked for a behavior still worth pinning under the new model; such a behavior moves to a new test before its file is deleted.
- `internal/apptranscript`: `TurnCache`, `turn_index.go`, `item_paging.go`, `ItemTurnProjection*`, `TurnGrouper` users other than the index — whatever has no production caller after Task 12 (`staticcheck`/`make lint` `unused` finds the rest). Remove the `.appwire-index.json` sidecar writers.
- `appwire`: `NotifyTurnStarted`, `NotifyTurnCompleted`, `NotifyItemStarted`, `NotifyItemCompleted`, `NotifyAgentMessageDelta`, `NotifyAgentMessageReset`, `NotifyReasoningSummaryDelta`, `NotifyToolOutputDelta`, `NotifyEvenerSteeringInjected` and their params types when unused; `make generate`.
- appwire-client: the unversioned fold/coverage machinery (Task 15's list) and every remaining reference.

- [ ] **Step 1:** Delete in that order, building and testing after each group (`go build ./... && go vet ./...`, the touched packages' tests, `make test-web`).
- [ ] **Step 2:** `git diff --stat <Task 19 commit>..HEAD` → record lines removed for the PR.
- [ ] **Step 3: Commit** (one commit per group) — `refactor(server): delete snapshot history`, `refactor(apptranscript): delete the JSON turn index and its caches`, `refactor(appwire): drop the turn and item notifications`, `refactor(appwire-client): drop the unversioned history merge`

## Unit G: gates and PR

### Task 21: Simplify, gates, PR, roborev

- [ ] Run the `simplify` skill over the whole diff from `wip/trm-phase2d`; apply worthwhile fixes; re-test; commit.
- [ ] Gates, pristine: `set -o pipefail; go test ./agent -count=1 -timeout 30m`, `go test -race ./agent/transcript ./internal/transcriptindex ./internal/appoverlay ./internal/apptranscript ./internal/appserver ./server ./cmd/evener-hub/... -count=1`, `make vet`, `make lint`, `make test-web`, `make test-native`, `WEB=0 make test`, `make test-web-browser` on a Chrome-capable host.
- [ ] Push `wip/trm-phase3`; open the PR against `wip/trm-phase2d` (body per brief: what/why with links to #2301, #2303, #2305-#2308, the commit stack, acceptance numbers, boundary tests, protocol bump and mobile-must-update note, local gates, CI only runs for PRs into main). If an acceptance criterion failed, open it as a draft with the numbers.
- [ ] roborev rounds (brief steps 2-8), up to three.

## Acceptance results

(Filled in by Task 19.)

## Self-review

- Spec coverage: persisted identity consumption (Tasks 2-3), turn status from facts (3), projector rules incl. communicate, notices, reasoning summary, text runs, fold copies (2-3), incremental state (Spec issue 1; 3, 7), live history notifications and ordering (7, 11, 18), failure and resync epochs, failed state, subscription termination (7, 8, 18), overlay streams/roundID/attempt/reset/end, previews exempt, tool state with 256 KB and display rule, running state, notices with caps and anchors incl. `sub` (6, 11, 15), reads with cut and recorded length, request generations, snapshot identity, incarnation rules, authoritative daemonless replacement and changes-since (9, 12, 15, 16), index sidecar shared by hub and daemon and handle cache (4, 10, 12), fork by key (14), protocol major bump and upgrade required (11), reducer (15), web (16), mobile and TUI (17), SetProcessingTurn (5, 11), server-side steering identity (13), deletion list (20), boundary tests (18), acceptance (19).
- Every carried-over finding has a test: request generation ordering (15, 18.10), queue fed under the append lock and two-goroutine order (7, 18.9), daemonless held-range refresh (12), tool execution state dropped once TOOL_RESULTS is in the read (9, 18.6), turn summary legacy latch and new-format failure/retry (3), persistent projection failure (7).
