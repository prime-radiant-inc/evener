package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// The parent and child use real sessions, tools and managed git worktrees; only
// model responses are scripted so both lifecycle branches stay deterministic.
func TestDelegateDisposalHint_DelegateSendReportsFinalWorktreeState(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		name := "clean"
		if dirty {
			name = "dirty"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var childSteps []func(llm.Request) llm.Response
			if dirty {
				childSteps = append(childSteps,
					func(llm.Request) llm.Response {
						return agenttest.ToolCallResponse(writeFileCall("dirty-file", "CHILD_WORK.txt", "dirty worktree e2e"))
					},
					func(req llm.Request) llm.Response {
						if text := toolResultTextForCall(req, "dirty-file"); !strings.Contains(text, "wrote 18 bytes to CHILD_WORK.txt") {
							t.Errorf("write_file result = %q, want success", text)
						}
						return communicateWithDefaultOutput("created the uncommitted file")
					},
				)
			} else {
				childSteps = append(childSteps, func(llm.Request) llm.Response {
					return communicateWithDefaultOutput("made no worktree changes")
				})
			}
			childSteps = append(childSteps, func(llm.Request) llm.Response {
				return communicateWithDefaultOutput("follow-up complete")
			})
			childClient := llm.NewClient()
			childClient.Register(&fakeAdapter{name: "openai", steps: childSteps})
			registerTestSessionNamer(childClient)

			repo := newRetirementWorktreeRepo(t)
			repo.s.cfg.testOnly.childClientFactory = func() *llm.Client { return childClient }

			var created delegateCreateResult
			var sendOutput string
			createArgs, err := json.Marshal(map[string]any{
				"prompt":    "Create CHILD_WORK.txt in your worktree and report completion.",
				"isolation": "worktree",
				"name":      "disposal-hint-e2e",
			})
			if err != nil {
				t.Fatalf("marshal delegate args: %v", err)
			}
			parentAdapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
				if created.DelegateID == "" {
					if creationOutput := toolResultTextForCall(req, "create-worktree"); creationOutput != "" {
						if err := json.Unmarshal([]byte(creationOutput), &created); err != nil {
							t.Errorf("decode delegate creation result %q: %v", creationOutput, err)
							return communicateWithDefaultOutput("delegate creation result could not be decoded")
						}
						return communicateWithDefaultOutput("child created")
					}
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: "create-worktree", Name: "delegate", Arguments: createArgs, Type: "function"})
				}
				if output := toolResultTextForCall(req, "send-worktree"); output != "" {
					sendOutput = output
					return communicateWithDefaultOutput("delegate result received")
				}
				if latestUserMessageText(req) == "fetch final child result" {
					args, err := json.Marshal(map[string]any{
						"to":          created.DelegateID,
						"message":     "Report completion and make no further filesystem changes.",
						"max_wait_ms": 60_000,
					})
					if err != nil {
						t.Errorf("marshal delegate_send args: %v", err)
						return communicateWithDefaultOutput("delegate_send args could not be encoded")
					}
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: "send-worktree", Name: "delegate_send", Arguments: args, Type: "function"})
				}
				return communicateWithDefaultOutput("delegate notification received")
			}}
			repo.s.client.Register(parentAdapter)

			turnCtx := tripwireCtx(t)
			if _, err := repo.s.ProcessInput(turnCtx, "create isolated child", nil); err != nil {
				t.Fatalf("create-child ProcessInput: %v", err)
			}
			if created.DelegateID == "" || created.ChildSessionID == "" {
				t.Fatalf("delegate creation result = %+v", created)
			}
			if created.Worktree == nil || created.Worktree.Path == "" {
				t.Fatalf("delegate creation omitted its isolated worktree: %+v", created)
			}
			child := repo.s.subagents.get(created.ChildSessionID)
			if child == nil {
				t.Fatalf("created child session %q is missing", created.ChildSessionID)
			}
			waitSubagentRun(t, child)

			if _, err := repo.s.ProcessInput(turnCtx, "fetch final child result", nil); err != nil {
				t.Fatalf("delegate_send ProcessInput: %v", err)
			}
			if !strings.Contains(sendOutput, "dirty="+strconv.FormatBool(dirty)) {
				t.Fatalf("delegate_send output = %q, want dirty=%t", sendOutput, dirty)
			}
			if !strings.Contains(sendOutput, created.Worktree.Path) || !strings.Contains(sendOutput, created.Worktree.Branch) {
				t.Fatalf("delegate_send output omitted its worktree identity: %q", sendOutput)
			}
			status := strings.TrimSpace(wtGit(t, created.Worktree.Path, "status", "--short"))
			if dirty {
				contents, err := os.ReadFile(filepath.Join(created.Worktree.Path, "CHILD_WORK.txt"))
				if err != nil || string(contents) != "dirty worktree e2e" {
					t.Fatalf("child worktree file = %q, err=%v", contents, err)
				}
				if status != "?? CHILD_WORK.txt" {
					t.Fatalf("dirty child worktree status = %q, want untracked CHILD_WORK.txt", status)
				}
				if !strings.Contains(sendOutput, "Inspect or resume this delegate before disposal") || !strings.Contains(sendOutput, "force_dirty discards uncommitted changes") {
					t.Fatalf("dirty delegate_send output lacks safe cleanup guidance: %q", sendOutput)
				}
				lane := filepath.Join(repo.s.stateDir, "worktrees")
				if !strings.Contains(sendOutput, lane) {
					t.Fatalf("dirty delegate_send output omitted worktree location: %q", sendOutput)
				}
			} else {
				if status != "" {
					t.Fatalf("clean child worktree status = %q, want clean", status)
				}
				if !strings.Contains(sendOutput, "When you're done with this delegate's work") || strings.Contains(sendOutput, "force_dirty") {
					t.Fatalf("clean delegate_send output has incorrect cleanup guidance: %q", sendOutput)
				}
			}
		})
	}
}

