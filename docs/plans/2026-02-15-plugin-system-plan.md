# Plugin System Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add 100% drop-in Claude Code plugin support to serf via `--plugin-dir <path>`.

**Architecture:** A PluginManager in `agent/` loads plugin directories, discovers
skills/agents/hooks/MCP, and integrates them into the session via namespacing and a
HookRunner that dispatches at 9 lifecycle points. See `docs/plans/2026-02-15-plugin-system-design.md`.

**Tech Stack:** Go, os/exec for command hooks, existing llm.Client for prompt hooks,
existing frontmatter package for agent/skill parsing.

**Working directory:** `/Users/jesse/prime-radiant/serf/.worktrees/plugin-system/`

---

### Task 1: Plugin Manifest Types and Parsing

**Files:**
- Create: `agent/plugin.go`
- Create: `agent/plugin_test.go`

**Context:** The plugin.json manifest is the foundation. It defines the plugin name,
metadata, and optional component path overrides. The `${CLAUDE_PLUGIN_ROOT}` variable
must be expanded in all string values.

**Step 1: Write the failing tests**

```go
// plugin_test.go
func TestParsePluginManifest(t *testing.T) {
    tests := []struct {
        name    string
        json    string
        want    PluginManifest
        wantErr bool
    }{
        {
            name: "minimal",
            json: `{"name": "my-plugin"}`,
            want: PluginManifest{Name: "my-plugin"},
        },
        {
            name: "full metadata",
            json: `{"name":"test-plugin","version":"1.0.0","description":"A test"}`,
            want: PluginManifest{Name: "test-plugin", Version: "1.0.0", Description: "A test"},
        },
        {
            name: "missing name",
            json: `{"version": "1.0.0"}`,
            wantErr: true,
        },
        {
            name: "invalid name - spaces",
            json: `{"name": "my plugin"}`,
            wantErr: true,
        },
        {
            name: "invalid name - uppercase",
            json: `{"name": "MyPlugin"}`,
            wantErr: true,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ParsePluginManifest([]byte(tt.json))
            if tt.wantErr {
                if err == nil { t.Fatal("expected error") }
                return
            }
            if err != nil { t.Fatalf("unexpected error: %v", err) }
            if got.Name != tt.want.Name { t.Errorf("Name = %q, want %q", got.Name, tt.want.Name) }
        })
    }
}

func TestValidatePluginName(t *testing.T) {
    valid := []string{"my-plugin", "a", "test-123", "a-b-c"}
    for _, name := range valid {
        if err := validatePluginName(name); err != nil {
            t.Errorf("validatePluginName(%q) = %v, want nil", name, err)
        }
    }
    invalid := []string{"", "My Plugin", "UPPER", "under_score", "-leading", "trailing-", "has space"}
    for _, name := range invalid {
        if err := validatePluginName(name); err == nil {
            t.Errorf("validatePluginName(%q) = nil, want error", name)
        }
    }
}

func TestExpandPluginRoot(t *testing.T) {
    input := `{"command": "bash ${CLAUDE_PLUGIN_ROOT}/scripts/run.sh"}`
    got := expandPluginRoot(input, "/home/user/plugins/my-plugin")
    want := `{"command": "bash /home/user/plugins/my-plugin/scripts/run.sh"}`
    if got != want { t.Errorf("got %q, want %q", got, want) }
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/plugin-system && go test ./agent/ -run TestParsePluginManifest -v`
Expected: compilation error (types don't exist yet)

**Step 3: Write the implementation**

```go
// plugin.go
package agent

import (
    "encoding/json"
    "fmt"
    "regexp"
    "strings"
)

// PluginManifest represents the .claude-plugin/plugin.json file.
type PluginManifest struct {
    Name        string   `json:"name"`
    Version     string   `json:"version,omitempty"`
    Description string   `json:"description,omitempty"`
    Author      any      `json:"author,omitempty"`
    Homepage    string   `json:"homepage,omitempty"`
    Repository  string   `json:"repository,omitempty"`
    License     string   `json:"license,omitempty"`
    Keywords    []string `json:"keywords,omitempty"`

    Commands   any `json:"commands,omitempty"`
    Agents     any `json:"agents,omitempty"`
    Hooks      any `json:"hooks,omitempty"`
    MCPServers any `json:"mcpServers,omitempty"`
}

var pluginNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func validatePluginName(name string) error {
    if name == "" {
        return fmt.Errorf("plugin name is required")
    }
    if !pluginNameRe.MatchString(name) {
        return fmt.Errorf("plugin name %q must be lowercase alphanumeric with hyphens, no leading/trailing hyphens", name)
    }
    return nil
}

func ParsePluginManifest(data []byte) (PluginManifest, error) {
    var m PluginManifest
    if err := json.Unmarshal(data, &m); err != nil {
        return m, fmt.Errorf("parsing plugin.json: %w", err)
    }
    if err := validatePluginName(m.Name); err != nil {
        return m, err
    }
    return m, nil
}

func expandPluginRoot(s string, pluginDir string) string {
    return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginDir)
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/plugin-system && go test ./agent/ -run "TestParsePlugin|TestValidatePlugin|TestExpandPlugin" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): add manifest types, parsing, and validation"
```

---

### Task 2: Plugin Loading from Directories

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

**Context:** A `LoadedPlugin` struct represents a fully loaded plugin. `LoadPlugin()`
reads the manifest from `.claude-plugin/plugin.json`, resolves component directory paths
(defaults supplemented by custom paths from manifest), and returns the loaded plugin.

**Step 1: Write the failing tests**

```go
func TestLoadPlugin(t *testing.T) {
    // Create temp plugin directory with manifest
    dir := t.TempDir()
    os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
    os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
        []byte(`{"name":"test-plugin","version":"1.0.0"}`), 0o644)

    plugin, err := LoadPlugin(dir)
    if err != nil { t.Fatalf("LoadPlugin: %v", err) }
    if plugin.Manifest.Name != "test-plugin" { t.Errorf("Name = %q", plugin.Manifest.Name) }
    if plugin.Dir != dir { t.Errorf("Dir = %q, want %q", plugin.Dir, dir) }
}

func TestLoadPlugin_MissingManifest(t *testing.T) {
    dir := t.TempDir()
    _, err := LoadPlugin(dir)
    if err == nil { t.Fatal("expected error for missing manifest") }
}

func TestLoadPlugins_DuplicateName(t *testing.T) {
    dir1, dir2 := t.TempDir(), t.TempDir()
    for _, d := range []string{dir1, dir2} {
        os.MkdirAll(filepath.Join(d, ".claude-plugin"), 0o755)
        os.WriteFile(filepath.Join(d, ".claude-plugin", "plugin.json"),
            []byte(`{"name":"dupe"}`), 0o644)
    }
    _, err := LoadPlugins([]string{dir1, dir2})
    if err == nil { t.Fatal("expected error for duplicate name") }
}

func TestResolveComponentDirs(t *testing.T) {
    dir := t.TempDir()
    // Create default agents/ dir
    os.MkdirAll(filepath.Join(dir, "agents"), 0o755)
    // Create custom dir from manifest
    os.MkdirAll(filepath.Join(dir, "extra-agents"), 0o755)

    m := PluginManifest{Name: "test", Agents: "./extra-agents"}
    dirs := resolveComponentDirs(dir, "agents", m.Agents)
    // Should include both default and custom
    if len(dirs) != 2 { t.Errorf("got %d dirs, want 2", len(dirs)) }
}
```

**Step 2: Run tests to verify they fail**

**Step 3: Implement LoadPlugin, LoadPlugins, resolveComponentDirs**

`LoadPlugin(dir)`: reads manifest, resolves absolute dir path (EvalSymlinks),
returns `LoadedPlugin` with Dir set. Does NOT load components yet (separate tasks).

`LoadPlugins(dirs)`: calls LoadPlugin for each, checks for duplicate names.

`resolveComponentDirs(pluginDir, defaultName, manifestOverride)`: returns list of
absolute paths. Always includes `<pluginDir>/<defaultName>/` if it exists.
If manifest specifies a string or []string of custom paths, resolves them relative
to pluginDir and adds them.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): add plugin directory loading and duplicate detection"
```

---

### Task 3: CLI Flag and SessionConfig Plumbing

**Files:**
- Modify: `cmd/serf/main.go`
- Modify: `cmd/serf/run.go`
- Modify: `agent/session.go` (add PluginDirs to SessionConfig)
- Modify: `cmd/serf/run_test.go` (if flag parsing tests exist)

**Context:** Wire `--plugin-dir` from CLI through to SessionConfig.

**Step 1: Write the failing test**

```go
// In run_test.go or a new test
func TestRunConfig_PluginDirs(t *testing.T) {
    cfg := runConfig{
        pluginDirs: []string{"/path/to/plugin1", "/path/to/plugin2"},
    }
    // Verify the field exists and is passed through
    if len(cfg.pluginDirs) != 2 { t.Fatal("pluginDirs not set") }
}
```

**Step 2: Run test to verify fail (field doesn't exist)**

**Step 3: Implement**

In `main.go`: add `var pluginDirs stringSliceFlag` and `flag.Var(&pluginDirs, "plugin-dir", ...)`.
In `runConfig`: add `pluginDirs []string`.
In `SessionConfig`: add `PluginDirs []string`.
In `run()`: pass `cfg.pluginDirs` → `sessionCfg.PluginDirs`.
Update `flag.Usage` with `--plugin-dir` documentation.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): add --plugin-dir CLI flag and SessionConfig plumbing"
```

