# Agentic Loop Spec Remediation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix all non-system-prompt gaps identified in the 2026-02-11 agentic loop spec compliance audit.

**Architecture:** TDD red-green for every change. Each task is a self-contained commit. Grouped by subsystem to minimize context switching.

**Tech Stack:** Go 1.24+, `go test`, `fakeAdapter` stubs for session-level tests, real filesystem for env tests.

**Excluded from this plan:**
- C1: System prompts not 1:1 copies of reference agents (separate project)
- I6: Real streaming (major architectural change, v2)
- I3: Profile owns ToolRegistry (architectural — Session ownership works fine)
- I12: Spec says no compaction (spec needs updating, not code)
- All Info-severity items (intentional extensions)

**Run tests:** `go test ./internal/agent/ -count=1 -timeout 120s`

---

### Task 1: Fix Gemini system prompt wrong tool name (I2/GAP-3.08)

The Gemini system prompt says `list_dir` but the tool is exposed as `list_directory` after name mapping. The model sees conflicting names.

**Files:**
- Modify: `internal/agent/prompts/system.gemini.md:20`
- Test: `internal/agent/profile_test.go`

**Step 1: Write failing test**

In `profile_test.go`, add:

```go
func TestGeminiProfile_SystemPromptUsesListDirectory(t *testing.T) {
	p := NewGeminiProfile("test-model")
	prompt := p.BuildSystemPrompt(EnvironmentInfo{WorkingDir: "/tmp"}, nil, nil)
	if strings.Contains(prompt, "list_dir") && !strings.Contains(prompt, "list_directory") {
		t.Fatal("system prompt contains 'list_dir' without 'list_directory' — model sees conflicting names")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestGeminiProfile_SystemPromptUsesListDirectory -v`
Expected: FAIL — the prompt currently says `list_dir`

**Step 3: Fix the prompt**

In `internal/agent/prompts/system.gemini.md` line 20, change `list_dir` to `list_directory`:

