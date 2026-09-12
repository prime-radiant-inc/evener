package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

// useSkillCall builds a tool call to the use_skill tool.
func useSkillCall(id, skillName string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]string{"skill_name": skillName})
	return llm.ToolCallData{
		ID:        id,
		Name:      "use_skill",
		Arguments: args,
		Type:      "function",
	}
}

func TestBuildPromptDataHasUseSkillTracksCallableDefinitions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		keep   []string
		legacy bool
		want   bool
	}{
		{name: "legacy uninitialized cache", legacy: true},
		{name: "empty final cache"},
		{name: "restricted final cache", keep: []string{"read_file"}},
		{name: "callable final cache", keep: []string{"use_skill"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			if tc.legacy {
				// A pre-cache/legacy session has no final definitions. This is the
				// only direct assignment here; the other cases exercise the real
				// registry restriction and rebuildToolDefsCache lifecycle below.
				s.cachedToolDefs = nil
			} else {
				rebuildPromptToolCacheForTest(s, tc.keep...)
			}
			if got := s.buildPromptData(s.currentEnv()).HasUseSkill; got != tc.want {
				t.Fatalf("HasUseSkill = %v, want %v", got, tc.want)
			}
		})
	}
}

// rebuildPromptToolCacheForTest models the production final-tool restriction
// boundary: restrict the initialized registry, then rebuild the cached provider
// definitions that are sent to the model. HasUseSkill must follow this cache,
// not the profile's larger initial definition set.
func rebuildPromptToolCacheForTest(s *Session, keep ...string) {
	allowed := make(map[string]bool, len(keep))
	for _, name := range keep {
		allowed[name] = true
	}
	for name := range s.reg.RegisteredNames() {
		if !allowed[name] {
			s.reg.Remove(name)
		}
	}
	s.rebuildToolDefsCache()
}

// use_skill tests exercise provider profiles that expose the use_skill tool.

func TestUseSkill_ReturnsBody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nGreet people warmly.\n")

	c := llm.NewClient()
	skill := useSkillCall("s1", "greet")
	comm := communicateCall("c1", "done")

	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(skill) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "greet Jesse", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// Verify the use_skill tool result was returned in the conversation.
	// The fakeAdapter captures requests; the second request should include
	// a tool result containing the skill body.
	reqs := f.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(reqs))
	}

	// Look for "Greet people warmly" in the second request's messages (tool result).
	found := false
	for _, msg := range reqs[1].Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentToolResult {
				content, _ := part.ToolResult.Content.(string)
				if strings.Contains(content, "Greet people warmly") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected skill body 'Greet people warmly' in tool result of second request")
	}
}

func TestUseSkill_InlineMentionPreservesUserInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nGreet people warmly.\n")

	input := "Please use /greet for Jesse"
	skillBody := "Greet people warmly."
	c := llm.NewClient()
	skillCall := useSkillCall("s1", "greet")
	comm := communicateCall("c1", "done")
	adapter := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(skillCall) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var eventsSeen []events.SessionEvent
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			eventsSeen = append(eventsSeen, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, input, nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-eventsDone

	var userInputs []string
	for _, ev := range eventsSeen {
		if ev.Kind != events.EventUserInput {
			continue
		}
		data, ok := ev.Data.(events.UserInputData)
		if ok {
			userInputs = append(userInputs, data.Text)
		}
	}
	if len(userInputs) != 1 || userInputs[0] != input {
		t.Fatalf("user input events = %q, want complete sentence %q", userInputs, input)
	}
	if strings.Contains(userInputs[0], "Greet people warmly.") {
		t.Fatal("inline mention expanded the skill body before the model turn")
	}

	requests := adapter.Requests()
	if len(requests) < 2 {
		t.Fatalf("expected tool follow-up request, got %d requests", len(requests))
	}
	var requestUserTexts []string
	for _, msg := range requests[0].Messages {
		if msg.Role == llm.RoleUser {
			requestUserTexts = append(requestUserTexts, msg.Text())
		}
	}
	if len(requestUserTexts) == 0 || requestUserTexts[len(requestUserTexts)-1] != input {
		t.Fatalf("provider user messages = %q, want final message exactly %q", requestUserTexts, input)
	}
	if strings.Contains(requestUserTexts[len(requestUserTexts)-1], skillBody) {
		t.Fatal("provider request expanded the inline skill body before the model turn")
	}

	foundSkillBody := false
	for _, msg := range requests[1].Messages {
		for _, part := range msg.Content {
			if part.Kind != llm.ContentToolResult {
				continue
			}
			content, _ := part.ToolResult.Content.(string)
			if strings.Contains(content, "Greet people warmly.") {
				foundSkillBody = true
			}
		}
	}
	if !foundSkillBody {
		t.Fatal("scripted inline use_skill call did not return the skill body")
	}
}

