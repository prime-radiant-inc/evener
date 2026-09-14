package appprojector

import (
	"fmt"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// A CONTEXT_COMPACTION record is its own logical turn on reload: it is one of
// the standalone kinds, so it closes the group it lands in. An announcement
// folded into the live turn that happened to be open would therefore land in a
// different turn after a restart, and the frontend, which reconciles by turn
// and transcript key, shows the surrounding items twice. The live projection
// has to group it the way the record does.
func TestAppEventProjectorContextCompactionGroupsLikeItsRecord(t *testing.T) {
	compaction := events.ContextCompactionData{
		Layer:           "checkpoint",
		TurnsBefore:     42,
		TurnsAfter:      8,
		EstTokensBefore: 120000,
		EstTokensAfter:  23000,
	}

	projector := NewAppEventProjector("th_1", "local:th_1")
	var live []appwire.Turn
	record := func(out []AppNotification) {
		for _, notification := range out {
			live = applyLiveNotification(t, live, notification)
		}
	}
	record(projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "before"}}))
	record(projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: compaction}))
	record(projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "after"}}))

	persisted := schema.NewTurn(schema.TurnContextCompaction, llm.System(""))
	persisted.ContextCompaction = &schema.ContextCompaction{
		Layer:           compaction.Layer,
		TurnsBefore:     compaction.TurnsBefore,
		TurnsAfter:      compaction.TurnsAfter,
		EstTokensBefore: compaction.EstTokensBefore,
		EstTokensAfter:  compaction.EstTokensAfter,
	}
	persisted.Message = llm.System(persisted.ContextCompaction.Announcement())
	entries := []transcript.Entry{
		{Turn: schema.NewTurn(schema.TurnUserInput, llm.User("before"))},
		{Turn: persisted},
		{Turn: schema.NewTurn(schema.TurnUserInput, llm.User("after"))},
	}
	cold, err := apptranscript.ItemTurnsFromEntries(transcript.Header{SessionID: "th_1"}, entries,
		func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
			return apptranscript.ProjectTurn(turnID, turnIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
		})
	if err != nil {
		t.Fatalf("cold projection: %v", err)
	}

	if got, want := turnShapes(live), turnShapes(cold); !reflect.DeepEqual(got, want) {
		t.Fatalf("live and reload group a compaction differently:\nlive: %v\ncold: %v", got, want)
	}
}

