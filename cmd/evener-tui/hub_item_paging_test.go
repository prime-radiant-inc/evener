package tui

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func TestCollectHubThreadPagesMergesFragmentsAndKeepsEveryItem(t *testing.T) {
	var listParams []appwire.ThreadTurnsListParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			listParams = append(listParams, params)
			switch params.Cursor {
			case "older-1":
				return appwire.ThreadTurnsListResponse{
					Data: []appwire.Turn{
						fragmentTurn("turn-1", "item-1", "item-2"),
						fragmentTurn("turn-0", "item-0"),
					},
					NextCursor: "older-2",
				}, nil
			case "older-2":
				return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{fragmentTurn("turn-prelude", "item-prelude")}}, nil
			default:
				t.Fatalf("unexpected cursor %q", params.Cursor)
				return appwire.ThreadTurnsListResponse{}, nil
			}
		})
	})
	defer cleanup()

	thread, err := collectHubThreadPages(context.Background(), client, "local:thread-1", appwire.Thread{
		ID:    "thread-1",
		Turns: []appwire.Turn{fragmentTurn("turn-1", "item-3")},
	}, "older-1")
	if err != nil {
		t.Fatalf("collectHubThreadPages: %v", err)
	}
	if got, want := turnItemIDs(thread.Turns), []string{"item-prelude", "item-0", "item-1", "item-2", "item-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged item IDs=%v, want %v", got, want)
	}
	if got, want := turnIDs(thread.Turns), []string{"turn-prelude", "turn-0", "turn-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged turn IDs=%v, want %v", got, want)
	}
	if len(listParams) != 2 || listParams[0].Cursor != "older-1" || listParams[1].Cursor != "older-2" {
		t.Fatalf("turns/list params=%+v, want two ordered opaque cursors", listParams)
	}
	for _, params := range listParams {
		if params.Ref != "local:thread-1" || params.ItemLimit != 40 || params.ItemsView != "full" {
			t.Fatalf("turns/list params=%+v, want ref/itemsView/itemLimit", params)
		}
	}
}

func TestFetchHubTranscriptCollects41SplitTurnItems(t *testing.T) {
	var listParams []appwire.ThreadTurnsListParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if params.Ref != "local:thread-1" || !params.IncludeTurns || params.ItemsView != "full" || params.ItemLimit != 40 {
				t.Fatalf("thread/read params=%+v, want bounded full item read", params)
			}
			return appwire.ThreadReadResponse{
				Thread:      appwire.Thread{ID: "thread-1", Turns: []appwire.Turn{publicFragmentTurn("turn-1", "item-40")}},
				OlderCursor: "older-1",
			}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			listParams = append(listParams, params)
			switch params.Cursor {
			case "older-1":
				items := make([]string, 0, 39)
				for index := 1; index <= 39; index++ {
					items = append(items, fmt.Sprintf("item-%d", index))
				}
				return appwire.ThreadTurnsListResponse{
					Data:       []appwire.Turn{publicFragmentTurn("turn-1", items...)},
					NextCursor: "older-2",
				}, nil
			case "older-2":
				return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{publicFragmentTurn("turn-1", "item-0")}}, nil
			default:
				t.Fatalf("unexpected cursor %q", params.Cursor)
				return appwire.ThreadTurnsListResponse{}, nil
			}
		})
	})
	defer cleanup()

	msg, ok := fetchHubTranscript(client, appwire.ThreadTranscriptTarget{Ref: "local:thread-1"})().(hubTranscriptMsg)
	if !ok || msg.err != nil {
		t.Fatalf("fetchHubTranscript message=%#v, err=%v", msg, msg.err)
	}
	if got, want := len(msg.messages), 41; got != want {
		t.Fatalf("public transcript message count=%d, want %d", got, want)
	}
	seen := make(map[string]bool, len(msg.messages))
	for index, message := range msg.messages {
		wantID := fmt.Sprintf("item-%d", index)
		if message.ItemID != wantID {
			t.Fatalf("message[%d].ItemID=%q, want %q", index, message.ItemID, wantID)
		}
		if seen[message.ItemID] {
			t.Fatalf("item %q appeared more than once", message.ItemID)
		}
		seen[message.ItemID] = true
	}
	if len(listParams) != 2 || listParams[0].Cursor != "older-1" || listParams[1].Cursor != "older-2" {
		t.Fatalf("turns/list params=%+v, want two ordered opaque cursors", listParams)
	}
	for _, params := range listParams {
		if params.Ref != "local:thread-1" || params.ItemLimit != 40 || params.ItemsView != "full" {
			t.Fatalf("turns/list params=%+v, want ref/itemsView/itemLimit", params)
		}
	}
}

