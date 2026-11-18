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

// These tests pin the stamping fix: a turn id the writer recorded at write
// time identifies exactly one turn, and both readers must render it that way
// (#1166).
//
// The settlement and status-fold work that also lived in this file was cut
// back out of this branch — it belongs to the issues filed for the stamped
// execution's status fold and for joining only what is genuinely one turn.

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
// prelude's id, but the delivery takes a turn of its own and ends the
// prelude's span. Both readers must report the same turn boundaries: no id
// may name two turns, and the delivery must keep its own turn.
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

// TestPlainProjectorCarriesAnnouncementRound covers the projector the hub's
// whole-file fallback uses. ProjectEntryParts is the identity-aware wrapper
// and sets RoundID on the announcement; the plain ProjectTurn did not, so a
// stamped announcement projected through the fallback carried no round and a
// settled result — which has no round of its own — could not inherit one. A
// client grouping by round then loses the round the call belonged to
// (#1166 review).
func TestPlainProjectorCarriesAnnouncementRound(t *testing.T) {
	call := llm.ToolCallData{ID: "call_round", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	turn := schema.Turn{
		Format:  schema.TurnFormatIdentity,
		Kind:    schema.TurnAssistant,
		TurnID:  "t_round",
		RoundID: "r_announce",
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}},
	}
	items := apptranscript.ProjectTurn("t_round", 1, turn, apptranscript.NewToolCallRegistry(), nil, apptranscript.ToolResultOutputImages)
	if len(items) != 1 {
		t.Fatalf("ProjectTurn items = %d (%+v), want the announcement", len(items), items)
	}
	if items[0].RoundID != "r_announce" {
		t.Errorf("announcement round id = %q, want %q: a settled result carries no round of its own",
			items[0].RoundID, "r_announce")
	}
}

// TestDeliveryBeforePreludeKeepsPositionsIncreasing covers a delivery recorded
// before the header prelude's first stamped entry. The header items hold entry
// position 0, so a merge that emitted the delivery ahead of them would flatten
// to decreasing positions, which item paging rejects. It also covers the
// writer's rule that a delivery ends the prelude's span: every turn id names
// one contiguous span.
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

// TestReusedCallIDKeepsBothRounds covers a provider that reuses one tool call
// id across two rounds of ONE execution, separated by a hook completion. The
// index settles a result only against a call of the turn's awaiting assistant
// entry (build.go's applyResults), so a reused id is two calls there. A full
// read that merges by call id alone overwrites the first call's arguments with
// the second's, losing a record the transcript still holds.
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
	var indexedCalls []string
	for _, candidate := range indexCandidates(t, path) {
		if candidate.Item.CallID == "call_x" && candidate.Item.Type == "commandExecution" {
			indexedCalls = append(indexedCalls, candidate.Item.ArgumentsJSON)
		}
	}
	if !reflect.DeepEqual(indexedCalls, wantCalls) {
		t.Errorf("index call arguments = %v, want %v", indexedCalls, wantCalls)
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
		t.Errorf("full read call arguments = %v, want %v: a reused call id is two calls", calls, wantCalls)
	}
}

