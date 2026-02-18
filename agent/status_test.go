package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestSession_DetailedStatus_CoreTools(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Should have core tools.
	if len(ds.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	// All tools from a vanilla session should be "core".
	for _, tool := range ds.Tools {
		if tool.Source != "core" {
			t.Errorf("tool %q has source %q, want core", tool.Name, tool.Source)
		}
	}

	// Verify some known core tools are present.
	toolNames := map[string]bool{}
	for _, tool := range ds.Tools {
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"shell", "read_file", "write_file", "edit_file"} {
		if !toolNames[name] {
			t.Errorf("missing core tool %q", name)
		}
	}
}

func TestSession_DetailedStatus_CustomTool(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Register a custom tool after session init.
	sess.RegisterTool("my_custom_tool", "A custom tool", map[string]any{
		"type": "object", "properties": map[string]any{},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	ds := sess.DetailedStatus()

	found := false
	for _, tool := range ds.Tools {
		if tool.Name == "my_custom_tool" {
			if tool.Source != "custom" {
				t.Errorf("custom tool source = %q, want custom", tool.Source)
			}
			found = true
		}
	}
	if !found {
		t.Error("custom tool not found in DetailedStatus")
	}
}

func TestSession_DetailedStatus_Skills(t *testing.T) {
	dir := t.TempDir()

	// Create a skill directory.
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: A test skill
---
# My Skill
`), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	found := false
	for _, skill := range ds.Skills {
		if skill.Name == "my-skill" {
			found = true
			if skill.Description != "A test skill" {
				t.Errorf("skill description = %q, want %q", skill.Description, "A test skill")
			}
		}
	}
	if !found {
		t.Error("skill my-skill not found in DetailedStatus")
	}
}

func TestSession_DetailedStatus_EmptySections(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// No MCP servers in vanilla session.
	if len(ds.MCP) != 0 {
		t.Errorf("expected no MCP servers, got %d", len(ds.MCP))
	}
	// No plugins.
	if len(ds.Plugins) != 0 {
		t.Errorf("expected no plugins, got %d", len(ds.Plugins))
	}
	// No subagents.
	if len(ds.Subagents) != 0 {
		t.Errorf("expected no subagents, got %d", len(ds.Subagents))
	}
	// No plugin agents.
	if len(ds.Agents) != 0 {
		t.Errorf("expected no agents, got %d", len(ds.Agents))
	}
}

func TestSession_DetailedStatus_Subagents(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("sub done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Spawn a subagent directly.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := sess.spawnAgent(ctx, "test task", "", "", 1, "")
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	_ = result

	// Give subagent a moment to register.
	time.Sleep(50 * time.Millisecond)

	ds := sess.DetailedStatus()

	if len(ds.Subagents) != 1 {
		t.Fatalf("expected 1 subagent, got %d", len(ds.Subagents))
	}
	sub := ds.Subagents[0]
	if sub.ID == "" {
		t.Error("subagent ID should not be empty")
	}
}

func TestSession_DetailedStatus_ToolsSorted(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	names := make([]string, len(ds.Tools))
	for i, tool := range ds.Tools {
		names[i] = tool.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("tools not sorted: %v", names)
	}
}
