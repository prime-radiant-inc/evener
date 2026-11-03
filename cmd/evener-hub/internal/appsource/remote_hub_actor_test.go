package appsource

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// Each carrier projects remote-local actors without changing saved inputs.
func TestRemoteManagerActorNamespace(t *testing.T) {
	t.Parallel()
	s := NewRemoteHubSource("host-b", nil, nil)
	for _, actorCase := range []struct{ name, kind, saved, want string }{
		{"B_local_Manager", "manager", "local:manager-on-b", "host-b:manager-on-b"},
		{"A_origin_hub_identity", "manager", "hub-a-id:manager-on-a", "hub-a-id:manager-on-a"},
		{"human", "human", "", ""},
	} {
		t.Run(actorCase.name, func(t *testing.T) {
			savedActor := appwire.SessionActor{Kind: actorCase.kind, ManagerRef: actorCase.saved}
			t.Cleanup(func() {
				if savedActor.ManagerRef != actorCase.saved {
					t.Errorf("saved actor mutated: %+v", savedActor)
				}
			})
			item := func() appwire.ThreadItem {
				return appwire.ThreadItem{Type: "userMessage", Text: "local:payload-must-stay", Actor: &savedActor}
			}
			check := func(t *testing.T, projected appwire.ThreadItem) {
				got := projected.Actor.ManagerRef
				if projected.Actor.Kind != actorCase.kind || projected.Text != "local:payload-must-stay" {
					t.Errorf("non-ref data changed: %+v", projected)
				}
				t.Helper()
				t.Logf("saved actor=%s, controller actor=%s, expected=%s", actorCase.saved, got, actorCase.want)
				if got != actorCase.want {
					t.Errorf("actor source mismatch")
				}
			}
			t.Run("snapshot", func(t *testing.T) {
				response := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "worker", Source: "local", Evener: appwire.EvenerThread{Ref: "local:worker", InstanceID: "worker-instance"}, Turns: []appwire.Turn{{ID: "turn-1", Items: []appwire.ThreadItem{item()}}}}}
				if err := s.translateOut(nil, &response); err != nil {
					t.Fatal(err)
				}
				if response.Thread.Evener.Ref != "host-b:worker" {
					t.Fatalf("target ref=%s", response.Thread.Evener.Ref)
				}
				check(t, response.Thread.Turns[0].Items[0])
			})
			t.Run("paged_turns", func(t *testing.T) {
				response := appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "turn-1", Items: []appwire.ThreadItem{item()}}}}
				if err := s.translateOut(nil, &response); err != nil {
					t.Fatal(err)
				}
				check(t, response.Data[0].Items[0])
			})
			t.Run("paged_items", func(t *testing.T) {
				response := appwire.ThreadTurnItemsListResponse{Data: []appwire.ThreadItem{item()}}
				if err := s.translateOut(nil, &response); err != nil {
					t.Fatal(err)
				}
				check(t, response.Data[0])
			})
			t.Run("read_changes_and_overlay", func(t *testing.T) {
				nonlocal := appwire.ThreadItem{Type: "userMessage", Text: "other", Actor: &appwire.SessionActor{Kind: "manager", ManagerRef: "hub-a-id:manager-on-a"}}
				response := appwire.ThreadReadResponse{
					Thread:  appwire.Thread{ID: "worker", Source: "local", Evener: appwire.EvenerThread{Ref: "local:worker"}},
					Changes: &appwire.HistoryChanges{Items: []appwire.ThreadItem{item(), nonlocal}, Turns: []appwire.Turn{{ID: "turn-2", Items: []appwire.ThreadItem{item(), nonlocal}}}},
					Overlay: []appwire.OverlayItem{{Key: "stream:1", Kind: appwire.OverlayStream, Item: item()}, {Key: "stream:2", Kind: appwire.OverlayStream, Item: nonlocal}},
				}
				original := *item().Actor
				if err := s.translateOut(nil, &response); err != nil {
					t.Fatal(err)
				}
				for _, projected := range []appwire.ThreadItem{
					response.Changes.Items[0], response.Changes.Turns[0].Items[0], response.Overlay[0].Item,
				} {
					check(t, projected)
				}
				for _, projected := range []appwire.ThreadItem{
					response.Changes.Items[1], response.Changes.Turns[0].Items[1], response.Overlay[1].Item,
				} {
					if projected.Actor.ManagerRef != "hub-a-id:manager-on-a" || projected.Text != "other" {
						t.Errorf("nonlocal item changed: %+v", projected)
					}
				}
				if savedActor != original {
					t.Errorf("original actor changed: before=%+v after=%+v", original, savedActor)
				}
			})
			for _, method := range []string{appwire.NotifyHistoryUpdated} {
				t.Run(method, func(t *testing.T) {
					fields := map[string]any{"ref": "local:worker", "threadId": "worker"}
					fields["items"] = []appwire.ThreadItem{item()}
					raw, err := json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					translated, target, ok := s.translateNotification(appwire.Notification{Method: method, Params: raw}, nil)
					if !ok || target != "worker" {
						t.Fatalf("notification rejected: ok=%v target=%q", ok, target)
					}
					var out struct {
						Ref   string               `json:"ref"`
						Item  appwire.ThreadItem   `json:"item"`
						Items []appwire.ThreadItem `json:"items"`
					}
					if err := json.Unmarshal(translated.Params, &out); err != nil {
						t.Fatal(err)
					}
					if out.Ref != "host-b:worker" {
						t.Fatalf("target ref=%s", out.Ref)
					}
					check(t, out.Items[0])
				})
			}
		})
	}
}

