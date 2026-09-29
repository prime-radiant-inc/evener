package agent

// A subagent reaches the transcript first as the coordinator's `delegate` tool
// call: a commandExecution item whose callId is the one the subagent's own
// record names as originToolCallId, whose description is the call's intent,
// and whose output is the launch receipt, settled as soon as the receipt
// returns. A client that loses that callId, or reads a settled call as a
// finished subagent, shows every running subagent as "done"; a hand-built item
// can't catch that, because it only carries the fields its author thought of.
//
// This test is the corpus for those rows. It announces the calls and returns
// their results the way a session records them (one ASSISTANT entry, one
// TOOL_RESULTS entry) and projects both turns through apptranscript the way
// history reaches the wire. The delegate and delegate_send calls run for real,
// on a session whose child blocks in its first model call until the test
// ends; the send waits for the child to reach that call, so it always steers
// a running delegate. The shell and task_list
// outputs are hand-written text, since those rows read only their calls'
// intents and states. The phone's transcript row tests read the file this
// test pins.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// subagentWireFixturePath is the committed corpus the phone reads.
const subagentWireFixturePath = "testdata/subagentwire/calls.json"

type subagentWireCall struct {
	id     string
	tool   string
	args   map[string]any
	output string
	state  json.RawMessage
}

// The delegate's id, its child session's id and the session's state
// directory are minted per run; the corpus records each as one of these.
const (
	subagentWireDelegateID   = "dlg_02wMz5TxvSettleRace001"
	subagentWireChildSession = "02wMz5TxvChildSession1"
	subagentWireStateDir     = "/home/jesse/.local/state/evener"
)

// subagentWireSession is a session that can delegate, whose children block
// in their first model call until the test ends: a delegate it starts stays
// running for as long as the recording takes. The channel it returns closes
// when a child reaches that call.
func subagentWireSession(t *testing.T) (*Session, string, <-chan struct{}) {
	t.Helper()
	stateDir := realTempDirForTest(t)
	reached := make(chan struct{})
	var reachedOnce sync.Once
	release := make(chan struct{})
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		reachedOnce.Do(func() { close(reached) })
		<-release
		return llm.Response{Message: llm.Assistant("done")}
	}})
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	workspace := realTempDirForTest(t)
	s, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		ForceRealIO:      true,
		// The host's sandbox facts fixed, so the receipt is the same on every
		// machine.
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace)},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Cleanups run last-registered first: the child's model call returns
	// before the session closes.
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(release) })
	return s, stateDir, reached
}

// run executes one call on the session, failing the test on an error result.
func (c subagentWireCall) run(t *testing.T, s *Session) subagentWireCall {
	t.Helper()
	args, err := json.Marshal(c.args)
	if err != nil {
		t.Fatalf("%s arguments: %v", c.id, err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.currentEnv(), llm.ToolCallData{ID: c.id, Name: c.tool, Arguments: args})
	if res.IsError {
		t.Fatalf("%s: the %s call failed: %s", c.id, c.tool, res.Output)
	}
	c.output, c.state = res.Output, res.ToolState
	return c
}

func TestSubagentCallWireFixtures(t *testing.T) {
	t.Parallel()
	s, stateDir, childRunning := subagentWireSession(t)
	delegate := subagentWireCall{
		id:   "call_delegate_1",
		tool: "delegate",
		args: map[string]any{
			"prompt": "Find and fix the race between tree settle and the drain.",
			"name":   "settle-race",
			"intent": "Fix race in tree settle",
		},
	}.run(t, s)
	var receipt stableDelegateCreateResult
	if err := json.Unmarshal([]byte(delegate.output), &receipt); err != nil || receipt.DelegateID == "" {
		t.Fatalf("delegate receipt %q names no delegate: %v", delegate.output, err)
	}
	// The send steers the delegate only once its child is in its model call,
	// so it always finds the delegate running.
	select {
	case <-childRunning:
	case <-time.After(30 * time.Second):
		t.Fatal("the delegate's child never reached its first model call")
	}
	calls := []subagentWireCall{
		delegate,
		subagentWireCall{
			id:   "call_send_1",
			tool: "delegate_send",
			args: map[string]any{"to": receipt.DelegateID, "message": "Also check drain ordering.", "intent": "Tell it to check drain ordering"},
		}.run(t, s),
		{
			id:     "call_shell_1",
			tool:   "shell",
			args:   map[string]any{"command": "go test ./agent/...", "intent": "Run the agent tests"},
			output: "ok  \tprimeradiant.com/evener/agent\t12.3s",
		},
		{
			id:     "call_tasks_1",
			tool:   "task_list",
			args:   map[string]any{"operation": "update", "id": 3, "status": "done"},
			output: `{"tasks":[{"id":3,"status":"done"}]}`,
		},
	}

	announce := llm.Message{Role: llm.RoleAssistant}
	results := llm.Message{Role: llm.RoleTool}
	for _, call := range calls {
		args, err := json.Marshal(call.args)
		if err != nil {
			t.Fatalf("%s arguments: %v", call.id, err)
		}
		announce.Content = append(announce.Content, llm.ContentPart{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: args},
		})
		results.Content = append(results.Content, llm.ContentPart{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: call.id, Name: call.tool, Content: call.output, ToolState: call.state},
		})
	}
	reg := apptranscript.NewToolCallRegistry()
	items := apptranscript.ProjectTurn("turn_1", 1, schema.Turn{Kind: schema.TurnAssistant, Message: announce, Timestamp: wireFixtureStart}, reg, nil, nil)
	items = append(items, apptranscript.ProjectTurn("turn_1", 2, schema.Turn{Kind: schema.TurnToolResults, Message: results, Timestamp: wireFixtureStart.Add(2 * time.Second)}, reg, nil, nil)...)

	relocate := strings.NewReplacer(
		receipt.DelegateID, subagentWireDelegateID,
		receipt.ChildSessionID, subagentWireChildSession,
		stateDir, subagentWireStateDir,
	)
	checkWireFixture(t, subagentWireFixturePath, struct {
		Note  string               `json:"note"`
		Items []appwire.ThreadItem `json:"items"`
	}{
		Note:  "One ASSISTANT entry announcing delegate, delegate_send, shell (with an intent) and task_list (without one), and the TOOL_RESULTS entry answering them, projected through apptranscript. The delegate and delegate_send calls ran for real, the delegate still running when the send steered it (its id, its child session's id and the state directory fixed); the shell and task_list outputs are hand-written text.",
		Items: toolWireRelocated(t, items, relocate.Replace),
	}, "the mobile-native tests that read it")
}
