package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	if got := localSpawnWorkspaceRef(entry); got != want || got != "local:thread-token" {
		t.Fatalf("spawn workspace=%q discovery=%q", got, want)
	}
}

func TestSessionOnlyUnconfirmedDaemonScopesOwnership(t *testing.T) {
	t.Parallel()
	peer := httptest.NewServer(http.NotFoundHandler())
	defer peer.Close()
	for _, threadID := range []string{"", " \t "} {
		t.Run(threadID, func(t *testing.T) {
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 4101, Protocol: appwire.ProtocolVersion,
				Endpoint:  "ws" + strings.TrimPrefix(peer.URL, "http"),
				SessionID: " owner-token \t", ThreadID: threadID}
			writeRendezvous(t, runDir, entry)
			roster := liveClaimRoster(runDir, &hubcore.StatusProber{})
			roster.Refresh()
			if err := roster.OwnershipError(); err != nil {
				t.Fatal(err)
			}
			if entries := roster.UnconfirmedEntries(); len(entries) != 1 || entries[0].SessionID != entry.SessionID {
				t.Fatalf("unconfirmed entries=%+v", entries)
			}
			if got := localSpawnWorkspaceRef(entry); got != "local:owner-token" {
				t.Errorf("workspace ref=%q", got)
			}
			if !unconfirmedDaemonForThread(roster, "owner-token") {
				t.Error("unconfirmed owner was admitted")
			}
			if unconfirmedDaemonForThread(roster, "unrelated-token") {
				t.Error("unrelated target acquired the owner's restriction")
			}
		})
	}
}

func TestRosterDiscoveryUsesProjectedThreadFallback(t *testing.T) {
	t.Parallel()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", ThreadID: "thread-token", SessionID: "instance-token"}
	snapshot := navigationSnapshot{
		metas: []schema.SessionMeta{{ID: "projected-token"}},
		live:  []hubcore.LiveEntry{{Entry: entry, SessionID: "projected-token", Status: appwire.ThreadStatusAwaiting, PendingAsk: true}},
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return localDaemonEntriesFromRoster(snapshot.live)
	}, nil)
	rows, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Data) != 1 || rows.Data[0].ID != "projected-token" || rows.Data[0].Evener.Ref != "local:projected-token" {
		t.Fatalf("discovery rows=%+v", rows.Data)
	}
	facts, _ := hubcore.DeriveAttention(snapshot.metas, snapshot.live, nil)
	var broker attentionBroker
	broker.Publish(facts, rosterIdentities(snapshot))
	broker.enrichDiscovery(rows.Data)
	if row := rows.Data[0].Evener; !row.AskPending || row.AttentionLevel != "needs_you" || row.AttentionAsOf == 0 {
		t.Fatalf("discovery lost roster attention: %+v", row)
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
