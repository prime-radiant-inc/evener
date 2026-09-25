# Transcript Read Model Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the two phase 1 gates of the transcript read model: a parity harness that pins today's live-versus-file divergences, and a seekable on-disk transcript index whose window reads equal the whole-file projection and meet the latency criteria.

**Architecture:** A new package `internal/transcriptindex` keeps fixed-size item and turn-summary records in a sidecar directory next to a transcript. It reproduces today's file projection (grouping, IDs, merge, status, usage) by projecting only each item's contributor entries with `ReadAt`, under the spec's entry-ordinal positions. `internal/apptranscript` exports the few primitives the index needs (per-part projection, the grouping rule, the merge, the turn stamp) so both paths share one implementation. The parity harness lives in `server` and drives a real `agent.Session` with a scripted provider through `BridgeEvent` into a real `Server`, then diffs the live snapshot against the file projection through a table of known divergences.

**Tech Stack:** Go 1.x (module `primeradiant.com/evener`), standard library only (`encoding/binary`, `crypto/sha256`, `os.File.ReadAt`).

**Spec:** `docs/superpowers/specs/2026-09-25-transcript-read-model-design.md` (revision 7; the plan was written against revision 6 and Task 4b aligns the index with revision 7), sections "Recorded length, entry ordinals and Seq", "Index", "Acceptance criteria", "Migration" phase 1. Also `docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`.

## Global Constraints

- Default tests are deterministic: no provider credentials, network, or ambient machine state. The scripted provider sits at the LLM boundary; real Evener code runs below it (AGENTS.md, `docs/developing-evener/testing.md`).
- Real-transcript runs are opt-in only: `EVENER_TRANSCRIPT_INDEX_REAL=<path>[:<path>...]`. Never read Jesse's state directory from a default test; measurement copies live under `/tmp/claude-1000/trm-data/`.
- Identity and order use the entry ordinal: the 0-based index of a non-blank entry line after the header. `Seq` is never used (real transcripts contain duplicate `Seq`s).
- Position is `{entry: ordinal + 1, item: part}`; header-derived prelude items use `{entry: 0, item: i}`.
- Item key: `apptranscript-item-v2:<turnID>:<ordinal>:<part>`; prelude items use `apptranscript-item-v2:<turnID>:header:<i>`.
- Phase 1 reproduces TODAY's grouping, turn IDs, item IDs, call-id merge, turn status, error and usage exactly. Only positions and keys follow the spec's scheme.
- The index is not wired into production reads in this phase and must not change `agent/transcript`'s writer.
- Latency criteria (spec): latest window at the default page size (40), 200 samples, idle writer p99 < 50 ms and ≤ 2× today's in-memory read; appending one entry per 100 ms p99 < 100 ms.
- Per-test ceiling in the default suite is 3 s (`testing-budget.json`).
- Commit messages end with `Claude-Session: https://claude.ai/code/session_01ALoA3J8ymsuJuXoApTZqLJ`.

## Review Focus

- A transcript whose final line is incomplete (writer mid-append): the index stops at the last complete line and picks the tail up on the next catch-up.
- Duplicate `Seq` values and blank lines between entries: ordinals count non-blank entry lines only, exactly as `scanSemanticTranscript` does.
- The transcript replaced or truncated under a live index (rename over the path, truncate and regrow): validation by file identity, length and trailing bytes rebuilds instead of serving stale records.
- A crash between the index's record writes and its meta write: replaying the same entries must not double usage or duplicate items (versions make updates idempotent).
- Legacy shapes the fast path cannot see locally: a tool result with no name whose call sits in an earlier turn, and a call id reused three times in one turn. Both must still equal the whole-file projection.

Each of these is pinned by a test in Task 3 or Task 4.

## File Structure

- `internal/apptranscript/apptranscript.go` — `ProjectTurnParts` (items plus content part index); `ProjectTurn` wraps it.
- `internal/apptranscript/logical_turn.go` — exported `TurnGrouper`, `MergeThreadItems`, `StampGroupedTurn`; the accumulator uses them.
- `internal/apptranscript/turn_index.go` — `FileIdentity` exported (rename of `fileIdentity`).
- `internal/transcriptindex/records.go` — fixed-size record encodings.
- `internal/transcriptindex/build.go` — applying one entry to the index (grouping, merge contributors, turn summaries, tool-name seeds).
- `internal/transcriptindex/index.go` — `Open`, validation, rebuild, `CatchUp`, meta, `Close`.
- `internal/transcriptindex/window.go` — `Latest`, `Before`, item and turn reconstruction.
- `internal/transcriptindex/*_test.go` — fixture corpus, reference projection, window equality, append/reopen/crash, opt-in real data and latency.
- `server/appwire_turns_latency_test.go` — opt-in baseline: today's `appTurnSnapshot.LatestItemCandidates` on the same transcript.
- `server/transcript_parity_test.go` and `server/transcript_parity_diff_test.go` — the parity harness and its divergence classifier.

---

### Task 1: Per-part projection in apptranscript

**Files:**
- Modify: `internal/apptranscript/apptranscript.go` (ProjectTurn, ~lines 305-640)
- Test: `internal/apptranscript/project_turn_parts_test.go`

**Interfaces:**
- Produces: `func ProjectTurnParts(turnID string, turnIndex int, turn schema.Turn, toolNames map[string]string, imageProjector ImageProjector, outputImageProjector OutputImageProjector) (items []appwire.ThreadItem, parts []int)` — `parts[i]` is the content part index item `i` came from; kinds that project the whole entry report `0`. `ProjectTurn` returns the same items.

