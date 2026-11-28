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
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

func countSkillContextEnvelopesInText(t *testing.T, content, skillName string) int {
	t.Helper()
	const open, closeTag = "<skill-context>\n", "\n</skill-context>"
	rest := content
	count := 0
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return count
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, closeTag)
		if j < 0 {
			return count
		}
		var doc skill.SkillDocument
		if err := json.Unmarshal([]byte(rest[:j]), &doc); err == nil && doc.Name == skillName {
			if doc.Source == "" || strings.TrimSpace(doc.Instructions) == "" {
				t.Fatalf("skill-context envelope for %q missing source or instructions: %+v", skillName, doc)
			}
			count++
		}
		rest = rest[j+len(closeTag):]
	}
}

func countSkillContextEnvelopes(t *testing.T, req llm.Request, skillName string) int {
	t.Helper()
	count := 0
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			switch part.Kind {
			case llm.ContentText:
				count += countSkillContextEnvelopesInText(t, part.Text, skillName)
			case llm.ContentToolResult:
				if part.ToolResult != nil {
					if text, ok := part.ToolResult.Content.(string); ok {
						count += countSkillContextEnvelopesInText(t, text, skillName)
					}
				}
			}
		}
	}
	return count
}

func requireSkillInventoryOrdinary(t *testing.T, s *Session, skillName, wantRoute string) schema.OrdinarySkillActivation {
	t.Helper()
	s.mu.Lock()
	entry := s.skillLifecycle.Inventory[skillName]
	obligations := append([]schema.SkillDeliveryObligation(nil), s.skillLifecycle.Obligations...)
	s.mu.Unlock()
	if entry.Ordinary == nil {
		t.Fatalf("skillLifecycle.Inventory[%q].Ordinary = nil, want active record (obligations=%+v)", skillName, obligations)
	}
	if entry.Ordinary.Route != wantRoute {
		t.Fatalf("skillLifecycle.Inventory[%q].Ordinary.Route = %q, want %q", skillName, entry.Ordinary.Route, wantRoute)
	}
	if entry.Ordinary.UserAuthorized {
		t.Fatalf("skillLifecycle.Inventory[%q].Ordinary.UserAuthorized = true, want false", skillName)
	}
	if entry.Ordinary.Identity.Name != skillName || entry.Ordinary.Identity.RenderedDigest == "" || entry.Ordinary.Identity.Source == "" {
		t.Fatalf("skillLifecycle.Inventory[%q].Ordinary.Identity incomplete: %+v", skillName, entry.Ordinary.Identity)
	}
	return *entry.Ordinary
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

	// A model-invocable project or user override matching a bundled guide name
	// wins discovery and must remain advertised in both the launch catalog and
	// envRuntimeSnapshot without triggering bundled auto-disclosure.
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
		last := reqs[len(reqs)-1]
		for _, overriddenName := range []string{skill.UsingWorktreesSkillName, skill.RunningJobsSkillName} {
			if got := countSkillContextEnvelopes(t, last, overriddenName); got != 0 {
				t.Fatalf("overridden guide %q auto-disclosed %d times, want 0", overriddenName, got)
			}
		}
		if got := countSkillContextEnvelopes(t, last, skill.ReadingTranscriptsSkillName); got != 1 {
			t.Fatalf("unoverridden bundled guide %q auto-disclosed %d times, want 1", skill.ReadingTranscriptsSkillName, got)
		}
		requireSkillInventoryOrdinary(t, overridden, skill.ReadingTranscriptsSkillName, "auto_disclosure")
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
	if got := countSkillContextEnvelopes(t, reqs[1], skill.RunningJobsSkillName); got != 1 {
		t.Fatalf("after first background shell: running-jobs envelopes = %d, want 1; sh1=%q", got, toolResultTextForCall(reqs[1], "sh1"))
	}
	if got := countSkillContextEnvelopes(t, reqs[2], skill.RunningJobsSkillName); got != 1 {
		t.Fatalf("after second background shell: running-jobs envelopes = %d, want 1 (must not repeat)", got)
	}
	requireSkillInventoryOrdinary(t, sess, skill.RunningJobsSkillName, "auto_disclosure")
	sess.mu.Lock()
	remainingObligations := len(sess.skillLifecycle.Obligations)
	sess.mu.Unlock()
	if remainingObligations != 0 {
		t.Fatalf("pending obligations after delivery = %d, want 0", remainingObligations)
	}

	// Without job tools exposed, background shell must NOT activate running-jobs.
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
	reqsNoJobs := aNoJobs.Requests()
	if got := countSkillContextEnvelopes(t, reqsNoJobs[len(reqsNoJobs)-1], skill.RunningJobsSkillName); got != 0 {
		t.Fatalf("session without job tools injected %d running-jobs envelopes, want 0", got)
	}
	if _, ok := noJobs.skillInventorySnapshot()[skill.RunningJobsSkillName]; ok {
		t.Fatal("session without job tools unexpectedly recorded running-jobs in inventory")
	}

	// Ephemeral one-shot sessions without StateDir (no transcript writer or
	// meta.json on disk) must activate running-jobs cleanly through the native
	// lifecycle without persistence errors.
	t.Run("one_shot_ephemeral_without_state_dir", func(t *testing.T) {
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
		if got := countSkillContextEnvelopes(t, reqs[1], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("one-shot ephemeral session running-jobs envelopes = %d, want 1", got)
		}
		requireSkillInventoryOrdinary(t, oneShot, skill.RunningJobsSkillName, "auto_disclosure")

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
			t.Fatalf("job_watch create recipe failed: %s", watchRes.Output)
		}
	})

	// Explicit use_skill and a triggering background shell in the same tool
	// round must not double-inject the skill carrier.
	t.Run("same_round_use_skill_deduplicates", func(t *testing.T) {
		t.Parallel()
		cDup := llm.NewClient()
		aDup := newFakeAdapter("anthropic",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(
					useSkillCall("sk1", skill.RunningJobsSkillName),
					llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"},
				)
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("done", "ok"))
			},
		)
		cDup.Register(aDup)
		sDup := newSession(t, withClient(cDup), withProfile(newAnthropicProfile("claude-test")), withDir(dir), withConfig(SessionConfig{
			StateDir:      t.TempDir(),
			AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
		}))
		defer sDup.Close()
		if _, err := sDup.ProcessInput(tripwireCtx(t), "load skill and run background shell together", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		reqs := aDup.Requests()
		if len(reqs) < 2 {
			t.Fatalf("expected at least 2 requests, got %d", len(reqs))
		}
		if got := countSkillContextEnvelopes(t, reqs[1], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("same-round use_skill + background shell produced %d running-jobs envelopes, want 1", got)
		}
		requireSkillInventoryOrdinary(t, sDup, skill.RunningJobsSkillName, "model_tool")
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
	c := llm.NewClient()
	adapter := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "d1", Name: "delegate", Arguments: delArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "d2", Name: "delegate", Arguments: delArgs, Type: "function"})
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
	if got := countSkillContextEnvelopes(t, reqs[1], skill.RunningJobsSkillName); got != 1 {
		t.Fatalf("after first delegate: running-jobs envelopes = %d, want 1", got)
	}
	if got := countSkillContextEnvelopes(t, reqs[2], skill.RunningJobsSkillName); got != 1 {
		t.Fatalf("after second delegate: running-jobs envelopes = %d, want 1", got)
	}
	requireSkillInventoryOrdinary(t, sess, skill.RunningJobsSkillName, "auto_disclosure")
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
	if got := countSkillContextEnvelopes(t, reqs[1], skill.ReadingTranscriptsSkillName); got != 1 {
		t.Fatalf("after first truncated read_file: reading-transcripts envelopes = %d, want 1", got)
	}
	if got := countSkillContextEnvelopes(t, reqs[2], skill.ReadingTranscriptsSkillName); got != 1 {
		t.Fatalf("after second truncated read_file: reading-transcripts envelopes = %d, want 1", got)
	}
	requireSkillInventoryOrdinary(t, sess, skill.ReadingTranscriptsSkillName, "auto_disclosure")

	// Without read_transcript callable, truncation must NOT activate reading-transcripts.
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
	reqs2 := adapter2.Requests()
	if got := countSkillContextEnvelopes(t, reqs2[len(reqs2)-1], skill.ReadingTranscriptsSkillName); got != 0 {
		t.Fatalf("session without read_transcript injected %d reading-transcripts envelopes, want 0", got)
	}
}