func TestFetchHubStatusDoesNotRequestTranscriptHistory(t *testing.T) {
	var got appwire.ThreadReadParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			got = params
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread-1"}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, func(_ context.Context, _ appwire.TaskListParams) (appwire.TaskListResponse, error) {
			return appwire.TaskListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthStatus, func(_ context.Context, _ appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
			return appwire.AuthStatusResponse{}, nil
		})
	})
	defer cleanup()

	msg, ok := fetchHubStatus(client, appwire.Ref{SourceID: "local", ThreadID: "thread-1"})().(hubStatusMsg)
	if !ok || msg.err != nil {
		t.Fatalf("fetchHubStatus message=%#v, err=%v", msg, msg.err)
	}
	if got.IncludeTurns {
		t.Fatalf("status read requested transcript history: %+v", got)
	}
	if got.ItemLimit != 0 {
		t.Fatalf("status read requested item page: %+v", got)
	}
}

func TestCollectHubThreadPagesRejectsMultiStepCursorCycle(t *testing.T) {
	var calls int
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			calls++
			switch params.Cursor {
			case "a":
				return appwire.ThreadTurnsListResponse{NextCursor: "b"}, nil
			case "b":
				return appwire.ThreadTurnsListResponse{NextCursor: "a"}, nil
			default:
				t.Fatalf("unexpected cursor %q", params.Cursor)
				return appwire.ThreadTurnsListResponse{}, nil
			}
		})
	})
	defer cleanup()

	_, err := collectHubThreadPages(context.Background(), client, "local:thread-1", appwire.Thread{}, "a")
	if err == nil {
		t.Fatal("multi-step cursor cycle was accepted")
	}
	if calls != 2 {
		t.Fatalf("turns/list calls=%d, want 2 before cycle rejection", calls)
	}
}

func TestMergeHubFragmentsPreservesSettledStateAndOptionalPayloads(t *testing.T) {
	started, completed := int64(10), int64(20)
	oldError := &appwire.TurnError{Message: "settled error"}
	oldUsage := &appwire.EvenerUsage{}
	older := appwire.Turn{
		ID:        "turn-1",
		Status:    "completed",
		Error:     oldError,
		StartedAt: &started,
		Usage:     oldUsage,
		Cost:      "$1",
		Items: []appwire.ThreadItem{{
			Type:          "commandExecution",
			ID:            "item-1",
			TranscriptKey: "key-1",
			CallID:        "call-1",
			Output:        "settled output",
			Status:        "completed",
			CompletedAt:   &completed,
			Position:      &appwire.ThreadItemPosition{Entry: 1, Item: 0},
			PrevalOnly:    true,
		}},
	}
	newer := appwire.Turn{
		ID:     "turn-1",
		Status: "inProgress",
		Items: []appwire.ThreadItem{{
			Type:          "commandExecution",
			ID:            "item-1",
			TranscriptKey: "key-1",
			Text:          "newer content",
			Status:        "inProgress",
		}},
	}

	merged := mergeHubTurnPages([]appwire.Turn{older}, []appwire.Turn{newer})
	if len(merged) != 1 || merged[0].Status != "completed" || merged[0].Error != oldError || merged[0].StartedAt != &started || merged[0].Usage != oldUsage || merged[0].Cost != "$1" {
		t.Fatalf("turn merge downgraded or discarded scalar state: %+v", merged)
	}
	item := merged[0].Items[0]
	if item.Status != "completed" || item.Output != "settled output" || item.CallID != "call-1" || item.CompletedAt != &completed || item.Position == nil || !item.PrevalOnly || item.Text != "newer content" {
		t.Fatalf("item merge lost settled/payload/current state: %+v", item)
	}

	interrupted := mergeHubTurnPages(
		[]appwire.Turn{{ID: "turn-interrupted", Status: appwire.TurnStatusInterrupted, Items: []appwire.ThreadItem{{ID: "item-interrupted", Status: appwire.TurnStatusInterrupted}}}},
		[]appwire.Turn{{ID: "turn-interrupted", Status: appwire.TurnStatusInProgress, Items: []appwire.ThreadItem{{ID: "item-interrupted", Status: appwire.TurnStatusInProgress}}}},
	)
	if len(interrupted) != 1 || interrupted[0].Status != appwire.TurnStatusInterrupted || len(interrupted[0].Items) != 1 || interrupted[0].Items[0].Status != appwire.TurnStatusInterrupted {
		t.Fatalf("interrupted state downgraded: %+v", interrupted)
	}
}

