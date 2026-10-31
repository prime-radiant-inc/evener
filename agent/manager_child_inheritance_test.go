package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func assertOrdinaryManagerChild(t *testing.T, child *Session) {
	t.Helper()
	meta := child.Meta()
	if meta.Manager || meta.Config.Manager || child.cfg.Manager {
		t.Errorf("ordinary child inherited Manager: metadata=%v snapshot=%v runtime=%v", meta.Manager, meta.Config.Manager, child.cfg.Manager)
	}
	if child.reg.Get("session_control") != nil {
		t.Error("ordinary child registered session_control")
	}
}

func assertOrdinaryManagerRequest(t *testing.T, req llm.Request) {
	t.Helper()
	for _, tool := range req.Tools {
		if tool.Name == "session_control" {
			t.Error("ordinary child advertised session_control")
		}
		props, _ := tool.Parameters["properties"].(map[string]any)
		if _, ok := props["hub"]; ok {
			t.Errorf("ordinary child advertised hub on %s", tool.Name)
		}
	}
}

func TestManagerForkAsideDoNotInheritRole(t *testing.T) {
	t.Parallel()
	for _, aside := range []bool{false, true} {
		t.Run(map[bool]string{false: "fork", true: "aside"}[aside], func(t *testing.T) {
			stateDir, parentID := buildParentSession(t)
			meta, err := schema.LoadSessionMeta(stateDir, parentID)
			if err != nil {
				t.Fatal(err)
			}
			meta.Manager, meta.Config.Manager = true, true
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
			var childID string
			if aside {
				childID, err = AsideSession(stateDir, parentID)
			} else {
				childID, err = ForkSession(stateDir, parentID, 3, "fork-input", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			for cycle := range 2 {
				childMeta, err := schema.LoadSessionMeta(stateDir, childID)
				if err != nil {
					t.Fatal(err)
				}
				if childMeta.Manager || childMeta.Config.Manager {
					t.Errorf("cycle %d persisted child inherited Manager", cycle)
				}
				client := llm.NewClient()
				client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
					assertOrdinaryManagerRequest(t, req)
					return agenttest.FinalResponse("child-result")
				}}})
				child, err := RestoreSessionFromMeta(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), childMeta, stateDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(child.Close)
				assertOrdinaryManagerChild(t, child)
				if _, err := child.ProcessInput(t.Context(), "child-input", nil); err != nil {
					t.Fatal(err)
				}
				child.Close()
			}
			unchanged, err := schema.LoadSessionMeta(stateDir, parentID)
			if err != nil {
				t.Fatal(err)
			}
			if !unchanged.Manager || !unchanged.Config.Manager {
				t.Error("fork changed parent Manager role")
			}
		})
	}
}

func TestDelegateManagerRoleNotInheritedOnCreateOrResume(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	requests := make(chan llm.Request, 8)
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response { requests <- req; return agenttest.FinalResponse("child-result") }}})
	root := newSession(t, withClient(client), withConfig(SessionConfig{Manager: true, StateDir: t.TempDir(), MaxSubagentDepth: 1, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}))
	defer root.Close()
	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-input", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	for cycle := range 2 {
		sub := root.getSub(result.ChildSessionID)
		if sub == nil {
			t.Fatal("missing child runtime")
		}
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		select {
		case <-done:
		// TRIPWIRE: one scripted provider response completes in milliseconds;
		// this unchanged bound catches a stuck child, not successful completion.
		case <-time.After(10 * time.Second):
			t.Fatal("scripted child did not complete")
		}
		assertOrdinaryManagerChild(t, sub.sess)
		descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
		if descriptor.Config.Manager {
			t.Error("frozen delegate descriptor inherited Manager")
		}
		select {
		case req := <-requests:
			assertOrdinaryManagerRequest(t, req)
		default:
			t.Fatal("no captured provider request")
		}
		if cycle == 0 {
			root.delegateController.mu.Lock()
			root.delegateController.maxRetainedTerminal = 1
			root.delegateController.mu.Unlock()
			if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
				t.Fatal(err)
			}
			if root.getSub(result.ChildSessionID) != nil {
				t.Fatal("child runtime not reclaimed")
			}
			client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response { requests <- req; return agenttest.FinalResponse("resumed-result") }}})
			outcome := (delegateRuntime{owner: root}).send(context.Background(), result.DelegateID, "followup-input", 0)
			if outcome.result.Err != nil {
				t.Fatal(outcome.result.Err)
			}
		}
	}
	if !root.Meta().Manager || !root.cfg.Manager {
		t.Error("delegate changed root Manager role")
	}
}
