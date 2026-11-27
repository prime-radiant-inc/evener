package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

func countGuideNotifications(reqs []llm.Request, topic string) int {
	tag := guideNotificationOpenTag(topic)
	var seen int
	if len(reqs) == 0 {
		return 0
	}
	last := reqs[len(reqs)-1]
	for _, msg := range last.Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentText && strings.Contains(part.Text, tag) {
				seen++
			}
		}
	}
	return seen
}

func TestAutoDisclosedGuideSkills_OmittedFromTurn1Catalog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSkillMD(t, dir, "project-skill", "---\nname: project-skill\ndescription: Project skill\n---\nBody.\n")

	sess := newSession(t, withDir(dir), withConfig(SessionConfig{
		MemoryStateRoot: t.TempDir(),
		MemoryProjectID: "guide-catalog-test",
		AgentsDocPath:   filepath.Join(t.TempDir(), "no-AGENTS.md"),
	}))

	data, _ := sess.buildPromptData(sess.currentEnv())
	for _, hidden := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName, skill.UsingWorktreesSkillName} {
		if slices.ContainsFunc(data.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == hidden }) {
			t.Fatalf("Turn-1 data.Skills unexpectedly contains auto-disclosed guide %q", hidden)
		}
	}
	for _, kept := range []string{"doctoring-evener", skill.GardeningMemorySkillName, "project-skill"} {
		if !slices.ContainsFunc(data.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == kept }) {
			t.Fatalf("Turn-1 data.Skills missing expected skill %q", kept)
		}
	}

	prompt, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("renderSystemPrompt warning: %s", warning)
	}
	if !strings.Contains(prompt, "<skill-catalog>") {
		t.Fatal("expected <skill-catalog> when non-guide skills are present")
	}

	// If only the 3 auto-disclosed guides exist in the catalog, <skill-catalog>
	// must be omitted cleanly rather than rendered empty.
	onlyGuides := skill.Catalog{Entries: map[string]skill.Descriptor{}}
	for _, name := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName, skill.UsingWorktreesSkillName} {
		if d, ok := sess.skills.Entries[name]; ok {
			onlyGuides.Entries[name] = d
		}
	}
	sess.skills = onlyGuides
	emptyData, _ := sess.buildPromptData(sess.currentEnv())
	if len(emptyData.Skills) != 0 {
		t.Fatalf("expected 0 prompt skills when only auto-disclosed guides exist, got %d", len(emptyData.Skills))
	}
	emptyPrompt, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("renderSystemPrompt (only guides): %s", warning)
	}
	if strings.Contains(emptyPrompt, "<skill-catalog>") {
		t.Fatal("expected <skill-catalog> to be omitted when all catalog entries are auto-disclosed guides")
	}

	headless := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		TurnEndsProcess: true,
		AgentsDocPath:   filepath.Join(t.TempDir(), "no-AGENTS.md"),
		testOnly:        testConfig{skipGitSnapshot: true},
	}))
	headlessPrompt, _, _ := headless.renderSystemPrompt(headless.env)
	toolJSON, _ := json.Marshal(headless.ToolDefinitions())
	var removedCatalog int
	for _, name := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName, skill.UsingWorktreesSkillName} {
		if d, ok := headless.skills.Entries[name]; ok {
			removedCatalog += len("- " + d.CatalogName + ": " + d.Meta.Description + "\n")
		}
	}
	t.Logf("headless Turn-1 prompt=%d bytes toolDefs=%d bytes removedCatalog=%d bytes", len(headlessPrompt), len(toolJSON), removedCatalog)
}

func TestGuideDisclosures_BackgroundShellFiresOnceAndPreservesSystemPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	bgArgs := idleShellJSON(`,"mode":"background"`)
	adapter := newFakeAdapter("anthropic",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh2", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("done", "ok"))
		},
	)
	c.Register(adapter)

	sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(dir), withConfig(SessionConfig{
		StateDir:      t.TempDir(),
		AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
	}))
	defer sess.Close()

	if _, err := sess.ProcessInput(tripwireCtx(t), "run background jobs", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) < 3 {
		t.Fatalf("expected 3 requests, got %d", len(reqs))
	}
	if reqs[0].Messages[0].Text() != reqs[1].Messages[0].Text() || reqs[1].Messages[0].Text() != reqs[2].Messages[0].Text() {
		t.Fatal("system prompt mutated across rounds, breaking KV prefix cache")
	}
	if got := countGuideNotifications(reqs[:2], guideTopicRunningJobs); got != 1 {
		t.Fatalf("after first background shell: running-jobs disclosures = %d, want 1; sh1=%q", got, toolResultTextForCall(reqs[1], "sh1"))
	}
	if got := countGuideNotifications(reqs[:3], guideTopicRunningJobs); got != 1 {
		t.Fatalf("after second background shell: running-jobs disclosures = %d, want 1 (must not repeat)", got)
	}

	// Without job tools exposed, background shell must NOT inject running-jobs.
	cNoJobs := llm.NewClient()
	aNoJobs := newFakeAdapter("anthropic",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("done", "ok"))
		},
	)
	cNoJobs.Register(aNoJobs)
	noJobs := newSession(t, withClient(cNoJobs), withProfile(newAnthropicProfile("claude-test")), withDir(dir), withConfig(SessionConfig{
		StateDir:      t.TempDir(),
		AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
	}))
	defer noJobs.Close()
	for _, name := range []string{"job_status", "job_stop", "job_list", "job_watch", "delegate"} {
		noJobs.reg.Remove(name)
	}
	refreshModelFacingCaches(noJobs)
	if _, err := noJobs.ProcessInput(tripwireCtx(t), "run background shell without job tools", nil); err != nil {
		t.Fatalf("noJobs ProcessInput: %v", err)
	}
	if got := countGuideNotifications(aNoJobs.Requests(), guideTopicRunningJobs); got != 0 {
		t.Fatalf("session without job tools injected %d running-jobs disclosures, want 0", got)
	}
}

