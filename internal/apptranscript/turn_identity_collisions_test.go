package apptranscript_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// These tests pin the three identities the stamped grouping must NOT fold
// together, each of which two entries legitimately share (#1166 review):
//
//   - a stamped turn id can name two distinct spans (the prelude, interrupted
//     by a delivery, and resumed before the first execution);
//   - one execution can reuse a tool call id across rounds;
//   - a delivery can be recorded before the header prelude's own items.

// fullReadShapes projects the whole transcript the way the hub's cold read
// does, and reports each turn's id and its items' types in order.
func fullReadShapes(t *testing.T, path string) []turnShape {
	t.Helper()
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	return shapesFromTurns(turns)
}

func newHookTurn(event string) schema.Turn {
	turn := schema.NewTurn(schema.TurnHookCompleted, llm.System("hook ran"))
	turn.Hook = &schema.HookInfo{Event: event}
	return turn
}

// TestStampedPreludeKeepsOneSpanPerTurn covers a startup hook, an
// asynchronous attention delivery, and a model switch before the first
// execution. The writer stamps the hook and the model switch with the
// prelude's id, but the delivery takes a turn of its own and leaves the
// prelude open. Both readers must report the same turn boundaries: the
// prelude must stay ONE turn (not two turns sharing turn_system), and the
// delivery must keep its own turn.
func TestStampedPreludeKeepsOneSpanPerTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "prelude_spans"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	record(newHookTurn("SessionStart"), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnSteering, llm.User("attention")), transcript.PlaceAsync)
	record(schema.NewTurn(schema.TurnModelSwitch, llm.System("switch")), transcript.PlaceSession)
	w.BeginExecution("t_exec", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("done")), transcript.PlaceSession)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// The delivery ends the prelude's span, so the model switch opens a gap
	// turn of its own rather than rejoining an id another turn now splits.
	// Every turn id appears once, and the delivery keeps its own turn.
	full := fullReadShapes(t, path)
	if len(full) != 4 {
		t.Fatalf("full read turns = %+v, want 4 (prelude, delivery, gap, execution)", full)
	}
	if full[0].ID != appwire.SystemPreludeTurnID || !reflect.DeepEqual(full[0].Items, []string{"systemMessage/hook_completed"}) {
		t.Errorf("prelude = %+v, want the startup hook alone", full[0])
	}
	if full[1].ID == appwire.SystemPreludeTurnID || full[1].ID == "t_exec" {
		t.Errorf("delivery turn = %+v, want a turn of its own", full[1])
	}
	if full[3].ID != "t_exec" {
		t.Errorf("last turn = %+v, want the execution", full[3])
	}
	seen := map[string]bool{}
	for _, turn := range full {
		if turn.ID == "" {
			t.Errorf("turn with no id: %+v", turn)
		}
		if seen[turn.ID] {
			t.Errorf("turn id %q repeats across the turn list: %+v", turn.ID, full)
		}
		seen[turn.ID] = true
	}

	// The index must publish the same boundaries and the same ids.
	indexed := shapesFromCandidates(indexCandidates(t, path))
	if !reflect.DeepEqual(indexed, full) {
		t.Errorf("bounded index turn structure = %+v, want the full read's %+v", indexed, full)
	}
}

// TestReusedCallIDKeepsBothRounds covers a provider that reuses one tool call
// id across two rounds of ONE execution, separated by a hook completion. Both
// readers must keep both rounds as distinct calls, each with its own
// arguments; folding them by call id alone loses the first call's arguments.
func TestReusedCallIDKeepsBothRounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "call_reuse"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	call := func(args string) schema.Turn {
		tc := llm.ToolCallData{ID: "call_x", Name: "read_file", Arguments: json.RawMessage(args)}
		return schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &tc}}}}
	}
	w.BeginExecution("t_exec", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	record(call(`{"round":1}`), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_x", "read_file", "first-out", false)), transcript.PlaceSession)
	record(newHookTurn("Stop"), transcript.PlaceSession)
	record(call(`{"round":2}`), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_x", "read_file", "second-out", false)), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Both readers keep two commandExecution items for the reused id, each
	// carrying the arguments its own round announced.
	wantCalls := []string{`{"round":1}`, `{"round":2}`}
	for _, read := range []struct {
		name string
		got  []appitempaging.TranscriptItemCandidate
	}{
		{name: "index", got: indexCandidates(t, path)},
	} {
		var calls []string
		for _, candidate := range read.got {
			if candidate.Item.CallID == "call_x" && candidate.Item.Type == "commandExecution" {
				calls = append(calls, candidate.Item.ArgumentsJSON)
			}
		}
		if !reflect.DeepEqual(calls, wantCalls) {
			t.Errorf("%s call arguments = %v, want %v", read.name, calls, wantCalls)
		}
	}

	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	var calls []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_x" && item.Type == "commandExecution" {
				calls = append(calls, item.ArgumentsJSON)
			}
		}
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("full read call arguments = %v, want %v", calls, wantCalls)
	}
}

