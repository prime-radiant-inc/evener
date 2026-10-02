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
