package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// --- Issue 1: allToolDefinitions caching ---

func TestCachedToolDefs_MatchUncached(t *testing.T) {
	// Verify that cached tool definitions produce the same result as
	// building them from scratch each round.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("ok")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Get cached defs (no MinResultRound gate).
	defs := sess.allToolDefinitions(0)
	if len(defs) == 0 {
		t.Fatal("allToolDefinitions returned empty list")
	}

	// Call again — should return identical results.
	defs2 := sess.allToolDefinitions(0)
	if len(defs) != len(defs2) {
		t.Fatalf("tool def count mismatch: %d vs %d", len(defs), len(defs2))
	}
	for i := range defs {
		if defs[i].Name != defs2[i].Name {
			t.Errorf("tool %d name mismatch: %q vs %q", i, defs[i].Name, defs2[i].Name)
		}
	}
}

func TestCachedToolDefs_AlwaysIncludesCommunicate(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	resultName := sess.resultToolName()
	defs := sess.allToolDefinitions(0)
	found := false
	for _, td := range defs {
		if td.Name == resultName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("communicate tool %q should always be present", resultName)
	}
}

func TestCachedToolDefs_IncludesMCPTools(t *testing.T) {
	// MCP tools should be included in the cached list.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Simulate MCP tools by adding to mcpTools and registering.
	mcpTool := llm.ToolDefinition{
		Name:        "mcp__server__tool1",
		Description: "An MCP tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
	sess.mcpTools = append(sess.mcpTools, mcpTool)
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: mcpTool},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})

	// Rebuild cache after adding MCP tool.
	sess.rebuildToolDefsCache()

	defs := sess.allToolDefinitions(0)
	found := false
	for _, td := range defs {
		if td.Name == "mcp__server__tool1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("MCP tool not found in allToolDefinitions")
	}
}

func TestCachedToolDefs_IncludesRegistryOnlyTools(t *testing.T) {
	// Tools registered directly (e.g. approve/reject) should be included.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Register a custom tool directly.
	customTool := RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        "custom_tool",
				Description: "A custom tool",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	}
	_ = sess.reg.Register(customTool)

	// Rebuild cache to pick up new tool.
	sess.rebuildToolDefsCache()

	defs := sess.allToolDefinitions(0)
	found := false
	for _, td := range defs {
		if td.Name == "custom_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom tool not found in allToolDefinitions")
	}
}

// --- web_search tool dedup (Anthropic duplicate tool names) ---

func TestToolDefs_WebSearchExcludedForNonGemini(t *testing.T) {
	// The web_search function tool is a shim for Gemini only (see tool_web_search.go).
	// For Anthropic and OpenAI, native web search is used via req.WebSearch.
	// Including the web_search function tool for Anthropic causes a duplicate
	// "web_search" name (the adapter also injects a server-side web_search tool),
	// which Anthropic's API rejects.
	for _, tc := range []struct {
		name    string
		profile ProviderProfile
		adapter string
		want    bool // true = web_search should be present
	}{
		{"anthropic", NewAnthropicProfile("claude-test"), "anthropic", false},
		{"openai", NewOpenAIProfile("gpt-test"), "openai", false},
		{"gemini", NewGeminiProfile("gemini-test"), "gemini", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: tc.adapter})

			sess, err := NewSession(c, tc.profile, NewLocalExecutionEnvironment(dir), SessionConfig{})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			defer sess.Close()

			defs := sess.allToolDefinitions(0)
			found := false
			for _, td := range defs {
				if td.Name == "web_search" {
					found = true
					break
				}
			}
			if found != tc.want {
				if tc.want {
					t.Error("web_search tool should be present for Gemini but was not found")
				} else {
					t.Errorf("web_search function tool should NOT be in tool definitions for %s (causes duplicate name with native web search)", tc.name)
				}
			}
		})
	}
}

