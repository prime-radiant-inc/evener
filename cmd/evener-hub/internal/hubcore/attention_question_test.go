package hubcore

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestAttentionCarriesOnePendingAskSnapshot(t *testing.T) {
	t.Parallel()
	var row appwire.Thread
	if err := json.Unmarshal([]byte(`{"id":"lane-token","status":{"type":"awaiting"},"evener":{"askPending":true,"capabilities":{"askFence":true},"pendingQuestion":{"question":"q-token","count":3,"turnId":"turn-token","callId":"call-token","complete":false,"truncated":true},"pendingAsk":{"version":1,"instanceId":"lane-token","bootGeneration":"boot-token","revision":4,"calls":[{"turnId":"turn-token","callId":"call-token"}],"compositeTotal":2,"calllessCount":1}}}`), &row); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	probe := ProbeResultFromThread(row)
	live := LiveEntryThreadFacts(LiveEntry{SessionID: "lane-token", Capabilities: probe.Capabilities}, probe)
	entries, _ := DeriveAttention(nil, []LiveEntry{live}, nil)
	after := time.Now().UnixMilli()
	raw, err := json.Marshal(entries["lane-token"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pendingQuestion", "askBootGeneration", "askRevision", "compositeList", "compositeTotal", "calllessCount", "askPageable", "publishedAt"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("attention omitted wire field %s", key)
		}
	}
	var got appwire.AttentionEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.PendingQuestion == nil || got.PendingQuestion.TurnID != "turn-token" || got.PendingQuestion.Complete == nil || *got.PendingQuestion.Complete || got.AskBootGeneration != "boot-token" || got.AskRevision != 4 || len(got.CompositeList) != 1 || got.CompositeList[0].CallID != "call-token" || got.CompositeTotal != 2 || got.CalllessCount != 1 || !got.AskPageable || got.NeedsResponse {
		t.Fatalf("attention lost the atomic ask facts: %s", raw)
	}
	if got.PublishedAt < before || got.PublishedAt > after {
		t.Fatalf("derivation moment=%d outside [%d,%d]", got.PublishedAt, before, after)
	}
}