- [ ] **Step 1: Write the failing test**

```go
package apptranscript

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestProjectTurnPartsReportsTheContentPartOfEachItem(t *testing.T) {
	communicate := json.RawMessage(`{"message":"hello"}`)
	assistant := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: ""}, // empty: no item, still part 0
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think"}},
		{Kind: llm.ContentText, Text: "hello"},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: communicate}}, // echo: hidden
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c2", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
	}}}
	items, parts := ProjectTurnParts("turn_1", 1, assistant, map[string]string{}, nil, nil)
	if !reflect.DeepEqual(parts, []int{1, 2, 4}) {
		t.Fatalf("parts = %v, want [1 2 4]", parts)
	}
	if want := ProjectTurn("turn_1", 1, assistant, map[string]string{}, nil, nil); !reflect.DeepEqual(items, want) {
		t.Fatalf("ProjectTurnParts items differ from ProjectTurn")
	}

	results := schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "communicate"}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c2", Name: "read_file", Content: "ok"}},
	}}}
	if _, parts := ProjectTurnParts("turn_2", 2, results, map[string]string{}, nil, nil); !reflect.DeepEqual(parts, []int{1}) {
		t.Fatalf("tool result parts = %v, want [1]", parts)
	}

	user := schema.NewTurn(schema.TurnUserInput, llm.User("hi"))
	if _, parts := ProjectTurnParts("turn_3", 3, user, nil, nil, nil); !reflect.DeepEqual(parts, []int{0}) {
		t.Fatalf("user parts = %v, want [0]", parts)
	}
	empty := schema.NewTurn(schema.TurnModelSwitch, llm.User(""))
	if items, parts := ProjectTurnParts("turn_4", 4, empty, nil, nil, nil); len(items) != 0 || len(parts) != 0 {
		t.Fatalf("empty model switch projected %v / %v", items, parts)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/apptranscript -run TestProjectTurnParts -count=1`
Expected: FAIL, `undefined: ProjectTurnParts`.

- [ ] **Step 3: Implement**

Rename the body of `ProjectTurn` to `ProjectTurnParts` with named results `(out []appwire.ThreadItem, parts []int)`. Every single-item `return []appwire.ThreadItem{{...}}` becomes `return []appwire.ThreadItem{{...}}, []int{0}`; every `return nil` becomes `return nil, nil`; the item-building hook branch returns `[]appwire.ThreadItem{item}, []int{0}`. In the ASSISTANT and TOOL/TOOL_RESULTS loops add `parts = append(parts, i)` next to each `items = append(items, ...)` and `return items, parts`. Then:

```go
// ProjectTurn maps a typed transcript turn into AppWire transcript items.
func ProjectTurn(turnID string, turnIndex int, turn schema.Turn, toolNames map[string]string, imageProjector ImageProjector, outputImageProjector OutputImageProjector) []appwire.ThreadItem {
	items, _ := ProjectTurnParts(turnID, turnIndex, turn, toolNames, imageProjector, outputImageProjector)
	return items
}

// ProjectTurnParts is ProjectTurn plus, for each item, the index of the entry
// content part it came from. Each part projects at most one item, so the index
// names an item within its entry. A kind that projects the whole entry as one
// item reports part 0. Hidden parts (an echoed communicate, empty text) still
// occupy their index, so a part's index never depends on its neighbours.
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/apptranscript -count=1`
Expected: PASS (new test and every existing test).

- [ ] **Step 5: Commit** — `feat(apptranscript): report the content part of each projected item`

### Task 2: Export the grouping, merge and stamp rules

**Files:**
- Modify: `internal/apptranscript/logical_turn.go`, `internal/apptranscript/item_paging.go` (caller of the stamp), `internal/apptranscript/turn_index.go` (`fileIdentity` → `FileIdentity`, all callers)
- Test: `internal/apptranscript/turn_grouper_test.go`

**Interfaces:**
- Produces:
  - `type TurnGrouper struct { Open bool; TurnID string }` with `func (g *TurnGrouper) Place(entry schema.Turn, entryIndex int) (turnID string, newTurn bool)` — `entryIndex` is 1-based (ordinal + 1).
  - `func MergeThreadItems(existing, incoming appwire.ThreadItem) appwire.ThreadItem` (renamed `mergeAppThreadItems`).
  - `func StampGroupedTurn(turn *appwire.Turn, entries []schema.Turn)` (renamed `stampGroupedTurnFromEntries`).
  - `func FileIdentity(info os.FileInfo) string` (renamed `fileIdentity`).

- [ ] **Step 1: Write the failing test**