func TestToolDefs_NoDuplicateNames(t *testing.T) {
	// Verify that no provider profile produces duplicate tool names in
	// allToolDefinitions, which would be rejected by APIs like Anthropic.
	for _, tc := range []struct {
		name    string
		profile ProviderProfile
		adapter string
	}{
		{"anthropic", NewAnthropicProfile("claude-test"), "anthropic"},
		{"openai", NewOpenAIProfile("gpt-test"), "openai"},
		{"gemini", NewGeminiProfile("gemini-test"), "gemini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: tc.adapter})

			sess, err := NewSession(c, tc.profile, NewLocalExecutionEnvironment(dir), SessionConfig{})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			defer sess.Close()

			defs := sess.allToolDefinitions(0)
			seen := make(map[string]bool, len(defs))
			for _, td := range defs {
				if seen[td.Name] {
					t.Errorf("duplicate tool name %q in allToolDefinitions", td.Name)
				}
				seen[td.Name] = true
			}
		})
	}
}

func TestToolDefs_MCPToolSameNameAsProfileTool_NoDuplicate(t *testing.T) {
	// If an MCP tool has the same name as a profile tool (unlikely due to
	// namespacing, but possible), it should not produce a duplicate in the
	// tool definitions sent to the API.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Inject an MCP tool with the same name as a profile tool.
	mcpTool := llm.ToolDefinition{
		Name:        "shell",
		Description: "MCP shell (conflict)",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
	sess.mcpTools = append(sess.mcpTools, mcpTool)
	// The tool is already registered via registerCoreTools, so it's in registered.
	sess.rebuildToolDefsCache()

	defs := sess.allToolDefinitions(0)
	count := 0
	for _, td := range defs {
		if td.Name == "shell" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 'shell' tool, got %d", count)
	}
}

// --- Issue 2: History copy reduction ---

func TestHistoryCopyReduction_ContextAndExpansionShareCopy(t *testing.T) {
	// Verify that context management and history expansion produce
	// correct results when sharing a single history copy.
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo hello",
			"description": "test",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}

	comm := submitResultCall("c1", "done after tools")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(shellCall("s1"))
			},
			func(req llm.Request) llm.Response {
				// Verify history has the tool result from round 0.
				hasToolResult := false
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleTool {
						hasToolResult = true
						break
					}
				}
				if !hasToolResult {
					t.Error("round 1 should see tool result from round 0")
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "test")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "done after tools" {
		t.Fatalf("got %q, want %q", out, "done after tools")
	}
	sess.Close()
}

func TestAfterAction_ReceivesCurrentHistory(t *testing.T) {
	// Verify that AfterAction sees the full history including the latest tool results.
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "test",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}
	comm := submitResultCall("c1", "done")

	var afterActionHistory []Turn
	spy := &spyStrategy{
		toolsDefs: nil,
	}

	// Override AfterAction to capture what it sees.
	type captureSpy struct {
		*spyStrategy
	}
	capturer := &afterActionCapture{
		spyStrategy: spy,
		onAfterAction: func(history []Turn) {
			afterActionHistory = append([]Turn{}, history...)
		},
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(shellCall("s1"))
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ContextStrategyOverride: capturer,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "test")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// AfterAction should have been called and should see tool results.
	if len(afterActionHistory) == 0 {
		t.Fatal("AfterAction was never called with history")
	}

	// The history should contain at least: user input, assistant (tool call), tool results.
	hasToolResults := false
	for _, turn := range afterActionHistory {
		if turn.Kind == TurnToolResults {
			hasToolResults = true
			break
		}
	}
	if !hasToolResults {
		t.Error("AfterAction history should contain tool results")
	}
}

// afterActionCapture wraps a spyStrategy and captures the history passed to AfterAction.
type afterActionCapture struct {
	*spyStrategy
	onAfterAction func(history []Turn)
}

func (a *afterActionCapture) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	if a.onAfterAction != nil {
		a.onAfterAction(history)
	}
	return a.spyStrategy.AfterAction(ctx, history, client)
}

// --- Issue 3: System prompt caching ---