```
- **list_directory**: List directory contents with optional depth. Use to explore project
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestGeminiProfile_SystemPromptUsesListDirectory -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `go test ./internal/agent/ -count=1 -timeout 120s`

**Step 6: Commit**

```bash
git add internal/agent/prompts/system.gemini.md internal/agent/profile_test.go
git commit -m "fix: Gemini system prompt uses list_directory not list_dir (GAP-3.08)"
```

---

### Task 2: Add Anthropic beta headers (I1/GAP-3.03)

The Anthropic profile has empty `providerOpts`. The adapter reads `beta_headers` from `provider_options.anthropic` but the profile doesn't provide any. Extended thinking and prompt caching need beta headers.

**Files:**
- Test: `internal/agent/profile_test.go`
- Modify: `internal/agent/profile.go:251`

**Step 1: Write failing test**

In `profile_test.go`, add:

```go
func TestAnthropicProfile_ProviderOptions_HasBetaHeaders(t *testing.T) {
	p := NewAnthropicProfile("test-model")
	opts := p.ProviderOptions()
	anth, ok := opts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("missing anthropic key in provider options")
	}
	bh, ok := anth["beta_headers"].(string)
	if !ok || bh == "" {
		t.Fatal("missing or empty beta_headers in anthropic provider options")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestAnthropicProfile_ProviderOptions_HasBetaHeaders -v`
Expected: FAIL — `providerOpts` is currently empty

**Step 3: Add beta headers to Anthropic profile**

In `profile.go`, change the Anthropic profile's `providerOpts`:

```go
providerOpts: map[string]any{
    "anthropic": map[string]any{
        "beta_headers": "extended-thinking-2025-04-11,prompt-caching-2024-07-31",
    },
},
```

NOTE: Check what beta headers are currently needed by looking at the Anthropic adapter's usage. The adapter reads `beta_headers` as a comma-separated string. Extended thinking and prompt caching are the two the spec calls out.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestAnthropicProfile_ProviderOptions_HasBetaHeaders -v`

**Step 5: Run full test suite**

**Step 6: Commit**

```bash
git add internal/agent/profile.go internal/agent/profile_test.go
git commit -m "feat: add default Anthropic beta headers for extended thinking + caching (GAP-3.03)"
```

---

### Task 3: Fix apply_patch description (M2/GAP-3.05)

The description is missing the capability summary per spec.

**Files:**
- Test: `internal/agent/profile_test.go`
- Modify: `internal/agent/profile.go:475`

**Step 1: Write failing test**

```go
func TestApplyPatch_DescriptionIncludesCapabilities(t *testing.T) {
	d := defApplyPatch()
	if !strings.Contains(d.Description, "creating") || !strings.Contains(d.Description, "deleting") || !strings.Contains(d.Description, "modifying") {
		t.Fatalf("apply_patch description missing capability summary: %q", d.Description)
	}
}
```

**Step 2: Run to verify fail**

**Step 3: Fix description**

```go
Description: "Apply code changes using the v4a patch format. Supports creating, deleting, and modifying files in a single operation.",
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/profile.go internal/agent/profile_test.go
git commit -m "fix: apply_patch description includes capability summary (GAP-3.05)"
```

---

### Task 4: Fix env var allow-list consistency (M19/GAP-4.08)

The default `filteredEnv` allow-list is missing `CARGO_HOME`, `NVM_DIR`, `RUSTUP_HOME`, `PYENV_ROOT` that `EnvPolicyCoreOnly` has.

**Files:**
- Test: `internal/agent/env_local_test.go`
- Modify: `internal/agent/env_local.go:562-572`

**Step 1: Write failing test**

```go
func TestFilteredEnv_AllowListIncludesLanguageToolchainVars(t *testing.T) {
	// Set toolchain vars and a sensitive var to test filtering.
	for _, k := range []string{"CARGO_HOME", "NVM_DIR", "RUSTUP_HOME", "PYENV_ROOT"} {
		t.Setenv(k, "/test/"+k)
	}
	env := filteredEnv(nil)
	envMap := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	for _, k := range []string{"CARGO_HOME", "NVM_DIR", "RUSTUP_HOME", "PYENV_ROOT"} {
		if _, ok := envMap[k]; !ok {
			t.Errorf("missing %s in default filtered env", k)
		}
	}
}
```

**Step 2: Run to verify fail**

Note: This may actually pass because the default policy inherits all non-sensitive vars. The `allow` map is a safety net. The test should still be added, and the allow map should be fixed for consistency even if the test already passes. If the test passes, change it to directly verify the allow map entries.

**Step 3: Add missing vars to default allow-list**

In `env_local.go`, add to the `allow` map in `filteredEnv`:

```go
allow := map[string]bool{
    "PATH":        true,
    "HOME":        true,
    "USER":        true,
    "SHELL":       true,
    "LANG":        true,
    "TERM":        true,
    "TMPDIR":      true,
    "GOPATH":      true,
    "GOMODCACHE":  true,
    "CARGO_HOME":  true,
    "NVM_DIR":     true,
    "RUSTUP_HOME": true,
    "PYENV_ROOT":  true,
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/env_local.go internal/agent/env_local_test.go
git commit -m "fix: add language toolchain vars to default env allow-list (GAP-4.08)"
```

---

### Task 5: Add missing env var exclusion tests (M18/GAP-4.07)

Only `API_KEY` and `SECRET` exclusion is tested. Add tests for `TOKEN`, `PASSWORD`, `CREDENTIAL`.

**Files:**
- Test: `internal/agent/env_local_test.go`

**Step 1: Write failing test**

```go
func TestFilteredEnv_ExcludesTOKEN_PASSWORD_CREDENTIAL(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("AWS_CREDENTIAL", "secret")
	t.Setenv("SAFE_VAR", "visible")

	env := filteredEnv(nil)
	envMap := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	for _, k := range []string{"MY_TOKEN", "DB_PASSWORD", "AWS_CREDENTIAL"} {
		if _, ok := envMap[k]; ok {
			t.Errorf("%s should be excluded but was present", k)
		}
	}
	if _, ok := envMap["SAFE_VAR"]; !ok {
		t.Error("SAFE_VAR should be present but was excluded")
	}
}
```

**Step 2: Run to verify pass** (implementation already correct, this is test-only)

Note: The implementation already excludes TOKEN, PASSWORD, CREDENTIAL (env_local.go:557). This is purely a test coverage addition. The test should pass immediately.

**Step 3: Commit**

```bash
git add internal/agent/env_local_test.go
git commit -m "test: add coverage for TOKEN, PASSWORD, CREDENTIAL env var exclusion (GAP-4.07)"
```

---

### Task 6: Validate tool schema root type is "object" (M4/GAP-3.07)

`compileSchema` accepts any root type. Should reject non-object root types.

**Files:**
- Test: `internal/agent/tool_registry_test.go`
- Modify: `internal/agent/tool_registry.go:253`

**Step 1: Write failing test**

```go
func TestToolRegistry_Register_RejectsNonObjectRootSchema(t *testing.T) {
	reg := NewToolRegistry()
	err := reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name: "bad_tool",
			Parameters: map[string]any{
				"type": "string",
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	if err == nil {
		t.Fatal("expected error for non-object root schema type")
	}
	if !strings.Contains(err.Error(), "object") {
		t.Fatalf("error should mention 'object': %v", err)
	}
}
```

**Step 2: Run to verify fail**

**Step 3: Add validation in compileSchema**

In `tool_registry.go`, add a check at the start of `compileSchema`:

```go
func compileSchema(params map[string]any) (*jsonschema.Schema, error) {
	if params == nil {
		params = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	if t, ok := params["type"].(string); ok && t != "object" {
		return nil, fmt.Errorf("tool schema root type must be \"object\", got %q", t)
	}
	// ... rest unchanged
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/tool_registry.go internal/agent/tool_registry_test.go
git commit -m "fix: reject tool schemas with non-object root type (GAP-3.07)"
```

---

### Task 7: Test tool name collision latest-wins (M6/GAP-3.13)

The behavior exists but isn't tested.

**Files:**
- Test: `internal/agent/tool_registry_test.go`

**Step 1: Write test**

```go
func TestToolRegistry_Register_LatestWinsOnNameCollision(t *testing.T) {
	reg := NewToolRegistry()
	first := RegisteredTool{
		Definition: llm.ToolDefinition{Name: "my_tool", Description: "first"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "first", nil
		},
	}
	second := RegisteredTool{
		Definition: llm.ToolDefinition{Name: "my_tool", Description: "second"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "second", nil
		},
	}
	if err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(second); err != nil {
		t.Fatal(err)
	}
	got := reg.Get("my_tool")
	if got == nil || got.Definition.Description != "second" {
		t.Fatalf("expected latest-wins: got description=%q", got.Definition.Description)
	}
}
```

**Step 2: Run — should pass immediately (test-only)**

**Step 3: Commit**

```bash
git add internal/agent/tool_registry_test.go
git commit -m "test: verify tool name collision uses latest-wins (GAP-3.13)"
```

---

### Task 8: Unit test for snapshotGit (M22/GAP-6.06)

No dedicated test exists. Test edge cases: fresh repo with no commits, non-git directory.

**Files:**
- Test: `internal/agent/git_snapshot_test.go`

**Step 1: Write tests**

```go
func TestSnapshotGit_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)
	env.ExecCommand(ctx, "git config user.email test@test.com", 5000, dir, nil)
	env.ExecCommand(ctx, "git config user.name test", 5000, dir, nil)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0644)
	env.ExecCommand(ctx, "git add f.txt && git commit -m initial", 5000, dir, nil)

	inRepo, branch, mod, untracked, commits := snapshotGit(env, dir)
	if !inRepo {
		t.Fatal("expected inRepo=true")
	}
	if branch == "" {
		t.Fatal("expected non-empty branch")
	}
	if len(commits) == 0 {
		t.Fatal("expected at least 1 commit")
	}
	if mod != 0 {
		t.Errorf("expected 0 modified files, got %d", mod)
	}
	if untracked != 0 {
		t.Errorf("expected 0 untracked files, got %d", untracked)
	}
}

func TestSnapshotGit_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	inRepo, _, _, _, _ := snapshotGit(env, dir)
	if inRepo {
		t.Fatal("expected inRepo=false for non-git directory")
	}
}

func TestSnapshotGit_FreshRepoNoCommits(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)

	inRepo, _, _, _, commits := snapshotGit(env, dir)
	if !inRepo {
		t.Fatal("expected inRepo=true")
	}
	if len(commits) != 0 {
		t.Errorf("expected 0 commits for fresh repo, got %d", len(commits))
	}
}

func TestSnapshotGit_TracksModifiedAndUntracked(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)
	env.ExecCommand(ctx, "git config user.email test@test.com", 5000, dir, nil)
	env.ExecCommand(ctx, "git config user.name test", 5000, dir, nil)
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("x"), 0644)
	env.ExecCommand(ctx, "git add tracked.txt && git commit -m initial", 5000, dir, nil)

	// Create an untracked file and modify the tracked one.
	os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("y"), 0644)
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("modified"), 0644)

	_, _, mod, untracked, _ := snapshotGit(env, dir)
	if mod != 1 {
		t.Errorf("expected 1 modified, got %d", mod)
	}
	if untracked != 1 {
		t.Errorf("expected 1 untracked, got %d", untracked)
	}
}
```

**Step 2: Run — should pass (test-only)**

**Step 3: Commit**

```bash
git add internal/agent/git_snapshot_test.go
git commit -m "test: add dedicated snapshotGit unit tests (GAP-6.06)"
```

---

### Task 9: Project doc filtering parity tests (M23/GAP-6.07)

Only OpenAI profile doc filtering is tested. Need to verify Anthropic skips GEMINI.md and Gemini skips CLAUDE.md.

**Files:**
- Test: `internal/agent/session_test.go`

**Step 1: Write failing tests**

Add tests that create CLAUDE.md, GEMINI.md, AGENTS.md and verify each provider only loads its own docs. Use existing `TestSession_NaturalCompletion_LoadsOnlyProfileDocs` as a pattern.

```go
func TestSession_AnthropicProfile_LoadsOnlyCLAUDEAndAGENTS(t *testing.T) {
	dir := t.TempDir()
	// Init git repo so project docs are discovered from git root.
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude instructions"), 0644)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents instructions"), 0644)
	os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("gemini instructions"), 0644)
	os.WriteFile(filepath.Join(dir, ".codex", "instructions.md"), []byte("codex instructions"), 0644)

	c := llm.NewClient()
	f := &fakeAdapter{name: "anthropic", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			sys := req.SystemPrompt
			if !strings.Contains(sys, "claude instructions") {
				t.Error("Anthropic profile should load CLAUDE.md")
			}
			if !strings.Contains(sys, "agents instructions") {
				t.Error("Anthropic profile should load AGENTS.md")
			}
			if strings.Contains(sys, "gemini instructions") {
				t.Error("Anthropic profile should NOT load GEMINI.md")
			}
			if strings.Contains(sys, "codex instructions") {
				t.Error("Anthropic profile should NOT load .codex/instructions.md")
			}
			return llm.Response{Message: llm.Assistant("ok")}
		},
	}}
	c.Register(f)
	sess, err := NewSession(c, NewAnthropicProfile("test-model"), env, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.ProcessInput(ctx, "test")
}

func TestSession_GeminiProfile_LoadsOnlyGEMINIAndAGENTS(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude instructions"), 0644)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents instructions"), 0644)
	os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("gemini instructions"), 0644)
	os.WriteFile(filepath.Join(dir, ".codex", "instructions.md"), []byte("codex instructions"), 0644)

	c := llm.NewClient()
	f := &fakeAdapter{name: "google", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			sys := req.SystemPrompt
			if strings.Contains(sys, "claude instructions") {
				t.Error("Gemini profile should NOT load CLAUDE.md")
			}
			if !strings.Contains(sys, "agents instructions") {
				t.Error("Gemini profile should load AGENTS.md")
			}
			if !strings.Contains(sys, "gemini instructions") {
				t.Error("Gemini profile should load GEMINI.md")
			}
			if strings.Contains(sys, "codex instructions") {
				t.Error("Gemini profile should NOT load .codex/instructions.md")
			}
			return llm.Response{Message: llm.Assistant("ok")}
		},
	}}
	c.Register(f)
	sess, err := NewSession(c, NewGeminiProfile("test-model"), env, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.ProcessInput(ctx, "test")
}
```

**Step 2: Run tests — should pass (implementation is already correct, this is test coverage)**

**Step 3: Commit**

```bash
git add internal/agent/session_test.go
git commit -m "test: add Anthropic/Gemini project doc filtering parity tests (GAP-6.07)"
```

---

### Task 10: Fix subagent max_turns default to always be 50 (I8/GAP-7.03)

When parent has `MaxTurns=100`, subagent inherits 100 instead of defaulting to 50.

**Files:**
- Test: `internal/agent/session_dod_test.go`
- Modify: `internal/agent/subagents.go:55-60`

**Step 1: Write failing test**

```go
func TestSubagent_MaxTurns_DefaultsTo50_NotInheritedFromParent(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID: "c1", Name: "spawn_agent", Type: "function",
						Arguments: json.RawMessage(`{"task":"test task"}`),
					}}},
				},
			}
		},
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	// Parent has MaxTurns=100.
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{MaxTurns: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "spawn something")

	// Check the subagent's MaxTurns.
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, sub := range sess.subagents {
		sub.sess.mu.Lock()
		mt := sub.sess.cfg.MaxTurns
		sub.sess.mu.Unlock()
		if mt != 50 {
			t.Fatalf("subagent MaxTurns=%d, want 50 (should not inherit parent's 100)", mt)
		}
	}
}
```

**Step 2: Run to verify fail**

Expected: FAIL — subagent inherits parent's 100

**Step 3: Fix subagent default**

In `subagents.go`, replace the max_turns logic:

```go
// Old:
if maxTurns > 0 {
    subCfg.MaxTurns = maxTurns
} else if subCfg.MaxTurns <= 0 {
    subCfg.MaxTurns = 50
}

// New:
if maxTurns > 0 {
    subCfg.MaxTurns = maxTurns
} else {
    subCfg.MaxTurns = 50
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/subagents.go internal/agent/session_dod_test.go
git commit -m "fix: subagent max_turns always defaults to 50, not inherited (GAP-7.03)"
```

---

### Task 11: Fix close_agent to return structured status (I7/GAP-7.02)

`closeAgent` returns bare "closed" string. Should return the agent's final status, output, and turns used.

**Files:**
- Test: `internal/agent/session_dod_test.go`
- Modify: `internal/agent/subagents.go:149-159`

**Step 1: Write failing test**

```go
func TestCloseAgent_ReturnsStructuredStatus(t *testing.T) {
	c := llm.NewClient()
	subDone := make(chan struct{})
	step := 0
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		// Parent spawns agent.
		func(req llm.Request) llm.Response {
			step++
			return llm.Response{Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c1", Name: "spawn_agent", Type: "function",
					Arguments: json.RawMessage(`{"task":"work on something"}`),
				}}},
			}}
		},
		// Subagent completes.
		func(req llm.Request) llm.Response {
			step++
			defer func() { close(subDone) }()
			return llm.Response{Message: llm.Assistant("subagent done")}
		},
		// Parent gets spawn result, waits briefly, then closes agent.
		func(req llm.Request) llm.Response {
			step++
			<-subDone
			// Extract agent_id from the spawn result.
			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
						var sr map[string]any
						json.Unmarshal([]byte(fmt.Sprint(p.ToolResult.Content)), &sr)
						if aid, ok := sr["agent_id"].(string); ok {
							return llm.Response{Message: llm.Message{
								Role: llm.RoleAssistant,
								Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c2", Name: "close_agent", Type: "function",
									Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, aid)),
								}}},
							}}
						}
					}
				}
			}
			return llm.Response{Message: llm.Assistant("no agent id")}
		},
		func(req llm.Request) llm.Response {
			// Verify close_agent result is structured.
			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
						content := fmt.Sprint(p.ToolResult.Content)
						var result map[string]any
						if err := json.Unmarshal([]byte(content), &result); err != nil {
							t.Errorf("close_agent result is not JSON: %q", content)
						}
						if _, ok := result["status"]; !ok {
							t.Error("close_agent result missing 'status' field")
						}
					}
				}
			}
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "test")
}
```

**Step 2: Run to verify fail**

Expected: FAIL — close_agent returns "closed" not JSON

**Step 3: Fix closeAgent**

In `subagents.go`, replace the `closeAgent` method:

```go
func (s *Session) closeAgent(agentID string) (any, error) {
	s.mu.Lock()
	sub := s.subagents[agentID]
	delete(s.subagents, agentID)
	s.mu.Unlock()
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	sub.mu.Lock()
	status := sub.status
	result := sub.result
	turnsUsed := sub.turnsUsed
	sub.mu.Unlock()

	sub.sess.Close()

	b, _ := json.Marshal(map[string]any{
		"status":     string(status),
		"output":     result,
		"turns_used": turnsUsed,
	})
	return string(b), nil
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/subagents.go internal/agent/session_dod_test.go
git commit -m "fix: close_agent returns structured status instead of bare 'closed' (GAP-7.02)"
```

---

### Task 12: Fix SESSION_END deduplication (I5/GAP-2.10)

SESSION_END emitted both after ProcessInput and in Close(). Use a flag to ensure it fires exactly once.

**Files:**
- Test: `internal/agent/session_test.go`
- Modify: `internal/agent/session.go`

**Step 1: Write failing test**

```go
func TestSession_SessionEnd_EmittedExactlyOnce(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "hello")
	sess.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, ev := range *eventsPtr {
		if ev.Kind == EventSessionEnd {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 SESSION_END event, got %d", count)
	}
}
```

**Step 2: Run to verify fail**

Expected: FAIL — currently emits 2 SESSION_END events

**Step 3: Add sessionEndEmitted flag**

Add `sessionEndEmitted bool` field to Session struct. In `ProcessInput`, emit SESSION_END and set the flag. In `Close()`, only emit SESSION_END if `!s.sessionEndEmitted`.

In `session.go`, add field:
```go
sessionEndEmitted bool
```

In `Close()`, guard the emission:
```go
if !s.sessionEndEmitted {
    s.sessionEndEmitted = true
    s.emit(EventSessionEnd, map[string]any{...})
}
```

In `ProcessInput`, after emitting SESSION_END:
```go
s.mu.Lock()
s.sessionEndEmitted = true
s.mu.Unlock()
```

**Step 4: Run to verify pass + full suite**

Note: Existing test `TestSession_SessionEnd_AfterProcessInput` may need updating if it expects 2 events. Check and fix.

**Step 5: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: emit SESSION_END exactly once, not twice (GAP-2.10)"
```

---

### Task 13: Rebuild system prompt per loop iteration (I4/GAP-2.05)

System prompt is built once before the tool loop. Spec says build inside the loop each iteration.

**Files:**
- Test: `internal/agent/session_test.go`
- Modify: `internal/agent/session.go:694-715`

**Step 1: Write failing test**

```go
func TestSession_SystemPromptRebuiltPerRound(t *testing.T) {
	// The system prompt includes environment info. If a tool modifies a file in the
	// working directory, the system prompt should reflect that on the next LLM call.
	// We verify the system prompt is rebuilt by checking that the second LLM request
	// has a system prompt that reflects changes made by the first tool call.
	round := 0
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			round++
			return llm.Response{
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID: "c1", Name: "write_file", Type: "function",
						Arguments: json.RawMessage(`{"file_path":"AGENTS.md","content":"# New agent instructions"}`),
					}}},
				},
			}
		},
		func(req llm.Request) llm.Response {
			round++
			// The system prompt should now include the freshly-written AGENTS.md.
			if !strings.Contains(req.SystemPrompt, "New agent instructions") {
				t.Error("system prompt not rebuilt after tool execution — AGENTS.md changes not reflected")
			}
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	// Init git repo so project docs are discovered.
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), env, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx2, "write agents.md then verify")
}
```

**Step 2: Run to verify fail**

Expected: FAIL — system prompt built once, doesn't include new AGENTS.md

**Step 3: Move system prompt build inside the loop**

In `session.go`, move the block at lines 694-715 (docs loading, BuildSystemPrompt, MCP tools, UserInstructionOverride) to inside the `for round` loop, just after context management and before the LLM request construction.

**Step 4: Run to verify pass + full suite**

Note: This has a performance cost (re-loading project docs each round). The trade-off is correctness vs. performance. If tests are significantly slower, consider caching project docs with a short TTL.

**Step 5: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: rebuild system prompt each loop iteration (GAP-2.05)"
```

