package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// One-shot sessions (TurnEndsProcess) drop the tools whose only reader is a
// later session. The prompt sections and the advertised tool list read the
// registry, so this test checks all three views agree.
func TestOneShotOmitsCeremonyTools(t *testing.T) {
	t.Parallel()
	ceremonyTools := oneShotCeremonyToolNames

	t.Run("one-shot omits notes and URL tools", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: true,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		advertised := toolNameSetFromDefinitions(s.ToolDefinitions())
		for _, name := range ceremonyTools {
			if s.reg.Get(name) != nil {
				t.Errorf("registry still has %q under TurnEndsProcess=true", name)
			}
			if advertised[name] {
				t.Errorf("advertised tools still include %q under TurnEndsProcess=true", name)
			}
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Error("registry omitted manage_worktree under TurnEndsProcess=true")
		}
		if !advertised["manage_worktree"] || !advertised["tool_search"] {
			t.Error("advertised tools omitted manage_worktree stub or tool_search under TurnEndsProcess=true")
		}
		if advertised["call_tool"] {
			t.Error("advertised tools unexpectedly included call_tool under TurnEndsProcess=true")
		}

		data, _ := s.buildPromptData(s.env)
		for _, name := range ceremonyTools {
			if data.HasTool(name) {
				t.Errorf("promptData.HasTool(%q) = true under TurnEndsProcess=true", name)
			}
		}
		if !data.HasTool("tool_search") {
			t.Error("promptData.HasTool(\"tool_search\") = false under TurnEndsProcess=true")
		}
		if data.HasTool("manage_worktree") {
			t.Error("promptData.HasTool(\"manage_worktree\") = true while manage_worktree is deferred")
		}

		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if strings.Contains(prompt, "\n## Session links\n") {
			t.Error("prompt renders ## Session links although no URL tool is callable")
		}
		finishing := systemPromptSection(t, prompt, "## Finishing")
		if strings.Contains(finishing, "|---|---|") {
			t.Errorf("## Finishing renders a table with no rows:\n%s", finishing)
		}
		if strings.Contains(finishing, "notes_agent_set") {
			t.Errorf("## Finishing mentions omitted tool notes_agent_set:\n%s", finishing)
		}
		if strings.Contains(prompt, "\n## Memory\n") {
			t.Error("prompt renders ## Memory when memory is unconfigured")
		}
	})

	t.Run("one-shot keeps memory finishing guidance when memory saves are enabled", func(t *testing.T) {
		t.Parallel()
		withoutMemory := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: true,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		withoutPrompt, warning, _ := withoutMemory.renderSystemPrompt(withoutMemory.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt (no memory): %s", warning)
		}
		withoutFinishing := systemPromptSection(t, withoutPrompt, "## Finishing")

		s := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: true,
			MemoryStateRoot: t.TempDir(),
			MemoryProjectID: "oneshot-ceremony",
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		data, _ := s.buildPromptData(s.env)
		if !data.MemorySaves {
			t.Fatal("fixture precondition: MemorySaves = false, want true")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		finishing := systemPromptSection(t, prompt, "## Finishing")
		if !strings.Contains(prompt, "\n## Memory\n") {
			t.Error("prompt omitted ## Memory although MemorySaves=true")
		}
		if strings.Contains(finishing, "notes_agent_set") {
			t.Errorf("## Finishing mentions omitted tool notes_agent_set:\n%s", finishing)
		}
		if finishing == withoutFinishing {
			t.Errorf("## Finishing did not add memory-save guidance when MemorySaves=true:\n%s", finishing)
		}
	})

	t.Run("persistent session keeps notes and URL tools", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: false,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		defs := s.ToolDefinitions()
		advertised := toolNameSetFromDefinitions(defs)
		byName := make(map[string]llm.ToolDefinition, len(defs))
		for _, td := range defs {
			byName[td.Name] = td
		}
		for _, name := range ceremonyTools {
			if s.reg.Get(name) == nil {
				t.Errorf("registry missing %q under TurnEndsProcess=false", name)
			}
			if !advertised[name] {
				t.Errorf("advertised tools missing deferred stub %q under TurnEndsProcess=false", name)
				continue
			}
			assertDeferredStubDef(t, byName[name], "")
		}
		if !advertised["tool_search"] || advertised["call_tool"] {
			t.Error("advertised tools want tool_search without call_tool under TurnEndsProcess=false")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if !strings.Contains(prompt, "\n## Session links\n") {
			t.Error("prompt missing ## Session links under TurnEndsProcess=false")
		}
		finishing := systemPromptSection(t, prompt, "## Finishing")
		if !strings.Contains(finishing, "notes_agent_set") {
			t.Errorf("## Finishing missing notes_agent_set guidance under TurnEndsProcess=false:\n%s", finishing)
		}
	})
}

