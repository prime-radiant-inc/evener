package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/execsupport/fsatomic"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// --- Issue 1: allToolDefinitions caching ---

func TestCachedToolDefs_MatchUncached(t *testing.T) {
	t.Parallel()
	// Verify that cached tool definitions contain all registered profile tools.
	// Expected names are derived directly from profile.ToolDefinitions() filtered
	// by reg.RegisteredNames() — the "uncached" path — so a silent drop in
	// rebuildToolDefsCache would cause names to go missing here.
	dir := t.TempDir()
	c := llm.NewClient()
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		return agenttest.FinalResponse("ok")
	})
	c.Register(f)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))

	defs := sess.allToolDefinitions(0)
	if len(defs) == 0 {
		t.Fatal("allToolDefinitions returned empty list")
	}

	// Build the expected wire names independently from the cache:
	// each profile tool that is registered in the registry should appear
	// in allToolDefinitions under its provider-mapped (wire) name.
	registered := sess.reg.RegisteredNames()
	nameMap := sess.profile.ToolNameMap() // canonical → wire; nil means identity
	wantNames := make(map[string]bool)
	for _, td := range sess.profile.ToolDefinitions() {
		if !registered[td.Name] {
			continue
		}
		wireName := td.Name
		if n, ok := nameMap[td.Name]; ok {
			wireName = n
		}
		wantNames[wireName] = true
	}
	if len(wantNames) == 0 {
		t.Fatal("no registered profile tools found — test precondition violated")
	}

	gotNames := make(map[string]bool, len(defs))
	for _, td := range defs {
		gotNames[td.Name] = true
	}
	for name := range wantNames {
		if !gotNames[name] {
			t.Errorf("tool %q (from profile+registry) missing in allToolDefinitions", name)
		}
	}
}

func TestCachedToolDefs_AlwaysIncludesCommunicate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))

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
	t.Parallel()
	// MCP tools should be included in the cached list.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))

	// Simulate MCP tools by adding to mcpTools and registering.
	mcpTool := llm.ToolDefinition{
		Name:        "mcp__server__tool1",
		Description: "An MCP tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
	sess.mcpTools = append(sess.mcpTools, mcpTool)
	_ = sess.reg.Register(tool.RegisteredTool{
		Definition: mcpTool,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
	t.Parallel()
	// Tools registered directly on the registry should be included.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))

	// Register a custom tool directly.
	customTool := tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "custom_tool",
			Description: "A custom tool",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
	t.Parallel()
	// The web_search function tool is a shim for Gemini only (see tool_web_search.go).
	// For Anthropic and OpenAI, native web search is used via req.WebSearch.
	// Including the web_search function tool for Anthropic causes a duplicate
	// "web_search" name (the adapter also injects a server-side web_search tool),
	// which Anthropic's API rejects.
	for _, tc := range []struct {
		name    string
		profile *provider.Profile
		adapter string
		want    bool // true = web_search should be present
	}{
		{"anthropic", newAnthropicProfile("claude-test"), "anthropic", false},
		{"openai", NewOpenAIProfile("gpt-test"), "openai", false},
		{"gemini", newGeminiProfile("gemini-test"), "gemini", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: tc.adapter})

			sess := newSession(t, withClient(c), withProfile(tc.profile), withDir(dir), withConfig(SessionConfig{}))
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
	t.Parallel()
	// Verify that no provider profile produces duplicate tool names in
	// allToolDefinitions, which would be rejected by APIs like Anthropic.
	for _, tc := range []struct {
		name    string
		profile *provider.Profile
		adapter string
	}{
		{"anthropic", newAnthropicProfile("claude-test"), "anthropic"},
		{"openai", NewOpenAIProfile("gpt-test"), "openai"},
		{"gemini", newGeminiProfile("gemini-test"), "gemini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: tc.adapter})

			sess := newSession(t, withClient(c), withProfile(tc.profile), withDir(dir), withConfig(SessionConfig{}))
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
	t.Parallel()
	// If an MCP tool has the same name as a profile tool (unlikely due to
	// namespacing, but possible), it should not produce a duplicate in the
	// tool definitions sent to the API.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})

	sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(dir), withConfig(SessionConfig{}))
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
	t.Parallel()
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

	comm := communicateCall("c1", "done after tools")
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		return agenttest.ToolCallResponse(shellCall("s1"))
	}, func(req llm.Request) llm.Response {
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
		return agenttest.ToolCallResponse(comm)
	})
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	out, err := sess.ProcessInput(ctx, "test", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "done after tools" {
		t.Fatalf("got %q, want %q", out, "done after tools")
	}
	sess.Close()
}

func TestAfterAction_ReceivesCurrentHistory(t *testing.T) {
	t.Parallel()
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
	comm := communicateCall("c1", "done")

	var afterActionHistory []schema.Turn
	spy := &spyStrategy{
		toolsDefs: nil,
	}

	// Override AfterAction to capture what it sees.
	capturer := &afterActionCapture{
		spyStrategy: spy,
		onAfterAction: func(history []schema.Turn) {
			afterActionHistory = append([]schema.Turn{}, history...)
		},
	}

	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		return agenttest.ToolCallResponse(shellCall("s1"))
	}, func(req llm.Request) llm.Response {
		return agenttest.ToolCallResponse(comm)
	})
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{contextStrategyOverride: capturer},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "test", nil)
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
		if turn.Kind == schema.TurnToolResults {
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
	onAfterAction func(history []schema.Turn)
}

