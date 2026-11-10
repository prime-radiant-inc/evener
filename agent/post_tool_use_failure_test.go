package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
//
// A PreToolUse hook denies any grep call, so the tests can prove a post-hoc
// deny fires neither tool-outcome event.
func failurePartitionSession(t *testing.T) (sess *Session, sentinel, capture string) {
	t.Helper()
	control := t.TempDir()
	sentinel = filepath.Join(control, "sentinel.txt")
	capture = filepath.Join(control, "failure-input.json")

	quote := func(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }
	successCmd := "printf 'success\\n' >> " + quote(sentinel)
	failureCmd := fmt.Sprintf("printf 'failure\\n' >> %s; cat >> %s; echo >> %s",
		quote(sentinel), quote(capture), quote(capture))
	preDenyCmd := `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"denied by partition test"}}'`

	hooksJSON, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"matcher": "Grep", "hooks": []any{
					map[string]any{"type": "command", "timeout": 10, "command": preDenyCmd},
				}},
			},
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
// Three refusals are covered because a real session refuses at three layers: a
// PreToolUse deny returns before the outcome events entirely, the session's
// repair step (PrevalOnly) catches an unknown name, and the registry's breaker
// park (Rejected) catches a repeated identical failure. Each is a live guard:
// blanking any of them, or routing a deny through the outcome switch, adds a
// sentinel line.
//
// Coverage boundary: the registry's own unknown-tool gate is unreachable from
// execTool (prepareToolCall refuses first), so its Rejected assignment is
// covered only by TestRegistry_MarksPreExecutionRejections in
// agent/internal/tool. This test's Rejected case is the breaker park.
//
// The failing call is a registered probe whose error text exceeds the default
// 20 000-char limit, so its model-facing Output is SHORTER than FullOutput.
// That is what makes the `error` assertion able to tell `res.Output` from
// `res.FullOutput`. It is not `bash -c 'exit 3'` because a nonzero shell exit is
// deliberately NOT an error in evener (agent/transcript/failures.go).
func TestPostToolUseFailure_FiresForExecutedFailureOnly(t *testing.T) {
	sess, sentinel, capture := failurePartitionSession(t)
	// 25 000 chars of error text, over the default 20 000-char tool limit, so
	// the registry truncates the model-facing Output but keeps FullOutput whole.
	longError := strings.Repeat("e", 25_000)
	// The probe shadows edit_file, whose default limit is 10 000 chars, so the
	// 25 000-char error truncates. The name also maps to Claude's "Edit", which
	// is what the tool_name assertion checks.
	sess.RegisterTool("edit_file", "fails with a long error",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"payload": map[string]any{"type": "string"}},
		},
		func(context.Context, any) (any, error) { return nil, errors.New(longError) })
	failingProbe := llm.ToolCallData{
		ID:        "call_failure",
		Name:      "edit_file",
		Arguments: json.RawMessage(`{"payload":"x"}`),
	}

	failRes := sess.execTool(context.Background(), failingProbe, "")
	if !failRes.IsError {
		t.Fatalf("failure probe = %+v, want IsError", failRes)
	}
	if failRes.PrevalOnly || failRes.Rejected {
		t.Fatalf("failure probe = %+v, want an executed failure (PrevalOnly=false, Rejected=false)", failRes)
	}
	if len(failRes.Output) >= len(failRes.FullOutput) {
		t.Fatalf("output %d chars, full %d: the failure must be truncated for the Output-vs-FullOutput assertion to mean anything",
			len(failRes.Output), len(failRes.FullOutput))
	}

	// A second identical failure builds the breaker's failure streak. It is
	// still an executed failure, so it fires PostToolUseFailure like the first.
	secondRes := sess.execTool(context.Background(), failingProbe, "")
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
	if !unknownRes.IsError || (!unknownRes.PrevalOnly && !unknownRes.Rejected) {
		t.Fatalf("unknown tool = %+v, want IsError and a pre-execution refusal (PrevalOnly or Rejected)", unknownRes)
	}

	// A PreToolUse hook denies this grep. The deny returns inside the PreToolUse
	// block, before the outcome switch, so it must fire neither event even
	// though its result is IsError with PrevalOnly=false and Rejected=false.
	deniedRes := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_denied",
		Name:      "grep",
		Arguments: json.RawMessage(`{"pattern":"x"}`),
	}, "")
	if !deniedRes.IsError {
		t.Fatalf("denied grep = %+v, want IsError", deniedRes)
	}
	if deniedRes.PrevalOnly || deniedRes.Rejected {
		t.Fatalf("denied grep = %+v, want a plain deny result; only the early return keeps it quiet", deniedRes)
	}
	if deniedRes.Output != "denied by partition test" {
		t.Fatalf("denied grep output = %q, want the PreToolUse hook's deny reason", deniedRes.Output)
	}

	// The third identical failure is refused by the breaker before dispatch.
	parkedRes := sess.execTool(context.Background(), failingProbe, "")
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
		t.Fatalf("hook sentinels = %v, want exactly %v: the failure event fires only for an executed failure, the success event only for a success, and a PreToolUse deny, a PrevalOnly refusal, or a breaker park fires neither", got, want)
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
	if input.ToolName != "Edit" {
		t.Errorf("tool_name = %q, want the Claude name Edit", input.ToolName)
	}
	if input.ToolUseID != "call_failure" {
		t.Errorf("tool_use_id = %q, want call_failure", input.ToolUseID)
	}
	// The error is the model-facing text (ExecResult.Output), which is what
	// Claude means by "the text Claude receives as the failed tool's result" —
	// NOT the untruncated FullOutput. The call's error is longer than the limit,
	// so these two differ and the assertion can tell them apart.
	if input.Error != failRes.Output {
		t.Errorf("error = %q, want the model-facing output %q", input.Error, failRes.Output)
	}
	if input.Error == failRes.FullOutput {
		t.Error("error equals FullOutput; the hook must receive the truncated model-facing Output")
	}
	if len(input.Error) >= len(failRes.FullOutput) {
		t.Errorf("len(error) = %d, want shorter than FullOutput's %d", len(input.Error), len(failRes.FullOutput))
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
	if got := input.ToolInput["payload"]; got != "x" {
		t.Errorf("tool_input.payload = %v, want %q", got, "x")
	}
}

// TestPostToolUseFailure_ShellNonzeroExitFiresPostToolUseNotFailure pins a
// deliberate divergence from Claude, not an accident.
//
// Claude fires PostToolUseFailure when a Bash command exits nonzero. Evener
// fires PostToolUse, because a shell call that ran and exited nonzero is a
// SUCCESS here: "A SHELL call that ran and exited nonzero is NOT one of them
// (Jesse, 2026-10-16). A nonzero exit is common and expected" — the rule in
// agent/transcript/failures.go. The same rule as FailedToolResult drives the
// transcript glyphs and the failure counts, and the IsError flag it reads is
// what loop detection consults (agent/session_tool_round.go). Making a nonzero
// exit an error just to fire PostToolUseFailure would change all of them.
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
		t.Fatalf("hook sentinels = %v, want exactly [success] — the PostToolUse sentinel fired and no PostToolUseFailure sentinel did: a nonzero shell exit must fire PostToolUse and not PostToolUseFailure (Claude does the opposite — see the test comment)", got)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("failure hook ran for a nonzero shell exit (capture exists): %v", err)
	}
}