```go
package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestTurnGrouperPlacesEntriesLikeTheAccumulator(t *testing.T) {
	user := schema.NewTurn(schema.TurnUserInput, llm.User("u"))
	assistant := schema.NewTurn(schema.TurnAssistant, llm.Assistant("a"))
	hook := schema.NewTurn(schema.TurnHookCompleted, llm.User("h"))
	owned := schema.NewTurn(schema.TurnSteering, llm.User("s"))
	owned.OwningTurnID = "turn_1"
	stable := schema.NewTurn(schema.TurnUserInput, llm.User("m"))
	stable.StableTurnID = "turn_m7"

	var g TurnGrouper
	steps := []struct {
		entry   schema.Turn
		wantID  string
		wantNew bool
	}{
		{assistant, "turn_1", true}, // stray continuation opens its own group
		{user, "turn_2", true},
		{assistant, "turn_2", false},
		{owned, "turn_1", true}, // owner is not the open turn: new group under the owner id
		{hook, "turn_5", true},  // standalone closes
		{assistant, "turn_6", true},
		{stable, "turn_m7", true},
		{owned, "turn_1", true},
	}
	for i, step := range steps {
		id, isNew := g.Place(step.entry, i+1)
		if id != step.wantID || isNew != step.wantNew {
			t.Fatalf("entry %d: Place = (%q, %v), want (%q, %v)", i+1, id, isNew, step.wantID, step.wantNew)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails** — `go test ./internal/apptranscript -run TestTurnGrouper -count=1`; expected `undefined: TurnGrouper`.

- [ ] **Step 3: Implement**

```go
// TurnGrouper applies the logical-turn grouping rule to entries in file order.
// Its zero value is the state before the first entry. Open and TurnID are the
// whole state, so a caller that persists them can resume grouping later.
type TurnGrouper struct {
	// Open reports whether continuations join the latest group.
	Open bool
	// TurnID is the latest group's turn id.
	TurnID string
}

// Place reports the turn the entry belongs to and whether it starts a new
// logical turn. entryIndex is the entry's 1-based index (its ordinal + 1).
func (g *TurnGrouper) Place(entry schema.Turn, entryIndex int) (turnID string, newTurn bool) {
	kind, owner := entry.Kind, entry.OwningTurnID
	switch {
	case opensLogicalTurn(kind, entry.GoalContinuation != nil):
		g.TurnID, g.Open = persistedTurnID(entry, entryIndex), true
		return g.TurnID, true
	case kind == schema.TurnSteering && owner != "":
		if g.Open && g.TurnID == owner {
			return g.TurnID, false
		}
		g.TurnID, g.Open = owner, true
		return g.TurnID, true
	case continuesLogicalTurn(kind) && g.Open:
		return g.TurnID, false
	default:
		// Standalone kind, or a continuation with no open group: its own
		// group. A standalone closes it; a stray continuation stays open.
		g.TurnID, g.Open = persistedTurnID(entry, entryIndex), continuesLogicalTurn(kind)
		return g.TurnID, true
	}
}
```

`logicalTurnAccumulator` drops its `open` field for a `grouper TurnGrouper`, and `appendEntry` becomes:

```go
func (a *logicalTurnAccumulator) appendEntry(entry schema.Turn, entryIndex int, items []appwire.ThreadItem) {
	if turnID, newTurn := a.grouper.Place(entry, entryIndex); newTurn {
		a.turns = append(a.turns, groupedTurn{turnID: turnID})
	}
	last := &a.turns[len(a.turns)-1]
	last.entries = append(last.entries, entry)
	if len(items) > 0 {
		last.items = append(last.items, items...)
	}
}
```

Rename `mergeAppThreadItems` → `MergeThreadItems`, `stampGroupedTurnFromEntries` → `StampGroupedTurn`, `fileIdentity` → `FileIdentity` (doc comments updated to the exported names; `gofmt`/callers via `grep -rn`).

- [ ] **Step 4: Run** — `go test ./internal/apptranscript ./server -count=1`; expected PASS.

- [ ] **Step 5: Commit** — `refactor(apptranscript): export the grouping, merge and turn stamp rules`

### Task 3: Fixture corpus and the reference projection

The index is proven equal to a *reference*: today's whole-file projection with the spec's positions and keys. The reference is test code built only from the exported production rules, and this task proves it equals `apptranscript.ItemTurnProjectionFromFile` once positions and keys are stripped. Later tasks compare the index to the reference.

**Files:**
- Create: `internal/transcriptindex/key.go` (package doc + `ItemKey`)
- Create: `internal/transcriptindex/fixture_test.go`, `internal/transcriptindex/reference_test.go`

**Interfaces:**
- Produces:
  - `func ItemKey(turnID string, position appwire.ThreadItemPosition) string`
  - test helpers: `writeFixture(t, fixture) string` (path), `fixtures() []fixture` (named corpus), `referenceTurns(t, path) []appwire.Turn`, `referenceCandidates(t, path) []appitempaging.TranscriptItemCandidate`.

- [ ] **Step 1: Write the fixture corpus and the failing equivalence test**

`fixture_test.go` writes raw JSONL (header line via `json.Marshal(transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, ...})`, entries via `json.Marshal(transcript.Entry{Kind: "entry", Seq: seq, Turn: turn, MachineryFlagged: true})`), so fixtures can carry duplicate `Seq`s, blank lines and an unterminated tail. A `fixture` is `{name string; header transcript.Header; lines []fixtureLine}` where `fixtureLine` is either an entry (`turn schema.Turn`, optional `seq` override) or a blank line. Corpus (one fixture each, plus one "everything" fixture concatenating them):

1. prelude (system prompt) + user + assistant text/reasoning/tool call + TOOL_RESULTS (merge) with an image result (`ImageData` PNG bytes) + usage and timestamps on several entries;
2. communicate echo and non-echo; communicate result hidden;
3. orphan TOOL_RESULTS whose call is in the previous group (hook between), with and without a result name;
4. a nameless TOOL_RESULTS whose call was a `communicate` call (skipped) and one after a `communicate`-named result deleted the name;
5. call id reused three times in one group (three contributor entries) and twice inside one entry;
6. steering: plain, owned by the open turn, owned by an earlier turn, interrupted steering; goal-continuation steering opener;
7. TURN_FAILURE twice in one group (last error wins), failure without text;
8. standalone kinds: MODEL_SWITCH, CHECKPOINT, SUMMARY, ENVIRONMENT, NOTES_CONTEXT, HOOK_COMPLETED, ATTENTION_RESOLUTION (zero items), empty-text markers (zero-item groups);
9. `StableTurnID` openers (`turn_m3`), duplicate `Seq`s (9390, 9391, 9390, 9391), blank lines between entries.

`reference_test.go`:

```go
// referenceTurns is today's whole-file projection with the spec's positions:
// the production grouping, per-entry projection, call-id merge and turn stamp,
// with each item keyed by the entry and content part that opened it.
func referenceTurns(t testing.TB, path string) []appwire.Turn {
	t.Helper()
	header, entries := readFixtureFile(t, path) // strict: DecodeHeader/DecodeEntry, blank lines skipped
	type group struct {
		id        string
		entries   []schema.Turn
		items     []appwire.ThreadItem
		positions []appwire.ThreadItemPosition
		calls     map[string]int
	}
	var groups []*group
	var grouper apptranscript.TurnGrouper
	toolNames := map[string]string{}
	for i, entry := range entries {
		entryIndex := i + 1
		id, isNew := grouper.Place(entry, entryIndex)
		if isNew {
			groups = append(groups, &group{id: id, calls: map[string]int{}})
		}
		g := groups[len(groups)-1]
		g.entries = append(g.entries, entry)
		items, parts := apptranscript.ProjectTurnParts(id, entryIndex, entry, toolNames, nil, apptranscript.ToolResultOutputImages)
		for j, item := range items {
			if item.Type == "commandExecution" && item.CallID != "" {
				if at, ok := g.calls[item.CallID]; ok {
					g.items[at] = apptranscript.MergeThreadItems(g.items[at], item)
					continue
				}
				g.calls[item.CallID] = len(g.items)
			}
			g.items = append(g.items, item)
			g.positions = append(g.positions, appwire.ThreadItemPosition{Entry: uint64(entryIndex), Item: uint32(parts[j])})
		}
	}
	var turns []appwire.Turn
	if prelude := apptranscript.PreludeTurn(header); prelude != nil {
		for i := range prelude.Items {
			position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
			prelude.Items[i].Position = &position
			prelude.Items[i].TranscriptKey = ItemKey(prelude.ID, position)
		}
		turns = append(turns, *prelude)
	}
	for _, g := range groups {
		if len(g.items) == 0 {
			continue
		}
		turn := appwire.Turn{ID: g.id, ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted}
		for j := range g.items {
			position := g.positions[j]
			g.items[j].TurnID = g.id
			g.items[j].Position = &position
			g.items[j].TranscriptKey = ItemKey(g.id, position)
		}
		turn.Items = g.items
		apptranscript.StampGroupedTurn(&turn, g.entries)
		turns = append(turns, turn)
	}
	return turns
}