func TestCachedSystemPromptComponents_SkillList(t *testing.T) {
	// Verify that the cached skill list matches what would be built each round.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Build the skill list manually from s.skills (like processOneInput does).
	var manualList []SkillMeta
	for _, sm := range sess.skills {
		manualList = append(manualList, sm)
	}

	// The cached list should have the same length.
	if len(sess.cachedSkillList) != len(manualList) {
		t.Errorf("cached skill list length %d != manual %d", len(sess.cachedSkillList), len(manualList))
	}
}

func TestCachedSystemPromptComponents_ExtraToolsString(t *testing.T) {
	// Verify the cached extra tools string is consistent.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Add an MCP tool and rebuild to test that extra tools string includes it.
	mcpTool := llm.ToolDefinition{
		Name:        "mcp__test__tool",
		Description: "Test MCP tool",
	}
	sess.mcpTools = append(sess.mcpTools, mcpTool)
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: mcpTool},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	sess.rebuildPromptCache()

	if !strings.Contains(sess.cachedExtraTools, "mcp__test__tool") {
		t.Errorf("cached extra tools should contain MCP tool name, got: %q", sess.cachedExtraTools)
	}
	if !strings.Contains(sess.cachedExtraTools, "MCP Tools") {
		t.Errorf("cached extra tools should contain 'MCP Tools' header, got: %q", sess.cachedExtraTools)
	}
}

func TestCachedSystemPromptComponents_NonInteractiveGuidance(t *testing.T) {
	// NonInteractive guidance should be included in the rendered system prompt.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NonInteractive: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	prompt := sess.cachedSystemPrompt
	if !strings.Contains(prompt, "Non-interactive mode") {
		t.Error("system prompt should contain non-interactive guidance when NonInteractive is set")
	}
}

func TestCachedSystemPromptComponents_AgentSection(t *testing.T) {
	// Plugin agents section should be cached.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// The cached agent section should match FormatPluginAgentsPrompt.
	expected := FormatPluginAgentsPrompt(sess.pluginAgents)
	if sess.cachedAgentSection != expected {
		t.Errorf("cached agent section mismatch:\ngot:  %q\nwant: %q", sess.cachedAgentSection, expected)
	}
}

func TestSystemPromptConsistency_WithAndWithoutCache(t *testing.T) {
	// The system prompt produced with caching should be identical to one
	// built without caching. Use buildInitialSystemPrompt as the reference.
	dir := t.TempDir()
	c := llm.NewClient()

	var capturedSysPrompt string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Capture the system prompt from the first round.
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSysPrompt = req.Messages[0].Text()
				}
				return finalResponse("ok")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NonInteractive: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// buildInitialSystemPrompt builds from scratch (no cache).
	referencePrompt := sess.buildInitialSystemPrompt()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// The prompt used in processOneInput should match the reference.
	if capturedSysPrompt != referencePrompt {
		// Find first difference for debugging.
		minLen := len(capturedSysPrompt)
		if len(referencePrompt) < minLen {
			minLen = len(referencePrompt)
		}
		diffIdx := minLen
		for i := 0; i < minLen; i++ {
			if capturedSysPrompt[i] != referencePrompt[i] {
				diffIdx = i
				break
			}
		}
		t.Errorf("system prompt mismatch at byte %d (of %d vs %d):\ncaptured: ...%q...\nreference: ...%q...",
			diffIdx, len(capturedSysPrompt), len(referencePrompt),
			safeSubstring(capturedSysPrompt, diffIdx-20, diffIdx+40),
			safeSubstring(referencePrompt, diffIdx-20, diffIdx+40))
	}
}

func safeSubstring(s string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(s) {
		end = len(s)
	}
	if start >= end {
		return ""
	}
	return s[start:end]
}

// --- Benchmark ---

// Issue 1: Project docs should be loaded once at init, not every round
// ---------------------------------------------------------------------------

func TestSession_ProjectDocsLoadedOnceAtInit(t *testing.T) {
	dir := t.TempDir()

	// Create a project doc file so LoadProjectDocs has something to find.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# My Agent\nHello"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Session should have cached project docs.
	if sess.projectDocs == nil {
		t.Fatal("expected projectDocs to be cached after NewSession")
	}
}

