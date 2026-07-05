package repair

import (
	"encoding/json"
	"testing"
)

func TestRepairJSON_NoOpOnValid(t *testing.T) {
	in := []byte(`{"a":"b"}`)
	out, changes := RepairJSON(in)
	if string(out) != string(in) || changes != nil {
		t.Fatalf("out=%s changes=%+v", out, changes)
	}
}

func TestRepairJSON_LoneHighSurrogate(t *testing.T) {
	in := []byte(`{"s":"\uD800x"}`) // lone high surrogate, invalid JSON string content
	out, changes := RepairJSON(in)
	if len(changes) == 0 {
		t.Fatal("expected a change")
	}
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("repaired JSON still invalid: %v (%s)", err, out)
	}
	if m["s"] != "�x" {
		t.Fatalf("s = %q", m["s"])
	}
}

func TestRepairJSON_ValidSurrogatePairUntouched(t *testing.T) {
	in := []byte(`{"s":"😀"}`) // 😀, a valid pair
	out, changes := RepairJSON(in)
	if changes != nil {
		t.Fatalf("valid pair altered: %+v", changes)
	}
	if string(out) != string(in) {
		t.Fatalf("out=%s", out)
	}
}

func TestRepairJSON_BrokenEscape(t *testing.T) {
	in := []byte(`{"s":"\uZZ"}`) // \u not followed by 4 hex digits
	out, changes := RepairJSON(in)
	if len(changes) == 0 {
		t.Fatal("expected a change")
	}
	if _, err := json.Marshal(json.RawMessage(out)); err != nil {
		t.Fatalf("output not marshalable: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("repaired JSON still invalid: %v (%s)", err, out)
	}
}
