package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePluginAgent(t *testing.T) {
	content := `---
name: code-reviewer
description: Use this agent when reviewing code
model: inherit
color: blue
tools: ["Read", "Grep", "Bash"]
---

You are a code review specialist.

**Process:**
1. Read the files
2. Analyze for issues
3. Report findings`

	agent, err := parsePluginAgent([]byte(content), "my-plugin")
	if err != nil {
		t.Fatalf("parsePluginAgent: %v", err)
	}
	if agent.Name != "code-reviewer" {
		t.Errorf("Name = %q", agent.Name)
	}
	if agent.Description != "Use this agent when reviewing code" {
		t.Errorf("Description = %q", agent.Description)
	}
	if agent.Model != "inherit" {
		t.Errorf("Model = %q", agent.Model)
	}
	if agent.Color != "blue" {
		t.Errorf("Color = %q", agent.Color)
	}
	if agent.PluginName != "my-plugin" {
		t.Errorf("PluginName = %q", agent.PluginName)
	}
	// Tools should be mapped to serf names
	wantTools := []string{"read_file", "grep", "shell"}
	if len(agent.Tools) != len(wantTools) {
		t.Fatalf("Tools = %v, want %v", agent.Tools, wantTools)
	}
	for i, tool := range agent.Tools {
		if tool != wantTools[i] {
			t.Errorf("Tools[%d] = %q, want %q", i, tool, wantTools[i])
		}
	}
	if !strings.Contains(agent.SystemPrompt, "code review specialist") {
		t.Error("SystemPrompt missing body content")
	}
}

func TestParsePluginAgent_MissingRequired(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"missing name", "---\ndescription: foo\nmodel: inherit\ncolor: blue\n---\nbody"},
		{"missing description", "---\nname: test\nmodel: inherit\ncolor: blue\n---\nbody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePluginAgent([]byte(tc.content), "p")
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParsePluginAgent_OptionalModelAndColor(t *testing.T) {
	// Model and color are optional — default to "inherit" and "blue".
	content := "---\nname: test\ndescription: does things\n---\nbody"
	agent, err := parsePluginAgent([]byte(content), "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Model != "inherit" {
		t.Errorf("Model = %q, want inherit", agent.Model)
	}
	if agent.Color != "blue" {
		t.Errorf("Color = %q, want blue", agent.Color)
	}
}

func TestParsePluginAgent_NoTools(t *testing.T) {
	content := "---\nname: test\ndescription: desc\nmodel: inherit\ncolor: green\n---\nbody"
	agent, err := parsePluginAgent([]byte(content), "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agent.Tools) != 0 {
		t.Errorf("Tools = %v, want empty", agent.Tools)
	}
}

func TestParsePluginAgent_AllToolsScalar(t *testing.T) {
	content := "---\nname: test\ndescription: desc\ntools: all\n---\nbody"
	agent, err := parsePluginAgent([]byte(content), "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !agent.AllTools {
		t.Fatal("AllTools = false, want true")
	}
	if len(agent.Tools) != 0 {
		t.Fatalf("Tools = %v, want empty when AllTools is true", agent.Tools)
	}
}

func TestParsePluginAgent_AllToolsListFormsRejected(t *testing.T) {
	cases := []string{
		"---\nname: test\ndescription: desc\ntools: \"*\"\n---\nbody",
		"---\nname: test\ndescription: desc\ntools: [all]\n---\nbody",
		"---\nname: test\ndescription: desc\ntools: [\"*\"]\n---\nbody",
		"---\nname: test\ndescription: desc\ntools: [all, shell]\n---\nbody",
	}
	for _, content := range cases {
		_, err := parsePluginAgent([]byte(content), "p")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "use `tools: all`") {
			t.Fatalf("error = %v, want canonical form hint", err)
		}
	}
}

