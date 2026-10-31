package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
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
			parentAdapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					args, err := json.Marshal(map[string]any{
						"prompt":    "Create CHILD_WORK.txt in your worktree and report completion.",
						"isolation": "worktree",
						"name":      "disposal-hint-e2e",
					})
					if err != nil {
						t.Fatalf("marshal delegate args: %v", err)
					}
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: "create-worktree", Name: "delegate", Arguments: args, Type: "function"})
				},
				func(req llm.Request) llm.Response {
					creationOutput := toolResultTextForCall(req, "create-worktree")
					if err := json.Unmarshal([]byte(creationOutput), &created); err != nil {
						t.Fatalf("decode delegate creation result %q: %v", creationOutput, err)
					}
					return communicateWithDefaultOutput("child created")
				},
				func(llm.Request) llm.Response {
					args, err := json.Marshal(map[string]any{
						"to":          created.DelegateID,
						"message":     "Report completion and make no further filesystem changes.",
						"max_wait_ms": 60_000,
					})
					if err != nil {
						t.Fatalf("marshal delegate_send args: %v", err)
					}
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: "send-worktree", Name: "delegate_send", Arguments: args, Type: "function"})
				},
				func(req llm.Request) llm.Response {
					sendOutput = toolResultTextForCall(req, "send-worktree")
					return communicateWithDefaultOutput("delegate result received")
				},
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
