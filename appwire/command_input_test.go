package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCommandInputCanonicalSelection(t *testing.T) {
	want := []InputItem{{Type: "text", Text: "OPAQUE_318"}, {Type: "skill", Name: "pkg:probe"}, {Type: "command", Name: "pkg:probe"}, {Type: "command", Name: "pkg:probe"}}
	got, err := NormalizeMutationInput(want)
	if err != nil || !reflect.DeepEqual(got.Items, want) {
		t.Fatalf("normalized=%+v error=%v", got, err)
	}
	for _, field := range []string{`"arguments":""`, `"body":"x"`, `"text":""`, `"path":""`, `"metadata":{}`} {
		var item InputItem
		if err := json.Unmarshal([]byte(`{"type":"command","name":"pkg:probe",`+field+`}`), &item); err == nil {
			t.Fatalf("accepted extra field %s", field)
		}
	}
	for _, item := range []InputItem{{Type: "command"}, {Type: "command", Name: " "}, {Type: "command", Name: "pkg:probe", Text: "x"}} {
		if _, err := NormalizeMutationInput([]InputItem{item}); err == nil {
			t.Fatalf("accepted invalid command=%+v", item)
		}
	}
}

func TestCommandInputQueueClone(t *testing.T) {
	original := Thread{Evener: EvenerThread{Queue: QueueState{CommandNames: [][]string{{"probe"}, {"pkg:probe"}}}}}
	cloned := CloneThread(original)
	cloned.Evener.Queue.CommandNames[0][0] = "changed"
	cloned.Evener.Queue.CommandNames[1] = append(cloned.Evener.Queue.CommandNames[1], "other")
	if original.Evener.Queue.CommandNames[0][0] != "probe" || len(original.Evener.Queue.CommandNames[1]) != 1 {
		t.Fatal("command selections alias cloned queue")
	}
}

func TestCommandInputSupport(t *testing.T) {
	items := []InputItem{{Type: "command", Name: "probe"}}
	if err := ValidateCommandInputSupport(items, false); err == nil {
		t.Fatal("accepted selection without target command support")
	}
	if err := ValidateCommandInputSupport(items, true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommandInputSupport([]InputItem{{Type: "text", Text: "DATA_318 /probe"}}, false); err != nil {
		t.Fatal(err)
	}
}

func TestInputMentionsEditingMetadata(t *testing.T) {
	t.Parallel()
	want := []InputItem{{Type: "text", Text: "😀 /same /same /same", Mentions: []InputMention{{Kind: "command", Name: "same", Offset: 3}, {Kind: "skill", Name: "same", Offset: 9}}}, {Type: "command", Name: "same"}, {Type: "skill", Name: "same"}}
	normalized, err := NormalizeMutationInput(want)
	if err != nil || !reflect.DeepEqual(normalized.Items, want) {
		t.Fatalf("normalize = %+v, %v", normalized, err)
	}
	normalized.Items[0].Mentions[0].Offset = 0
	if want[0].Mentions[0].Offset != 3 {
		t.Fatal("input mentions alias normalized copy")
	}
	thread := Thread{Evener: EvenerThread{Queue: QueueState{Mentions: [][]InputMention{want[0].Mentions}}}}
	clone := CloneThread(thread)
	clone.Evener.Queue.Mentions[0][0].Name = "changed"
	if thread.Evener.Queue.Mentions[0][0].Name != "same" {
		t.Fatal("queue mentions alias clone")
	}
	for _, mentions := range [][]InputMention{
		{{Kind: "command", Name: "same", Offset: -1}},
		{{Kind: "command", Name: "same", Offset: 2}},
		{{Kind: "command", Name: "missing", Offset: 3}},
		{{Kind: "body", Name: "same", Offset: 3}},
		{{Kind: "command", Name: "same", Offset: int(^uint(0) >> 1)}},
		{{Kind: "command", Name: "same", Offset: 3}, {Kind: "skill", Name: "same", Offset: 3}},
	} {
		input := append([]InputItem(nil), want...)
		input[0].Mentions = mentions
		if _, err := NormalizeMutationInput(input); err == nil {
			t.Fatalf("accepted invalid mentions: %+v", mentions)
		}
	}
	if _, err := NormalizeMutationInput([]InputItem{{Type: "text", Text: "/same", Mentions: []InputMention{{Kind: "command", Name: "same", Offset: 0}}}}); err == nil {
		t.Fatal("editing metadata authorized unselected command")
	}
}
