package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// failurePartitionSession builds a real session whose plugin carries one
// PostToolUse hook and one PostToolUseFailure hook. The success hook appends
// "success" to the sentinel file; the failure hook appends "failure" and then
// copies its stdin (the hook input JSON) to the capture file so a test can
// decode exactly what the event delivered. Both hooks are ordinary `bash -c`
// commands, so the test proves the partition through the same process boundary
// production uses.
func failurePartitionSession(t *testing.T) (sess *Session, sentinel, capture string) {
	t.Helper()
	control := t.TempDir()
	sentinel = filepath.Join(control, "sentinel.txt")
	capture = filepath.Join(control, "failure-input.json")

	quote := func(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }
	successCmd := fmt.Sprintf("printf 'success\\n' >> %s", quote(sentinel))
	failureCmd := fmt.Sprintf("printf 'failure\\n' >> %s; cat >> %s; echo >> %s",
		quote(sentinel), quote(capture), quote(capture))

	hooksJSON, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{"matcher": "*", "hooks": []any{
					map[string]any{"type": "command", "timeout": 10, "command": successCmd},
				}},
			},
			"PostToolUseFailure": []any{
				map[string]any{"matcher": "*", "hooks": []any{
					map[string]any{"type": "command", "timeout": 10, "command": failureCmd},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal hooks.json: %v", err)
	}

	dir := writePluginHooks(t, "failure-partition-plugin", string(hooksJSON))
	client := llm.NewClient()
	sess, err = NewSession(client, NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{PluginDirs: []string{dir}, testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, sentinel, capture
}

// TestPostToolUseFailure_FiresForExecutedFailureOnly proves Claude's partition
// from a real session: an executed tool failure fires PostToolUseFailure only,
// an executed success fires PostToolUse only, and a call refused before
// execution fires neither.
//
// Two refusals are covered because a real session refuses at two layers: the
// session's repair step (PrevalOnly) catches an unknown name, and the registry's
// breaker park (Rejected) catches a repeated identical failure. Each flag is a
// live guard in this test: blanking either one adds a sentinel line.
//
// Coverage boundary: the registry's own unknown-tool gate is unreachable from
// execTool (prepareToolCall refuses first), so its Rejected assignment is
// covered only by TestRegistry_MarksPreExecutionRejections in
// agent/internal/tool. This test's Rejected case is the breaker park.
//
// The failing call is read_file on a missing path, not `bash -c 'exit 3'`: a
// nonzero shell exit is deliberately NOT an error in evener (a shell call that
// ran and exited nonzero is not a failure — see agent/transcript/failures.go),
// so it cannot stand for a failed tool. read_file runs, touches the filesystem,
// and returns a genuine error (IsError=true, PrevalOnly=false).
func TestPostToolUseFailure_FiresForExecutedFailureOnly(t *testing.T) {
	sess, sentinel, capture := failurePartitionSession(t)
	missing := filepath.Join(t.TempDir(), "missing.txt")
	failingRead := llm.ToolCallData{
		ID:        "call_failure",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"` + missing + `"}`),
	}

	failRes := sess.execTool(context.Background(), failingRead, "")
	if !failRes.IsError {
		t.Fatalf("missing-file read = %+v, want IsError", failRes)
	}
	if failRes.PrevalOnly || failRes.Rejected {
		t.Fatalf("missing-file read = %+v, want an executed failure (PrevalOnly=false, Rejected=false)", failRes)
	}

	// A second identical failure builds the breaker's failure streak. It is
	// still an executed failure, so it fires PostToolUseFailure like the first.
	secondRes := sess.execTool(context.Background(), failingRead, "")
	if !secondRes.IsError || secondRes.Rejected {
		t.Fatalf("second identical failure = %+v, want an executed failure", secondRes)
	}

	okRes := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_success",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"echo ok"}`),
	}, "")
	if okRes.IsError {
		t.Fatalf("successful shell = %+v, want no error", okRes)
	}

	unknownRes := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_unknown",
		Name:      "no_such_tool_xyz",
		Arguments: json.RawMessage(`{}`),
	}, "")
	// The session's repair step refuses an unknown tool before dispatch, so it
	// arrives as PrevalOnly; the registry's own unknown-tool gate (Rejected) is
	// what covers a direct ExecuteCall. Both must keep the tool events quiet.
	if !unknownRes.IsError || !(unknownRes.PrevalOnly || unknownRes.Rejected) {
		t.Fatalf("unknown tool = %+v, want IsError and a pre-execution refusal (PrevalOnly or Rejected)", unknownRes)
	}

	// The third identical failure is refused by the breaker before dispatch.
	parkedRes := sess.execTool(context.Background(), failingRead, "")
	if !parkedRes.IsError || !parkedRes.Rejected {
		t.Fatalf("parked repeat = %+v, want IsError and Rejected", parkedRes)
	}

	raw, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	got := strings.Fields(string(raw))
	want := []string{"failure", "failure", "success"}
	if len(got) != len(want) {
		t.Fatalf("hook sentinels = %v, want exactly %v: the failure event fires only for an executed failure, the success event only for a success, and a pre-execution refusal fires neither", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hook sentinels = %v, want exactly %v", got, want)
		}
	}

	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured hook input: %v", err)
	}
	var input struct {
		HookEventName string         `json:"hook_event_name"`
		ToolName      string         `json:"tool_name"`
		ToolUseID     string         `json:"tool_use_id"`
		ToolInput     map[string]any `json:"tool_input"`
		Error         string         `json:"error"`
		DurationMS    *int64         `json:"duration_ms"`
		IsInterrupt   *bool          `json:"is_interrupt"`
	}
	// The hook ran twice (two executed failures), so the capture holds one
	// input JSON per line; the first is the one this assertion set describes.
	firstLine, _, _ := strings.Cut(strings.TrimSpace(string(captured)), "\n")
	if err := json.Unmarshal([]byte(firstLine), &input); err != nil {
		t.Fatalf("decode captured hook input: %v (raw: %s)", err, firstLine)
	}
	if input.HookEventName != "PostToolUseFailure" {
		t.Errorf("hook_event_name = %q, want PostToolUseFailure", input.HookEventName)
	}
	if input.ToolName != "Read" {
		t.Errorf("tool_name = %q, want the Claude name Read", input.ToolName)
	}
	if input.ToolUseID != "call_failure" {
		t.Errorf("tool_use_id = %q, want call_failure", input.ToolUseID)
	}
	// The error is the model-facing text (ExecResult.Output), which is what
	// Claude means by "the text Claude receives as the failed tool's result".
	if input.Error != failRes.Output {
		t.Errorf("error = %q, want the model-facing output %q", input.Error, failRes.Output)
	}
	if input.Error == "" {
		t.Error("error must not be empty for a failed tool")
	}
	if input.DurationMS == nil {
		t.Error("duration_ms must be present")
	} else if *input.DurationMS != failRes.DurationMS {
		t.Errorf("duration_ms = %d, want %d", *input.DurationMS, failRes.DurationMS)
	}
	if input.IsInterrupt == nil || *input.IsInterrupt {
		t.Errorf("is_interrupt = %v, want false (a real cancel never reaches this point)", input.IsInterrupt)
	}
	if got := input.ToolInput["file_path"]; got != missing {
		t.Errorf("tool_input.file_path = %v, want %q", got, missing)
	}
}