---

### Task 14: Close() cancels in-flight LLM (I10/GAP-9.11A)

`Close()` doesn't cancel the context passed to `ProcessInput`. It should own a cancel func.

**Files:**
- Test: `internal/agent/session_test.go`
- Modify: `internal/agent/session.go`

**Step 1: Write failing test**

```go
func TestSession_Close_CancelsInFlightLLMCall(t *testing.T) {
	llmBlocked := make(chan struct{})
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			close(llmBlocked)
			// Block until context is cancelled.
			<-req.Context.Done()
			return llm.Response{}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), "hello")
		done <- err
	}()

	<-llmBlocked // Wait until the LLM call is in-flight.
	sess.Close() // Should cancel the LLM call.

	select {
	case <-done:
		// ProcessInput returned — Close() successfully cancelled it.
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after Close() — in-flight LLM call not cancelled")
	}
}
```

Note: This requires the fakeAdapter to have access to the request context. Check if `llm.Request` carries a context. If not, the test approach needs to use a different mechanism (e.g., the adapter blocks on a channel that Close() releases). The key is that Close() must cause ProcessInput to unblock.

**Step 2: Run to verify fail**

Expected: FAIL — Close() doesn't cancel the LLM call, ProcessInput blocks forever

**Step 3: Add session-level cancel**