---

### Task 4: Plugin Skills Discovery and Namespacing

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

**Context:** Discover skills from each plugin's skills/ directory (reuse existing
`scanSkillsDir`), then namespace them as `plugin-name:skill-name`.

**Step 1: Write the failing tests**

```go
func TestDiscoverPluginSkills(t *testing.T) {
    dir := t.TempDir()
    // Create plugin with a skill
    os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
    os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
        []byte(`{"name":"my-plugin"}`), 0o644)
    os.MkdirAll(filepath.Join(dir, "skills", "my-skill"), 0o755)
    os.WriteFile(filepath.Join(dir, "skills", "my-skill", "SKILL.md"),
        []byte("---\nname: my-skill\ndescription: A test skill\n---\nSkill body"),
        0o644)

    plugin, _ := LoadPlugin(dir)
    skills := discoverPluginSkills(plugin)
    // Key should be namespaced
    if _, ok := skills["my-plugin:my-skill"]; !ok {
        t.Errorf("expected key 'my-plugin:my-skill', got keys: %v", mapKeys(skills))
    }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement `discoverPluginSkills`**

Uses `resolveComponentDirs` for skills dirs, calls `scanSkillsDir` for each,
then namespaces the results by prepending `plugin.Manifest.Name + ":"`.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): discover and namespace skills from plugins"
```

