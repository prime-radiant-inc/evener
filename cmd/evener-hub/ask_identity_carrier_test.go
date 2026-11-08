package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestLocalSpawnWorkspaceRefSharesDiscoveryFallback(t *testing.T) {
	t.Parallel()
	entry := rendezvous.Entry{SessionID: "instance-token", ThreadID: "thread-token"}
	want := appsource.LocalDaemonWorkspaceRef(appwire.LocalSourceID, entry, entry.ThreadID)
	if got := localSpawnWorkspaceRef(entry); got != want || got != "local:instance-token" {
		t.Fatalf("spawn workspace=%q discovery=%q", got, want)
	}
}

func TestRemoteRosterKeepsAskPageability(t *testing.T) {
	t.Parallel()
	thread := appwire.Thread{ID: "remote-thread", SessionID: "remote-instance", Source: "host-a",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
		Evener: appwire.EvenerThread{Ref: "host-a:remote-thread", InstanceID: "remote-instance", AskPending: true,
			PendingQuestion: &appwire.PendingQuestion{Question: "QUESTION_719", Count: 1},
			PendingAsk: &appwire.PendingAskState{Version: 1, InstanceID: "remote-instance", BootGeneration: "boot-3", Revision: 1, CompositeTotal: 1,
				Calls: []appwire.PendingAskCall{{TurnID: "turn-token", CallID: "call-token"}}},
			Capabilities: appwire.ThreadCapabilities{AskFence: true, Send: true}}}
	meta, live, ok := appThreadTreeEntries(thread)
	if !ok {
		t.Fatal("remote thread was not projected")
	}
	entries, _ := hubcore.DeriveAttention([]schema.SessionMeta{meta}, []hubcore.LiveEntry{live}, nil)
	if ask := entries[meta.ID]; !ask.AskPageable || ask.AskBootGeneration != "boot-3" || len(ask.CompositeList) != 1 {
		t.Errorf("remote attention lost the page contract: %+v", ask)
	}
	if !live.Capabilities.AskFence {
		t.Fatal("remote live entry lost ask-pageability")
	}
	if live.Capabilities.Send || live.CapabilitiesKnown {
		t.Fatal("remote ask fact acquired local-only capability authority")
	}
}
