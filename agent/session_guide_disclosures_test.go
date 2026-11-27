package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/envctx"
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

	// A model-invocable project or user override matching a bundled guide name
	// wins discovery and must remain advertised in both the launch catalog and
	// envRuntimeSnapshot while preserving use_skill resolution.
	t.Run("project_and_user_overrides_remain_advertised", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		initGitRepo(t, repo)
		writeSkillMD(t, repo, skill.UsingWorktreesSkillName, "---\nname: using-worktrees\ndescription: Project custom worktree workflow\n---\nProject-worktree-override-body.\n")
		bigPath := filepath.Join(repo, "big.txt")
		if err := os.WriteFile(bigPath, []byte(strings.Repeat("line-of-text\n", 400)), 0o600); err != nil {
			t.Fatal(err)
		}
		store, err := artifactstore.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		userSkills := t.TempDir()
		if err := os.MkdirAll(filepath.Join(userSkills, skill.RunningJobsSkillName), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userSkills, skill.RunningJobsSkillName, "SKILL.md"),
			[]byte("---\nname: running-jobs\ndescription: User custom jobs workflow\n---\nUser-jobs-override-body.\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		readArgs, _ := json.Marshal(map[string]any{"file_path": bigPath})
		bgArgs := idleShellJSON(`,"mode":"background"`)
		c := llm.NewClient()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(
					llm.ToolCallData{ID: "w1", Name: "manage_worktree", Arguments: json.RawMessage(`{"operation":"list"}`), Type: "function"},
					llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"},
					llm.ToolCallData{ID: "r1", Name: "read_file", Arguments: readArgs, Type: "function"},
				)
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("done", "ok"))
			},
		)
		c.Register(adapter)

		overridden := newSession(t, withClient(c), withDir(repo), withConfig(SessionConfig{
			StateDir:      t.TempDir(),
			SkillsDirs:    []string{userSkills},
			artifactStore: store,
			AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
			ToolOutputLimits: map[string]schema.ToolOutputLimit{
				"read_file": {MaxChars: 512, Strategy: schema.TruncHeadTail},
			},
		}))
		defer overridden.Close()

		ovData, _ := overridden.buildPromptData(overridden.currentEnv())
		for _, want := range []string{skill.UsingWorktreesSkillName, skill.RunningJobsSkillName} {
			if !slices.ContainsFunc(ovData.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == want }) {
				t.Fatalf("overridden guide %q missing from Turn-1 data.Skills", want)
			}
		}
		if slices.ContainsFunc(ovData.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == skill.ReadingTranscriptsSkillName }) {
			t.Fatalf("unoverridden bundled guide %q unexpectedly in Turn-1 data.Skills", skill.ReadingTranscriptsSkillName)
		}

		overridden.mu.Lock()
		snap := overridden.envRuntimeSnapshot(envctx.Snapshot{})
		overridden.mu.Unlock()
		for _, want := range []string{skill.UsingWorktreesSkillName, skill.RunningJobsSkillName} {
			if !strings.Contains(snap.AvailableSkills, want) {
				t.Fatalf("overridden guide %q missing from envRuntimeSnapshot.AvailableSkills: %q", want, snap.AvailableSkills)
			}
		}
		if strings.Contains(snap.AvailableSkills, skill.ReadingTranscriptsSkillName) {
			t.Fatalf("unoverridden bundled guide %q unexpectedly in envRuntimeSnapshot.AvailableSkills: %q", skill.ReadingTranscriptsSkillName, snap.AvailableSkills)
		}

		res := overridden.reg.ExecuteCall(context.Background(), overridden.env, llm.ToolCallData{
			ID:        "load-override",
			Name:      "use_skill",
			Arguments: json.RawMessage(`{"skill_name":"using-worktrees"}`),
		})
		if res.IsError || !strings.Contains(res.Output, "Project-worktree-override-body.") {
			t.Fatalf("use_skill(using-worktrees) = (err=%v, %q), want project override body", res.IsError, res.Output)
		}
		if res := overridden.reg.ExecuteCall(context.Background(), overridden.env, llm.ToolCallData{
			ID:        "load-user-override",
			Name:      "use_skill",
			Arguments: json.RawMessage(`{"skill_name":"running-jobs"}`),
		}); res.IsError || !strings.Contains(res.Output, "User-jobs-override-body.") {
			t.Fatalf("use_skill(running-jobs) = (err=%v, %q), want user override body", res.IsError, res.Output)
		}

		if _, err := overridden.ProcessInput(tripwireCtx(t), "list worktrees, start background shell, and read big file", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		reqs := adapter.Requests()
		for _, topic := range []string{guideTopicUsingWorktrees, guideTopicRunningJobs} {
			if got := countGuideNotifications(reqs, topic); got != 0 {
				t.Fatalf("overridden guide %q emitted %d bundled disclosures, want 0", topic, got)
			}
		}
		if got := countGuideNotifications(reqs, guideTopicReadingTranscripts); got != 1 {
			t.Fatalf("unoverridden bundled guide %q emitted %d disclosures, want 1", guideTopicReadingTranscripts, got)
		}
	})
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

	// One-shot running-jobs disclosure must give a schema-valid job_watch recipe
	// including required operation="create".
	t.Run("one_shot_job_watch_recipe_includes_operation_create", func(t *testing.T) {
		t.Parallel()
		cOne := llm.NewClient()
		aOne := newFakeAdapter("anthropic",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh-one", Name: "shell", Arguments: bgArgs, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("done", "ok"))
			},
		)
		cOne.Register(aOne)
		oneShot := newSession(t, withClient(cOne), withProfile(newAnthropicProfile("claude-test")), withDir(dir), withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			TurnEndsProcess: true,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-AGENTS.md"),
		}))
		defer oneShot.Close()
		if _, err := oneShot.ProcessInput(tripwireCtx(t), "run one-shot background shell", nil); err != nil {
			t.Fatalf("oneShot ProcessInput: %v", err)
		}
		reqs := aOne.Requests()
		if len(reqs) < 2 {
			t.Fatalf("expected at least 2 requests, got %d", len(reqs))
		}
		var disclosure string
		for _, msg := range reqs[1].Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentText && strings.Contains(part.Text, guideNotificationOpenTag(guideTopicRunningJobs)) {
					disclosure = part.Text
				}
			}
		}
		for _, token := range []string{`job_watch(operation="create"`, `source=`, `progress_interval_ms=180000`, `output_match`} {
			if !strings.Contains(disclosure, token) {
				t.Fatalf("one-shot running-jobs disclosure missing required API token %q: %q", token, disclosure)
			}
		}
		// Confirm the documented recipe validates and succeeds against job_watch.
		jobs := oneShot.jobManager.list(listFilter{})
		if len(jobs) == 0 {
			t.Fatal("expected running background shell job in one-shot session")
		}
		watchArgs, _ := json.Marshal(map[string]any{
			"operation":            "create",
			"source":               jobs[0].JobID,
			"progress_interval_ms": 180000,
		})
		if watchRes := oneShot.reg.ExecuteCall(context.Background(), oneShot.env, llm.ToolCallData{
			ID: "w-recipe", Name: "job_watch", Arguments: watchArgs,
		}); watchRes.IsError {
			t.Fatalf("documented job_watch recipe failed validation/execution: %s", watchRes.Output)
		}
	})
}

