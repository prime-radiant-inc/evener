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
// history reaches the wire. The delegate receipt comes from the tool's own
// marshaller; the delegate_send, shell and task_list outputs are hand-written
// text, since those rows read only their calls' intents and states. The phone's transcript row tests read the file this test
// pins.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"encoding/json"
	"testing"
	"time"

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
}

func TestSubagentCallWireFixtures(t *testing.T) {
	t.Parallel()
	receipt, err := marshalStableDelegateCreateResult(stableDelegateCreateResult{
		DelegateID:     "dlg_1",
		ChildSessionID: "child1",
		Type:           "delegate",
		Status:         "running",
		Name:           "Fix race in tree settle",
		AgentType:      "general-purpose",
		TranscriptRef:  "local:child1",
	}, 1<<20)
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	calls := []subagentWireCall{
		{
			id:   "call_delegate_1",
			tool: "delegate",
			args: map[string]any{
				"name":   "Fix race in tree settle",
				"task":   "Find and fix the race between tree settle and the drain.",
				"intent": "Fix race in tree settle",
			},
			output: receipt,
		},
		{
			id:     "call_send_1",
			tool:   "delegate_send",
			args:   map[string]any{"delegate_id": "dlg_1", "message": "Also check drain ordering.", "intent": "Tell it to check drain ordering"},
			output: `{"delegate_id":"dlg_1","type":"delegate","status":"running"}`,
		},
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
			ToolResult: &llm.ToolResultData{ToolCallID: call.id, Name: call.tool, Content: call.output},
		})
	}
	reg := apptranscript.NewToolCallRegistry()
	items := apptranscript.ProjectTurn("turn_1", 1, schema.Turn{Kind: schema.TurnAssistant, Message: announce, Timestamp: wireFixtureStart}, reg, nil, nil)
	items = append(items, apptranscript.ProjectTurn("turn_1", 2, schema.Turn{Kind: schema.TurnToolResults, Message: results, Timestamp: wireFixtureStart.Add(2 * time.Second)}, reg, nil, nil)...)

	checkWireFixture(t, subagentWireFixturePath, struct {
		Note  string               `json:"note"`
		Items []appwire.ThreadItem `json:"items"`
	}{
		Note:  "One ASSISTANT entry announcing delegate, delegate_send, shell (with an intent) and task_list (without one), and the TOOL_RESULTS entry answering them, projected through apptranscript. The delegate receipt comes from the tool's own marshaller; the other outputs are hand-written text.",
		Items: items,
	}, "the mobile-native tests that read it")
}