// TestPostToolUseFailure_ShellNonzeroExitFiresPostToolUseNotFailure pins a
// deliberate divergence from Claude, not an accident.
//
// Claude fires PostToolUseFailure when a Bash command exits nonzero. Evener
// fires PostToolUse, because a shell call that ran and exited nonzero is a
// SUCCESS here: "A SHELL call that ran and exited nonzero is NOT one of them
// (Jesse, 2026-10-16). A nonzero exit is common and expected" — the rule in
// agent/transcript/failures.go (FailedToolResult), which also drives the
// transcript glyphs, the failure counts, and loop detection. Making a nonzero
// exit an error just to fire PostToolUseFailure would change all three.
//
// Both halves are asserted for the SAME nonzero-exit call: the PostToolUse
// hook fires and the PostToolUseFailure hook does not. If evener's shell
// semantics ever change, this test names it.
func TestPostToolUseFailure_ShellNonzeroExitFiresPostToolUseNotFailure(t *testing.T) {
	sess, sentinel, capture := failurePartitionSession(t)

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_exit3",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"exit 3"}`),
	}, "")
	if res.IsError {
		t.Fatalf("nonzero shell exit = %+v, want a success result", res)
	}

	raw, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	got := strings.Fields(string(raw))
	if len(got) != 1 || got[0] != "success" {
		t.Fatalf("hook sentinels = %v, want exactly [success]: a nonzero shell exit must fire PostToolUse and not PostToolUseFailure (Claude does the opposite — see the test comment)", got)
	}
	if slices.Contains(got, "failure") {
		t.Fatalf("hook sentinels = %v: PostToolUseFailure fired for a nonzero shell exit", got)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("failure hook ran for a nonzero shell exit (capture exists): %v", err)
	}
}
