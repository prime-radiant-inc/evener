package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestRosterRemoteClearSameRef(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	publish := func(instance string) {
		thread := appwire.Thread{
			ID: "worker", SessionID: instance, Source: "remote", Name: "worker",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Ref: "remote:worker", InstanceID: instance},
		}
		meta, live, ok := appThreadTreeEntries(thread)
		if !ok {
			t.Fatal("remote thread was not captured")
		}
		snapshot := navigationSnapshot{metas: []schema.SessionMeta{meta}, live: []hubcore.LiveEntry{live}}
		entries, _ := hubcore.DeriveAttention(snapshot.metas, snapshot.live, nil)
		b.Publish(entries, rosterIdentities(snapshot))
	}
	publish("before-clear")
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:manager", Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	initial := <-sub.Updates
	if !initial.Initial || len(initial.Events) != 1 || initial.Events[0].Ref != "remote:worker" || initial.Events[0].ID != "remote:worker" || initial.Events[0].Level != "idle" {
		t.Fatalf("remote initial snapshot=%+v", initial)
	}
	if initial.Events[0].RosterInstanceID != "before-clear" {
		t.Fatalf("remote initial instance=%q", initial.Events[0].RosterInstanceID)
	}
	// The initial is consumed before the next synchronous publication, with
	// no missing tick between the old instance and its replacement.
	publish("after-clear")
	select {
	case update := <-sub.Updates:
		if update.Initial || update.Resync || len(update.Events) != 1 || update.Events[0].Ref != "remote:worker" || update.Events[0].ID != initial.Events[0].ID || update.Events[0].Level != "idle" || update.Events[0].Kind != "new" {
			t.Fatalf("remote Clear delta=%+v, want only a new idle replacement under the stable ref", update)
		}
		if update.Events[0].RosterInstanceID != "after-clear" {
			t.Fatalf("remote replacement instance=%q", update.Events[0].RosterInstanceID)
		}
	default:
		t.Fatal("remote Clear replacement was not published")
	}
	publish("after-clear")
	select {
	case update := <-sub.Updates:
		t.Fatalf("unchanged replacement repeated an event: %+v", update)
	default:
	}
}