func (a *afterActionCapture) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	if a.onAfterAction != nil {
		a.onAfterAction(history)
	}
	return a.spyStrategy.AfterAction(ctx, history, client)
}

// --- Issue 3: System prompt caching ---

func TestCachedSystemPromptComponents_SkillList(t *testing.T) {
	t.Parallel()
	// Verify that prompt data reflects the discovered skills. Memory is bound
	// and save-capable here so nothing is filtered: a session without the save
	// tools legitimately drops the gardening-memory entry (see
	// TestMemoryGardeningSkillGatedBySaveCapability).
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}))

	data, _ := sess.buildPromptData(sess.currentEnv())
	if len(sess.skills.Entries) == 0 {
		t.Fatal("no skills discovered; the length comparison below would be vacuous")
	}
	if got := len(data.Skills); got != len(sess.skills.Entries) {
		t.Errorf("buildPromptData skills length %d != discovered %d", got, len(sess.skills.Entries))
	}
}

func TestCachedSystemPromptComponents_DoesNotDuplicateMCPToolDescriptions(t *testing.T) {
	t.Parallel()
	// Verify MCP tool descriptions stay in provider tool definitions, not duplicated in the cached prompt.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{}))

	// A tool added during the session belongs in its live definitions.
	mcpTool := llm.ToolDefinition{
		Name:        "mcp__test__tool",
		Description: "Test MCP tool",
	}
	sess.mcpTools = append(sess.mcpTools, mcpTool)
	_ = sess.reg.Register(tool.RegisteredTool{
		Definition: mcpTool,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	sess.rebuildToolDefsCache()

	if strings.Contains(sess.cachedSystemPrompt, "Test MCP tool") {
		t.Errorf("cached system prompt should not duplicate MCP tool description, got: %q", sess.cachedSystemPrompt)
	}

	found := false
	for _, td := range sess.allToolDefinitions(0) {
		if td.Name == "mcp__test__tool" && td.Description == "Test MCP tool" {
			found = true
		}
	}
	if !found {
		t.Fatal("MCP tool description should remain present in provider tool definitions")
	}
}

func TestCachedSystemPromptComponents_UsesProviderVisibleToolNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), coordinatorWorkflowSessionConfig(t, SessionConfig{
		AgentName: "reviewer",
	}))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data, _ := sess.buildPromptData(sess.currentEnv())
	if !slices.Contains(data.CallableToolNames, "exec_command") {
		t.Fatalf("CallableToolNames = %q, want the provider-visible exec_command", data.CallableToolNames)
	}
	if slices.Contains(data.CallableToolNames, "shell") {
		t.Fatalf("CallableToolNames = %q, must not list the canonical shell", data.CallableToolNames)
	}
	if slices.Contains(data.UnavailableProfileToolNames, "exec_command") {
		t.Fatalf("UnavailableProfileToolNames = %q, must not mark exec_command unavailable", data.UnavailableProfileToolNames)
	}
}

