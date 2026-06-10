package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// No MCP servers in vanilla session.
	if len(ds.MCP) != 0 {
		t.Errorf("expected no MCP servers, got %d", len(ds.MCP))
	}
	// No plugins in a vanilla session.
	if len(ds.Plugins) != 0 {
		t.Errorf("expected no plugins, got %d", len(ds.Plugins))
	}
	// No jobs.
	if len(ds.Jobs) != 0 {
		t.Errorf("expected no jobs, got %d", len(ds.Jobs))
	}
	// Core agents are always present.
	foundDefault := false
	foundExplorer := false
	foundSubagent := false
	for _, name := range ds.Agents {
		if name == "default" {
			foundDefault = true
		}
		if name == "explorer" {
			foundExplorer = true
		}
		if name == "subagent" {
			foundSubagent = true
		}
	}
	if !foundDefault {
		t.Errorf("expected core 'default' agent in %v", ds.Agents)
	}
	if !foundExplorer {
		t.Errorf("expected core 'explorer' agent in %v", ds.Agents)
	}
	if !foundSubagent {
		t.Errorf("expected core 'subagent' agent in %v", ds.Agents)
	}
}

func TestSession_DetailedStatus_ConfiguredWorkflowPlugin(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), coordinatorWorkflowSessionConfig(t, SessionConfig{}))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	if len(ds.Plugins) != 1 {
		t.Fatalf("expected 1 coordinator workflow plugin, got %d", len(ds.Plugins))
	}
	if ds.Plugins[0].Name != coordinatorWorkflowPluginName {
		t.Fatalf("plugin name = %q, want %q", ds.Plugins[0].Name, coordinatorWorkflowPluginName)
	}

	foundReviewer := false
	for _, name := range ds.Agents {
		if name == "reviewer" {
			foundReviewer = true
			break
		}
	}
	if !foundReviewer {
		t.Fatalf("expected configured coordinator workflow reviewer agent in %v", ds.Agents)
	}
}

func TestSession_DetailedStatus_Jobs(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	exitCode := 7
	startedAt := time.Now().UTC()
	endedAt := startedAt.Add(time.Second)
	const jobID = "job_status_projection"
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append started event: %v", err)
	}
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:          jobstore.EventJobSessionAssigned,
		TS:            startedAt,
		JobID:         jobID,
		TranscriptRef: "local:child-status",
	}); err != nil {
		t.Fatalf("append session assignment event: %v", err)
	}
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusFailed,
		Reason:      "exit_nonzero",
		ExitCode:    &exitCode,
		EndedAt:     &endedAt,
		OutputBytes: 128,
	}); err != nil {
		t.Fatalf("append finished event: %v", err)
	}

	ds := sess.DetailedStatus()

	if len(ds.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(ds.Jobs))
	}
	job := ds.Jobs[0]
	if job.JobID != jobID || job.JobType != string(jobstore.JobDelegate) || job.Status != string(jobstore.StatusFailed) ||
		job.Reason != "exit_nonzero" || job.TranscriptRef != "local:child-status" ||
		job.OutputBytes != 128 || job.ExitCode == nil || *job.ExitCode != exitCode {
		t.Fatalf("job status = %+v", job)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

// TestDetailedStatus_HookEvents_ExcludesDeadHooks verifies that /status's supported
// hook count reflects only hooks that can actually run: a hook whose handler type is
// unsupported (http) or whose matcher is an invalid regex is dispatch-time dead, so
// it must not be counted as a supported active hook. The legacy Hooks map (registered
// hooks per event) still counts them (Fix 4).
func TestDetailedStatus_HookEvents_ExcludesDeadHooks(t *testing.T) {
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	os.MkdirAll(metaDir, 0o755)
	os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "dead-hook-test"}`), 0o644)
	hooksDir := filepath.Join(pluginDir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	// PreToolUse: ONLY an http handler (never executes → dispatch-time dead).
	// PostToolUse: a command handler with an invalid-regex matcher (skipped at dispatch).
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{
		"hooks": {
			"PreToolUse":  [{"matcher": "*", "hooks": [{"type": "http", "url": "http://example"}]}],
			"PostToolUse": [{"matcher": "(", "hooks": [{"type": "command", "command": "echo x", "timeout": 5}]}]
		}
	}`), 0o644)

	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Neither dead hook may surface as a supported active hook.
	for _, he := range ds.HookEvents {
		if !he.Supported {
			continue
		}
		if he.Event == plugin.HookPreToolUse {
			t.Errorf("PreToolUse has only an http (unsupported-type) handler; it must not be a supported active hook (got Count=%d)", he.Count)
		}
		if he.Event == plugin.HookPostToolUse {
			t.Errorf("PostToolUse's only handler has an invalid matcher; it must not be a supported active hook (got Count=%d)", he.Count)
		}
	}
}

// TestDetailedStatus_HookEvents verifies that DetailedStatus.HookEvents lists
// supported hook events with their tier and count, and lists recognized-but-
// unsupported events with Supported=false, Count=0, Tier="reserved-placeholder".
// The legacy Hooks map is preserved for backward compatibility.
func TestDetailedStatus_HookEvents(t *testing.T) {
	// Build a plugin dir with PreToolUse (supported) and "Setup" (recognized but
	// not fired by serf — reserved-placeholder).
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	os.MkdirAll(metaDir, 0o755)
	os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "hook-diag-test"}`), 0o644)
	hooksDir := filepath.Join(pluginDir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{
		"hooks": {
			"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo ok", "timeout": 5}]}],
			"Setup":      [{"matcher": "*", "hooks": [{"type": "command", "command": "echo setup", "timeout": 5}]}]
		}
	}`), 0o644)

	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Legacy Hooks map should have PreToolUse with count ≥ 1.
	if ds.Hooks[plugin.HookPreToolUse] < 1 {
		t.Errorf("Hooks[PreToolUse] = %d, want ≥ 1", ds.Hooks[plugin.HookPreToolUse])
	}

	// HookEvents should include PreToolUse as supported/claude-compatible-subset.
	var foundSupported, foundUnsupported bool
	for _, he := range ds.HookEvents {
		switch he.Event {
		case plugin.HookPreToolUse:
			if !he.Supported {
				t.Errorf("PreToolUse: Supported = false, want true")
			}
			if he.Tier != "claude-compatible-subset" {
				t.Errorf("PreToolUse: Tier = %q, want claude-compatible-subset", he.Tier)
			}
			if he.Count < 1 {
				t.Errorf("PreToolUse: Count = %d, want ≥ 1", he.Count)
			}
			foundSupported = true
		case "Setup":
			if he.Supported {
				t.Errorf("Setup: Supported = true, want false")
			}
			if he.Tier != "reserved-placeholder" {
				t.Errorf("Setup: Tier = %q, want reserved-placeholder", he.Tier)
			}
			if he.Count != 0 {
				t.Errorf("Setup: Count = %d, want 0", he.Count)
			}
			foundUnsupported = true
		}
	}
	if !foundSupported {
		t.Error("HookEvents missing PreToolUse (supported)")
	}
	if !foundUnsupported {
		t.Error("HookEvents missing Setup (unsupported/reserved-placeholder)")
	}
}
