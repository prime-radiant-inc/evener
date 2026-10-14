package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
)

func TestHubHistoryContentRemoteRetentionRotation(t *testing.T) {
	t.Run("candidate-bound", func(t *testing.T) { checkHistoryContentRemoteRetention(t, 2201, 600, 0) })
	t.Run("byte-bound", func(t *testing.T) { checkHistoryContentRemoteRetention(t, 71, 20, 150000) })
}

func checkHistoryContentRemoteRetention(t *testing.T, length int64, spacing uint64, reasoningBytes int) {
	t.Helper()
	identity := appitempaging.CursorIdentity{ThreadRef: "local:t1", Incarnation: "source-incarnation", ProjectionVersion: 1}
	snapshot := &appwire.SnapshotIdentity{Incarnation: identity.Incarnation, Length: length}
	var candidates []appitempaging.TranscriptItemCandidate
	for entry := uint64(1); entry <= uint64(snapshot.Length); entry++ {
		kind := "reasoning"
		if entry%spacing == 1 {
			kind = "agentMessage"
		}
		text := ""
		if kind == "reasoning" {
			text = strings.Repeat("x", reasoningBytes)
		}
		id := strconv.FormatUint(entry, 10)
		position := appwire.ThreadItemPosition{Entry: entry}
		candidates = append(candidates, appitempaging.TranscriptItemCandidate{
			TurnID: id, Turn: appwire.Turn{ID: id}, Position: position,
			Item: appwire.ThreadItem{Type: kind, Text: text, ID: id, TranscriptKey: id, Position: &position, TurnID: id},
		})
	}
	page := func(before *appwire.ThreadItemPosition, limit int) appwire.ThreadTurnsListResponse {
		t.Helper()
		window, older, err := appitempaging.SelectCandidates(candidates, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		// The upstream Hub also caps page bytes. Keep this external fixture's
		// raw windows within that bound before the controller packs them.
		if reasoningBytes > 0 {
			byteLimit := (1 << 20) / reasoningBytes
			if len(window) > byteLimit {
				window = window[len(window)-byteLimit:]
				older = true
			}
		}
		turns, err := appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(window))
		if err != nil {
			t.Fatal(err)
		}
		cursor := ""
		if older {
			cursor, err = appitempaging.EncodeCursor(identity, window[0].Position)
			if err != nil {
				t.Fatal(err)
			}
		}
		return appwire.ThreadTurnsListResponse{Data: turns, NextCursor: cursor, Snapshot: snapshot, BootGeneration: "1", Epoch: 1, Authoritative: true}
	}
	remote, calls := newScriptedRemoteHub(t, func(method string, raw json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			var p appwire.ThreadReadParams
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Error(err)
			}
			p1 := page(nil, p.ItemLimit)
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "t1", Source: "local", Evener: appwire.EvenerThread{Ref: "local:t1"}, Turns: p1.Data}, OlderCursor: p1.NextCursor, Snapshot: snapshot, BootGeneration: "1", Epoch: 1, Authoritative: true}
		case appwire.MethodThreadTurnsList:
			var p appwire.ThreadTurnsListParams
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Error(err)
			}
			before, err := appitempaging.Boundary(p, identity)
			if err != nil {
				return err
			}
			return page(&before, p.ItemLimit)
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) { return remote, nil })
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	client := dialHubRPC(t, srv)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	no := false
	selection := appwire.ThreadContentParams{IncludeThinking: &no}
	params := appwire.ThreadReadParams{ThreadContentParams: selection, Ref: "h1:t1", IncludeTurns: true, ItemLimit: 2}
	read, err := client.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(turns []appwire.Turn) []string {
		var result []string
		for _, turn := range turns {
			for _, item := range turn.Items {
				result = append(result, item.ID)
			}
		}
		return result
	}
	if got := ids(read.Thread.Turns); !reflect.DeepEqual(got, []string{strconv.FormatUint(1+2*spacing, 10), strconv.FormatUint(1+3*spacing, 10)}) {
		t.Fatalf("latest ids=%v", got)
	}
	// Filling two chat items plus lookahead crosses the real remote source's
	// candidate or byte retention bound without changing the transcript snapshot.
	if reasoningBytes == 0 && len(scriptedRemoteHubParams[appwire.ThreadTurnsListParams](t, calls(), appwire.MethodThreadTurnsList)) < 26 {
		t.Fatal("selected fill did not exercise retention rotation")
	}
	olderParams := appwire.ThreadTurnsListParams{ThreadContentParams: selection, Ref: params.Ref, Cursor: read.OlderCursor, ItemLimit: 2}
	for range 2 {
		older, err := client.ThreadTurnsList(t.Context(), olderParams)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(older.Data); !reflect.DeepEqual(got, []string{"1", strconv.FormatUint(1+spacing, 10)}) || older.NextCursor != "" {
			t.Fatalf("older ids=%v hasMore=%t", got, older.NextCursor != "")
		}
		// Another read may replace the retained source window; the selected
		// boundary must remain useful while the source snapshot is unchanged.
		if _, err := client.ThreadRead(t.Context(), params); err != nil {
			t.Fatal(err)
		}
	}
}