func TestGuideDisclosures_PromotedShellTriggersBothOnce(t *testing.T) {
	t.Parallel()
	promotedArgs := json.RawMessage(`{"command":"printf '%02048d\\n' 1; ` + idleShellCommand + `"}`)
	shellTurn := func(id string) func(llm.Request) llm.Response {
		return func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: id, Name: "shell", Arguments: promotedArgs, Type: "function"})
		}
	}
	c := llm.NewClient()
	adapter := newFakeAdapter("openai",
		shellTurn("sh1"),
		shellTurn("sh2"),
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "done"))
		},
	)
	c.Register(adapter)

	sess := newSession(t, withClient(c), withDir(t.TempDir()), withConfig(SessionConfig{
		StateDir:                t.TempDir(),
		DefaultCommandTimeoutMS: 100,
		MaxCommandTimeoutMS:     100,
		AgentsDocPath:           filepath.Join(t.TempDir(), "no-AGENTS.md"),
	}))
	defer sess.Close()

	clk := agenttest.NewFakeClock()
	sess.jobManager.clock = clk
	seen := map[string]bool{}
	var promotedIDs []string
	defer func() {
		for _, id := range promotedIDs {
			_, _ = sess.jobManager.stop(id)
			waitForShellDone(t, sess.jobManager, id)
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(tripwireCtx(t), "run two noisy foreground commands", nil)
		errCh <- err
	}()

	promoteNext := func(label string) string {
		clk.BlockUntil(1)
		var jobID string
		testwait.Until(t, label, func() bool {
			sess.jobManager.mu.Lock()
			defer sess.jobManager.mu.Unlock()
			for id, run := range sess.jobManager.running {
				if seen[id] || run == nil || run.rec == nil || run.rec.Background {
					continue
				}
				if run.output.Len() > shellDefaultTailBytes {
					jobID = id
					return true
				}
			}
			return false
		})
		seen[jobID] = true
		promotedIDs = append(promotedIDs, jobID)
		clk.Advance(time.Second)
		return jobID
	}

	job1 := promoteNext("first promoted shell output exceeds peek tail")
	job2 := promoteNext("second promoted shell output exceeds peek tail")
	if err := <-errCh; err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) < 3 {
		t.Fatalf("expected 3 requests, got %d", len(reqs))
	}
	for _, check := range []struct {
		round  int
		callID string
		jobID  string
	}{{1, "sh1", job1}, {2, "sh2", job2}} {
		if got := toolResultTextForCall(reqs[check.round], check.callID); !strings.Contains(got, check.jobID) {
			t.Fatalf("round %d shell result missing promoted job_id %q: %q", check.round, check.jobID, got)
		}
		for _, name := range []string{skill.RunningJobsSkillName, skill.ReadingTranscriptsSkillName} {
			if got := countSkillContextEnvelopes(t, reqs[check.round], name); got != 1 {
				t.Fatalf("after promoted shell round %d: %s envelopes = %d, want 1", check.round, name, got)
			}
		}
	}
	requireSkillInventoryOrdinary(t, sess, skill.RunningJobsSkillName, "auto_disclosure")
	requireSkillInventoryOrdinary(t, sess, skill.ReadingTranscriptsSkillName, "auto_disclosure")
}

