package appserver

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
)

func TestHistoryContentOverlappingPreservation(t *testing.T) {
	for _, registration := range []string{"daemon", "handoff", "relay"} {
		for _, tc := range []struct {
			name           string
			initialCompact bool
			readFails      bool
			metadataFails  bool
			cancelMetadata bool
			unsubscribe    bool
			mutation       bool
		}{
			{name: "metadata"},
			{name: "metadata-error", metadataFails: true},
			{name: "metadata-cancel", cancelMetadata: true},
			{name: "unsubscribe", unsubscribe: true},
			{name: "read-error", readFails: true},
			{name: "full-view", initialCompact: true},
			{name: "full-view-error", initialCompact: true, readFails: true},
			{name: "mutation", mutation: true},
			{name: "mutation-after-error", mutation: true, readFails: true},
		} {
			name := registration + "/" + tc.name
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := NewServer(ServerConfig{})
					c := s.NewConnection("client")
					s.registerConnection(c)
					c.setInitialized()
					stream := "prior"
					readFails := false
					HandleThreadHistory(s.Router(), func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
						var captured bool
						switch registration {
						case "relay":
							captured = SubscribeWithAdmission(ctx, "thread", p.ReplaceSubscription) == nil
						case "handoff":
							captured = CaptureSubscriptionWithHandoff(ctx, p.ReplaceSubscription, func() string { return "thread" }, func() uint64 { return s.routedSeq }, func() bool { return true }, CaptureSubscriptionHandoff{})
						default:
							captured = CaptureSubscription(ctx, p.ReplaceSubscription, func() string { return "thread" }, func() uint64 { return s.routedSeq }, func() bool { return true })
						}
						if !captured || (p.IncludeTurns && readFails) || (!p.IncludeTurns && tc.metadataFails) {
							return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("fixture")
						}
						r := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread"}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "inc", Length: 1}}
						if p.IncludeTurns {
							r.Overlay = []appwire.OverlayItem{{Key: stream, StreamID: "s", Item: appwire.ThreadItem{ID: "a", Type: "agentMessage"}}}
						}
						return r, nil
					}, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
						t.Error("empty source was paged")
						return appwire.ThreadTurnsListResponse{}, nil
					})
					HandleTyped(s.Router(), appwire.MethodTurnStart, func(ctx context.Context, _ appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
						return RetrySubscription(ctx, func(ctx context.Context) (appwire.TurnStartResponse, error) {
							return appwire.TurnStartResponse{}, SubscribeWithAdmission(ctx, "thread", false)
						})
					})
					read := func(id int64, compact bool) appwire.Message {
						thinking := !compact
						return c.HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(id), appwire.MethodThreadRead, appwire.ThreadReadParams{
							ThreadID: "thread", IncludeTurns: true, Subscribe: true, ReplaceSubscription: true,
							IncludeThinking: &thinking,
						}))
					}
					enqueue := func(response appwire.Message) {
						if err := c.enqueueResponse(t.Context(), response); err != nil {
							t.Fatal(err)
						}
						<-c.send
					}
					initial := read(1, tc.initialCompact)
					if initial.Error != nil {
						t.Fatal(initial.Error)
					}
					enqueue(initial)
					stream, readFails = "next", tc.readFails
					pending := read(2, !tc.initialCompact)
					if (pending.Error != nil) != tc.readFails {
						t.Fatalf("read error = %v", pending.Error)
					}

					HandleTyped(s.Router(), appwire.MethodThreadUnsubscribe, func(ctx context.Context, p appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
						Unsubscribe(ctx, p.ThreadID)
						return appwire.EmptyResponse{}, nil
					})
					if !tc.mutation {
						admission := c.beginSubscriptionAdmission("3", "thread")
						defer c.finishSubscriptionAdmission(admission)
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					preserved := make(chan appwire.Message, 1)
					go func() {
						request := appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadID: "thread", Subscribe: true, ReplaceSubscription: true})
						if tc.mutation {
							request = appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnStart, appwire.TurnStartParams{ThreadID: "thread", ClientMutationID: "mutation", ExpectedInstanceID: "instance"})
						}
						preserved <- c.HandleMessage(ctx, request)
					}()
					// Let the second request reach either its response or a durable
					// wait for the first response, without relying on scheduler timing.
					synctest.Wait()
					if tc.cancelMetadata {
						cancel()
						synctest.Wait()
					}
					if tc.unsubscribe {
						enqueue(c.HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodThreadUnsubscribe, appwire.ThreadUnsubscribeParams{ThreadID: "thread"})))
					}
					enqueue(pending)
					preservation := <-preserved
					if (preservation.Error != nil) != (tc.metadataFails || tc.cancelMetadata || tc.unsubscribe) {
						t.Fatalf("preservation error = %v", preservation.Error)
					}
					enqueue(preservation)
					if tc.unsubscribe {
						if s.subs.IsSubscribed(c.id, "thread") {
							t.Fatal("waiting read resurrected an unsubscribed view")
						}
						return
					}

					wantCompact := !tc.initialCompact
					wantStream := "next"
					if tc.readFails {
						wantCompact, wantStream = tc.initialCompact, "prior"
					}
					s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2}, Items: []appwire.ThreadItem{{ID: "a", Type: "agentMessage"}, {ID: "r", Type: "reasoning"}}})
					var update appwire.HistoryUpdatedParams
					if err := json.Unmarshal((<-c.send).Notification.Params, &update); err != nil {
						t.Fatal(err)
					}
					wantItems := 2
					if wantCompact {
						wantItems = 1
					}
					if len(update.Items) != wantItems || (update.Snapshot.Incarnation != "inc") != wantCompact {
						t.Fatalf("view after overlapping reads: items=%d incarnation=%s; want compact=%t", len(update.Items), update.Snapshot.Incarnation, wantCompact)
					}
					s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: wantStream, Field: appwire.OverlayDeltaText, Delta: "chunk-data"})
					select {
					case frame := <-c.send:
						if frame.Notification == nil || frame.Notification.Method != appwire.NotifyOverlayDelta {
							t.Fatal("unexpected stream delivery")
						}
					default:
						t.Fatal("selected stream delta dropped")
					}
				})
			})
		}
	}
}