func TestStandaloneSkillActivationUsesCanonicalPluginName(t *testing.T) {
	s := newTestSession(t)
	s.skills = map[string]skill.SkillMeta{
		"plugin:simplify": {Name: "simplify", SkillFile: writeSkillBodyFile(t, "plugin steps")},
	}
	_ = drainSlashEvents(s)

	got, ok := s.expandSlashCommand(context.Background(), "/plugin:simplify")
	if !ok || got != "plugin steps" {
		t.Fatalf("expanded = %q, %v; want plugin skill body", got, ok)
	}
	var activated []string
	for _, ev := range drainSlashEvents(s) {
		if ev.Kind != events.EventSkillActivated {
			continue
		}
		if data, ok := ev.Data.(events.SkillActivatedData); ok {
			activated = append(activated, data.Name)
		}
	}
	if len(activated) != 1 || activated[0] != "plugin:simplify" {
		t.Fatalf("activation names = %v, want [plugin:simplify]", activated)
	}
}

func TestUseSkill_NotFound_ReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	// No skills directory — use_skill("nonexistent") should return error.

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.reg.ExecuteCall(context.Background(), sess.env, useSkillCall("s1", "nonexistent"))
	if !res.IsError {
		t.Fatalf("expected error for unknown skill, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "nonexistent") {
		t.Fatalf("error should mention skill name, got: %s", res.Output)
	}
}

func TestUseSkill_EmitsEvent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "deploy", "---\nname: deploy\ndescription: \"Deploy skill\"\n---\nDeploy instructions.\n")

	c := llm.NewClient()
	skill := useSkillCall("s1", "deploy")
	comm := communicateCall("c1", "done")

	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(skill) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "deploy", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-evDone

	var skillEvents []events.SessionEvent
	for _, ev := range evs {
		if ev.Kind == events.EventSkillActivated {
			skillEvents = append(skillEvents, ev)
		}
	}
	if len(skillEvents) != 1 {
		t.Fatalf("expected 1 SKILL_ACTIVATED event, got %d", len(skillEvents))
	}
	if d, ok := skillEvents[0].Data.(events.SkillActivatedData); !ok || d.Name != "deploy" {
		t.Fatalf("event name: got %+v want name %q", skillEvents[0].Data, "deploy")
	}
}

func TestUseSkill_SystemPromptContainsSkillList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nBody.\n")

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Capture the system prompt from the first message.
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Error("system prompt missing <skills> section")
	}
	if !strings.Contains(capturedSystem, "greet: Greeting skill") {
		t.Error("system prompt missing greet skill entry")
	}
}

func TestOpenAI_SkillsSectionUsesUseSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nBody.\n")

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Error("OpenAI system prompt should contain <skills> section")
	}
	if !strings.Contains(capturedSystem, "Load a skill by calling use_skill with its name") {
		t.Error("OpenAI system prompt should instruct model to use use_skill for skills")
	}
	if !strings.Contains(capturedSystem, "greet: Greeting skill") {
		t.Error("OpenAI system prompt missing greet skill entry")
	}
	if !strings.Contains(capturedSystem, "[") || !strings.Contains(capturedSystem, filepath.Join("skills", "greet")) {
		t.Error("OpenAI system prompt should list the skill directory for use_skill profiles")
	}
}