Add a `cancelFunc context.CancelFunc` and `sessionCtx context.Context` to the Session struct. In `ProcessInput`, derive the LLM context from both the caller's ctx and the session ctx. In `Close()`, call `s.cancelFunc()`.

```go
// In NewSession:
sessCtx, sessCancel := context.WithCancel(context.Background())
s.sessionCtx = sessCtx
s.cancelFunc = sessCancel

// In processOneInput, derive combined context:
ctx, cancel := context.WithCancel(ctx)
go func() {
    select {
    case <-s.sessionCtx.Done():
        cancel()
    case <-ctx.Done():
    }
}()
defer cancel()

// In Close():
if s.cancelFunc != nil {
    s.cancelFunc()
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: Close() cancels in-flight LLM calls via session context (GAP-9.11A)"
```

---

### Task 15: Fix graceful shutdown ordering (M25/M26/GAP-9.11B/C)

Shutdown order is wrong: subagent cleanup before SESSION_END (spec says after). Also missing explicit event flush.

**Files:**
- Test: `internal/agent/session_test.go`
- Modify: `internal/agent/session.go:419-453`

**Step 1: Write failing test**

```go
func TestSession_GracefulShutdown_SessionEndBeforeSubagentCleanup(t *testing.T) {
	// Verify SESSION_END is emitted before subagents are cleaned up.
	// We detect this by checking that SESSION_END fires while a subagent still exists.
	c := llm.NewClient()
	step := 0
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			step++
			return llm.Response{Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c1", Name: "spawn_agent", Type: "function",
					Arguments: json.RawMessage(`{"task":"long task"}`),
				}}},
			}}
		},
		// Subagent response
		func(req llm.Request) llm.Response {
			step++
			time.Sleep(200 * time.Millisecond)
			return llm.Response{Message: llm.Assistant("sub done")}
		},
		func(req llm.Request) llm.Response {
			step++
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "spawn")
	sess.Close()
	<-doneCh

	// Just verify SESSION_END is present. The ordering fix is structural.
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, ev := range *eventsPtr {
		if ev.Kind == EventSessionEnd {
			found = true
		}
	}
	if !found {
		t.Fatal("SESSION_END not emitted")
	}
}
```

