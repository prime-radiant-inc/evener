package agent

import (
	"context"
	"os"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// sandboxableExecTools is the set of registered tools whose handler can be
// EXECUTED (not merely validated) entirely inside the deny env: every one of
// them routes its filesystem access through execenv.ExecutionEnvironment, so a
// DenyEnv (which never touches the real FS, forks a process, or opens a socket)
// fully contains it. FuzzToolExecution drives exactly these through the real
// Registry.ExecuteCall seam — argument decode → schema validate → middleware →
// handler → result formatting — which Phase-1's validate-only target
// (FuzzToolArgsValidate) deliberately stopped short of.
//
// Tools held at the validate boundary, and WHY each is unsafe or
// non-deterministic to execute under a fuzzer even with the deny env:
//
//   - apply_patch: its handler (internal/tool/apply_patch.go) calls os.* directly
//     (os.WriteFile/os.Rename/os.Remove), NOT the execution environment, so the
//     deny env cannot contain it — fuzzing its execution would mutate the real FS.
//   - shell: routes through the job manager + a background goroutine; its lifecycle
//     (foreground and background) is fuzzed deterministically by the lifecycle
//     harness (TestLifecycleSeqFuzz), not here.
//   - delegate, delegate_send: spawn real child sessions / goroutines; covered by
//     the lifecycle harness's delegate ops.
//   - web_fetch: performs real network IO (and out-of-root cache writes).
//   - use_skill: reads skill files from disk outside the env abstraction.
//   - communicate, compact, update_goal, job_list, job_status, job_stop,
//     job_watch, task_list, read_transcript: stateful session/turn tools whose
//     behavior is a function of live Session state, not the env; their execution
//     is exercised through the lifecycle harness, where that state is modeled.
var sandboxableExecTools = []string{
	"read_file",
	"write_file",
	"edit_file",
	"glob",
	"grep",
	"list_dir",
}

// FuzzToolExecution fuzzes tool-handler EXECUTION for the env-backed tools,
// driving the real Registry.ExecuteCall against a DenyEnv. It asserts the
// clean-error + containment contract:
//
//   - ExecuteCall never panics, for any decodable argument bytes (a panic fails
//     the test — the seam has no runtime recover() around validate or the handler).
//   - The result is always a well-formed ExecResult (ToolName and CallID set),
//     never a partial/garbage value: a schema-invalid input is reported as a
//     structured tool error rather than reaching the handler with a side effect.
//   - No real side effect escapes the sandbox: a real witness directory (the deny
//     env's working directory) stays empty across every call, proving the handler
//     touched no real filesystem. This is the guard that would catch a tool that
//     bypassed the env to call os.* directly.
func FuzzToolExecution(f *testing.F) {
	reg, env, witnessDir := execFuzzSetup(f)

	// Seeds: a valid-ish read, an empty object, and adversarial shapes that
	// exercise the handler past validation (wrong types, nulls, oversized
	// numbers, lone surrogate, non-object roots).
	seeds := []struct {
		tool int
		args string
	}{
		{0, `{"file_path":"a.txt"}`},
		{0, `{"file_path":"a.txt","offset":1,"limit":2}`},
		{1, `{"file_path":"b.txt","content":"hello"}`},
		{2, `{"file_path":"c.txt","old_string":"x","new_string":"y"}`},
		{2, `{"file_path":"c.txt","old_string":"x","new_string":"y","replace_all":true}`},
		{3, `{"pattern":"*.go"}`},
		{4, `{"pattern":"x","path":".","output_mode":"count"}`},
		{5, `{"path":"."}`},
		{0, `{}`},
		{0, `{"file_path":123}`},
		{0, `{"file_path":null,"limit":1e308}`},
		{1, `{"file_path":["a"],"content":{"k":"v"}}`},
		{4, `{"pattern":"a","output_mode":"files_with_matches"}`},
		{0, `not json`},
		{3, `{"pattern":"\ud800"}`},
	}
	for _, s := range seeds {
		f.Add(s.tool, []byte(s.args))
	}

	f.Fuzz(func(t *testing.T, toolIdx int, argsBytes []byte) {
		name := sandboxableExecTools[((toolIdx%len(sandboxableExecTools))+len(sandboxableExecTools))%len(sandboxableExecTools)]

		res := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
			ID:        "fuzz-call",
			Name:      name,
			Arguments: argsBytes,
			Type:      "function",
		})

		// The result must always be a structured ExecResult, never partial garbage.
		if res.ToolName != name {
			t.Fatalf("ExecuteCall(%s): result ToolName = %q, want %q", name, res.ToolName, name)
		}
		if res.CallID == "" {
			t.Fatalf("ExecuteCall(%s): result has empty CallID", name)
		}

		// Containment: no call may write to the real witness directory. The deny
		// env routes every FS op to a pure, in-memory result, so this stays empty;
		// a non-empty witness means a handler escaped the sandbox.
		entries, err := os.ReadDir(witnessDir)
		if err != nil {
			t.Fatalf("read witness dir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("ExecuteCall(%s, %q) wrote to the real filesystem: witness dir has %d entries", name, argsBytes, len(entries))
		}
	})
}

// execFuzzSetup stands up a real Session so registerCoreTools wires the full
// tool set, then returns its registry, a DenyEnv pointed at a real (witness)
// working directory, and that directory's path. The session is built once per
// fuzz run and closed when it ends; the registry and deny env are immutable
// across the run (ExecuteCall is read-only on the registry and the deny env is
// pure), so reusing them across iterations is race-free.
func execFuzzSetup(f *testing.F) (*tool.Registry, *agenttest.DenyEnv, string) {
	f.Helper()

	witnessDir := f.TempDir()
	c := llm.NewClient()
	c.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}})

	env := &agenttest.DenyEnv{WorkDir: witnessDir}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), env, SessionConfig{})
	if err != nil {
		f.Fatalf("NewSession: %v", err)
	}
	f.Cleanup(sess.Close)

	// Confirm every sandboxable tool is actually registered with an executor, so
	// the target cannot silently stop exercising a handler if a tool is renamed.
	for _, name := range sandboxableExecTools {
		rt := sess.reg.Get(name)
		if rt == nil || rt.Exec == nil {
			f.Fatalf("sandboxable tool %q is not registered with an executor", name)
		}
	}
	return sess.reg, env, witnessDir
}
