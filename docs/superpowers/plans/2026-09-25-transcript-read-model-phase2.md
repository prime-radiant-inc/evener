# Transcript Read Model Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every transcript entry a session or cold writer records carries the persisted identity phase 3 projects from (format marker, `TurnID`, `TurnKind`, `roundID`, `OriginalOrdinal`, model), the writer returns an explicit recorded-or-not result with the entry's ordinal, the new transcript-only entry kinds are written and skipped by every consumer, and a served session fails closed on writer failure. Nothing clients see changes except that fail-closed rule.

**Architecture:** The per-file append tail (`agent/transcript/append_tail.go`, the registry that already owns the append lock and next `Seq`) also owns the next entry ordinal, the recorded length, a recorded-entry hook, and the turn placement state: the running execution's turn ID, the open gap turn ID, and whether the file is still in its prelude. A single `Writer.Record(turn, RecordOptions)` door stamps identity under that lock and returns a `Record`. The session names the running execution on the tail when it admits one and ends it with a completion entry; everything else it writes is placed by the tail's rule. New entry kinds are written to the transcript only; `schema.TurnKind.TranscriptOnly()` is the one predicate every consumer uses to skip them.

**Tech Stack:** Go (module `primeradiant.com/evener`), standard library; the existing `identifier` module for time-ordered IDs.

**Spec:** `docs/superpowers/specs/2026-09-25-transcript-read-model-design.md` (revision 7 + amendments). Sections: "Recorded length, entry ordinals and Seq", "Persisted identity", "Turn status", "The projector" (communicate, fold copies), "New entry kinds stay out of model history", "Writer failure", "Schema compatibility", Migration phase 2. Carried-over review items: the phase 2 row "Single fail-closed rule" and the registry hook for "Projection queue fed under append lock" (see Task 5 and Task 27).

## Global Constraints