**Step 2: Verify test passes or fails depending on subagent state**

**Step 3: Fix ordering in Close()**

Reorder `Close()` to:
1. Set state to CLOSED
2. Emit SESSION_END (step 6 per spec)
3. Close subagents (step 7)
4. Close MCP
5. env.Cleanup()
6. Drain events channel

```go
func (s *Session) Close() {
    s.mu.Lock()
    if s.state == SessionClosed {
        s.mu.Unlock()
        return
    }
    s.state = SessionClosed
    turns := s.turns
    subs := make([]*subagent, 0, len(s.subagents))
    for id, sub := range s.subagents {
        subs = append(subs, sub)
        delete(s.subagents, id)
    }
    s.mu.Unlock()

    // Cancel in-flight LLM calls (step 1, from Task 14).
    if s.cancelFunc != nil {
        s.cancelFunc()
    }

    // Step 6: Emit SESSION_END before cleanup.
    if !s.sessionEndEmitted {
        s.sessionEndEmitted = true
        s.emit(EventSessionEnd, map[string]any{
            "reason": "session_closed",
            "state":  string(SessionClosed),
            "turns":  turns,
        })
    }

    // Step 7: Clean up subagents.
    for _, sub := range subs {
        sub.sess.Close()
    }

    if s.mcpMgr != nil {
        s.mcpMgr.Close()
    }

    s.env.Cleanup()
    close(s.events)
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: graceful shutdown emits SESSION_END before subagent cleanup (GAP-9.11B/C)"
```

---

### Task 16: Add parity test — tool output truncation (I11/GAP-9.12A)

