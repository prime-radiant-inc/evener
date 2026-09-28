package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A subscribing thread/read's response reaches the connection before any
// notification committed after its cut: history/updated for an entry
// recorded while the read projects history after the cut is held until the
// response is in the send queue (appserver's releaseHydration), so a client
// never merges an update into a thread it has not read yet.
func TestThreadReadResponseReachesTheConnectionBeforePostCutNotifications(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("before the read")))
	srv := st.srv
	st.settle(t)
	threadReadAfterCutHook = func() {
		st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("after the cut")))
		st.settle(t) // its history/updated is committed before the read goes on
	}
	t.Cleanup(func() { threadReadAfterCutHook = nil })

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	send := func(id int64, method string, params any) {
		t.Helper()
		if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(id), method, params)); err != nil {
			t.Fatal(err)
		}
	}
	send(1, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if msg, err := transport.Recv(ctx); err != nil || msg.Response == nil {
		t.Fatalf("initialize = %+v, %v", msg, err)
	}
	send(2, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, Subscribe: true, ItemLimit: 40})
	responded := false
	for {
		msg, err := transport.Recv(ctx)
		if err != nil {
			t.Fatalf("waiting for the read and its post-cut update: %v", err)
		}
		if msg.Response != nil {
			responded = true
			continue
		}
		if msg.Notification == nil || msg.Notification.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		var update appwire.HistoryUpdatedParams
		if err := json.Unmarshal(msg.Notification.Params, &update); err != nil {
			t.Fatal(err)
		}
		if !responded {
			t.Fatalf("history/updated %+v reached the connection before the read's response", update)
		}
		for _, item := range update.Items {
			if item.Text == "after the cut" {
				return
			}
		}
	}
}
