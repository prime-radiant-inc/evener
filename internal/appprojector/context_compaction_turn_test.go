package appprojector

import (
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
