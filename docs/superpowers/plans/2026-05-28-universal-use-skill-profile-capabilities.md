# Universal use_skill and Data-Driven Profiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `use_skill` available in every provider profile and refactor provider profile construction around clear capability-group composition.

**Architecture:** Keep the refactor in `agent/profile.go` for this change. Introduce purpose-named tool capability groups and a small profile builder that applies shared defaults, resolves effort levels, copies mutable config, and expands capability groups into deterministic tool definitions. Provider-specific constructors keep their existing catalog and `WithModel` special cases, but delegate common `baseProfile` assembly to the builder.

**Tech Stack:** Go, existing `llm.ToolDefinition`, existing `ProviderProfile`/`baseProfile` types, existing Go test suite.

---

## Files

- Modify: `agent/profile.go`
  - Add capability group types/helpers.
  - Add `profileSpec` and `buildBaseProfile` helper.
  - Refactor constructors to use the helper.
  - Ensure every profile capability composition includes `workflow`, which contains `use_skill`.
- Modify: `agent/profile_test.go`
  - Update exact tool list expectations.
  - Add universal `use_skill` coverage.
  - Preserve profile comparison tests.
- Modify: `agent/session_skills_test.go`
  - Replace OpenAI-specific “read_file for skills/no use_skill” expectations with universal `use_skill` expectations.
  - Add OpenAI session-level `use_skill` executor coverage.
- Potentially modify: `agent/builtin_skills_test.go`
  - Update any stale OpenAI skill prompt expectations.

---

### Task 1: Add failing tests for universal `use_skill`

**Files:**
- Modify: `agent/profile_test.go`
- Modify: `agent/session_skills_test.go`
- Potentially modify: `agent/builtin_skills_test.go`

- [ ] **Step 1: Update OpenAI exact tool-list expectation**

In `agent/profile_test.go`, update the OpenAI exact list in `TestProviderProfiles_ToolListExact` so it expects `use_skill`.

Expected OpenAI list:

```go
assertToolListExact(t, p, []string{
    "read_file",
    "apply_patch",
    "write_file",
    "exec_command",
    "grep_files",
    "list_dir",
    "spawn_agent",
    "resume_agent",
    "wait",
    "close_agent",
    "task_list",
    "web_fetch",
    "communicate",
    "use_skill",
})
```

- [ ] **Step 2: Add a matrix test that every profile exposes `use_skill`**

Add this test near the existing profile tool-list tests in `agent/profile_test.go`:

```go
func TestProviderProfiles_AllIncludeUseSkill(t *testing.T) {
    profiles := []ProviderProfile{
        NewOpenAIProfile("gpt-5.2"),
        NewAnthropicProfile("claude-test"),
        NewGeminiProfile("gemini-test"),
        NewMiniMaxProfile("MiniMax-M2.7"),
        NewOpenRouterAnthropicProfile("anthropic/claude-test"),
        NewOpenAICompatProfile("openrouter", "openai/gpt-test", 0),
        NewOpenAICompatProfile("kimi", "kimi-test", 0),
        NewOpenAICompatProfile("glm", "glm-test", 0),
        NewOpenAICompatProfile("ollama", "llama3", 0),
    }
    for _, p := range profiles {
        t.Run(p.ID(), func(t *testing.T) {
            assertHasTool(t, p, "use_skill")
        })
    }
}
```

- [ ] **Step 3: Replace the OpenAI no-use-skill test**

In `agent/session_skills_test.go`, replace `TestOpenAI_NoUseSkillTool` with:

```go
func TestOpenAI_IncludesUseSkillTool(t *testing.T) {
    p := NewOpenAIProfile("gpt-5.2")
    assertHasTool(t, p, "use_skill")
}
```

If `assertHasTool` is not available in this test file, use an inline loop:

```go
func TestOpenAI_IncludesUseSkillTool(t *testing.T) {
    p := NewOpenAIProfile("gpt-5.2")
    for _, td := range p.ToolDefinitions() {
        if td.Name == "use_skill" {
            return
        }
    }
    t.Fatal("OpenAI profile should include use_skill tool definition")
}
```

- [ ] **Step 4: Update OpenAI skill prompt expectations**

In `agent/session_skills_test.go`, find the test that currently asserts OpenAI should use `read_file` for skills. Update it to assert the universal skill-tool path:

