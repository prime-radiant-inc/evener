package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCommand_ValidFull(t *testing.T) {
	data := []byte(`---
description: Create a git commit
argument-hint: "[message]"
allowed-tools:
  - Bash(git add:*)
  - Bash(git commit:*)
model: sonnet
---
# Commit
Create a commit with message: $ARGUMENTS
`)
	cmd, err := ParseCommand(data, "commit", "test-plugin")
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	if cmd.Name != "commit" {
		t.Errorf("Name = %q, want %q", cmd.Name, "commit")
	}
	if cmd.Description != "Create a git commit" {
		t.Errorf("Description = %q", cmd.Description)
	}
	if cmd.ArgumentHint != "[message]" {
		t.Errorf("ArgumentHint = %q, want %q", cmd.ArgumentHint, "[message]")
	}
	if cmd.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", cmd.Model)
	}
	if len(cmd.AllowedTools) != 2 || cmd.AllowedTools[0] != "Bash(git add:*)" || cmd.AllowedTools[1] != "Bash(git commit:*)" {
		t.Errorf("AllowedTools = %v", cmd.AllowedTools)
	}
	if cmd.Body != "# Commit\nCreate a commit with message: $ARGUMENTS\n" {
		t.Errorf("Body = %q", cmd.Body)
	}
	if cmd.PluginName != "test-plugin" {
		t.Errorf("PluginName = %q", cmd.PluginName)
	}
}

func TestParseCommand_Minimal(t *testing.T) {
	data := []byte(`---
description: Does the minimum
---
Body here.
`)
	cmd, err := ParseCommand(data, "minimal-cmd", "p")
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	if cmd.Name != "minimal-cmd" {
		t.Errorf("Name = %q", cmd.Name)
	}
	if cmd.Description != "Does the minimum" {
		t.Errorf("Description = %q", cmd.Description)
	}
	if cmd.ArgumentHint != "" {
		t.Errorf("ArgumentHint = %q, want empty", cmd.ArgumentHint)
	}
	if cmd.Model != "" {
		t.Errorf("Model = %q, want empty", cmd.Model)
	}
	if len(cmd.AllowedTools) != 0 {
		t.Errorf("AllowedTools = %v, want empty", cmd.AllowedTools)
	}
	if cmd.Body != "Body here.\n" {
		t.Errorf("Body = %q", cmd.Body)
	}
}

// TestParseCommand_NoFrontmatter matches real Claude Code commands with no
// frontmatter at all (e.g. a bare "do X" instruction file): the whole file is
// the template body, and every metadata field defaults to empty. Real
// commands (many plugins, and ~/.claude/commands/*.md files) look exactly
// like this, so this must not error.
func TestParseCommand_NoFrontmatter(t *testing.T) {
	data := []byte("Just do the thing, no frontmatter at all.\n")
	cmd, err := ParseCommand(data, "bare", "p")
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	if cmd.Name != "bare" {
		t.Errorf("Name = %q, want %q", cmd.Name, "bare")
	}
	if cmd.Description != "" {
		t.Errorf("Description = %q, want empty", cmd.Description)
	}
	if cmd.Body != "Just do the thing, no frontmatter at all.\n" {
		t.Errorf("Body = %q", cmd.Body)
	}
}

// TestParseCommand_FrontmatterNameFieldIsIgnored covers a real-world pattern
// (seen in installed marketplace plugins) where a command's frontmatter
// includes a "name" field anyway. serf must not read it — the filename (via
// discoverPluginCommands, or the name argument here) is authoritative — so a
// frontmatter name that disagrees with the actual command name is silently
// ignored, not an override and not an error.
func TestParseCommand_FrontmatterNameFieldIsIgnored(t *testing.T) {
	data := []byte(`---
name: something-else-entirely
description: d
---
Body.
`)
	cmd, err := ParseCommand(data, "real-name", "p")
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	if cmd.Name != "real-name" {
		t.Errorf("Name = %q, want %q (the passed-in name, not the frontmatter field)", cmd.Name, "real-name")
	}
}

func TestParseCommand_AllowedToolsNotAList(t *testing.T) {
	data := []byte(`---
description: bad
allowed-tools: "not-a-list"
---
Body.
`)
	cmd, err := ParseCommand(data, "bad-tools", "p")
	if err != nil {
		t.Fatalf("ParseCommand error: %v, want no error (malformed allowed-tools degrades to none)", err)
	}
	if len(cmd.AllowedTools) != 0 {
		t.Errorf("AllowedTools = %v, want empty", cmd.AllowedTools)
	}
}

func TestParseCommand_AllowedToolsNonStringEntrySkipped(t *testing.T) {
	data := []byte(`---
description: bad
allowed-tools:
  - Bash
  - 123
  - Read
---
Body.
`)
	cmd, err := ParseCommand(data, "mixed-tools", "p")
	if err != nil {
		t.Fatalf("ParseCommand error: %v", err)
	}
	if len(cmd.AllowedTools) != 2 || cmd.AllowedTools[0] != "Bash" || cmd.AllowedTools[1] != "Read" {
		t.Errorf("AllowedTools = %v, want [Bash Read] (non-string entry skipped)", cmd.AllowedTools)
	}
}

func TestParseCommand_InvalidFrontmatter(t *testing.T) {
	data := []byte(`---
not yaml: [}
---
Body.
`)
	_, err := ParseCommand(data, "bad", "p")
	if err == nil {
		t.Fatal("expected error for invalid frontmatter YAML")
	}
}