// TestSettledCallKeepsAnnouncementRound covers the round a settled call must
// keep. A stamped announcement carries the assistant entry's RoundID; the
// result entry carries only the call id, so a merge that does not inherit the
// round drops it and a client grouping by round loses the call's round once it
// settles.
func TestSettledCallKeepsAnnouncementRound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "settled_round"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	tc := llm.ToolCallData{ID: "call_r", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	announce := schema.Turn{Kind: schema.TurnAssistant, RoundID: "r_announce",
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &tc}}}}
	w.BeginExecution("t_exec", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	record(announce, transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_r", "read_file", "out", false)), transcript.PlaceSession)
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
	var settled *appwire.ThreadItem
	for i := range turns {
		for j := range turns[i].Items {
			if turns[i].Items[j].CallID == "call_r" {
				settled = &turns[i].Items[j]
			}
		}
	}
	if settled == nil {
		t.Fatalf("full read turns = %+v, want the settled call", shapesFromTurns(turns))
	}
	if settled.RoundID != "r_announce" {
		t.Errorf("settled call round = %q, want %q: the result carries no round of its own", settled.RoundID, "r_announce")
	}
	if settled.Output != "out" {
		t.Errorf("settled call output = %q, want %q: the result must still settle the call", settled.Output, "out")
	}
}

// TestLegacySplitPreludeReplaysAsFragments covers a transcript written before
// the writer ended the prelude's span at a delivery: a prelude-stamped startup
// hook, an idle delivery, then a model switch carrying the same prelude id.
// The writer change only affects new appends, so replay is the only place this
// shape can be observed. The index reports one summary for the id; a full read
// that emits two turns with that id and calls both "full" tells a client two
// complete turns exist where the index has one.
func TestLegacySplitPreludeReplaysAsFragments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "legacy_split_prelude"})
	if err != nil {
		t.Fatal(err)
	}
	// PlaceVerbatim records each entry with the id it carries, reproducing a
	// file whose prelude spans a delivery.
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record(stamp(newHookTurn("SessionStart"), appwire.SystemPreludeTurnID, schema.TurnSpanPrelude))
	record(stamp(schema.NewTurn(schema.TurnSteering, llm.User("attention")), "turn_delivery", schema.TurnSpanDelivery))
	record(stamp(schema.NewTurn(schema.TurnModelSwitch, llm.System("switch")), appwire.SystemPreludeTurnID, schema.TurnSpanPrelude))
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
	// The id must not name two full turns. Either the runs coalesce into one
	// turn or each is marked a fragment — never two "full" turns with one id.
	seen := 0
	for _, turn := range turns {
		if turn.ID != appwire.SystemPreludeTurnID {
			continue
		}
		seen++
		if turn.ItemsView != appwire.TurnItemsViewFragment && seen > 1 {
			t.Errorf("turn %q appears %d times and one is %q, want fragments: a turn id names one turn",
				turn.ID, seen, turn.ItemsView)
		}
	}
	if seen == 0 {
		t.Fatalf("full read turns = %+v, want the prelude", shapesFromTurns(turns))
	}
	// Chronology must still hold across whatever it emits.
	candidates, err := appitempaging.CandidatesFromTurns(turns)
	if err != nil {
		t.Fatalf("CandidatesFromTurns: %v", err)
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		t.Fatalf("full read candidates rejected: %v", err)
	}
}

// TestTwoAnnouncementsInOneEntryStayDistinct covers one assistant entry whose
// message records two tool calls with the SAME provider id. The index keeps
// every recorded part and settles a result against the first part carrying the
// id (build.go's applyResults); a full read that merges the second
// announcement into the first replaces the first call's arguments before
// either result is applied (#1166 review).
func TestTwoAnnouncementsInOneEntryStayDistinct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "same_entry_dup"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	first := llm.ToolCallData{ID: "call_dup", Name: "read_file", Arguments: json.RawMessage(`{"n":1}`)}
	second := llm.ToolCallData{ID: "call_dup", Name: "read_file", Arguments: json.RawMessage(`{"n":2}`)}
	announce := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &first},
		{Kind: llm.ContentToolCall, ToolCall: &second},
	}}}
	w.BeginExecution("t_exec", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	record(announce, transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_dup", "read_file", "out", false)), transcript.PlaceSession)
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
	var args, statuses []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_dup" && item.Type == "commandExecution" {
				args = append(args, item.ArgumentsJSON)
				statuses = append(statuses, item.Status)
			}
		}
	}
	want := []string{`{"n":1}`, `{"n":2}`}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("full read call arguments = %v, want %v: each recorded part is its own announcement", args, want)
	}
	// The result settles the FIRST recorded part, as the index does. Arguments
	// alone cannot see this: both are right even when the result lands on the
	// wrong call, which is why the defect survived the first version of this
	// test (#1166 review).
	if len(statuses) != 2 {
		t.Fatalf("call items = %v, want two announcements", statuses)
	}
	if statuses[0] == appwire.TurnStatusInProgress {
		t.Errorf("first announcement status = %q, want the result to settle the FIRST recorded part", statuses[0])
	}
	if statuses[1] != appwire.TurnStatusInProgress {
		t.Errorf("second announcement status = %q, want it left untouched", statuses[1])
	}
}

