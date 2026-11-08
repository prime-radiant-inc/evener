package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCloneThreadRetainsPendingQuestionMetadata(t *testing.T) {
	t.Parallel()
	const raw = `{"evener":{"attentionLevel":"needs_you","attentionAsOf":1700000000123,"capabilities":{"askFence":true},"pendingQuestion":{"question":"q-token","count":1,"turnId":"turn-token","callId":"call-token","complete":false,"truncated":true}}}`
	var original Thread
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	clone := CloneThread(original)
	encoded, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Evener struct {
			AttentionLevel string `json:"attentionLevel"`
			AttentionAsOf  int64  `json:"attentionAsOf"`
			Capabilities   struct {
				AskFence bool `json:"askFence"`
			} `json:"capabilities"`
			PendingQuestion struct {
				TurnID    string `json:"turnId"`
				CallID    string `json:"callId"`
				Complete  *bool  `json:"complete"`
				Truncated *bool  `json:"truncated"`
			} `json:"pendingQuestion"`
		} `json:"evener"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	q := got.Evener.PendingQuestion
	if q.TurnID != "turn-token" || q.CallID != "call-token" || q.Complete == nil || *q.Complete || q.Truncated == nil || !*q.Truncated {
		t.Fatalf("clone lost pending-question metadata: %+v", q)
	}
	if got.Evener.AttentionLevel != "needs_you" || got.Evener.AttentionAsOf != 1700000000123 || !got.Evener.Capabilities.AskFence {
		t.Fatalf("clone lost discovery facts: %+v", got.Evener)
	}
	*clone.Evener.PendingQuestion.Complete = true
	*clone.Evener.PendingQuestion.Truncated = false
	if *original.Evener.PendingQuestion.Complete || !*original.Evener.PendingQuestion.Truncated {
		t.Fatal("cloned question aliases the original flags")
	}
}

func TestEqualAttentionEntryComparesEveryFact(t *testing.T) {
	t.Parallel()
	base := AttentionEntry{}
	if !EqualAttentionEntry(base, AttentionEntry{CompositeList: []PendingAskCall{}}) {
		t.Fatal("absent and empty composite lists differ")
	}
	for i := range reflect.TypeFor[AttentionEntry]().NumField() {
		changed := AttentionEntry{}
		v := reflect.ValueOf(&changed).Elem().Field(i)
		switch v.Kind() {
		case reflect.String:
			v.SetString("token")
		case reflect.Bool:
			v.SetBool(true)
		case reflect.Int, reflect.Int64:
			v.SetInt(1)
		case reflect.Uint64:
			v.SetUint(1)
		case reflect.Pointer:
			v.Set(reflect.ValueOf(&PendingQuestion{Count: 1}))
		case reflect.Slice:
			v.Set(reflect.ValueOf([]PendingAskCall{{TurnID: "turn-token", CallID: "call-token"}}))
		default:
			t.Fatalf("uncovered equality field %s", reflect.TypeFor[AttentionEntry]().Field(i).Name)
		}
		if EqualAttentionEntry(base, changed) || EqualAttentionEntry(changed, base) {
			t.Errorf("equality ignored %s", reflect.TypeFor[AttentionEntry]().Field(i).Name)
		}
	}
	flag := false
	a := AttentionEntry{PendingQuestion: &PendingQuestion{Question: "q-token", Count: 1, Complete: &flag}, CompositeList: []PendingAskCall{{TurnID: "turn-token", CallID: "call-token"}}}
	b := CloneAttentionEntry(a)
	if !EqualAttentionEntry(a, b) {
		t.Fatal("independent copies differ")
	}
	*b.PendingQuestion.Complete = true
	b.CompositeList[0].CallID = "other-token"
	if *a.PendingQuestion.Complete || a.CompositeList[0].CallID != "call-token" || EqualAttentionEntry(a, b) {
		t.Fatal("attention clone aliases mutable facts or equality ignored them")
	}
}
