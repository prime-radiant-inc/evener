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
// history reaches the wire. The delegate and both delegate_send calls run for
// real, on a session whose child blocks in its first model call until the
// recording lets it answer. The first send waits for the child to reach that
// call, so it always steers a running delegate. The second waits for the
// delegate's reply: it is sent once the delegate has gone idle, and returns
// the reply the child's next generation gives it. The shell and task_list
// outputs are hand-written text, since those rows read only their calls'
// intents and states. The phone's transcript row tests read the file this
// test pins.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
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

// The waiting send's question, and the reply the delegate gives it.
const (
	subagentWireQuestion = "Is drain ordering safe now?"
	subagentWireReply    = "Yes: tree settle now waits for the drain, and a test pins the order."
)

// subagentWireSession is a session that can delegate, whose children block
// in their first model call until the returned answer func is called (or the
// test ends): a delegate it starts stays running until the recording lets it
// answer. The channel it returns closes when a child reaches that call. A
// child answers through its result tool, and answers the waiting send's
// question with subagentWireReply.
func subagentWireSession(t *testing.T) (*Session, string, <-chan struct{}, func()) {
	t.Helper()
	stateDir := realTempDirForTest(t)
	reached := make(chan struct{})
	var reachedOnce, releaseOnce sync.Once
	release := make(chan struct{})
	answer := func() { releaseOnce.Do(func() { close(release) }) }
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
		reachedOnce.Do(func() { close(reached) })
		<-release
		// The request carries the whole history, so only the generation the
		// question started sees it.
		if requestContainsText(req, subagentWireQuestion) {
			return finalResponse(subagentWireReply)
		}
		return finalResponse("Fixed the race between tree settle and the drain.")
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
	t.Cleanup(answer)
	return s, stateDir, reached, answer
}

// arguments is the call's arguments as the model sends them.
func (c subagentWireCall) arguments(t *testing.T) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(c.args)
	if err != nil {
		t.Fatalf("%s arguments: %v", c.id, err)
	}
	return args
}

// run executes one call on the session the way its tool round does, with
// the call's id in the context, failing the test on an error result.
func (c subagentWireCall) run(t *testing.T, s *Session) subagentWireCall {
	t.Helper()
	ctx := context.WithValue(context.Background(), ctxToolCallID, c.id)
	res := s.reg.ExecuteCall(ctx, s.currentEnv(), llm.ToolCallData{ID: c.id, Name: c.tool, Arguments: c.arguments(t)})
	if res.IsError {
		t.Fatalf("%s: the %s call failed: %s", c.id, c.tool, res.Output)
	}
	c.output, c.state = res.Output, res.ToolState
	return c
}

func TestSubagentCallWireFixtures(t *testing.T) {
	t.Parallel()
	s, stateDir, childRunning, answer := subagentWireSession(t)
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
	// TRIPWIRE: a hang guard only; the child reaches its model call within
	// milliseconds, and the receive above is what orders the send.
	case <-time.After(30 * time.Second):
		t.Fatal("the delegate's child never reached its first model call")
	}
	steer := subagentWireCall{
		id:   "call_send_1",
		tool: "delegate_send",
		args: map[string]any{"to": receipt.DelegateID, "message": "Also check drain ordering.", "intent": "Tell it to check drain ordering"},
	}.run(t, s)
	// Let the child answer, and send the question once the delegate is idle.
	answer()
	awaitDelegateIdle(t, s, receipt.DelegateID)
	waiting := subagentWireCall{
		id:   "call_send_2",
		tool: "delegate_send",
		args: map[string]any{"to": receipt.DelegateID, "message": subagentWireQuestion, "max_wait_ms": 60000, "intent": "Ask whether drain ordering is safe"},
	}.run(t, s)
	calls := []subagentWireCall{
		delegate,
		steer,
		waiting,
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
		announce.Content = append(announce.Content, llm.ContentPart{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: call.arguments(t)},
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
		Note:  "One ASSISTANT entry announcing delegate, two delegate_sends, shell (with an intent) and task_list (without one), and the TOOL_RESULTS entry answering them, projected through apptranscript. The delegate and delegate_send calls ran for real (the delegate's id, its child session's id and the state directory fixed): the first send steered the running delegate, and the second, sent once it was idle, waited for and returned its reply (its run times fixed too). The shell and task_list outputs are hand-written text.",
		Items: toolWireRelocated(t, items, func(encoded string) string { return subagentWireRunTimesFixed(t, relocate.Replace(encoded)) }),
	}, "the mobile-native tests that read it")
}

// awaitDelegateIdle waits until delegateID's generation has finished and the
// delegate reads idle. It reads the controller's own state: the session's
// event stream is best effort and could drop the idle event.
func awaitDelegateIdle(t *testing.T, s *Session, delegateID string) {
	t.Helper()
	// TRIPWIRE: a hang guard only; the scripted child answers at once and its
	// generation goes idle within milliseconds.
	waitForCondition(t, 30*time.Second, "the delegate going idle after its child answered", func() bool {
		c := s.delegateController
		c.mu.Lock()
		defer c.mu.Unlock()
		aggregate := c.durable[delegateID]
		return aggregate != nil && aggregate.Phase == delegatestore.PhaseIdle && !aggregate.CurrentRunOpen
	})
}

// subagentWireRunTimes matches the run times a delegate_send result carries,
// which are the clock's.
var subagentWireRunTimes = regexp.MustCompile(`"(run_started_at|latest_activity_at|run_ended_at)":"([^"]*)"`)

// subagentWireRunFields are the run-time fields, in the order a run's times
// must fall.
var subagentWireRunFields = []string{"run_started_at", "latest_activity_at", "run_ended_at"}

// subagentWireRunTimesFixed records each run time as a fixed time after the
// fixture's start, by field, so two equal times can't swap places. It first
// checks the recorded times are real and in order (started, then latest
// activity, then ended), and that none appears escaped inside a string the
// replacement can't reach.
func subagentWireRunTimesFixed(t *testing.T, encoded string) string {
	t.Helper()
	for _, field := range subagentWireRunFields {
		if strings.Contains(encoded, `\"`+field+`\"`) {
			t.Fatalf("a %s appears escaped inside a string, where the fixture can't fix it", field)
		}
	}
	recorded := make(map[string]time.Time, len(subagentWireRunFields))
	for _, match := range subagentWireRunTimes.FindAllStringSubmatch(encoded, -1) {
		at, err := time.Parse(time.RFC3339Nano, match[2])
		if err != nil {
			t.Fatalf("%s %q isn't a time: %v", match[1], match[2], err)
		}
		recorded[match[1]] = at
	}
	for i, field := range subagentWireRunFields {
		if _, ok := recorded[field]; !ok {
			t.Fatalf("the waiting send's result carries no %s", field)
		}
		if i > 0 && recorded[field].Before(recorded[subagentWireRunFields[i-1]]) {
			t.Fatalf("%s %v is before %s %v", field, recorded[field], subagentWireRunFields[i-1], recorded[subagentWireRunFields[i-1]])
		}
	}
	fixed := map[string]time.Time{
		"run_started_at":     wireFixtureStart.Add(time.Second),
		"latest_activity_at": wireFixtureStart.Add(1500 * time.Millisecond),
		"run_ended_at":       wireFixtureStart.Add(2 * time.Second),
	}
	return subagentWireRunTimes.ReplaceAllStringFunc(encoded, func(field string) string {
		name := subagentWireRunTimes.FindStringSubmatch(field)[1]
		return `"` + name + `":"` + fixed[name].Format(time.RFC3339Nano) + `"`
	})
}