// TestDeliveryBeforePreludeKeepsPositionsIncreasing covers a delivery recorded
// before the first prelude-stamped entry in a transcript that has a system
// prompt. The header prelude's items hold entry position 0, so they must stay
// first: emitting them after the delivery makes positions decrease, which the
// item-paging validator rejects.
func TestDeliveryBeforePreludeKeepsPositionsIncreasing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "delivery_first", SystemPrompt: "You are Evener."})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	record(schema.NewTurn(schema.TurnSteering, llm.User("attention")), transcript.PlaceAsync)
	record(newHookTurn("SessionStart"), transcript.PlaceSession)
	w.BeginExecution("t_exec", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	candidates, err := appitempaging.CandidatesFromTurns(turns)
	if err != nil {
		t.Fatalf("CandidatesFromTurns: %v", err)
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		t.Fatalf("full read candidates rejected: %v", err)
	}
	if len(turns) > 0 && turns[0].ID != appwire.SystemPreludeTurnID {
		t.Errorf("first turn = %q, want the header prelude %q first", turns[0].ID, appwire.SystemPreludeTurnID)
	}
	// The index serves the same history; its candidates must satisfy the same
	// chronology.
	if err := appitempaging.ValidateCandidates(indexCandidates(t, path)); err != nil {
		t.Errorf("bounded index candidates rejected: %v", err)
	}
}

// TestAnnouncementAfterFailedResultKeepsBothCalls covers the case a buffered
// result write failed: a call is announced, no result ever settles it, and a
// later round of the same execution reuses the call id after a recorded hook.
// The second announcement is a second call, not a merge into the first, so it
// keeps its own arguments — the index opens a second call for it. A merge
// would overwrite the first call's arguments and its start time with the
// second round's, which is the record the first round no longer has (#1166
// review).
func TestAnnouncementAfterFailedResultKeepsBothCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "unsent_result"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	call := func(args string) schema.Turn {
		tc := llm.ToolCallData{ID: "call_x", Name: "read_file", Arguments: json.RawMessage(args)}
		return schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &tc}}}}
	}
	w.BeginExecution("t_exec", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	record(call(`{"round":1}`), transcript.PlaceSession)
	// No TOOL_RESULTS for round 1: its write failed.
	record(newHookTurn("Stop"), transcript.PlaceSession)
	record(call(`{"round":2}`), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_x", "read_file", "second-out", false)), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// The index opens one call per announcement.
	var indexed []string
	for _, candidate := range indexCandidates(t, path) {
		if candidate.Item.CallID == "call_x" && candidate.Item.Type == "commandExecution" {
			indexed = append(indexed, candidate.Item.ArgumentsJSON)
		}
	}
	wantCalls := []string{`{"round":1}`, `{"round":2}`}
	if !reflect.DeepEqual(indexed, wantCalls) {
		t.Fatalf("index call arguments = %v, want %v", indexed, wantCalls)
	}

	// The full read must keep both calls with their own arguments, as the
	// index does.
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	var full []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_x" && item.Type == "commandExecution" {
				full = append(full, item.ArgumentsJSON)
			}
		}
	}
	if !reflect.DeepEqual(full, wantCalls) {
		t.Errorf("full read call arguments = %v, want the index's %v", full, wantCalls)
	}
}

// TestSplitPreludeRunsAgreeOnCompleteness covers a file written before this
// change: a prelude-span entry, a delivery, and another prelude-span entry, so
// one stamped id names two runs. No reader can join the runs — their items sit
// on both sides of the delivery's, and a joined turn would emit decreasing
// positions, which item paging rejects — so both must report the same two runs
// with the same completeness: the first run continues later, the last run
// continues earlier (#1166 review).
func TestSplitPreludeRunsAgreeOnCompleteness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "split_prelude", SystemPrompt: "You are Evener."})
	if err != nil {
		t.Fatal(err)
	}
	stamped := func(kind schema.TurnKind, msg llm.Message, span schema.TurnSpanKind) schema.Turn {
		turn := schema.Turn{Kind: kind, Message: msg}
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = appwire.SystemPreludeTurnID
		turn.TurnKind = span
		return turn
	}
	hook := stamped(schema.TurnHookCompleted, llm.System("hook ran"), schema.TurnSpanPrelude)
	hook.Hook = &schema.HookInfo{Event: "SessionStart"}
	delivery := schema.Turn{Kind: schema.TurnSteering, Message: llm.User("attention")}
	delivery.Format = schema.TurnFormatIdentity
	delivery.TurnID = "turn_delivery_1"
	delivery.TurnKind = schema.TurnSpanDelivery
	switched := stamped(schema.TurnModelSwitch, llm.System("switch"), "")
	for _, turn := range []schema.Turn{hook, delivery, switched} {
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	// Both readers keep the delivery in its own turn, and the two prelude runs
	// as two turns — never one joined turn with decreasing positions.
	full := shapesFromTurns(turns)
	indexed := shapesFromCandidates(indexCandidates(t, path))
	if !reflect.DeepEqual(full, indexed) {
		t.Fatalf("full read turn structure = %+v, want the index's %+v", full, indexed)
	}
	// The runs of the prelude id report themselves as fragments of one turn.
	var runs []appwire.Turn
	for _, turn := range turns {
		if turn.ID == appwire.SystemPreludeTurnID {
			runs = append(runs, turn)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("prelude runs = %d turns (%+v), want 2", len(runs), runs)
	}
	if runs[0].HasEarlierItems || !runs[0].HasLaterItems {
		t.Errorf("first prelude run earlier=%v later=%v, want earlier=false later=true", runs[0].HasEarlierItems, runs[0].HasLaterItems)
	}
	if !runs[1].HasEarlierItems || runs[1].HasLaterItems {
		t.Errorf("last prelude run earlier=%v later=%v, want earlier=true later=false", runs[1].HasEarlierItems, runs[1].HasLaterItems)
	}
	// The delivery is a whole turn of its own: it continues nothing.
	for _, turn := range turns {
		if turn.ID == "turn_delivery_1" {
			if turn.HasEarlierItems || turn.HasLaterItems {
				t.Errorf("delivery turn earlier=%v later=%v, want both false", turn.HasEarlierItems, turn.HasLaterItems)
			}
		}
	}
}