func TestDelegateDisposalHint_UsesImmediateParentToolCapability(t *testing.T) {
	for _, parentCanManage := range []bool{false, true} {
		name := "parent-without-manage-worktree"
		if parentCanManage {
			name = "parent-with-manage-worktree"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newRetirementWorktreeRepo(t)
			root := repo.s
			if root.reg.Get("manage_worktree") == nil {
				t.Fatal("root session must retain manage_worktree for this scenario")
			}

			parentTools := []string{"delegate", "delegate_send", "write_file"}
			if parentCanManage {
				parentTools = append(parentTools, "manage_worktree")
			}
			const coordinatorType = "test:limited-coordinator"
			root.pluginAgents[coordinatorType] = plugin.Agent{
				Name:         "limited-coordinator",
				Model:        "inherit",
				Tools:        parentTools,
				SystemPrompt: "Use the supplied tools.",
				PluginName:   "test",
			}

			nestedArgs, err := json.Marshal(map[string]any{
				"prompt":    "Create a dirty isolated child worktree.",
				"isolation": "worktree",
				"name":      "nested-dirty-hint",
			})
			if err != nil {
				t.Fatalf("marshal nested delegate args: %v", err)
			}
			parentSteps := []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return communicateWithDefaultOutput("coordinator ready")
				},
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: "create-nested", Name: "delegate", Arguments: nestedArgs, Type: "function"})
				},
				func(req llm.Request) llm.Response {
					if toolResultTextForCall(req, "create-nested") == "" {
						t.Error("coordinator did not receive its nested delegate creation result")
					}
					return communicateWithDefaultOutput("nested child launched")
				},
			}
			for range 4 {
				parentSteps = append(parentSteps, func(llm.Request) llm.Response {
					return communicateWithDefaultOutput("parent handled child notification")
				})
			}
			parentClient := llm.NewClient()
			parentClient.Register(&fakeAdapter{name: "openai", steps: parentSteps})
			registerTestSessionNamer(parentClient)

			childClient := llm.NewClient()
			childClient.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(writeFileCall("dirty-file", "CHILD_WORK.txt", "nested dirty worktree"))
				},
				func(req llm.Request) llm.Response {
					if toolResultTextForCall(req, "dirty-file") == "" {
						t.Error("child did not receive its file-write result")
					}
					return communicateWithDefaultOutput("child worktree updated")
				},
			}})
			registerTestSessionNamer(childClient)

			clients := make(chan *llm.Client, 2)
			clients <- parentClient
			clients <- childClient
			root.cfg.testOnly.childClientFactory = func() *llm.Client { return <-clients }

			allowance := 1
			coordinator := root.createDelegate(context.Background(), delegateArgs{
				Task:                "coordinator startup",
				AgentType:           coordinatorType,
				DelegationAllowance: &allowance,
			})
			if coordinator.Err != nil {
				t.Fatalf("create coordinator: %v", coordinator.Err)
			}
			parent := root.subagents.get(coordinator.ChildSessionID)
			if parent == nil || parent.sess == nil {
				t.Fatalf("coordinator session %q is missing", coordinator.ChildSessionID)
			}
			if got := parent.sess.reg.Get("manage_worktree") != nil; got != parentCanManage {
				t.Fatalf("coordinator manage_worktree capability = %t, want %t", got, parentCanManage)
			}
			waitSubagentRun(t, parent)

			outcome := (delegateRuntime{owner: root}).send(context.Background(), coordinator.DelegateID, "create nested dirty child", 60_000)
			if outcome.result.Err != nil {
				t.Fatalf("send nested-create to coordinator: %v", outcome.result.Err)
			}

			var nestedID string
			root.delegateController.mu.Lock()
			for id, aggregate := range root.delegateController.durable {
				if aggregate.Descriptor.ParentDelegateID == coordinator.DelegateID {
					nestedID = id
					break
				}
			}
			root.delegateController.mu.Unlock()
			if nestedID == "" {
				t.Fatalf("coordinator %q did not create a nested delegate", coordinator.DelegateID)
			}
			nested := delegateAggregateSnapshot(t, root.delegateController, nestedID)
			if nested.Descriptor.ParentDelegateID != coordinator.DelegateID {
				t.Fatalf("nested delegate parent = %q, want coordinator %q", nested.Descriptor.ParentDelegateID, coordinator.DelegateID)
			}
			child := parent.sess.subagents.get(nested.Descriptor.ChildSessionID)
			if child == nil {
				t.Fatalf("nested child session %q is missing", nested.Descriptor.ChildSessionID)
			}
			waitSubagentRun(t, child)

			nested = delegateAggregateSnapshot(t, root.delegateController, nestedID)
			if nested.LatestPacket == nil {
				t.Fatal("nested delegate has no terminal packet")
			}
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(nested.LatestPacket.Metadata, &metadata); err != nil {
				t.Fatalf("decode nested terminal metadata: %v", err)
			}
			if metadata.Worktree == nil || !metadata.Worktree.Dirty {
				t.Fatalf("nested terminal worktree report = %+v, want dirty worktree", metadata.Worktree)
			}
			if got := metadata.Worktree.DisposalHint != ""; got != parentCanManage {
				t.Fatalf("nested disposal hint present = %t, want %t based on immediate parent's capability", got, parentCanManage)
			}
			worktreePath := nested.Descriptor.WorkingDir
			if contents, err := os.ReadFile(filepath.Join(worktreePath, "CHILD_WORK.txt")); err != nil || string(contents) != "nested dirty worktree" {
				t.Fatalf("nested worktree file = %q, err=%v", contents, err)
			}
			if status := strings.TrimSpace(wtGit(t, worktreePath, "status", "--short")); status != "?? CHILD_WORK.txt" {
				t.Fatalf("nested dirty worktree status = %q", status)
			}
		})
	}
}

