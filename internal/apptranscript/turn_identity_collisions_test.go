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

func TestResultSettlesAnnouncementAcrossDeliverySplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "mid_execution_delivery"})
	if err != nil {
		t.Fatal(err)
	}
	call := func() schema.Turn {
		tc := llm.ToolCallData{ID: "call_x", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
		return schema.Turn{Kind: schema.TurnAssistant, RoundID: "r_announce", Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &tc}}}}
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	record(stamp(schema.NewTurn(schema.TurnUserInput, llm.User("go")), "t_exec", schema.TurnSpanExecution))
	record(stamp(call(), "t_exec", ""))
	// A cold writer's delivery, between the announcement and its result.
	record(stamp(schema.NewTurn(schema.TurnSteering, llm.User("cold")), "turn_delivery_1", schema.TurnSpanDelivery))
	record(stamp(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_x", "read_file", "out", false)), "t_exec", ""))
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
	// The result settled the announcement: one item carries both the
	// arguments and the output, and no second item lacks the arguments.
	var calls []appwire.ThreadItem
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_x" && item.Type == "commandExecution" {
				calls = append(calls, item)
			}
		}
	}
	if len(calls) != 1 {
		t.Fatalf("full read call items = %d (%+v), want 1 settled call", len(calls), calls)
	}
	if calls[0].ArgumentsJSON != `{"path":"a"}` || calls[0].Output != "out" {
		t.Errorf("settled call arguments=%q output=%q, want the announcement's arguments and the result's output", calls[0].ArgumentsJSON, calls[0].Output)
	}
	// The settled call keeps the ANNOUNCEMENT's identity. The announcement is
	// the item a client already saw, and the index never replaces it: a result
	// adds a contributor to the item at the announcing part's position
	// (transcriptindex build.go's applyResults -> addContributor), so ID and
	// RoundID stay the announcement's. A merge must not hand the item the
	// result entry's own identity, or a client reconciling by item id sees one
	// item disappear and another appear for the same call (#1166 review).
	if calls[0].ID != "item_tool_2_0" || calls[0].RoundID != "r_announce" {
		t.Errorf("settled call identity id=%q round=%q, want the announcement's item_tool_2_0/r_announce", calls[0].ID, calls[0].RoundID)
	}
	// The delivery keeps a turn of its own.
	var delivery appwire.Turn
	for _, turn := range turns {
		if turn.ID == "turn_delivery_1" {
			delivery = turn
		}
	}
	if delivery.ID == "" || len(delivery.Items) != 1 || delivery.Items[0].Type != "steering" {
		t.Errorf("delivery turn = %+v, want one steering item of its own", delivery)
	}
}