func TestGuideDisclosures_WorktreeFiresOnceAndNeverWithoutTool(t *testing.T) {
	t.Parallel()
	listArgs := json.RawMessage(`{"operation":"list"}`)
	repo := t.TempDir()
	initGitRepo(t, repo)

	c := llm.NewClient()
	adapter := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "w1", Name: "manage_worktree", Arguments: listArgs, Type: "function"})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "w2", Name: "manage_worktree", Arguments: listArgs, Type: "function"})
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
	if got := countSkillContextEnvelopes(t, reqs[1], skill.UsingWorktreesSkillName); got != 1 {
		t.Fatalf("after first manage_worktree: using-worktrees envelopes = %d, want 1", got)
	}
	if got := countSkillContextEnvelopes(t, reqs[2], skill.UsingWorktreesSkillName); got != 1 {
		t.Fatalf("after second manage_worktree: using-worktrees envelopes = %d, want 1", got)
	}
	requireSkillInventoryOrdinary(t, sess, skill.UsingWorktreesSkillName, "auto_disclosure")
}

func appendFillerTurnsForFold(s *Session, count int) {
	for i := range count {
		u := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("filler-user-%d", i)))
		s.recordTurn(u, u)
		a := schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("filler-assistant-%d %s", i, strings.Repeat("x", 200))))
		s.recordTurn(a, a)
	}
}

