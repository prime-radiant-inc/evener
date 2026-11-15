package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestHistoryServingRemoteFactsSurviveTranslation(t *testing.T) {
	facts := appwire.HistoryServingFacts{ServedFrom: "saved-index", FallbackReason: "live-read-empty", AsOf: "2026-11-08T01:02:03Z"}
	client, _ := newScriptedClient(t, func(method string, _ json.RawMessage) scriptedReply {
		read := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "lane", Source: "local", Evener: appwire.EvenerThread{Ref: "local:lane"}}, LatestExecutionTurnID: "old-execution"}
		if method == appwire.MethodThreadHistoryRead {
			return scriptedReply{result: appwire.ThreadHistoryReadResponse{ThreadReadResponse: read, HistoryServingFacts: facts}}
		}
		return scriptedReply{result: read}
	})
	source := NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	read, err := source.ReadThread(appwire.WithHistoryServingFacts(t.Context()), appwire.ThreadReadParams{Ref: "remote:lane"})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Ref != "remote:lane" {
		t.Fatalf("ref=%q", read.Thread.Evener.Ref)
	}
	if read.ServingFacts == nil || *read.ServingFacts != facts || read.LatestExecutionTurnID != "old-execution" {
		t.Fatalf("remote serving facts=%+v frontier=%q", read.ServingFacts, read.LatestExecutionTurnID)
	}
}

func TestHistoryServingOlderRemoteDegradesWithoutInventingFacts(t *testing.T) {
	client, calls := newScriptedClient(t, func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadHistoryRead {
			wire := appwire.MethodNotFound(method)
			return scriptedReply{wireErr: &wire}
		}
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "lane", Source: "local", Evener: appwire.EvenerThread{Ref: "local:lane"}}}}
	})
	source := NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	for range 2 {
		read, err := source.ReadThread(appwire.WithHistoryServingFacts(t.Context()), appwire.ThreadReadParams{Ref: "remote:lane"})
		if err != nil {
			t.Fatal(err)
		}
		if read.ServingFacts == nil || read.ServingFacts.ServedFrom != "" || read.ServingFacts.AsOf != "" || read.ServingFacts.ServingFactsUnavailableReason != "older-source" {
			t.Fatalf("older remote facts=%+v", read.ServingFacts)
		}
	}
	count := 0
	for _, call := range wireCalls(calls()) {
		if call.method == appwire.MethodThreadHistoryRead {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("new-method attempts=%d want 1", count)
	}
}

func TestHistoryServingRemotePageFactsAndUnreachable(t *testing.T) {
	for _, facts := range []appwire.HistoryServingFacts{
		{ServedFrom: "live-daemon", AsOf: "2026-11-08T01:02:03Z", InstanceID: "remote-instance"},
		{ServedFrom: "saved-index", FallbackReason: "live-read-empty", AsOf: "2026-11-08T01:02:03Z"},
		{ServedFrom: "saved-index", FallbackReason: "live-read-failed", AsOf: "2026-11-08T01:02:03Z"},
	} {
		t.Run(facts.ServedFrom+facts.FallbackReason, func(t *testing.T) {
			client, _ := newScriptedClient(t, func(method string, _ json.RawMessage) scriptedReply {
				if method != appwire.MethodThreadHistoryTurnsList {
					t.Errorf("unexpected method %q", method)
				}
				return scriptedReply{result: appwire.ThreadHistoryTurnsListResponse{ThreadTurnsListResponse: appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "old-page"}}}, HistoryServingFacts: facts, LatestExecutionTurnID: "newest-view-execution"}}
			})
			source := NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
			page, err := source.ListTurns(appwire.WithHistoryServingFacts(t.Context()), appwire.ThreadTurnsListParams{Ref: "remote:lane"})
			if err != nil {
				t.Fatal(err)
			}
			if page.ServingFacts == nil || *page.ServingFacts != facts || page.LatestExecutionTurnID != "newest-view-execution" || len(page.Data) != 1 {
				t.Fatalf("remote page facts lost: %+v", page)
			}
		})
	}
	unreachable := errors.New("transport-offline")
	source := NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return nil, unreachable })
	read, err := source.ReadThread(appwire.WithHistoryServingFacts(t.Context()), appwire.ThreadReadParams{Ref: "remote:lane"})
	if !errors.Is(err, unreachable) {
		t.Fatalf("unreachable source result=%v", err)
	}
	if read.ServingFacts != nil || read.LatestExecutionTurnID != "" || len(read.Thread.Turns) != 0 {
		t.Fatalf("unreachable source invented a view: %+v", read)
	}
}