func TestSession_CachedProjectDocsUsedInSystemPrompt(t *testing.T) {
	dir := t.TempDir()

	// Create a project doc.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("cached-doc-content"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// The system prompt sent to the LLM should contain the cached doc content.
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
	// System prompt is the first message (role=system).
	if len(reqs[0].Messages) == 0 {
		t.Fatal("expected at least 1 message in request")
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "cached-doc-content") {
		t.Fatalf("system prompt should contain cached project doc content, got: %s", sys[:min(200, len(sys))])
	}
}

// ---------------------------------------------------------------------------
// Issue 2: SessionMeta — lightweight save instead of full snapshot
// ---------------------------------------------------------------------------

func testSessionMeta() SessionMeta {
	return SessionMeta{
		ID:        "01JTEST_META_00000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: SessionConfig{
			MaxToolRoundsPerInput: 200,
			ReasoningEffort:       "high",
		},
		EnvInfo: EnvironmentInfo{
			WorkingDir: "/tmp/test",
			Platform:   "linux",
			IsGitRepo:  true,
			GitBranch:  "main",
		},
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
}

func TestSessionMeta_JSONRoundTrip(t *testing.T) {
	orig := testSessionMeta()
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != orig.ID {
		t.Fatalf("id: got %q want %q", got.ID, orig.ID)
	}
	if got.ProfileID != orig.ProfileID {
		t.Fatalf("profile_id: got %q want %q", got.ProfileID, orig.ProfileID)
	}
	if got.Model != orig.Model {
		t.Fatalf("model: got %q want %q", got.Model, orig.Model)
	}
	if got.TurnCount != orig.TurnCount {
		t.Fatalf("turn_count: got %d want %d", got.TurnCount, orig.TurnCount)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("created_at: got %v want %v", got.CreatedAt, orig.CreatedAt)
	}
}

func TestSaveSessionMeta_CreatesMetaFile(t *testing.T) {
	dir := t.TempDir()
	meta := testSessionMeta()

	if err := SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	// File should exist at sessions/<id>.meta.json
	path := filepath.Join(dir, "sessions", meta.ID+".meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Should be compact JSON (no indentation).
	if strings.Contains(string(data), "  ") {
		t.Fatal("meta.json should use compact JSON (no indentation)")
	}

	var got SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("saved id: got %q want %q", got.ID, meta.ID)
	}
}

func TestLoadSessionMeta(t *testing.T) {
	dir := t.TempDir()
	meta := testSessionMeta()

	if err := SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	got, err := LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("id: got %q want %q", got.ID, meta.ID)
	}
	if got.Model != meta.Model {
		t.Fatalf("model: got %q want %q", got.Model, meta.Model)
	}
}

func TestLoadSessionMeta_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSessionMeta(dir, "NONEXISTENT")
	if err == nil {
		t.Fatal("expected error for nonexistent meta")
	}
}

func TestListSessionMetas_SortedByUpdatedAt(t *testing.T) {
	dir := t.TempDir()

	meta1 := testSessionMeta()
	meta1.ID = "01JTEST_META_00000000001"
	meta1.UpdatedAt = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	meta2 := testSessionMeta()
	meta2.ID = "01JTEST_META_00000000002"
	meta2.UpdatedAt = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	meta3 := testSessionMeta()
	meta3.ID = "01JTEST_META_00000000003"
	meta3.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	for _, m := range []SessionMeta{meta1, meta2, meta3} {
		if err := SaveSessionMeta(dir, m); err != nil {
			t.Fatalf("SaveSessionMeta %s: %v", m.ID, err)
		}
	}

	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list length: got %d want 3", len(list))
	}
	// Most recently updated first.
	if list[0].ID != meta2.ID {
		t.Fatalf("list[0].id: got %q want %q", list[0].ID, meta2.ID)
	}
	if list[1].ID != meta3.ID {
		t.Fatalf("list[1].id: got %q want %q", list[1].ID, meta3.ID)
	}
	if list[2].ID != meta1.ID {
		t.Fatalf("list[2].id: got %q want %q", list[2].ID, meta1.ID)
	}
}