func TestSystemPromptConsistency_WithAndWithoutCache(t *testing.T) {
	t.Parallel()
	// The system prompt produced with caching should match the prompt that is
	// actually sent to the model.
	dir := t.TempDir()
	c := llm.NewClient()

	var capturedSysPrompt string
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		// Capture the system prompt from the first round.
		if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
			capturedSysPrompt = req.Messages[0].Text()
		}
		return agenttest.FinalResponse("ok")
	})
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NonInteractive: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	referencePrompt := sess.cachedSystemPrompt

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// The prompt used in processOneInput should match the reference.
	if capturedSysPrompt != referencePrompt {
		// Find first difference for debugging.
		minLen := min(len(referencePrompt), len(capturedSysPrompt))
		diffIdx := minLen
		for i := range minLen {
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
	t.Parallel()
	dir := t.TempDir()

	// Create a project doc file so LoadProjectDocs has something to find.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# My Agent\nHello"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		// The count below is of repo docs only, so the personal doc is pointed at nothing regardless of the environment.
		AgentsDocPath: filepath.Join(dir, "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Session should have cached project docs.
	if len(sess.projectDocs) != 1 {
		t.Fatalf("projectDocs = %d, want the one doc written into the working directory: %+v", len(sess.projectDocs), sess.projectDocs)
	}
}

func TestSession_CachedProjectDocsUsedInSystemPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a project doc.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("cached-doc-content"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
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

func TestSession_PersonalDocUsedInSystemPrompt(t *testing.T) {
	// t.Setenv rules out t.Parallel, and the config root has to be a temp dir so
	// the run never picks up the developer's own personal doc.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configRoot := filepath.Join(configHome, "evener")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, UserDocFile), []byte("personal-doc-content"), 0o644); err != nil {
		t.Fatalf("write personal %s: %v", UserDocFile, err)
	}

	// The working directory holds no instruction file at all: the personal doc
	// reaches the prompt only through the session's config-root wiring.
	dir := t.TempDir()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		testOnly:              testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
	// System prompt is the first message (role=system).
	if len(reqs[0].Messages) == 0 {
		t.Fatal("expected at least 1 message in request")
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "personal-doc-content") {
		t.Fatalf("system prompt should contain the personal doc content, got: %s", sys[:min(200, len(sys))])
	}
}

// ---------------------------------------------------------------------------
// Issue 2: SessionMeta — lightweight save instead of full snapshot
// ---------------------------------------------------------------------------

func testSessionMeta() schema.SessionMeta {
	return schema.SessionMeta{
		ID:        "01JTEST_META_00000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: schema.ConfigSnapshot{
			MaxToolRoundsPerInput: 200,
			ReasoningEffort:       "high",
		},
		EnvInfo: schema.EnvironmentInfo{
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
	t.Parallel()
	orig := testSessionMeta()
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got schema.SessionMeta
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
	t.Parallel()
	dir := t.TempDir()
	meta := testSessionMeta()

	if err := schema.SaveSessionMeta(dir, meta); err != nil {
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

	var got schema.SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("saved id: got %q want %q", got.ID, meta.ID)
	}
}

func TestLoadSessionMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	meta := testSessionMeta()

	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	got, err := schema.LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("id: got %q want %q", got.ID, meta.ID)
	}
	if got.Model != meta.Model {
		t.Fatalf("model: got %q want %q", got.Model, meta.Model)
	}
	if got.ProfileID != meta.ProfileID {
		t.Fatalf("profile_id: got %q want %q", got.ProfileID, meta.ProfileID)
	}
	if got.TurnCount != meta.TurnCount {
		t.Fatalf("turn_count: got %d want %d", got.TurnCount, meta.TurnCount)
	}
	if got.Config.MaxToolRoundsPerInput != meta.Config.MaxToolRoundsPerInput {
		t.Fatalf("config.max_tool_rounds_per_input: got %d want %d", got.Config.MaxToolRoundsPerInput, meta.Config.MaxToolRoundsPerInput)
	}
	if got.Config.ReasoningEffort != meta.Config.ReasoningEffort {
		t.Fatalf("config.reasoning_effort: got %q want %q", got.Config.ReasoningEffort, meta.Config.ReasoningEffort)
	}
	if got.EnvInfo.WorkingDir != meta.EnvInfo.WorkingDir {
		t.Fatalf("env_info.working_dir: got %q want %q", got.EnvInfo.WorkingDir, meta.EnvInfo.WorkingDir)
	}
}

func TestLoadSessionMeta_NotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := schema.LoadSessionMeta(dir, "NONEXISTENT")
	if err == nil {
		t.Fatal("expected error for nonexistent meta")
	}
}

