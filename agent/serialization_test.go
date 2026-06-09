package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func TestTurn_JSONRoundTrip(t *testing.T) {
	orig := schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Assistant("hello world"),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Verify snake_case JSON keys.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, ok := raw["kind"]; !ok {
		t.Fatalf("expected 'kind' key in JSON, got keys: %v", raw)
	}
	if _, ok := raw["message"]; !ok {
		t.Fatalf("expected 'message' key in JSON, got keys: %v", raw)
	}

	var got schema.Turn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != orig.Kind {
		t.Fatalf("kind: got %q want %q", got.Kind, orig.Kind)
	}
	if got.Message.Role != orig.Message.Role {
		t.Fatalf("role: got %q want %q", got.Message.Role, orig.Message.Role)
	}
	if got.Message.Text() != orig.Message.Text() {
		t.Fatalf("text: got %q want %q", got.Message.Text(), orig.Message.Text())
	}
}

func TestTurn_JSONRoundTrip_ToolResult(t *testing.T) {
	orig := schema.Turn{
		Kind:    schema.TurnTool,
		Message: llm.ToolResultNamed("call-123", "read_file", "file contents here", false),
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got schema.Turn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != schema.TurnTool {
		t.Fatalf("kind: got %q want %q", got.Kind, schema.TurnTool)
	}
	if got.Message.ToolCallID != "call-123" {
		t.Fatalf("tool_call_id: got %q want %q", got.Message.ToolCallID, "call-123")
	}
}

func TestTurn_JSONRoundTrip_ToolResults(t *testing.T) {
	orig := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "read_file", Content: "file A contents", IsError: false}},
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c2", Name: "write_file", Content: "wrote ok", IsError: false}},
			},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got schema.Turn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != schema.TurnToolResults {
		t.Fatalf("kind: got %q want %q", got.Kind, schema.TurnToolResults)
	}
	if len(got.Message.Content) != 2 {
		t.Fatalf("content parts: got %d want 2", len(got.Message.Content))
	}
	if got.Message.Content[0].ToolResult.ToolCallID != "c1" {
		t.Fatalf("content[0].tool_call_id: got %q want %q", got.Message.Content[0].ToolResult.ToolCallID, "c1")
	}
	if got.Message.Content[1].ToolResult.ToolCallID != "c2" {
		t.Fatalf("content[1].tool_call_id: got %q want %q", got.Message.Content[1].ToolResult.ToolCallID, "c2")
	}
}

func TestToolOutputLimit_JSONRoundTrip(t *testing.T) {
	orig := schema.ToolOutputLimit{
		MaxChars: 50000,
		MaxLines: 200,
		Strategy: schema.TruncHeadTail,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Verify field names in JSON.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, ok := raw["max_chars"]; !ok {
		t.Fatalf("expected max_chars key in JSON, got keys: %v", raw)
	}
	if _, ok := raw["max_lines"]; !ok {
		t.Fatalf("expected max_lines key in JSON, got keys: %v", raw)
	}
	if _, ok := raw["strategy"]; !ok {
		t.Fatalf("expected strategy key in JSON, got keys: %v", raw)
	}

	var got schema.ToolOutputLimit
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Fatalf("round-trip: got %+v want %+v", got, orig)
	}
}

