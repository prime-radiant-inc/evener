package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// TestDelegateSendTimeoutReportsTheGenerationsCurrentObservation is the medium
// that a timed-out send returned the registry sample taken BEFORE the wait, so a
// tool the child recovered during the wait was missing from the reply while
// job_status and the eventual terminal packet both listed it.
//
// The child registers an extra tool while the send is waiting and the wait then
// ends without a packet. Both surfaces must name that tool: the pre-wait sample
// cannot.
func TestDelegateSendTimeoutReportsTheGenerationsCurrentObservation(t *testing.T) {
	root, fixture, entered, _ := newBlockingColdDelegateRuntime(t)
	const recovered = "recovered_mcp__echo"

	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	callCtx = context.WithValue(callCtx, ctxToolCallID, "timeout-observation")
	root.cfg.testOnly.delegateInlineWaitReady = func(waitCtx context.Context, duration time.Duration) {
		<-entered
		// The child is mid-turn: register the tool the way a healing MCP retry
		// would, then end the wait without a packet.
		sub := root.getSub(fixture.childID)
		if sub == nil || sub.sess == nil || sub.sess.reg == nil {
			t.Errorf("fixture has no live child registry to register into")
			cancelCall()
			return
		}
		if err := sub.sess.reg.Register(regTool(llm.ToolDefinition{Name: recovered, Description: "recovered"})); err != nil {
			t.Errorf("register %q on the child: %v", recovered, err)
		}
		// Publishing is what makes the registration observable. Availability
		// readers read the published set, not the registry, because the registry
		// is briefly a superset while a server's tools are parsed and before the
		// filtering rules run. A healing retry publishes once its filtering is
		// done, so the fixture has to do the same or it would be testing a
		// registration no reader has ever been promised.
		sub.sess.mu.Lock()
		sub.sess.publishCallableTools()
		sub.sess.mu.Unlock()
		cancelCall()
	}

	arguments, err := json.Marshal(map[string]any{
		"to":          fixture.delegateID,
		"message":     "observe the timeout reply",
		"max_wait_ms": 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := root.reg.ExecuteCall(callCtx, root.env, llm.ToolCallData{
		ID:        "timeout-observation",
		Name:      "delegate_send",
		Arguments: arguments,
	})
	if call.IsError {
		t.Fatalf("send failed: %s", call.Output)
	}
	var result struct {
		Tools    []string `json:"tools"`
		TimedOut bool     `json:"timed_out"`
		Status   string   `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(call), &result); err != nil {
		t.Fatalf("decode send result: %v", err)
	}
	if !result.TimedOut || result.Status != string(jobstore.StatusRunning) {
		t.Fatalf("fixture did not produce a timeout reply: %#v", result)
	}
	found := false
	for _, name := range result.Tools {
		if name == recovered {
			found = true
		}
	}
	if !found {
		t.Errorf("the timeout reply omitted %q, which the child registered during the wait: %v", recovered, result.Tools)
	}
}
