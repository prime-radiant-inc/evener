package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestHistoryContentRealProducerPagingRefreshAndReconnect(t *testing.T) {
	// Not parallel: servedTranscript installs a package-wide publication observer.
	st := newServedTranscript(t, NewServer(ServerConfig{}), "selected")
	var want []string
	for i := range 7 {
		text := fmt.Sprintf("fixture-%d", i)
		want = append(want, text)
		st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
		for range 12 {
			st.record(t, schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning-data"}}}}))
		}
	}
	st.settle(t)
	httpServer := httptest.NewServer(http.HandlerFunc(st.srv.AppServer().ServeWebSocket))
	t.Cleanup(httpServer.Close)
	connect := func() *appwire.Client {
		transport, err := appwire.DialWebSocket(t.Context(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
		if err != nil {
			t.Fatal(err)
		}
		client := appwire.NewClient(transport)
		client.Start(t.Context())
		t.Cleanup(func() { _ = client.Close() })
		if _, err = client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		return client
	}
	no := false
	options := appwire.ThreadContentParams{IncludeThinking: &no, IncludeToolCalls: &no, IncludeToolCallIntents: &no, IncludeSystemMessages: &no, IncludeDiagnostics: &no}
	params := appwire.ThreadReadParams{ThreadContentParams: options, Ref: "local:selected", IncludeTurns: true, Subscribe: true, ItemLimit: 2}
	compact, full := connect(), connect()
	r, err := compact.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if r.Thread.Evener.Diagnostics != nil || !r.Thread.Evener.MutationStateAuthoritative {
		t.Fatal("selected read changed mutation authority or retained diagnostics")
	}
	all := historyContentTexts(r.Thread.Turns)
	for cursor := r.OlderCursor; cursor != ""; {
		page, err := compact.ThreadTurnsList(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: options, Ref: params.Ref, Cursor: cursor, ItemLimit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(historyContentTexts(page.Data)) == 0 {
			t.Fatal("empty selected page")
		}
		all = append(historyContentTexts(page.Data), all...)
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("paged text=%v want=%v", all, want)
	}
	fullParams := params
	fullParams.ThreadContentParams = appwire.ThreadContentParams{}
	fullRead, err := full.ThreadRead(t.Context(), fullParams)
	if err != nil {
		t.Fatal(err)
	}
	if fullRead.Snapshot.Incarnation == r.Snapshot.Incarnation {
		t.Fatal("full/selected snapshots share view identity")
	}
	st.record(t, schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "live-reasoning"}}, {Kind: llm.ContentText, Text: "live-data"}}}))
	st.settle(t)
	for client, expected := range map[*appwire.Client]int{compact: 1, full: 2} {
		for {
			var n appwire.Notification
			select {
			case n = <-client.Notifications():
			case <-t.Context().Done():
				t.Fatal(context.Cause(t.Context()))
			}
			if n.Method != appwire.NotifyHistoryUpdated {
				continue
			}
			var p appwire.HistoryUpdatedParams
			if err = json.Unmarshal(n.Params, &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Items) != expected {
				t.Fatalf("live items=%d want=%d", len(p.Items), expected)
			}
			if client == compact && p.Snapshot.Incarnation != r.Snapshot.Incarnation {
				t.Fatal("live update changed selected identity")
			}
			break
		}
	}
	params.HeldSnapshot = r.Snapshot
	refresh, err := compact.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if got := historyContentTexts(refresh.Thread.Turns); !reflect.DeepEqual(got, []string{want[6], "live-data"}) {
		t.Fatalf("refresh=%v", got)
	}
	if err = compact.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected := connect()
	again, err := reconnected.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(historyContentTexts(again.Thread.Turns), historyContentTexts(refresh.Thread.Turns)) {
		t.Fatal("reconnect lost selected latest window")
	}
	// A cursor saved after loading a later page remains usable after reconnect.
	older, err := reconnected.ThreadTurnsList(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: options, Ref: params.Ref, Cursor: r.OlderCursor, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := historyContentTexts(older.Data); !reflect.DeepEqual(got, want[3:5]) {
		t.Fatalf("reconnected older page=%v", got)
	}
}

func historyContentTexts(turns []appwire.Turn) []string {
	var texts []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			texts = append(texts, item.Text)
		}
	}
	return texts
}