func TestEnvironmentInfo_JSONRoundTrip(t *testing.T) {
	orig := schema.EnvironmentInfo{
		WorkingDir:            "/home/user/project",
		Platform:              "linux",
		OSVersion:             "Ubuntu 22.04",
		Today:                 "2025-01-15",
		KnowledgeCutoff:       "2025-04-01",
		IsGitRepo:             true,
		GitBranch:             "main",
		GitModifiedFiles:      3,
		GitUntrackedFiles:     1,
		GitRecentCommitTitles: []string{"fix: typo", "feat: add login"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	// Verify snake_case keys.
	for _, key := range []string{"working_dir", "platform", "is_git_repo", "git_branch"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("expected %q key in JSON, got keys: %v", key, raw)
		}
	}

	var got schema.EnvironmentInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.WorkingDir != orig.WorkingDir {
		t.Fatalf("working_dir: got %q want %q", got.WorkingDir, orig.WorkingDir)
	}
	if got.GitBranch != orig.GitBranch {
		t.Fatalf("git_branch: got %q want %q", got.GitBranch, orig.GitBranch)
	}
	if got.GitModifiedFiles != orig.GitModifiedFiles {
		t.Fatalf("git_modified_files: got %d want %d", got.GitModifiedFiles, orig.GitModifiedFiles)
	}
	if len(got.GitRecentCommitTitles) != len(orig.GitRecentCommitTitles) {
		t.Fatalf("git_recent_commit_titles: got %v want %v", got.GitRecentCommitTitles, orig.GitRecentCommitTitles)
	}
}

func TestSessionConfig_JSONOmitsFunctionFields(t *testing.T) {
	cfg := SessionConfig{
		MaxToolRoundsPerInput:   200,
		MaxTurns:                50,
		DefaultCommandTimeoutMS: 10000,
		UserInstructionOverride: "be helpful",
		ReasoningEffort:         "high",
		LLMSleep:                func(ctx context.Context, d time.Duration) error { return nil }, // function field; should be omitted
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	// Function fields should not appear in JSON.
	for _, key := range []string{"llm_sleep", "LLMSleep"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("function field %q should not be in JSON", key)
		}
	}
	// Serializable fields should be present.
	if _, ok := raw["max_tool_rounds_per_input"]; !ok {
		t.Fatalf("expected max_tool_rounds_per_input key in JSON, got keys: %v", raw)
	}
	if _, ok := raw["reasoning_effort"]; !ok {
		t.Fatalf("expected reasoning_effort key in JSON, got keys: %v", raw)
	}

	// Round-trip the serializable subset.
	var got SessionConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MaxToolRoundsPerInput != cfg.MaxToolRoundsPerInput {
		t.Fatalf("max_tool_rounds: got %d want %d", got.MaxToolRoundsPerInput, cfg.MaxToolRoundsPerInput)
	}
	if got.ReasoningEffort != cfg.ReasoningEffort {
		t.Fatalf("reasoning_effort: got %q want %q", got.ReasoningEffort, cfg.ReasoningEffort)
	}
	if got.LLMSleep != nil {
		t.Fatalf("expected LLMSleep to be nil after round-trip")
	}
}

// TestSessionConfig_SpawnFieldsDropOnPersist pins the load-bearing persistence
// invariant for the spawn sub-struct: spawnConfig is tagged json:"-" on
// SessionConfig, so a populated spawn drops entirely on marshal and is the zero
// spawnConfig on unmarshal. This reproduces the pre-refactor per-field json:"-"
// behavior the subagent restore path depends on -- a restored subagent must get
// spawn.depth==0 / spawn.parentSessionID=="" so ATIF root-export gating and the
// subagent-management-is-top-level guards stay correct. If this test fails,
// someone likely added json tags to spawnConfig; do not do that.
func TestSessionConfig_SpawnFieldsDropOnPersist(t *testing.T) {
	cfg := SessionConfig{
		MaxToolRoundsPerInput: 200,
		spawn: spawnConfig{
			parentSessionID:      "01PARENT",
			parentToolCallID:     "call_abc",
			parentSteer:          func(string) {},
			subagentTask:         "do the thing",
			depth:                3,
			sharedTaskStore:      task.NewTaskStore("", "01PARENT"),
			rolePromptOverride:   "be a reviewer",
			activatedSkillBodies: []string{"skill-body"},
			allowedToolNames:     []string{"read_file"},
			deniedToolNames:      []string{"shell"},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// No spawn key and none of the nested field names leak into the JSON.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{
		"spawn", "parent_session_id", "parentSessionID", "subagent_task",
		"parent_steer", "parentSteer", "depth", "shared_task_store", "role_prompt_override",
		"allowed_tool_names", "denied_tool_names",
	} {
		if _, ok := raw[key]; ok {
			t.Fatalf("spawn-related key %q must not appear in persisted JSON, got keys: %v", key, raw)
		}
	}

	// Round-trip yields the zero spawnConfig (drop-on-persist + zero-on-restore).
	// spawnConfig has slice fields, so check the components rather than == .
	var got SessionConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	s := got.spawn
	if s.parentSessionID != "" || s.parentToolCallID != "" || s.subagentTask != "" ||
		s.parentSteer != nil || s.depth != 0 || s.sharedTaskStore != nil || s.rolePromptOverride != "" ||
		s.activatedSkillBodies != nil || s.allowedToolNames != nil || s.deniedToolNames != nil {
		t.Fatalf("restored spawn must be the zero spawnConfig, got %+v", s)
	}
}

func TestSession_ID_ReturnsULID(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	id := sess.ID()
	if id == "" {
		t.Fatalf("ID() returned empty string")
	}
	// ULID is 26 uppercase alphanumeric characters.
	if !regexp.MustCompile(`^[0-9A-Z]{26}$`).MatchString(id) {
		t.Fatalf("ID() %q is not a valid ULID", id)
	}
}