// A one-shot session keeps manage_worktree, whose handler resolves the
// repository on every call. The launch snapshot must resolve symlinks too so a
// --dir symlink into a checkout reports IsGitRepo=true and renders ## Git and safety.
func TestOneShotWorktreeToolFollowsResolvedCheckout(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	subDir := filepath.Join(repoDir, "nested", "workspace")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link-into-checkout")
	if err := os.Symlink(subDir, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	oneShot := func(t *testing.T, dir string, snapshot bool) *Session {
		t.Helper()
		opts := []sessionOpt{
			withDir(dir),
			withConfig(SessionConfig{
				TurnEndsProcess: true,
				AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			}),
		}
		if snapshot {
			opts = append(opts, withEnvironmentSnapshot())
		}
		return newSession(t, opts...)
	}

	t.Run("symlink into a checkout keeps manage_worktree", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, link, false)
		if s.envInfo.IsGitRepo {
			t.Fatal("fixture precondition: the lexical launch snapshot claims a repository, so this case cannot prove symlink resolution")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted although the working directory resolves into a checkout through a symlink")
		}
		if !toolNameSetFromDefinitions(s.ToolDefinitions())["manage_worktree"] {
			t.Fatal("advertised tools omit manage_worktree although the directory resolves into a checkout")
		}
	})

	t.Run("confirmed non-repository keeps manage_worktree and renders Safety", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, t.TempDir(), true)
		if s.envInfo.IsGitRepo {
			t.Fatal("non-repository directory reported IsGitRepo=true")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted although its handler would serve a task that runs git init")
		}
		if !toolNameSetFromDefinitions(s.ToolDefinitions())["manage_worktree"] {
			t.Fatal("advertised tools omit manage_worktree in a confirmed non-repository")
		}
		data, _ := s.buildPromptData(s.env)
		if data.IsGitRepo {
			t.Fatal("promptData.IsGitRepo = true for a non-repository directory")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if strings.Contains(prompt, "\n## Git and safety\n") {
			t.Error("prompt renders ## Git and safety in a confirmed non-repository")
		}
		if !strings.Contains(prompt, "\n## Safety\n") {
			t.Error("prompt is missing ## Safety in a confirmed non-repository")
		}
	})

	t.Run("symlinked --dir into a checkout reports a repository", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, link, true)
		if !s.envInfo.IsGitRepo {
			t.Fatal("launch snapshot missed the checkout behind the symlinked --dir")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted although the working directory resolves into a checkout")
		}
		data, _ := s.buildPromptData(s.env)
		if !data.IsGitRepo {
			t.Fatal("promptData.IsGitRepo = false for a symlinked --dir into a checkout")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if !strings.Contains(prompt, "\n## Git and safety\n") {
			t.Error("prompt is missing ## Git and safety although the directory resolves into a checkout")
		}
		if strings.Contains(prompt, "\n## Safety\n") {
			t.Error("prompt renders ## Safety instead of ## Git and safety for a symlinked checkout")
		}
	})

	t.Run("real checkout keeps manage_worktree and Git and safety", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, repoDir, true)
		if !s.envInfo.IsGitRepo {
			t.Fatal("fixture precondition: launch snapshot did not detect the repository")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted inside a real checkout")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if !strings.Contains(prompt, "\n## Git and safety\n") {
			t.Error("prompt is missing ## Git and safety inside a real checkout")
		}
	})

	t.Run("persistent session keeps manage_worktree outside a repository", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
			TurnEndsProcess: false,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted under TurnEndsProcess=false")
		}
	})
}