func TestGuideDisclosures_CompactionSurvival(t *testing.T) {
	t.Parallel()
	bgArgs := idleShellJSON(`,"mode":"background"`)

	t.Run("reload_skills_reloads_auto_disclosed_guide", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		step := 0
		var reqs []llm.Request
		main := func(req llm.Request) llm.Response {
			reqs = append(reqs, req)
			step++
			switch step {
			case 1:
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
			case 2:
				return agenttest.ToolCallResponse(communicateCall("c1", "first done"))
			case 3:
				return agenttest.ToolCallResponse(compactContextCall("cmp1", []string{skill.RunningJobsSkillName}))
			case 4:
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh2", Name: "shell", Arguments: bgArgs, Type: "function"})
			default:
				return agenttest.ToolCallResponse(communicateCall("c2", "second done"))
			}
		}
		sess, _, _ := newReloadSession(t, dir, 0, main, reloadSummaryResponder("SUM_RELOAD_7910", nil), withConfig(SessionConfig{
			StateDir:      t.TempDir(),
			AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
			testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
		}))
		defer sess.Close()

		if _, err := sess.ProcessInput(tripwireCtx(t), "start initial background job", nil); err != nil {
			t.Fatalf("turn 1 ProcessInput: %v", err)
		}
		requireSkillInventoryOrdinary(t, sess, skill.RunningJobsSkillName, "auto_disclosure")
		if _, ok := sess.advertisedSkillInventory()[skill.RunningJobsSkillName]; !ok {
			t.Fatal("auto-disclosed running-jobs missing from advertisedSkillInventory for compaction elicitation")
		}

		appendFillerTurnsForFold(sess, 20)

		if _, err := sess.ProcessInput(tripwireCtx(t), "compact and reload running-jobs", nil); err != nil {
			t.Fatalf("turn 2 ProcessInput: %v", err)
		}
		if len(reqs) < 5 {
			t.Fatalf("expected 5 model requests, got %d", len(reqs))
		}
		if got := countSkillContextEnvelopes(t, reqs[3], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("post-compaction request after reload_skills=[running-jobs] has %d envelopes, want 1", got)
		}
		if got := countSkillContextEnvelopes(t, reqs[4], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("post-compaction follow-up background shell produced %d envelopes, want 1 (no duplicate)", got)
		}
	})

	t.Run("retrigger_after_compaction_fold_without_reload", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		step := 0
		var reqs []llm.Request
		main := func(req llm.Request) llm.Response {
			reqs = append(reqs, req)
			step++
			switch step {
			case 1:
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
			case 2:
				return agenttest.ToolCallResponse(communicateCall("c1", "first done"))
			case 3:
				return agenttest.ToolCallResponse(compactContextCall("cmp-none", []string{}))
			case 4:
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh2", Name: "shell", Arguments: bgArgs, Type: "function"})
			case 5:
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh3", Name: "shell", Arguments: bgArgs, Type: "function"})
			default:
				return agenttest.ToolCallResponse(communicateCall("c2", "second done"))
			}
		}
		sess, _, _ := newReloadSession(t, dir, 0, main, reloadSummaryResponder("SUM_DROP_7910", nil), withConfig(SessionConfig{
			StateDir:      t.TempDir(),
			AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
			testOnly:      testConfig{skipGitSnapshot: true, noSyncJobStore: true},
		}))
		defer sess.Close()

		if _, err := sess.ProcessInput(tripwireCtx(t), "start initial background job", nil); err != nil {
			t.Fatalf("turn 1 ProcessInput: %v", err)
		}
		prior := requireSkillInventoryOrdinary(t, sess, skill.RunningJobsSkillName, "auto_disclosure")

		appendFillerTurnsForFold(sess, 20)

		if _, err := sess.ProcessInput(tripwireCtx(t), "compact without reload then run background shells", nil); err != nil {
			t.Fatalf("turn 2 ProcessInput: %v", err)
		}
		if len(reqs) < 6 {
			t.Fatalf("expected 6 model requests, got %d", len(reqs))
		}
		if got := countSkillContextEnvelopes(t, reqs[3], skill.RunningJobsSkillName); got != 0 {
			t.Fatalf("fixture setup error: post-fold request before re-trigger still had %d running-jobs envelopes", got)
		}
		if got := countSkillContextEnvelopes(t, reqs[4], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("post-fold re-trigger delivered %d running-jobs envelopes, want 1", got)
		}
		if got := countSkillContextEnvelopes(t, reqs[5], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("second post-fold background shell produced %d running-jobs envelopes, want 1 (no duplicate)", got)
		}
		if !sess.skillContentInLiveHistory(prior.Identity) {
			t.Fatal("expected re-delivered running-jobs carrier to remain in live post-compaction history")
		}
	})

	t.Run("resume_deduplicates_when_present_and_redelivers_after_drop", func(t *testing.T) {
		t.Parallel()
		stateDir := t.TempDir()
		dir := t.TempDir()

		c1 := llm.NewClient()
		a1 := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh1", Name: "shell", Arguments: bgArgs, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("c1", "done"))
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
			t.Fatalf("initial ProcessInput: %v", err)
		}
		prior := requireSkillInventoryOrdinary(t, sess, skill.RunningJobsSkillName, "auto_disclosure")
		id := sess.ID()
		sess.Close()

		// Resume while the skill carrier is still in live history: a new
		// background shell must not inject a duplicate carrier.
		meta1, err := schema.LoadSessionMeta(stateDir, id)
		if err != nil {
			t.Fatalf("LoadSessionMeta (before compact): %v", err)
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
		registerTestSessionNamer(c2)
		resumed1, err := RestoreSessionFromMetaWithConfig(c2, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta1, RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig (present): %v", err)
		}
		if _, err := resumed1.ProcessInput(tripwireCtx(t), "run while carrier present", nil); err != nil {
			resumed1.Close()
			t.Fatalf("resumed1 ProcessInput: %v", err)
		}
		reqs2 := a2.Requests()
		if len(reqs2) < 2 {
			resumed1.Close()
			t.Fatalf("resumed1 expected 2 requests, got %d", len(reqs2))
		}
		if got := countSkillContextEnvelopes(t, reqs2[1], skill.RunningJobsSkillName); got != 1 {
			resumed1.Close()
			t.Fatalf("resumed session with carrier present had %d running-jobs envelopes, want 1", got)
		}

		// Fold away the carrier via Compact, close, and restore again.
		appendFillerTurnsForFold(resumed1, 20)
		if err := resumed1.Compact(tripwireCtx(t)); err != nil {
			resumed1.Close()
			t.Fatalf("Compact: %v", err)
		}
		if resumed1.skillContentInLiveHistory(prior.Identity) {
			resumed1.Close()
			t.Fatal("fixture setup error: running-jobs carrier survived Compact")
		}
		resumed1.Close()

		meta2, err := schema.LoadSessionMeta(stateDir, id)
		if err != nil {
			t.Fatalf("LoadSessionMeta (after compact): %v", err)
		}
		c3 := llm.NewClient()
		a3 := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh3", Name: "shell", Arguments: bgArgs, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "sh4", Name: "shell", Arguments: bgArgs, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("c3", "done"))
			},
		)
		c3.Register(a3)
		resumed2, err := RestoreSessionFromMetaWithConfig(c3, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta2, RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig (compacted): %v", err)
		}
		defer resumed2.Close()

		requireSkillInventoryOrdinary(t, resumed2, skill.RunningJobsSkillName, "auto_disclosure")
		if resumed2.skillContentInLiveHistory(prior.Identity) {
			t.Fatal("fixture setup error: resumed compacted history still contained running-jobs carrier")
		}
		if _, err := resumed2.ProcessInput(tripwireCtx(t), "run after compacted resume", nil); err != nil {
			t.Fatalf("resumed2 ProcessInput: %v", err)
		}
		reqs3 := a3.Requests()
		if len(reqs3) < 3 {
			t.Fatalf("resumed2 expected 3 requests, got %d", len(reqs3))
		}
		inv := requestInventoryDocument(t, reqs3[0])
		if !slices.ContainsFunc(inv, func(e schema.SkillInventorySummary) bool {
			return e.Name == skill.RunningJobsSkillName && e.Availability == skillAvailabilityReloadable
		}) {
			t.Fatalf("post-compaction reload reminder missing reloadable running-jobs: %+v", inv)
		}
		if got := countSkillContextEnvelopes(t, reqs3[0], skill.RunningJobsSkillName); got != 0 {
			t.Fatalf("resumed2 request 0 before trigger had %d running-jobs envelopes, want 0", got)
		}
		if got := countSkillContextEnvelopes(t, reqs3[1], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("resumed2 request 1 after trigger had %d running-jobs envelopes, want 1", got)
		}
		if got := countSkillContextEnvelopes(t, reqs3[2], skill.RunningJobsSkillName); got != 1 {
			t.Fatalf("resumed2 request 2 after second trigger had %d running-jobs envelopes, want 1", got)
		}
	})
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
		appendFillerTurnsForFold(resumed, 20)
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