func toolResultTextForCall(req llm.Request, callID string) string {
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return fmt.Sprint(part.ToolResult.Content)
			}
		}
	}
	return ""
}

func latestUserMessageText(req llm.Request) string {
	for _, message := range slices.Backward(req.Messages) {
		if message.Role == llm.RoleUser {
			return message.Text()
		}
	}
	return ""
}

func TestDisposalHint_StableOwnedDelegateDistinguishesDirtyWorktree(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	desc := delegatestore.Descriptor{OwnerSessionID: s.id}
	clean := s.delegateDisposalHint(desc, "dlg_01ISODISPOSALHINT0000001", false)
	dirty := s.delegateDisposalHint(desc, "dlg_01ISODISPOSALHINT0000001", true)
	if clean == "" || dirty == "" {
		t.Fatalf("clean hint %q and dirty hint %q must both be present", clean, dirty)
	}
	if clean == dirty {
		t.Fatalf("dirty hint must differ from clean disposal guidance: %q", dirty)
	}
	if !strings.Contains(dirty, "force_dirty") {
		t.Fatalf("dirty hint must identify the explicit discard option: %q", dirty)
	}
}

func TestDisposalHint_JSONRoundTripEmitsField(t *testing.T) {
	t.Parallel()
	in := &delegateWorktreeReport{Path: "/lane", Branch: "b", HeadSHA: "abc123", DisposalHint: "cleanup guidance"}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"disposal_hint":`) {
		t.Fatalf("JSON missing disposal_hint key: %s", blob)
	}
	var back delegateWorktreeReport
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.DisposalHint != in.DisposalHint || back.HeadSHA != "abc123" {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestDisposalHint_EmptyHintOmittedFromJSON(t *testing.T) {
	t.Parallel()
	blob, err := json.Marshal(&delegateWorktreeReport{Path: "/lane", Branch: "b"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "disposal_hint") {
		t.Fatalf("empty hint must be omitted, got: %s", blob)
	}
}

func TestDisposalHint_StableOwnershipAndToolGate(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	id := "dlg_01ISODISPOSALHINT0000001"
	if got := s.delegateDisposalHint(delegatestore.Descriptor{OwnerSessionID: "another-session"}, id, true); got != "" {
		t.Fatalf("foreign descriptor hint = %q", got)
	}
	s.reg.Remove("manage_worktree")
	if got := s.delegateDisposalHint(delegatestore.Descriptor{OwnerSessionID: s.id}, id, true); got != "" {
		t.Fatalf("session without dispose tool hint = %q", got)
	}
}

func TestDisposalHint_TerminalMetadataAndInlineResultPreserveHint(t *testing.T) {
	t.Parallel()
	hint := "inspect before disposal; force_dirty discards changes"
	inputs := delegateTerminalRunInputs{
		descriptor: delegatestore.Descriptor{OwnerSessionID: "owner"},
		worktree:   &delegateWorktreeReport{Path: "/lane", Branch: "feature", HeadSHA: "abc123", Ahead: 2, Dirty: true, DisposalHint: hint},
	}
	metadata := delegateTerminalMetadataFromRun(inputs)
	if metadata.Worktree == nil || metadata.Worktree.DisposalHint != hint || !metadata.Worktree.Dirty {
		t.Fatalf("terminal worktree metadata = %+v", metadata.Worktree)
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal terminal metadata: %v", err)
	}
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Metadata: rawMetadata}
	var result sendMessageResult
	populateDelegateSendResult(&result, packet)
	if result.Worktree == nil || result.Worktree.DisposalHint != hint || !result.Worktree.Dirty {
		t.Fatalf("inline worktree result = %+v", result.Worktree)
	}
}

func TestDisposalHint_NonIsolatedDelegateHasNoReport(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if report := s.descriptorWorktreeReport(delegatestore.Descriptor{OwnerSessionID: s.id}); report != nil {
		t.Fatalf("non-isolated delegate report = %+v", report)
	}
}