```go
if !strings.Contains(capturedSystem, "Load a skill by calling use_skill with its name") {
    t.Error("OpenAI system prompt should instruct model to use use_skill for skills")
}
if !strings.Contains(capturedSystem, "greet: Greeting skill") {
    t.Error("OpenAI system prompt missing greet skill entry")
}
if !strings.Contains(capturedSystem, "["+filepath.Dir(skillPath)+"]") {
    t.Error("OpenAI system prompt should list the skill directory for use_skill profiles")
}
```

Use the actual local variables already present in the test. If the current test does not expose `skillPath`, derive the expected directory from the temp root and skill name.

Remove assertions that OpenAI should not have `use_skill`.

- [ ] **Step 5: Add OpenAI session-level executor coverage**

Add a test in `agent/session_skills_test.go` showing the OpenAI session wires and executes `use_skill`:

```go
func TestOpenAIUseSkillToolExecutes(t *testing.T) {
    root := t.TempDir()
    initGitRepo(t, root)
    writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nUse greeting style.\n")

    c := llm.NewClient()
    c.Register(&fakeAdapter{name: "openai"})

    sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(root), SessionConfig{})
    if err != nil {
        t.Fatalf("NewSession: %v", err)
    }
    defer sess.Close()

    tool := sess.reg.Get("use_skill")
    if tool == nil {
        t.Fatal("OpenAI session registry missing use_skill executor")
    }

    result := sess.reg.Execute(context.Background(), NewLocalExecutionEnvironment(root), llm.ToolCallData{
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
```

Add imports if needed: `encoding/json` and `context` may already exist in the file.

- [ ] **Step 6: Run failing tests**

Run:

```bash
go test ./agent -run 'TestProviderProfiles_ToolListExact|TestProviderProfiles_AllIncludeUseSkill|TestOpenAI_.*UseSkill|TestOpenAIUseSkillToolExecutes|TestUseSkill'
```

Expected before implementation: at least OpenAI-related tests fail because `NewOpenAIProfile` does not include `use_skill` and OpenAI skill prompts still render the read-file path.

---

### Task 2: Add capability groups and profile builder

**Files:**
- Modify: `agent/profile.go`

- [ ] **Step 1: Add capability group types**

Near the `baseProfile` type in `agent/profile.go`, add:

```go
type toolCapability string

const (
    capabilityFiles            toolCapability = "files"
    capabilityCodexEditing     toolCapability = "codex_editing"
    capabilityExactEditing     toolCapability = "exact_editing"
    capabilityShellSearch      toolCapability = "shell_search"
    capabilityDirectoryListing toolCapability = "directory_listing"
    capabilityAgentControl     toolCapability = "agent_control"
    capabilityWorkflow         toolCapability = "workflow"
    capabilityWebFetch         toolCapability = "web_fetch"
    capabilityWebSearch        toolCapability = "web_search"
)
```

- [ ] **Step 2: Add `profileSpec`**

Near the capability definitions, add:

```go
type profileSpec struct {
    id              string
    model           string
    parallel        bool
    contextWindow   int
    docFiles        []string
    reasoning       bool
    streaming       bool
    webSearch       bool
    defaultTimeout  int
    knowledgeCutoff string
    defaultEfforts  []string
    providerOpts    map[string]any
    toolNameMap     map[string]string
    capabilities    []toolCapability
}
```

- [ ] **Step 3: Add copy helpers for mutable data**

Add helpers in `agent/profile.go`:

```go
func cloneStringSlice(in []string) []string {
    if in == nil {
        return nil
    }
    return append([]string(nil), in...)
}

func cloneStringMap(in map[string]string) map[string]string {
    if in == nil {
        return nil
    }
    out := make(map[string]string, len(in))
    for k, v := range in {
        out[k] = v
    }
    return out
}

func cloneAnyMap(in map[string]any) map[string]any {
    if in == nil {
        return nil
    }
    out := make(map[string]any, len(in))
    for k, v := range in {
        out[k] = cloneAnyValue(v)
    }
    return out
}

func cloneAnyValue(v any) any {
    switch x := v.(type) {
    case map[string]any:
        return cloneAnyMap(x)
    case []map[string]any:
        out := make([]map[string]any, len(x))
        for i := range x {
            out[i] = cloneAnyMap(x[i])
        }
        return out
    case []any:
        out := make([]any, len(x))
        for i := range x {
            out[i] = cloneAnyValue(x[i])
        }
        return out
    case []string:
        return append([]string(nil), x...)
    default:
        return v
    }
}
```