// TestDuplicateAnnouncementInOneEntrySettlesFirst covers one assistant entry
// that announces the same provider call id twice. The index projects an item
// per announcing part, so both announcements stay, and it attaches results to
// the FIRST matching part of the awaiting entry, so the first item carries
// both results. The full read must make the same choice: the second
// announcement keeps its own arguments instead of receiving the first's
// result, and the first keeps the arguments the result settled (#1166
// review).
// TestHiddenAnnouncementAdvancesAwaitingLookup covers a provider that reuses a
// completed read_file call id for a later communicate. The assistant entry
// announcing the communicate projects NO item — every communicate call is
// deferred to its result — and that result failed prevalidation, so the
// projector renders it as a standalone error item without an announcement.
//
// The index advances its awaiting entry from the assistant ENTRY whenever the
// entry carries a tool call (transcriptindex build.go's recordTurn: "if
// r.Kind != turnKindLegacy && entry.Kind == schema.TurnAssistant &&
// hasToolCall(entry)"), whether or not that call projects an item. Its lookup
// therefore stops pointing at the read_file announcement, and the failed
// communicate result attaches to nothing. The full read must do the same: a
// lookup advanced only by VISIBLE announcements still points at read_file and
// would merge the communicate's error onto the earlier call, replacing its
// name, arguments and completed status (#1166 review).
func TestHiddenAnnouncementAdvancesAwaitingLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "hidden_announcement"})
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	readCall := llm.ToolCallData{ID: "call_reuse", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	commCall := llm.ToolCallData{ID: "call_reuse", Name: "communicate", Arguments: json.RawMessage(`{"message":"hi"}`)}
	record(stamp(schema.NewTurn(schema.TurnUserInput, llm.User("go")), "t_exec", schema.TurnSpanExecution))
	record(stamp(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &readCall},
	}}}, "t_exec", ""))
	record(stamp(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_reuse", "read_file", "read-out", false)), "t_exec", ""))
	// A later round reuses the id for a communicate, which projects no item.
	// It has to be an INHERITED announcement: only that defers a communicate
	// through the registry, and only then does the rejected result project an
	// item of its own for the merge to reach.
	commTurn := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &commCall},
	}}}
	commTurn.Authorship = &schema.TranscriptAuthorship{Inherited: true}
	record(stamp(commTurn, "t_exec", ""))
	record(stamp(schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_reuse", Name: "communicate", Content: "bad args", IsError: true, PrevalOnly: true}},
	}}), "t_exec", ""))
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
	var readItems []appwire.ThreadItem
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_reuse" && item.ToolName == "read_file" {
				readItems = append(readItems, item)
			}
		}
	}
	if len(readItems) != 1 {
		t.Fatalf("read_file items = %d (%+v), want 1", len(readItems), readItems)
	}
	// The earlier call survives intact: the communicate's error must not
	// overwrite the read_file announcement it happens to share an id with.
	if readItems[0].ToolName != "read_file" || readItems[0].ArgumentsJSON != `{"path":"a"}` ||
		readItems[0].Output != "read-out" || readItems[0].Status != appwire.TurnStatusCompleted {
		t.Errorf("read_file call = name=%q args=%q output=%q status=%q, want the earlier completed call untouched",
			readItems[0].ToolName, readItems[0].ArgumentsJSON, readItems[0].Output, readItems[0].Status)
	}
}

// TestSplitExecutionSharesOneMetadata covers a cold delivery landing inside an
// execution that then fails. The execution's entries land on two runs of one
// stamped id, with the delivery between them. The index holds ONE summary per
// turn id, so both of its fragments carry the execution's whole status and
// whole usage; a full read that stamped each run from its own local entries
// would call the pre-delivery run completed on partial usage while the turn
// actually failed, and a client comparing the two readers would see a turn
// that succeeded in one and failed in the other (#1166 review).
func TestSplitExecutionSharesOneMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "split_execution_metadata"})
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	tc := llm.ToolCallData{ID: "call_split", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	goTurn := stamp(schema.NewTurn(schema.TurnUserInput, llm.User("go")), "t_exec", schema.TurnSpanExecution)
	goTurn.Usage = llm.Usage{InputTokens: 10, OutputTokens: 1, TotalTokens: 11}
	record(goTurn)
	callTurn := stamp(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &tc},
	}}}, "t_exec", "")
	callTurn.Usage = llm.Usage{InputTokens: 20, OutputTokens: 2, TotalTokens: 22}
	record(callTurn)
	// A cold writer's delivery, splitting the execution.
	record(stamp(schema.NewTurn(schema.TurnSteering, llm.User("cold")), "turn_delivery_2", schema.TurnSpanDelivery))
	// The execution fails.
	failTurn := stamp(schema.NewTurn(schema.TurnFailure, llm.User("boom")), "t_exec", "")
	failTurn.Usage = llm.Usage{InputTokens: 30, OutputTokens: 3, TotalTokens: 33}
	record(failTurn)
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
		if turn.ID == "t_exec" && len(turn.Items) > 0 {
			runs = append(runs, turn)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("t_exec runs = %d (%+v), want the execution split by the delivery", len(runs), runs)
	}
	// Every visible run carries the execution's whole metadata, so no run
	// advertises a completed turn that in fact failed, and no run reports a
	// share of the usage that the index reports as a total.
	for i, run := range runs {
		if run.Status != appwire.TurnStatusFailed {
			t.Errorf("run %d status = %q, want the execution's failed status", i, run.Status)
		}
		if run.Usage == nil {
			t.Fatalf("run %d usage = nil, want the execution's total", i)
		}
		if got := run.Usage.InputTokens; got != 60 {
			t.Errorf("run %d input tokens = %d, want the execution's total 60", i, got)
		}
	}
}