// applyLiveNotification folds one live frame into the turns a client would
// hold: turn/completed carries a whole turn, item/completed adds an item to
// the turn it names.
func applyLiveNotification(t *testing.T, turns []appwire.Turn, notification AppNotification) []appwire.Turn {
	t.Helper()
	switch notification.Method {
	case appwire.NotifyTurnStarted:
		params, ok := notification.Params.(appwire.TurnStartedParams)
		if !ok {
			t.Fatalf("turn/started params: %#v", notification.Params)
		}
		return upsertLiveTurn(turns, params.Turn.ID, nil)
	case appwire.NotifyTurnCompleted:
		params, ok := notification.Params.(map[string]any)
		if !ok {
			t.Fatalf("turn/completed params: %#v", notification.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn/completed turn: %#v", params["turn"])
		}
		return upsertLiveTurn(turns, turn.ID, turn.Items)
	case appwire.NotifyItemCompleted:
		params, ok := notification.Params.(appwire.ItemLifecycleParams)
		if !ok {
			t.Fatalf("item/completed params: %#v", notification.Params)
		}
		return upsertLiveTurn(turns, params.TurnID, []appwire.ThreadItem{params.Item})
	}
	return turns
}

func upsertLiveTurn(turns []appwire.Turn, id string, items []appwire.ThreadItem) []appwire.Turn {
	for i := range turns {
		if turns[i].ID == id {
			turns[i].Items = append(turns[i].Items, items...)
			return turns
		}
	}
	return append(turns, appwire.Turn{ID: id, Items: items})
}

// turnShapes renders what a reader coming back has to find unchanged: each
// turn's key, and the items that turn holds, in order. Empty turns are left
// out — the live stream announces a turn's lifecycle in frames the file has no
// equivalent of, and what must agree is where the items landed.
func turnShapes(turns []appwire.Turn) []turnShape {
	var shapes []turnShape
	for _, turn := range turns {
		var items []string
		for _, item := range turn.Items {
			kind := item.Type
			if item.EventKind != "" {
				kind += ":" + string(item.EventKind)
			}
			items = append(items, kind)
		}
		if len(items) == 0 {
			continue
		}
		shapes = append(shapes, turnShape{turnKey: turn.ID, items: items})
	}
	return shapes
}

type turnShape struct {
	turnKey string
	items   []string
}

// A fold runs its layers in one flush: checkpoint, then summarize, sometimes a
// third. Each publishes its own EventContextCompaction and each is written as
// its own CONTEXT_COMPACTION record, so reload shows one turn per layer. A
// live projection that folded the second and third into one turn would show a
// different conversation before and after a restart — and the later layer's
// items would land in a turn the earlier one had already completed.
func TestAppEventProjectorEveryCompactionLayerGetsItsOwnTurn(t *testing.T) {
	layers := []events.ContextCompactionData{
		{Layer: "checkpoint", TurnsBefore: 42, TurnsAfter: 20, EstTokensBefore: 120000, EstTokensAfter: 60000},
		{Layer: "summarize", TurnsBefore: 20, TurnsAfter: 8, EstTokensBefore: 60000, EstTokensAfter: 23000},
		{Layer: "distill", TurnsBefore: 8, TurnsAfter: 6, EstTokensBefore: 23000, EstTokensAfter: 18000},
	}
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprintf("%d layers", count), func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			var live []appwire.Turn
			for _, layer := range layers[:count] {
				for _, notification := range projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: layer}) {
					live = applyLiveNotification(t, live, notification)
				}
			}

			var entries []transcript.Entry
			for _, layer := range layers[:count] {
				entries = append(entries, transcript.Entry{Turn: compactionRecord(layer)})
			}
			cold, err := apptranscript.ItemTurnsFromEntries(transcript.Header{SessionID: "th_1"}, entries, coldCompactionProjector)
			if err != nil {
				t.Fatalf("cold projection: %v", err)
			}

			if got, want := turnShapes(live), turnShapes(cold); !reflect.DeepEqual(got, want) {
				t.Fatalf("live and reload disagree over %d compaction layers:\nlive: %v\ncold: %v", count, got, want)
			}
			if got := len(turnShapes(live)); got != count {
				t.Fatalf("live turns for %d layers = %d, want one per layer", count, got)
			}
		})
	}
}

// compactionRecord builds the durable record the fold writes for one layer.
func compactionRecord(layer events.ContextCompactionData) schema.Turn {
	payload := schema.ContextCompaction{
		Layer:           layer.Layer,
		TurnsBefore:     layer.TurnsBefore,
		TurnsAfter:      layer.TurnsAfter,
		EstTokensBefore: layer.EstTokensBefore,
		EstTokensAfter:  layer.EstTokensAfter,
	}
	turn := schema.NewTurn(schema.TurnContextCompaction, llm.System(payload.Announcement()))
	turn.ContextCompaction = &payload
	return turn
}

func coldCompactionProjector(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
	return apptranscript.ProjectTurn(turnID, turnIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
}

// A fold's layers arrive back to back while the round that triggered them is
// still running. The first closes the turn it interrupted; the ones after it
// find no turn open, and a client that reads "the active turn completed" as
// "the session went idle" (the TUI does, kata s8x8) must not be left there by
// the last layer of the batch. Every layer says the thread is still active,
// and a compaction with no turn running still claims nothing.
func TestAppEventProjectorEveryLayerOfAnInterruptedFoldKeepsTheThreadActive(t *testing.T) {
	layer := func(name string) events.ContextCompactionData {
		return events.ContextCompactionData{Layer: name, TurnsBefore: 20, TurnsAfter: 8}
	}
	activeFrames := func(out []AppNotification) int {
		count := 0
		for _, notification := range out {
			params, ok := notification.Params.(appwire.ThreadStatusChangedParams)
			if notification.Method == appwire.NotifyThreadStatusChanged && ok && params.Status.Type == appwire.ThreadStatusActive {
				count++
			}
		}
		return count
	}

	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "ask"}})
	if got := activeFrames(projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: layer("checkpoint")})); got != 1 {
		t.Fatalf("active frames after the first layer = %d, want 1", got)
	}
	if got := activeFrames(projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: layer("summarize")})); got != 1 {
		t.Fatalf("active frames after the second layer = %d, want 1: the round the fold interrupted is still running", got)
	}
	if got := activeFrames(projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: layer("distill")})); got != 1 {
		t.Fatalf("active frames after the third layer = %d, want 1", got)
	}

	// A fold between turns interrupts nothing, and the batch state does not
	// survive the events that end it.
	idle := NewAppEventProjector("th_2", "local:th_2")
	idle.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_2", Data: events.UserInputData{Text: "ask"}})
	idle.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_2", Data: events.SessionEndData{}})
	if got := activeFrames(idle.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_2", Data: layer("checkpoint")})); got != 0 {
		t.Fatalf("active frames for a compaction with no turn running = %d, want 0", got)
	}
}