func TestGuideDisclosures_DelegateFiresOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childClient := llm.NewClient()
	childClient.Register(newFakeAdapter("openai", repeatFinalResponse(8, "child done")...))
	registerTestSessionNamer(childClient)

	delArgs := json.RawMessage(`{"prompt":"return ok","delegation_allowance":0}`)

	for _, tc := range []struct {
		name string
		call llm.ToolCallData
	}{
		{name: "direct", call: llm.ToolCallData{ID: "d1", Name: "delegate", Arguments: delArgs, Type: "function"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := llm.NewClient()
			call2 := tc.call
			call2.ID = "d2"
			adapter := newFakeAdapter("openai",
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(tc.call)
				},
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(call2)
				},
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(communicateCall("c1", "done"))
				},
			)
			c.Register(adapter)

			sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
				MaxSubagentDepth: 2,
				StateDir:         t.TempDir(),
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-AGENTS.md"),
				testOnly:         testConfig{childClientFactory: func() *llm.Client { return childClient }},
			}))
			defer sess.Close()

			if _, err := sess.ProcessInput(tripwireCtx(t), "start delegates", nil); err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			reqs := adapter.Requests()
			if len(reqs) < 3 {
				t.Fatalf("expected 3 requests, got %d", len(reqs))
			}
			if got := countGuideNotifications(reqs[:2], guideTopicRunningJobs); got != 1 {
				t.Fatalf("after first delegate (%s): running-jobs disclosures = %d, want 1", tc.name, got)
			}
			if got := countGuideNotifications(reqs[:3], guideTopicRunningJobs); got != 1 {
				t.Fatalf("after second delegate (%s): running-jobs disclosures = %d, want 1", tc.name, got)
			}
		})
	}
}

func TestGuideDisclosures_ArtifactTruncationAndCapabilityGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(strings.Repeat("line-of-text\n", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	readArgs, _ := json.Marshal(map[string]any{"file_path": bigPath})
	c := llm.NewClient()
	adapter := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "r1", Name: "read_file", Arguments: readArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "r2", Name: "read_file", Arguments: readArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "done"))
		},
	)
	c.Register(adapter)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
		artifactStore: store,
		AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"read_file": {MaxChars: 512, Strategy: schema.TruncHeadTail},
		},
	}))
	defer sess.Close()

	if _, err := sess.ProcessInput(tripwireCtx(t), "read big file", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) < 3 {
		t.Fatalf("expected 3 requests, got %d", len(reqs))
	}
	if got := countGuideNotifications(reqs[:2], guideTopicReadingTranscripts); got != 1 {
		t.Fatalf("after first truncated read_file: reading-transcripts disclosures = %d, want 1", got)
	}
	if got := countGuideNotifications(reqs[:3], guideTopicReadingTranscripts); got != 1 {
		t.Fatalf("after second truncated read_file: reading-transcripts disclosures = %d, want 1", got)
	}

	// Without read_transcript callable, truncation must NOT disclose reading-transcripts.
	c2 := llm.NewClient()
	adapter2 := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "r1", Name: "read_file", Arguments: readArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "done"))
		},
	)
	c2.Register(adapter2)
	gated := newSession(t, withClient(c2), withDir(dir), withConfig(SessionConfig{
		artifactStore: store,
		AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"read_file": {MaxChars: 512, Strategy: schema.TruncHeadTail},
		},
	}))
	defer gated.Close()
	gated.reg.Remove("read_transcript")
	refreshModelFacingCaches(gated)
	if _, err := gated.ProcessInput(tripwireCtx(t), "read big file", nil); err != nil {
		t.Fatalf("gated ProcessInput: %v", err)
	}
	if got := countGuideNotifications(adapter2.Requests(), guideTopicReadingTranscripts); got != 0 {
		t.Fatalf("session without read_transcript injected %d reading-transcripts disclosures, want 0", got)
	}
}

