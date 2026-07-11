package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgent_ValidFull(t *testing.T) {
	data := []byte(`---
name: code-reviewer
description: Reviews code for quality and correctness
model: sonnet
color: green
tools:
  - read_file
  - shell
skills:
  - brainstorming
tasks:
  - title: Review
    prompt: Review this code
    reasoning_effort: high
    type: verify
    insert: parent_tasks
---
# System Prompt
You are a code reviewer.
`)
	agent, err := ParseAgent(data, "test-plugin")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.Name != "code-reviewer" {
		t.Errorf("Name = %q, want %q", agent.Name, "code-reviewer")
	}
	if agent.Description != "Reviews code for quality and correctness" {
		t.Errorf("Description = %q", agent.Description)
	}
	if agent.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", agent.Model)
	}
	if agent.Color != "green" {
		t.Errorf("Color = %q, want green", agent.Color)
	}
	if agent.AllTools {
		t.Error("AllTools should be false")
	}
	if len(agent.Tools) != 2 || agent.Tools[0] != "read_file" || agent.Tools[1] != "shell" {
		t.Errorf("Tools = %v, want [read_file shell]", agent.Tools)
	}
	if len(agent.Skills) != 1 || agent.Skills[0] != "brainstorming" {
		t.Errorf("Skills = %v", agent.Skills)
	}
	if len(agent.Tasks) != 1 {
		t.Fatalf("Tasks = %v, want 1 task", agent.Tasks)
	}
	if agent.Tasks[0].Title != "Review" || agent.Tasks[0].Prompt != "Review this code" {
		t.Errorf("Task[0] = %+v", agent.Tasks[0])
	}
	if agent.Tasks[0].ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q", agent.Tasks[0].ReasoningEffort)
	}
	if agent.Tasks[0].Type != "verify" {
		t.Errorf("Type = %q", agent.Tasks[0].Type)
	}
	if agent.Tasks[0].Insert != "parent_tasks" {
		t.Errorf("Insert = %q", agent.Tasks[0].Insert)
	}
	if agent.SystemPrompt != "# System Prompt\nYou are a code reviewer.\n" {
		t.Errorf("SystemPrompt = %q", agent.SystemPrompt)
	}
	if agent.PluginName != "test-plugin" {
		t.Errorf("PluginName = %q", agent.PluginName)
	}
}

func TestParseAgent_Minimal(t *testing.T) {
	data := []byte(`---
name: minimal-agent
description: Does the minimum
---
Body here.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.Name != "minimal-agent" {
		t.Errorf("Name = %q", agent.Name)
	}
	if agent.Description != "Does the minimum" {
		t.Errorf("Description = %q", agent.Description)
	}
	if agent.Model != "inherit" {
		t.Errorf("Model = %q, want inherit", agent.Model)
	}
	if agent.Color != "blue" {
		t.Errorf("Color = %q, want blue", agent.Color)
	}
	if agent.SystemPrompt != "Body here.\n" {
		t.Errorf("SystemPrompt = %q", agent.SystemPrompt)
	}
	if len(agent.Tools) != 0 {
		t.Errorf("Tools = %v, want empty", agent.Tools)
	}
	if len(agent.Skills) != 0 {
		t.Errorf("Skills = %v, want empty", agent.Skills)
	}
	if len(agent.Tasks) != 0 {
		t.Errorf("Tasks = %v, want empty", agent.Tasks)
	}
}

func TestParseAgent_MissingName(t *testing.T) {
	data := []byte(`---
description: No name here
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q should mention 'name'", err.Error())
	}
}

func TestParseAgent_MissingDescription(t *testing.T) {
	data := []byte(`---
name: no-desc
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for missing description")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error %q should mention 'description'", err.Error())
	}
}

func TestParseAgent_EmptyName(t *testing.T) {
	data := []byte(`---
name: ""
description: desc
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q should mention 'name'", err.Error())
	}
}