---

### Task 5: Tool Name Mapping

**Files:**
- Create: `agent/plugin_tools.go`
- Create: `agent/plugin_tools_test.go`

**Context:** Claude Code plugins reference tools by Claude Code names (Read, Write,
Bash, etc.). Serf uses canonical names (read_file, write_file, shell, etc.). We need
bidirectional mapping for: (1) agent frontmatter tool lists, (2) hook input tool names,
(3) hook matchers.

**Step 1: Write the failing tests**

```go
func TestMapClaudeToolName(t *testing.T) {
    tests := map[string]string{
        "Read": "read_file", "Write": "write_file", "Edit": "edit_file",
        "Bash": "shell", "Grep": "grep", "Glob": "glob",
        "Task": "spawn_agent", "WebFetch": "web_fetch", "WebSearch": "web_search",
        "NotebookEdit": "notebook_edit",
        "unknown_tool": "unknown_tool", // passthrough
        "mcp__server__tool": "mcp__server__tool", // passthrough
    }
    for input, want := range tests {
        if got := MapClaudeToolName(input); got != want {
            t.Errorf("MapClaudeToolName(%q) = %q, want %q", input, got, want)
        }
    }
}

func TestMapSerfToolNameToClaude(t *testing.T) {
    tests := map[string]string{
        "read_file": "Read", "write_file": "Write", "edit_file": "Edit",
        "shell": "Bash", "grep": "Grep", "glob": "Glob",
        "spawn_agent": "Task", "web_fetch": "WebFetch", "web_search": "WebSearch",
        "unknown": "unknown", // passthrough
    }
    for input, want := range tests {
        if got := MapSerfToolNameToClaude(input); got != want {
            t.Errorf("MapSerfToolNameToClaude(%q) = %q, want %q", input, got, want)
        }
    }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

Two maps and two functions. `MapClaudeToolName(name) string` and
`MapSerfToolNameToClaude(name) string`. Unknown names pass through unchanged.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): add bidirectional Claude/serf tool name mapping"
```

---

### Task 6: Plugin Agent Discovery

**Files:**
- Create: `agent/plugin_agents.go`
- Create: `agent/plugin_agents_test.go`

**Context:** Parse `.md` files from plugin `agents/` directories. Each file has YAML
frontmatter (name, description, model, color, tools) and a markdown body (system prompt).
Tool names in the `tools` array are mapped from Claude Code names to serf canonical names.

**Step 1: Write the failing tests**

```go
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
    if err != nil { t.Fatalf("parsePluginAgent: %v", err) }
    if agent.Name != "code-reviewer" { t.Errorf("Name = %q", agent.Name) }
    if agent.Model != "inherit" { t.Errorf("Model = %q", agent.Model) }
    // Tools should be mapped to serf names
    wantTools := []string{"read_file", "grep", "shell"}
    if !slices.Equal(agent.Tools, wantTools) {
        t.Errorf("Tools = %v, want %v", agent.Tools, wantTools)
    }
    if !strings.Contains(agent.SystemPrompt, "code review specialist") {
        t.Error("SystemPrompt missing body content")
    }
}

func TestParsePluginAgent_MissingRequired(t *testing.T) {
    // Missing name
    _, err := parsePluginAgent([]byte("---\ndescription: foo\nmodel: inherit\ncolor: blue\n---\nbody"), "p")
    if err == nil { t.Fatal("expected error for missing name") }
}

func TestDiscoverPluginAgents(t *testing.T) {
    dir := t.TempDir()
    os.MkdirAll(filepath.Join(dir, "agents"), 0o755)
    os.WriteFile(filepath.Join(dir, "agents", "reviewer.md"),
        []byte("---\nname: reviewer\ndescription: reviews\nmodel: inherit\ncolor: blue\n---\nYou review."),
        0o644)

    agents, err := discoverPluginAgents(dir, nil, "my-plugin")
    if err != nil { t.Fatal(err) }
    if _, ok := agents["my-plugin:reviewer"]; !ok {
        t.Errorf("expected 'my-plugin:reviewer', got %v", mapKeys(agents))
    }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

`PluginAgent` struct. `parsePluginAgent(data, pluginName)` uses `frontmatter.Parse`,
extracts required fields, maps tool names via `MapClaudeToolName`, returns agent with
body as SystemPrompt.

`discoverPluginAgents(pluginDir, manifestAgentPaths, pluginName)` scans agent directories,
parses each `.md` file, namespaces results.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): discover and parse plugin agent definitions"
```

---

### Task 7: Hook Config Parsing

**Files:**
- Create: `agent/plugin_hooks.go`
- Create: `agent/plugin_hooks_test.go`

**Context:** Parse hook configurations from three sources: `hooks/hooks.json` (wrapper
format with `"hooks"` key), direct format (events at top level), and inline in
plugin.json. Produce a flat list of `RegisteredHook` structs per event.

**Step 1: Write the failing tests**