func TestFetchHubSessionReadKeepsCaptureThroughOlderPages(t *testing.T) {
	var app *appserver.Server
	client, feed, cleanup := newTestHubClientWithFeed(t, func(server *appserver.Server) {
		app = server
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if !params.Subscribe {
				t.Fatalf("initial read lost subscription: %+v", params)
			}
			appserver.CaptureSubscription(ctx, params.ReplaceSubscription, func() string { return "01TUI" }, func() uint64 { return 0 }, func() bool { return true })
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread-1", Turns: []appwire.Turn{fragmentTurn("turn-1", "item-new")}}, OlderCursor: "older-1"}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			if params.Cursor != "older-1" {
				t.Fatalf("older cursor=%q, want older-1", params.Cursor)
			}
			app.Broadcast("01TUI", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{ThreadID: "01TUI", Ref: "local:01TUI", TurnID: "turn-1", Delta: "live-after-page"})
			return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{fragmentTurn("turn-1", "item-old")}}, nil
		})
	})
	defer cleanup()

	msg, ok := fetchHubSessionRead(feed, client, appwire.Ref{SourceID: "local", ThreadID: "01TUI"}, "", 0, true, true)().(hubSessionMsg)
	if !ok || msg.err != nil || msg.capture == nil {
		t.Fatalf("session read message=%#v", msg)
	}
	awaitPostCutFrame(t, feed, "live-after-page")
	msg.capture.Release()
	select {
	case notification := <-feed.Notifications():
		if notification.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("released notification method=%q, want %q", notification.Method, appwire.NotifyAgentMessageDelta)
		}
	default:
		t.Fatal("post-cut live notification was not released after the full snapshot")
	}
}

func fragmentTurn(turnID string, itemIDs ...string) appwire.Turn {
	items := make([]appwire.ThreadItem, 0, len(itemIDs))
	for index, itemID := range itemIDs {
		items = append(items, appwire.ThreadItem{Type: "agentMessage", ID: itemID, TranscriptKey: itemID, TurnID: turnID, Text: itemID, Status: "completed", Position: &appwire.ThreadItemPosition{Entry: uint64(index + 1), Item: 0}})
	}
	return appwire.Turn{ID: turnID, Items: items, ItemsView: appwire.TurnItemsViewFragment, Status: "completed"}
}

func publicFragmentTurn(turnID string, itemIDs ...string) appwire.Turn {
	turn := fragmentTurn(turnID, itemIDs...)
	for index := range turn.Items {
		turn.Items[index].Type = "systemMessage"
	}
	return turn
}

func turnIDs(turns []appwire.Turn) []string {
	ids := make([]string, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	return ids
}

func turnItemIDs(turns []appwire.Turn) []string {
	var ids []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