func TestListSessionMetas_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestSession_MaybeAutoSave_WritesMetaNotSnapshot(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		StateDir:              dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// Should find a .meta.json file, not a full .json snapshot.
	sessDir := filepath.Join(dir, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	foundMeta := false
	foundFullSnapshot := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".meta.json") {
			foundMeta = true
		}
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".meta.json") && !strings.HasSuffix(name, ".transcript.jsonl") {
			foundFullSnapshot = true
		}
	}

	if !foundMeta {
		t.Fatal("expected .meta.json file after ProcessInput")
	}
	if foundFullSnapshot {
		t.Fatal("should not write full .json snapshot from maybeAutoSave (only .meta.json)")
	}
}

func TestSession_Meta_ReturnsLightweightMeta(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	meta := sess.Meta()

	if meta.ID != sess.ID() {
		t.Fatalf("id: got %q want %q", meta.ID, sess.ID())
	}
	if meta.ProfileID != "openai" {
		t.Fatalf("profile_id: got %q want %q", meta.ProfileID, "openai")
	}
	if meta.Model != "gpt-5.2" {
		t.Fatalf("model: got %q want %q", meta.Model, "gpt-5.2")
	}
}

func TestRestoreSession_FromMetaAndTranscript(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := SessionMeta{
		ID:        "01JTEST_META_RESTORE_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 2,
	}

	// Write a transcript file with history.
	tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID: meta.ID,
		CreatedAt: meta.CreatedAt,
		ProfileID: meta.ProfileID,
		Model:     meta.Model,
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	if err := tw.Append(NewTurn(TurnUserInput, llm.User("transcript-msg"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Append(NewTurn(TurnAssistant, llm.Assistant("transcript-reply"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	if sess.ID() != meta.ID {
		t.Fatalf("id: got %q want %q", sess.ID(), meta.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}

	// The restored history should come from the transcript.
	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	foundTranscript := false
	for _, text := range userTexts {
		if text == "transcript-msg" {
			foundTranscript = true
		}
	}
	if !foundTranscript {
		t.Fatalf("expected transcript history in restored session, got user texts: %v", userTexts)
	}
}

func TestRestoreSessionFromMeta_NoTranscript_StartsClean(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := SessionMeta{
		ID:        "01JTEST_META_CLEAN_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 0,
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "test"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Should process fine with no history.
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
}

func TestRestoreSessionFromMeta_TranscriptWithCompaction(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := SessionMeta{
		ID:        "01JTEST_META_COMPACT_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 5,
	}

	// Write a transcript with compaction.
	tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID: meta.ID,
		CreatedAt: meta.CreatedAt,
		ProfileID: meta.ProfileID,
		Model:     meta.Model,
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	tw.Append(NewTurn(TurnUserInput, llm.User("old-msg")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("old-reply")))
	tw.Append(NewTurn(TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT] Summary")))
	tw.Append(NewTurn(TurnUserInput, llm.User("post-compact-msg")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("post-compact-reply")))
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}

	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	// Should have checkpoint + post-compact, NOT old-msg.
	foundCheckpoint := false
	foundPostCompact := false
	foundOld := false
	for _, text := range userTexts {
		if strings.Contains(text, "[CONTEXT CHECKPOINT]") {
			foundCheckpoint = true
		}
		if text == "post-compact-msg" {
			foundPostCompact = true
		}
		if text == "old-msg" {
			foundOld = true
		}
	}

	if !foundCheckpoint {
		t.Fatalf("expected checkpoint, got user texts: %v", userTexts)
	}
	if !foundPostCompact {
		t.Fatalf("expected post-compact message, got user texts: %v", userTexts)
	}
	if foundOld {
		t.Fatalf("pre-compaction messages should not appear, got user texts: %v", userTexts)
	}
}