```go
func TestParsePluginHooks_WrapperFormat(t *testing.T) {
    data := []byte(`{
        "description": "test hooks",
        "hooks": {
            "PreToolUse": [{
                "matcher": "Write|Edit",
                "hooks": [
                    {"type": "command", "command": "bash validate.sh", "timeout": 30}
                ]
            }]
        }
    }`)
    hooks, err := ParsePluginHooks(data, "/plugin/dir", "my-plugin")
    if err != nil { t.Fatal(err) }
    pre := hooks[HookPreToolUse]
    if len(pre) != 1 { t.Fatalf("got %d PreToolUse hooks, want 1", len(pre)) }
    if pre[0].Matcher != "Write|Edit" { t.Errorf("Matcher = %q", pre[0].Matcher) }
    if pre[0].Type != "command" { t.Errorf("Type = %q", pre[0].Type) }
    if pre[0].Timeout != 30 { t.Errorf("Timeout = %d", pre[0].Timeout) }
}

func TestParsePluginHooks_DirectFormat(t *testing.T) {
    data := []byte(`{
        "SessionStart": [{
            "matcher": "*",
            "hooks": [{"type": "command", "command": "echo hello"}]
        }]
    }`)
    hooks, err := ParsePluginHooks(data, "/dir", "test")
    if err != nil { t.Fatal(err) }
    if len(hooks[HookSessionStart]) != 1 { t.Fatal("expected 1 SessionStart hook") }
}

func TestParsePluginHooks_ExpandsPluginRoot(t *testing.T) {
    data := []byte(`{
        "hooks": {
            "PreToolUse": [{
                "matcher": "*",
                "hooks": [{"type": "command", "command": "bash ${CLAUDE_PLUGIN_ROOT}/run.sh"}]
            }]
        }
    }`)
    hooks, err := ParsePluginHooks(data, "/my/plugin", "test")
    if err != nil { t.Fatal(err) }
    if hooks[HookPreToolUse][0].Command != "bash /my/plugin/run.sh" {
        t.Errorf("Command = %q", hooks[HookPreToolUse][0].Command)
    }
}

func TestParsePluginHooks_PromptType(t *testing.T) {
    data := []byte(`{
        "hooks": {
            "Stop": [{
                "matcher": "*",
                "hooks": [{"type": "prompt", "prompt": "Check if done: $TOOL_INPUT"}]
            }]
        }
    }`)
    hooks, err := ParsePluginHooks(data, "/dir", "test")
    if err != nil { t.Fatal(err) }
    if hooks[HookStop][0].Type != "prompt" { t.Error("expected prompt type") }
    if hooks[HookStop][0].Prompt == "" { t.Error("expected prompt text") }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

Types: `HookEvent` string constants for all 9 events. `RegisteredHook` struct.
`ParsePluginHooks(data, pluginDir, pluginName)` detects format (wrapper vs direct)
by checking for `"hooks"` key. Expands `${CLAUDE_PLUGIN_ROOT}`. Sets default timeouts
(60s command, 30s prompt).

Also: `discoverPluginHooks(pluginDir, manifestHooksPath, pluginName)` reads from
`hooks/hooks.json` or the manifest's hooks field.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): parse hook configurations in wrapper and direct formats"
```

---

### Task 8: Command Hook Execution

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_test.go`

**Context:** Execute a command hook: spawn a subprocess with environment variables,
pipe hook input JSON to stdin, enforce timeout, parse exit code and output.

**Step 1: Write the failing tests**

```go
func TestExecuteCommandHook(t *testing.T) {
    hook := RegisteredHook{
        Type:      "command",
        Command:   "cat",  // echoes stdin back
        Timeout:   5,
        PluginDir: t.TempDir(),
    }
    input := HookInput{
        SessionID:     "test-123",
        CWD:           "/tmp",
        HookEventName: "PreToolUse",
        ToolName:      "Write",
    }
    result, err := executeCommandHook(context.Background(), hook, input)
    if err != nil { t.Fatal(err) }
    if result.ExitCode != 0 { t.Errorf("ExitCode = %d", result.ExitCode) }
    // stdout should contain the JSON input
    if !strings.Contains(result.Stdout, "test-123") {
        t.Errorf("stdout missing session_id: %q", result.Stdout)
    }
}

func TestExecuteCommandHook_Timeout(t *testing.T) {
    hook := RegisteredHook{
        Type:    "command",
        Command: "sleep 60",
        Timeout: 1, // 1 second
    }
    _, err := executeCommandHook(context.Background(), hook, HookInput{})
    if err == nil { t.Fatal("expected timeout error") }
}

func TestExecuteCommandHook_ExitCode2(t *testing.T) {
    hook := RegisteredHook{
        Type:    "command",
        Command: "bash -c 'echo error >&2; exit 2'",
        Timeout: 5,
    }
    result, err := executeCommandHook(context.Background(), hook, HookInput{})
    // Exit 2 is a blocking error, not a Go error
    if err != nil { t.Fatal(err) }
    if result.ExitCode != 2 { t.Errorf("ExitCode = %d, want 2", result.ExitCode) }
    if !strings.Contains(result.Stderr, "error") { t.Error("missing stderr") }
}

