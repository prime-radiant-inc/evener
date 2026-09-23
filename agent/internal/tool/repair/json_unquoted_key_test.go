package repair

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// TestRepairJSON_UnquotedObjectKey pins the recovery for the observed
// session-034TMrIEL8VtHauQnvpW0I failure: a tool call whose arguments held a
// bare identifier object key ({"update":[..., {id: 2, ...}]}) failed strict
// parsing with "invalid character 'i' looking for beginning of object key
// string". RepairJSON must quote a key position holding
// [A-Za-z_][A-Za-z0-9_]* followed by ':', re-parse cleanly, and report the
// change — nothing more.
func TestRepairJSON_UnquotedObjectKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"observed fragment", `{id: 2}`, `{"id": 2}`},
		{"observed task_list shape", `{"update": [{"id": 1}, {id: 2, status: "in_progress"}]}`,
			`{"update": [{"id": 1}, {"id": 2, "status": "in_progress"}]}`},
		{"top-level key", `{value: "recovered"}`, `{"value": "recovered"}`},
		{"whitespace around colon", `{ id : 2 }`, `{ "id" : 2 }`},
		{"underscore and digits", `{_id1: 2}`, `{"_id1": 2}`},
		{"nested objects and arrays", `{"a":1,"b":{c:[{d:2}]}}`, `{"a":1,"b":{"c":[{"d":2}]}}`},
		{"identifier inside string value untouched", `{"msg": "note id: 5", id: 2}`,
			`{"msg": "note id: 5", "id": 2}`},
		{"multiple keys in one object", `{add: [{type: "implement", description: "d", prompt: "p"}]}`,
			`{"add": [{"type": "implement", "description": "d", "prompt": "p"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changes := RepairJSON([]byte(tc.in))
			if string(out) != tc.want {
				t.Fatalf("RepairJSON(%q) = %q, want %q", tc.in, out, tc.want)
			}
			if !json.Valid(out) {
				t.Fatalf("repaired %q is not valid JSON", out)
			}
			if len(changes) != 1 || changes[0].Kind != ChangeQuoteObjectKey {
				t.Fatalf("changes = %+v, want exactly one %s change", changes, ChangeQuoteObjectKey)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("repaired %q does not parse: %v", out, err)
			}
		})
	}
}

// TestRepairJSON_UnquotedKeyCombinedWithEscapeRepair proves the key-quoting
// pass composes with the existing broken-escape repair under the shared
// json.Valid gate: both fixes apply and the result parses.
func TestRepairJSON_UnquotedKeyCombinedWithEscapeRepair(t *testing.T) {
	in := `{"a":"\u12", id: 2}`
	out, changes := RepairJSON([]byte(in))
	if !json.Valid(out) {
		t.Fatalf("repaired %q is not valid JSON", out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("repaired %q does not parse: %v", out, err)
	}
	want := map[string]any{"a": "�", "id": 2.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repaired = %#v, want %#v", got, want)
	}
	kinds := map[ChangeKind]bool{}
	for _, c := range changes {
		kinds[c.Kind] = true
	}
	if !kinds[ChangeUnicodeRepair] || !kinds[ChangeQuoteObjectKey] {
		t.Fatalf("changes = %+v, want both %s and %s", changes, ChangeUnicodeRepair, ChangeQuoteObjectKey)
	}
}

// TestRepairJSON_UnquotedKeyCombinedWithSurrogateRepair proves the
// key-quoting pass also composes with the lone-surrogate repair: both fixes
// apply under the shared json.Valid gate and the result parses.
func TestRepairJSON_UnquotedKeyCombinedWithSurrogateRepair(t *testing.T) {
	in := `{"a":"\ud800", id: 2}`
	out, changes := RepairJSON([]byte(in))
	if !json.Valid(out) {
		t.Fatalf("repaired %q is not valid JSON", out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("repaired %q does not parse: %v", out, err)
	}
	want := map[string]any{"a": "�", "id": 2.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repaired = %#v, want %#v", got, want)
	}
	kinds := map[ChangeKind]bool{}
	for _, c := range changes {
		kinds[c.Kind] = true
	}
	if !kinds[ChangeUnicodeRepair] || !kinds[ChangeQuoteObjectKey] {
		t.Fatalf("changes = %+v, want both %s and %s", changes, ChangeUnicodeRepair, ChangeQuoteObjectKey)
	}
}

// TestRepairJSON_UnquotedKeyBesideKeyLikeTextInString is the shepherd-round-1
// regression (PR #2162): a legitimate bare key alongside syntax-like text
// inside a string value must still repair. Rewriting every regex match
// together used to break the string, and the json.Valid gate then discarded
// the legitimate repair along with the bogus one — the tool call failed
// where a structure-aware repair succeeds.
func TestRepairJSON_UnquotedKeyBesideKeyLikeTextInString(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{msg: "example {id: 2}"}`, `{"msg": "example {id: 2}"}`},
		{`{"a": "x {id: 1", id: 2}`, `{"a": "x {id: 1", "id": 2}`},
		{`{"msg": "note, id: 5", id: 2}`, `{"msg": "note, id: 5", "id": 2}`},
		{`{"a": "b\" c, d: 1", e: 2}`, `{"a": "b\" c, d: 1", "e": 2}`},
		{`{ msg: "a, id: 1" }`, `{ "msg": "a, id: 1" }`},
		{`{"a": "x {id: 1", id: 2, "b": "y {k: 1}", c: 3}`,
			`{"a": "x {id: 1", "id": 2, "b": "y {k: 1}", "c": 3}`},
	} {
		t.Run(tc.in, func(t *testing.T) {
			out, changes := RepairJSON([]byte(tc.in))
			if string(out) != tc.want {
				t.Fatalf("RepairJSON(%q) = %q, want %q", tc.in, out, tc.want)
			}
			if !json.Valid(out) {
				t.Fatalf("repaired %q is not valid JSON", out)
			}
			if len(changes) != 1 || changes[0].Kind != ChangeQuoteObjectKey {
				t.Fatalf("changes = %+v, want exactly one %s change", changes, ChangeQuoteObjectKey)
			}
		})
	}
}

// TestRepairJSON_UnquotedKeyScopeGuard pins what the repair must NOT do:
// bare values, single-quoted keys, trailing commas, comments, digit-leading
// keys, keys without colons, and inputs needing a second kind of fix (missing
// outer brace) all stay untouched. No general JSON slop repair.
func TestRepairJSON_UnquotedKeyScopeGuard(t *testing.T) {
	for _, in := range []string{
		`{"update": id}`,       // bare identifier VALUE, not a key
		`{"value": broken`,     // existing malformed-test fragment: bare value
		`{'a': 1}`,             // single-quoted key
		`{"a":1,}`,             // trailing comma
		`{"a":1} // c`,         // comment
		`{id 2}`,               // key not followed by a colon
		`{2id: 1}`,             // digit-leading key
		`{"a":1, b: 2`,         // unquoted key AND missing outer brace: never combined
		`{id: 2} trailing`,     // repaired form still invalid: gate must refuse
		`{"id": 2}`,            // valid JSON passes through unchanged
		`{"a": "x {id: 2} y"}`, // identifier inside a string value only
		`{"a":"\q", id: 2}`,    // unrepairable escape corrupts the result: gate must refuse
	} {
		t.Run(in, func(t *testing.T) {
			out, changes := RepairJSON([]byte(in))
			if !bytes.Equal(out, []byte(in)) || len(changes) != 0 {
				t.Fatalf("RepairJSON(%q) = (%q, %+v), want unchanged", in, out, changes)
			}
		})
	}
}
