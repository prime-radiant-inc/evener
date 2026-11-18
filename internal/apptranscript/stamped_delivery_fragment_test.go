package apptranscript_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// deliveryInsideExecutionTranscript writes the shape a standalone steering
// delivery produces when it lands INSIDE a running execution, through the real
// writer's own placements: the execution names itself, records a tool call,
// another writer delivers a standalone steering turn, and the execution records
// the call's result and one more assistant entry before it completes.
//
// It is the fixture the transcript index already covers as "delivery inside an
// execution" (internal/transcriptindex/fixture_test.go), written through the
// writer so every entry carries the id the writer stamped at write time.
func deliveryInsideExecutionTranscript(t *testing.T, withUsage bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "delivery_fragment"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if withUsage && turn.Kind == schema.TurnAssistant {
			turn.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		}
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	w.BeginExecution("t_run", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("deliver")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(`{"cmd":"ls"}`)}},
	}}), transcript.PlaceSession)
	// A standalone delivery, recorded the way a cold writer records one.
	record(schema.NewTurn(schema.TurnSteering, llm.User("a delivered note")), transcript.PlaceDelivery)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: "listing"}},
	}}}, transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("after the delivery")), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fullTurns(t *testing.T, path string) []appwire.Turn {
	t.Helper()
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	return turns
}

// A delivery that lands between a call and its result does not split the call.
// The whole-file read must settle the result onto the call the execution
// announced, keeping ONE command row at the call's position, exactly as the
// index does: two rows for one call id is a client holding an in-progress call
// that never settles beside a second row that already completed.
func TestDeliveryInsideExecutionKeepsOneCommandRow(t *testing.T) {
	path := deliveryInsideExecutionTranscript(t, false)
	turns := fullTurns(t, path)

	rows := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "commandExecution" && item.CallID == "d1" {
				rows++
				if item.Status != appwire.TurnStatusCompleted {
					t.Errorf("command d1 status = %q in turn %q, want completed: the result must settle the call it announced", item.Status, turn.ID)
				}
				if item.Output == "" {
					t.Errorf("command d1 in turn %q carries no output: the result did not settle into this row", turn.ID)
				}
			}
		}
	}
	if rows != 1 {
		t.Fatalf("command d1 appears in %d rows, want 1: the full read split one call across the delivery and emitted a second row for its result", rows)
	}

	// The index is the oracle for the same transcript.
	candidates := indexCandidates(t, path)
	indexRows := 0
	for _, candidate := range candidates {
		if candidate.Item.Type == "commandExecution" && candidate.Item.CallID == "d1" {
			indexRows++
		}
	}
	if indexRows != rows {
		t.Fatalf("index kept %d command rows for d1 and the full read kept %d: the two readers disagree", indexRows, rows)
	}
}

// A delivery that lands between two runs of one execution does not halve the
// turn's usage. Every fragment of the turn reports the EXECUTION's total, which
// is what the index reports, so a client that reconciles turn metadata by id
// does not keep a later fragment's partial figure and underreport tokens.
func TestDeliveryInsideExecutionReportsWholeTurnUsage(t *testing.T) {
	path := deliveryInsideExecutionTranscript(t, true)
	turns := fullTurns(t, path)

	var fragments int
	for _, turn := range turns {
		if turn.ID != "t_run" {
			continue
		}
		fragments++
		if turn.Usage == nil {
			t.Fatalf("fragment %d of t_run carries no usage, want the execution's total", fragments)
		}
		if turn.Usage.InputTokens != 20 || turn.Usage.OutputTokens != 10 {
			t.Errorf("fragment %d of t_run usage = {in %d, out %d}, want the execution's {20 10}: a fragment reporting only its own half is a saved turn that underreports", fragments, turn.Usage.InputTokens, turn.Usage.OutputTokens)
		}
	}
	if fragments < 2 {
		t.Fatalf("t_run projected as %d fragment(s), want at least 2: the fixture did not split the execution", fragments)
	}

	// A delivery's own usage is its own: it must not absorb the execution's.
	for _, turn := range turns {
		if turn.ID == "t_run" || turn.Usage == nil {
			continue
		}
		if turn.Usage.InputTokens == 20 {
			t.Errorf("turn %q carries the execution's usage total", turn.ID)
		}
	}
}

// The delivery keeps its own position among the fragments: it is neither
// absorbed into the execution nor moved behind it.
func TestDeliveryInsideExecutionKeepsItsOwnTurn(t *testing.T) {
	path := deliveryInsideExecutionTranscript(t, false)
	turns := fullTurns(t, path)

	var ids []string
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	var seen int
	for i, id := range ids {
		if id == "t_run" {
			seen++
		}
		if i > 0 && i < len(ids)-1 && id != "t_run" {
			// The delivery sits between two fragments of the execution.
			if !reflect.DeepEqual([]string{ids[i-1], ids[i+1]}, []string{"t_run", "t_run"}) {
				t.Fatalf("turn order = %v, want the delivery between two t_run fragments", ids)
			}
		}
	}
	if seen < 2 {
		t.Fatalf("turn order = %v, want t_run recorded as two fragments split by the delivery", ids)
	}

	// Each fragment says where it sits in the turn: the first has later items,
	// the last has earlier ones, and the delivery between them has neither.
	// A client paging one fragment relies on those flags to know it does not
	// hold the whole turn.
	first, last := ids[0], ids[len(ids)-1]
	if first != "t_run" || last != "t_run" {
		t.Fatalf("turn order = %v, want t_run at both ends of the delivery", ids)
	}
	for _, turn := range turns {
		switch {
		case turn.ID == "t_run" && turn.Items[0].Type == "userMessage":
			if turn.ItemsView != appwire.TurnItemsViewFragment || !turn.HasLaterItems || turn.HasEarlierItems {
				t.Errorf("first fragment = view %q earlier=%v later=%v, want fragment with later items only", turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems)
			}
		case turn.ID == "t_run":
			if turn.ItemsView != appwire.TurnItemsViewFragment || turn.HasLaterItems || !turn.HasEarlierItems {
				t.Errorf("last fragment = view %q earlier=%v later=%v, want fragment with earlier items only", turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems)
			}
		default:
			if turn.ItemsView != appwire.TurnItemsViewFull || turn.HasEarlierItems || turn.HasLaterItems {
				t.Errorf("delivery turn %q = view %q earlier=%v later=%v, want a whole turn of its own", turn.ID, turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems)
			}
		}
	}
}
