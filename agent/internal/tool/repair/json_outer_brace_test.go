package repair

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRepairJSON_MissingOuterBrace(t *testing.T) {
	for _, in := range []string{
		`{"end_turn":true,"message":"OK","output":{"artifacts":[],"data":{},"message":""}`,
		`{"s":"done"`, `{"n":12`, `{"b":false`, `{"v":null`, `{"a":[]`, `{"o":{}`,
		" \n{\"a\":1\t\n", `{"s":"} [ \\"`, `{"s":"\uD800"`,
	} {
		t.Run(in, func(t *testing.T) {
			raw := []byte(in)
			out, changes := RepairJSON(raw)
			if string(out) != in+"}" || !json.Valid(out) {
				t.Fatalf("got %q; want sole appended brace", out)
			}
			if len(changes) != 1 || changes[0].Kind != ChangeKind("missing_outer_brace") {
				t.Fatalf("changes = %+v", changes)
			}
			if string(raw) != in {
				t.Fatalf("input mutated: %q", raw)
			}
		})
	}
}

func TestRepairJSON_MissingOuterBraceRefused(t *testing.T) {
	for _, in := range []string{
		``, `{`, " { \n", `{}`, `{"a":1}`, `[]`, `[1`, `[{"a":1`, `null`,
		`{"a"`, `{"a":`, `{"a":tru`, `{"a":1e`, `{"a":1,`,
		`{"a":"unfinished`, `{"a":"escape\`, `{"a":{`, `{"a":{"b":1`, `{"a":[1`,
		`{"a":"\u12"`, `{"a":"\q"`, `{"a":1} garbage`, `{"a":1} {`,
	} {
		t.Run(in, func(t *testing.T) {
			out, changes := RepairJSON([]byte(in))
			if !bytes.Equal(out, []byte(in)) || len(changes) != 0 {
				t.Fatalf("got %q, %+v; want unchanged", out, changes)
			}
		})
	}
}
