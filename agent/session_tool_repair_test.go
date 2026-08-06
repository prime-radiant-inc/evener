package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// regTool builds a RegisteredTool with a no-op executor so registration succeeds.
func regTool(def llm.ToolDefinition) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: def},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	}
}

func editTool(t *testing.T) *tool.RegisteredTool {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefEditFile())); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg.Get("edit_file")
}

func TestPrepareToolCall_AliasesArgs(t *testing.T) {
	et := editTool(t)
	call := llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"/x","old_str":"a","new_string":"b"}`)}
	res := prepareToolCall(call, et, []string{"edit_file"}, "edit_file", "")
	if res.PrevalErr != "" {
		t.Fatalf("unexpected prevalErr: %s", res.PrevalErr)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
		t.Fatalf("unmarshal healed: %v", err)
	}
	if got["old_string"] != "a" {
		t.Fatalf("not aliased: %v", got)
	}
	if len(res.Changes) == 0 {
		t.Fatal("expected changes recorded")
	}
}

func TestPrepareToolCall_UnknownTool(t *testing.T) {
	call := llm.ToolCallData{ID: "c1", Name: "reed_file", Arguments: json.RawMessage(`{}`)}
	res := prepareToolCall(call, nil, []string{"read_file", "edit_file"}, "reed_file", "")
	if res.PrevalErr == "" {
		t.Fatal("expected prevalErr for unknown tool")
	}
}

func TestPrepareToolCall_EmptyArgsValidForNoRequiredTool(t *testing.T) {
	reg := tool.NewRegistry()
	def := tool.DefListDir()
	_ = reg.Register(regTool(def)) // regTool: helper building a RegisteredTool with a no-op Exec (see below)
	res := prepareToolCall(
		llm.ToolCallData{ID: "c1", Name: "list_dir", Arguments: json.RawMessage(``)},
		reg.Get("list_dir"), []string{"list_dir"}, "list_dir", "")
	if res.PrevalErr != "" {
		t.Fatalf("empty args rejected: %s", res.PrevalErr)
	}
}

func TestPrepareToolCall_SynthesizesStableIDWhenEmpty(t *testing.T) {
	et := editTool(t)
	call := llm.ToolCallData{Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"/x","old_string":"a","new_string":"b"}`)}
	res := prepareToolCall(call, et, []string{"edit_file"}, "edit_file", "")
	if res.Call.ID == "" {
		t.Fatal("expected synthesized ID")
	}
}

// A length-stopped turn with unparseable args is truncation, not a JSON
// syntax problem: the error must say so, and no repair may run (a "healed"
// truncated write would silently write a truncated file).
func TestPrepareToolCall_TruncatedByLength(t *testing.T) {
	et := editTool(t)
	truncated := json.RawMessage(`{"file_path":"/x","old_string":"a","new_string":"unterminat`)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file", Arguments: truncated},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonLength)
	if res.PrevalErr == "" || !strings.Contains(res.PrevalErr, "truncated") {
		t.Fatalf("want truncation error, got: %q", res.PrevalErr)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("no repair may run on truncated args, got changes: %v", res.Changes)
	}
}

// A length cut can land before any argument byte streams. Empty args on a
// tool with required parameters would otherwise fail schema validation with
// "missing required field" — the same misdiagnosis the truncation message
// exists to prevent.
func TestPrepareToolCall_TruncatedBeforeAnyArgs(t *testing.T) {
	et := editTool(t)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(``)},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonLength)
	if res.PrevalErr == "" || !strings.Contains(res.PrevalErr, "truncated") {
		t.Fatalf("want truncation error, got: %q", res.PrevalErr)
	}
}

// Empty args on a length-stopped turn still execute when the tool requires
// nothing — an intentionally argument-free call is not evidence of truncation.
func TestPrepareToolCall_LengthStopEmptyArgsNoRequired(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(regTool(tool.DefListDir()))
	res := prepareToolCall(
		llm.ToolCallData{ID: "c1", Name: "list_dir", Arguments: json.RawMessage(``)},
		reg.Get("list_dir"), []string{"list_dir"}, "list_dir", llm.FinishReasonLength)
	if res.PrevalErr != "" {
		t.Fatalf("empty args rejected: %s", res.PrevalErr)
	}
}

// Valid JSON on a length-stopped turn executes normally — the truncation may
// have landed after this tool call closed.
func TestPrepareToolCall_LengthStopWithValidArgs(t *testing.T) {
	et := editTool(t)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"/x","old_string":"a","new_string":"b"}`)},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonLength)
	if res.PrevalErr != "" {
		t.Fatalf("valid args must execute: %q", res.PrevalErr)
	}
}

// A non-length stop keeps the existing invalid-JSON coaching path.
func TestPrepareToolCall_BrokenJSONNonLengthStop(t *testing.T) {
	et := editTool(t)
	res := prepareToolCall(llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path": nope}`)},
		et, []string{"edit_file"}, "edit_file", llm.FinishReasonStop)
	if res.PrevalErr == "" || !strings.Contains(res.PrevalErr, "not valid JSON") {
		t.Fatalf("want invalid-JSON error, got: %q", res.PrevalErr)
	}
}