// TestItemlessDeliveryKeepsOneVisibleRun covers a stamped delivery that
// projects no item at all, landing between two visible runs of one execution
// (an attention-resolution entry with no text). The index has no candidate for
// the delivery, so its items of the execution are one contiguous run; the full
// read must not emit the execution as two adjacent turns sharing an id, which
// a client reconciling by turn id would read as one overwriting the other
// (#1166 review).
func TestItemlessDeliveryKeepsOneVisibleRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "itemless_delivery"})
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	record(stamp(schema.NewTurn(schema.TurnUserInput, llm.User("go")), "t_split", schema.TurnSpanExecution))
	// An attention resolution is a real delivery turn that projects NO item:
	// it is transcript-only bookkeeping (agent/schema/turn.go's TranscriptOnly
	// includes TurnAttentionResolution in the kinds that project nothing), and
	// the writer stamps it as a delivery of its own (PlaceAsync).
	itemless := schema.NewTurn(schema.TurnAttentionResolution, llm.System("Attention resolved."))
	itemless.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "att_1", Disposition: "resume"}
	record(stamp(itemless, "turn_delivery_empty", schema.TurnSpanDelivery))
	record(stamp(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "done"},
	}}}, "t_split", ""))
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
	full := shapesFromTurns(turns)
	indexed := shapesFromCandidates(indexCandidates(t, path))
	if !reflect.DeepEqual(full, indexed) {
		t.Fatalf("full read turn structure = %+v, want the index's %+v", full, indexed)
	}
}

// TestLegacySiblingGroupsStayWhole covers a legacy transcript where a
// standalone entry (a hook completion) closes one group and an explicitly
// owned steering entry later reopens a group with the SAME id. Those are two
// legacy groups, and the index keeps a separate summary for each — no summary
// spans them — so each is a whole turn to the bounded reader. A full read that
// marked them partial because the id repeats would tell a client a turn
// continues where the index says it ends (#1166 review).
func TestLegacySiblingGroupsStayWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "legacy_siblings"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	user := schema.NewTurn(schema.TurnUserInput, llm.User("go"))
	user.StableTurnID = "turn_a"
	record(user)
	hook := schema.NewTurn(schema.TurnHookCompleted, llm.System("hook ran"))
	hook.Hook = &schema.HookInfo{Event: "Stop"}
	record(hook)
	// An explicitly owned steering entry reopens the SAME id after the hook
	// closed the group.
	steering := schema.NewTurn(schema.TurnSteering, llm.User("again"))
	steering.OwningTurnID = "turn_a"
	record(steering)
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
	full := shapesFromTurns(turns)
	indexed := shapesFromCandidates(indexCandidates(t, path))
	if !reflect.DeepEqual(full, indexed) {
		t.Fatalf("full read turn structure = %+v, want the index's %+v", full, indexed)
	}
	// No legacy group claims to continue another: the index has one summary
	// per group and reports each whole.
	for _, turn := range turns {
		if turn.HasEarlierItems || turn.HasLaterItems {
			t.Errorf("legacy turn %q earlier=%v later=%v view=%q, want a whole turn",
				turn.ID, turn.HasEarlierItems, turn.HasLaterItems, turn.ItemsView)
		}
	}
}