func TestParsePluginAgent_Skills(t *testing.T) {
	data := []byte("---\nname: test-eng\ndescription: test engineer\nskills: [test-engineering, debugging]\n---\nYou write tests.\n")
	agent, err := parsePluginAgent(data, "builtin")
	if err != nil {
		t.Fatalf("parsePluginAgent: %v", err)
	}
	if len(agent.Skills) != 2 {
		t.Fatalf("Skills = %v, want 2 items", agent.Skills)
	}
	if agent.Skills[0] != "test-engineering" || agent.Skills[1] != "debugging" {
		t.Errorf("Skills = %v, want [test-engineering debugging]", agent.Skills)
	}
}

func TestParsePluginAgent_NoSkills(t *testing.T) {
	data := []byte("---\nname: explorer\ndescription: explore\n---\nRead-only.\n")
	agent, err := parsePluginAgent(data, "builtin")
	if err != nil {
		t.Fatalf("parsePluginAgent: %v", err)
	}
	if len(agent.Skills) != 0 {
		t.Errorf("Skills = %v, want empty", agent.Skills)
	}
}

func TestDiscoverPluginAgents(t *testing.T) {
	dir := makePluginDir(t, "my-plugin")
	agentsDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentsDir, 0755)
	os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\nname: reviewer\ndescription: reviews code\nmodel: inherit\ncolor: blue\n---\nYou review."),
		0644)

	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plugin.Agents["my-plugin:reviewer"]; !ok {
		t.Errorf("expected 'my-plugin:reviewer', got keys: %v", keys(plugin.Agents))
	}
	agent := plugin.Agents["my-plugin:reviewer"]
	if agent.Description != "reviews code" {
		t.Errorf("Description = %q", agent.Description)
	}
}

func TestDiscoverPluginAgents_NoAgentsDir(t *testing.T) {
	dir := makePluginDir(t, "no-agents")
	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugin.Agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(plugin.Agents))
	}
}

func TestDiscoverPluginAgents_SkipsNonMd(t *testing.T) {
	dir := makePluginDir(t, "md-plugin")
	agentsDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentsDir, 0755)
	os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\nname: reviewer\ndescription: reviews\nmodel: inherit\ncolor: blue\n---\nbody"),
		0644)
	os.WriteFile(filepath.Join(agentsDir, "notes.txt"),
		[]byte("not an agent"), 0644)

	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugin.Agents) != 1 {
		t.Errorf("expected 1 agent, got %d: %v", len(plugin.Agents), keys(plugin.Agents))
	}
}

func TestParsePluginAgent_WithTasks(t *testing.T) {
	input := []byte("---\nname: test-agent\ndescription: \"Test agent\"\nmodel: inherit\ntasks:\n  - title: First step\n    prompt: \"Do the first thing\"\n    reasoning_effort: low\n  - title: Do work\n    insert: parent_tasks\n    prompt: \"Implement it\"\n    reasoning_effort: xhigh\n  - title: Verify\n    prompt: \"Check it\"\n---\n\nYou are a test agent.\n")
	agent, err := parsePluginAgent(input, "test-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(agent.Tasks))
	}
	if agent.Tasks[0].Title != "First step" {
		t.Errorf("task 0 title = %q", agent.Tasks[0].Title)
	}
	if agent.Tasks[0].ReasoningEffort != "low" {
		t.Errorf("task 0 effort = %q", agent.Tasks[0].ReasoningEffort)
	}
	if agent.Tasks[1].Insert != "parent_tasks" {
		t.Errorf("task 1 insert = %q", agent.Tasks[1].Insert)
	}
	if agent.Tasks[1].ReasoningEffort != "xhigh" {
		t.Errorf("task 1 effort = %q", agent.Tasks[1].ReasoningEffort)
	}
}

func TestParsePluginAgent_NoTasks(t *testing.T) {
	input := []byte("---\nname: simple\ndescription: \"No tasks\"\n---\n\nJust a prompt.\n")
	agent, err := parsePluginAgent(input, "builtin")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.Tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(agent.Tasks))
	}
}