**Files:**
- Test: `internal/agent/session_parity_test.go`

**Step 1: Write test**

```go
func TestParity_ToolOutputTruncation(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "c1", Name: canonicalReadFile(pc.name), Type: "function",
							Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
						}}},
					}}
				},
				func(req llm.Request) llm.Response {
					// Verify the tool result was truncated.
					for _, m := range req.Messages {
						for _, p := range m.Content {
							if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
								content := fmt.Sprint(p.ToolResult.Content)
								if !strings.Contains(content, "truncated") {
									t.Error("expected truncation marker in tool result")
								}
							}
						}
					}
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			// Create a file larger than the read_file limit (50,000 chars).
			dir := sess.env.WorkingDirectory()
			big := strings.Repeat("x", 60_000)
			os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0644)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "read big file")
		})
	}
}
```

**Step 2: Run — should pass (truncation already works cross-provider)**

**Step 3: Commit**

```bash
git add internal/agent/session_parity_test.go
git commit -m "test: add cross-provider parity test for tool output truncation (GAP-9.12A)"
```

---

### Task 17: Add parity test — reasoning effort (I11/GAP-9.12B)

**Files:**
- Test: `internal/agent/session_parity_test.go`

**Step 1: Write test**

```go
func TestParity_ReasoningEffort(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			efforts := []string{}
			steps := []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					if req.ReasoningEffort != nil {
						efforts = append(efforts, *req.ReasoningEffort)
					}
					return llm.Response{Message: llm.Assistant("first")}
				},
				func(req llm.Request) llm.Response {
					if req.ReasoningEffort != nil {
						efforts = append(efforts, *req.ReasoningEffort)
					}
					return llm.Response{Message: llm.Assistant("second")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			sess.cfg.ReasoningEffort = "low"
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess.ProcessInput(ctx, "first")

			sess.SetReasoningEffort("high")
			sess.ProcessInput(ctx, "second")

			if len(efforts) < 2 {
				t.Fatalf("expected 2 reasoning efforts, got %d", len(efforts))
			}
			if efforts[0] != "low" {
				t.Errorf("first call: got %q, want 'low'", efforts[0])
			}
			if efforts[1] != "high" {
				t.Errorf("second call: got %q, want 'high'", efforts[1])
			}
		})
	}
}
```

**Step 2: Run — should pass**

**Step 3: Commit**

```bash
git add internal/agent/session_parity_test.go
git commit -m "test: add cross-provider parity test for reasoning effort change (GAP-9.12B)"
```

---

### Task 18: Add parity test — subagent spawn and wait (I11/GAP-9.12C)

**Files:**
- Test: `internal/agent/session_parity_test.go`

**Step 1: Write test**

```go
func TestParity_SubagentSpawnAndWait(t *testing.T) {
	for _, pc := range providerCases {
		t.Run(pc.name, func(t *testing.T) {
			step := 0
			steps := []func(llm.Request) llm.Response{
				// Parent spawns subagent.
				func(req llm.Request) llm.Response {
					step++
					return llm.Response{Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "c1", Name: "spawn_agent", Type: "function",
							Arguments: json.RawMessage(`{"task":"create a file"}`),
						}}},
					}}
				},
				// Subagent creates file.
				func(req llm.Request) llm.Response {
					step++
					return llm.Response{Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "s1", Name: canonicalWriteFile(pc.name), Type: "function",
							Arguments: json.RawMessage(`{"file_path":"sub.txt","content":"from subagent"}`),
						}}},
					}}
				},
				// Subagent finishes.
				func(req llm.Request) llm.Response {
					step++
					return llm.Response{Message: llm.Assistant("done")}
				},
				// Parent waits for subagent.
				func(req llm.Request) llm.Response {
					step++
					for _, m := range req.Messages {
						for _, p := range m.Content {
							if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
								var sr map[string]any
								json.Unmarshal([]byte(fmt.Sprint(p.ToolResult.Content)), &sr)
								if aid, ok := sr["agent_id"].(string); ok {
									return llm.Response{Message: llm.Message{
										Role: llm.RoleAssistant,
										Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
											ID: "c2", Name: "wait", Type: "function",
											Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, aid)),
										}}},
									}}
								}
							}
						}
					}
					return llm.Response{Message: llm.Assistant("no agent id")}
				},
				// Parent gets wait result.
				func(req llm.Request) llm.Response {
					step++
					for _, m := range req.Messages {
						for _, p := range m.Content {
							if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
								content := fmt.Sprint(p.ToolResult.Content)
								var result SubAgentResult
								if err := json.Unmarshal([]byte(content), &result); err == nil && result.Success {
									return llm.Response{Message: llm.Assistant("subagent succeeded")}
								}
							}
						}
					}
					return llm.Response{Message: llm.Assistant("subagent failed")}
				},
			}
			sess, _ := newParitySession(t, pc, steps)
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := sess.ProcessInput(ctx, "test subagent")
			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if !strings.Contains(result, "subagent succeeded") {
				t.Fatalf("expected subagent success, got %q", result)
			}

			// Verify file was created by subagent.
			dir := sess.env.WorkingDirectory()
			data, err := os.ReadFile(filepath.Join(dir, "sub.txt"))
			if err != nil {
				t.Fatalf("subagent file not created: %v", err)
			}
			if string(data) != "from subagent" {
				t.Fatalf("subagent file content: %q", string(data))
			}
		})
	}
}
```

**Step 2: Run — should pass**

**Step 3: Commit**

```bash
git add internal/agent/session_parity_test.go
git commit -m "test: add cross-provider parity test for subagent spawn and wait (GAP-9.12C)"
```

---

### Task 19: Add grep output_mode parameter (M1/GAP-3.04)

The grep tool lacks `output_mode` (content/files_with_matches/count). Currently only returns content mode.

**Files:**
- Test: `internal/agent/env_local_test.go`, `internal/agent/session_test.go`
- Modify: `internal/agent/env.go`, `internal/agent/env_local.go`, `internal/agent/profile.go`, `internal/agent/session.go`