func TestDiscoverPluginCommands(t *testing.T) {
	dir := t.TempDir()

	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeCommand := func(file, content string) {
		if err := os.WriteFile(filepath.Join(commandsDir, file+".md"), []byte(content), 0644); err != nil {
			t.Fatalf("write command %s: %v", file, err)
		}
	}

	writeCommand("cmd-a", `---
description: Command A
---
# A
`)
	writeCommand("cmd-b", `---
description: Command B
model: opus
---
# B
`)

	commands, err := discoverPluginCommands(dir, nil, "my-plugin")
	if err != nil {
		t.Fatalf("discoverPluginCommands error: %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("expected 2 commands, got %d: %v", len(commands), commands)
	}

	a, ok := commands["my-plugin:cmd-a"]
	if !ok {
		t.Fatalf("missing my-plugin:cmd-a")
	}
	if a.Name != "cmd-a" || a.Description != "Command A" {
		t.Errorf("cmd-a = %+v", a)
	}
	if a.Body != "# A\n" {
		t.Errorf("cmd-a Body = %q", a.Body)
	}

	b, ok := commands["my-plugin:cmd-b"]
	if !ok {
		t.Fatalf("missing my-plugin:cmd-b")
	}
	if b.Name != "cmd-b" || b.Model != "opus" {
		t.Errorf("cmd-b = %+v", b)
	}
}

// TestDiscoverPluginCommands_NoFrontmatterFile covers a bare, frontmatter-less
// command file discovered from disk (not just constructed via ParseCommand
// directly): its name still comes from the filename and it does not error.
func TestDiscoverPluginCommands_NoFrontmatterFile(t *testing.T) {
	dir := t.TempDir()
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "par.md"),
		[]byte("Assign two subagents to review adversarially.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	commands, err := discoverPluginCommands(dir, nil, "p")
	if err != nil {
		t.Fatalf("discoverPluginCommands error: %v", err)
	}
	cmd, ok := commands["p:par"]
	if !ok {
		t.Fatalf("expected p:par, got %v", commands)
	}
	if cmd.Body != "Assign two subagents to review adversarially.\n" {
		t.Errorf("Body = %q", cmd.Body)
	}
}

func TestDiscoverPluginCommands_NoDir(t *testing.T) {
	dir := t.TempDir()
	commands, err := discoverPluginCommands(dir, nil, "p")
	if err != nil {
		t.Fatalf("discoverPluginCommands error: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(commands))
	}
}

func TestDiscoverPluginCommands_BadCommandFile(t *testing.T) {
	dir := t.TempDir()
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "bad.md"), []byte(`---
not yaml: [}
---
`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverPluginCommands(dir, nil, "p"); err == nil {
		t.Fatal("expected error for bad command file")
	}
}

func TestDiscoverPluginCommands_IgnoresNonMarkdownAndDirs(t *testing.T) {
	dir := t.TempDir()
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(filepath.Join(commandsDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "notes.txt"), []byte("not a command"), 0644); err != nil {
		t.Fatal(err)
	}
	commands, err := discoverPluginCommands(dir, nil, "p")
	if err != nil {
		t.Fatalf("discoverPluginCommands error: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("expected 0 commands, got %d: %v", len(commands), commands)
	}
}

// TestDiscoverPluginCommands_Override covers discoverPluginCommands's override
// branch: a manifest-declared custom commands path supplements the default
// commands/ dir (mirrors TestDiscoverPluginAgents_Override).
func TestDiscoverPluginCommands_Override(t *testing.T) {
	dir := t.TempDir()
	customDir := filepath.Join(dir, "extra-commands")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "extra.md"), []byte(`---
description: Extra command
---
Body.
`), 0644); err != nil {
		t.Fatal(err)
	}

	override, err := json.Marshal("extra-commands")
	if err != nil {
		t.Fatal(err)
	}
	commands, err := discoverPluginCommands(dir, override, "p")
	if err != nil {
		t.Fatalf("discoverPluginCommands error: %v", err)
	}
	if _, ok := commands["p:extra"]; !ok {
		t.Fatalf("expected override dir command to be discovered, got %v", commands)
	}
}

func TestResolveCommand_ExactMatch(t *testing.T) {
	commands := map[string]Command{
		"plugin-a:foo": {Name: "foo", PluginName: "plugin-a"},
	}
	cmd, ok := ResolveCommand(commands, "plugin-a:foo")
	if !ok || cmd.Name != "foo" {
		t.Fatalf("ResolveCommand exact match = %+v, %v", cmd, ok)
	}
}

func TestResolveCommand_UnqualifiedMatch(t *testing.T) {
	commands := map[string]Command{
		"plugin-a:foo": {Name: "foo", PluginName: "plugin-a"},
	}
	cmd, ok := ResolveCommand(commands, "foo")
	if !ok || cmd.PluginName != "plugin-a" {
		t.Fatalf("ResolveCommand unqualified match = %+v, %v", cmd, ok)
	}
}

func TestResolveCommand_NotFound(t *testing.T) {
	commands := map[string]Command{
		"plugin-a:foo": {Name: "foo", PluginName: "plugin-a"},
	}
	if _, ok := ResolveCommand(commands, "bar"); ok {
		t.Fatal("expected no match for unknown command name")
	}
}