func TestExecuteCommandHook_Environment(t *testing.T) {
    dir := t.TempDir()
    hook := RegisteredHook{
        Type:      "command",
        Command:   "bash -c 'echo $CLAUDE_PLUGIN_ROOT'",
        Timeout:   5,
        PluginDir: dir,
    }
    result, err := executeCommandHook(context.Background(), hook, HookInput{CWD: "/work"})
    if err != nil { t.Fatal(err) }
    if !strings.Contains(result.Stdout, dir) {
        t.Errorf("CLAUDE_PLUGIN_ROOT not in output: %q", result.Stdout)
    }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

`HookInput` struct with JSON tags matching Claude Code format. `HookResult` struct
with Stdout, Stderr, ExitCode. `executeCommandHook(ctx, hook, input)` marshals input
to JSON, creates `exec.CommandContext` with timeout, sets env vars
(`CLAUDE_PROJECT_DIR`, `CLAUDE_PLUGIN_ROOT`), pipes stdin, captures stdout/stderr.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): implement command hook execution with env vars and timeout"
```

---

### Task 9: Prompt Hook Execution

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_test.go`

**Context:** Execute a prompt hook by calling the LLM with the prompt text after
variable substitution. The LLM response is the hook output.

**Step 1: Write the failing tests**

```go
func TestSubstituteHookVariables(t *testing.T) {
    prompt := "Check this: $TOOL_INPUT and result: $TOOL_RESULT"
    input := HookInput{
        ToolName:  "Write",
        ToolInput: map[string]any{"file_path": "/test.go"},
    }
    got := substituteHookVariables(prompt, input)
    if !strings.Contains(got, `"file_path"`) {
        t.Errorf("$TOOL_INPUT not substituted: %q", got)
    }
    // $TOOL_RESULT should be empty/null since no result in input
    if strings.Contains(got, "$TOOL_RESULT") {
        t.Error("$TOOL_RESULT not substituted")
    }
}

func TestExecutePromptHook(t *testing.T) {
    // Use a mock LLM client that returns a canned response
    client := &mockPromptHookClient{
        response: "approve",
    }
    hook := RegisteredHook{
        Type:   "prompt",
        Prompt: "Should we allow $TOOL_INPUT?",
    }
    input := HookInput{ToolInput: map[string]any{"file_path": "/test"}}
    result, err := executePromptHook(context.Background(), client, "test-model", hook, input)
    if err != nil { t.Fatal(err) }
    if result.Stdout != "approve" { t.Errorf("got %q", result.Stdout) }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

`substituteHookVariables(prompt, input)`: replaces `$TOOL_INPUT`, `$TOOL_RESULT`,
`$USER_PROMPT`, `$TOOL_NAME` with JSON-encoded values from input.

`executePromptHook(ctx, client, model, hook, input)`: calls `client.Complete()` with
the substituted prompt, parses response text as hook output. Uses hook's Model field
if set, otherwise the provided default model.

Define a `PromptHookClient` interface (`Complete(ctx, Request) (Response, error)`) so
we can mock it in tests without needing a real LLM.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): implement prompt hook execution with variable substitution"
```

---

### Task 10: HookRunner — Matching, Dispatch, Aggregation

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_test.go`

**Context:** The `HookRunner` aggregates hooks from all plugins, matches them against
tool names using regex, runs matching hooks in parallel, and aggregates results.
For PreToolUse, any "deny" blocks the tool. For Stop, any "block" prevents stopping.

**Step 1: Write the failing tests**

```go
func TestHookRunner_MatcherRegex(t *testing.T) {
    runner := NewHookRunner()
    runner.Add(HookPreToolUse, RegisteredHook{Matcher: "Write|Edit", Type: "command", Command: "echo match"})
    runner.Add(HookPreToolUse, RegisteredHook{Matcher: "mcp__.*", Type: "command", Command: "echo mcp"})

    // "Write" matches first hook
    matched := runner.matchHooks(HookPreToolUse, "Write")
    if len(matched) != 1 { t.Fatalf("got %d, want 1", len(matched)) }

    // "mcp__server__tool" matches second hook
    matched = runner.matchHooks(HookPreToolUse, "mcp__server__tool")
    if len(matched) != 1 { t.Fatalf("got %d, want 1", len(matched)) }

    // "Read" matches neither
    matched = runner.matchHooks(HookPreToolUse, "Read")
    if len(matched) != 0 { t.Fatalf("got %d, want 0", len(matched)) }
}

func TestHookRunner_WildcardMatcher(t *testing.T) {
    runner := NewHookRunner()
    runner.Add(HookStop, RegisteredHook{Matcher: "*", Type: "command", Command: "echo stop"})
    matched := runner.matchHooks(HookStop, "anything")
    if len(matched) != 1 { t.Fatal("wildcard should match") }
}

func TestHookRunner_PreToolUse_Deny(t *testing.T) {
    runner := NewHookRunner()
    // Hook that returns deny
    runner.Add(HookPreToolUse, RegisteredHook{
        Matcher: "*",
        Type:    "command",
        Command: `bash -c 'echo "{\"hookSpecificOutput\":{\"permissionDecision\":\"deny\"}}"'`,
    })
    result := runner.RunPreToolUse(context.Background(), HookInput{ToolName: "Write"})
    if !result.Denied { t.Error("expected deny") }
}