func TestOpenAI_PluginSkillCatalogUsesNamespacedName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	pluginDir := makePluginDir(t, "skill-plugin")
	skillDir := filepath.Join(pluginDir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: \"Plugin skill\"\n---\nBody.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "- skill-plugin:my-skill: Plugin skill [") {
		t.Fatalf("system prompt missing namespaced plugin skill entry:\n%s", capturedSystem)
	}
	if strings.Contains(capturedSystem, "- my-skill: Plugin skill [") {
		t.Fatalf("system prompt advertised bare plugin skill name:\n%s", capturedSystem)
	}
}

func TestOpenAI_IncludesUseSkillTool(t *testing.T) {
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	for _, td := range p.ToolDefinitions() {
		if td.Name == "use_skill" {
			return
		}
	}
	t.Fatal("OpenAI profile should include use_skill tool definition")
}

func TestOpenAIUseSkillToolExecutes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nUse greeting style.\n")

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	tool := sess.reg.Get("use_skill")
	if tool == nil {
		t.Fatal("OpenAI session registry missing use_skill executor")
	}

	result := sess.reg.ExecuteCall(context.Background(), execenv.NewLocalExecutionEnvironment(root), llm.ToolCallData{
		ID:        "call_use_skill",
		Name:      "use_skill",
		Arguments: json.RawMessage(`{"skill_name":"greet","intent":"test skill loading"}`),
		Type:      "function",
	})
	if result.IsError {
		t.Fatalf("use_skill returned error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Use greeting style.") {
		t.Fatalf("use_skill output missing skill body: %q", result.Output)
	}
}

func TestDiscoverSkills_PopulatedOnSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nBody.\n")

	// Verify DiscoverSkills actually works at the filesystem level — the session
	// field test is in TestUseSkill_SystemPromptContainsSkillList above.
	// Also verify that a non-git directory (skill dir created manually) still works.
	nonGit := t.TempDir()
	skillDir := filepath.Join(nonGit, "skills", "manual")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: manual\ndescription: \"Manual skill\"\n---\nManual.\n"), 0o644)

	env := execenv.NewLocalExecutionEnvironment(nonGit)
	skills := skill.DiscoverSkills(env)
	if _, ok := skills["manual"]; !ok {
		t.Errorf("expected skill 'manual' discovered in non-git directory")
	}
}

func TestNewSessionAutomaticallyDiscoversUserSkill(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	markGitRoot(t, project)

	userSkillDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "skills", "automatic-user")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const body = "automatic user skill sentinel"
	if err := os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte("---\nname: automatic-user\ndescription: automatic user skill\n---\n"+body+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sess, err := NewSession(llm.NewClient(), newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(project), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, ok := sess.skills["automatic-user"]; !ok {
		t.Fatal("automatic user skill was not discovered")
	}
	got, ok := sess.expandSlashCommand(context.Background(), "/automatic-user")
	if !ok || !strings.Contains(got, body) {
		t.Fatalf("slash skill expansion = %q, %v; want body %q", got, ok, body)
	}
}

func TestConfiguredSkillDirShadowsAutomaticUserSkill(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	markGitRoot(t, project)

	userSkillDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "skills", "same-name")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll user skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte("---\nname: same-name\ndescription: user\n---\nuser body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile user skill: %v", err)
	}

	extraDir := t.TempDir()
	extraSkillDir := filepath.Join(extraDir, "same-name")
	if err := os.MkdirAll(extraSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll configured skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extraSkillDir, "SKILL.md"), []byte("---\nname: same-name\ndescription: configured\n---\nconfigured body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile configured skill: %v", err)
	}

	sess, err := NewSession(llm.NewClient(), newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(project), SessionConfig{SkillsDirs: []string{extraDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if got := sess.skills["same-name"].Description; got != "configured" {
		t.Fatalf("skill description = %q, want configured", got)
	}
}

func TestProjectSkillShadowsAutomaticUserSkill(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	markGitRoot(t, project)
	writeSkillMD(t, project, "same-name", "---\nname: same-name\ndescription: project\n---\nproject body\n")

	userSkillDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "skills", "same-name")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll user skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte("---\nname: same-name\ndescription: user\n---\nuser body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile user skill: %v", err)
	}

	sess, err := NewSession(llm.NewClient(), newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(project), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if got := sess.skills["same-name"].Description; got != "project" {
		t.Fatalf("skill description = %q, want project", got)
	}
}
