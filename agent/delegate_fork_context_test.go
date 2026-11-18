package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// Only an explicit opt-in may send the parent's conversation to a delegate.
// Completed tool exchanges must survive, while the in-flight spawn must not.
func TestDelegateForkContext_OptInHistory(t *testing.T) {
	t.Parallel()
	t.Run("checkout observations", testForkDelegateCheckout)
	for _, option := range []any{nil, false, true} {
		name := "default"
		if option != nil {
			name = map[bool]string{false: "clean", true: "fork"}[option.(bool)]
		}
		t.Run(name, func(t *testing.T) {
			root, client, _ := newDelegateResourceBootstrapSession(t)
			adapter := newTask6FrozenDescriptorAdapter()
			client.Register(adapter)
			t.Cleanup(adapter.releaseRun)
			root.appendTurn(schema.TurnUserInput, llm.User("parent-context-sentinel"))
			root.appendTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("image-sentinel"), MediaType: "image/png"}}}})
			completed := schema.NewTurn(schema.TurnAssistant, forkContextToolCall("completed", "read_file"))
			completed.ResponseID = "parent-response-anchor"
			completed.Usage = llm.Usage{TotalTokens: 999}
			root.recordTurn(completed, completed)
			root.appendTurn(schema.TurnTool, llm.ToolResult("completed", "parent-result-sentinel", true))
			root.appendTurn(schema.TurnUserInput, llm.User("current-input-sentinel"))
			root.appendTurn(schema.TurnAssistant, forkContextToolCall("pending-spawn", "delegate"))
			// A model setting can change while a tool is still executing.
			root.appendTurn(schema.TurnModelSwitch, llm.System("model-marker-sentinel"))

			params := map[string]any{"prompt": "child-assignment-sentinel", "delegation_allowance": float64(0)}
			if option != nil {
				params["fork_context"] = option
			}
			args, err := decodeDelegateArgs(params)
			if err != nil {
				t.Fatal(err)
			}
			result := root.createDelegate(context.Background(), args)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			var request llm.Request
			select {
			case request = <-adapter.entered:
			case <-time.After(10 * time.Second): // TRIPWIRE: the scripted provider signals the first request.
				t.Fatal("child did not issue a request")
			}
			wantHistory := option == true
			for _, marker := range []string{"parent-context-sentinel", "current-input-sentinel"} {
				if got := requestContainsText(request, marker); got != wantHistory {
					t.Errorf("inherited %s = %v, want %v", marker, got, wantHistory)
				}
			}
			if !requestContainsText(request, "child-assignment-sentinel") {
				t.Error("child assignment missing")
			}
			foundToolResult := false
			foundImage := false
			for _, message := range request.Messages {
				for _, part := range message.Content {
					if part.Image != nil && string(part.Image.Data) == "image-sentinel" {
						foundImage = true
					}
					if part.ToolResult != nil && part.ToolResult.ToolCallID == "completed" && part.ToolResult.Content == "parent-result-sentinel" {
						foundToolResult = true
					}
					if part.ToolCall != nil && part.ToolCall.ID == "pending-spawn" {
						t.Error("unfinished spawn call reached the child")
					}
				}
			}
			if foundToolResult != wantHistory {
				t.Errorf("inherited completed tool result = %v, want %v", foundToolResult, wantHistory)
			}
			if foundImage != wantHistory {
				t.Errorf("inherited image = %v, want %v", foundImage, wantHistory)
			}
			child := root.getSub(result.ChildSessionID).sess
			for _, msg := range request.Messages {
				tracker := envctx.NewTracker(envctx.State{})
				if tracker.ReplayBlock(msg.Text()) && tracker.State().Last.SessionID != "" {
					t.Error("delegate repeated its startup identity without an inherited identity update")
				}
			}
			meta := child.Meta()
			if !meta.IsSubagent || meta.OriginalPrompt != "child-assignment-sentinel" || meta.CumulativeUsage.TotalTokens != 0 {
				t.Errorf("child identity or accounting inherited: %+v", meta)
			}
			if child.reg.Get("ask_user") != nil || child.reg.Get("delegate") != nil {
				t.Error("leaf child gained parent tools")
			}
			if failures, measured := child.FailedToolCallsSnapshot(); !measured || failures != 0 {
				t.Errorf("child charged for parent's tool failure: %d, measured=%v", failures, measured)
			}
			data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, child.id), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range data.Entries {
				if entry.Turn.ResponseID == "parent-response-anchor" {
					t.Error("parent continuation anchor copied into child")
				}
			}
		})
	}
}

