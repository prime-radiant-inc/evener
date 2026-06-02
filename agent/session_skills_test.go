package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/llm"
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

// use_skill tests exercise provider profiles that expose the use_skill tool.

func TestUseSkill_ReturnsBody(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func TestUseSkill_NotFound_ReturnsError(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
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
	root := t.TempDir()
	initGitRepo(t, root)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	root := t.TempDir()
	initGitRepo(t, root)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	root := t.TempDir()
	initGitRepo(t, root)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func TestOpenAI_IncludesUseSkillTool(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	for _, td := range p.ToolDefinitions() {
		if td.Name == "use_skill" {
			return
		}
	}
	t.Fatal("OpenAI profile should include use_skill tool definition")
}

func TestOpenAIUseSkillToolExecutes(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
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
		Arguments: json.RawMessage(`{"skill_name":"greet","purpose":"test skill loading"}`),
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
	root := t.TempDir()
	initGitRepo(t, root)
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