- "Every projection rule and client-visible behavior stays as it is today." The only intended behavior change: "a served session fails closed on writer failure."
- Default tests are deterministic: scripted provider at the LLM boundary, no network, no credentials, no ambient state (AGENTS.md, `docs/developing-evener/testing.md`). No sleeps; await structured completions.
- Per-test ceiling in the default suite is 3 s (`testing-budget.json`); `./agent` runs need `-timeout 30m`.
- Entry ordinal: "the 0-based index of an entry line in the file, excluding the header." Only a recorded line gets an ordinal. "A rolled-back append consumes its `Seq` but not an ordinal."
- `TurnID` spellings: client-mutation turns `turn_m<N>` (`appwire.ClientMutationTurnID`); every other turn `t_<id>` from `identifier.NewTurnID()`; the prelude turn `appwire.SystemPreludeTurnID` (`turn_system`, the ID today's header prelude already uses).
- `TurnKind` values: `execution`, `gap`, `delivery`, `prelude`, persisted on the first entry of each turn only.
- `StableTurnID` keeps its current meaning; `TurnID` is a new field.
- New entry kinds never enter in-memory history; strict decode stays (`DisallowUnknownFields`); old transcripts decode unchanged.
- The append lock is a leaf: nothing a `Record` does under `tail.mu` takes another lock, and the recorded hook must not either.
- `schema.Turn` and `transcript.Entry` fields stay in alphabetical JSON-key order (the public line projection relies on it; `TestReadSessionTranscriptExpansionLosslesslyReturnsEverySemanticTurn` pins it).
- Commit messages end with `Claude-Session: https://claude.ai/code/session_01ALoA3J8ymsuJuXoApTZqLJ`. Never skip hooks.

## Work units and PRs

| Unit | Branch | PR base | Tasks | Deliverable |
|---|---|---|---|---|
| 2a | `wip/trm-phase2a` | `wip/trm-phase1` | 1-6 | schema for every new field and kind; `Record` with ordinal, recorded length, `Seq` on rollback; turn placement in the tail; recorded hook |
| 2b | `wip/trm-phase2b` | `wip/trm-phase2a` | 7-18 | every consumer skips the transcript-only kinds (test fixture interleaves them) |
| 2c | `wip/trm-phase2c` | `wip/trm-phase2b` | 19-26 | session and cold writers write identity, executions, completions, COMMUNICATE, notices, fold `OriginalOrdinal`, fork sequence seeding |
| 2d | `wip/trm-phase2d` | `wip/trm-phase2c` | 27-28 | fail closed; final gates |

Consumers skip the new kinds (2b) before anything writes them (2c), so each unit is green on its own.

## Review Focus

1. **An async attention write racing a turn's completion** — it must take a delivery turn, never the completed turn's ID. Pinned in Task 4 (`TestAsyncRecordAfterCompletionTakesADeliveryTurn`) and Task 21.
2. **A rolled-back durable append followed by a successful one** — the next record takes the rolled-back entry's ordinal and a fresh `Seq`; the recorded hook never saw the rolled-back one. Pinned in Task 3.
3. **A resumed session whose last execution crashed open, and a reclaimed turn** — resume writes an interrupted completion for the first and a reopen marker for the second, never both for one turn. Pinned in Task 23.
4. **A legacy (pre-phase-2) transcript resumed by this build** — legacy turns are never rewritten, new entries get identity, and today's file projection of the legacy prefix is unchanged. Pinned in Task 26 (parity harness after restart) and Task 13.
5. **Two writers on one file (session writer plus a cold writer) with a running execution** — the cold write is a delivery turn and the running execution's span continues after it. Pinned in Task 4.

## Spec issues found

Recorded here as found; each takes the most conservative reading.

1. **`t_<ulid>`.** The repository has no ULID dependency; `identifier` mints time-ordered UUIDv7 payloads in base62 for every other domain ID. `TurnID`s use `identifier.NewTurnID()` (`t_` + that payload). Same properties the spec wants (unique, disjoint from `turn_<n>`, time-ordered); different alphabet.
2. **Publishing the running turn through `SetProcessingTurn`** replaces the server's own `turn_<n>` reservation, which is the live turn ID clients see. That is a visible change, so it moves to phase 3 with the rest of server-side identity. Phase 2 stores the running turn ID in the registry only.
3. **"A transcript containing the new kinds projects exactly as one without them."** The spec's entry ordinal counts every entry line, new kinds included, and today's file projection derives fallback turn IDs (`turn_<entryIndex>`) and item IDs from that same index. A new-kind line therefore shifts those derived IDs of later daemon turns. Phase 2 keeps the projection rule unchanged (entry index = line ordinal + 1) and skips the new kinds; the consumer tests compare everything except those index-derived identifiers, and the phase 1 index and whole-file projection keep agreeing because both count lines. No parity row changes, because live and file IDs already differ in every one of those fields.
4. **Cold writers and a running execution in the same process.** The spec makes every cold write its own delivery turn. It is followed literally, so a cold write during a running in-process execution is a separate delivery turn inside the execution's span; turn membership is by `TurnID`, so phase 3 is unaffected.
5. **Which session writes are "asynchronous".** The spec names model-bound attention STEERING and ATTENTION_RESOLUTION. Delegate steering written into a target session from another goroutine (`delegate_tree_steer.go` `appendDelegateSteeringDurablyWithMetadata`) has the same shape and is treated the same way.
6. **Fail closed "for a served session".** A session cannot know at construction whether a daemon will serve it (`servedByDaemon` flips when the authoritative consumer attaches). Phase 2 decides at the moment of failure or admission: a transcript create failure is remembered and refuses input once the session is served; a poisoned writer or an unrecorded COMMUNICATE fails closed if the session is served then. Unserved sessions keep today's warn-and-continue.
7. **`overlay/end` and "projection state is dropped"** in the fail-closed list are phase 3 machinery that does not exist yet; phase 2 implements interrupt, refusal of further input, and one diagnostic.
8. **Tool `DurationMS` in TOOL_RESULTS** already ships (`llm.ToolResultData.DurationMS`, set at `agent/session_tools.go:1006` and kept by `projectToolResultsForTranscript`). Phase 2 pins it with a test instead of adding a field.
9. **Salvage entries** are ordinary ASSISTANT entries today (`persistSalvagedTurn` → `appendAssistantTurn`); they get `RoundID` through the same door.

---

## Unit 2a: schema, write result, registry

### Task 1: Schema for every new field and entry kind

**Files:**
- Modify: `agent/schema/turn.go`
- Modify: `identifier/domains.go`
- Test: `agent/schema/turn_identity_test.go`, `identifier/domains_test.go`

**Interfaces:**
- Produces:
  - `const TurnFormatIdentity = 1`
  - kinds `TurnCompletion TurnKind = "TURN_COMPLETION"`, `TurnReopen TurnKind = "TURN_REOPEN"`, `TurnCommunicate TurnKind = "COMMUNICATE"`, `TurnNotice TurnKind = "NOTICE"`
  - `func (k TurnKind) TranscriptOnly() bool` — true for exactly those four
  - `type TurnSpanKind string` with `TurnSpanExecution = "execution"`, `TurnSpanGap = "gap"`, `TurnSpanDelivery = "delivery"`, `TurnSpanPrelude = "prelude"`
  - `type TurnCompletionInfo struct { Status TurnCompletionStatus; CompletedAt time.Time; DurationMS int64 }`, statuses `TurnCompleted = "completed"`, `TurnFailed = "failed"`, `TurnInterrupted = "interrupted"`
  - `type CommunicateInfo struct { CallID string; EndTurn bool; Message string }`
  - `type NoticeInfo struct { Kind NoticeKind; ToolRepair *ToolRepairNotice; GoalEnded *GoalEndedNotice; TurnLimit *TurnLimitNotice; SkillActivated *SkillActivatedNotice }` with `NoticeToolRepair = "tool_repair"`, `NoticeGoalEnded = "goal_ended"`, `NoticeTurnLimit = "turn_limit"`, `NoticeSkillActivated = "skill_activated"`; the payloads mirror `events.ToolCallRepairedData`, `GoalEndedData`, `TurnLimitData`, `SkillActivatedData` field for field
  - `schema.Turn` gains, in key order: `Communicate *CommunicateInfo "communicate"`, `Completion *TurnCompletionInfo "completion"`, `Format int "format"`, `Model string "model"`, `Notice *NoticeInfo "notice"`, `OriginalOrdinal *uint64 "original_ordinal"`, `RoundID string "round_id"`, `TurnID string "turn_id"`, `TurnKind TurnSpanKind "turn_kind"` (all `omitempty`)
  - `identifier.NewTurnID()`, `MustNewTurnID()`, `ValidateTurnID()` (`t_`), and the same for `RoundID` (`r_`)

- [ ] **Step 1: Write the failing tests**

```go
package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestNewTurnFieldsRoundTripAndStayInKeyOrder(t *testing.T) {
	ordinal := uint64(7)
	turn := Turn{
		Communicate:     &CommunicateInfo{CallID: "c1", EndTurn: true, Message: "hi"},
		Completion:      &TurnCompletionInfo{Status: TurnInterrupted, CompletedAt: time.Unix(5, 0).UTC(), DurationMS: 12},
		Format:          TurnFormatIdentity,
		Kind:            TurnCompletion,
		Message:         llm.Message{Role: llm.RoleUser},
		Model:           "gpt-5.4",
		Notice:          &NoticeInfo{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete", Iterations: 2}},
		OriginalOrdinal: &ordinal,
		RoundID:         "r_1",
		Timestamp:       time.Unix(4, 0).UTC(),
		TurnID:          "t_1",
		TurnKind:        TurnSpanExecution,
	}
	data, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var back Turn
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, turn) {
		t.Fatalf("round trip = %+v, want %+v", back, turn)
	}
	// The public line projection re-marshals through sorted maps and relies on
	// the struct's field order matching the sort.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	var sorted []byte
	if sorted, err = json.Marshal(keys); err != nil {
		t.Fatal(err)
	}
	if string(sorted) != string(data) {
		t.Fatalf("field order differs from key order:\n got %s\nwant %s", data, sorted)
	}
}

func TestLegacyTurnCarriesNoIdentity(t *testing.T) {
	data, err := json.Marshal(NewTurn(TurnUserInput, llm.User("hi")))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"format", "turn_id", "turn_kind", "round_id", "original_ordinal", "model", "completion", "communicate", "notice"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("a turn without identity marshals %q: %s", key, data)
		}
	}
}

func TestTranscriptOnlyKinds(t *testing.T) {
	for _, kind := range []TurnKind{TurnCompletion, TurnReopen, TurnCommunicate, TurnNotice} {
		if !kind.TranscriptOnly() {
			t.Errorf("%s is not transcript-only", kind)
		}
	}
	for _, kind := range []TurnKind{TurnUserInput, TurnSteering, TurnAssistant, TurnTool, TurnToolResults, TurnSystem, TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnFailure, TurnHookCompleted, TurnEnvironment, TurnNotesContext, TurnAttentionResolution} {
		if kind.TranscriptOnly() {
			t.Errorf("%s is transcript-only", kind)
		}
	}
}
```

In `identifier/domains_test.go` add `"turn": MustNewTurnID, "round": MustNewRoundID` to the existing prefix table (it asserts each mints a valid ID of its prefix) and the matching validators to `domains_fuzz_test.go`'s table.

- [ ] **Step 2: Run to verify failure** — `go test ./agent/schema -run 'TestNewTurnFields|TestLegacyTurnCarries|TestTranscriptOnly' -count=1` → FAIL `undefined: CommunicateInfo`; `cd identifier && go test ./... -run Domain -count=1` → FAIL `undefined: MustNewTurnID`.

- [ ] **Step 3: Implement** — add the kinds with doc comments in the style of `TurnModelSwitch` ("Transcript only: written to the transcript, never to in-memory history, skipped by every consumer; phase 3 projects it"), the payload types, the predicate:

```go
// TranscriptOnly reports whether entries of this kind are written to the
// transcript only. They never enter a session's in-memory history, resume
// skips them, and every other consumer of transcript entries skips them too:
// they exist for the history projection alone.
func (k TurnKind) TranscriptOnly() bool {
	switch k {
	case TurnCompletion, TurnReopen, TurnCommunicate, TurnNotice:
		return true
	default:
		return false
	}
}
```

and the nine fields in key order. `identifier/domains.go`: `func NewTurnID() (string, error) { return newDomainID("t_") }`, `func NewRoundID() (string, error) { return newDomainID("r_") }` plus the `Validate*`/`MustNew*` pairs.

- [ ] **Step 4: Run** the same commands plus `go test ./agent/schema ./agent/transcript -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(schema): persisted turn identity fields and transcript-only entry kinds`

### Task 2: Recorded result, entry ordinal and recorded length

**Files:**
- Modify: `agent/transcript/append_tail.go`, `agent/transcript/transcript.go`
- Test: `agent/transcript/record_test.go`

**Interfaces:**
- Produces:

```go
// Door is how an append reaches durability: Append, AppendDurable or AppendSynced.
type Door int
const (
	DoorBuffered Door = iota
	DoorDurable
	DoorSynced
)

// RecordOptions chooses an append's door and its turn placement.
type RecordOptions struct {
	Door  Door
	Place Placement
}

// Record is the outcome of one append.
type Record struct {
	Recorded bool
	Ordinal  uint64      // entry ordinal: 0-based index among entry lines
	Seq      int
	Offset   int64       // byte offset of the line in the file
	Length   int64       // line length including its newline
	Turn     schema.Turn // the turn as written, identity stamped
}

func (w *Writer) Record(turn schema.Turn, opts RecordOptions) (Record, error)
func (w *Writer) RecordedLength() int64
```

  `Append`, `AppendDurable`, `AppendSynced` become `Record` with `PlaceSession` (Task 4 defines placements; until then the zero `Placement` stamps nothing) and keep their error contracts. A nil writer, and a closed writer through the buffered and durable doors, return `(Record{}, nil)`: not recorded, no error — today's no-op made explicit.
- appendTail gains `nextOrdinal uint64` and `recordedLength int64`. `newWriterFS` sets them from the header line; `resumeWriter` sets them from the scan (`len(entries)`, valid length after crash-tail truncation) under the tail lock, never lowering a shared tail's values.

- [ ] **Step 1: Write the failing test**

```go
package transcript

func TestRecordReportsOrdinalSeqAndRecordedLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	w, err := NewWriterNoSync(path, sharedFileHeader)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck // fixture
	headerLen := w.RecordedLength()
	first, err := w.Record(steeringTurn("one"), RecordOptions{Door: DoorDurable})
	if err != nil || !first.Recorded || first.Ordinal != 0 || first.Seq != 0 || first.Offset != headerLen {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := w.Record(steeringTurn("two"), RecordOptions{Door: DoorBuffered})
	if err != nil || second.Ordinal != 1 || second.Offset != first.Offset+first.Length {
		t.Fatalf("second = %+v, %v", second, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if w.RecordedLength() != info.Size() {
		t.Fatalf("recorded length %d, file %d", w.RecordedLength(), info.Size())
	}
	// A resumed writer on the same file continues the ordinal.
	resumed := openSharedFileWriterPath(t, path)
	defer resumed.Close() //nolint:errcheck // fixture
	third, err := resumed.Record(steeringTurn("three"), RecordOptions{Door: DoorDurable})
	if err != nil || third.Ordinal != 2 || third.Seq != 2 {
		t.Fatalf("third = %+v, %v", third, err)
	}
}

func TestRecordOnMissingOrClosedWriterIsNotRecorded(t *testing.T) {
	var missing *Writer
	if rec, err := missing.Record(steeringTurn("x"), RecordOptions{Door: DoorDurable}); err != nil || rec.Recorded {
		t.Fatalf("nil writer: %+v, %v", rec, err)
	}
	w, err := NewWriterNoSync(filepath.Join(t.TempDir(), "t.jsonl"), sharedFileHeader)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if rec, err := w.Record(steeringTurn("x"), RecordOptions{Door: DoorBuffered}); err != nil || rec.Recorded {
		t.Fatalf("closed writer, buffered: %+v, %v", rec, err)
	}
	if _, err := w.Record(steeringTurn("x"), RecordOptions{Door: DoorSynced}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("closed writer, synced: %v", err)
	}
}
```

(`openSharedFileWriterPath` is `openSharedFileWriter` taking the header's session ID; add it to `shared_file_test.go` if the existing helper does not fit.)

- [ ] **Step 2: Run** `go test ./agent/transcript -run TestRecord -count=1` → FAIL `w.Record undefined`.
- [ ] **Step 3: Implement.** `appendBatchLocked` takes a `recorded func(Record)` sink; after the whole buffer is a record (a clean write, or a retained record in `settleFailedWriteLocked`), it walks the batch assigning `tail.nextOrdinal++`, offsets from `tail.recordedLength`, and advances `recordedLength` by each line's length, before returning. `Record` is `appendBatch` of one with the door's `forceSync/queueRetained/failClosed` flags (buffered: false/true/false; durable: true/true/false; synced: the `AppendSynced` path, whose barrier and `RetainedUnsyncedError` logic moves into `Record`). The old doors call `Record` and drop the result.
- [ ] **Step 4: Run** `go test ./agent/transcript -count=1 -race` → PASS (every existing writer test too).
- [ ] **Step 5: Commit** — `feat(transcript): an explicit write result with the entry ordinal and recorded length`

### Task 3: A rolled-back append consumes its Seq but not an ordinal

**Files:** Modify `agent/transcript/transcript.go`; Test `agent/transcript/record_test.go`, update the `AppendBatch` doc and any test asserting the old "spends no sequence number".

- [ ] **Step 1: Write the failing test** — reuse the fault filesystem the durability tests use (`durability_test.go`'s failing-sync `afero.Fs` wrapper):

```go
func TestRolledBackAppendConsumesSeqButNotOrdinal(t *testing.T) {
	fs := newSyncFailingFs(t) // fails the next Sync once, truncate succeeds
	w, err := NewWriterWithFS(fs, "/t.jsonl", sharedFileHeader)
	if err != nil {
		t.Fatal(err)
	}
	var hooked []Record
	w.OnRecorded(func(r Record) { hooked = append(hooked, r) })
	fs.failNextSync()
	rolled, err := w.Record(steeringTurn("rolled back"), RecordOptions{Door: DoorDurable})
	if err == nil || rolled.Recorded {
		t.Fatalf("rolled back append = %+v, %v", rolled, err)
	}
	next, err := w.Record(steeringTurn("kept"), RecordOptions{Door: DoorDurable})
	if err != nil || next.Ordinal != 0 || next.Seq != 1 {
		t.Fatalf("next = %+v, %v; want ordinal 0 (reused) and seq 1 (not reused)", next, err)
	}
	if len(hooked) != 1 || hooked[0].Ordinal != 0 || hooked[0].Turn.Message.Text() != "kept" {
		t.Fatalf("hook saw %+v", hooked)
	}
}
```

- [ ] **Step 2: Run** → FAIL (seq 0, and `OnRecorded` undefined until Task 5; write Task 5's `OnRecorded` stub signature first if needed, or assert the hook part in Task 5).
- [ ] **Step 3: Implement** — `appendBatchLocked` spends `len(turns)` sequence numbers as soon as the batch is encoded (`tail.nextSeq += len(turns)`), so every outcome after that point, rollback included, has consumed them; `countAppendedEntryLocked` keeps only the failure count. Update the doc comments ("A rolled-back append still consumes its Seq: a reader in another process may have seen the line").
- [ ] **Step 4: Run** `go test ./agent/transcript -count=1 -race` → PASS.
- [ ] **Step 5: Commit** — `fix(transcript): a rolled-back append consumes its sequence number`

### Task 4: Turn placement in the append tail

**Files:** Modify `agent/transcript/append_tail.go`, `agent/transcript/transcript.go`; Create `agent/transcript/placement.go`; Test `agent/transcript/placement_test.go`

**Interfaces:**
- Produces:

```go
// Placement names the turn an appended entry joins. The tail resolves it
// under the append lock, against the running execution and open gap it holds.
type Placement struct{ mode placementMode; turnID string }

var (
	PlaceVerbatim   Placement // the turn's identity as given: copies of recorded entries
	PlaceSession    Placement // a session's own write: running execution, else prelude, else the open gap turn
	PlaceAsync      Placement // a session's asynchronous write: running execution, else a delivery turn
	PlaceDelivery   Placement // a cold writer's entry: a delivery turn of its own
	PlaceCompletion Placement // the running execution's completion; ends the execution
)
func PlaceInTurn(turnID string) Placement // an explicit turn, for resume's interrupted completions

func (w *Writer) BeginExecution(turnID string, reopen bool) // nil-safe
func (w *Writer) RunningTurnID() string                     // nil-safe
```

- Rules, applied under `tail.mu` while encoding (so the entry and the state change are one step):
  - every placement except `PlaceVerbatim` sets `Format = schema.TurnFormatIdentity` and a `TurnID`;
  - `PlaceSession`: running execution → its ID; else if `tail.prelude` → `appwire.SystemPreludeTurnID`; else the open gap ID, minting one when none is open;
  - `PlaceAsync`: running execution → its ID; else a fresh delivery ID;
  - `PlaceDelivery`: a fresh delivery ID;
  - `PlaceInTurn(id)`: `id`;
  - `PlaceCompletion`: the running execution's ID; with none running the record is `Record{}` and nil error (nothing to complete). When the completion is recorded the running execution is cleared.
  - `TurnKind` is written on the first recorded entry of a turn: the execution's first entry when `BeginExecution(id, false)` (a reopen never re-stamps), a new gap's first, every delivery entry, the prelude's first.
  - Any execution, delivery or prelude entry closes the open gap. `BeginExecution` ends the prelude.
  - A new tail from `NewWriter*` starts in the prelude; a tail from a resume scan does not.
  - State advances only for recorded entries: a rolled-back first entry leaves the gap open and its `TurnKind` pending for the next entry.
- `identifier.NewTurnID` failing is an append error (nothing written).

- [ ] **Step 1: Write the failing tests**

```go
func TestPlacementFollowsTheRunningExecutionGapAndPrelude(t *testing.T) {
	w := newPlacementWriter(t)
	rec := func(p Placement) schema.Turn {
		t.Helper()
		r, err := w.Record(steeringTurn("x"), RecordOptions{Door: DoorBuffered, Place: p})
		if err != nil || !r.Recorded {
			t.Fatalf("record: %+v, %v", r, err)
		}
		if r.Turn.Format != schema.TurnFormatIdentity {
			t.Fatalf("format marker missing: %+v", r.Turn)
		}
		return r.Turn
	}
	p1, p2 := rec(PlaceSession), rec(PlaceSession)
	if p1.TurnID != appwire.SystemPreludeTurnID || p1.TurnKind != schema.TurnSpanPrelude || p2.TurnKind != "" {
		t.Fatalf("prelude = %+v, %+v", p1, p2)
	}
	w.BeginExecution("turn_m1", false)
	e1, e2 := rec(PlaceSession), rec(PlaceAsync)
	if e1.TurnID != "turn_m1" || e1.TurnKind != schema.TurnSpanExecution || e2.TurnID != "turn_m1" || e2.TurnKind != "" {
		t.Fatalf("execution = %+v, %+v", e1, e2)
	}
	done := rec(PlaceCompletion)
	if done.TurnID != "turn_m1" || w.RunningTurnID() != "" {
		t.Fatalf("completion = %+v, running %q", done, w.RunningTurnID())
	}
	g1, g2 := rec(PlaceSession), rec(PlaceSession)
	if !strings.HasPrefix(g1.TurnID, "t_") || g1.TurnKind != schema.TurnSpanGap || g2.TurnID != g1.TurnID || g2.TurnKind != "" {
		t.Fatalf("gap = %+v, %+v", g1, g2)
	}
	d := rec(PlaceAsync)
	if d.TurnID == g1.TurnID || d.TurnKind != schema.TurnSpanDelivery {
		t.Fatalf("idle async = %+v", d)
	}
	if g3 := rec(PlaceSession); g3.TurnID == g1.TurnID || g3.TurnKind != schema.TurnSpanGap {
		t.Fatalf("a delivery entry must close the gap: %+v", g3)
	}
	if again := rec(PlaceSession); again.TurnKind != "" {
		t.Fatalf("second gap entry re-stamped TurnKind: %+v", again)
	}
	verbatim := steeringTurn("copy")
	verbatim.TurnID, verbatim.Format = "turn_m9", schema.TurnFormatIdentity
	if r, _ := w.Record(verbatim, RecordOptions{Place: PlaceVerbatim}); r.Turn.TurnID != "turn_m9" {
		t.Fatalf("verbatim = %+v", r.Turn)
	}
	legacy := steeringTurn("legacy copy")
	if r, _ := w.Record(legacy, RecordOptions{Place: PlaceVerbatim}); r.Turn.Format != 0 || r.Turn.TurnID != "" {
		t.Fatalf("verbatim stamped a legacy copy: %+v", r.Turn)
	}
}

func TestReopenedExecutionDoesNotRestampTurnKind(t *testing.T) {
	w := newPlacementWriter(t)
	w.BeginExecution("turn_m3", true)
	r, err := w.Record(schema.NewTurn(schema.TurnReopen, llm.Message{Role: llm.RoleUser}), RecordOptions{Place: PlaceSession})
	if err != nil || r.Turn.TurnID != "turn_m3" || r.Turn.TurnKind != "" {
		t.Fatalf("reopen marker = %+v, %v", r.Turn, err)
	}
}

func TestResumedTailIsNotInThePrelude(t *testing.T) {
	path := newSharedFileTranscript(t)
	w := openSharedFileWriter(t, path)
	defer w.Close() //nolint:errcheck // fixture
	r, err := w.Record(steeringTurn("after resume"), RecordOptions{Place: PlaceSession})
	if err != nil || r.Turn.TurnKind != schema.TurnSpanGap {
		t.Fatalf("resume startup entry = %+v, %v; want a gap turn", r.Turn, err)
	}
}

func TestAsyncRecordAfterCompletionTakesADeliveryTurn(t *testing.T) {
	w := newPlacementWriter(t)
	w.BeginExecution("turn_m4", false)
	if _, err := w.Record(steeringTurn("in turn"), RecordOptions{Place: PlaceSession}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Record(schema.NewTurn(schema.TurnCompletion, llm.Message{Role: llm.RoleUser}), RecordOptions{Place: PlaceCompletion}); err != nil {
		t.Fatal(err)
	}
	late, err := w.Record(steeringTurn("late attention"), RecordOptions{Door: DoorSynced, Place: PlaceAsync})
	if err != nil || late.Turn.TurnID == "turn_m4" || late.Turn.TurnKind != schema.TurnSpanDelivery {
		t.Fatalf("late async write = %+v, %v", late.Turn, err)
	}
}

func TestColdWriterOnASharedFileTakesADeliveryTurnMidExecution(t *testing.T) {
	path := newSharedFileTranscript(t)
	session := openSharedFileWriter(t, path)
	defer session.Close() //nolint:errcheck // fixture
	session.BeginExecution("turn_m5", false)
	cold := openSharedFileWriter(t, path)
	r, err := cold.Record(steeringTurn("cold"), RecordOptions{Door: DoorSynced, Place: PlaceDelivery})
	_ = cold.Close()
	if err != nil || r.Turn.TurnKind != schema.TurnSpanDelivery || r.Turn.TurnID == "turn_m5" {
		t.Fatalf("cold = %+v, %v", r.Turn, err)
	}
	after, err := session.Record(steeringTurn("still running"), RecordOptions{Place: PlaceSession})
	if err != nil || after.Turn.TurnID != "turn_m5" {
		t.Fatalf("the execution span must continue after a cold write: %+v, %v", after.Turn, err)
	}
}
```

`newPlacementWriter` creates a fresh transcript with `NewWriterNoSync` in `t.TempDir()`.

- [ ] **Step 2: Run** `go test ./agent/transcript -run 'Placement|Reopened|ResumedTail|AsyncRecord|ColdWriterOnAShared' -count=1` → FAIL `undefined: Placement`.
- [ ] **Step 3: Implement** `placement.go`: the `Placement` type, the tail fields (`running struct{ id string; fresh bool }`, `gapID string`, `gapFresh bool`, `prelude bool`, `preludeStamped bool`), `(t *appendTail) place(turn *schema.Turn, p Placement) (commit func(), err error)` which stamps the turn and returns the state change to apply once the line is recorded, and `BeginExecution`/`RunningTurnID` taking `tail.mu`. `appendBatchLocked` calls `place` per turn before encoding and each `commit` in the recorded walk.
- [ ] **Step 4: Run** `go test ./agent/transcript -count=1 -race` → PASS.
- [ ] **Step 5: Commit** — `feat(transcript): the append tail places each entry in its turn`

### Task 5: Recorded-entry hook under the append lock

**Files:** Modify `agent/transcript/append_tail.go`, `transcript.go`; Test `agent/transcript/recorded_hook_test.go`

**Interfaces:** Produces `func (w *Writer) OnRecorded(fn func(Record))` — installs the file's one hook on the shared tail (nil removes it). Phase 3 hands each recorded entry to a thread's projection queue from here. The hook runs under `tail.mu` for each recorded line, in ordinal order, after `recordedLength` includes the line; it must not append or take a lock that an appender holds.

- [ ] **Step 1: Write the failing test** — the carried-over boundary test:

```go
func TestRecordedHookSeesEveryOrdinalInOrderUnderConcurrentAppends(t *testing.T) {
	path := newSharedFileTranscript(t) // one entry already recorded
	a := openSharedFileWriter(t, path)
	defer a.Close() //nolint:errcheck // fixture
	b := openSharedFileWriter(t, path)
	defer b.Close() //nolint:errcheck // fixture
	var seen []uint64
	var lengths []int64
	a.OnRecorded(func(r Record) {
		// Under the append lock: the recorded length already covers the line,
		// and no other append can interleave with this call.
		seen = append(seen, r.Ordinal)
		lengths = append(lengths, r.Offset+r.Length)
	})
	const perWriter = 200
	var wg sync.WaitGroup
	for _, w := range []*Writer{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				door := DoorBuffered
				if i%3 == 0 {
					door = DoorDurable
				}
				if _, err := w.Record(steeringTurn(fmt.Sprint(i)), RecordOptions{Door: door, Place: PlaceAsync}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != 2*perWriter {
		t.Fatalf("hook saw %d records, want %d", len(seen), 2*perWriter)
	}
	for i, ordinal := range seen {
		if ordinal != uint64(i+1) {
			t.Fatalf("hook call %d saw ordinal %d, want %d", i, ordinal, i+1)
		}
		if i > 0 && lengths[i] <= lengths[i-1] {
			t.Fatalf("recorded length did not grow at call %d", i)
		}
	}
	if got := a.RecordedLength(); got != lengths[len(lengths)-1] {
		t.Fatalf("recorded length %d, last hooked end %d", got, lengths[len(lengths)-1])
	}
}
```

Plus a test that a hook installed on one writer sees a cold writer's append on the same file (the hook lives on the tail, not the writer).

- [ ] **Step 2: Run** with `-race` → FAIL `OnRecorded undefined`.
- [ ] **Step 3: Implement** — `tail.onRecorded func(Record)` set under `tail.mu`; the recorded walk (Task 2) calls it after advancing `recordedLength`.
- [ ] **Step 4: Run** `go test ./agent/transcript -count=1 -race` → PASS.
- [ ] **Step 5: Commit** — `feat(transcript): a recorded-entry hook called in ordinal order under the append lock`

### Task 6: Unit 2a gates, simplify, PR

- [ ] Run `set -o pipefail; go test ./agent/transcript ./agent/schema ./internal/apptranscript ./internal/transcriptindex -count=1 -race`, `go test ./server -run 'TestTranscriptParity|TestParity' -count=1`, `go test ./agent -count=1 -timeout 30m`, `make vet`, `make lint`. All pristine.
- [ ] Invoke `simplify` on the unit's diff; apply worthwhile fixes; re-run; commit.
- [ ] `git branch wip/trm-phase2a` at this commit; push; open the PR against `wip/trm-phase1` (body: what/why linking spec PR #2301, tests, gates run locally since CI only runs for PRs into main, the inherited merge files).
- [ ] roborev rounds per the brief.

---

## Unit 2b: every consumer skips the transcript-only kinds

Shared fixture first, then one task per consumer. Each consumer test builds a transcript (or history) twice, once plain and once with transcript-only entries interleaved, and asserts the consumer's output is the same (for the app projection: the same apart from entry-index-derived identifiers, Spec issue 3).

### Task 7: A fixture that interleaves transcript-only entries

**Files:** Create `agent/schema/schematest/transcript_only.go`; Test `agent/schema/schematest/transcript_only_test.go`

**Interfaces:** Produces `func TranscriptOnlySamples() []schema.Turn` (one of each kind: completion completed, reopen, COMMUNICATE with call ID, NOTICE of each notice kind; each with `Format`, `TurnID`) and `func InterleaveTranscriptOnly(turns []schema.Turn) []schema.Turn` (a sample before the first turn, between every pair — including between an ASSISTANT with tool calls and its TOOL_RESULTS — and after the last, cycling through the samples).

- [ ] Test: the output minus `TranscriptOnly()` entries equals the input; every sample kind appears; a sample sits between each adjacent pair.
- [ ] Implement, run `go test ./agent/schema/... -count=1`, commit `test(schema): a fixture that interleaves transcript-only entries`.

### Task 8: Resume skips them, with divergence mapping intact

**Files:** Modify `agent/transcript_read.go` (`resumeHistoryIndexed`, `retainedFrom`, `mapDivergenceThroughResumedHistory`); Test `agent/transcript_only_resume_test.go`

- [ ] **Step 1: failing test** — build entries for a session with a user turn, an assistant tool call, its results, a compaction SUMMARY, and a later turn; `resumeHistoryIndexed(interleaved)` must equal `resumeHistoryIndexed(plain)` (turns and repair insertion indexes), and `mapDivergenceThroughResumedHistory` for every divergence index of the plain transcript must map to the same resumed coordinate as the corresponding index in the interleaved one.
- [ ] **Step 2: run** `go test ./agent -run TestResumeSkipsTranscriptOnly -count=1` → FAIL (the new kinds land in history).
- [ ] **Step 3: implement** — `resumeHistoryIndexed` drops `Turn.Kind.TranscriptOnly()` entries while copying; the divergence mapper subtracts the transcript-only entries it skipped at or before the mapped index (a helper `transcriptOnlyBefore(entries, i) int` used by both).
- [ ] **Step 4: run** the new test and `go test ./agent -run 'Resume|Restore|Divergence|Fork' -count=1 -timeout 30m` → PASS.
- [ ] **Step 5: commit** — `fix(agent): resume keeps transcript-only entries out of history`

### Task 9: Orphan repair, the wire builder and Responses continuation

Belt and braces behind Task 8: in-memory history never holds these kinds, but these three switches are the ones that would send a stray one to the model.

**Files:** Modify `agent/history_repair.go` (pass-through case), `agent/session_model_call.go` expandHistory (drop without ending the tool round, like HOOK_COMPLETED), `agent/responses_continuation_eligibility.go` (skip); Test `agent/transcript_only_model_history_test.go`

- [ ] Tests: `repairOrphanedToolResultsIndexed(interleaved)` inserts no synthetic results and returns the plain repair after filtering; the wire messages built from an interleaved history equal the plain history's (drive `expandHistory` through the same helper existing expandHistory tests use); the continuation-delta check returns "" for an interleaved delta that is eligible plain.
- [ ] Run → FAIL; implement the three cases; run `go test ./agent -run 'Repair|ExpandHistory|ContinuationEligib|TranscriptOnly' -count=1 -timeout 30m` → PASS; commit `fix(agent): model-history builders pass over transcript-only entries`.

### Task 10: The public transcript line projection (transcript tool, find, raw ranges)

**Files:** Modify `agent/session_tools_transcript.go` (`publicTranscriptEntry`, `publicTranscriptLine`, the `transcriptExpansionJSONL` guard); Test `agent/transcript_only_public_test.go`

- [ ] Test: `publicTranscriptEntries(interleaved)` equals `publicTranscriptEntries(plain)` (including the renumbered `Seq`), and `publicTranscriptLine` of a transcript-only line reports it omitted exactly as it does for ATTENTION_RESOLUTION. Covers `read_session_transcript`, `find` (it reads through `publicTranscriptEntries`) and `transcript_render.go`'s raw range.
- [ ] Run → FAIL; implement by extending the existing ATTENTION_RESOLUTION omission to `Kind.TranscriptOnly()` in each of the three places (keep the panic guard's two checks in sync); run `go test ./agent -run 'Transcript|Find' -count=1 -timeout 30m`; commit `fix(agent): the model-facing transcript omits transcript-only entries`.

### Task 11: Outline and the markdown renderer

**Files:** Modify `agent/session_outline.go` (`outlineLine` skip), `agent/transcript_render.go` (`writeEntry` no-op case); Test `agent/transcript_only_render_test.go`

- [ ] Test: `renderOutline` and the markdown body of an interleaved transcript equal the plain ones byte for byte (turn numbering included: the renderer numbers visible entries; if it numbers by entry index, compare after mapping — assert the rendered sections, not headings that carry indexes).
- [ ] Run → FAIL; implement; run `go test ./agent -run 'Outline|Render' -count=1 -timeout 30m`; commit `fix(agent): outline and transcript rendering skip transcript-only entries`.

### Task 12: Delegate fork context and conversation signals

**Files:** Modify `agent/delegate_fork_context.go` (builder skip list; `completedDelegateContext` pass-through), `agent/session_init.go` `conversationSignals`; Test `agent/transcript_only_fork_context_test.go`

- [ ] Test: the fork context built from interleaved entries equals the plain one; `completedDelegateContext` cuts at the same point (an interleaved entry between a call and its result must not clear pending calls); `conversationSignals` counts the same.
- [ ] Run → FAIL; implement; run `go test ./agent -run 'ForkContext|ConversationSignals|Delegate' -count=1 -timeout 30m`; commit `fix(agent): delegate fork context skips transcript-only entries`.

### Task 13: Grouping, the whole-file projection and the bounded index

**Files:** Modify `internal/apptranscript/logical_turn.go` (`logicalTurnAccumulator.appendEntry`/`appendProjectedEntry` skip), `internal/apptranscript/turn_index.go` (the index scan: record the entry but neither group nor project it, and take `prevKind` from the last non-transcript-only record), `internal/apptranscript/apptranscript.go` `ProjectTurnParts` (explicit `nil` for the new kinds); Test `internal/apptranscript/transcript_only_test.go`

- [ ] Test: for the phase 1 fixture corpus shapes (reuse `apptranscript` test builders), `ItemTurnsFromFile`, the bounded latest/before window readers and the turn index all project an interleaved transcript to the same turns and items as the plain one after `normalizeEntryIndexIDs` (strips `id`, `turnId`, `transcriptKey`, `transcriptEntryIndex` where they derive from the entry index, keeping `StableTurnID`-derived turn IDs) — and, without normalization, keep every turn ID that comes from `StableTurnID`.
- [ ] Run → FAIL (a completion entry splits its turn); implement; run `go test ./internal/apptranscript -count=1`; commit `fix(apptranscript): grouping and projection pass over transcript-only entries`.

### Task 14: The phase 1 index still equals the whole-file projection

**Files:** Modify `internal/transcriptindex/build.go` (`apply`: a transcript-only entry consumes its ordinal and nothing else); Test `internal/transcriptindex/fixture_test.go` (add an interleaved variant of the "everything" fixture) and `reference_test.go` (`referenceTurns` skips them the way production does).

- [ ] Run the existing window-equality tests over the new fixture → FAIL; implement; run `go test ./internal/transcriptindex -count=1` → PASS; commit `fix(transcriptindex): index transcript-only entries as ordinals only`.

### Task 15: Derived totals, the failure counter and the attention fold

**Files:** Test only unless a test fails: `internal/apptranscript/transcript_only_totals_test.go`, `agent/transcript/failures_test.go`, `agent/transcript_only_attention_test.go`

- [ ] Tests: `scanDerivedTotals`/usage total/failed tool calls over interleaved equal plain (sample entries carry no usage or tool parts — assert the fixture keeps that invariant); `FailureCounter.Observe` over interleaved equals plain; `foldDelegateAttention(interleaved)` equals the plain fold. If any fails, add the explicit skip; commit `test: derived totals and the attention fold ignore transcript-only entries`.

### Task 16: Eval probes, ATIF export and doctor reconstruct

**Files:** Modify `agent/eval_probes.go` `turnsToMessages`, `agent/internal/atif/atif.go` (look-ahead past them between ASSISTANT and TOOL_RESULTS; skip in the default branch), `cmd/evener-doctor/reconstruct.go` (skip before the role switch and the round reset); Tests alongside each.

- [ ] Tests: interleaved equals plain for each (ATIF: the trajectory JSON; doctor: the reconstructed transcript's entries).
- [ ] Run → FAIL; implement; run the three packages' tests; commit `fix: eval probes, ATIF export and doctor reconstruct skip transcript-only entries`.

### Task 17: Server seeding from a restored transcript

**Files:** Test `server/transcript_only_seed_test.go`

- [ ] Test: `PrepareAppIdentityFromEntriesForPath` over interleaved entries yields the same turns as over plain entries (normalized as in Task 13). It projects through apptranscript, so this should pass after Task 13; it pins the server path. Commit `test(server): restored identity seeding ignores transcript-only entries`.

### Task 18: Unit 2b gates, simplify, PR

- [ ] Gates as Task 6, plus `go test ./cmd/evener-doctor ./agent/internal/atif -count=1`.
- [ ] `simplify`; commit; `git branch wip/trm-phase2b`; push; PR against `wip/trm-phase2a`; roborev rounds.

---

## Unit 2c: writers write identity and the new entries

### Task 19: Session doors return the record, stamp the model, and place entries

**Files:** Modify `agent/session.go` (doors), `agent/session_attention.go` (async writes), `agent/delegate_tree_steer.go`; Test `agent/transcript_identity_test.go`

**Interfaces:**
- Produces:
  - `func (s *Session) recordTranscriptLocked(t schema.Turn, door transcript.Door, place transcript.Placement) (transcript.Record, error)` — the one session door; holds a pre-attach turn exactly as today (reporting `Record{}`, nil) and flushes held turns at attach with `PlaceSession`. `writeTranscriptLocked`, `writeTranscriptDurableLocked`, `writeTranscriptSyncedLocked` wrap it with `PlaceSession`.
  - Every session door stamps `Model` (the session's configured model, `s.currentProfile().Model()`) when the turn has none.
  - `s.lastTranscriptRecord transcript.Record`, set by every door under `attentionMu`; the pair log reads it (Task 24).
  - Async writes use `PlaceAsync`: `appendDelegateAttentionMessageDurably`, `appendDelegateAttentionResolutions` (resident writer), `stabilizeAttentionForStop`, and `appendDelegateSteeringDurablyWithMetadata` (through a `writeTranscriptSyncedAsyncLocked` door).
  - Cold writes use `PlaceDelivery`: `appendColdDelegateAttentionMessageDurablyWithOpen`, `appendColdAttentionResolutionForGenerationWithOpen`.
  - Copies use `PlaceVerbatim`: fork/aside prefix copies (`writeForkChildWithConfig`), `NewSession`'s inherited delegate context.

- [ ] **Step 1: failing tests** — through a real session with a scripted provider (the helpers the parity harness and existing session tests use: `newTestSession`/scripted `llm.ProviderAdapter`):
  1. a fresh session's startup HOOK_COMPLETED entries are `turn_system`/`prelude`;
  2. every entry carries `Format` and `Model`;
  3. an attention delivery while idle is a `delivery` turn; while an execution runs (hold the provider mid-request) it takes the execution's `TurnID`;
  4. a cold attention write is a `delivery` turn;
  5. a fork child's copied prefix is byte-identical in identity to the parent's lines.
- [ ] **Step 2: run** `go test ./agent -run TestTranscriptIdentity -count=1 -timeout 30m` → FAIL.
- [ ] **Step 3: implement.**
- [ ] **Step 4: run** the test plus `go test ./agent -count=1 -timeout 30m`.
- [ ] **Step 5: commit** — `feat(agent): session and cold writers place entries in turns`

### Task 20: Executions — admission names the turn, the completion entry ends it

**Files:** Modify `agent/session_lifecycle.go` (`processOneInput`, `processInputKindWithProvenance`), `agent/session_events.go` (`recordTurnFailure` marks the running execution failed); Create `agent/session_execution.go`; Test `agent/session_execution_test.go`

**Interfaces:**
- Produces:

```go
// beginExecution names the running execution on the transcript's registry
// entry: turnID when the turn has a client-mutation name, a fresh t_ id
// otherwise (and for a legacy-spelled recovered id). reopen marks a reclaimed
// turn, whose first entry is a TURN_REOPEN marker.
func (s *Session) beginExecution(turnID string)
// completeExecution records the running execution's completion entry as the
// last entry of its span. status is interrupted or completed; a TURN_FAILURE
// recorded in the span makes it failed. It is a no-op when no execution runs.
func (s *Session) completeExecution(status schema.TurnCompletionStatus)
```

- Admission: in `processOneInput` after the closed check and `turnStartedAt`, for a user input (the claimed identity's `StableTurnID` when it parses as `turn_m<N>`, else a fresh ID) — and for a continuation or notification after its name is minted (`runningTurnID` when set, else a fresh ID) and before `acceptContinuationInput`/`acceptNotificationInput`.
- Completion: in `processInputKindWithProvenance` after each `processOneInput`: interrupted after the interrupt branch has recorded its marker (or when the session is closing); failed before `endInputAtTurnFailure`; completed before the drain decision on success. `DurationMS` is `CompletedAt − turnStartedAt`.
- The roundID (Task 22) is cleared at completion.

- [ ] **Step 1: failing tests** (scripted provider):
  1. one user turn: its entries share one `t_` TurnID; the first carries `TurnKind: execution`; the last is `TURN_COMPLETION{completed}` with `DurationMS >= 0`;
  2. a client-mutation start: TurnID is its `turn_m<N>`;
  3. a turn that fails with a terminal provider error: TURN_FAILURE then `TURN_COMPLETION{failed}`, both in the turn;
  4. an interrupted turn: the interrupt STEERING then `TURN_COMPLETION{interrupted}`;
  5. a goal continuation and a notification turn each get their own execution;
  6. a model switch while idle is a gap turn; two idle announcements in a row share it;
  7. a retried round (503 then success) writes no completion in between;
  8. a recovered legacy-spelled `turn_11` runs under a fresh `t_` TurnID and keeps its `StableTurnID`;
  9. no completion entry ever enters `s.history`.
- [ ] Run → FAIL; implement; run `go test ./agent -run 'TestExecution' -count=1 -timeout 30m` and the full agent suite; commit `feat(agent): executions name their turn and end with a completion entry`.

### Task 21: Async writes pick their turn inside the append lock

**Files:** Test `agent/session_execution_async_test.go`

- [ ] Test (the spec's activation boundary, deterministic): start a turn with a scripted provider; from a second goroutine, block an attention write until the completion is being recorded (hook: `OnRecorded` on the session writer signals when the completion entry is recorded), then release it — the late write's TurnID is a fresh delivery ID, never the completed turn's; and an attention write released before the completion takes the execution's ID. Run with `-race`. Commit `test(agent): an async write racing a completion never joins the completed turn`.

### Task 22: roundID on ASSISTANT and salvage entries

**Files:** Modify `agent/session.go` (`appendAssistantTurn`), `agent/session_model_call.go` (`callModelWithFallback` takes the round's ID); Test `agent/round_id_test.go`

**Interfaces:** `func (s *Session) roundIDForModelCall() string` — returns the open round's ID, minting `identifier.NewRoundID()` when none is open. `appendAssistantTurn` stamps it and, once the entry is recorded, closes the round. `completeExecution` closes it too.

- [ ] Tests: two rounds of one turn carry different `RoundID`s; a round retried after a 503 keeps one `RoundID` on its single ASSISTANT; a pause_turn continuation and a bare-text retry each start a new one; an interrupted round's salvage ASSISTANT carries the round's ID. Run → FAIL; implement; commit `feat(agent): record each round's id on its assistant entry`.

### Task 23: Resume closes crashed executions and reopens reclaimed ones

**Files:** Modify `agent/session_init.go` (restore, after `attachTranscript`), `agent/session_execution.go`; Create helper in `agent/transcript/turn_status.go`: `func OpenExecutionTurns(entries []Entry) []string` and `func HasTurn(entries []Entry, turnID string) bool`; Tests `agent/transcript/turn_status_test.go`, `agent/session_execution_resume_test.go`

- Rules: an execution turn (first entry `TurnKind: execution`) is open unless its latest lifecycle entry (completion or reopen) is a completion. On restore the session writes `TURN_COMPLETION{interrupted}` with `PlaceInTurn(id)` for each open turn except `s.recoveredTurnID`. When the recovered turn runs again and the transcript already holds entries of its ID, `beginExecution` begins it with `reopen` and writes a `TURN_REOPEN` marker first.
- [ ] Tests: `OpenExecutionTurns` over a crafted entry list (open, completed, reopened-then-open, reopened-then-completed, legacy entries ignored); a session killed mid-turn (close the process-side session without completion by using the test seam that crashes before completion, or by writing a crafted transcript and restoring) gets exactly one interrupted completion on restore; a recovered client-mutation start whose USER_INPUT landed before the crash gets no interrupted completion and runs again after a `TURN_REOPEN` marker under the same `turn_m<N>`; restoring twice writes nothing the second time. Run → FAIL; implement; commit `feat(agent): resume closes crashed executions and reopens the one it reclaims`.

### Task 24: Fold copies carry OriginalOrdinal

**Files:** Modify `agent/session.go` (`logPairPersistedLocked`, `recordTurn`), `agent/session_compaction.go` (rewrite tail); Test `agent/fold_original_ordinal_test.go`

- The pair log stores each persisted turn with `OriginalOrdinal = &lastTranscriptRecord.Ordinal` once its write has recorded (`recordTurn` sets it on the logged entry after its write returns; `appendTurnAfterTranscriptWriteLocked` logs after the write). The fold re-appends the logged turns as they are.
- [ ] Test: a fold that publishes while a pair recorded during the fold is in the log re-appends that pair after its markers with `OriginalOrdinal` equal to the original line's ordinal (read the file and compare); resume still restores the same history as before the change. Run → FAIL; implement; commit `feat(agent): fold copies record the ordinal of the entry they copy`.

### Task 25: COMMUNICATE and the presentational entries

**Files:** Modify `agent/session_tools_communicate.go` (+ `toolDeps.recordCommunicate`), `agent/session_tools.go` (tool repair notice), `agent/session_goal.go` (`emitGoalEnded`), `agent/session_lifecycle.go` (turn limit at `:2483` and `:2615` — no TURN_FAILURE covers either), `agent/session_skill_delivery.go` (skill_activated only when no execution runs: none today, so this writes nothing and a test pins that the in-turn activation writes no notice); Test `agent/transcript_only_writes_test.go`

**Interfaces:** `toolDeps.recordCommunicate func(schema.CommunicateInfo) (recorded bool, err error)` backed by `s.recordTranscriptOnly(turn, transcript.DoorDurable)`; `func (s *Session) recordNotice(info schema.NoticeInfo)` (buffered door, warn on failure like other presentational writes).

- COMMUNICATE: recorded before `EventCommunicate` is emitted, carrying the call ID. If the session has no writer (no state directory) it emits as today. If the append is not recorded, it does not emit and reports the failure to the session (Task 27 turns that into fail-closed; until then it returns the tool error "communicate was not recorded").
- [ ] Tests: a communicate call writes one COMMUNICATE entry (message, end_turn, call ID) in its execution before the TOOL_RESULTS entry, and the event is emitted after the entry is recorded (the test's event consumer reads the file when it sees the event and finds the entry); the four failure paths write no entry and emit nothing; a no-state-dir session emits exactly as before; a tool repair, a goal end (parity scenario's `update_goal` complete) and a tool-round cap each write one NOTICE with the event's data; none of these ever enter `s.history`; the parity harness's `whyGoalEnd` row still holds (Task 26). Run → FAIL; implement; commit `feat(agent): record communicate at delivery and the presentational notices`.

### Task 26: Fork sequence seeding and the phase 2 parity check

**Files:** Modify `agent/session_client_mutation_queue.go` (`ensureClientMutationStore` raises `NextTurnSequence` to `s.turnSequenceFloor`), `agent/session_init.go` (floor from restore entries and from `inheritedContext`); Create `highestClientMutationTurnSequence(turns []schema.Turn) uint64` (max `turn_m<N>` over `TurnID`, `StableTurnID`, `OwningTurnID`); Tests `agent/fork_turn_sequence_test.go`, `server/transcript_parity_test.go`

- [ ] Tests: a fork child restored from a parent whose prefix holds `turn_m1..turn_m3` reserves `turn_m4` for its first client-mutation start; an aside the same; a delegate fork child with inherited context the same. Run → FAIL; implement.
- [ ] Parity: extend `assertParityScenarioCoverage` with `TURN_COMPLETION`, `COMMUNICATE`, `NOTICE` and "execution turn id" so the scenario proves the writers run; run `go test ./server -run TestTranscriptParity -count=1`. The divergence tables must not change; if a row changes, record it in the PR with its reason. Also run `go test ./internal/transcriptindex -count=1` with the opt-in real-data test skipped (default).
- [ ] Commit `feat(agent): seed a fork's turn sequence above the turns it copies`.
- [ ] Unit 2c gates as Task 6; `simplify`; commit; `git branch wip/trm-phase2c`; push; PR against `wip/trm-phase2b`; roborev rounds.

---

## Unit 2d: fail closed

### Task 27: A served session fails closed on writer failure

**Files:** Modify `agent/session_lifecycle.go` (`refuseOnUnhealthyTranscript` becomes a session method consulting the fail-closed state; every caller), `agent/session_init.go` (remember a create failure), `agent/session_tools_communicate.go`; Create `agent/session_fail_closed.go`; Test `agent/session_fail_closed_test.go`

**Interfaces:**

```go
// failClosed stops a served session whose transcript can no longer record
// what it announces: the running execution is interrupted, no further input
// is accepted, and one diagnostic is emitted. It runs once; later causes are
// ignored. Unserved sessions keep warn-and-continue.
func (s *Session) failClosed(cause error)
```

- Triggers (only when `servedByDaemon()` at that moment): the transcript could not be created (checked at admission, since serving attaches after construction); the writer is poisoned (checked where `refuseOnUnhealthyTranscript` runs today and after any door returns `ErrWriterPoisoned`); a COMMUNICATE append not recorded.
- Effects: cancel the running execution's context (store `processOneInput`'s cancel on the session under `s.mu`); `refuseOnUnhealthyTranscript` returns the fail-closed error so every admission path (`processInputKindWithProvenance`, `refuseBeforeClaimingOnUnhealthyTranscript`, the client-mutation claims) refuses; emit exactly one `EventError` with source `transcript`.
- [ ] Tests (served session: `ConsumeEventsLossless` sets the authoritative consumer, as the parity harness does):
  1. create failure (the `sessionInitFault("new_transcript")` seam) → the first input is refused with the fail-closed error and one EventError is emitted; a second input is refused and emits nothing more;
  2. a COMMUNICATE append that is not recorded (the writer fault seam the transcript tests use, injected through the session's test-only writer hook) → no EventCommunicate, the running turn ends interrupted, one EventError, later input refused;
  3. a poisoned writer mid-turn → the running execution is interrupted rather than running on in memory;
  4. an unserved session under the same faults keeps today's behavior (warning, EventCommunicate emitted);
  5. a durable append that rolls back cleanly does not fail closed.
- [ ] Run → FAIL; implement; run the agent suite; commit `feat(agent): a served session fails closed when its transcript cannot record`.

### Task 28: Final gates, simplify, PR

- [ ] `simplify` on the unit; commit.
- [ ] Gates: `go test ./agent -count=1 -timeout 30m`, `go test ./agent/transcript ./agent/schema/... ./internal/apptranscript ./internal/transcriptindex ./server -count=1 -race`, `make vet`, `make lint`, `WEB=0 make test`. Pristine.
- [ ] `git branch wip/trm-phase2d`; push; PR against `wip/trm-phase2c`; roborev rounds.

## Self-review

- Spec coverage: format marker (1, 4), TurnID spellings (4, 20, 26), fork seeding (26), legacy-spelled IDs (20), TurnKind rules including prelude and resume startup (4, 19), async writes (4, 19, 21), registry-held running and gap IDs (4), roundID (22), OriginalOrdinal (24), per-entry model (19), completion with timing and reopen (20, 23), COMMUNICATE before emit with call ID (25), presentational entries (25), tool DurationMS (Spec issue 8, pinned in 25's test of TOOL_RESULTS), consumers (8-17), write result (2), ordinal and recorded length (2), Seq on rollback (3), projection hook (5), fail closed (27), strict decode and old transcripts (1, and every existing decode test).
- Review Focus lines each map to a named test.