func TestHookRunner_ParallelExecution(t *testing.T) {
    runner := NewHookRunner()
    // Two hooks that each sleep briefly — should run in parallel
    runner.Add(HookSessionStart, RegisteredHook{Matcher: "*", Type: "command", Command: "sleep 0.1 && echo hook1"})
    runner.Add(HookSessionStart, RegisteredHook{Matcher: "*", Type: "command", Command: "sleep 0.1 && echo hook2"})

    start := time.Now()
    runner.RunSessionStart(context.Background(), HookInput{})
    elapsed := time.Since(start)
    // Parallel: should take ~100ms, not ~200ms
    if elapsed > 180*time.Millisecond {
        t.Errorf("hooks ran sequentially? elapsed=%v", elapsed)
    }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

`HookRunner` struct with `hooks map[HookEvent][]RegisteredHook`.
`NewHookRunner()`, `Add(event, hook)`, `matchHooks(event, toolName)`.
Matcher: `"*"` becomes `".*"`, then `regexp.MatchString`.

Dispatch methods per event type:
- `RunPreToolUse(ctx, input) PreToolUseResult` — returns Denied bool, SystemMessages, UpdatedInput
- `RunPostToolUse(ctx, input) HookRunResult` — returns SystemMessages
- `RunStop(ctx, input) StopResult` — returns Blocked bool, Reason
- `RunSessionStart(ctx, input) HookRunResult`
- `RunSessionEnd(ctx, input)`
- `RunUserPromptSubmit(ctx, input) HookRunResult`
- `RunPreCompact(ctx, input) HookRunResult`
- `RunSubagentStop(ctx, input) StopResult`

Each dispatch: filter matching hooks, run all in parallel (goroutines + WaitGroup),
collect results, aggregate.

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): implement HookRunner with matching, parallel dispatch, and result aggregation"
```

---

### Task 11: Hook Output Parsing

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_test.go`

**Context:** Parse hook output (stdout for command hooks, LLM response for prompt hooks)
into structured results. Handle both plain text and structured JSON output.

**Step 1: Write the failing tests**

```go
func TestParseHookOutput_PlainText(t *testing.T) {
    result := parseHookOutput("just some text", 0)
    if result.SystemMessage != "just some text" { t.Errorf("got %q", result.SystemMessage) }
    if result.Denied { t.Error("should not be denied") }
}

func TestParseHookOutput_StructuredJSON(t *testing.T) {
    json := `{"continue": true, "systemMessage": "Looks good", "suppressOutput": true}`
    result := parseHookOutput(json, 0)
    if result.SystemMessage != "Looks good" { t.Errorf("got %q", result.SystemMessage) }
    if result.SuppressOutput != true { t.Error("suppressOutput should be true") }
}

func TestParseHookOutput_PreToolUseDeny(t *testing.T) {
    json := `{"hookSpecificOutput": {"permissionDecision": "deny"}, "systemMessage": "Blocked"}`
    result := parseHookOutput(json, 0)
    if !result.Denied { t.Error("should be denied") }
}

func TestParseHookOutput_StopBlock(t *testing.T) {
    json := `{"decision": "block", "reason": "Tests not run"}`
    result := parseHookOutput(json, 0)
    if !result.Blocked { t.Error("should be blocked") }
    if result.BlockReason != "Tests not run" { t.Errorf("reason = %q", result.BlockReason) }
}

func TestParseHookOutput_ExitCode2(t *testing.T) {
    result := parseHookOutput("error message", 2)
    if !result.IsError { t.Error("exit 2 should be error") }
}
```

**Step 2-5: Implement, test, commit**

```bash
git commit -m "feat(plugin): parse hook output (plain text, structured JSON, deny/block)"
```

---

### Task 12: Session Hook Integration

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/plugin.go`
- Create: `agent/plugin_integration_test.go`

**Context:** Wire the HookRunner into the Session at all 9 lifecycle points. This is
the largest integration task. The session loads plugins during `initSessionState()`,
merges skills and agents, builds the HookRunner, and calls it at the right points.

**Step 1: Write the failing tests**

```go
func TestSession_PluginHooks_SessionStartEnd(t *testing.T) {
    // Create a plugin with a SessionStart hook that writes a marker file
    pluginDir := t.TempDir()
    setupTestPlugin(t, pluginDir, "test-plugin", map[string]any{
        "SessionStart": []any{map[string]any{
            "matcher": "*",
            "hooks": []any{map[string]any{
                "type": "command",
                "command": fmt.Sprintf("touch %s/started", pluginDir),
            }},
        }},
    })

    sess := newTestSession(t, SessionConfig{PluginDirs: []string{pluginDir}})
    defer sess.Close()

    // SessionStart hook should have fired during init
    if _, err := os.Stat(filepath.Join(pluginDir, "started")); err != nil {
        t.Error("SessionStart hook did not fire")
    }
}