func testForkDelegateCheckout(t *testing.T) {
	const parentBranch, childBranch, dispatchBranch = "parent-17", "child-28", "dispatch-39"
	for _, tc := range []struct {
		name, launch, dispatch, want string
		moved, legacy                bool
	}{
		{name: "shared checkout", want: parentBranch},
		{name: "confirmed child launch", launch: childBranch, want: childBranch},
		{name: "confirmed child dispatch", dispatch: dispatchBranch, want: dispatchBranch},
		{name: "different checkout", moved: true},
		{name: "inherited branch without a checkout", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := evalSyms(t, t.TempDir())
			copyWorktreeBaseRepo(t, cwd)
			base := execenv.NewLocalExecutionEnvironment(cwd)
			var childProbe bool
			probes := &envctx.Probes{GitBranch: func(dir string) (string, bool) {
				if childProbe {
					return tc.dispatch, tc.dispatch != ""
				}
				return snapshotGitBranch(base, dir)
			}}
			root := newSession(t, withEnv(base), withEnvironmentSnapshot(), withConfig(SessionConfig{
				StateDir: t.TempDir(), MaxSubagentDepth: 1, ForceRealIO: true, testOnly: testConfig{envProbes: probes},
			}))
			gittest.Run(t, cwd, "checkout", "-b", parentBranch)
			if tc.legacy {
				// Older environment turns could name a branch before any turn named its checkout.
				root.appendTurn(schema.TurnEnvironment, llm.User("<environment_context>\ngit branch: "+parentBranch+"\n</environment_context>"))
			} else if err := root.maybeAppendEnvironmentContext(); err != nil {
				t.Fatal(err)
			}
			if tc.moved {
				cwd = evalSyms(t, t.TempDir())
				gittest.Run(t, cwd, "init")
				if err := root.swapEnvAndRefresh(base.WithWorkingDirectory(cwd), nil); err != nil {
					t.Fatal(err)
				}
			} else if tc.launch != "" {
				gittest.Run(t, cwd, "checkout", "-b", tc.launch)
			} else {
				gittest.Run(t, cwd, "checkout", "--orphan", "unborn")
			}
			childProbe = true
			adapter := newTask6FrozenDescriptorAdapter()
			root.client.Register(adapter)
			t.Cleanup(adapter.releaseRun)
			result := root.createDelegate(t.Context(), delegateArgs{Task: "inspect checkout", ForkContext: true, DelegationAllowance: new(0)})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			select {
			case req := <-adapter.entered:
				child := root.getSub(result.ChildSessionID).sess
				state, _ := requestEnvironmentState(req)
				if state.Last.Cwd != cwd || state.Last.GitBranch != tc.want || state.GitBranchUnknown != (tc.want == "") || req.Messages[0].Text() != child.transcript.Header().SystemPrompt {
					t.Errorf("first delegate request saw cwd %q, branch %q, unknown %t; want %q on %q, original prompt %t", state.Last.Cwd, state.Last.GitBranch, state.GitBranchUnknown, cwd, tc.want, req.Messages[0].Text() == child.transcript.Header().SystemPrompt)
				}
			case <-time.After(10 * time.Second): // TRIPWIRE: the scripted provider signals its first request.
				t.Fatal("forked delegate did not issue a request")
			}
		})
	}
}

