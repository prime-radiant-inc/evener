package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each asking root's
// first pending question on its row, and no key on a row with none (S1b).
func TestLocalDaemonSourceListCarriesTheRootsPendingQuestion(t *testing.T) {
	question := &appwire.PendingQuestion{Question: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 1}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/asking", ThreadID: "th_asking", SessionID: "sess_asking"}, Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: question},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.PendingQuestion{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.PendingQuestion
	}
	if !reflect.DeepEqual(byID["th_asking"], question) || byID["th_idle"] != nil {
		t.Fatalf("questions = %+v, want th_asking's %+v and none on th_idle", byID, question)
	}
	byID["th_asking"].Options[0] = "changed"
	if question.Options[0] != "Drop them" {
		t.Fatal("a listed row aliases the roster's question")
	}
}
