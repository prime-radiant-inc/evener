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

// TestRepairJSON_AdjacentBrokenEscapes_NoFalseSuccess covers a case the
// single-pass broken-escape repair cannot fix: two adjacent broken \u
// escapes. The naive repair still leaves invalid JSON, so RepairJSON must
// not claim success (non-nil changes) for a result that is still broken.
func TestRepairJSON_AdjacentBrokenEscapes_NoFalseSuccess(t *testing.T) {
	in := []byte(`{"s":"\u\u12"}`)
	out, changes := RepairJSON(in)
	if changes != nil {
		t.Fatalf("expected no changes (false success), got %+v (out=%s)", changes, out)
	}
	if string(out) != string(in) {
		t.Fatalf("expected raw input returned unchanged, got %s", out)
	}
}

// TestRepairJSON_EscapedBackslashU_NotCorrupted covers a valid JSON string
// containing an escaped backslash followed by a literal "u" (\\u), which the
// broken-escape regex can misidentify as an incomplete \u escape since RE2
// has no lookbehind to check escape parity. RepairJSON must never turn valid
// JSON into invalid JSON.
func TestRepairJSON_EscapedBackslashU_NotCorrupted(t *testing.T) {
	in := []byte(`{"s":"a\\ub"}`) // valid JSON; decodes to literal `a\ub`
	out, changes := RepairJSON(in)
	if !json.Valid(out) {
		t.Fatalf("repair corrupted valid JSON into invalid JSON: %s", out)
	}
	if changes != nil {
		t.Fatalf("expected no changes for already-valid input, got %+v (out=%s)", changes, out)
	}
	if string(out) != string(in) {
		t.Fatalf("expected input returned unchanged, got %s", out)
	}
}