func TestParseAgent_EmptyDescription(t *testing.T) {
	data := []byte(`---
name: name
description: ""
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for empty description")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error %q should mention 'description'", err.Error())
	}
}

func TestParseAgent_ToolsAll(t *testing.T) {
	data := []byte(`---
name: all-tools
description: all
tools: all
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if !agent.AllTools {
		t.Error("AllTools should be true")
	}
	if len(agent.Tools) != 0 {
		t.Errorf("Tools should be empty when AllTools is true, got %v", agent.Tools)
	}
}

func TestParseAgent_ToolsList(t *testing.T) {
	data := []byte(`---
name: list-tools
description: list
tools:
  - read_file
  - shell
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.AllTools {
		t.Error("AllTools should be false")
	}
	if len(agent.Tools) != 2 || agent.Tools[0] != "read_file" || agent.Tools[1] != "shell" {
		t.Errorf("Tools = %v, want [read_file shell]", agent.Tools)
	}
}

func TestParseAgent_ToolsClaudeNames(t *testing.T) {
	data := []byte(`---
name: claude-tools
description: claude
tools:
  - Read
  - Bash
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.AllTools {
		t.Error("AllTools should be false")
	}
	if len(agent.Tools) != 2 || agent.Tools[0] != "read_file" || agent.Tools[1] != "shell" {
		t.Errorf("Tools = %v, want [read_file shell] (Claude names mapped to serf canonical)", agent.Tools)
	}
}

func TestParseAgent_ToolsCommaString(t *testing.T) {
	// Claude Code agent frontmatter commonly writes tools as a
	// comma-separated plain string (e.g. superpowers-chrome browser-user).
	data := []byte(`---
name: comma-tools
description: comma
tools: Read, Grep, Glob, Skill, mcp__plugin_superpowers-chrome_chrome__use_browser
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.AllTools {
		t.Error("AllTools should be false")
	}
	// Names without a serf canonical equivalent ("Skill", mcp__*) pass through unchanged.
	want := []string{"read_file", "grep", "glob", "Skill", "mcp__plugin_superpowers-chrome_chrome__use_browser"}
	if len(agent.Tools) != len(want) {
		t.Fatalf("Tools = %v, want %v", agent.Tools, want)
	}
	for i, w := range want {
		if agent.Tools[i] != w {
			t.Errorf("Tools[%d] = %q, want %q", i, agent.Tools[i], w)
		}
	}
}

func TestParseAgent_ToolsSingleString(t *testing.T) {
	data := []byte(`---
name: single-tool
description: single
tools: Read
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.AllTools {
		t.Error("AllTools should be false")
	}
	if len(agent.Tools) != 1 || agent.Tools[0] != "read_file" {
		t.Errorf("Tools = %v, want [read_file]", agent.Tools)
	}
}

func TestParseAgent_ToolsCommaStringMessy(t *testing.T) {
	// Extra whitespace, trailing comma, empty elements are tolerated.
	data := []byte(`---
name: messy-tools
description: messy
tools: "Read ,  Bash,, shell , "
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if len(agent.Tools) != 3 || agent.Tools[0] != "read_file" || agent.Tools[1] != "shell" || agent.Tools[2] != "shell" {
		t.Errorf("Tools = %v, want [read_file shell shell]", agent.Tools)
	}
}

func TestParseAgent_ToolsCommaStringAllElement(t *testing.T) {
	// "all"/"*" mixed into a comma list is rejected the same way as in a
	// YAML list: unrestricted access must use the scalar `tools: all`.
	for _, val := range []string{"Read, all", "Read, *"} {
		data := []byte("---\nname: comma-all\ndescription: bad\ntools: \"" + val + "\"\n---\nBody.\n")
		if _, err := ParseAgent(data, "p"); err == nil {
			t.Errorf("expected error for tools = %q", val)
		}
	}
}

func TestParseAgent_SkillsString(t *testing.T) {
	// Claude Code writes skills as a plain (possibly comma-separated) string.
	data := []byte(`---
name: string-skills
description: skills
skills: superpowers-chrome:browsing
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if len(agent.Skills) != 1 || agent.Skills[0] != "superpowers-chrome:browsing" {
		t.Errorf("Skills = %v, want [superpowers-chrome:browsing]", agent.Skills)
	}
}

func TestParseAgent_SkillsCommaString(t *testing.T) {
	data := []byte(`---
name: comma-skills
description: skills
skills: "a:one, b:two, "
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if len(agent.Skills) != 2 || agent.Skills[0] != "a:one" || agent.Skills[1] != "b:two" {
		t.Errorf("Skills = %v, want [a:one b:two]", agent.Skills)
	}
}

func TestParseAgent_ToolsStarScalar(t *testing.T) {
	data := []byte(`---
name: star
description: star
tools: "*"
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for tools = *")
	}
}

func TestParseAgent_ToolsListAll(t *testing.T) {
	data := []byte(`---
name: list-all
description: bad
tools:
  - all
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for tools list containing all")
	}
}

func TestParseAgent_ToolsListStar(t *testing.T) {
	data := []byte(`---
name: list-star
description: bad
tools:
  - "*"
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for tools list containing *")
	}
}

func TestParseAgent_ToolsInvalidType(t *testing.T) {
	data := []byte(`---
name: bad-tools
description: bad
tools: 123
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for tools = 123")
	}
}

func TestParseAgent_SkillsList(t *testing.T) {
	data := []byte(`---
name: skills
description: skills
skills:
  - brainstorming
  - research
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if len(agent.Skills) != 2 || agent.Skills[0] != "brainstorming" || agent.Skills[1] != "research" {
		t.Errorf("Skills = %v", agent.Skills)
	}
}

func TestParseAgent_SkillsInvalidType(t *testing.T) {
	data := []byte(`---
name: bad-skills
description: bad
skills: 123
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for skills of non-string, non-list type")
	}
}

func TestParseAgent_TasksList(t *testing.T) {
	data := []byte(`---
name: tasks
description: tasks
tasks:
  - title: T
    prompt: P
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if len(agent.Tasks) != 1 {
		t.Fatalf("Tasks = %v, want 1", agent.Tasks)
	}
	if agent.Tasks[0].Title != "T" || agent.Tasks[0].Prompt != "P" {
		t.Errorf("Task[0] = %+v", agent.Tasks[0])
	}
}

func TestParseAgent_TasksInvalidType(t *testing.T) {
	data := []byte(`---
name: bad-tasks
description: bad
tasks: not-a-list
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for tasks not a list")
	}
}

func TestParseAgent_ModelOverride(t *testing.T) {
	data := []byte(`---
name: model-test
description: model
model: opus
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.Model != "opus" {
		t.Errorf("Model = %q, want opus", agent.Model)
	}
}

func TestParseAgent_ColorOverride(t *testing.T) {
	data := []byte(`---
name: color-test
description: color
color: red
---
Body.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	if agent.Color != "red" {
		t.Errorf("Color = %q, want red", agent.Color)
	}
}

func TestParseAgent_SystemPromptFromBody(t *testing.T) {
	data := []byte(`---
name: sys
description: sys
---
# Heading
Some text.
More text.
`)
	agent, err := ParseAgent(data, "p")
	if err != nil {
		t.Fatalf("ParseAgent error: %v", err)
	}
	want := "# Heading\nSome text.\nMore text.\n"
	if agent.SystemPrompt != want {
		t.Errorf("SystemPrompt = %q, want %q", agent.SystemPrompt, want)
	}
}

func TestParseAgent_InvalidFrontmatter(t *testing.T) {
	data := []byte(`---
not yaml: [}
---
Body.
`)
	_, err := ParseAgent(data, "p")
	if err == nil {
		t.Fatal("expected error for invalid frontmatter")
	}
}

func TestDiscoverPluginAgents(t *testing.T) {
	dir := t.TempDir()

	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeAgent := func(name, content string) {
		if err := os.WriteFile(filepath.Join(agentsDir, name+".md"), []byte(content), 0644); err != nil {
			t.Fatalf("write agent %s: %v", name, err)
		}
	}

	writeAgent("agent-a", `---
name: agent-a
description: Agent A
---
# A
`)
	writeAgent("agent-b", `---
name: agent-b
description: Agent B
model: sonnet
color: green
---
# B
`)

	agents, err := discoverPluginAgents(dir, nil, "my-plugin")
	if err != nil {
		t.Fatalf("discoverPluginAgents error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(agents), agents)
	}

	a, ok := agents["my-plugin:agent-a"]
	if !ok {
		t.Fatalf("missing my-plugin:agent-a")
	}
	if a.Name != "agent-a" || a.Description != "Agent A" {
		t.Errorf("agent-a = %+v", a)
	}
	if a.SystemPrompt != "# A\n" {
		t.Errorf("agent-a SystemPrompt = %q", a.SystemPrompt)
	}

	b, ok := agents["my-plugin:agent-b"]
	if !ok {
		t.Fatalf("missing my-plugin:agent-b")
	}
	if b.Name != "agent-b" || b.Model != "sonnet" || b.Color != "green" {
		t.Errorf("agent-b = %+v", b)
	}
}

func TestDiscoverPluginAgents_NoDir(t *testing.T) {
	dir := t.TempDir()
	agents, err := discoverPluginAgents(dir, nil, "p")
	if err != nil {
		t.Fatalf("discoverPluginAgents error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestDiscoverPluginAgents_BadAgentFile(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentsDir, 0755)
	os.WriteFile(filepath.Join(agentsDir, "bad.md"), []byte(`---
not yaml: [}
---
`), 0644)
	_, err := discoverPluginAgents(dir, nil, "p")
	if err == nil {
		t.Fatal("expected error for bad agent file")
	}
}
