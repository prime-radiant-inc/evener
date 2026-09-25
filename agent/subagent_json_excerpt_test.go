package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestSubagentSeesFailingInputExcerpt drives the real delegate path with a
// scripted child whose first model turn emits a write_file call with truncated
// JSON arguments — the production failure mode behind "unexpected end of JSON
// input" — and asserts the subagent's NEXT model-facing request carries the
// failing-input excerpt in the error tool result. This proves the coaching a
// subagent actually receives quotes the bit of its own output that failed
// parsing, not just a bare parse error.
func TestSubagentSeesFailingInputExcerpt(t *testing.T) {
	clk := agenttest.NewFakeClock()
	env := &agenttest.DenyEnv{WorkDir: lifecycleWorkDir}
	stateDir := t.TempDir()

	parentClient := llm.NewClient()
	parentClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("parent")
	}})

	// The child's script: turn 1 emits a write_file call whose arguments are
	// truncated mid-string (unrepairable; RepairJSON only fixes escapes).
	const truncatedArgs = `{"content": "# Report\n\nall work so far...", "file_path": "/tmp/report.md`
	var childAdapter *agenttest.FakeAdapter
	var factoryCalls atomic.Int64
	factory := func() *llm.Client {
		factoryCalls.Add(1)
		childAdapter = &agenttest.FakeAdapter{
			Provider: "openai",
			Steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(llm.ToolCallData{
						ID:        "call_truncated",
						Name:      "write_file",
						Arguments: []byte(truncatedArgs),
						Type:      "function",
					})
				},
				func(llm.Request) llm.Response {
					return agenttest.FinalResponse("child recovered and done")
				},
			},
		}
		c := llm.NewClient()
		c.Register(childAdapter)
		registerTestSessionNamer(c)
		return c
	}

	cfg := SessionConfig{
		StateDir:              stateDir,
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		LLMSleep:              func(_ context.Context, d time.Duration) error { clk.Sleep(d); return nil },
	}
	cfg.testOnly.childClientFactory = factory

	sess, err := NewSession(parentClient, withTestSessionNamer(parentClient, NewOpenAIProfile("gpt-5.2")), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	defer func() {
		sess.Close()
		<-drainDone
	}()

	// TRIPWIRE: parent and child adapters are scripted in-process calls with no
	// real I/O; this normally completes in well under a second. 30s only fires
	// on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := sess.createDelegate(ctx, delegateArgs{Task: "write the report", DelegationAllowance: new(0)})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", res.TranscriptRef, err)
	}
	child := sess.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	<-done
	if factoryCalls.Load() != 1 {
		t.Fatalf("childClientFactory calls = %d, want 1", factoryCalls.Load())
	}
	if childAdapter == nil {
		t.Fatal("child adapter was never created")
	}

	reqs := childAdapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("child adapter requests = %d, want 2 (malformed turn + recovery turn)", len(reqs))
	}

	// The second request is what the subagent's model sees AFTER the failed
	// call: it must carry the error tool result quoting the failing input.
	recovery, err := json.Marshal(reqs[1])
	if err != nil {
		t.Fatalf("marshal recovery request: %v", err)
	}
	got := string(recovery)
	for _, want := range []string{
		"arguments were not valid JSON",
		"unexpected end of JSON input",
		"near byte",
		`\u003e\u003e\u003e`, // json.Marshal escapes ">" as \u003e
		"/tmp/report.md",
		"Send a single JSON object",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent recovery request missing %q\nrequest: %s", want, got)
		}
	}
	packet := loadStableDelegateTerminalPacket(t, sess, res.DelegateID)
	var output string
	if err := json.Unmarshal(packet.Message, &output); err != nil {
		t.Fatalf("decode stable terminal message: %v", err)
	}
	if !strings.Contains(output, "child recovered") {
		t.Fatalf("subagent did not report recovery: output=%q", output)
	}

	// The child's durable transcript must preserve the model's raw argument
	// bytes verbatim — the post-mortem sees what the child actually sent,
	// not just the replay-safe empty object.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	childLines := readTranscriptLines(t, childTranscriptPath)
	requireTranscriptRawArguments(t, childLines, truncatedArgs)
	_, childEntries, _, err := readTranscript(childTranscriptPath, "")
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	childCall, ok := findToolCallInHistory(ResumeHistory(childEntries), "call_truncated")
	if !ok {
		t.Fatalf("child history missing the truncated call_truncated")
	}
	if got := string(childCall.Arguments); got != "{}" {
		t.Fatalf("child call_truncated arguments = %q, want the replay-safe {}", got)
	}
	if got := childCall.RawArguments; got != truncatedArgs {
		t.Fatalf("child call_truncated raw arguments = %q, want the truncated bytes verbatim %q", got, truncatedArgs)
	}
}