// manage_worktree spends nothing on a directory that is not a repository, and
// its handler resolves git on each call. A task in a fresh directory can run
// git init and then use it, and a resumed one-shot session can retire the
// isolated delegate lanes its saved run left behind.
func TestOneShotKeepsWorktreeToolOutsideCheckout(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	launched := newSession(t, withDir(stateDir), withSteps(repeatFinalResponse(4, "ok")...), withConfig(SessionConfig{
		StateDir:        stateDir,
		TurnEndsProcess: true,
		AgentsDocPath:   filepath.Join(stateDir, "AGENTS.md"),
		testOnly:        testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	if launched.reg.Get("manage_worktree") == nil {
		t.Fatal("fresh one-shot session outside a checkout omitted manage_worktree, so git init could not lead to a worktree")
	}
	sendOneUserInput(t, launched, "start")
	id := launched.ID()
	launched.Close()

	resumed := restoreQueuePersistTestSessionWith(t, stateDir, id, RestoreSessionConfig{
		TurnEndsProcess: true,
		testOnly:        launched.cfg.testOnly,
	})
	t.Cleanup(resumed.Close)
	if resumed.reg.Get("manage_worktree") == nil {
		t.Fatal("resumed one-shot session dropped manage_worktree, so its saved delegate lanes cannot be unlocked or disposed")
	}
}

// One-shot roots and one-shot subagents have no session link list and must omit
// ## Session links entirely, while persistent roots and persistent subagents
// keep their respective ## Session links sections.
func TestOneShotSessionLinksHeadingOmitted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		isSubagent      bool
		turnEndsProcess bool
		wantHeading     bool
	}{
		{"one-shot root", false, true, false},
		{"one-shot subagent", true, true, false},
		{"persistent root", false, false, true},
		{"persistent subagent", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
				TurnEndsProcess:  tc.turnEndsProcess,
				MaxSubagentDepth: 2,
				StateDir:         t.TempDir(),
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
				testOnly:         testConfig{skipGitSnapshot: true, noSyncJobStore: true},
			}))
			target := parent
			if tc.isSubagent {
				prepared := prepareStableChild(t, parent, delegateArgs{Task: "task"})
				target = prepared.sub.sess
				t.Cleanup(target.Close)
			}
			data, _ := target.buildPromptData(target.env)
			checkPromptInput(t, "IsSubagent", data.IsSubagent, tc.isSubagent)
			checkPromptInput(t, "TurnEndsProcess", data.TurnEndsProcess, tc.turnEndsProcess)

			prompt, warning, _ := target.renderSystemPrompt(target.env)
			if warning != "" {
				t.Fatalf("renderSystemPrompt: %s", warning)
			}
			gotHeading := strings.Contains(prompt, "\n## Session links\n")
			wantHeading := tc.isSubagent && !tc.turnEndsProcess
			if gotHeading != wantHeading {
				t.Errorf("prompt contains ## Session links = %v, want %v", gotHeading, wantHeading)
			}
		})
	}
}

func sendAndSettleDelegate(t *testing.T, owner *Session, delegateID, message string) delegateSendOutcome {
	t.Helper()
	outcome := (delegateRuntime{owner: owner}).send(context.Background(), delegateID, message, 60_000)
	settleSendDelivery(t, owner, outcome)
	return outcome
}

func repeatFakeStep(n int, fn func(llm.Request) llm.Response) []func(llm.Request) llm.Response {
	out := make([]func(llm.Request) llm.Response, n)
	for i := range out {
		out[i] = fn
	}
	return out
}

func hasUserPromptSubstring(msgs []llm.Message, substr string) bool {
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, p := range m.Content {
			if p.Kind == llm.ContentText && strings.Contains(p.Text, substr) {
				return true
			}
		}
	}
	return false
}

