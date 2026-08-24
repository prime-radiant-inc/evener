package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// regTool builds a RegisteredTool with a no-op executor so registration succeeds.
func regTool(def llm.ToolDefinition) tool.RegisteredTool {
	return tool.RegisteredTool{
		Definition: def,
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

func TestExecTool_DelegateRejectsUnsupportedWaitWithoutStarting(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "max_wait_ms", args: `{"task":"must not start","max_wait_ms":1000}`},
		{name: "block", args: `{"task":"must not start","block":true}`},
		{name: "block_timeout_ms", args: `{"task":"must not start","block_timeout_ms":1000}`},
		{name: "background", args: `{"task":"must not start","background":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(s.delegateController.Snapshot().rows)
			res := s.execTool(context.Background(), llm.ToolCallData{
				ID:        "delegate-unsupported-" + tc.name,
				Name:      "delegate",
				Arguments: json.RawMessage(tc.args),
			}, "")
			if !res.IsError {
				t.Fatalf("delegate with unsupported %s succeeded: %s", tc.name, res.FullOutput)
			}
			if !strings.Contains(res.FullOutput, tc.name) {
				t.Fatalf("delegate error omitted unsupported parameter %s: %s", tc.name, res.FullOutput)
			}
			if got := len(s.delegateController.Snapshot().rows); got != before {
				t.Fatalf("invalid delegate call started %d delegate(s), want none", got-before)
			}
		})
	}
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

func TestPrepareToolCall_TaskListInheritEffortIsValid(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefTaskList([]string{"low", "medium", "high"}))); err != nil {
		t.Fatalf("register task_list: %v", err)
	}
	call := llm.ToolCallData{ID: "inherit", Name: "task_list",
		Arguments: json.RawMessage(`{"action":"append","tasks":[{"type":"implement","description":"step","prompt":"do it","reasoning_effort":"inherit"}]}`)}
	res := prepareToolCall(call, reg.Get("task_list"), []string{"task_list"}, "task_list", "")
	if res.PrevalErr != "" {
		t.Fatalf("inherit effort rejected: %s", res.PrevalErr)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("inherit effort unexpectedly repaired: %+v", res.Changes)
	}
}

// Regression guard: drives the REAL DefTaskList/DefAskUser definitions
// end-to-end through prepareToolCall so a future schema edit that drifts the
// repair package's hand-built fixtures out of sync fails loudly here, not
// silently in the leaf package's tests.
func TestPrepareToolCall_NestedSchemaErrors_NameRealFieldAndContainer(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefTaskList(nil))); err != nil {
		t.Fatalf("register task_list: %v", err)
	}
	if err := reg.Register(regTool(tool.DefAskUser())); err != nil {
		t.Fatalf("register ask_user: %v", err)
	}

	t.Run("task_list update missing status", func(t *testing.T) {
		call := llm.ToolCallData{ID: "c1", Name: "task_list",
			Arguments: json.RawMessage(`{"action":"update","updates":[{"id":1,"notes":"x"}]}`)}
		res := prepareToolCall(call, reg.Get("task_list"), []string{"task_list"}, "task_list", "")
		want := "task_list: missing required argument \"status\" in updates[0].\n" +
			"Required arguments in updates[0]: id (integer), status (string).\n" +
			"Example: {\"action\": \"...\"}"
		if res.PrevalErr != want {
			t.Fatalf("PrevalErr =\n%s\nwant:\n%s", res.PrevalErr, want)
		}
	})

	t.Run("ask_user question header too long", func(t *testing.T) {
		call := llm.ToolCallData{ID: "c2", Name: "ask_user",
			Arguments: json.RawMessage(`{"questions":[{"header":"way too long for a chip label","question":"q","options":[{"label":"a","detail":"a"},{"label":"b","detail":"b"}]}]}`)}
		res := prepareToolCall(call, reg.Get("ask_user"), []string{"ask_user"}, "ask_user", "")
		want := "ask_user: argument \"questions[0].header\" exceeds maxLength (12). Value \"way too long for a chip label\" is 29 characters."
		if res.PrevalErr != want {
			t.Fatalf("PrevalErr =\n%s\nwant:\n%s", res.PrevalErr, want)
		}
		if strings.Contains(res.PrevalErr, "Required arguments") {
			t.Fatalf("message must not include the generic required-arguments line: %q", res.PrevalErr)
		}
	})

	// Issue #193 repro: a header exceeding the documented maxLength (12)
	// must surface the actual constraint and value/length, not the misleading
	// "Required arguments" message that claims question/options were missing.
	t.Run("ask_user header Module & repo is 13 chars", func(t *testing.T) {
		call := llm.ToolCallData{ID: "c3", Name: "ask_user",
			Arguments: json.RawMessage(`{"questions":[{"header":"Module & repo","question":"q","options":[{"label":"a","detail":"a"},{"label":"b","detail":"b"}]}]}`)}
		res := prepareToolCall(call, reg.Get("ask_user"), []string{"ask_user"}, "ask_user", "")
		want := "ask_user: argument \"questions[0].header\" exceeds maxLength (12). Value \"Module & repo\" is 13 characters."
		if res.PrevalErr != want {
			t.Fatalf("PrevalErr =\n%s\nwant:\n%s", res.PrevalErr, want)
		}
	})

	// The RCA for issue #193 confirmed enum is a second, independently
	// affected constraint class: an invalid task_list action produced the
	// same misleading "has the wrong type or value" + "Required arguments:
	// action (string)." pair, even though action was present. Verify it
	// through the real DefTaskList schema, not a hand-built fixture.
	t.Run("task_list action bogus enum value", func(t *testing.T) {
		call := llm.ToolCallData{ID: "c4", Name: "task_list",
			Arguments: json.RawMessage(`{"action":"bogus"}`)}
		res := prepareToolCall(call, reg.Get("task_list"), []string{"task_list"}, "task_list", "")
		want := "task_list: argument \"action\" is not one of the allowed values: view, append, update. Value is \"bogus\"."
		if res.PrevalErr != want {
			t.Fatalf("PrevalErr =\n%s\nwant:\n%s", res.PrevalErr, want)
		}
		if strings.Contains(res.PrevalErr, "Required arguments") {
			t.Fatalf("message must not include the generic required-arguments line: %q", res.PrevalErr)
		}
	})
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