func TestGuideDisclosures_DelegateFiresOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childClientFactory := func() *llm.Client {
		cc := llm.NewClient()
		cc.Register(&blockingAdapter{name: "openai", blocked: make(chan struct{})})
		registerTestSessionNamer(cc)
		return cc
	}

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
				testOnly:         testConfig{childClientFactory: childClientFactory},
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
	a1 := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "done"))
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh-post-fold", Name: "shell", Arguments: bgArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c-post-fold", "done"))
		},
	)
	c1.Register(a1)
	registerTestSessionNamer(c1)
	sess := newSession(t, withClient(c1), withDir(dir), withConfig(SessionConfig{
		StateDir:      stateDir,
		AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
		testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	if _, err := sess.ProcessInput(tripwireCtx(t), "run one", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}

	// Mark the other two guide disclosures as already emitted too so we verify
	// all three stay set across an actual compaction fold and compacted restore.
	for _, topic := range []string{guideTopicReadingTranscripts, guideTopicUsingWorktrees} {
		m := llm.UserMachinery(wrapGuideNotification(topic, "disclosed"))
		sess.appendTurnWithTranscriptMessage(schema.TurnSystem, m, m)
	}
	sess.mu.Lock()
	sess.disclosedReadingTranscripts = true
	sess.disclosedUsingWorktrees = true
	sess.mu.Unlock()

	// Add enough turns so an actual compaction fold removes the earlier
	// TurnSystem running-jobs disclosure from retained model history.
	for i := range 20 {
		u := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("filler-user-%d", i)))
		sess.recordTurn(u, u)
		a := schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("filler-assistant-%d %s", i, strings.Repeat("x", 200))))
		sess.recordTurn(a, a)
	}
	if err := sess.Compact(tripwireCtx(t)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	sess.mu.Lock()
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSystem && strings.Contains(turn.Message.Text(), guideNotificationOpenTag(guideTopicRunningJobs)) {
			sess.mu.Unlock()
			t.Fatal("fixture setup error: running-jobs disclosure remained in post-fold history")
		}
	}
	if !sess.disclosedRunningJobs || !sess.disclosedReadingTranscripts || !sess.disclosedUsingWorktrees {
		sess.mu.Unlock()
		t.Fatalf("compaction fold cleared sticky guide flags: running=%v transcripts=%v worktrees=%v",
			sess.disclosedRunningJobs, sess.disclosedReadingTranscripts, sess.disclosedUsingWorktrees)
	}
	sess.mu.Unlock()

	// A later qualifying round after compaction must NOT emit a duplicate guide.
	if _, err := sess.ProcessInput(tripwireCtx(t), "run after fold", nil); err != nil {
		t.Fatalf("post-fold ProcessInput: %v", err)
	}
	if got := countGuideNotifications(a1.Requests(), guideTopicRunningJobs); got != 0 {
		t.Fatalf("post-fold session emitted %d duplicate running-jobs disclosures, want 0", got)
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

	if !resumed.disclosedRunningJobs || !resumed.disclosedReadingTranscripts || !resumed.disclosedUsingWorktrees {
		t.Fatalf("resumed compacted session did not recover all sticky guide flags from pre-compaction transcript: running=%v transcripts=%v worktrees=%v",
			resumed.disclosedRunningJobs, resumed.disclosedReadingTranscripts, resumed.disclosedUsingWorktrees)
	}
	if _, err := resumed.ProcessInput(tripwireCtx(t), "run two", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	if got := countGuideNotifications(a2.Requests(), guideTopicRunningJobs); got != 0 {
		t.Fatalf("resumed compacted session emitted %d duplicate running-jobs disclosures, want 0", got)
	}
}

func TestGuideDisclosures_UpgradedRestoreAvoidsBogusRemovedSkillsDelta(t *testing.T) {
	t.Parallel()

	setupPreUpgrade := func(t *testing.T) (stateDir, repo string, meta schema.SessionMeta) {
		t.Helper()
		stateDir = t.TempDir()
		repo = t.TempDir()
		initGitRepo(t, repo)
		writeSkillMD(t, repo, "kept-skill", "---\nname: kept-skill\ndescription: Kept project skill\n---\nKept.\n")
		writeSkillMD(t, repo, "deleted-skill", "---\nname: deleted-skill\ndescription: Deleted project skill\n---\nDeleted.\n")

		c1 := llm.NewClient()
		c1.Register(newFakeAdapter("openai", func(llm.Request) llm.Response {
			return agenttest.FinalResponse("ok")
		}))
		sess := newSession(t, withClient(c1), withDir(repo), withConfig(SessionConfig{
			StateDir:      stateDir,
			AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
			testOnly:      testConfig{noSyncJobStore: true},
		}))
		if _, err := sess.ProcessInput(tripwireCtx(t), "hi", nil); err != nil {
			t.Fatalf("initial ProcessInput: %v", err)
		}
		id := sess.ID()
		sess.Close()

		var err error
		meta, err = schema.LoadSessionMeta(stateDir, id)
		if err != nil {
			t.Fatalf("LoadSessionMeta: %v", err)
		}
		meta.LaunchEnvBaseline.AvailableSkills = envctx.JoinNames(append(
			strings.Split(meta.LaunchEnvBaseline.AvailableSkills, ", "),
			skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName, skill.UsingWorktreesSkillName,
		))
		return stateDir, repo, meta
	}

	t.Run("still_loadable_bundled_guides_produce_no_removed_delta", func(t *testing.T) {
		t.Parallel()
		stateDir, repo, meta := setupPreUpgrade(t)
		c2 := llm.NewClient()
		a2 := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return agenttest.FinalResponse("ok")
		}, func(llm.Request) llm.Response {
			return agenttest.FinalResponse("ok2")
		})
		c2.Register(a2)
		registerTestSessionNamer(c2)
		resumed, err := RestoreSessionFromMetaWithConfig(c2, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(repo), meta, RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		defer resumed.Close()

		if _, err := resumed.ProcessInput(tripwireCtx(t), "continue", nil); err != nil {
			t.Fatalf("resumed ProcessInput: %v", err)
		}
		reqs := a2.Requests()
		last := reqs[len(reqs)-1]
		for _, msg := range last.Messages {
			text := msg.Text()
			for _, guide := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName, skill.UsingWorktreesSkillName} {
				if strings.Contains(text, "removed skills: "+guide) {
					t.Fatalf("upgraded resume emitted bogus removed-skill delta for still-loadable %q:\n%s", guide, text)
				}
			}
		}
		// Bundled guides remain callable through use_skill.
		loadRes := resumed.reg.ExecuteCall(context.Background(), resumed.env, llm.ToolCallData{
			ID:        "load-guide",
			Name:      "use_skill",
			Arguments: json.RawMessage(`{"skill_name":"running-jobs"}`),
		})
		if loadRes.IsError {
			t.Fatalf("use_skill(running-jobs) failed on upgraded resumed session: %s", loadRes.Output)
		}
		// Compaction resets envTracker from LaunchEnvBaseline; verify it still
		// does not emit bogus removed-skill deltas for bundled guides afterward.
		for i := range 20 {
			u := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("compact-u-%d", i)))
			resumed.recordTurn(u, u)
			a := schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("compact-a-%d %s", i, strings.Repeat("x", 200))))
			resumed.recordTurn(a, a)
		}
		if err := resumed.Compact(tripwireCtx(t)); err != nil {
			t.Fatalf("Compact on upgraded resumed session: %v", err)
		}
		if _, err := resumed.ProcessInput(tripwireCtx(t), "after compact", nil); err != nil {
			t.Fatalf("post-compact ProcessInput: %v", err)
		}
		reqs2 := a2.Requests()
		for _, msg := range reqs2[len(reqs2)-1].Messages {
			text := msg.Text()
			for _, guide := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName, skill.UsingWorktreesSkillName} {
				if strings.Contains(text, "removed skills: "+guide) {
					t.Fatalf("post-compaction upgraded session emitted bogus removed-skill delta for %q:\n%s", guide, text)
				}
			}
		}
	})

	t.Run("genuine_skill_loss_still_reports_removed_delta", func(t *testing.T) {
		t.Parallel()
		stateDir, _, meta := setupPreUpgrade(t)
		// Resume into a checkout without deleted-skill and with a dispose-only
		// worktree surface so using-worktrees is genuinely unavailable while
		// running-jobs and reading-transcripts stay loadable.
		reducedDir := t.TempDir()
		writeSkillMD(t, reducedDir, "kept-skill", "---\nname: kept-skill\ndescription: Kept project skill\n---\nKept.\n")
		c3 := llm.NewClient()
		a3 := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return agenttest.FinalResponse("ok")
		})
		c3.Register(a3)
		resumed, err := RestoreSessionFromMetaWithConfig(c3, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(reducedDir), meta, RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{noSyncJobStore: true},
			spawn: spawnConfig{
				parentSessionID:     "parent-session",
				depth:               1,
				delegationAllowance: 1,
				isolation:           "worktree",
			},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		defer resumed.Close()

		if _, err := resumed.ProcessInput(tripwireCtx(t), "continue", nil); err != nil {
			t.Fatalf("resumed ProcessInput: %v", err)
		}
		reqs := a3.Requests()
		var joined strings.Builder
		for _, msg := range reqs[len(reqs)-1].Messages {
			joined.WriteString(msg.Text())
			joined.WriteByte('\n')
		}
		got := joined.String()
		for _, wantRemoved := range []string{"removed skills: deleted-skill", "removed skills: " + skill.UsingWorktreesSkillName} {
			if !strings.Contains(got, wantRemoved) {
				t.Fatalf("expected genuine removal %q in resumed environment context, got:\n%s", wantRemoved, got)
			}
		}
		for _, stillLoadable := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName} {
			if strings.Contains(got, "removed skills: "+stillLoadable) {
				t.Fatalf("emitted bogus removed-skill delta for still-loadable %q:\n%s", stillLoadable, got)
			}
		}
	})
}