func TestReferenceEqualsTodaysFileProjectionApartFromPositions(t *testing.T) {
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			path := writeFixture(t, fx)
			toolNames := map[string]string{}
			today, err := apptranscript.ItemTurnsFromFile(path, 128<<20, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
				return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages)
			})
			if err != nil {
				t.Fatal(err)
			}
			reference := referenceTurns(t, path)
			if len(reference) == 0 {
				t.Fatal("fixture projected no turns")
			}
			if !reflect.DeepEqual(stripPositions(today), stripPositions(reference)) {
				t.Fatalf("reference diverges from today's projection:\ntoday: %s\nref:   %s", dump(today), dump(reference))
			}
		})
	}
}
```

`stripPositions` clears every item's `Position` and `TranscriptKey`. `referenceCandidates` runs `appitempaging.CandidatesFromTurns(referenceTurns(...))` and clears each candidate's `Turn.Items`.

- [ ] **Step 2: Run** — `go test ./internal/transcriptindex -count=1`; expected FAIL (`undefined: ItemKey`).

- [ ] **Step 3: Implement `key.go`**

```go
// Package transcriptindex is a seekable, derived index over one session
// transcript. It answers item-window reads by projecting only the entries that
// contribute to the returned items, found through fixed-size records in a
// sidecar directory, and never decodes a whole turn or file to do it.
package transcriptindex

// ItemKey is the transcript key of the item at position in turn turnID:
// apptranscript-item-v2:<turnID>:<entry ordinal>:<part>. Header-derived
// prelude items, at entry 0, name "header" in place of the ordinal.
func ItemKey(turnID string, position appwire.ThreadItemPosition) string {
	if position.Entry == 0 {
		return fmt.Sprintf("apptranscript-item-v2:%s:header:%d", turnID, position.Item)
	}
	return fmt.Sprintf("apptranscript-item-v2:%s:%d:%d", turnID, position.Entry-1, position.Item)
}
```

- [ ] **Step 4: Run** — expected PASS for every fixture. If a fixture fails, the reference (not today's projection) is wrong: fix the reference.

- [ ] **Step 5: Commit** — `test(transcriptindex): fixture corpus and the reference projection`

### Task 4: The index: records, build, window reads, append, validation

**Files:**
- Create: `internal/transcriptindex/records.go`, `build.go`, `index.go`, `window.go`
- Test: `internal/transcriptindex/window_test.go`, `internal/transcriptindex/append_test.go`

**Interfaces:**
- Consumes: Task 1-3 exports.
- Produces:

```go
// Open returns the index for the transcript at path, kept in dir. A missing,
// stale or corrupt index is rebuilt from the transcript. Open catches up to
// the last complete line.
func Open(path, dir string) (*Index, error)
// CatchUp indexes complete lines appended since the last catch-up. A
// transcript that is no longer the indexed file grown by appends is rebuilt.
func (x *Index) CatchUp() error
// Latest returns the newest limit items (appwire.NormalizeTranscriptItemLimit).
func (x *Index) Latest(limit int) (Window, error)
// Before returns up to limit items immediately before the exclusive position.
// A position that names no item is appwire.TranscriptItemCursorStale().
func (x *Index) Before(before appwire.ThreadItemPosition, limit int) (Window, error)
func (x *Index) Close() error

