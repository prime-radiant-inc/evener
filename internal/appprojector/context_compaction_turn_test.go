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