If `cloneSchemaValue` can be reused safely, prefer that over duplicating clone logic, but keep the helper names clear for profile config.

- [ ] **Step 4: Add `toolDefinitionsForCapabilities`**

Add:

```go
func toolDefinitionsForCapabilities(capabilities []toolCapability, efforts []string) []llm.ToolDefinition {
    var defs []llm.ToolDefinition
    seen := make(map[string]bool)
    add := func(items ...llm.ToolDefinition) {
        for _, td := range items {
            if seen[td.Name] {
                continue
            }
            seen[td.Name] = true
            defs = append(defs, td)
        }
    }

    for _, capability := range capabilities {
        switch capability {
        case capabilityFiles:
            add(defReadFile(), defWriteFile())
        case capabilityCodexEditing:
            add(defApplyPatch())
        case capabilityExactEditing:
            add(defEditFile())
        case capabilityShellSearch:
            add(defShell(), defGrep(), defGlob())
        case capabilityDirectoryListing:
            add(defListDir())
        case capabilityAgentControl:
            add(defSpawnAgent(), defSendInput(), defWait(), defCloseAgent())
        case capabilityWorkflow:
            add(defTaskList(efforts), defCommunicate(), defUseSkill())
        case capabilityWebFetch:
            add(defWebFetch())
        case capabilityWebSearch:
            add(defWebSearch())
        }
    }
    return defs
}
```

The `seen` map makes capability composition set-like. The input order is only canonical output order; it is not semantic.

- [ ] **Step 5: Add canonical capability slices**

Add:

```go
var (
    openAICodexCapabilities = []toolCapability{
        capabilityFiles,
        capabilityCodexEditing,
        capabilityShellSearch,
        capabilityAgentControl,
        capabilityWorkflow,
        capabilityWebFetch,
    }
    anthropicStyleCapabilities = []toolCapability{
        capabilityFiles,
        capabilityExactEditing,
        capabilityShellSearch,
        capabilityAgentControl,
        capabilityWorkflow,
        capabilityWebFetch,
    }
    geminiStyleCapabilities = []toolCapability{
        capabilityFiles,
        capabilityExactEditing,
        capabilityShellSearch,
        capabilityDirectoryListing,
        capabilityAgentControl,
        capabilityWorkflow,
        capabilityWebFetch,
        capabilityWebSearch,
    }
)
```

- [ ] **Step 6: Add `buildBaseProfile`**

Add:

```go
func buildBaseProfile(spec profileSpec) baseProfile {
    model := strings.TrimSpace(spec.model)
    efforts := resolveEffortLevels(model, spec.defaultEfforts)

    parallel := spec.parallel
    reasoning := spec.reasoning
    streaming := spec.streaming
    defaultTimeout := spec.defaultTimeout
    if defaultTimeout == 0 {
        defaultTimeout = 120_000
    }

    return baseProfile{
        id:              spec.id,
        model:           model,
        parallel:        parallel,
        contextWindow:   spec.contextWindow,
        docFiles:        cloneStringSlice(spec.docFiles),
        reasoning:       reasoning,
        streaming:       streaming,
        webSearch:       spec.webSearch,
        defaultTimeout:  defaultTimeout,
        knowledgeCutoff: spec.knowledgeCutoff,
        effortLevels:    cloneStringSlice(efforts),
        providerOpts:    cloneAnyMap(spec.providerOpts),
        toolNameMap:     cloneStringMap(spec.toolNameMap),
        toolDefs:        toolDefinitionsForCapabilities(spec.capabilities, efforts),
    }
}
```

Note: constructors must explicitly set `parallel`, `reasoning`, and `streaming` to true for profiles that currently have them. Do not infer true from zero values unless the struct is changed to pointer booleans. Keeping explicit booleans avoids accidentally changing a future profile that intentionally disables a capability.

- [ ] **Step 7: Run compile-focused test**

Run:

```bash
go test ./agent -run TestProviderProfiles_AddPurposeToEveryToolSchema
```

Expected: it may still fail until constructors use the new builder, but the package should compile after this task. If it does not compile, fix type/import mistakes before continuing.

---

### Task 3: Refactor core profile constructors to use the builder

**Files:**
- Modify: `agent/profile.go`

- [ ] **Step 1: Refactor `NewOpenAIProfile`**

Replace the direct `&baseProfile{...}` in `NewOpenAIProfile` with:

```go
func NewOpenAIProfile(model string) ProviderProfile {
    return &baseProfile{baseProfile: buildBaseProfile(profileSpec{
        id:              "openai",
        model:           model,
        parallel:        true,
        contextWindow:   400_000,
        docFiles:        []string{"AGENTS.md", ".codex/instructions.md"},
        reasoning:       true,
        streaming:       true,
        webSearch:       true,
        defaultTimeout:  120_000,
        knowledgeCutoff: "2025-06-01",
        defaultEfforts:  []string{"low", "medium", "high", "xhigh"},
        providerOpts: map[string]any{
            "openai": map[string]any{
                "parallel_tool_calls": true,
            },
        },
        toolNameMap: map[string]string{
            "shell": "exec_command",
            "grep":  "grep_files",
            "glob":  "list_dir",
        },
        capabilities: openAICodexCapabilities,
    })}
}
```

Important: `baseProfile` is not embedded in itself. The exact return should be:

```go
bp := buildBaseProfile(profileSpec{ ... })
return &bp
```

Use that form.

- [ ] **Step 2: Refactor `NewAnthropicProfile`**

Keep the `[1m]` context/provider option computation. Build the embedded base with:

```go
bp := buildBaseProfile(profileSpec{
    id:              "anthropic",
    model:           model,
    parallel:        true,
    contextWindow:   ctxWindow,
    docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
    reasoning:       true,
    streaming:       true,
    webSearch:       true,
    defaultTimeout:  120_000,
    knowledgeCutoff: "2025-04-01",
    defaultEfforts:  []string{"low", "medium", "high", "max"},
    providerOpts:    anthropicProviderOpts(has1M),
    capabilities:    anthropicStyleCapabilities,
})
return &anthropicProfile{baseProfile: bp}
```

- [ ] **Step 3: Refactor `NewGeminiProfile`**

Use `geminiStyleCapabilities`, preserve provider opts and name map:

```go
bp := buildBaseProfile(profileSpec{
    id:              "gemini",
    model:           model,
    parallel:        true,
    contextWindow:   1_000_000,
    docFiles:        []string{"GEMINI.md", "AGENTS.md"},
    reasoning:       true,
    streaming:       true,
    webSearch:       true,
    defaultTimeout:  120_000,
    knowledgeCutoff: "2025-03-01",
    defaultEfforts:  []string{"low", "medium", "high"},
    providerOpts:    existingGeminiProviderOpts,
    toolNameMap: map[string]string{
        "shell":    "run_shell_command",
        "grep":     "grep_search",
        "list_dir": "list_directory",
    },
    capabilities: geminiStyleCapabilities,
})
return &bp
```

Use the current inline Gemini provider options exactly; do not change safety settings.

- [ ] **Step 4: Refactor `NewMiniMaxProfile`**

Use `anthropicStyleCapabilities`, preserve MiniMax metadata:

```go
bp := buildBaseProfile(profileSpec{
    id:              "minimax",
    model:           model,
    parallel:        true,
    contextWindow:   204_800,
    docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
    reasoning:       true,
    streaming:       true,
    defaultTimeout:  120_000,
    knowledgeCutoff: "2025-06-01",
    defaultEfforts:  []string{"low", "medium", "high", "max"},
    capabilities:    anthropicStyleCapabilities,
})
return &bp
```

- [ ] **Step 5: Run focused constructor tests**

Run:

```bash
go test ./agent -run 'TestProviderProfiles_ToolListExact|TestProviderProfiles_AllIncludeUseSkill|TestMiniMaxProfile_ToolListExact|TestProviderProfile_WithModel|TestAnthropicProfile_1M|TestGeminiProfile'
```

Expected after this task: OpenAI may pass universal `use_skill`; exact-list tests for OpenAI should pass after expected update. Any failure indicates changed constructor data and should be fixed before continuing.

---

### Task 4: Refactor OpenRouter Anthropic and OpenAI-compatible constructors

**Files:**
- Modify: `agent/profile.go`
- Modify tests only if exact expected lists need `use_skill` additions.

- [ ] **Step 1: Refactor `NewOpenRouterAnthropicProfile`**

Keep all catalog resolution logic unchanged. Replace the final `&baseProfile{...}` with:

```go
bp := buildBaseProfile(profileSpec{
    id:              "openrouter-anthropic",
    model:           model,
    parallel:        true,
    contextWindow:   contextWindow,
    docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
    reasoning:       true,
    streaming:       true,
    webSearch:       ws,
    defaultTimeout:  120_000,
    knowledgeCutoff: "2025-06-01",
    defaultEfforts:  defaultEfforts,
    providerOpts: map[string]any{
        "anthropic": map[string]any{
            "max_tokens": 16384,
        },
    },
    capabilities: anthropicStyleCapabilities,
})
bp.effortLevels = cloneStringSlice(efforts)
bp.toolDefs = toolDefinitionsForCapabilities(anthropicStyleCapabilities, efforts)
return &bp
```

If this override feels awkward, add a second builder helper that accepts already-resolved efforts:

```go
func buildBaseProfileWithEfforts(spec profileSpec, efforts []string) baseProfile
```

Prefer the helper if it avoids mutating after construction.

- [ ] **Step 2: Refactor `NewOpenAICompatProfile`**

Find `NewOpenAICompatProfile` and keep its catalog/default resolution. It should use `openAICodexCapabilities` unless current code uses a different editing/tool family. Preserve:

- provider id
- context window
- knowledge cutoff
- reasoning/streaming/webSearch settings
- provider options
- tool name maps
- default effort levels
- model lookup behavior

Ensure its capability list includes `capabilityWorkflow`, so `use_skill` is included.

- [ ] **Step 3: Run OpenRouter/OpenAI-compatible focused tests**

Run:

```bash
go test ./agent -run 'TestOpenRouter|TestOpenAICompat|TestProviderProfile_WithModel|TestProviderProfiles_AllIncludeUseSkill|TestProviderProfiles_AddPurposeToEveryToolSchema'
```

Expected: all pass. If exact tool lists fail, update expected lists only for the new `use_skill` addition unless the refactor accidentally changed more.

---

### Task 5: Update skill prompt tests and behavior expectations

**Files:**
- Modify: `agent/session_skills_test.go`
- Modify: `agent/builtin_skills_test.go` if stale OpenAI expectations remain.

- [ ] **Step 1: Search for stale OpenAI no-use-skill/read-file expectations**

Run:

```bash
rg "OpenAI.*use_skill|OpenAI.*read_file|NoUseSkill|should not have use_skill|uses read_file for skills|read_file for skills" agent
```

- [ ] **Step 2: Update stale expectations**

For every stale OpenAI-specific test:

- Replace “OpenAI should not have `use_skill`” with “OpenAI should have `use_skill`”.
- Replace “OpenAI should load skills by `read_file`” with “OpenAI should load skills by calling `use_skill`”.
- Preserve tests that verify `read_file` itself exists for OpenAI; only remove skill-loading fallback expectations.

- [ ] **Step 3: Run skill tests**

Run:

```bash
go test ./agent -run 'Test.*Skill|TestOpenAI.*UseSkill'
```

Expected: all pass.

---

### Task 6: Verify full relevant package and commit

**Files:**
- No planned code edits unless verification finds issues.

- [ ] **Step 1: Run focused profile tests**

Run:

```bash
go test ./agent -run 'TestProviderProfiles|TestProviderProfile|TestOpenAI|TestOpenRouter|TestMiniMax|TestGemini|TestAnthropic|Test.*Skill'
```

Expected: pass.

- [ ] **Step 2: Run broader relevant package**

Run:

```bash
go test ./agent
```

Expected: pass.

- [ ] **Step 3: Inspect diff**

Run:

```bash
git diff --stat
git diff -- agent/profile.go agent/profile_test.go agent/session_skills_test.go agent/builtin_skills_test.go
```

Verify:

- every profile includes `use_skill`
- profile constructors are easier to compare by capability groups
- provider-specific special cases are preserved
- no unrelated files changed

- [ ] **Step 4: Commit**

Run:

```bash
git status --short
git add agent/profile.go agent/profile_test.go agent/session_skills_test.go agent/builtin_skills_test.go
git commit -m "refactor: compose provider profile tools by capability"
```

If `agent/builtin_skills_test.go` was not modified, omit it from `git add`.

---

## Self-review

- Spec coverage: universal `use_skill` is covered by exact OpenAI expected list, all-profile matrix test, OpenAI prompt expectations, and OpenAI executor coverage.
- Refactor coverage: capability-group composition and shared profile builder are covered by constructor refactor tasks and exact-list/profile tests.
- Special cases: Anthropic `[1m]`, OpenRouter Anthropic catalog resolution, and OpenAI-compatible catalog/name-map logic are explicitly preserved.
- Placeholder scan: no TBD/TODO placeholders.
- Scope: focused on provider profile construction and skill availability only.
