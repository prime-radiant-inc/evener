package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

// Models routinely send "" or null for optional fields. delegate's isolation
// is an optional one-value enum whose absence is the documented default, so
// an empty value must reach the handler as absent instead of being rejected.
func TestPrepareToolCall_DelegateEmptyIsolationIsAbsent(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefDelegate([]string{"default"}))); err != nil {
		t.Fatalf("register: %v", err)
	}
	dt := reg.Get("delegate")
	for _, tc := range []struct{ name, args string }{
		{name: "empty", args: `{"prompt":"p","isolation":""}`},
		{name: "null", args: `{"prompt":"p","isolation":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := llm.ToolCallData{ID: "d-" + tc.name, Name: "delegate", Arguments: json.RawMessage(tc.args)}
			res := prepareToolCall(call, dt, []string{"delegate"}, "delegate", "communicate", "")
			if res.PrevalErr != "" {
				t.Fatalf("isolation %s rejected: %s", tc.name, res.PrevalErr)
			}
			var got map[string]any
			if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, present := got["isolation"]; present {
				t.Fatalf("isolation still present after repair: %v", got)
			}
			if len(res.Changes) != 1 || res.Changes[0].Kind != repair.ChangeNormalizeDefault || res.Changes[0].Field != "isolation" {
				t.Fatalf("changes = %+v, want one normalize_default on isolation", res.Changes)
			}
		})
	}
}

// A value that is not empty still has to be one of the allowed values.
func TestPrepareToolCall_DelegateUnknownIsolationStillRejected(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefDelegate([]string{"default"}))); err != nil {
		t.Fatalf("register: %v", err)
	}
	call := llm.ToolCallData{ID: "d", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"p","isolation":"none"}`)}
	res := prepareToolCall(call, reg.Get("delegate"), []string{"delegate"}, "delegate", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("isolation \"none\" accepted")
	}
}