func TestListSessionMetas_SortedByUpdatedAt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	meta1 := testSessionMeta()
	meta1.ID = "02wMz5Txv1C3Hut0M8GCeB"
	meta1.UpdatedAt = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	meta2 := testSessionMeta()
	meta2.ID = "02wMz5Txv2enqVTitaig6F"
	meta2.UpdatedAt = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	meta3 := testSessionMeta()
	meta3.ID = "02wMz5TxvEMoJEDTDGOTil"
	meta3.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	for _, m := range []schema.SessionMeta{meta1, meta2, meta3} {
		if err := schema.SaveSessionMeta(dir, m); err != nil {
			t.Fatalf("SaveSessionMeta %s: %v", m.ID, err)
		}
	}

	list, err := schema.ListSessionMetas(dir)
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
	t.Parallel()
	dir := t.TempDir()
	list, err := schema.ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestSession_MaybeAutoSave_WritesMetaNotSnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		StateDir:              dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
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
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := schema.SessionMeta{
		ID:        "01JTEST_META_RESTORE_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 200},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 2,
		PromptLaunch: schema.PromptLaunchFacts{
			TurnEndsProcess: new(true),
		},
	}

	// Write a transcript file with history.
	tpath := schema.SessionTranscriptPath(stateDir, meta.ID)
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: meta.ID,
		CreatedAt: meta.CreatedAt,
		ProfileID: meta.ProfileID,
		Model:     meta.Model,
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("transcript-msg"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("transcript-reply"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	if sess.ID() != meta.ID {
		t.Fatalf("id: got %q want %q", sess.ID(), meta.ID)
	}
	if mode := sess.Meta().PromptLaunch.TurnEndsProcess; mode == nil || !*mode {
		t.Error("restore with an external snapshot and no metadata file lost its supplied launch mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
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
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()
	appendFile := filepath.Join(dir, "append.md")
	const instruction = "RECOVERED_PROMPT_APPEND_126"
	writeTestFile(t, appendFile, instruction)

	meta := schema.SessionMeta{
		ID:        identifier.MustNewSessionID(),
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 200, SystemPromptAppend: []string{appendFile}},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 0,
		PromptLaunch: schema.PromptLaunchFacts{
			KnowledgeCutoff: new("absent-transcript-cutoff"),
			TurnEndsProcess: new(true),
		},
	}
	persisted := meta
	persisted.PinnedNote = "note-saved-after-restore-snapshot"
	if err := schema.SaveSessionMeta(stateDir, persisted); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	replacement, err := schema.LoadSessionMeta(stateDir, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	cutoff, mode := replacement.PromptLaunch.KnowledgeCutoff, replacement.PromptLaunch.TurnEndsProcess
	if cutoff == nil || *cutoff != sess.profile.KnowledgeCutoff() || mode == nil || *mode {
		t.Errorf("replacement transcript retained the missing prompt's startup facts: %+v", replacement.PromptLaunch)
	}
	if replacement.PinnedNote != persisted.PinnedNote {
		t.Error("publishing the replacement prompt's facts overwrote another metadata change")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "test", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Should process fine with no history.
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
	header, _, _, err := readTranscript(t.Context(), schema.SessionTranscriptPath(stateDir, meta.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header.SystemPrompt, instruction) || reqs[0].Messages[0].Text() != header.SystemPrompt {
		t.Fatal("the replacement transcript must contain the initial prompt sent to the provider")
	}

	profile := withTestSessionNamer(c, NewOpenAIProfile(meta.Model))
	restore := func(saved schema.SessionMeta, cfg RestoreSessionConfig) (*Session, error) {
		return RestoreSessionFromMetaWithConfig(c, profile, execenv.NewLocalExecutionEnvironment(dir), saved, cfg)
	}
	start := func(fresh bool, cfg RestoreSessionConfig) (*Session, error) {
		if !fresh {
			return restore(meta, cfg)
		}
		return NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			SessionID: meta.ID, StateDir: cfg.StateDir, TurnEndsProcess: cfg.TurnEndsProcess,
			ForceRealIO: cfg.ForceRealIO, testOnly: cfg.testOnly,
		})
	}
	lastRequest := func(t *testing.T, s *Session) (string, string) {
		t.Helper()
		requests := adapter.Requests()
		prompt, history := responsesInstructionsAndHistory(responsesPromptBody(t, requests[len(requests)-1], s.profile.Resolved()))
		var observations envctx.Tracker
		for _, msg := range history {
			observations.ReplayBlock(msg)
		}
		return prompt, observations.State().Last.ExecutionMode
	}
	for _, tc := range []struct {
		name                                     string
		freshWinner, freshContender, closeWinner bool
	}{
		{name: "winning writer open"},
		{name: "winning writer closed", closeWinner: true},
		{name: "fresh writer wins", freshWinner: true},
		{name: "fresh launch during replacement", freshContender: true},
		{name: "fresh retry after replacement closed", freshContender: true, closeWinner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
			beforeRecovery, err := schema.LoadSessionMeta(stateDir, meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			cfg := RestoreSessionConfig{StateDir: stateDir, ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}
			contenderCfg := cfg
			contenderCfg.TurnEndsProcess = true
			entered, resume := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			defer release()
			contenderCfg.testOnly.sessionInitFault = func(point string) error {
				if point == "builtin_agents" {
					close(entered)
					<-resume
				}
				return nil
			}
			type result struct {
				session *Session
				err     error
			}
			finished := make(chan result, 1)
			go func() {
				s, err := start(tc.freshContender, contenderCfg)
				finished <- result{s, err}
			}()
			select {
			case <-entered:
			case r := <-finished:
				t.Fatalf("contender exited before prompt initialization: %v", r.err)
			}
			if tc.freshContender && !tc.closeWinner {
				// Recovery has checked the final path and holds the publication lock.
				cfg.testOnly.sessionInitFault = func(point string) error {
					if point == "restore_new_transcript" {
						release()
					}
					return nil
				}
			}
			winner, err := start(tc.freshWinner, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(winner.Close)
			if _, err := winner.ProcessInput(t.Context(), "winning input", nil); err != nil {
				t.Fatal(err)
			}
			path := schema.SessionTranscriptPath(stateDir, meta.ID)
			prefix, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			winnerPrompt := winner.transcript.Header().SystemPrompt
			if tc.closeWinner {
				winner.Close()
			}
			release()
			r := <-finished
			if r.session != nil {
				r.session.Close()
			}
			if !errors.Is(r.err, os.ErrExist) {
				t.Errorf("contending publisher returned %v, want the existing transcript", r.err)
			}
			winner.Close()
			current := loadMetaForTest(t, winner)
			if mode := current.PromptLaunch.TurnEndsProcess; mode == nil || *mode {
				t.Error("one-shot contender replaced the winning daemon's launch facts")
			}
			cfg.TurnEndsProcess = true
			// One caller loaded its snapshot before either recovery began.
			supplied := current
			if tc.closeWinner {
				supplied = beforeRecovery
			}
			supplied.PinnedNote = "caller-pinned-823"
			cfg.CompactAtPercent = 63
			cold, err := restore(supplied, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close()
			if got := cold.Meta(); got.PinnedNote != supplied.PinnedNote || got.Config.CompactAtPercent != cfg.CompactAtPercent {
				t.Error("restore replaced the caller's pinned work or runtime compaction setting")
			}
			if _, err := cold.ProcessInput(t.Context(), "cold input", nil); err != nil {
				t.Fatal(err)
			}
			if prompt, mode := lastRequest(t, cold); prompt != winnerPrompt || mode != envctx.OneShotMode {
				t.Error("cold one-shot request lost the daemon prompt or its current job-lifetime guidance")
			}
			all, err := os.ReadFile(path)
			if err != nil || !bytes.HasPrefix(all, prefix) {
				t.Errorf("winning transcript prefix lost on restore: %v", err)
			}
		})
	}
	t.Run("fresh header observed before initial autosave", func(t *testing.T) {
		cfg := SessionConfig{SessionID: meta.ID, StateDir: t.TempDir()}
		var reader *Session
		cfg.spawn.descendantEvent = func(ev events.SessionEvent) {
			if ev.Kind == events.EventSessionStart {
				var err error
				reader, err = restore(meta, RestoreSessionConfig{StateDir: cfg.StateDir, TurnEndsProcess: true})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		winner, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer winner.Close()
		if reader == nil {
			t.Fatal("fresh launch did not publish its start")
		}
		defer reader.Close()
		if mode := reader.Meta().PromptLaunch.TurnEndsProcess; mode == nil || *mode || reader.cachedSystemPrompt != winner.cachedSystemPrompt {
			t.Error("the new header and the facts read for its job-lifetime guidance describe different launches")
		}
	})
	for _, missingFacts := range []bool{false, true} {
		name := "owned reader opens a published replacement"
		if missingFacts {
			name = "owned reader keeps supplied facts when sidecar disappears"
		}
		t.Run(name, func(t *testing.T) {
			cfg := RestoreSessionConfig{StateDir: t.TempDir()}
			cfg.TurnEndsProcess = missingFacts
			if err := schema.SaveSessionMeta(cfg.StateDir, meta); err != nil {
				t.Fatal(err)
			}
			var winnerPrompt string
			owned := cfg
			owned.AcquireSessionOwnership = func(string) error { return nil }
			owned.TurnEndsProcess, owned.CompactAtPercent = !cfg.TurnEndsProcess, 63
			owned.testOnly.metaFS = &afterPromptMetaOpenFS{Fs: afero.NewOsFs(), afterOpen: sync.OnceFunc(func() {
				// This descriptor retains the owned snapshot while the public
				// restore publishes a replacement prompt and its launch facts.
				winner, err := restore(meta, cfg)
				if err != nil {
					t.Fatal(err)
				}
				winnerPrompt = winner.transcript.Header().SystemPrompt
				winner.Close()
				if missingFacts {
					if err := os.Remove(schema.SessionMetaPath(cfg.StateDir, meta.ID)); err != nil {
						t.Fatal(err)
					}
				}
			})}
			reader, err := restore(meta, owned)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if reader.Meta().Config.CompactAtPercent != owned.CompactAtPercent {
				t.Fatal("owned restore lost its runtime compaction setting")
			}
			if _, err := reader.ProcessInput(t.Context(), "owned input", nil); err != nil {
				t.Fatal(err)
			}
			prompt, mode := lastRequest(t, reader)
			wantMode := envctx.OneShotMode
			if missingFacts {
				wantMode = envctx.DaemonMode
			}
			launch := reader.Meta().PromptLaunch.TurnEndsProcess
			if winnerPrompt == "" || prompt != winnerPrompt || launch == nil || *launch != cfg.TurnEndsProcess || mode != wantMode {
				t.Error("owned request lost the published prompt, launch facts, or current job-lifetime guidance")
			}
		})
	}
	t.Run("fresh retry after a partial first header", func(t *testing.T) {
		cfg := RestoreSessionConfig{StateDir: t.TempDir(), ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}
		err := schema.PublishSessionTranscript(nil, cfg.StateDir, meta, false, func(path string) error {
			if err := os.WriteFile(path, []byte(`{"kind":"header"`), 0o600); err != nil {
				return err
			}
			return io.ErrShortWrite
		}, fsatomic.SyncDir)
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("partial header write: %v", err)
		}
		if _, err := os.Lstat(schema.SessionTranscriptPath(cfg.StateDir, meta.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("failed header at the final path: %v", err)
		}
		retry, err := start(true, cfg)
		if err != nil {
			t.Fatalf("fresh retry with the same ID: %v", err)
		}
		defer retry.Close()
		if _, err := retry.ProcessInput(t.Context(), "continued after a partial header", nil); err != nil {
			t.Fatal(err)
		}
		if prompt, _ := lastRequest(t, retry); prompt != retry.transcript.Header().SystemPrompt {
			t.Error("the retried request did not use its published prompt")
		}
	})
	for _, tc := range []struct {
		name                    string
		fresh, repeated, reopen bool
	}{
		{name: "fresh header directory sync", fresh: true},
		{name: "replacement header directory sync"},
		{name: "fresh header directory sync unavailable at admission", fresh: true, repeated: true},
		{name: "replacement header directory sync unavailable at admission", repeated: true},
		{name: "directory sync still unavailable after reopening", fresh: true, repeated: true, reopen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := RestoreSessionConfig{StateDir: t.TempDir(), ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}
			if !tc.fresh {
				if err := schema.SaveSessionMeta(cfg.StateDir, meta); err != nil {
					t.Fatal(err)
				}
			}
			path := schema.SessionTranscriptPath(cfg.StateDir, meta.ID)
			var publishedHeader []byte
			syncs := 0
			unavailable := tc.repeated
			cfg.testOnly.sessionInitFault = func(point string) error {
				if point != "sync_transcript_publication" {
					return nil
				}
				syncs++
				if syncs == 1 {
					var err error
					publishedHeader, err = os.ReadFile(path)
					if err != nil {
						t.Fatalf("read published transcript at directory sync: %v", err)
					}
				}
				if syncs == 1 || unavailable {
					return syscall.EIO
				}
				return nil
			}
			s, err := start(tc.fresh, cfg)
			if err != nil {
				t.Fatalf("start with published header: %v", err)
			}
			defer s.Close()
			if syncs != 2 {
				t.Errorf("startup made %d directory sync attempts, want 2", syncs)
			}
			served := serveFailClosedSession(s)
			var startupEvents []events.SessionEvent
			if tc.repeated {
				if err := s.transcript.EstablishDurability(); !errors.Is(err, syscall.EIO) {
					t.Errorf("an explicit durability barrier returned %v during the directory failure", err)
				}
				otherWriter, _, err := transcript.OpenWriterForSession(path, meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err := otherWriter.AppendSynced(schema.NewTurn(schema.TurnUserInput, llm.User("refused from another writer"))); !errors.Is(err, syscall.EIO) {
					t.Errorf("a second writer returned %v during the directory failure", err)
				}
				if err := otherWriter.Close(); err != nil {
					t.Fatal(err)
				}
				before := len(adapter.Requests())
				if _, err := s.ProcessInput(t.Context(), "refused during sync failure", nil); !errors.Is(err, syscall.EIO) {
					t.Errorf("served input during the directory failure: %v", err)
				}
				if len(adapter.Requests()) != before {
					t.Error("input reached the provider with its transcript directory unsynced")
				}
				_, entries, _, err := readTranscript(t.Context(), path, "")
				if err != nil {
					t.Fatal(err)
				}
				if slices.ContainsFunc(entries, func(e transcript.Entry) bool { return e.Turn.Kind == schema.TurnUserInput }) {
					t.Error("input was recorded before its transcript pathname was durable")
				}
				if tc.reopen {
					startupEvents = served.settle(s)
					s, err = restore(loadMetaForTest(t, s), cfg)
					if err != nil {
						t.Fatalf("reopen while directory sync is unavailable: %v", err)
					}
					defer s.Close()
					served = serveFailClosedSession(s)
					if _, err := s.ProcessInput(t.Context(), "refused after reopening", nil); !errors.Is(err, syscall.EIO) {
						t.Errorf("reopened session admitted input without establishing directory durability: %v", err)
					}
					if len(adapter.Requests()) != before {
						t.Error("reopened session sent a provider request before its directory was synced")
					}
				}
				unavailable = false
			}
			if _, err := s.ProcessInput(t.Context(), "continue after the sync failure", nil); err != nil {
				t.Fatalf("served input after published header: %v", err)
			}
			if prompt, _ := lastRequest(t, s); prompt != s.transcript.Header().SystemPrompt {
				t.Error("request did not use the published header's system prompt")
			}
			systemPrompt := s.transcript.Header().SystemPrompt
			evs := slices.Concat(startupEvents, served.settle(s))
			if !slices.ContainsFunc(evs, func(ev events.SessionEvent) bool {
				warning, ok := ev.Data.(events.WarningData)
				return ok && ev.Kind == events.EventWarning && warning.Code == "transcript_publication_sync"
			}) {
				t.Error("the sync failure was not reported to the served event stream")
			}
			transcriptBytes, err := os.ReadFile(path)
			if err != nil || !bytes.HasPrefix(transcriptBytes, publishedHeader) || len(transcriptBytes) == len(publishedHeader) {
				t.Fatalf("served session did not append to the published header: %v", err)
			}
			current := loadMetaForTest(t, s)
			if mode := current.PromptLaunch.TurnEndsProcess; mode == nil || *mode {
				t.Errorf("published metadata did not retain the session's launch facts: %+v", current.PromptLaunch)
			}
			cfg.testOnly.sessionInitFault = nil
			cfg.TurnEndsProcess = true
			cold, err := restore(current, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close()
			if _, err := cold.ProcessInput(t.Context(), "cold input", nil); err != nil {
				t.Fatal(err)
			}
			if prompt, mode := lastRequest(t, cold); prompt != systemPrompt || mode != envctx.OneShotMode {
				t.Error("cold request lost the published prompt or its updated job-lifetime guidance")
			}
		})
	}
	t.Run("fresh metadata sync fails independently of the transcript", func(t *testing.T) {
		fs := &promptMetaSyncFailureFS{Fs: afero.NewOsFs()}
		fs.fail.Store(true)
		cfg := RestoreSessionConfig{StateDir: t.TempDir(), ForceRealIO: true, testOnly: testConfig{
			skipGitSnapshot: true, noSyncJobStore: true, metaFS: fs,
		}}
		s, err := start(true, cfg)
		if err != nil {
			t.Fatalf("fresh publication: %v", err)
		}
		defer s.Close()
		_, err = schema.LoadSessionMeta(cfg.StateDir, meta.ID)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("fresh startup published metadata despite failed content sync: %v", err)
		}

		fs.fail.Store(false)
		if _, err := s.ProcessInput(t.Context(), "retry metadata persistence", nil); err != nil {
			t.Fatal(err)
		}
		if prompt, _ := lastRequest(t, s); prompt != s.transcript.Header().SystemPrompt {
			t.Error("request did not use the published header's system prompt")
		}
		checkpoint := loadMetaForTest(t, s)
		if mode := checkpoint.PromptLaunch.TurnEndsProcess; mode == nil || *mode {
			t.Fatalf("retry omitted the visible prompt's launch facts: %+v", checkpoint.PromptLaunch)
		}

		fs.fail.Store(true)
		if _, err := s.ProcessInput(t.Context(), "continue during checkpoint failure", nil); err != nil {
			t.Fatal(err)
		}
		if got := loadMetaForTest(t, s); got.Revision != checkpoint.Revision {
			t.Errorf("failed autosave replaced revision %d with %d", checkpoint.Revision, got.Revision)
		}
		fs.fail.Store(false)
		if _, err := s.ProcessInput(t.Context(), "checkpoint after recovery", nil); err != nil {
			t.Fatal(err)
		}
		if got := loadMetaForTest(t, s); got.Revision <= checkpoint.Revision ||
			!reflect.DeepEqual(got.PromptLaunch, checkpoint.PromptLaunch) {
			t.Error("retry did not advance the checkpoint with the original launch facts")
		}
	})
	for _, failure := range []string{"metadata write", "metadata sync", "header write"} {
		t.Run("retry after "+failure, func(t *testing.T) {
			cfg := RestoreSessionConfig{StateDir: t.TempDir(), ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}
			if err := schema.SaveSessionMeta(cfg.StateDir, meta); err != nil {
				t.Fatal(err)
			}
			metaPath := schema.SessionMetaPath(cfg.StateDir, meta.ID)
			before, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			failedCfg, failedMeta := cfg, meta
			switch failure {
			case "metadata write":
				failedCfg.testOnly.metaFS = &notesRenameFailureFS{Fs: afero.NewOsFs(), fail: true, err: os.ErrPermission}
			case "metadata sync":
				fs := &promptMetaSyncFailureFS{Fs: afero.NewOsFs()}
				fs.fail.Store(true)
				failedCfg.testOnly.metaFS = fs
			case "header write":
				failedMeta.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			failed, err := restore(failedMeta, failedCfg)
			if err == nil {
				failed.Close()
				t.Fatal("restore unexpectedly succeeded")
			}
			var marshalErr *json.MarshalerError
			if failure == "metadata write" && !errors.Is(err, os.ErrPermission) ||
				failure == "metadata sync" && !errors.Is(err, syscall.EIO) ||
				failure == "header write" && !errors.As(err, &marshalErr) {
				t.Fatalf("restore did not reach the intended %s failure: %v", failure, err)
			}
			if _, err := os.Stat(schema.SessionTranscriptPath(cfg.StateDir, meta.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("failed publication left a transcript at the recovery path: %v", err)
			}
			if after, err := os.ReadFile(metaPath); err != nil || !bytes.Equal(after, before) {
				t.Errorf("failed publication replaced the last usable metadata: %v", err)
			}
			cfg.TurnEndsProcess = true
			recovered, err := restore(meta, cfg)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			defer recovered.Close()
			current := loadMetaForTest(t, recovered)
			if mode := current.PromptLaunch.TurnEndsProcess; mode == nil || !*mode || recovered.transcript.Header().SystemPrompt != recovered.cachedSystemPrompt {
				t.Error("retry did not publish its own prompt and launch facts")
			}
		})
	}
}

type promptMetaSyncFailureFS struct {
	afero.Fs
	fail atomic.Bool
}

func (fs *promptMetaSyncFailureFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err == nil && (strings.HasSuffix(name, ".meta.json.tmp") || strings.HasSuffix(name, ".meta.json")) {
		return promptMetaSyncFailureFile{File: f, fail: &fs.fail}, nil
	}
	return f, err
}

type promptMetaSyncFailureFile struct {
	afero.File
	fail *atomic.Bool
}

func (f promptMetaSyncFailureFile) Sync() error {
	if f.fail.Load() {
		return syscall.EIO
	}
	return f.File.Sync()
}

type afterPromptMetaOpenFS struct {
	afero.Fs
	afterOpen func()
}

func (fs *afterPromptMetaOpenFS) Open(name string) (afero.File, error) {
	f, err := fs.Fs.Open(name)
	if err == nil && strings.HasSuffix(name, ".meta.json") {
		fs.afterOpen()
	}
	return f, err
}

func TestRestoreSessionFromMeta_TranscriptWithCompaction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := schema.SessionMeta{
		ID:        "01JTEST_META_COMPACT_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 200},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 5,
	}

	// Write a transcript with compaction and an empty saved system prompt.
	tpath := schema.SessionTranscriptPath(stateDir, meta.ID)
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: meta.ID,
		CreatedAt: meta.CreatedAt,
		ProfileID: meta.ProfileID,
		Model:     meta.Model,
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("old-msg")))
	tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("old-reply")))
	tw.Append(schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT] Summary")))
	tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("post-compact-msg")))
	tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("post-compact-reply")))
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}

	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleSystem && m.Text() != "" {
			t.Fatalf("restored empty system prompt was replaced with %d bytes", len(m.Text()))
		}
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