// TestSplitExecutionRepeatsOneUsageTotal covers a delivery that splits a
// stamped execution whose rounds record usage on both sides. The index keeps
// one summary per turn id and reports the execution's total; a full read that
// stamps each fragment from its own entries understates the turn on both. Turn
// metadata is computed per execution, never per fragment (#1166 review).
func TestSplitExecutionRepeatsOneUsageTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "split_usage"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	firstRound := schema.NewTurn(schema.TurnUserInput, llm.User("go"))
	firstRound.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	record(stamp(firstRound, "t_exec", schema.TurnSpanExecution))
	record(stamp(schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved")), "turn_delivery", schema.TurnSpanDelivery))
	secondRound := schema.NewTurn(schema.TurnAssistant, llm.Assistant("done"))
	secondRound.Usage = llm.Usage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140}
	record(stamp(secondRound, "t_exec", schema.TurnSpanExecution))
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
	// Every fragment of the execution reports the execution's total, as the
	// index does, so a client reading either one sees the real cost.
	var fragments int
	for _, turn := range turns {
		if turn.ID != "t_exec" {
			continue
		}
		fragments++
		if turn.Usage == nil {
			t.Fatalf("turn %q fragment has no usage, want the execution total", turn.ID)
		}
		if turn.Usage.InputTokens != 110 || turn.Usage.OutputTokens != 45 {
			t.Errorf("turn %q usage input=%d output=%d, want input=110 output=45: metadata is per execution, not per fragment",
				turn.ID, turn.Usage.InputTokens, turn.Usage.OutputTokens)
		}
	}
	if fragments == 0 {
		t.Fatalf("full read turns = %+v, want the split execution", shapesFromTurns(turns))
	}
}

// TestLegacySplitPreludeMarksBothBoundaries covers BOTH continuation flags on a
// prelude split by a delivery. The first run is not "full" (the turn continues)
// and the second is not "earlier-less" (the turn started before it). A client
// reads these flags to decide whether to page further, so a run that omits them
// advertises completeness it does not have (#1166 review).
func TestLegacySplitPreludeMarksBothBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "split_flags"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record(stamp(newHookTurn("SessionStart"), appwire.SystemPreludeTurnID, schema.TurnSpanPrelude))
	record(stamp(schema.NewTurn(schema.TurnSteering, llm.User("attention")), "turn_delivery", schema.TurnSpanDelivery))
	record(stamp(schema.NewTurn(schema.TurnModelSwitch, llm.System("switch")), appwire.SystemPreludeTurnID, schema.TurnSpanPrelude))
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
	var runs []appwire.Turn
	for _, turn := range turns {
		if turn.ID == appwire.SystemPreludeTurnID {
			runs = append(runs, turn)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("prelude runs = %+v, want the id split into two fragments", shapesFromTurns(turns))
	}
	if runs[0].ItemsView != appwire.TurnItemsViewFragment || !runs[0].HasLaterItems || runs[0].HasEarlierItems {
		t.Errorf("first prelude run = %+v, want a fragment with later and no earlier", runs[0])
	}
	if runs[1].ItemsView != appwire.TurnItemsViewFragment || !runs[1].HasEarlierItems || runs[1].HasLaterItems {
		t.Errorf("second prelude run = %+v, want a fragment with earlier and no later", runs[1])
	}
}

