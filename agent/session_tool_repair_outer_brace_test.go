package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

func TestPrepareToolCall_MissingOuterBrace(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefCommunicate())); err != nil {
		t.Fatal(err)
	}
	raw := `{"end_turn":true,"message":"OK","output":{"artifacts":[],"data":{},"message":""}`
	for _, tc := range []struct {
		name, args, finish string
		repaired           bool
	}{
		{"completed", raw, "", true},
		{"length stopped", raw, llm.FinishReasonLength, false},
		{"empty object", `{}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: json.RawMessage(tc.args)}, reg.Get("communicate"), []string{"communicate"}, "communicate", "communicate", tc.finish)
			if !tc.repaired {
				if res.PrevalErr == "" || len(res.Changes) != 0 || string(res.Call.Arguments) != tc.args {
					t.Fatalf("expected unchanged rejected call: %+v", res)
				}
				return
			}
			if res.PrevalErr != "" {
				t.Fatal(res.PrevalErr)
			}
			if len(res.Changes) != 1 || res.Changes[0].Kind != repair.ChangeMissingOuterBrace {
				t.Fatalf("changes: %+v", res.Changes)
			}
			var got, want any
			if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw+"}"), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments changed: got %#v want %#v", got, want)
			}
		})
	}
}
