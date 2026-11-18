package agent

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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
	stateDir, workspace := t.TempDir(), t.TempDir()
	finish := func(llm.Request) llm.Response { return agenttest.FinalResponse("result") }
	adapter := newFakeAdapter("openai", finish)
	parent := newSession(t, withDir(workspace), withStateDir(stateDir), withAdapter(adapter), withConfig(SessionConfig{
		Manager: true, AgentsDocPath: filepath.Join(workspace, "personal.md"),
	}))
	sendOneUserInput(t, parent, "parent-input")
	parentID := parent.ID()
	parent.Close()
	header, entries, _, err := readTranscript(t.Context(), schema.SessionTranscriptPath(stateDir, parentID), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	parentReq := adapter.Requests()[0]
	if header.SystemPrompt == "" || parentReq.Messages[0].Text() != header.SystemPrompt {
		t.Fatal("parent request did not carry its saved startup instructions")
	}
	if !slices.ContainsFunc(parentReq.Tools, func(tool llm.ToolDefinition) bool { return tool.Name == "session_control" }) {
		t.Fatal("parent request did not advertise the Manager control tool")
	}
	userTurn := 1 + slices.IndexFunc(entries, func(entry transcript.Entry) bool { return entry.Turn.Kind == schema.TurnUserInput })
	for _, aside := range []bool{false, true} {
		t.Run(map[bool]string{false: "fork", true: "aside"}[aside], func(t *testing.T) {
			var childID string
			var err error
			if aside {
				childID, err = AsideSession(stateDir, parentID)
			} else {
				childID, err = ForkSession(stateDir, parentID, userTurn, "fork-input", "")
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
				adapter := newFakeAdapter("openai", finish)
				client.Register(adapter)
				child, err := RestoreSessionFromMeta(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(workspace), childMeta, stateDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(child.Close)
				assertOrdinaryManagerChild(t, child)
				sendOneUserInput(t, child, "child-input")
				child.Close()
				requests := adapter.Requests()
				if len(requests) != 1 {
					t.Fatalf("cycle %d sent %d requests, want one", cycle, len(requests))
				}
				req := requests[0]
				assertOrdinaryManagerRequest(t, req)
				if !slices.ContainsFunc(req.Tools, func(tool llm.ToolDefinition) bool { return tool.Name == "job_list" }) {
					t.Errorf("cycle %d lost the ordinary job_list tool", cycle)
				}
				if len(req.Messages) == 0 || req.Messages[0].Role != llm.RoleSystem || req.Messages[0].Text() != header.SystemPrompt {
					t.Errorf("cycle %d changed the Manager's saved startup instructions", cycle)
				}
				var roleUpdates int
				for _, msg := range req.Messages {
					var block envctx.Tracker
					if msg.Role == llm.RoleUser && block.ReplayBlock(msg.Text()) && block.State().Last.OrdinarySession {
						roleUpdates++
					}
				}
				if roleUpdates != 1 {
					t.Errorf("cycle %d received %d ordinary role updates, want one", cycle, roleUpdates)
				}
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
	client.Register(newFakeAdapter("openai", func(req llm.Request) llm.Response { requests <- req; return agenttest.FinalResponse("child-result") }))
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
			client.Register(newFakeAdapter("openai", func(req llm.Request) llm.Response { requests <- req; return agenttest.FinalResponse("resumed-result") }))
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