func TestDelegateForkContext_DescendantsStartClean(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	root.appendTurn(schema.TurnUserInput, llm.User("ancestor-context-sentinel"))
	grandchild := newTask6FrozenDescriptorAdapter()
	t.Cleanup(grandchild.releaseRun)
	var constructed atomic.Int32
	root.cfg.testOnly.childClientFactory = func() *llm.Client {
		client := llm.NewClient()
		if constructed.Add(1) == 1 {
			client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response {
				message := forkContextToolCall("nested-spawn", "delegate")
				message.Content[0].ToolCall.Arguments = []byte(`{"prompt":"grandchild-unit-sentinel","delegation_allowance":0}`)
				return llm.Response{Message: message}
			}, func(llm.Request) llm.Response { return agenttest.FinalResponse("child finished") }))
		} else {
			client.Register(grandchild)
		}
		return client
	}
	result := root.createDelegate(context.Background(), delegateArgs{Task: "middle-assignment-sentinel", ForkContext: true, DelegationAllowance: new(1)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case req := <-grandchild.entered:
		if requestContainsText(req, "ancestor-context-sentinel") || requestContainsText(req, "middle-assignment-sentinel") || !requestContainsText(req, "grandchild-unit-sentinel") {
			t.Fatal("grandchild inherited context without opting in")
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: real delegate dispatch must reach the scripted grandchild provider.
		t.Fatal("grandchild did not issue a request")
	}
}

func TestDelegateForkContext_RejectsMismatchedSourceTranscript(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	if err := root.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(transcriptPath(root.stateDir, root.id), transcript.Header{SessionID: identifier.MustNewSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.snapshotDelegateContext(); err == nil {
		t.Fatal("fork accepted another session's transcript")
	}
}

// A cold delegate resumes from its own saved snapshot, even after its parent
// has continued. Parent compaction controls the working context but must not
// erase the earlier conversation from the child's transcript.
func TestDelegateForkContext_ColdResumePreservesSnapshot(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	root.appendTurn(schema.TurnUserInput, llm.User("archived-parent-sentinel"))
	root.appendTurn(schema.TurnCheckpoint, llm.User("parent-summary-sentinel"))
	oldIdentity := envctx.NewTracker(envctx.State{}).RenderDiff(envctx.Snapshot{SessionID: root.ID(), CommitTrailer: root.commitTrailerPrompt()})
	root.appendTurn(schema.TurnEnvironment, llm.User(oldIdentity))
	root.appendTurn(schema.TurnUserInput, llm.User("recent-parent-sentinel"))
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("child-result-sentinel") })
	client.Register(adapter)
	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-unit-sentinel", ForkContext: true, DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	sub := root.getSub(result.ChildSessionID)
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: scripted completion; synchronization is the closed run channel.
		t.Fatal("child did not complete")
	}
	root.delegateController.mu.Lock()
	root.delegateController.maxRetainedTerminal = 1
	root.delegateController.mu.Unlock()
	if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatal(err)
	}
	if root.getSub(result.ChildSessionID) != nil {
		t.Fatal("child runtime was not reclaimed")
	}
	root.appendTurn(schema.TurnUserInput, llm.User("after-fork-sentinel"))
	resumedAdapter := newTask6FrozenDescriptorAdapter()
	client.Register(resumedAdapter)
	t.Cleanup(resumedAdapter.releaseRun)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), result.DelegateID, "followup-sentinel", 0)
	if outcome.result.Err != nil {
		t.Fatal(outcome.result.Err)
	}
	var req llm.Request
	select {
	case req = <-resumedAdapter.entered:
	case <-time.After(10 * time.Second): // TRIPWIRE: the restored runtime must reach the scripted provider.
		t.Fatal("resumed child did not issue a request")
	}
	for _, marker := range []string{"parent-summary-sentinel", "recent-parent-sentinel", "child-unit-sentinel", "followup-sentinel"} {
		if !requestContainsText(req, marker) {
			t.Errorf("missing resumed context %s", marker)
		}
	}
	for _, marker := range []string{"archived-parent-sentinel", "after-fork-sentinel"} {
		if requestContainsText(req, marker) {
			t.Errorf("unexpected context %s", marker)
		}
	}
	child := root.getSub(result.ChildSessionID).sess
	for _, captured := range []llm.Request{adapter.Requests()[0], req} {
		tracker := envctx.NewTracker(envctx.State{})
		for _, msg := range captured.Messages {
			tracker.ReplayBlock(msg.Text())
		}
		if last := tracker.State().Last; last.SessionID != child.ID() || last.CommitTrailer != child.commitTrailerPrompt() {
			t.Errorf("delegate kept inherited attribution: %+v", last)
		}
	}
	meta := child.Meta()
	if !meta.IsSubagent || meta.DivergenceTurn == 0 || meta.OriginalPrompt != "child-unit-sentinel" || child.reg.Get("ask_user") != nil {
		t.Fatalf("restored delegate lost identity or gained permissions: %+v", meta)
	}
	data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, child.id), "")
	if err != nil {
		t.Fatal(err)
	}
	foundArchive := false
	for _, entry := range data.Entries {
		if entry.Turn.Message.Text() == "archived-parent-sentinel" {
			foundArchive = true
		}
	}
	if !foundArchive {
		t.Fatal("fork discarded the parent's archived conversation")
	}
}

