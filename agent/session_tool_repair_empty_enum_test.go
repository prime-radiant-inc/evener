package agent

import (
	"encoding/json"
	"reflect"
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

// The empty-enum repair reaches enums nested in array items, so a model that
// materializes task_list.add[].reasoning_effort as "" gets the default too.
func TestPrepareToolCall_TaskListNestedEmptyEnumIsAbsent(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefTaskList([]string{"low", "medium", "high"}))); err != nil {
		t.Fatalf("register: %v", err)
	}
	call := llm.ToolCallData{ID: "tl", Name: "task_list", Arguments: json.RawMessage(
		`{"add":[{"type":"implement","description":"d","prompt":"p","reasoning_effort":""}]}`)}
	res := prepareToolCall(call, reg.Get("task_list"), []string{"task_list"}, "task_list", "communicate", "")
	if res.PrevalErr != "" {
		t.Fatalf("nested empty reasoning_effort rejected: %s", res.PrevalErr)
	}
	var got struct {
		Add []map[string]any `json:"add"`
	}
	if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got.Add[0]["reasoning_effort"]; present {
		t.Fatalf("reasoning_effort still present after repair: %v", got.Add[0])
	}
	if len(res.Changes) != 1 || res.Changes[0].Field != "add[0].reasoning_effort" {
		t.Fatalf("changes = %+v, want one change on add[0].reasoning_effort", res.Changes)
	}
}

// An artifact ref rejects any explicit format, the empty one included
// (TestArtifactExplicitFormatsRemainRejected), so the empty-enum repair must
// leave format for the handler's contract instead of applying the default.
func TestPrepareToolCall_ArtifactEmptyFormatStillRejected(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefReadTranscript())); err != nil {
		t.Fatalf("register: %v", err)
	}
	call := llm.ToolCallData{ID: "rt", Name: "read_transcript", Arguments: json.RawMessage(
		`{"transcript_ref":"artifact:abc","format":""}`)}
	res := prepareToolCall(call, reg.Get("read_transcript"), []string{"read_transcript"}, "read_transcript", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("artifact ref with format \"\" accepted; args = %s", res.Call.Arguments)
	}
}

// A model that sends a plain optional field as null means to leave it out, so
// read_file's optional offset, limit and vision_prompt reach the handler as
// absent. The required file_path sent as null is still refused.
func TestPrepareToolCall_ReadFileNullOptionalsAreAbsent(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefReadFile())); err != nil {
		t.Fatalf("register: %v", err)
	}
	rf := reg.Get("read_file")
	call := llm.ToolCallData{ID: "rf", Name: "read_file", Arguments: json.RawMessage(
		`{"file_path":"a.txt","offset":null,"limit":null,"vision_prompt":null}`)}
	res := prepareToolCall(call, rf, []string{"read_file"}, "read_file", "communicate", "")
	if res.PrevalErr != "" {
		t.Fatalf("null optionals rejected: %s", res.PrevalErr)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := map[string]any{"file_path": "a.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if len(res.Changes) != 3 {
		t.Fatalf("changes = %+v, want one normalize_default per null optional", res.Changes)
	}

	required := llm.ToolCallData{ID: "rf-req", Name: "read_file", Arguments: json.RawMessage(`{"file_path":null}`)}
	if res := prepareToolCall(required, rf, []string{"read_file"}, "read_file", "communicate", ""); res.PrevalErr == "" {
		t.Fatalf("required file_path=null accepted; args = %s", res.Call.Arguments)
	}
}
