package hub

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

type discoveryQuestionSource struct {
	*managerReadSource
	threads []appwire.Thread
}

func (s *discoveryQuestionSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: s.threads}, nil
}

func TestManagerDiscoveryRosterPopulationAndDroppedDelivery(t *testing.T) {
	t.Parallel()
	owner, local, remote, _ := managerReadFixture(t)
	question := &appwire.PendingQuestion{Question: "q-token", Count: 1, TurnID: "turn-token", CallID: "call-token"}
	root := local.thread
	root.ID, root.SessionID, root.Evener.Ref = "local-instance", "local-instance", "local:workspace-token"
	root.Evener.AskPending, root.Evener.PendingQuestion = true, question
	child := appwire.Thread{ID: "child", Evener: appwire.EvenerThread{Ref: "local:child", Kind: "subagent"}}
	disk := appwire.Thread{ID: "disk", Evener: appwire.EvenerThread{Ref: "local:disk"}}
	owner.sources.Add(&discoveryQuestionSource{managerReadSource: local, threads: []appwire.Thread{root, child, disk}})
	remote.thread.Evener.AskPending, remote.thread.Evener.PendingQuestion = true, question
	snapshot := navigationSnapshot{metas: []schema.SessionMeta{{ID: "local-instance"}, {ID: "child", IsSubagent: true}}, live: []hubcore.LiveEntry{
		{Entry: rendezvous.Entry{WorkspaceRef: "local:workspace-token", ThreadID: "different-token"}, SessionID: "local-instance", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: question},
		{SourceID: "remote", ThreadID: "shared", SessionID: "remote:shared", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: question},
		{SessionID: "child", Status: appwire.ThreadStatusAwaiting},
	}}
	// The Manager path deliberately passes no archive suppression decisions.
	entries, _ := hubcore.DeriveAttention(snapshot.metas, snapshot.live, nil)
	owner.rosterBroker().Publish(entries, rosterIdentities(snapshot))
	// No watch is registered, so there can be no successful push delivery.
	discovery, err := owner.discover(t.Context(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Threads.Data) != 4 {
		t.Fatalf("population=%+v", discovery.Threads.Data)
	}
	for _, row := range discovery.Threads.Data {
		switch row.Evener.Ref {
		case "local:workspace-token", "remote:shared":
			if !row.Evener.AskPending || row.Evener.PendingQuestion == nil || row.Evener.PendingQuestion.TurnID != "turn-token" || row.Evener.AttentionLevel != "needs_you" || row.Evener.AttentionAsOf == 0 {
				t.Fatalf("pull lost asking lane: %+v", row.Evener)
			}
		case "local:child", "local:disk":
			raw, _ := json.Marshal(row.Evener)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			if _, ok := fields["attentionLevel"]; ok {
				t.Fatalf("outside-roster attention=%s", raw)
			}
			if _, ok := fields["attentionAsOf"]; ok {
				t.Fatalf("outside-roster moment=%s", raw)
			}
		default:
			t.Fatalf("unexpected ref %q", row.Evener.Ref)
		}
	}
}

func TestManagerDiscoveryKeepsFresherAskBesideStaleAttention(t *testing.T) {
	t.Parallel()
	owner, local, remote, _ := managerReadFixture(t)
	remote.empty = true
	local.thread.Evener.AskPending = true
	local.thread.Evener.PendingQuestion = &appwire.PendingQuestion{Question: "new-q-token", Count: 1}
	owner.rosterBroker().Publish(map[string]appwire.AttentionEntry{"old-identity": {ID: "old-identity", Level: "idle", PublishedAt: 1700000000001}}, func(string) (rosterIdentity, bool) {
		return rosterIdentity{Ref: "local:shared", InstanceID: "instance-local"}, true
	})
	got, err := owner.discover(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Threads.Data) != 1 {
		t.Fatalf("rows=%+v", got.Threads.Data)
	}
	row := got.Threads.Data[0].Evener
	if !row.AskPending || row.PendingQuestion == nil || row.PendingQuestion.Question != "new-q-token" || row.AttentionLevel != "idle" || row.AttentionAsOf != 1700000000001 {
		t.Fatalf("skew lost source fact or observation moment: %+v", row)
	}
}
