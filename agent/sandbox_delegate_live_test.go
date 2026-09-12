package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

func requireLiveDelegateSandbox(t *testing.T) sandbox.HostFacts {
	t.Helper()
	if os.Getenv("EVENER_SEATBELT_LIVE") != "1" {
		t.Skip("live delegate sandbox test: set EVENER_SEATBELT_LIVE=1 to launch a kernel boundary")
	}
	facts := sandbox.RealProber{}.Probe()
	switch {
	case facts.OS == "darwin" && facts.SeatbeltAvailable():
	case facts.OS == "linux" && facts.BwrapCapable:
	default:
		t.Skipf("live delegate sandbox test: no usable backend on %s", facts.OS)
	}
	return facts
}

func liveDelegateToolCall(id, name string, args map[string]any) llm.ToolCallData {
	encoded, _ := json.Marshal(args)
	return llm.ToolCallData{ID: id, Name: name, Arguments: encoded, Type: "function"}
}

func liveDelegateToolResult(req llm.Request, id string) (llm.ToolResultData, bool) {
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == id {
				return *part.ToolResult, true
			}
		}
	}
	return llm.ToolResultData{}, false
}

func TestReadOnlyRoleDelegateUsesRealWriteBlockedBoundary(t *testing.T) {
	facts := requireLiveDelegateSandbox(t)
	lane, _ := sbxLane(t)
	readPath := filepath.Join(lane, "readable.txt")
	modifyPath := filepath.Join(lane, "deliverable.txt")
	createPath := filepath.Join(lane, "created-by-shell")
	if err := os.WriteFile(readPath, []byte("readable sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modifyPath, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var failures []string
	childFinished := make(chan struct{}, 1)
	record := func(message string) { failures = append(failures, message) }
	resultText := func(req llm.Request, id string) string {
		result, ok := liveDelegateToolResult(req, id)
		if !ok {
			record("missing tool result for " + id)
			return ""
		}
		return fmt.Sprint(result.Content)
	}
	expectShellFailure := func(req llm.Request, id string) {
		text := resultText(req, id)
		if !strings.Contains(text, "exit ") || strings.Contains(text, "exit 0") {
			record(fmt.Sprintf("%s was not refused: %q", id, text))
		}
	}
	expectShellSuccess := func(req llm.Request, id string) {
		text := resultText(req, id)
		if !strings.Contains(text, "exit 0") {
			record(fmt.Sprintf("%s did not succeed: %q", id, text))
		}
	}

	childAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return toolCallResponse(liveDelegateToolCall("read", "read_file", map[string]any{
					"file_path": readPath,
				}))
			},
			func(req llm.Request) llm.Response {
				result, ok := liveDelegateToolResult(req, "read")
				if !ok || result.IsError || !strings.Contains(fmt.Sprint(result.Content), "readable sentinel") {
					record(fmt.Sprintf("read_file did not succeed: %#v", result))
				}
				return toolCallResponse(liveDelegateToolCall("modify", "shell", map[string]any{
					"command": "printf changed > '" + strings.ReplaceAll(modifyPath, "'", "'\\''") + "'",
				}))
			},
			func(req llm.Request) llm.Response {
				expectShellFailure(req, "modify")
				return toolCallResponse(liveDelegateToolCall("create", "shell", map[string]any{
					"command": "mkdir '" + strings.ReplaceAll(createPath, "'", "'\\''") + "'",
				}))
			},
			func(req llm.Request) llm.Response {
				expectShellFailure(req, "create")
				return toolCallResponse(liveDelegateToolCall("delete", "shell", map[string]any{
					"command": "rm '" + strings.ReplaceAll(modifyPath, "'", "'\\''") + "'",
				}))
			},
			func(req llm.Request) llm.Response {
				expectShellFailure(req, "delete")
				return toolCallResponse(liveDelegateToolCall("scratch", "shell", map[string]any{
					"command": `printf scratch > "$EVENER_SCRATCH_DIR/report.txt"`,
				}))
			},
			func(req llm.Request) llm.Response {
				expectShellSuccess(req, "scratch")
				childFinished <- struct{}{}
				return communicateWithDefaultOutput("checked the read-only boundary")
			},
		},
	}
	childClient := llm.NewClient()
	childClient.Register(childAdapter)
	registerTestSessionNamer(childClient)
	parent := newSession(t,
		withClient(delegateTestClient(func(llm.Request) llm.Response {
			return communicateWithDefaultOutput("parent done")
		})),
		withDir(lane),
		withConfig(SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				childClientFactory:  func() *llm.Client { return childClient },
				sandboxProber:       sandbox.FakeProber{Facts: facts},
			},
		}),
	)
	eventsDone := make(chan struct{})
	go func() {
		for range parent.Events() {
		}
		close(eventsDone)
	}()
	t.Cleanup(func() {
		parent.Close()
		<-eventsDone
	})
	parent.pluginAgents["test:verifier"] = plugin.Agent{
		Name:         "verifier",
		Description:  "Read-only verifier",
		Model:        "inherit",
		Tools:        []string{"glob", "grep", "read_file", "shell"},
		SystemPrompt: "Use read-only commands only; do not modify files.",
		PluginName:   "test",
	}

	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:      "inspect the workspace without modifying it",
		AgentType: "test:verifier",
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	child := parent.getSub(res.ChildSessionID)
	if child == nil || child.sess == nil {
		t.Fatalf("created child %q is not tracked", res.ChildSessionID)
	}
	local, ok := child.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("read-only role delegate did not use a local execution environment")
	}
	if local.Sandbox == nil || local.Sandbox.Mode != sandbox.ModeReadOnly || local.Wrapper == nil {
		t.Fatalf("read-only role delegate must have a real read-only wrapper, got sandbox=%+v wrapper=%v", local.Sandbox, local.Wrapper)
	}
	scratch := local.Wrapper.SessionTmp()
	if scratch == "" {
		t.Fatal("read-only role delegate has no private scratch directory")
	}

	waitForTestSignal(t, childFinished, "read-only role delegate completion")
	if len(childAdapter.Requests()) < 6 {
		t.Fatalf("child made %d provider requests, want the read/read-write-boundary sequence", len(childAdapter.Requests()))
	}
	if len(failures) > 0 {
		t.Fatalf("boundary observations failed: %s", strings.Join(failures, "; "))
	}
	contents, err := os.ReadFile(modifyPath)
	if err != nil {
		t.Fatalf("read preserved deliverable: %v", err)
	}
	if string(contents) != "before\n" {
		t.Fatalf("shell modify changed the deliverable to %q", contents)
	}
	if _, err := os.Stat(createPath); !os.IsNotExist(err) {
		t.Fatalf("shell create unexpectedly changed the workspace: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch, "report.txt")); err != nil {
		t.Fatalf("read-only delegate could not write its own scratch report: %v", err)
	}
}