// TestSubagentUnquotedKeyToolCallRecoversAndExecutes drives the real delegate
// path with a scripted child whose first model turn emits a task_list call
// with a bare identifier object key — the observed session-034TMrIEL8VtHauQnvpW0I
// failure shape. The unquoted-key repair must recover the call inside the
// child so it executes, and the child's next model-facing request must not
// carry the invalid-JSON coaching at all.
func TestSubagentUnquotedKeyToolCallRecoversAndExecutes(t *testing.T) {
	clk := agenttest.NewFakeClock()
	env := &agenttest.DenyEnv{WorkDir: lifecycleWorkDir}
	stateDir := t.TempDir()

	parentClient := llm.NewClient()
	parentClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("parent")
	}})

	// The child's script: turn 1 emits a task_list add call whose top-level
	// key is unquoted (recoverable by the unquoted-key repair); turn 2 wraps up.
	const unquotedArgs = `{add: [{"type": "implement", "description": "child task", "prompt": "do the work"}]}`
	var childAdapter *agenttest.FakeAdapter
	factory := func() *llm.Client {
		childAdapter = &agenttest.FakeAdapter{
			Provider: "openai",
			Steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(llm.ToolCallData{
						ID:        "call_unq",
						Name:      "task_list",
						Arguments: []byte(unquotedArgs),
						Type:      "function",
					})
				},
				func(llm.Request) llm.Response {
					return agenttest.FinalResponse("child recovered and done")
				},
			},
		}
		c := llm.NewClient()
		c.Register(childAdapter)
		registerTestSessionNamer(c)
		return c
	}

	cfg := SessionConfig{
		StateDir:              stateDir,
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		LLMSleep:              func(_ context.Context, d time.Duration) error { clk.Sleep(d); return nil },
	}
	cfg.testOnly.childClientFactory = factory

	sess, err := NewSession(parentClient, withTestSessionNamer(parentClient, NewOpenAIProfile("gpt-5.2")), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	defer func() {
		sess.Close()
		<-drainDone
	}()

	// TRIPWIRE: parent and child adapters are scripted in-process calls with no
	// real I/O; this normally completes in well under a second. 30s only fires
	// on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := sess.createDelegate(ctx, delegateArgs{Task: "track the child task", DelegationAllowance: new(0)})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", res.TranscriptRef, err)
	}
	child := sess.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	<-done
	if childAdapter == nil {
		t.Fatal("child adapter was never created")
	}

	reqs := childAdapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("child adapter requests = %d, want 2 (recovered call turn + final turn)", len(reqs))
	}
	recovery, err := json.Marshal(reqs[1])
	if err != nil {
		t.Fatalf("marshal recovery request: %v", err)
	}
	got := string(recovery)
	if strings.Contains(got, "arguments were not valid JSON") {
		t.Fatalf("recovered call still surfaced the invalid-JSON coaching:\n%s", got)
	}
	if !strings.Contains(got, `"name":"task_list"`) && !strings.Contains(got, `"Name":"task_list"`) {
		t.Fatalf("recovery request missing the executed task_list call or result:\n%s", got)
	}
	// The recovered call executed: its tool result is present and is not an error.
	var sawTaskResult bool
	for _, msg := range reqs[1].Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.Name == "task_list" {
				sawTaskResult = true
				if part.ToolResult.IsError {
					t.Fatalf("recovered task_list result is an error: %+v", part.ToolResult)
				}
			}
		}
	}
	if !sawTaskResult {
		t.Fatalf("recovery request has no task_list tool result:\n%s", got)
	}

	packet := loadStableDelegateTerminalPacket(t, sess, res.DelegateID)
	var output string
	if err := json.Unmarshal(packet.Message, &output); err != nil {
		t.Fatalf("decode stable terminal message: %v", err)
	}
	if !strings.Contains(output, "child recovered") {
		t.Fatalf("subagent did not report recovery: output=%q", output)
	}

	// The child's durable transcript preserves the raw unquoted text verbatim.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	childLines := readTranscriptLines(t, childTranscriptPath)
	requireTranscriptRawArguments(t, childLines, unquotedArgs)
	_, childEntries, _, err := readTranscript(childTranscriptPath, "")
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	childCall, ok := findToolCallInHistory(ResumeHistory(childEntries), "call_unq")
	if !ok {
		t.Fatalf("child history missing the recovered call_unq")
	}
	if got := string(childCall.Arguments); got != "{}" {
		t.Fatalf("child call_unq arguments = %q, want the replay-safe {}", got)
	}
	if got := childCall.RawArguments; got != unquotedArgs {
		t.Fatalf("child call_unq raw arguments = %q, want the unquoted bytes verbatim %q", got, unquotedArgs)
	}
}