func TestSession_PluginHooks_PreToolUse_Deny(t *testing.T) {
    pluginDir := t.TempDir()
    setupTestPlugin(t, pluginDir, "blocker", map[string]any{
        "PreToolUse": []any{map[string]any{
            "matcher": "Write",
            "hooks": []any{map[string]any{
                "type":    "command",
                "command": `bash -c 'echo "{\"hookSpecificOutput\":{\"permissionDecision\":\"deny\"}}" '`,
            }},
        }},
    })

    sess := newTestSession(t, SessionConfig{PluginDirs: []string{pluginDir}})
    defer sess.Close()

    // Attempt a write_file tool call — should be blocked by hook
    call := llm.ToolCallData{Name: "write_file", ID: "call-1",
        Arguments: json.RawMessage(`{"file_path":"/tmp/test","content":"x"}`)}
    result := sess.execTool(context.Background(), call)
    if !result.IsError { t.Error("write should have been denied by hook") }
}
```

**Step 2: Run tests to verify fail**

**Step 3: Implement**

Add to `Session` struct:
```go
hookRunner  *HookRunner
pluginAgents map[string]PluginAgent
```

In `initSessionState()`, after skills discovery:
```go
if len(s.cfg.PluginDirs) > 0 {
    if err := s.initPlugins(); err != nil {
        return fmt.Errorf("plugin initialization: %w", err)
    }
}
```

`initPlugins()`:
1. `LoadPlugins(s.cfg.PluginDirs)` — load manifests
2. For each plugin: discover skills (merge namespaced), discover agents (store),
   discover hooks (add to HookRunner), collect MCP configs
3. Fire SessionStart hooks
4. Return collected MCP configs for initMCP to use

Modify `execTool()`:
- Before execution: `hookRunner.RunPreToolUse(ctx, input)` — if denied, return error result
- After execution: `hookRunner.RunPostToolUse(ctx, input)` — inject systemMessages via Steer

Modify `processOneInput()`:
- After UserInput emit: `hookRunner.RunUserPromptSubmit(ctx, input)`
- Before MaybeCompact: `hookRunner.RunPreCompact(ctx, input)`
- Before result return (communicate): `hookRunner.RunStop(ctx, input)` — if blocked, continue loop

Modify `Close()`:
- Before SESSION_END emit: `hookRunner.RunSessionEnd(ctx, input)`

Subagent completion: `hookRunner.RunSubagentStop(ctx, input)`

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git commit -m "feat(plugin): wire HookRunner into session at all lifecycle points"
```

---

### Task 13: Plugin MCP Integration

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`
- Modify: `agent/plugin.go`

**Context:** Plugin `.mcp.json` configs merge into MCP discovery. Plugin MCP servers
use the existing `mcp__plugin_<pluginname>_<servername>__<toolname>` namespacing.

**Step 1: Write the failing tests**

```go
func TestDiscoverPluginMCPConfigs(t *testing.T) {
    pluginDir := t.TempDir()
    os.WriteFile(filepath.Join(pluginDir, ".mcp.json"),
        []byte(`{"my-server": {"command": "echo", "args": ["hello"]}}`), 0o644)

    configs, err := discoverPluginMCPConfigs(pluginDir, nil, "test-plugin")
    if err != nil { t.Fatal(err) }
    // Server name should be prefixed for plugin namespacing
    if _, ok := configs["plugin_test-plugin_my-server"]; !ok {
        t.Errorf("expected namespaced server, got %v", mapKeys(configs))
    }
}

func TestDiscoverPluginMCPConfigs_ExpandsRoot(t *testing.T) {
    pluginDir := t.TempDir()
    os.WriteFile(filepath.Join(pluginDir, ".mcp.json"),
        []byte(`{"srv": {"command": "${CLAUDE_PLUGIN_ROOT}/server"}}`), 0o644)

    configs, err := discoverPluginMCPConfigs(pluginDir, nil, "p")
    if err != nil { t.Fatal(err) }
    if configs["plugin_p_srv"].Command != pluginDir+"/server" {
        t.Errorf("Command = %q", configs["plugin_p_srv"].Command)
    }
}
```

**Step 2-5: Implement, test, commit**

```bash
git commit -m "feat(plugin): integrate plugin MCP configs with existing discovery"
```

---

### Task 14: Plugin Settings

**Files:**
- Create: `agent/plugin_settings.go`
- Create: `agent/plugin_settings_test.go`

**Context:** Parse `.claude/<plugin-name>.local.md` files for per-project plugin config.
YAML frontmatter becomes key-value settings, markdown body is available as content.

**Step 1: Write the failing tests**

```go
func TestLoadPluginSettings(t *testing.T) {
    dir := t.TempDir()
    os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
    os.WriteFile(filepath.Join(dir, ".claude", "my-plugin.local.md"),
        []byte("---\nenabled: true\nstrict_mode: false\nmax_retries: 3\n---\n\n# Config\n\nExtra context here."),
        0o644)

    settings, err := LoadPluginSettings(dir, "my-plugin")
    if err != nil { t.Fatal(err) }
    if settings.Frontmatter["enabled"] != true { t.Error("enabled should be true") }
    if !strings.Contains(settings.Body, "Extra context") { t.Error("body missing") }
}

