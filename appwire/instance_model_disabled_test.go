package appwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A per-model disable toggle rides InstanceEntry as a model inventory, and a
// new instance method flips one row. The toggle writes an explicit bool on
// the row (never deletes), so the choice survives catalog refreshes.
func TestInstanceModelDisabledWire(t *testing.T) {
	in := InstanceListResponse{
		Instances: []InstanceEntry{{
			Name: "work", ProviderID: "openai", Protocol: "openai-chat", Auth: "bearer",
			ActiveSource: "env:WORK_KEY", CredentialRequired: true,
			Models: []InstanceModelEntry{{ID: "gpt-5.5"}, {ID: "gpt-4o", Disabled: true}},
		}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"models":[{"id":"gpt-5.5"},{"id":"gpt-4o","disabled":true}]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out InstanceListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("roundtrip=%+v, want %+v", out, in)
	}
	params := InstanceSetModelDisabledParams{Name: "work", Model: "gpt-4o", Disabled: true}
	raw, err = json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if got := string(raw); got != `{"name":"work","model":"gpt-4o","disabled":true}` {
		t.Fatalf("params marshal=%s", got)
	}
}
