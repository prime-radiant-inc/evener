package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestNotificationObservationPrivateProjection(t *testing.T) {
	t.Parallel()
	line := []byte(`{"seq":1,"turn":{"kind":"ASSISTANT","observed_notification_ids":["obs-a"],"message":{"role":"assistant","content":[{"kind":"text","text":"answer-822d"}]}}}`)
	projected, include, err := publicTranscriptLine(line, 0)
	if err != nil || !include {
		t.Fatalf("public line include=%t err=%v", include, err)
	}
	var raw struct {
		Turn map[string]json.RawMessage `json:"turn"`
	}
	if err := json.Unmarshal(projected, &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw.Turn["observed_notification_ids"]; leaked {
		t.Fatal("public line leaks receipt IDs")
	}
	var entry transcript.Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		t.Fatal(err)
	}
	public, include := publicTranscriptEntry(entry)
	if !include {
		t.Fatal("public entry omitted assistant response")
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw.Turn["observed_notification_ids"]; leaked {
		t.Fatal("public entry leaks receipt IDs")
	}
}

func TestNotificationProvenancePrivateProjection(t *testing.T) {
	t.Parallel()
	line := []byte(`{"seq":1,"turn":{"kind":"STEERING","attention_id":"notification-test-1","notification_provenance":{"watch_keys":[{"watch_id":"watch_accept","watch_generation":"wg_accept"}]},"message":{"role":"user","content":[{"kind":"text","text":"payload-7c41"}]}}}`)
	projected, include, err := publicTranscriptLine(line, 0)
	if err != nil || !include {
		t.Fatalf("public line include=%t err=%v", include, err)
	}
	var raw struct {
		Turn map[string]json.RawMessage `json:"turn"`
	}
	if err := json.Unmarshal(projected, &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw.Turn["notification_provenance"]; leaked {
		t.Fatal("public line leaks causal metadata")
	}
	var entry transcript.Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		t.Fatal(err)
	}
	public, include := publicTranscriptEntry(entry)
	if !include {
		t.Fatal("public entry omitted notification")
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw.Turn["notification_provenance"]; leaked {
		t.Fatal("public entry leaks causal metadata")
	}
}

func TestAttentionFoldIgnoresTranscriptOnlyEntries(t *testing.T) {
	attention := schema.NewTurn(schema.TurnSteering, llm.User("delegate reported"))
	attention.AttentionID = "delegate:d1"
	plain := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("go")),
		attention,
		delegateAttentionResolutionTurn("delegate:d1", delegateAttentionConsumed),
	}
	want, err := foldDelegateAttention(entriesOf(plain))
	if err != nil {
		t.Fatal(err)
	}
	if len(want.order) == 0 {
		t.Fatal("the fixture folds no attention")
	}
	got, err := foldDelegateAttention(entriesOf(schematest.InterleaveTranscriptOnly(plain)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fold = %+v, want %+v", got, want)
	}
}