func TestLoadPluginSettings_Missing(t *testing.T) {
    settings, err := LoadPluginSettings(t.TempDir(), "nonexistent")
    if err != nil { t.Fatal(err) }
    if settings != nil { t.Error("should be nil for missing file") }
}
```

**Step 2-5: Implement, test, commit**

```bash
git commit -m "feat(plugin): add plugin settings (.local.md) loading"
```

---

### Task 15: spawn_agent Enhancement for Plugin Agents

**Files:**
- Modify: `agent/subagents.go`
- Modify: `agent/session.go`
- Create: `agent/plugin_agents_integration_test.go`

**Context:** When `spawn_agent` is called, check if the task/model references a known
plugin agent. If so, apply the agent's system prompt, model override, and tool restrictions
to the subagent session.

**Step 1: Write the failing tests**

```go
func TestSpawnAgent_PluginAgent(t *testing.T) {
    // Create a plugin with an agent definition
    pluginDir := t.TempDir()
    setupTestPluginWithAgent(t, pluginDir, "my-plugin", "reviewer",
        "You are a code reviewer.", "inherit", []string{"read_file", "grep"})

    sess := newTestSession(t, SessionConfig{PluginDirs: []string{pluginDir}})
    defer sess.Close()

    // The session should know about the plugin agent
    agent, ok := sess.pluginAgents["my-plugin:reviewer"]
    if !ok { t.Fatal("plugin agent not found") }
    if agent.SystemPrompt != "You are a code reviewer." {
        t.Errorf("SystemPrompt = %q", agent.SystemPrompt)
    }
}
```

**Step 2-5: Implement, test, commit**

Add `agent_type` parameter to `spawn_agent` tool definition. When set, look up
the agent in `s.pluginAgents`, create the subagent with:
- `UserInstructionOverride` set to the agent's SystemPrompt
- Model override via `s.profile.WithModel(agent.Model)` (if not "inherit")
- Tool restriction by removing tools not in agent.Tools from the subagent registry

```bash
git commit -m "feat(plugin): enhance spawn_agent for plugin-defined agent types"
```

---

### Task 16: System Prompt Additions

**Files:**
- Modify: `agent/profile.go`
- Modify: `agent/profile_test.go`

**Context:** Include plugin agents and namespaced skills in the system prompt so the
LLM knows they're available.

**Step 1: Write the failing tests**

```go
func TestBuildSystemPrompt_IncludesPluginAgents(t *testing.T) {
    agents := map[string]PluginAgent{
        "my-plugin:reviewer": {Name: "reviewer", Description: "Reviews code", PluginName: "my-plugin"},
    }
    prompt := buildSystemPromptWithPluginAgents(basePrompt, agents)
    if !strings.Contains(prompt, "my-plugin:reviewer") {
        t.Error("prompt should include plugin agent name")
    }
    if !strings.Contains(prompt, "Reviews code") {
        t.Error("prompt should include agent description")
    }
}
```

**Step 2-5: Implement, test, commit**

Extend `BuildSystemPrompt` (or the call site in `processOneInput`) to include a
section listing available plugin agents with their namespaced names and descriptions.
Plugin skills are already included via the skills list.

```bash
git commit -m "feat(plugin): include plugin agents in system prompt"
```

---

### Task 17: Events and Observability

**Files:**
- Modify: `agent/events.go`
- Modify: `cmd/serf/run.go`

**Context:** Add event types for plugin lifecycle and hook execution. Update the
human-readable event formatter.

**Step 1: Write the failing tests**

```go
func TestPluginEvents(t *testing.T) {
    // Verify event types exist and data structs are correct
    ev := SessionEvent{Kind: EventPluginLoaded, Data: PluginLoadedData{Name: "test", Version: "1.0"}}
    if ev.Kind != EventPluginLoaded { t.Error("wrong kind") }
    d := ev.Data.(PluginLoadedData)
    if d.Name != "test" { t.Error("wrong name") }
}
```

**Step 2-5: Implement, test, commit**

Add `EventPluginLoaded`, `EventHookStart`, `EventHookEnd` to events.go.
Add `PluginLoadedData`, `HookStartData`, `HookEndData` structs.
Update `drainEventsHuman` in run.go to format these events.

```bash
git commit -m "feat(plugin): add plugin and hook events for observability"
```

---

### Task 18: End-to-End Integration Test

**Files:**
- Create: `agent/plugin_e2e_test.go`

**Context:** A comprehensive test that creates a plugin directory with all component
types (skills, agents, hooks, settings), loads it into a session, and verifies
everything works together.

**Step 1: Write the test**

```go
func TestPlugin_EndToEnd(t *testing.T) {
    pluginDir := setupFullTestPlugin(t, "e2e-plugin")
    // Plugin has:
    // - skills/test-skill/SKILL.md
    // - agents/helper.md (with tools: ["Read", "Grep"])
    // - hooks/hooks.json (SessionStart command hook that writes marker)
    // - .claude/<plugin>.local.md settings

    sess := newTestSession(t, SessionConfig{
        PluginDirs: []string{pluginDir},
    })
    defer sess.Close()

    // 1. Skills namespaced correctly
    if _, ok := sess.skills["e2e-plugin:test-skill"]; !ok {
        t.Error("plugin skill not found")
    }

    // 2. Agents registered
    if _, ok := sess.pluginAgents["e2e-plugin:helper"]; !ok {
        t.Error("plugin agent not found")
    }

    // 3. Agent tools mapped to serf names
    agent := sess.pluginAgents["e2e-plugin:helper"]
    if !slices.Contains(agent.Tools, "read_file") {
        t.Error("agent tools not mapped")
    }

    // 4. SessionStart hook fired (marker file exists)
    if _, err := os.Stat(filepath.Join(pluginDir, "started")); err != nil {
        t.Error("SessionStart hook did not fire")
    }

    // 5. Events emitted
    // (drain events and check for EventPluginLoaded)
}
```

**Step 2: Run test, verify it passes**

**Step 3: Commit**

```bash
git commit -m "test(plugin): add end-to-end plugin integration test"
```

---

### Task 19: Run Full Test Suite and Fix

**Files:** Various (whatever needs fixing)

**Context:** Run the full test suite to catch any regressions. Fix any issues.

**Step 1: Run all tests**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/plugin-system && go test ./... -count=1`
Expected: All tests pass

**Step 2: Fix any failures**

**Step 3: Run tests again to confirm**

**Step 4: Commit any fixes**

```bash
git commit -m "fix: address test regressions from plugin system integration"
```