func TestHistoryContentPendingSnapshotKeepsBufferedDelta(t *testing.T) {
	for _, registration := range []string{"capture", "relay"} {
		for _, mutation := range []bool{false, true} {
			name := "metadata"
			if mutation {
				name = "mutation"
			}
			t.Run(registration+"/"+name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := NewServer(ServerConfig{})
					c := s.NewConnection("client")
					s.registerConnection(c)
					c.setInitialized()
					captured, finishRead := make(chan struct{}), make(chan struct{})
					HandleThreadHistory(s.Router(), func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
						if registration == "relay" {
							if err := SubscribeWithAdmission(ctx, "thread", false); err != nil {
								return appwire.ThreadReadResponse{}, err
							}
						} else if !CaptureSubscriptionWithHandoff(ctx, false, func() string { return "thread" }, func() uint64 { return s.routedSeq }, func() bool { return true }, CaptureSubscriptionHandoff{}) {
							return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("fixture")
						}
						r := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread"}, Snapshot: &appwire.SnapshotIdentity{Incarnation: "inc", Length: 1}}
						if p.IncludeTurns {
							close(captured)
							<-finishRead
							r.Overlay = []appwire.OverlayItem{{Key: "stream", StreamID: "s", Item: appwire.ThreadItem{ID: "a", Type: "agentMessage"}}}
						}
						return r, nil
					}, nil)
					HandleTyped(s.Router(), appwire.MethodTurnStart, func(ctx context.Context, _ appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
						return RetrySubscription(ctx, func(ctx context.Context) (appwire.TurnStartResponse, error) {
							return appwire.TurnStartResponse{}, SubscribeWithAdmission(ctx, "thread", false)
						})
					})
					no := false
					selected := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadID: "thread", IncludeTurns: true, Subscribe: true, IncludeThinking: &no})
					preservation := appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{ThreadID: "thread", Subscribe: true})
					if mutation {
						preservation = appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ThreadID: "thread", ClientMutationID: "mutation", ExpectedInstanceID: "instance"})
					}
					request := func(msg appwire.Message) {
						response := c.HandleMessage(t.Context(), msg)
						if response.Error != nil {
							t.Error(response.Error)
						}
						if err := c.enqueueResponse(t.Context(), response); err != nil {
							t.Error(err)
						}
					}
					go request(selected)
					<-captured
					go request(preservation)
					synctest.Wait()
					s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: "stream", Field: appwire.OverlayDeltaText, Delta: "chunk-data"})
					var frames []appwire.Message
					drain := func() {
						for {
							select {
							case frame := <-c.send:
								frames = append(frames, frame)
							default:
								return
							}
						}
					}
					drain()
					for _, frame := range frames {
						if frame.Notification != nil {
							t.Error("notification preceded the selected snapshot")
						}
					}
					close(finishRead)
					synctest.Wait()
					drain()
					selectedIndex, deltaIndex, deltas := -1, -1, 0
					for i, frame := range frames {
						if frame.Response != nil && requestIDKey(frame.Response.ID) == requestIDKey(selected.Request.ID) {
							selectedIndex = i
						}
						if frame.Notification != nil && frame.Notification.Method == appwire.NotifyOverlayDelta {
							deltaIndex, deltas = i, deltas+1
						}
					}
					if selectedIndex < 0 || deltas != 1 || deltaIndex <= selectedIndex {
						t.Fatalf("selected response index=%d, delta index=%d, delta count=%d", selectedIndex, deltaIndex, deltas)
					}
				})
			})
		}
	}
}
