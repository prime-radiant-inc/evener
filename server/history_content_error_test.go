package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestHistoryContentFailedDaemonReadPreservesSelectionAndStream(t *testing.T) {
	no := false
	selected := appwire.ThreadContentParams{IncludeThinking: &no}
	for _, tc := range []struct {
		name            string
		initial, failed appwire.ThreadContentParams
		wantItems       int
		resync          bool
	}{
		{"selected-refresh", selected, selected, 1, false},
		{"selected-to-full", selected, appwire.ThreadContentParams{}, 1, false},
		{"full-to-selected", appwire.ThreadContentParams{}, selected, 2, false},
		{"resync-selected-refresh", selected, selected, 1, true},
		{"resync-selected-to-full", selected, appwire.ThreadContentParams{}, 1, true},
		{"resync-full-to-selected", appwire.ThreadContentParams{}, selected, 2, true},
	} {
		for _, replace := range []bool{false, true} {
			name := tc.name
			if replace {
				name += "-replace"
			}
			t.Run(name, func(t *testing.T) {
				// The package-wide cut hook holds the real daemon read while its
				// transcript becomes unavailable and live notifications arrive.
				st := newServedTranscript(t, NewServer(ServerConfig{}), "selected", schema.NewTurn(schema.TurnUserInput, llm.User("input-data")))
				srv := st.srv
				st.settle(t)
				srv.RecordAppEvent(threadEvent(st.threadID, events.RoundStartedData{RoundID: "round"}))
				srv.RecordAppEvent(threadEvent(st.threadID, events.AssistantTextDeltaData{Delta: "initial-data"}))
				httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
				t.Cleanup(httpServer.Close)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = transport.Close() })
				request := func(id int64, method string, params any) appwire.Message {
					t.Helper()
					if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(id), method, params)); err != nil {
						t.Fatal(err)
					}
					msg, err := transport.Recv(ctx)
					if err != nil {
						t.Fatal(err)
					}
					return msg
				}
				if msg := request(1, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); msg.Response == nil {
					t.Fatalf("initialize=%+v", msg)
				}
				params := appwire.ThreadReadParams{ThreadContentParams: tc.initial, Ref: "local:selected", IncludeTurns: true, Subscribe: true, ItemLimit: 2}
				msg := request(2, appwire.MethodThreadRead, params)
				if msg.Response == nil {
					t.Fatalf("initial read=%+v", msg)
				}
				raw, err := json.Marshal(msg.Response.Result)
				if err != nil {
					t.Fatal(err)
				}
				var initial appwire.ThreadReadResponse
				if err := json.Unmarshal(raw, &initial); err != nil {
					t.Fatal(err)
				}
				if len(initial.Overlay) != 1 {
					t.Fatalf("initial overlays=%d", len(initial.Overlay))
				}
				if tc.resync {
					// Environment resync invalidates the history envelope without
					// ending the producer's active assistant stream.
					srv.RecordAppEvent(threadEvent(st.threadID, events.EnvironmentChangedData{WorkingDir: t.TempDir()}))
					for {
						frame, err := transport.Recv(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if frame.Notification != nil && frame.Notification.Method == appwire.NotifyEvenerThreadResync {
							break
						}
					}
				}
				threadReadAfterCutHook = func() {
					if err := os.Rename(st.path, st.path+".unavailable"); err != nil {
						t.Error(err)
					}
					srv.RecordAppEvent(threadEvent(st.threadID, events.AssistantTextDeltaData{Delta: "buffered-data"}))
				}
				t.Cleanup(func() { threadReadAfterCutHook = nil })
				params.ThreadContentParams = tc.failed
				params.ReplaceSubscription = replace
				msg = request(3, appwire.MethodThreadRead, params)
				if msg.Error == nil || msg.IDString() != "3" {
					t.Fatalf("failed read=%+v", msg)
				}
				threadReadAfterCutHook = nil
				if err := os.Rename(st.path+".unavailable", st.path); err != nil {
					t.Fatal(err)
				}
				srv.RecordAppEvent(threadEvent(st.threadID, events.AssistantTextDeltaData{Delta: "continued-data"}))
				st.record(t, schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning-data"}}, {Kind: llm.ContentText, Text: "completed-data"}}}))
				st.settle(t)
				// Recovery frames must still follow a failed read. This terminal
				// marker also bounds the delta assertions without a timing race.
				srv.AppServer().Broadcast(srv.appNotificationTarget(st.threadID), appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{ThreadID: st.threadID})
				deltas, items, recovery := 0, 0, false
				for !recovery {
					frame, err := transport.Recv(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if frame.Notification == nil {
						t.Fatalf("notification=%+v", frame)
					}
					switch frame.Notification.Method {
					case appwire.NotifyOverlayDelta:
						deltas++
					case appwire.NotifyHistoryUpdated:
						var update appwire.HistoryUpdatedParams
						if err := json.Unmarshal(frame.Notification.Params, &update); err != nil {
							t.Fatal(err)
						}
						items += len(update.Items)
					case appwire.NotifyEvenerThreadResync:
						recovery = true
					}
				}
				if deltas != 2 || items != tc.wantItems {
					t.Fatalf("after failed read: deltas=%d items=%d, want 2 and %d", deltas, items, tc.wantItems)
				}
				params.ThreadContentParams = tc.initial
				if msg := request(4, appwire.MethodThreadRead, params); msg.Response == nil {
					t.Fatalf("recovered read=%+v", msg)
				}
			})
		}
	}
}