// TestDeliverySplitSettlesTheAnnouncement covers a standalone delivery landing
// between a stamped tool announcement and its result. The index keeps one
// settled item at the announcement, because its summary spans the runs. A
// replay that settles each run independently leaves the announcement in
// progress and emits the result as a SECOND item with no arguments and no
// round. Settlement is a property of the execution, not of a run (#1166
// review).
func TestDeliverySplitSettlesTheAnnouncement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "delivery_split_settle"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	call := llm.ToolCallData{ID: "call_d", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	announce := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
	announce.RoundID = "r_announce"
	record(stamp(announce, "t_exec", schema.TurnSpanExecution))
	record(stamp(schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved")), "turn_delivery", schema.TurnSpanDelivery))
	record(stamp(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_d", "read_file", "out", false)), "t_exec", schema.TurnSpanExecution))
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
	var calls []appwire.ThreadItem
	for _, turn := range turns {
		if turn.ID != "t_exec" {
			continue
		}
		for _, item := range turn.Items {
			if item.CallID == "call_d" {
				calls = append(calls, item)
			}
		}
	}
	if len(calls) != 1 {
		t.Fatalf("execution call items = %d (%+v), want one settled item at the announcement", len(calls), calls)
	}
	if calls[0].Status == appwire.TurnStatusInProgress {
		t.Errorf("settled call status = %q, want the result to settle it", calls[0].Status)
	}
	if calls[0].ArgumentsJSON != `{"path":"a"}` {
		t.Errorf("settled call arguments = %q, want the announcement's", calls[0].ArgumentsJSON)
	}
	if calls[0].RoundID != "r_announce" {
		t.Errorf("settled call round = %q, want %q", calls[0].RoundID, "r_announce")
	}
	// The item that was ADMITTED keeps its identity. The client already holds
	// this ID, so a settled call that renames itself to the result entry's ID
	// is a different item to every reader that reconciles by identity.
	if want := "item_tool_1_0"; calls[0].ID != want {
		t.Errorf("settled call id = %q, want the announcement's %q", calls[0].ID, want)
	}
	// An attention resolution is transcript-only bookkeeping: it projects no
	// item, so it shows no visible turn. That matches the index, which creates
	// no candidate for it — the point here is only that it must not orphan the
	// result of the execution it split.
	for _, turn := range turns {
		if turn.ID == "turn_delivery" {
			t.Errorf("full read turns = %+v, want no visible turn for an itemless delivery", shapesFromTurns(turns))
		}
	}
}

// TestLegacyGroupSharingAStampedIDKeepsItsOwnItems covers a legacy group whose
// id a later stamped execution also uses. Membership, not an id match, decides
// which path a group takes: a legacy group is not a run of the stamped
// execution, so it must render its own items rather than be dropped and have
// the stamped run emitted twice (#1166 review).
func TestLegacyGroupSharingAStampedIDKeepsItsOwnItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "legacy_shares_id"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	// A legacy user turn whose id matches the stamped execution's.
	legacy := schema.NewTurn(schema.TurnUserInput, llm.User("legacy question"))
	legacy.StableTurnID = "turn_shared"
	record(legacy)
	// Another group between them, so the two are not adjacent.
	record(schema.NewTurn(schema.TurnHookCompleted, llm.System("hook ran")))
	// The stamped execution reusing that id.
	stamped := schema.NewTurn(schema.TurnUserInput, llm.User("stamped question"))
	stamped.Format = schema.TurnFormatIdentity
	stamped.TurnID = "turn_shared"
	stamped.TurnKind = schema.TurnSpanExecution
	record(stamped)
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
	// The defect this covers dropped the legacy group's item and emitted the
	// stamped run twice. Both are structural facts, so the check is structure:
	// the turns in order, and the item identity each source entry produced,
	// counted rather than compared as prose. The userMessage items carry the
	// two questions and the hook completion is the systemMessage between them.
	var shapes []string
	for _, turn := range turns {
		shapes = append(shapes, turn.ID)
	}
	// The legacy group, the hook, and the stamped execution are three turns,
	// and the legacy one comes first because the file puts it first.
	if len(shapes) < 3 {
		t.Fatalf("full read turn ids = %v, want the legacy group, the hook and the stamped execution", shapes)
	}
	var legacyUser, stampedUser, hooks int
	for at, turn := range turns {
		for _, item := range turn.Items {
			switch item.Type {
			case "userMessage":
				// The legacy group is the turn that precedes the hook, so it is
				// the first turn carrying a userMessage; the stamped execution
				// is the later one.
				if at == 0 {
					legacyUser++
				} else {
					stampedUser++
				}
			case "systemMessage":
				if item.EventKind == appwire.ThreadItemEventKindHookCompleted {
					hooks++
				}
			}
		}
	}
	// Each source entry's item appears exactly ONCE. The regression emitted the
	// stamped question twice and dropped the legacy one, which this count sees
	// without reading the text.
	if legacyUser != 1 {
		t.Errorf("legacy userMessage items = %d, want 1: the legacy group is not a run of the stamped execution", legacyUser)
	}
	if stampedUser != 1 {
		t.Errorf("stamped userMessage items = %d, want 1: the stamped run is emitted once", stampedUser)
	}
	if hooks != 1 {
		t.Errorf("hook completion items = %d, want 1", hooks)
	}
}

// TestInterruptedCompletionInterruptsItsCall covers a crash that recovery
// closed with an interrupted completion while a tool call was still awaiting a
// result. The completion record projects no item, but it decides the state the
// execution ended in: the turn is interrupted AND the unanswered call is
// interrupted. Deriving status from the remaining entries instead reported the
// turn completed while the call still read in progress, which is the pair a
// client reads to decide whether to keep waiting (#1166 review).
func TestInterruptedCompletionInterruptsItsCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "crash_recovered"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	tc := llm.ToolCallData{ID: "call_crashed", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	w.BeginExecution("t_crash", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("go")), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &tc}}}}, transcript.PlaceSession)
	// The process died here: no result was ever recorded for call_crashed.
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnInterrupted}}, transcript.PlaceCompletion)
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
	var turn *appwire.Turn
	for i := range turns {
		if turns[i].ID == "t_crash" {
			turn = &turns[i]
		}
	}
	if turn == nil {
		t.Fatalf("full read turns = %+v, want the crashed execution", shapesFromTurns(turns))
	}
	if turn.Status != appwire.TurnStatusInterrupted {
		t.Errorf("execution status = %q, want %q: the completion record says it was interrupted",
			turn.Status, appwire.TurnStatusInterrupted)
	}
	var call *appwire.ThreadItem
	for i := range turn.Items {
		if turn.Items[i].CallID == "call_crashed" {
			call = &turn.Items[i]
		}
	}
	if call == nil {
		t.Fatalf("execution items = %+v, want the unanswered call", turn.Items)
	}
	if call.Status != appwire.TurnStatusInterrupted {
		t.Errorf("unanswered call status = %q, want %q: nothing else was coming for it",
			call.Status, appwire.TurnStatusInterrupted)
	}
}