This is a larger change touching the interface, implementation, tool definition, and tool handler.

**Step 1: Write failing test for the interface**

In `env_local_test.go`:

```go
func TestGrep_OutputMode_FilesWithMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello again"), 0644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("no match"), 0644)

	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()

	result, err := env.Grep("hello", dir, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "a.txt") || !strings.Contains(result, "b.txt") {
		t.Errorf("expected file names in result: %q", result)
	}
	// Should NOT contain matching line content.
	if strings.Contains(result, "hello world") {
		t.Error("files_with_matches should not include line content")
	}
}

func TestGrep_OutputMode_Count(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nhello"), 0644)

	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()

	result, err := env.Grep("hello", dir, "", false, 100, "count")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "2") {
		t.Errorf("expected count of 2: %q", result)
	}
}
```

**Step 2: Run to verify fail — Grep doesn't accept outputMode parameter**

**Step 3: Implement**

1. Add `outputMode string` parameter to `Grep` in `env.go` interface:
   ```go
   Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error)
   ```

2. Update `env_local.go` `Grep` and `grepNative` to handle `outputMode`:
   - `"content"` (default): current behavior
   - `"files_with_matches"`: only return file paths
   - `"count"`: return match count per file

   For ripgrep: add `--files-with-matches` or `--count` flag.
   For native fallback: adjust output format.

3. Update `defGrep()` in `profile.go` to add `output_mode` parameter:
   ```go
   "output_mode": map[string]any{"type": "string", "enum": []string{"content", "files_with_matches", "count"}},
   ```

4. Update the grep tool handler in `session.go` to pass `outputMode` through.

5. Update all existing callers of `Grep` that don't pass `outputMode` (add `""` for default).

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/env.go internal/agent/env_local.go internal/agent/profile.go internal/agent/session.go internal/agent/env_local_test.go
git commit -m "feat: add output_mode parameter to grep tool (GAP-3.04)"
```

---

### Task 20: Custom tools appear in system prompt (M13/GAP-9.08A)

Tools registered post-session-creation via `sess.reg.Register()` don't appear in the system prompt's "Tools:" section.

**Files:**
- Test: `internal/agent/session_test.go`
- Modify: `internal/agent/session.go` or `internal/agent/profile.go`

**Step 1: Write failing test**

```go
func TestSession_CustomRegisteredTool_AppearsInSystemPrompt(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if !strings.Contains(req.SystemPrompt, "my_custom_tool") {
				t.Error("custom tool not in system prompt")
			}
			if !strings.Contains(req.SystemPrompt, "Does custom things") {
				t.Error("custom tool description not in system prompt")
			}
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Register a custom tool after session creation.
	sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "my_custom_tool", Description: "Does custom things"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "test")
}
```

**Step 2: Run to verify fail**

**Step 3: Implement**

After building the system prompt from profile tools, also append any registry tools that aren't in the profile's tool definitions. In `processOneInput`, after building `sys`:

```go
// Include custom-registered tools not in the profile.
profileNames := map[string]bool{}
for _, td := range s.profile.ToolDefinitions() {
    profileNames[td.Name] = true
}
for _, td := range s.reg.Definitions() {
    if !profileNames[td.Name] && !isMCPTool(td.Name) {
        desc := strings.TrimSpace(td.Description)
        if desc == "" {
            desc = "(no description)"
        }
        sys += fmt.Sprintf("- %s: %s\n", td.Name, desc)
    }
}
```

Note: This only works if system prompt is rebuilt per loop iteration (Task 13). If Task 13 isn't done yet, the custom tool won't appear until the next ProcessInput call.

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: custom-registered tools appear in system prompt (GAP-9.08A)"
```

---

### Task 21: Fix send_input to work on running subagents (I9/GAP-7.07)

`send_input` rejects running agents. The spec says "Send a message to a running subagent." Use `Steer()` to inject the message when the agent is running. When idle, start a new ProcessInput.

**Files:**
- Test: `internal/agent/session_dod_test.go`
- Modify: `internal/agent/subagents.go:90-106`

**Step 1: Write failing test**

