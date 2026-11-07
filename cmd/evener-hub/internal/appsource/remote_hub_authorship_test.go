package appsource

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func remoteAuthorshipItem() appwire.ThreadItem {
	return appwire.ThreadItem{ID: "item", Type: "agentMessage", Text: `{"authorship":{"author":{"sessionRef":"local:opaque"}}}`, Raw: json.RawMessage(`{"authorship":{"author":{"sessionRef":"local:opaque","ownerRef":"local:opaque"}}}`), Authorship: &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:child", OwnerRef: "local:parent", DelegateID: "dlg_child", DelegateName: "Child"}, Inherited: true}}
}
func TestRemoteAuthorship(t *testing.T) {
	for _, host := range []string{"east", "west"} {
		t.Run(host, func(t *testing.T) {
			item := remoteAuthorshipItem()
			turn := appwire.Turn{ID: "turn", Items: []appwire.ThreadItem{item}}
			source, _ := newScriptedRemote(t, host, func(method string, _ json.RawMessage) scriptedReply {
				switch method {
				case appwire.MethodThreadRead:
					return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "child", Evener: appwire.EvenerThread{Ref: "local:child"}, Turns: []appwire.Turn{turn}}, Changes: &appwire.HistoryChanges{Items: []appwire.ThreadItem{item}}, Overlay: []appwire.OverlayItem{{Key: "preview:call", Kind: appwire.OverlayPreview, Item: item}}}}
				case appwire.MethodThreadTurnsList:
					return scriptedReply{result: appwire.ThreadTurnsListResponse{Data: []appwire.Turn{turn}}}
				default:
					t.Errorf("unexpected method %s", method)
					return scriptedReply{}
				}
			})
			check := func(got appwire.ThreadItem) {
				t.Helper()
				want := remoteAuthorshipItem()
				want.Authorship.Author.SessionRef = host + ":child"
				want.Authorship.Author.OwnerRef = host + ":parent"
				if !reflect.DeepEqual(got, want) {
					t.Errorf("item=%+v author=%+v want=%+v", got, got.Authorship, want.Authorship)
				}
			}
			read, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: host + ":child", IncludeTurns: true})
			if err != nil {
				t.Fatal(err)
			}
			check(read.Thread.Turns[0].Items[0])
			check(read.Overlay[0].Item)
			check(read.Changes.Items[0])
			page, err := source.ListTurns(context.Background(), appwire.ThreadTurnsListParams{Ref: host + ":child"})
			if err != nil {
				t.Fatal(err)
			}
			check(page.Data[0].Items[0])
			for _, method := range []string{appwire.NotifyHistoryUpdated, appwire.NotifyOverlayUpserted, appwire.NotifyThreadStarted} {
				fields := map[string]any{"threadId": "child", "ref": "local:child", "future": 17}
				switch method {
				case appwire.NotifyHistoryUpdated:
					fields["items"] = []appwire.ThreadItem{item}
				case appwire.NotifyOverlayUpserted:
					fields["item"] = appwire.OverlayItem{Item: item}
				case appwire.NotifyThreadStarted:
					fields["thread"] = appwire.Thread{ID: "child", Evener: appwire.EvenerThread{Ref: "local:child"}, Turns: []appwire.Turn{turn}}
				}
				raw, _ := json.Marshal(fields)
				n, _, ok := source.translateNotification(appwire.Notification{Method: method, Params: raw}, nil)
				if !ok {
					t.Fatal("notification dropped")
				}
				var got map[string]json.RawMessage
				if err := json.Unmarshal(n.Params, &got); err != nil {
					t.Fatal(err)
				}
				if string(got["future"]) != "17" {
					t.Fatal("unknown field lost")
				}
				var projected appwire.ThreadItem
				switch method {
				case appwire.NotifyHistoryUpdated:
					var items []appwire.ThreadItem
					if err := json.Unmarshal(got["items"], &items); err != nil {
						t.Fatal(err)
					}
					projected = items[0]
				case appwire.NotifyOverlayUpserted:
					var overlay appwire.OverlayItem
					if err := json.Unmarshal(got["item"], &overlay); err != nil {
						t.Fatal(err)
					}
					projected = overlay.Item
				case appwire.NotifyThreadStarted:
					var thread appwire.Thread
					if err := json.Unmarshal(got["thread"], &thread); err != nil {
						t.Fatal(err)
					}
					projected = thread.Turns[0].Items[0]
				}
				check(projected)
			}
		})
	}
}

func TestRemoteAuthorshipStructuralVisitor(t *testing.T) {
	s := NewRemoteHubSource("east", nil, nil)
	raw := json.RawMessage(`{"id":"item","authorship":{"future":17,"inherited":true,"author":{"kind":"agent","sessionRef":"local:child","ownerRef":"local:parent","future":"kept"}},"raw":{"authorship":{"author":{"sessionRef":"local:opaque","ownerRef":"local:opaque"}}}}`)
	once := s.translateRawItemAuthorship(raw)
	twice := s.translateRawItemAuthorship(once)
	if string(once) != string(twice) {
		t.Fatal("qualification not idempotent")
	}
	var fields map[string]any
	if err := json.Unmarshal(once, &fields); err != nil {
		t.Fatal(err)
	}
	a := fields["authorship"].(map[string]any)
	author := a["author"].(map[string]any)
	if a["future"] != float64(17) || author["future"] != "kept" || author["sessionRef"] != "east:child" || author["ownerRef"] != "east:parent" {
		t.Fatal(fields)
	}
	opaque := fields["raw"].(map[string]any)["authorship"].(map[string]any)["author"].(map[string]any)
	if opaque["sessionRef"] != "local:opaque" || opaque["ownerRef"] != "local:opaque" {
		t.Fatal("opaque data rewritten")
	}
	item := remoteAuthorshipItem()
	original := item.Authorship
	s.translateItemAuthorship(&item)
	s.translateItemAuthorship(&item)
	if original.Author.SessionRef != "local:child" || item.Authorship.Author.SessionRef != "east:child" {
		t.Fatal("shared source mutated or double qualified")
	}
	item.Authorship.Author.SessionRef = "nested:child"
	s.translateItemAuthorship(&item)
	if item.Authorship.Author.SessionRef != "nested:child" {
		t.Fatal("unsupported nested ref retargeted")
	}
}
