package appwire

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestHistoryServingUnsupportedSourceIsExplicit(t *testing.T) {
	read := HistoryReadResponse(ThreadReadResponse{Thread: Thread{ID: "legacy"}})
	page := HistoryTurnsResponse(ThreadTurnsListResponse{})
	for _, facts := range []HistoryServingFacts{read.HistoryServingFacts, page.HistoryServingFacts} {
		if facts.ServingFactsUnavailableReason == "" || facts.ServedFrom != "" || facts.AsOf != "" || facts.InstanceID != "" {
			t.Fatalf("unlabelled older source: %+v", facts)
		}
	}
}

func TestHistoryServingWrappersLeaveLegacyStampsAndShape(t *testing.T) {
	legacy := ThreadTurnsListResponse{Data: []Turn{}, Authoritative: true, ServingFacts: &HistoryServingFacts{ServedFrom: "saved-index", AsOf: "2026-11-08T01:02:03Z", InstanceID: "must-not-leak"}, LatestExecutionTurnID: "view-frontier"}
	before, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	versioned := HistoryTurnsResponse(legacy)
	if versioned.Authoritative || versioned.InstanceID != "" || versioned.LatestExecutionTurnID != "view-frontier" {
		t.Fatalf("versioned saved page=%+v", versioned)
	}
	after, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !legacy.Authoritative {
		t.Fatalf("legacy response changed: before=%s after=%s", before, after)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(after, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"servedFrom", "asOf", "instanceId", "latestExecutionTurnId"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("private field %q leaked", key)
		}
	}
}