func TestGuideDisclosures_PromotedShellTriggersBothOnce(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	tru := true
	promotedState, _ := json.Marshal(shellToolResult{
		JobID:     "job_promoted_1",
		Type:      "shell",
		Status:    "running",
		Mode:      "background",
		TimedOut:  true,
		Truncated: &tru,
	})
	call := llm.ToolCallData{ID: "sh1", Name: "shell"}
	res := tool.ExecResult{CallID: "sh1", ToolName: "shell", ToolState: promotedState}
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{res}); err != nil {
		t.Fatalf("first persistToolResults: %v", err)
	}
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{res}); err != nil {
		t.Fatalf("second persistToolResults: %v", err)
	}

	var jobsCount, transcriptsCount int
	sess.mu.Lock()
	for _, turn := range sess.history {
		if turn.Kind != schema.TurnSystem {
			continue
		}
		text := turn.Message.Text()
		if strings.Contains(text, guideNotificationOpenTag(guideTopicRunningJobs)) {
			jobsCount++
		}
		if strings.Contains(text, guideNotificationOpenTag(guideTopicReadingTranscripts)) {
			transcriptsCount++
		}
	}
	sess.mu.Unlock()
	if jobsCount != 1 || transcriptsCount != 1 {
		t.Fatalf("promoted shell counts: running-jobs=%d reading-transcripts=%d, want 1 and 1", jobsCount, transcriptsCount)
	}
}

func TestGuideDisclosures_WorktreeFiresOnceAndNeverWithoutTool(t *testing.T) {
	t.Parallel()
	listArgs := json.RawMessage(`{"operation":"list"}`)

	for _, tc := range []struct {
		name string
		call llm.ToolCallData
	}{
		{name: "direct", call: llm.ToolCallData{ID: "w1", Name: "manage_worktree", Arguments: listArgs, Type: "function"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			initGitRepo(t, repo)

			c := llm.NewClient()
			call2 := tc.call
			call2.ID = "w2"
			adapter := newFakeAdapter("openai",
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(tc.call)
				},
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(call2)
				},
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(communicateCall("c1", "done"))
				},
			)
			c.Register(adapter)

			sess := newSession(t, withClient(c), withDir(repo), withConfig(SessionConfig{
				StateDir:      t.TempDir(),
				AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
			}))
			defer sess.Close()

			if _, err := sess.ProcessInput(tripwireCtx(t), "list worktrees", nil); err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			reqs := adapter.Requests()
			if len(reqs) < 3 {
				t.Fatalf("expected 3 requests, got %d", len(reqs))
			}
			if got := countGuideNotifications(reqs[:2], guideTopicUsingWorktrees); got != 1 {
				t.Fatalf("after first manage_worktree (%s): using-worktrees disclosures = %d, want 1", tc.name, got)
			}
			if got := countGuideNotifications(reqs[:3], guideTopicUsingWorktrees); got != 1 {
				t.Fatalf("after second manage_worktree (%s): using-worktrees disclosures = %d, want 1", tc.name, got)
			}
		})
	}
}

func TestGuideDisclosures_RestoreAndCompactionSync(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	dir := t.TempDir()
	bgArgs := idleShellJSON(`,"mode":"background"`)

	c1 := llm.NewClient()
	c1.Register(newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "done"))
		},
	))
	sess := newSession(t, withClient(c1), withDir(dir), withConfig(SessionConfig{
		StateDir:      stateDir,
		AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
		testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	if _, err := sess.ProcessInput(tripwireCtx(t), "run one", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	id := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c2 := llm.NewClient()
	a2 := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh2", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c2", "done"))
		},
	)
	c2.Register(a2)
	resumed, err := RestoreSessionFromMetaWithConfig(c2, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer resumed.Close()

	if !resumed.disclosedRunningJobs {
		t.Fatal("resumed session did not preserve disclosedRunningJobs from history")
	}
	if _, err := resumed.ProcessInput(tripwireCtx(t), "run two", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	if got := countGuideNotifications(a2.Requests(), guideTopicRunningJobs); got != 1 {
		t.Fatalf("resumed session running-jobs disclosures = %d, want 1 (must not re-emit)", got)
	}

	// Compaction that folds away the TurnSystem disclosure resets the flag.
	resumed.mu.Lock()
	resumed.syncDisclosedGuidesFromHistoryLocked(nil)
	stillDisclosed := resumed.disclosedRunningJobs
	resumed.mu.Unlock()
	if stillDisclosed {
		t.Fatal("expected disclosedRunningJobs to reset when disclosure is absent from post-fold history")
	}
}
