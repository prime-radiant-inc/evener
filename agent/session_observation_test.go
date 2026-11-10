package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The event stream is deliberately not consumed: the disk record, not a live
// notification that a disconnected Manager missed, must retain its ancestry.
func TestManagerObservationReplayCausal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	causal := provenance.WithWatch(nil, "watch-A", "generation-A", "delivery-A", "source-A", "job-A")
	sess.replaceActiveProvenance(causal)
	if err := sess.deliverCommunicate(events.CommunicateData{CallID: "call-A", Message: "result A", EndTurn: true, EndReason: "done"}); err != nil {
		t.Fatal(err)
	}
	sess.replaceActiveProvenance(nil)
	if err := sess.deliverCommunicate(events.CommunicateData{CallID: "call-empty", Message: "result B"}); err != nil {
		t.Fatal(err)
	}
	path := sess.TranscriptPath()
	sess.Close()
	writer, entries, err := transcript.OpenWriterForSession(path, sess.id)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	seen := map[string]bool{}
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind != schema.TurnCommunicate || turn.Communicate == nil {
			continue
		}
		encoded, err := json.Marshal(turn)
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			CausalKnown bool               `json:"causal_known"`
			Causal      *provenance.Causal `json:"causal"`
		}
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatal(err)
		}
		seen[turn.Communicate.CallID] = true
		if !record.CausalKnown {
			t.Fatalf("replayed %s lost known causal evidence", turn.Communicate.CallID)
		}
		want := causal
		if turn.Communicate.CallID == "call-empty" {
			want = nil
		}
		if !reflect.DeepEqual(record.Causal, want) {
			t.Fatalf("replayed %s causal = %+v, want %+v", turn.Communicate.CallID, record.Causal, want)
		}
	}
	if !seen["call-A"] || !seen["call-empty"] {
		t.Fatalf("missing communicated records: %+v", seen)
	}
}