// TestDeliveryBeforePreludeMarksTheHeaderAsAFragment covers an OLDER transcript
// the current writer can no longer produce: a header prompt, a standalone
// attention delivery, then the first entry stamped `turn_system`. The header
// cannot merge into that later run without putting position 0 after the
// delivery, so it is emitted first — but it is not a complete turn. Marked
// full, it advertised a turn the index keeps open, and a client keyed by turn
// id saw two `turn_system` turns where the index reports parts of one
// (#1166 review).
func TestDeliveryBeforePreludeMarksTheHeaderAsAFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "old_prelude", SystemPrompt: "header prompt"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record(stamp(schema.NewTurn(schema.TurnSteering, llm.User("attention")), "turn_delivery", schema.TurnSpanDelivery))
	record(stamp(newHookTurn("SessionStart"), appwire.SystemPreludeTurnID, schema.TurnSpanPrelude))
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
	var runs []appwire.Turn
	for _, turn := range turns {
		if turn.ID == appwire.SystemPreludeTurnID {
			runs = append(runs, turn)
		}
	}
	if len(runs) == 0 {
		t.Fatalf("full read turns = %+v, want the header and the stamped prelude", shapesFromTurns(turns))
	}
	// The turn id must name ONE turn's fragments, not two complete turns: every
	// run of the prelude is a fragment, the first says the turn goes on, and
	// each later one says it started earlier.
	for at, run := range runs {
		if run.ItemsView != appwire.TurnItemsViewFragment {
			t.Errorf("prelude run %d ItemsView = %q, want %q: one id must not name two complete turns",
				at, run.ItemsView, appwire.TurnItemsViewFragment)
		}
		if at == 0 && !run.HasLaterItems {
			t.Errorf("prelude run %d HasLaterItems = false, want true: the turn continues", at)
		}
		if at > 0 && !run.HasEarlierItems {
			t.Errorf("prelude run %d HasEarlierItems = false, want true: the prompt came first", at)
		}
	}
	// Chronology is what item paging validates: position 0 must be first.
	if got := turns[0].ID; got != appwire.SystemPreludeTurnID {
		// The delivery opens the file, and an itemless delivery projects no
		// turn, so the first emitted turn is the header at position 0.
		t.Errorf("first emitted turn = %q, want the header at position 0", got)
	}
	if err := appitempaging.ValidateCandidates(indexCandidates(t, path)); err != nil {
		t.Errorf("bounded index candidates rejected: %v", err)
	}
}

