package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestHistoryContentSurvivesMetadataAndMutation(t *testing.T) {
	for _, operation := range []string{"metadata", "metadata-selected", "metadata-subscribed", "mutation"} {
		t.Run(operation, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			c := s.NewConnection("compact")
			s.registerConnection(c)
			c.setInitialized()
			no := false
			content := appwire.ThreadContentParams{IncludeThinking: &no}
			HandleThreadHistory(s.Router(), func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				if !CaptureSubscriptionWithHandoff(ctx, false, func() string { return "thread" }, func() uint64 { return s.routedSeq }, func() bool { return true }, CaptureSubscriptionHandoff{}) {
					t.Fatal("capture rejected")
				}
				r := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread"}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "inc", Length: 1}}
				if p.IncludeTurns {
					r.Overlay = []appwire.OverlayItem{{Key: "stream", StreamID: "s", Item: appwire.ThreadItem{ID: "a", Type: "agentMessage"}}}
				}
				return r, nil
			}, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				t.Fatal("empty history paged")
				return appwire.ThreadTurnsListResponse{}, nil
			})
			HandleTyped(s.Router(), appwire.MethodTurnStart, func(ctx context.Context, _ appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				return appwire.TurnStartResponse{}, SubscribeWithAdmission(ctx, "thread", false)
			})
			request := func(id int64, method string, params any) {
				response := c.HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(id), method, params))
				if response.Error != nil {
					t.Fatal(response.Error)
				}
				if err := c.enqueueResponse(t.Context(), response); err != nil {
					t.Fatal(err)
				}
				<-c.send
			}
			request(1, appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadContentParams: content, ThreadID: "thread", IncludeTurns: true, Subscribe: true})
			if operation == "mutation" {
				request(2, appwire.MethodTurnStart, appwire.TurnStartParams{ThreadID: "thread", ClientMutationID: "mutation", ExpectedInstanceID: "instance"})
			} else {
				params := appwire.ThreadReadParams{ThreadID: "thread", Subscribe: operation == "metadata-subscribed"}
				if operation == "metadata-selected" {
					params.ThreadContentParams = content
				}
				request(2, appwire.MethodThreadRead, params)
			}
			s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2}, Items: []appwire.ThreadItem{{ID: "a", Type: "agentMessage"}, {ID: "r", Type: "reasoning"}}})
			var update appwire.HistoryUpdatedParams
			if err := json.Unmarshal((<-c.send).Notification.Params, &update); err != nil {
				t.Fatal(err)
			}
			if len(update.Items) != 1 || update.Snapshot.Incarnation == "inc" {
				t.Fatalf("subscription selection lost: items=%d snapshot=%v", len(update.Items), update.Snapshot)
			}
			s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: "stream", Field: appwire.OverlayDeltaText, Delta: "chunk-data"})
			select {
			case frame := <-c.send:
				if frame.Notification.Method != appwire.NotifyOverlayDelta {
					t.Fatal("unexpected notification")
				}
			default:
				t.Fatal("existing stream delta dropped")
			}
		})
	}
}

func TestHistoryContentIndependentSubscribersAndRollback(t *testing.T) {
	s := NewServer(ServerConfig{})
	a, b := s.NewConnection("compact"), s.NewConnection("full")
	s.registerConnection(a)
	s.registerConnection(b)
	a.setInitialized()
	b.setInitialized()
	no := false
	content := appwire.ThreadContentParams{IncludeThinking: &no}
	fail := false
	HandleThreadHistory(s.Router(), func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if p.Selection() != 0 {
			t.Fatal("producer was filtered")
		}
		if !CaptureSubscriptionWithHandoff(ctx, p.ReplaceSubscription, func() string { return "thread" }, func() uint64 { return s.routedSeq }, func() bool { return true }, CaptureSubscriptionHandoff{}) {
			t.Fatal("capture rejected")
		}
		if fail {
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("fixture")
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread"}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "inc", Length: 1}, Overlay: []appwire.OverlayItem{{Key: "stream", StreamID: "s", Item: appwire.ThreadItem{ID: "a", Type: "agentMessage"}}}}, nil
	}, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		t.Fatal("metadata read paged")
		return appwire.ThreadTurnsListResponse{}, nil
	})
	read := func(c *Connection, id int, options appwire.ThreadContentParams) {
		response := c.HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(int64(id)), appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadContentParams: options, ThreadID: "thread", IncludeTurns: true, Subscribe: true}))
		if err := c.enqueueResponse(t.Context(), response); err != nil {
			t.Fatal(err)
		}
		<-c.send
	}
	read(a, 1, content)
	read(b, 2, appwire.ThreadContentParams{})
	assertSelection := func() {
		s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2}, Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "a"}, {Type: "reasoning", ID: "r"}}})
		for c, want := range map[*Connection]int{a: 1, b: 2} {
			msg := <-c.send
			var p appwire.HistoryUpdatedParams
			if err := json.Unmarshal(msg.Notification.Params, &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Items) != want {
				t.Fatalf("%s items=%d want=%d", c.id, len(p.Items), want)
			}
		}
	}
	assertSelection()
	// A failed replacement must restore both the old owner and its filter.
	fail = true
	read(a, 3, appwire.ThreadContentParams{})
	fail = false
	assertSelection()
	s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: "stream", Field: appwire.OverlayDeltaText, Delta: "chunk-data"})
	if (<-a.send).Notification.Method != appwire.NotifyOverlayDelta || (<-b.send).Notification.Method != appwire.NotifyOverlayDelta {
		t.Fatal("initial overlay lost on rollback")
	}
	// A successful default read replaces the compact view with the full view.
	read(a, 4, appwire.ThreadContentParams{})
	s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{Items: []appwire.ThreadItem{{Type: "reasoning", ID: "r"}}})
	var p appwire.HistoryUpdatedParams
	if err := json.Unmarshal((<-a.send).Notification.Params, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 {
		t.Fatal("replacement kept obsolete selection")
	}
	<-b.send
}
