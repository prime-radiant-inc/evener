package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

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

// A required array sent as null is refused as that field, never coerced into
// [null] and then blamed on its first item.
func TestPrepareToolCall_RequiredArrayNullIsReportedAsTheField(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefAskUser())); err != nil {
		t.Fatalf("register: %v", err)
	}
	call := llm.ToolCallData{ID: "au", Name: "ask_user", Arguments: json.RawMessage(`{"questions":null}`)}
	res := prepareToolCall(call, reg.Get("ask_user"), []string{"ask_user"}, "ask_user", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("required questions=null accepted; args = %s", res.Call.Arguments)
	}
	if !strings.Contains(res.PrevalErr, `"questions"`) || strings.Contains(res.PrevalErr, "questions[0]") {
		t.Fatalf("questions=null error = %q, want it to name questions itself, not questions[0]", res.PrevalErr)
	}
}