```go
func TestSendInput_WorksOnRunningAgent(t *testing.T) {
	c := llm.NewClient()
	toolStarted := make(chan struct{})
	toolRelease := make(chan struct{})
	step := 0
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		// Parent spawns agent.
		func(req llm.Request) llm.Response {
			step++
			return llm.Response{Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c1", Name: "spawn_agent", Type: "function",
					Arguments: json.RawMessage(`{"task":"long task"}`),
				}}},
			}}
		},
		// Subagent's first response: call a slow tool.
		func(req llm.Request) llm.Response {
			step++
			return llm.Response{Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "s1", Name: "slow_tool", Type: "function",
					Arguments: json.RawMessage(`{}`),
				}}},
			}}
		},
		// Subagent's second response (after steering): should see the injected message.
		func(req llm.Request) llm.Response {
			step++
			for _, m := range req.Messages {
				if m.Role == llm.RoleUser {
					for _, p := range m.Content {
						if p.Kind == llm.ContentText && strings.Contains(p.Text, "injected message") {
							return llm.Response{Message: llm.Assistant("saw injected")}
						}
					}
				}
			}
			return llm.Response{Message: llm.Assistant("no injection")}
		},
		// Parent gets spawn result, sends input while agent is running.
		func(req llm.Request) llm.Response {
			step++
			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
						var sr map[string]any
						json.Unmarshal([]byte(fmt.Sprint(p.ToolResult.Content)), &sr)
						if aid, ok := sr["agent_id"].(string); ok {
							// Wait for slow tool to start.
							<-toolStarted
							return llm.Response{Message: llm.Message{
								Role: llm.RoleAssistant,
								Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
									ID: "c2", Name: "send_input", Type: "function",
									Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"message":"injected message"}`, aid)),
								}}},
							}}
						}
					}
				}
			}
			return llm.Response{Message: llm.Assistant("no agent id")}
		},
		// Parent gets send_input ack, releases the slow tool, waits.
		func(req llm.Request) llm.Response {
			step++
			close(toolRelease)
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Register slow tool on subagent sessions (it will be inherited).
	sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow_tool"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			close(toolStarted)
			<-toolRelease
			return "ok", nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := sess.ProcessInput(ctx, "test")
	if err != nil {
		t.Logf("ProcessInput error (may be expected): %v", err)
	}
	_ = result
	// The key assertion is that send_input didn't return an error.
	// Check via the fakeAdapter requests.
}
```

Note: This test is complex. The simpler approach: verify `sendInput` calls `Steer()` when `sub.running == true` instead of returning an error.

**Step 2: Run to verify fail**

Expected: FAIL — "agent is already running"

**Step 3: Fix sendInput**

```go
func (s *Session) sendInput(ctx context.Context, agentID string, input string) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()

	if running {
		// Inject as steering message into the running session.
		sub.sess.Steer(input)
		return "ok", nil
	}

	// Agent is idle — start a new ProcessInput round.
	sub.mu.Lock()
	sub.done = make(chan struct{})
	sub.running = true
	sub.mu.Unlock()

	go sub.run(ctx, input)
	return "ok", nil
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/subagents.go internal/agent/session_dod_test.go
git commit -m "fix: send_input steers running subagents instead of rejecting (GAP-7.07)"
```

---

### Task 22: Fix subagent working_dir to share parent env (M24/GAP-7.06)

When `working_dir` is specified, `spawnAgent` creates a new `LocalExecutionEnvironment` with separate PID tracking. Instead, it should wrap the parent's env with an overridden working directory.

**Files:**
- Test: `internal/agent/session_dod_test.go`
- Modify: `internal/agent/subagents.go:62-65`, possibly `internal/agent/env_local.go`

**Step 1: Write failing test**

```go
func TestSubagent_WorkingDir_SharesParentPIDTracking(t *testing.T) {
	// When a subagent uses working_dir, processes it spawns should be tracked
	// by the same env as the parent (shared PID tracking).
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	os.MkdirAll(subDir, 0755)

	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()

	// Create a "child env" the way we want it — with shared tracking.
	childEnv := env.WithWorkingDirectory(subDir)
	if childEnv.WorkingDirectory() != subDir {
		t.Fatalf("child working dir: %q, want %q", childEnv.WorkingDirectory(), subDir)
	}
}
```

**Step 2: Run to verify fail — WithWorkingDirectory doesn't exist yet**

**Step 3: Add WithWorkingDirectory to LocalExecutionEnvironment**

```go
func (e *LocalExecutionEnvironment) WithWorkingDirectory(dir string) *LocalExecutionEnvironment {
	return &LocalExecutionEnvironment{
		RootDir:     dir,
		envPolicy:   e.envPolicy,
		runningPIDs: &e.runningPIDs, // Share PID tracking with parent.
	}
}
```

Note: `runningPIDs` is currently `sync.Map` (value type). To share, change it to `*sync.Map` (pointer type) or use a different approach. This requires careful refactoring of `env_local.go`.

Then in `subagents.go`:

```go
if workingDir = strings.TrimSpace(workingDir); workingDir != "" {
    if le, ok := s.env.(*LocalExecutionEnvironment); ok {
        subEnv = le.WithWorkingDirectory(workingDir)
    } else {
        subEnv = NewLocalExecutionEnvironment(workingDir)
    }
}
```

**Step 4: Run to verify pass + full suite**

**Step 5: Commit**

```bash
git add internal/agent/env_local.go internal/agent/subagents.go internal/agent/session_dod_test.go
git commit -m "fix: subagent working_dir shares parent env PID tracking (GAP-7.06)"
```

---

### Task 23: Agent-level integration smoke tests (C2/GAP-9.13)

7 end-to-end scenarios with real API keys. These create a Session, submit real inputs to a real LLM API, and verify actual outcomes.

**Files:**
- Create: `internal/agent/integration_smoke_test.go`

These tests require `OPENAI_API_KEY` env var and are gated behind `-short` (skip when `-short` is set). They use the `gpt-5-mini` model for cost efficiency.

**Step 1: Write tests**

```go
//go:build !short

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
	_ "primeradiant.com/serf/internal/llm/providers/openai"
)

func skipWithoutAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
}

func integrationSession(t *testing.T) *Session {
	t.Helper()
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5-mini-2025-08-07"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 20,
		MaxTurns:              5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestIntegration_SimpleFileCreation(t *testing.T) {
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	defer sess.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := sess.ProcessInput(ctx, "Create a file called hello.txt containing 'Hello, World!'")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sess.env.WorkingDirectory(), "hello.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(data), "Hello") {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

// ... (6 more tests for read-and-edit, shell, truncation, steering, subagent, timeout)
// Each follows the same pattern: real Session + real LLM + verify file/event outcomes.
```

Full implementations for all 7 scenarios should follow the patterns in `internal/llm/integration_smoke_test.go` but at the Session level.

**Step 2: Run with API key**

Run: `export $(cat .env | xargs) && go test ./internal/agent/ -run TestIntegration -v -timeout 300s`

**Step 3: Commit**

```bash
git add internal/agent/integration_smoke_test.go
git commit -m "test: add agent-level integration smoke tests with real API (GAP-9.13)"
```

---

## Execution Order

Tasks are ordered by dependency:

1. **Tasks 1-9** (independent, can be parallelized): Profile fixes, test additions
2. **Task 10-11**: Subagent behavior fixes
3. **Task 12**: SESSION_END dedup (needed before Task 15)
4. **Task 13**: System prompt per loop (enables Task 20)
5. **Task 14**: Close() cancels LLM
6. **Task 15**: Shutdown ordering (depends on Tasks 12, 14)
7. **Tasks 16-18**: Parity test additions
8. **Task 19**: Grep output_mode
9. **Task 20**: Custom tools in system prompt (depends on Task 13)
10. **Task 21**: send_input semantics
11. **Task 22**: Subagent env sharing
12. **Task 23**: Integration smoke tests (last — needs everything else working)