func TestDelegateForkContext_RejectsDifferentModelBeforeCreation(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	result := root.createDelegate(context.Background(), delegateArgs{Task: "unit", ForkContext: true, Model: "gpt-5.5"})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "fork_context") || result.DelegateID != "" {
		t.Fatalf("different-model fork = %+v", result)
	}
}

// A direct resume has no live parent spawn config. It must still identify the
// delegate by its assignment rather than the first inherited user message.
func TestDelegateForkContext_DirectResumeKeepsAssignment(t *testing.T) {
	t.Parallel()
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	workspace = evalSyms(t, workspace)
	meta.IsSubagent = true
	meta.ParentSessionID = identifier.MustNewSessionID()
	meta.DivergenceTurn = 3
	meta.OriginalPrompt = "assigned-unit-sentinel"
	if branch := meta.PromptLaunch.GitBranch; branch == nil || *branch != "" {
		t.Fatal("fixture did not launch outside a worktree")
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	header := transcript.Header{SessionID: meta.ID, ParentSessionID: meta.ParentSessionID, Task: meta.OriginalPrompt, WorkingDir: workspace, SystemPrompt: "\tdelegate prompt\n"}
	writer, err := transcript.NewWriter(transcriptPath(stateDir, meta.ID), header)
	if err != nil {
		t.Fatal(err)
	}
	parentEnv := envctx.NewTracker(envctx.State{}).RenderDiff(envctx.Snapshot{Cwd: workspace, GitBranch: "parent-17"})
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("parent-request-sentinel")),
		schema.NewTurn(schema.TurnEnvironment, llm.User(parentEnv)),
		schema.NewTurn(schema.TurnUserInput, llm.User(meta.OriginalPrompt)),
	} {
		if err := writer.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreDelegateResourceBootstrapSessionWithTestConfig(client, profile, workspace, meta, stateDir, testConfig{
		envProbes: &envctx.Probes{GitBranch: func(string) (string, bool) { return "", false }},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.Meta(); !got.IsSubagent || got.OriginalPrompt != "assigned-unit-sentinel" {
		t.Fatalf("directly resumed delegate identity = %+v", got)
	}
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	client.Register(adapter)
	sendOneUserInput(t, restored, "resume checkout inspection")
	req := adapter.Requests()[0]
	state, _ := requestEnvironmentState(req)
	if state.Last.Cwd != workspace || state.GitBranchUnknown || state.Last.GitBranch != "" || req.Messages[0].Text() != header.SystemPrompt {
		t.Errorf("resumed delegate saw cwd %q, branch %q, unknown %t; want confirmed outside at %q, original prompt %t", state.Last.Cwd, state.Last.GitBranch, state.GitBranchUnknown, workspace, req.Messages[0].Text() == header.SystemPrompt)
	}
}

func TestDelegateForkContext_SchemaOffersOptionalBoolean(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	definition := root.delegateToolDefinition()
	property, ok := definition.Parameters["properties"].(map[string]any)["fork_context"].(map[string]any)
	if !ok || property["type"] != "boolean" || property["default"] != false {
		t.Fatalf("fork_context schema = %#v", property)
	}
	for _, required := range definition.Parameters["required"].([]string) {
		if required == "fork_context" {
			t.Fatal("clean-session callers must not need to supply fork_context")
		}
	}
}

func TestDelegateForkContext_RejectsMalformedOption(t *testing.T) {
	t.Parallel()
	for _, option := range []any{"true", 1, nil, []any{true}} {
		if _, err := decodeDelegateArgs(map[string]any{"prompt": "unit", "fork_context": option}); err == nil {
			t.Errorf("accepted fork_context=%#v", option)
		}
	}
}

func forkContextToolCall(id, name string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: []byte(`{}`), Type: "function"},
	}}}
}