func TestOneShotDefaultDelegateRegainsNotesOnPersistentResume(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		agentType string
	}{
		{name: "implicit default", agentType: ""},
		{name: "explicit default", agentType: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			stateDir := realTempDirForTest(t)
			agentsDoc := filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
			testOnly := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}

			var mu sync.Mutex
			oneShotToolsBySession := map[string]map[string]bool{}
			oneShotClient := llm.NewClient()
			oneShotClient.Register(newFakeAdapter("openai", repeatFakeStep(16, func(req llm.Request) llm.Response {
				mu.Lock()
				oneShotToolsBySession[req.SessionID] = toolNameSetFromDefinitions(req.Tools)
				mu.Unlock()
				return agenttest.FinalResponse("oneshot child done")
			})...))
			oneShotProfile := withTestSessionNamer(oneShotClient, NewOpenAIProfile("gpt-5.2"))

			root, err := NewSession(oneShotClient, oneShotProfile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
				StateDir:         stateDir,
				MaxSubagentDepth: 2,
				ForceRealIO:      true,
				AgentsDocPath:    agentsDoc,
				TurnEndsProcess:  true,
				testOnly:         testOnly,
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			created := root.createDelegate(context.Background(), delegateArgs{
				Task:                "initial oneshot work",
				AgentType:           tc.agentType,
				DelegationAllowance: new(0),
			})
			if created.Err != nil {
				t.Fatalf("createDelegate: %v", created.Err)
			}
			awaitTreeQuiesced(t, root)

			mu.Lock()
			oneShotChildReqTools := oneShotToolsBySession[created.ChildSessionID]
			mu.Unlock()
			for _, omitted := range oneShotCeremonyToolNames {
				if oneShotChildReqTools[omitted] {
					t.Errorf("one-shot delegate request advertised %q while TurnEndsProcess=true", omitted)
				}
			}
			child := root.getSub(created.ChildSessionID)
			if child == nil || child.sess == nil {
				t.Fatalf("missing child session %q", created.ChildSessionID)
			}
			for _, omitted := range oneShotCeremonyToolNames {
				if child.sess.reg.Get(omitted) != nil {
					t.Errorf("one-shot delegate registry retained %q while TurnEndsProcess=true", omitted)
				}
			}
			desc := delegateAggregateSnapshot(t, root.delegateController, created.DelegateID).Descriptor
			for _, omitted := range oneShotCeremonyToolNames {
				if hasString(desc.ToolNameCeiling, omitted) {
					t.Errorf("one-shot delegate descriptor.ToolNameCeiling included %q: %v", omitted, desc.ToolNameCeiling)
				}
			}

			meta := root.Meta()
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatalf("SaveSessionMeta: %v", err)
			}
			root.Close()

			const noteSentinel = "whiteboard-sentinel-7621"
			var (
				resumedTools      map[string]bool
				sawNotesSetResult bool
				sawNotesReadBody  string
			)
			resumeClient := llm.NewClient()
			resumeClient.Register(newFakeAdapter("openai", repeatFakeStep(8, func(req llm.Request) llm.Response {
				mu.Lock()
				if resumedTools == nil {
					resumedTools = toolNameSetFromDefinitions(req.Tools)
				}
				mu.Unlock()
				var sawNotesReadResult bool
				for _, m := range req.Messages {
					for _, p := range m.Content {
						if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
							continue
						}
						switch p.ToolResult.Name {
						case "notes_agent_set":
							if p.ToolResult.IsError {
								t.Errorf("notes_agent_set failed on resumed delegate: %v", p.ToolResult.Content)
							}
							sawNotesSetResult = true
						case "notes_read":
							sawNotesReadResult = true
							if p.ToolResult.IsError {
								t.Errorf("notes_read failed on resumed delegate: %v", p.ToolResult.Content)
							}
							sawNotesReadBody, _ = p.ToolResult.Content.(string)
						}
					}
				}
				if !sawNotesSetResult {
					args, _ := json.Marshal(map[string]any{"note": noteSentinel, "intent": "record delegate whiteboard status"})
					return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call-notes-set", Name: "notes_agent_set", Arguments: args},
					}}}}
				}
				if !sawNotesReadResult {
					args, _ := json.Marshal(map[string]any{"intent": "verify delegate whiteboard status"})
					return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call-notes-read", Name: "notes_read", Arguments: args},
					}}}}
				}
				return agenttest.FinalResponse("resumed persistent child done")
			})...))
			resumeProfile := withTestSessionNamer(resumeClient, NewOpenAIProfile("gpt-5.2"))

			resumedRoot, err := RestoreSessionFromMetaWithConfig(resumeClient, resumeProfile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
				StateDir:        stateDir,
				ForceRealIO:     true,
				AgentsDocPath:   agentsDoc,
				TurnEndsProcess: false,
				testOnly:        testOnly,
			})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			t.Cleanup(resumedRoot.Close)
			for _, rootTool := range oneShotCeremonyToolNames {
				if resumedRoot.reg.Get(rootTool) == nil {
					t.Fatalf("resumed persistent root missing %q", rootTool)
				}
			}

			outcome := sendAndSettleDelegate(t, resumedRoot, created.DelegateID, "continue in persistent mode")
			if outcome.result.Err != nil {
				t.Fatalf("delegate_send on persistent resume: %v", outcome.result.Err)
			}
			if outcome.result.Status != "completed" {
				t.Fatalf("delegate_send status = %q, want completed (reason=%q)", outcome.result.Status, outcome.result.Reason)
			}
			for _, want := range []string{"notes_agent_set", "notes_read"} {
				if !resumedTools[want] {
					t.Errorf("resumed persistent default delegate request omitted %q", want)
				}
			}
			for _, forbidden := range []string{"urls_add", "urls_remove", "ask_user"} {
				if resumedTools[forbidden] {
					t.Errorf("resumed persistent default delegate unexpectedly gained protected tool %q", forbidden)
				}
			}
			if !sawNotesSetResult || !strings.Contains(sawNotesReadBody, noteSentinel) {
				t.Errorf("resumed delegate whiteboard update/read failed: set=%v read=%q", sawNotesSetResult, sawNotesReadBody)
			}
			resumedChild := resumedRoot.getSub(created.ChildSessionID)
			if resumedChild == nil || resumedChild.sess == nil {
				t.Fatalf("missing resumed child session %q", created.ChildSessionID)
			}
			if got := resumedChild.sess.Meta().AgentNote; got != noteSentinel {
				t.Errorf("resumed delegate persisted AgentNote = %q, want %q", got, noteSentinel)
			}
		})
	}

	t.Run("one-shot resume keeps notes tools omitted", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		stateDir := realTempDirForTest(t)
		agentsDoc := filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
		testOnly := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}

		client := llm.NewClient()
		var mu sync.Mutex
		toolsSeen := map[string]bool{}
		client.Register(newFakeAdapter("openai", repeatFakeStep(16, func(req llm.Request) llm.Response {
			mu.Lock()
			toolsSeen = toolNameSetFromDefinitions(req.Tools)
			mu.Unlock()
			return agenttest.FinalResponse("done")
		})...))
		profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
		root, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 2,
			ForceRealIO:      true,
			AgentsDocPath:    agentsDoc,
			TurnEndsProcess:  true,
			testOnly:         testOnly,
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		created := root.createDelegate(context.Background(), delegateArgs{Task: "oneshot", DelegationAllowance: new(0)})
		if created.Err != nil {
			t.Fatalf("createDelegate: %v", created.Err)
		}
		awaitTreeQuiesced(t, root)
		meta := root.Meta()
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		root.Close()

		resumedRoot, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
			StateDir:        stateDir,
			ForceRealIO:     true,
			AgentsDocPath:   agentsDoc,
			TurnEndsProcess: true,
			testOnly:        testOnly,
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(resumedRoot.Close)

		outcome := sendAndSettleDelegate(t, resumedRoot, created.DelegateID, "continue one-shot")
		if outcome.result.Err != nil {
			t.Fatalf("delegate_send: %v", outcome.result.Err)
		}
		mu.Lock()
		got := toolsSeen
		mu.Unlock()
		for _, omitted := range oneShotCeremonyToolNames {
			if got[omitted] {
				t.Errorf("one-shot resumed default delegate exposed %q", omitted)
			}
		}
	})

	t.Run("nested default grandchild regains notes tools on persistent resume", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		stateDir := realTempDirForTest(t)
		agentsDoc := filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
		testOnly := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}

		var mu sync.Mutex
		var grandchildDelegateID, grandchildSessionID string
		var grandchildResumedTools map[string]bool
		var oneShotGrandchildReqTools map[string]bool
		childSpawned := false
		childResumedGrandchild := false
		client := llm.NewClient()
		client.Register(newFakeAdapter("openai", repeatFakeStep(32, func(req llm.Request) llm.Response {
			mu.Lock()
			gid := grandchildDelegateID
			gsid := grandchildSessionID
			resumed := childResumedGrandchild
			mu.Unlock()
			if hasUserPromptSubstring(req.Messages, "coordinator child") || hasUserPromptSubstring(req.Messages, "resume child and grandchild") {
				for _, m := range req.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.Name == "delegate" {
							var out struct {
								DelegateID     string `json:"delegate_id"`
								ChildSessionID string `json:"child_session_id"`
							}
							if text, _ := p.ToolResult.Content.(string); json.Unmarshal([]byte(text), &out) == nil && out.DelegateID != "" {
								mu.Lock()
								grandchildDelegateID = out.DelegateID
								grandchildSessionID = out.ChildSessionID
								gid = out.DelegateID
								gsid = out.ChildSessionID
								mu.Unlock()
							}
						}
					}
				}
				mu.Lock()
				alreadySpawned := childSpawned
				if !alreadySpawned {
					childSpawned = true
				}
				mu.Unlock()
				if !alreadySpawned {
					args, _ := json.Marshal(map[string]any{
						"prompt":               "nested default grandchild",
						"agent_type":           "default",
						"delegation_allowance": 0,
						"intent":               "spawn nested default grandchild",
					})
					return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call-nested-delegate", Name: "delegate", Arguments: args},
					}}}}
				}
				if gid != "" && !resumed && hasUserPromptSubstring(req.Messages, "resume child and grandchild") {
					mu.Lock()
					childResumedGrandchild = true
					mu.Unlock()
					args, _ := json.Marshal(map[string]any{
						"to":          gid,
						"message":     "continue grandchild in persistent mode",
						"max_wait_ms": 60_000,
						"intent":      "resume grandchild in persistent mode",
					})
					return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call-nested-send", Name: "delegate_send", Arguments: args},
					}}}}
				}
			}
			if hasUserPromptSubstring(req.Messages, "nested default grandchild") && (gsid == "" || req.SessionID == gsid) {
				mu.Lock()
				if !resumed {
					oneShotGrandchildReqTools = toolNameSetFromDefinitions(req.Tools)
				} else {
					grandchildResumedTools = toolNameSetFromDefinitions(req.Tools)
				}
				mu.Unlock()
			}
			return agenttest.FinalResponse("done")
		})...))
		profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
		root, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 3,
			ForceRealIO:      true,
			AgentsDocPath:    agentsDoc,
			TurnEndsProcess:  true,
			testOnly:         testOnly,
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		childRes := root.createDelegate(context.Background(), delegateArgs{
			Task:                "coordinator child",
			AgentType:           "default",
			DelegationAllowance: new(1),
		})
		if childRes.Err != nil {
			t.Fatalf("create child: %v", childRes.Err)
		}
		awaitTreeQuiesced(t, root)
		mu.Lock()
		gDelegate := grandchildDelegateID
		gotOneShotGrandchild := oneShotGrandchildReqTools
		mu.Unlock()
		if gDelegate == "" {
			t.Fatal("child did not create nested default grandchild during one-shot run")
		}
		for _, omitted := range oneShotCeremonyToolNames {
			if gotOneShotGrandchild[omitted] {
				t.Errorf("one-shot default grandchild retained %q", omitted)
			}
		}

		meta := root.Meta()
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		root.Close()

		resumedRoot, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
			StateDir:        stateDir,
			ForceRealIO:     true,
			AgentsDocPath:   agentsDoc,
			TurnEndsProcess: false,
			testOnly:        testOnly,
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(resumedRoot.Close)

		childSend := sendAndSettleDelegate(t, resumedRoot, childRes.DelegateID, "resume child and grandchild")
		if childSend.result.Err != nil {
			t.Fatalf("resume child: %v", childSend.result.Err)
		}
		awaitTreeQuiesced(t, resumedRoot)
		mu.Lock()
		gotTools := grandchildResumedTools
		mu.Unlock()
		if len(gotTools) == 0 {
			t.Fatal("grandchild never ran on persistent resume")
		}
		for _, want := range []string{"notes_agent_set", "notes_read"} {
			if !gotTools[want] {
				t.Errorf("resumed persistent default grandchild omitted %q", want)
			}
		}
	})
}