// A fold that runs both layers writes four records — one per layer, then the
// checkpoint and the summary it produced — and reload gives each its own turn.
// The markers' live announcements shared one turn, so the conversation had one
// turn where a reader coming back finds two, and every item after it moved.
func TestAppEventProjectorAFoldsMarkersGroupLikeTheirRecords(t *testing.T) {
	layers := []events.ContextCompactionData{
		{Layer: "checkpoint", TurnsBefore: 20, TurnsAfter: 12},
		{Layer: "summarize", TurnsBefore: 12, TurnsAfter: 6},
	}
	markers := []struct {
		kind schema.TurnKind
		text string
	}{
		{schema.TurnCheckpoint, "[CHECKPOINT]"},
		{schema.TurnSummary, "[CONTEXT SUMMARY]"},
	}

	projector := NewAppEventProjector("th_1", "local:th_1")
	var live []appwire.Turn
	record := func(out []AppNotification) {
		for _, notification := range out {
			live = applyLiveNotification(t, live, notification)
		}
	}
	record(projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "ask"}}))
	for _, layer := range layers {
		record(projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: layer}))
	}
	for _, marker := range markers {
		record(projector.Project(events.SessionEvent{Kind: events.EventCompactionTurn, SessionID: "th_1", Data: events.CompactionTurnData{Kind: string(marker.kind), Text: marker.text}}))
	}

	entries := []transcript.Entry{{Turn: schema.NewTurn(schema.TurnUserInput, llm.User("ask"))}}
	for _, layer := range layers {
		entries = append(entries, transcript.Entry{Turn: compactionRecord(layer)})
	}
	for _, marker := range markers {
		entries = append(entries, transcript.Entry{Turn: schema.NewTurn(marker.kind, llm.System(marker.text))})
	}
	cold, err := apptranscript.ItemTurnsFromEntries(transcript.Header{SessionID: "th_1"}, entries, coldCompactionProjector)
	if err != nil {
		t.Fatalf("cold projection: %v", err)
	}

	if got, want := turnShapes(live), turnShapes(cold); !reflect.DeepEqual(got, want) {
		t.Fatalf("live and reload group a two-layer fold differently:\nlive: %v\ncold: %v", got, want)
	}
	if got := len(turnShapes(live)); got != 5 {
		t.Fatalf("live turns = %d, want the input, a turn per layer and a turn per marker", got)
	}
}

// The markers are part of the same interruption as the layers before them: the
// round that triggered the fold is still running behind all four
// announcements, so the last of them must not leave a client believing the
// session went idle.
func TestAppEventProjectorAFoldsMarkersKeepTheThreadActive(t *testing.T) {
	activeFrames := func(out []AppNotification) int {
		count := 0
		for _, notification := range out {
			params, ok := notification.Params.(appwire.ThreadStatusChangedParams)
			if notification.Method == appwire.NotifyThreadStatusChanged && ok && params.Status.Type == appwire.ThreadStatusActive {
				count++
			}
		}
		return count
	}
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "ask"}})
	projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: events.ContextCompactionData{Layer: "checkpoint", TurnsBefore: 20, TurnsAfter: 12}})
	for _, kind := range []schema.TurnKind{schema.TurnCheckpoint, schema.TurnSummary} {
		out := projector.Project(events.SessionEvent{Kind: events.EventCompactionTurn, SessionID: "th_1", Data: events.CompactionTurnData{Kind: string(kind), Text: "marker"}})
		if got := activeFrames(out); got != 1 {
			t.Fatalf("active frames after the %s marker = %d, want 1: the round the fold interrupted is still running", kind, got)
		}
	}
}
