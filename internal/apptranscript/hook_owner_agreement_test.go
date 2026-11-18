package apptranscript_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// hookOwnerTranscript writes the sequence issue #1166 names, through the real
// writer's own placements: a session-start hook completing before any turn is
// named (the fresh transcript's prelude), then a user turn whose Stop hook
// completes inside it. Every entry therefore carries the TurnID the writer
// stamped at write time — the owner both readers must honour.
func hookOwnerTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "hook_owner"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	hook := func(event string) schema.Turn {
		turn := schema.NewTurn(schema.TurnHookCompleted, llm.System("hook ran"))
		turn.Hook = &schema.HookInfo{Event: event}
		return turn
	}
	record(hook("SessionStart"), transcript.PlaceSession)
	w.BeginExecution("t_user", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("first question")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first answer")), transcript.PlaceSession)
	record(hook("Stop"), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// turnShape is one projected turn's structure: its id and the types of its
// items in order, each systemMessage qualified by its event kind. It carries
// no text and no item key, because this test is about which turn an item
// belongs to, not about wording or per-entry ordinal allocation.
type turnShape struct {
	ID    string
	Items []string
}

func itemSubject(item appwire.ThreadItem) string {
	if item.Type == "systemMessage" && item.EventKind != "" {
		return item.Type + "/" + string(item.EventKind)
	}
	return item.Type
}

// shapesFromTurns reads a whole-file projection's turns.
func shapesFromTurns(turns []appwire.Turn) []turnShape {
	shapes := make([]turnShape, 0, len(turns))
	for _, turn := range turns {
		shape := turnShape{ID: turn.ID}
		for _, item := range turn.Items {
			shape.Items = append(shape.Items, itemSubject(item))
		}
		shapes = append(shapes, shape)
	}
	return shapes
}

// shapesFromCandidates reads the bounded index's items as the live client
// receives them: turn boundaries in item arrival order, each turn's items in
// arrival order.
func shapesFromCandidates(candidates []appitempaging.TranscriptItemCandidate) []turnShape {
	var shapes []turnShape
	for _, candidate := range candidates {
		if len(shapes) == 0 || shapes[len(shapes)-1].ID != candidate.TurnID {
			shapes = append(shapes, turnShape{ID: candidate.TurnID})
		}
		last := &shapes[len(shapes)-1]
		last.Items = append(last.Items, itemSubject(candidate.Item))
	}
	return shapes
}

// The two readers of one transcript must agree about where a hook completion
// lands. The writer stamps every entry with the turn it belongs to (see
// transcript.Placement): a session-start hook completing before any turn is
// named joins the fresh transcript's prelude, and a Stop hook completing
// inside a user turn joins that execution. The whole-file full read
// (apptranscript) and the bounded index the live history publishes from
// (transcriptindex) must both place those items in exactly those turns; a
// boundary present in one and not the other reaches a client that reconciles
// live items against a cold read as a phantom turn split or merge (#1166).
func TestHookCompletionsGroupAlikeInBothProjections(t *testing.T) {
	path := hookOwnerTranscript(t)
	reg := apptranscript.NewToolCallRegistry()
	full, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	candidates := indexCandidates(t, path)

	want := []turnShape{
		{ID: appwire.SystemPreludeTurnID, Items: []string{"systemMessage/hook_completed"}},
		{ID: "t_user", Items: []string{"userMessage", "agentMessage", "systemMessage/hook_completed"}},
	}
	if got := shapesFromTurns(full); !reflect.DeepEqual(got, want) {
		t.Errorf("full read turn structure = %+v, want %+v", got, want)
	}
	if got := shapesFromCandidates(candidates); !reflect.DeepEqual(got, want) {
		t.Errorf("bounded index turn structure = %+v, want %+v", got, want)
	}
}

// indexCandidates reads the whole bounded index in arrival order.
func indexCandidates(t *testing.T, path string) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	idx, err := transcriptindex.Open(path, t.TempDir())
	if err != nil {
		t.Fatalf("transcriptindex.Open: %v", err)
	}
	defer idx.Close() //nolint:errcheck // a read-only walk
	var candidates []appitempaging.TranscriptItemCandidate
	window, err := idx.Latest(appwire.TranscriptItemPageLimit)
	for {
		if err != nil {
			t.Fatalf("index window: %v", err)
		}
		candidates = append(window.Candidates, candidates...)
		if !window.HasOlder {
			break
		}
		window, err = idx.Before(window.Candidates[0].Position, appwire.TranscriptItemPageLimit)
	}
	return candidates
}

// preludeOwnerTranscript is hookOwnerTranscript with the header content a real
// session has: a system prompt, which the full read projects as a prelude turn
// under the same id the writer stamps on a fresh transcript's first entries.
func preludeOwnerTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "prelude_owner", SystemPrompt: "You are Evener."})
	if err != nil {
		t.Fatal(err)
	}
	hook := schema.NewTurn(schema.TurnHookCompleted, llm.System("hook ran"))
	hook.Hook = &schema.HookInfo{Event: "SessionStart"}
	if _, err := w.Record(hook, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceSession}); err != nil {
		t.Fatalf("record hook: %v", err)
	}
	w.BeginExecution("t_user", false)
	if _, err := w.Record(schema.NewTurn(schema.TurnUserInput, llm.User("first question")), transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceSession}); err != nil {
		t.Fatalf("record user: %v", err)
	}
	if _, err := w.Record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first answer")), transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceSession}); err != nil {
		t.Fatalf("record assistant: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// The header prelude and a prelude-stamped entry are one turn, not two turns
// sharing an id. A fresh transcript records its pre-execution entries under
// the prelude turn, so a session-start hook that completes before any turn is
// named is stamped with the prelude's id — the same id the header's system
// prompt is projected under. Both readers must render that as a single turn
// holding the prompt and the hook; two turns with one id would make a client
// that reconciles by turn id overwrite one with the other, which is worse than
// the boundary bug this change fixes (#1166).
func TestPreludeAndStampedPreludeEntryAreOneTurn(t *testing.T) {
	path := preludeOwnerTranscript(t)
	reg := apptranscript.NewToolCallRegistry()
	full, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}

	want := []turnShape{
		{ID: appwire.SystemPreludeTurnID, Items: []string{"systemMessage/system_prompt", "systemMessage/hook_completed"}},
		{ID: "t_user", Items: []string{"userMessage", "agentMessage"}},
	}
	if got := shapesFromTurns(full); !reflect.DeepEqual(got, want) {
		t.Errorf("full read turn structure = %+v, want %+v", got, want)
	}
	if got := shapesFromCandidates(indexCandidates(t, path)); !reflect.DeepEqual(got, want) {
		t.Errorf("bounded index turn structure = %+v, want %+v", got, want)
	}
}