func TestOneShotDelegatePersistentResumePreservesRestrictions(t *testing.T) {
	t.Parallel()

	resumeRestoredChildTools := func(t *testing.T, configureRoot func(*SessionConfig), spawn func(*Session) (delegateID, childID string)) map[string]bool {
		t.Helper()
		workspace := t.TempDir()
		stateDir := realTempDirForTest(t)
		agentsDoc := filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
		testOnly := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}

		client := llm.NewClient()
		var mu sync.Mutex
		toolsBySession := map[string]map[string]bool{}
		client.Register(newFakeAdapter("openai", repeatFakeStep(16, func(req llm.Request) llm.Response {
			mu.Lock()
			toolsBySession[req.SessionID] = toolNameSetFromDefinitions(req.Tools)
			mu.Unlock()
			return agenttest.FinalResponse("done")
		})...))
		profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
		cfg := SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 3,
			ForceRealIO:      true,
			AgentsDocPath:    agentsDoc,
			TurnEndsProcess:  true,
			testOnly:         testOnly,
		}
		if configureRoot != nil {
			configureRoot(&cfg)
		}
		root, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		delegateID, childID := spawn(root)
		meta := root.Meta()
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		root.Close()

		resumedRoot, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
			StateDir:        stateDir,
			ForceRealIO:     true,
			AgentsDocPath:   agentsDoc,
			TurnEndsProcess: false,
			testOnly:        testOnly,
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(resumedRoot.Close)
		mu.Lock()
		delete(toolsBySession, childID)
		mu.Unlock()

		outcome := sendAndSettleDelegate(t, resumedRoot, delegateID, "continue after resume")
		if outcome.result.Err != nil {
			t.Fatalf("delegate_send: %v", outcome.result.Err)
		}
		mu.Lock()
		defer mu.Unlock()
		return toolsBySession[childID]
	}

	for _, role := range []string{"explorer", "subagent"} {
		t.Run("role "+role+" keeps notes tools excluded", func(t *testing.T) {
			t.Parallel()
			got := resumeRestoredChildTools(t, nil, func(root *Session) (string, string) {
				res := root.createDelegate(context.Background(), delegateArgs{
					Task:                "role task",
					AgentType:           role,
					DelegationAllowance: new(0),
				})
				if res.Err != nil {
					t.Fatalf("createDelegate(%s): %v", role, res.Err)
				}
				awaitTreeQuiesced(t, root)
				return res.DelegateID, res.ChildSessionID
			})
			for _, forbidden := range []string{"notes_agent_set", "notes_read"} {
				if got[forbidden] {
					t.Errorf("%s role unexpectedly gained %q on persistent resume", role, forbidden)
				}
			}
		})
	}

	t.Run("parent deniedToolNames preserves explicit notes_agent_set exclusion", func(t *testing.T) {
		t.Parallel()
		got := resumeRestoredChildTools(t, func(cfg *SessionConfig) {
			cfg.spawn.deniedToolNames = []string{"notes_agent_set"}
		}, func(root *Session) (string, string) {
			res := root.createDelegate(context.Background(), delegateArgs{
				Task:                "parent denied notes_agent_set",
				AgentType:           "default",
				DelegationAllowance: new(0),
			})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			awaitTreeQuiesced(t, root)
			return res.DelegateID, res.ChildSessionID
		})
		if got["notes_agent_set"] {
			t.Error("default delegate gained parent-denied notes_agent_set on persistent resume")
		}
		if !got["notes_read"] {
			t.Error("default delegate failed to regain non-denied notes_read on persistent resume")
		}
	})

	t.Run("parent allowedToolNames preserves notes exclusion", func(t *testing.T) {
		t.Parallel()
		got := resumeRestoredChildTools(t, func(cfg *SessionConfig) {
			cfg.spawn.allowedToolNames = []string{"read_file", "delegate", "communicate"}
		}, func(root *Session) (string, string) {
			res := root.createDelegate(context.Background(), delegateArgs{
				Task:                "parent restricted allowlist",
				AgentType:           "default",
				DelegationAllowance: new(0),
			})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			awaitTreeQuiesced(t, root)
			return res.DelegateID, res.ChildSessionID
		})
		for _, forbidden := range []string{"notes_agent_set", "notes_read"} {
			if got[forbidden] {
				t.Errorf("default delegate under restricted allowedToolNames gained %q on persistent resume", forbidden)
			}
		}
	})

	t.Run("parent toolNameCeiling preserves notes exclusion", func(t *testing.T) {
		t.Parallel()
		got := resumeRestoredChildTools(t, func(cfg *SessionConfig) {
			cfg.spawn.toolNameCeiling = []string{"read_file", "delegate", "communicate"}
		}, func(root *Session) (string, string) {
			res := root.createDelegate(context.Background(), delegateArgs{
				Task:                "parent restricted ceiling",
				AgentType:           "default",
				DelegationAllowance: new(0),
			})
			if res.Err != nil {
				t.Fatalf("createDelegate: %v", res.Err)
			}
			awaitTreeQuiesced(t, root)
			return res.DelegateID, res.ChildSessionID
		})
		for _, forbidden := range []string{"notes_agent_set", "notes_read"} {
			if got[forbidden] {
				t.Errorf("default delegate under restricted toolNameCeiling gained %q on persistent resume", forbidden)
			}
		}
	})

	t.Run("subagent parent spawning default grandchild preserves parent tool restriction", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		stateDir := realTempDirForTest(t)
		agentsDoc := filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
		testOnly := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}

		client := llm.NewClient()
		var mu sync.Mutex
		toolsBySession := map[string]map[string]bool{}
		var grandchildDelegateID, grandchildSessionID string
		parentSpawned := false
		parentResumedGrandchild := false
		client.Register(newFakeAdapter("openai", repeatFakeStep(32, func(req llm.Request) llm.Response {
			mu.Lock()
			toolsBySession[req.SessionID] = toolNameSetFromDefinitions(req.Tools)
			gid := grandchildDelegateID
			resumed := parentResumedGrandchild
			mu.Unlock()
			if hasUserPromptSubstring(req.Messages, "restricted parent") || hasUserPromptSubstring(req.Messages, "resume parent and grandchild") {
				for _, m := range req.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.Name == "delegate" {
							var out struct {
								DelegateID     string `json:"delegate_id"`
								ChildSessionID string `json:"child_session_id"`
							}
							if text, _ := p.ToolResult.Content.(string); json.Unmarshal([]byte(text), &out) == nil && out.DelegateID != "" {
								mu.Lock()
								grandchildDelegateID = out.DelegateID
								grandchildSessionID = out.ChildSessionID
								gid = out.DelegateID
								mu.Unlock()
							}
						}
					}
				}
				mu.Lock()
				alreadySpawned := parentSpawned
				if !alreadySpawned {
					parentSpawned = true
				}
				mu.Unlock()
				if !alreadySpawned {
					args, _ := json.Marshal(map[string]any{
						"prompt":               "default grandchild",
						"agent_type":           "default",
						"delegation_allowance": 0,
						"intent":               "spawn default grandchild",
					})
					return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call-sub-nested-delegate", Name: "delegate", Arguments: args},
					}}}}
				}
				if gid != "" && !resumed && hasUserPromptSubstring(req.Messages, "resume parent and grandchild") {
					mu.Lock()
					parentResumedGrandchild = true
					mu.Unlock()
					args, _ := json.Marshal(map[string]any{
						"to":          gid,
						"message":     "continue grandchild in persistent mode",
						"max_wait_ms": 60_000,
						"intent":      "resume grandchild in persistent mode",
					})
					return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call-sub-nested-send", Name: "delegate_send", Arguments: args},
					}}}}
				}
			}
			return agenttest.FinalResponse("done")
		})...))
		profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
		root, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 3,
			ForceRealIO:      true,
			AgentsDocPath:    agentsDoc,
			TurnEndsProcess:  true,
			testOnly:         testOnly,
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		parentRes := root.createDelegate(context.Background(), delegateArgs{
			Task:                "restricted parent",
			AgentType:           "subagent",
			DelegationAllowance: new(1),
		})
		if parentRes.Err != nil {
			t.Fatalf("create subagent parent: %v", parentRes.Err)
		}
		awaitTreeQuiesced(t, root)
		mu.Lock()
		gID := grandchildDelegateID
		gsID := grandchildSessionID
		mu.Unlock()
		if gID == "" || gsID == "" {
			t.Fatal("subagent parent did not spawn default grandchild")
		}

		meta := root.Meta()
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		root.Close()

		resumedRoot, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
			StateDir:        stateDir,
			ForceRealIO:     true,
			AgentsDocPath:   agentsDoc,
			TurnEndsProcess: false,
			testOnly:        testOnly,
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(resumedRoot.Close)

		mu.Lock()
		delete(toolsBySession, gsID)
		mu.Unlock()
		if send := sendAndSettleDelegate(t, resumedRoot, parentRes.DelegateID, "resume parent and grandchild"); send.result.Err != nil {
			t.Fatalf("resume subagent parent: %v", send.result.Err)
		}
		awaitTreeQuiesced(t, resumedRoot)
		mu.Lock()
		got := toolsBySession[gsID]
		mu.Unlock()
		if len(got) == 0 {
			t.Fatal("default grandchild did not run on persistent resume")
		}
		for _, forbidden := range []string{"notes_agent_set", "notes_read"} {
			if got[forbidden] {
				t.Errorf("default grandchild of subagent parent unexpectedly gained %q on persistent resume", forbidden)
			}
		}
	})
}