type Window struct {
	Candidates []appitempaging.TranscriptItemCandidate // chronological; Turn carries no Items
	HasOlder   bool
	Length     int64 // transcript bytes the window was read from
}
```

Sidecar directory layout (all little-endian):
- `meta.json`: `{format, projection, file_identity, length, entries, tail_sha256, header_offset, header_length, items, turns, open, open_turn_id, turn_slot}`; written to `meta.json.tmp` then renamed, after the tables.
- `items`: 112-byte item records sorted by position — `entry u64, part u32, turn u32, version u64, call strRef, opener contributor, completer contributor, middle strRef`; `strRef = off u64, len u32`; `contributor = offset i64, ordinal u64, length u32, name strRef` (32 bytes).
- `turns`: 96-byte turn summaries — `id strRef, firstOffset i64, firstOrdinal u64, failureOffset i64, failureLength u32, flags u32 (interrupted, started), startedAt i64, usage [4]i64 (input, output, cacheRead, total), version u64`, zero padded.
- `strings`: append-only bytes (turn ids, call ids, resolved tool names, encoded middle-contributor lists).

Rules the implementation must follow (each has a test below):
- Build scans with `transcript.ReadLine` (max 128 MiB), skips whitespace-only lines, decodes the header then every entry strictly; the ordinal counts entries only. An unterminated tail stops the scan; `length` is the end of the last complete line.
- `apply(ordinal, offset, length, turn)`: `TurnGrouper.Place` (a new group appends a turn record with the group's first entry); project with `ProjectTurnParts` using a seed holding, for each result part with an empty `Name`, the resolved name of its call; call items (`commandExecution` with a `CallID`) already in the open group's call map add a contributor to that item unless the entry is already its latest contributor; other items append a record `{entry: ordinal+1, part, turn slot, version: ordinal+1, opener}`. A tool-results contributor stores the name its seed gave the item's call.
- Name resolution: during a full build a whole-file map mirrors `ProjectTurn`'s effects (a tool call sets its id's name; a result whose resolved name is `communicate` deletes it). The open group keeps the same effects locally (with deletions). When extending, a nameless result whose call the open group does not know returns `errRebuild`, and `CatchUp` rebuilds.
- Turn summary per entry, applied only when `ordinal+1 > version`: last `TURN_FAILURE` entry's offset/length; interrupted flag for `STEERING` with `SteeringKind == events.SteeringKindInterrupted`; first non-zero timestamp; usage sums; `version = ordinal+1`. Items: a contributor is added only when `ordinal+1 > version`.
- Window read: prelude (from the header cached at open) is ranks `[0, P)`; item records follow. Read the needed item records (plus one neighbour each side for `HasEarlierItems`/`HasLaterItems`) in one `ReadAt`, each distinct turn record once, and each distinct contributor entry once (`ReadAt` + `transcript.DecodeEntry`). Item: project the opener with seed `{call: opener name}` and take the item whose part matches; for a call item fold (`MergeThreadItems`) later items of the same call in the opener, then each middle contributor and the completer projected with their own seeds; stamp `TurnID`, `Position`, `TranscriptKey`. Turn: `{ID, ItemsView: full, Status: completed}`, `StampTurnFailure` with the failure entry, interrupted unless failed, `StartedAt`, `EvenerUsageFromLLM(sums)`.
- Validation (`Open` and `CatchUp`): same `FileIdentity`, size ≥ `length`, SHA-256 of the last `min(4096, length)` bytes before `length` unchanged, projection and format match, tables at least as long as the meta counts (longer tables are truncated to the counts: an unfinished append). Anything else rebuilds.

- [ ] **Step 1: Write the failing window test**

```go
func TestWindowsEqualTheReferenceOnEveryBoundary(t *testing.T) {
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			path := writeFixture(t, fx)
			want := referenceCandidates(t, path)
			x, err := Open(path, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			for limit := 1; limit <= len(want)+1; limit++ {
				window, err := x.Latest(limit)
				if err != nil {
					t.Fatal(err)
				}
				assertWindow(t, "latest", window, want, len(want), limit)
				for end := 1; end < len(want); end++ {
					window, err := x.Before(want[end].Position, limit)
					if err != nil {
						t.Fatalf("before %v: %v", want[end].Position, err)
					}
					assertWindow(t, fmt.Sprintf("before %d", end), window, want, end, limit)
				}
			}
			if _, err := x.Before(appwire.ThreadItemPosition{Entry: 1 << 40}, 5); !isCursorStale(err) {
				t.Fatalf("unknown position: err = %v, want a stale cursor", err)
			}
		})
	}
}
```

`assertWindow` checks `window.Candidates` DeepEquals `want[max(0,end-limit):end]` and `HasOlder == end-limit > 0`.

- [ ] **Step 2: Run** — FAIL (`undefined: Open`).
- [ ] **Step 3: Implement `records.go`, `build.go`, `index.go` (build path only: Open always rebuilds), `window.go`** following the rules above.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Write the failing append/reopen/validation tests** (`append_test.go`):
  - `TestAppendEntryByEntryMatchesTheReference`: write the "everything" fixture's header only; open; then for each entry append its line, `CatchUp`, and require `Latest(40)` and every `Before` boundary to equal the reference of the file so far. Every 7th step closes and re-`Open`s the index instead (reopen must restore the grouper, open turn slot and call map from records and meta, with no rebuild: assert via an unexported `rebuilds` counter).
  - `TestUnterminatedTailIsPickedUpLater`: append half a line; `CatchUp`; windows equal the reference without it; finish the line; `CatchUp`; equal with it.
  - `TestReplacedTruncatedOrRewrittenTranscriptRebuilds`: (a) rename a different fixture over the path, (b) truncate below the indexed length, (c) truncate and regrow to a longer different file, (d) rewrite a byte inside the last 4096 bytes; each `CatchUp` rebuilds (counter) and equals the new file's reference.
  - `TestReplayAfterCrashIsIdempotent`: build over a prefix; snapshot `meta.json`; append 5 entries and `CatchUp`; restore the old `meta.json` (the tables now hold records and in-place updates past the meta, like a crash before the meta write); `Open`; equal to the reference with no rebuild and usage not double counted.
  - `TestNamelessResultForAnEarlierTurnRebuildsWhenExtending`: reopen over a prefix, append a nameless result whose call is in an earlier group; `CatchUp` rebuilds once and equals the reference.
  - `TestCorruptSidecarRebuilds`: garbage `meta.json`, short `items` file, unknown format; `Open` rebuilds.
- [ ] **Step 6: Run** — FAIL (no catch-up/validation yet).
- [ ] **Step 7: Implement** meta persistence, validation, reopen state recovery, `CatchUp`, `errRebuild`, table truncation on open.
- [ ] **Step 8: Run** — `go test -race ./internal/transcriptindex -count=1`; PASS, each test under 3 s.
- [ ] **Step 9: Commit** — `feat(transcriptindex): seekable item and turn-summary index over a transcript`

### Task 4b: Align the index with spec revision 7

Revision 7 (merged from `origin/wip/transcript-read-model`) changed the Index section: a read captures a length and extends to it; one extender at a time under a file lock shared by the hub and a daemon, readers under a shared lock; the covered length is written last and records past it are never trusted; a rebuild writes a new sidecar and renames it into place, and a reader holding the old one reopens on the incarnation change; the turn summary holds the latest lifecycle entry and the status it sets; the index can find a round's calls.

**Files:**
- Modify: `internal/transcriptindex/index.go`, `window.go`, `build.go`, `records.go`
- Create: `internal/transcriptindex/lock_unix.go`, `internal/transcriptindex/lock_windows.go`
- Test: `internal/transcriptindex/sharing_test.go`

**Interfaces:**
- Produces:
  - `func (x *Index) CatchUpTo(length int64) error` — index complete lines that end at or before `length` (the recorded length in-process); `CatchUp` is `CatchUpTo(file size)`.
  - `Window.Incarnation string` beside `Window.Length`: the snapshot identity. A rebuild mints a new incarnation; appends keep it.
- Layout: `<dir>/lock` (flock/LockFileEx; exclusive to extend or rebuild, shared to read records), `<dir>/CURRENT` (the live incarnation's name, replaced by rename), `<dir>/<incarnation>/{meta.json,items,turns,strings}`. A rebuild builds a fresh incarnation directory, renames `CURRENT` over, and removes the old directory.
- Every operation re-reads `CURRENT` and the incarnation's `meta.json` under the lock: a different incarnation reopens; a longer covered length (another extender) adopts the new counts and restores the builder from records before extending.
- Turn summary: `Lifecycle{Offset, Length}` and `Status` (completed, failed, interrupted) replace the failure offset and interrupted flag. Legacy rule: failed once any TURN_FAILURE is in the turn (its latest supplies the error), interrupted when an interrupted STEERING is and no failure is.
- Calls: the extender finds a result's item through the open turn's call map, rebuilt from the item records' call ids after any reload. Revision 7's "latest ASSISTANT awaiting results" field serves the new-format projector; today's grouping merges by call id across the whole turn, so the index keeps the turn-wide map (Ruling in the ledger).

- [ ] **Step 1: Failing tests** (`sharing_test.go`): `CatchUpTo` stops at the last complete line within the length; `Window.Incarnation` is stable across appends and changes after a rebuild; two handles on one sidecar (the hub and a daemon): A extends, B reads A's covered length without catching up, B then extends after A and equals the reference; A rebuilds while B holds the old incarnation, B's next read reopens and equals the reference; two handles extending concurrently from goroutines while a writer appends equal the reference (`-race`).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** the lock, the incarnation layout, refresh-under-lock, `CatchUpTo`, the lifecycle status.
- [ ] **Step 4: Run** the package with `-race`; PASS. Re-run the real-data equality and latency script.
- [ ] **Step 5: Commit** — `feat(transcriptindex): align the index with spec revision 7`

### Task 5: Real-data equality and the latency gate

**Files:**
- Create: `internal/transcriptindex/realdata_test.go`
- Create: `server/appwire_turns_latency_test.go`
- Create: `scripts/measure-transcript-index-latency.sh`

**Interfaces:**
- Consumes: `Open`, `Latest`, `Before`, `CatchUp`; `referenceCandidates`; server `appTurnsFromTranscriptFile`, `appTurnSnapshot.Seed`, `LatestItemCandidates`.
- Produces: log lines `latency <label> file=<name> samples=200 p50=<d> p99=<d>` parsed by the script.

- [ ] **Step 1: Write the opt-in tests** (skip unless `EVENER_TRANSCRIPT_INDEX_REAL` is set; each path is copied into `t.TempDir()` first so the source is never written):
  - `TestRealTranscriptWindowsEqualTheReference`: build; require `Latest(40)`, then the chain of 40-item `Before` pages walked back 50 pages, plus 200 seeded-random boundaries, to equal the reference candidates.
  - `TestRealTranscriptLatency`: fresh `Open` (build time logged); `idle`: 200 samples of `CatchUp()+Latest(40)`; `appending`: a goroutine appends one real entry line (taken from the file's own tail, cycled) every 100 ms with `os.O_APPEND` and the reader takes 200 samples of `CatchUp()+Latest(40)` spaced 10 ms apart, so ~20 appends land inside the run. Log p50/p99 for both; fail when idle p99 ≥ 50 ms or appending p99 ≥ 100 ms.
  - `server`: `TestRealTranscriptInMemoryReadLatency` (same env var): project with `appTurnsFromTranscriptFile`, seed an `appTurnSnapshot`, then 200 samples of `LatestItemCandidates(40)` with the item projection cached (steady idle) and 200 with it invalidated before each sample (a notification arrived, as while appending). Log p50/p99.
- [ ] **Step 2: Run each with a nonexistent path** to see the skip and a failing path error; then run on the real copies with the script.
- [ ] **Step 3: Script** `scripts/measure-transcript-index-latency.sh [files...]`: runs both tests with `-run` and `-v`, keeps full logs in a temp dir it prints, and prints only the `latency` lines plus a PASS/FAIL gate line computed from the index idle p99 against `2 ×` the baseline idle p99.
- [ ] **Step 4: Commit** — `test(transcriptindex): opt-in real-transcript equality and latency gate`

### Task 6: Parity divergence classifier

The harness compares two turn lists, the live snapshot and the file projection, and reduces their differences to a set of stable divergence keys. The classifier is plain code with its own unit tests, so the scenario test in Task 7 only drives the session and checks the table.

**Files:**
- Create: `server/transcript_parity_diff_test.go`

**Interfaces:**
- Produces:

```go
// parityDivergence is one kind of difference between the live view and the
// file projection. Keys hold no ids, text or counts, so a run's set is stable.
type parityDivergence struct {
	Class   string // "item-missing-live", "item-missing-file", "item-field", "turn-id", "turn-field", "turn-split"
	Subject string // item type (plus event kind for systemMessage), or turn field
	Field   string // for item-field / turn-field; "" otherwise
}

