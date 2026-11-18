package apptranscript_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
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