// TestAdjacentLegacyGroupDoesNotAbsorbAStampedExecution covers a legacy group
// immediately followed by a stamped execution with the same id, with no hook
// between them. Joining on the id alone appended the stamped entry to the
// legacy group, whose `stamped` flag stayed false — so the execution fell back
// to the legacy call-id merge and a reused call id collapsed two announcements
// into one. The index gives the stamped opener its own summary (#1166 review).
func TestAdjacentLegacyGroupDoesNotAbsorbAStampedExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "adjacent_shapes"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	// A legacy user turn, then the stamped execution reusing its id ADJACENTLY.
	legacy := schema.NewTurn(schema.TurnUserInput, llm.User("legacy question"))
	legacy.StableTurnID = "turn_shared"
	record(legacy)
	record(stamp(schema.NewTurn(schema.TurnUserInput, llm.User("stamped question")), "turn_shared", schema.TurnSpanExecution))
	// Two rounds of the same execution reusing one call id, with a result for
	// each. Only the stamped path keeps both announcements.
	call := func(args string) schema.Turn {
		tc := llm.ToolCallData{ID: "call_s", Name: "read_file", Arguments: json.RawMessage(args)}
		return stamp(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &tc}}}}, "turn_shared", schema.TurnSpanExecution)
	}
	record(call(`{"round":1}`))
	record(stamp(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_s", "read_file", "first-out", false)), "turn_shared", schema.TurnSpanExecution))
	record(call(`{"round":2}`))
	record(stamp(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_s", "read_file", "second-out", false)), "turn_shared", schema.TurnSpanExecution))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// The index keeps both announcements: that is the reference.
	wantRounds := []string{`{"round":1}`, `{"round":2}`}
	var indexed []string
	for _, candidate := range indexCandidates(t, path) {
		if candidate.Item.CallID == "call_s" && candidate.Item.Type == "commandExecution" {
			indexed = append(indexed, candidate.Item.ArgumentsJSON)
		}
	}
	if !reflect.DeepEqual(indexed, wantRounds) {
		t.Fatalf("index call arguments = %v, want %v", indexed, wantRounds)
	}

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
			if item.CallID == "call_s" && item.Type == "commandExecution" {
				full = append(full, item.ArgumentsJSON)
			}
		}
	}
	// The stamped execution keeps BOTH announcements, as the index does. A
	// legacy group that absorbed it would merge them by call id and lose one.
	if !reflect.DeepEqual(full, wantRounds) {
		t.Errorf("full read call arguments = %v, want %v: a stamped execution keeps its own settlement rules", full, wantRounds)
	}
	// And the legacy turn keeps its own item.
	found := false
	for _, turn := range turns {
		if turn.ID != "turn_shared" {
			continue
		}
		for _, item := range turn.Items {
			if item.Type == "userMessage" && item.Text != "" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("full read turns = %+v, want the legacy group to keep its item", shapesFromTurns(turns))
	}
}

// TestItemlessDeliveryDoesNotSplitTheVisibleTurn covers an execution split by an
// entry that projects no item. Its displayed items are adjacent, so it is ONE
// turn: emitting two fragments carried continuation flags that promised items
// nothing delivered, and a paging client asked for a page that holds nothing but
// the same turn again (#1166 review).
func TestItemlessDeliveryDoesNotSplitTheVisibleTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "itemless_split"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record(stamp(schema.NewTurn(schema.TurnUserInput, llm.User("go")), "t_exec", schema.TurnSpanExecution))
	record(stamp(schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved")), "turn_delivery", schema.TurnSpanDelivery))
	record(stamp(schema.NewTurn(schema.TurnAssistant, llm.Assistant("done")), "t_exec", schema.TurnSpanExecution))
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
	var runs []appwire.Turn
	for _, turn := range turns {
		if turn.ID == "t_exec" {
			runs = append(runs, turn)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("execution turns = %+v, want ONE turn: its visible items are adjacent", shapesFromTurns(turns))
	}
	// The joined turn is complete: nothing precedes or follows it.
	if runs[0].ItemsView != appwire.TurnItemsViewFull {
		t.Errorf("execution ItemsView = %q, want %q: the delivery projected no item", runs[0].ItemsView, appwire.TurnItemsViewFull)
	}
	if runs[0].HasEarlierItems || runs[0].HasLaterItems {
		t.Errorf("execution continuation flags earlier=%v later=%v, want both false",
			runs[0].HasEarlierItems, runs[0].HasLaterItems)
	}
	if len(runs[0].Items) != 2 {
		t.Errorf("execution items = %d, want both of its visible items", len(runs[0].Items))
	}
}
