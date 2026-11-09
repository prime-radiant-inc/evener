package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestAskFenceChildBootDurableBeforeCreateAndResumeInput(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	requests := make(chan struct{}, 2)
	var root *Session
	providerInput := func(expected string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			sub := root.getSub(req.SessionID)
			if sub == nil {
				t.Errorf("provider input has no adopted child runtime: %q", req.SessionID)
			} else {
				durable, err := os.ReadFile(schema.BootGenerationPath(root.stateDir, sub.sess.ID()))
				if err != nil || sub.sess.BootGeneration() != expected || strings.TrimSpace(string(durable)) != expected {
					t.Errorf("provider input generation=%q durable=%q error=%v, want %q", sub.sess.BootGeneration(), durable, err, expected)
				}
			}
			requests <- struct{}{}
			return agenttest.FinalResponse("child-result")
		}
	}
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{providerInput("1")}})
	root = newSession(t, withClient(client), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}))
	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-input", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	for cycle := range 2 {
		sub := root.getSub(result.ChildSessionID)
		if sub == nil {
			t.Fatal("missing child")
		}
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		select {
		case <-done:
		case <-tripwireCtx(t).Done():
			t.Fatal("child did not finish")
		}
		select {
		case <-requests:
		default:
			t.Fatal("provider input not reached")
		}
		generation := sub.sess.BootGeneration()
		durable, err := os.ReadFile(schema.BootGenerationPath(root.stateDir, sub.sess.ID()))
		if err != nil || generation != appwire.BootGeneration(uint64(cycle+1)) || strings.TrimSpace(string(durable)) != generation {
			t.Fatalf("cycle=%d generation=%q durable=%q err=%v", cycle, generation, durable, err)
		}
		if cycle == 0 {
			root.delegateController.mu.Lock()
			root.delegateController.maxRetainedTerminal = 1
			root.delegateController.mu.Unlock()
			if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
				t.Fatal(err)
			}
			if root.getSub(result.ChildSessionID) != nil {
				t.Fatal("runtime not reclaimed")
			}
			client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{providerInput("2")}})
			outcome := (delegateRuntime{owner: root}).send(context.Background(), result.DelegateID, "followup-input", 0)
			if outcome.result.Err != nil {
				t.Fatal(outcome.result.Err)
			}
		}
	}
}