// diffParity aligns live and file items and returns the divergences found.
func diffParity(live, file []appwire.Turn) map[parityDivergence]struct{}

// knownDivergence is one table row: a divergence today's code produces and the
// spec phase that removes it.
type knownDivergence struct {
	parityDivergence
	Phase int
	Why   string
}

// checkParity fails on an observed divergence missing from table, and on a
// table row that no longer occurs.
func checkParity(t *testing.T, observed map[parityDivergence]struct{}, table []knownDivergence)
```

Alignment: flatten each side to `(turnIndex, item)` in order; align items with an LCS whose equality is the item's content signature `type | eventKind | callID | toolName | trimmed text` (text omitted for `commandExecution`). Unaligned items give `item-missing-live`/`item-missing-file` keyed by signature subject. For aligned pairs compare: `ID`, `TranscriptKey`, `Position`, `TurnID` (via the turn mapping below), `Status`, `Text`, `Output`, `Error`, `ArgumentsJSON`, `ToolName`, `Description`, `Images` (count and media type), `OutputImages` (count, SHA, media type), `ExitCode`, `Source`, `SteeringKind`, `ClientMutationID`, `StartedAt`, `CompletedAt`, `EventKind`, `Raw`. Turns: every aligned pair maps a live turn to a file turn; a live turn mapped to two file turns (or the reverse) is `turn-split`; for each mapped pair compare `ID` (`turn-id`), `Status`, `Usage`, `Error`, `StartedAt` (`turn-field`).

- [ ] **Step 1: Write failing unit tests** for the classifier on hand-built turn lists: identical lists → empty set; a live-only notice → one `item-missing-file`; the same item with different `ID` and `Position` → two `item-field` keys; one live turn whose items land in two file turns → `turn-split`; status differs → `turn-field/Status`; `checkParity` reports an unknown key and a stale row (use a recording `testing.TB` fake: a struct embedding `testing.TB` that records `Errorf`).
- [ ] **Step 2: Run** — `go test ./server -run TestParityDiff -count=1`; FAIL (undefined).
- [ ] **Step 3: Implement** the classifier.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `test(server): a live-versus-file parity classifier`

### Task 7: The parity scenario

**Files:**
- Create: `server/transcript_parity_test.go`

**Interfaces:**
- Consumes: `diffParity`, `checkParity`, `knownDivergence`; `PrepareAppIdentity`, `PrepareAppIdentityFromEntriesForPath`, `NewServer`, `ReplaceAppIdentity`, `BridgeEvent`, `appTurnsFromTranscriptFile`, `srv.appTurns.Snapshot()`; agent APIs listed below.

Harness (from the API research; one test, one session, run under 3 s):
- `llm.NewClient()` + `Register(adapter)` with adapter name `openai`; `provider.NewOpenAIProfile("gpt-5.2")`; `execenv.NewLocalExecutionEnvironment(dir)`; `SessionConfig{StateDir: dir, VisionModel: "off", LLMRetryPolicy: &llm.RetryPolicy{MaxRetries: 2}, LLMSleep: noSleep, PluginDirs: []string{hookPluginDir}}` where the plugin registers a `Stop` hook `exit 0`; `SetClientMutationStartWakeFunc(func(){})`.
- Bridge exactly as serve: `PrepareAppIdentity`, `ReplaceAppIdentity`, then `sess.ConsumeEventsLossless(func(ev){ BridgeEvent(srv, ev, nil); seen <- ev }, onDrained)` called synchronously. Every step waits for its barrier event on `seen` (`EventSessionEnd`, `EventModelChanged`, `EventContextCompaction` with layer `summarize`, `EventGoalEnded`); no sleeps.
- The adapter is a mutex-guarded script keyed by request shape: `ResponseFormat != nil` → session namer (`{"name":"Parity"}`); no tools and a compaction prompt → summary text; otherwise the next main step. A step may run a side effect before returning (steering). Tool call ids are unique across the run.

Scenario, in order:
1. Client-mutation turn (`AcceptClientMutationStart` + `ProcessClientMutationStart`): response with reasoning + text + `read_file` of a real 1×1 PNG in the working dir; then `communicate end_turn` (Stop hook runs → HOOK_COMPLETED).
2. `ProcessInput`: main step 1 steers (`AcceptClientMutationSteer`) and returns `communicate end_turn=false`; step 2 `communicate end_turn`.
3. `SetModel("gpt-5.4")` while idle.
4. Two more plain turns (enough history), then `Compact` (summarizer answers).
5. Retry: step returns a 503 then succeeds with `communicate end_turn`.
6. Failure: step returns 401 → TURN_FAILURE.
7. Goal: `SetGoal(ctx, "finish the parity check")`, `ProcessInput`; the continuation turn calls `update_goal {"status":"complete"}` then `communicate end_turn`.
8. Delegate attention: a `delegate` call whose child blocks until the parent's input returns; then `ProcessInputKind(ctx, "", nil, agent.EntryNotification)` answers the notification.
9. Restart: close, wait for drain, `RestoreSessionFromMetaWithConfig` with `OnRestoredTranscript`, `PrepareAppIdentityFromEntriesForPath`, a new `Server`, bridge, one more turn.

Checks: after step 8 compare `srv.appTurns.Snapshot()` with `appTurnsFromTranscriptFile`; after step 9 compare the restarted server's snapshot with the file projection too. Each comparison runs `checkParity` against its own table (`beforeRestart`, `afterRestart`). The test also asserts that every scenario feature reached the transcript (one entry of each expected kind, an image-bearing tool result, a STEERING with `GoalContinuation`, a delegate attention STEERING), so a scenario that silently stopped covering something fails.

The tables start empty; the first run lists the observed keys. Each key gets a row with the phase that removes it and a one-line reason, from the spec: identity, positions and keys (phase 3: persisted identity and entry-ordinal positions); live-only notices (phase 3: overlay); file-only entries such as TURN_FAILURE/NOTES_CONTEXT/delivery steering (phase 3: live history from recorded entries); status inference (phase 3: status from persisted facts). A key that no phase removes is a bug, not a table row: report it.

- [ ] **Step 1: Write the scenario with empty tables.**
- [ ] **Step 2: Run** — `go test ./server -run TestTranscriptParity -count=1 -v`; FAIL listing observed divergences (and passing coverage assertions).
- [ ] **Step 3: Fill the tables** from the observed keys with phase and reason; for each key, confirm the cause in code before tagging it.
- [ ] **Step 4: Run** — PASS; run 20 times (`-count=20`) to prove the key set is stable; run with `-race`.
- [ ] **Step 5: Prove it can fail:** temporarily delete one table row → FAIL naming it as new; add a fake row → FAIL naming it as stale. Revert.
- [ ] **Step 6: Commit** — `test(server): live-versus-file parity harness with a phase-tagged divergence table`

### Task 8: Gates, measurement, PR

- [ ] **Step 1:** `go test -race ./internal/transcriptindex ./internal/apptranscript -count=1`, `go test ./server -count=1`, `make vet`, `make lint`. Output pristine.
- [ ] **Step 2:** `scripts/measure-transcript-index-latency.sh /tmp/claude-1000/trm-data/*.jsonl`; record the table (per transcript: build time, index idle p50/p99, index appending p50/p99, baseline idle p50/p99, baseline invalidated p50/p99).
- [ ] **Step 3:** If any criterion fails, stop: push, open the PR as a draft with the numbers, the bottleneck, and a proposed index redesign. Otherwise open the PR normally: `gh pr create --base wip/transcript-read-model --head wip/trm-phase1`.
- [ ] **Step 4:** Roborev loop per the brief (up to 3 rounds), CI green, do not merge.
