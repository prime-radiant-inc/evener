package agent

import (
	"context"
	"encoding/json"
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
		StateDir:              t.TempDir(),
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
}
