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
	var args []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_dup" && item.Type == "commandExecution" {
				args = append(args, item.ArgumentsJSON)
			}
		}
	}
	want := []string{`{"n":1}`, `{"n":2}`}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("full read call arguments = %v, want %v: each recorded part is its own announcement", args, want)
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