func TestDuplicateAnnouncementInOneEntrySettlesFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "duplicate_announcement"})
	if err != nil {
		t.Fatal(err)
	}
	first := llm.ToolCallData{ID: "call_dup", Name: "read_file", Arguments: json.RawMessage(`{"which":"first"}`)}
	second := llm.ToolCallData{ID: "call_dup", Name: "read_file", Arguments: json.RawMessage(`{"which":"second"}`)}
	assistant := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &first},
		{Kind: llm.ContentToolCall, ToolCall: &second},
	}}}
	results := schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_dup", Name: "read_file", Content: "first-out"}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_dup", Name: "read_file", Content: "second-out"}},
	}}}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	for _, turn := range []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("go")},
		assistant,
		results,
	} {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = "t_exec"
		record(turn)
	}
	// The first entry of the turn records its kind.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reg := apptranscript.NewToolCallRegistry()
	var calls []appwire.ThreadItem
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_dup" && item.Type == "commandExecution" {
				calls = append(calls, item)
			}
		}
	}
	// The index's shape is the expectation: one item per announcing part, both
	// result parts contributed to the first.
	var wantArgs, wantOutputs []string
	for _, candidate := range indexCandidates(t, path) {
		if candidate.Item.CallID != "call_dup" || candidate.Item.Type != "commandExecution" {
			continue
		}
		if candidate.Item.ArgumentsJSON != "" {
			wantArgs = append(wantArgs, candidate.Item.ArgumentsJSON)
		}
		if candidate.Item.Output != "" {
			wantOutputs = append(wantOutputs, candidate.Item.Output)
		}
	}
	var gotArgs, gotOutputs []string
	for _, call := range calls {
		if call.ArgumentsJSON != "" {
			gotArgs = append(gotArgs, call.ArgumentsJSON)
		}
		if call.Output != "" {
			gotOutputs = append(gotOutputs, call.Output)
		}
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("full read call arguments = %v, want the index's %v", gotArgs, wantArgs)
	}
	if !reflect.DeepEqual(gotOutputs, wantOutputs) {
		t.Errorf("full read call outputs = %v, want the index's %v", gotOutputs, wantOutputs)
	}
}

// TestSettledProvenanceNamesAnnouncementIdentity covers the order of the two
// decisions a settlement makes: which identity the settled item keeps, and
// what its provenance is derived from. MergeThreadItems derives
// ConversationProvenance.SourceItemID from the id the merged item will carry,
// so the identity has to be established BEFORE the merge. Restoring the
// announcement's id afterwards leaves the provenance naming the result entry's
// own item — an id this read never emits — and a client that follows
// SourceItemID lands on an item that does not exist (#1166 review).
//
// Only ask_user announcements carry provenance (apptranscript.go's
// `call.Name == "ask_user" && p != nil`), so this is the one settlement where
// the derived value is observable.
func TestSettledProvenanceNamesAnnouncementIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "settled_provenance"})
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(turn schema.Turn, id string, span schema.TurnSpanKind) schema.Turn {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = id
		turn.TurnKind = span
		return turn
	}
	record := func(turn schema.Turn) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	origin := &schema.ConversationOutputProvenance{Version: 1, InstanceID: "instance", RunGeneration: 9, RequestID: "request-a", FrontierID: "frontier-a"}
	ask := schema.Turn{
		Kind:                   schema.TurnAssistant,
		ConversationProvenance: origin,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_ask", Name: "ask_user", Arguments: json.RawMessage(`{"private":"original"}`)}},
		}},
	}
	result := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_ask", "ask_user", "ack", false))
	record(stamp(schema.NewTurn(schema.TurnUserInput, llm.User("go")), "t_ask", schema.TurnSpanExecution))
	record(stamp(ask, "t_ask", ""))
	record(stamp(result, "t_ask", ""))
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
	var settled []appwire.ThreadItem
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.CallID == "call_ask" && item.Status != appwire.TurnStatusInProgress {
				settled = append(settled, item)
			}
		}
	}
	if len(settled) != 1 {
		t.Fatalf("settled ask_user items = %d (%+v), want the result settled into the announcement", len(settled), settled)
	}
	item := settled[0]
	p := item.ConversationProvenance
	if p == nil {
		t.Fatalf("settled item lost the announcement's provenance: %+v", item)
	}
	// The identity the client reconciles on is the announcement's.
	if item.ID == "" {
		t.Fatalf("settled item id = %q, want the announcement's identity", item.ID)
	}
	// And the provenance must name THAT item, because it is derived from the
	// identity the merge saw.
	if p.SourceItemID != item.ID {
		t.Errorf("settled provenance source item = %q, want the emitted item %q: identity must be set before the merge derives from it",
			p.SourceItemID, item.ID)
	}
}