// Pending steering input carries its author on the mutation entry, not on a
// transcript item. A remote-local Manager author must be projected into the
// controller namespace exactly like transcript actors, on the typed read path
// and on the raw thread path; an author already qualified by another hub is
// preserved and a saved actor value is never mutated.
func TestRemotePendingMutationActorNamespace(t *testing.T) {
	t.Parallel()
	s := NewRemoteHubSource("host-b", nil, nil)
	for _, actorCase := range []struct{ name, kind, saved, want string }{
		{"local_manager", "manager", "local:manager-on-b", "host-b:manager-on-b"},
		{"origin_hub_identity", "manager", "hub-a-id:manager-on-a", "hub-a-id:manager-on-a"},
		{"human", "human", "", ""},
	} {
		t.Run(actorCase.name, func(t *testing.T) {
			mutation := func() appwire.PendingMutation {
				return appwire.PendingMutation{
					Actor:            appwire.SessionActor{Kind: actorCase.kind, ManagerRef: actorCase.saved},
					ClientMutationID: "cm-1",
					Method:           "turn/steer",
				}
			}
			check := func(t *testing.T, projected appwire.PendingMutation) {
				t.Helper()
				if projected.Actor.Kind != actorCase.kind || projected.Actor.ManagerRef != actorCase.want {
					t.Errorf("pending author = %+v, want kind %q ref %q", projected.Actor, actorCase.kind, actorCase.want)
				}
				if projected.ClientMutationID != "cm-1" || projected.Method != "turn/steer" {
					t.Errorf("non-actor data changed: %+v", projected)
				}
			}
			saved := mutation()
			t.Run("read", func(t *testing.T) {
				response := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "worker", Source: "local", Evener: appwire.EvenerThread{Ref: "local:worker", PendingMutations: []appwire.PendingMutation{mutation()}}}}
				if err := s.translateOut(nil, &response); err != nil {
					t.Fatal(err)
				}
				check(t, response.Thread.Evener.PendingMutations[0])
			})
			t.Run("raw_thread", func(t *testing.T) {
				raw, err := json.Marshal(appwire.Thread{ID: "worker", Source: "local", Evener: appwire.EvenerThread{Ref: "local:worker", PendingMutations: []appwire.PendingMutation{mutation()}}})
				if err != nil {
					t.Fatal(err)
				}
				translated, err := s.translateThreadRaw(raw, appwire.ThreadCapabilities{})
				if err != nil {
					t.Fatal(err)
				}
				var out appwire.Thread
				if err := json.Unmarshal(translated, &out); err != nil {
					t.Fatal(err)
				}
				check(t, out.Evener.PendingMutations[0])
			})
			if saved.Actor.Kind != actorCase.kind || saved.Actor.ManagerRef != actorCase.saved {
				t.Errorf("saved pending author mutated: %+v", saved.Actor)
			}
		})
	}
}
