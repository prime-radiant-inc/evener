package agent

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
)

// fullSessionConfig builds a SessionConfig with every persisted wire field set
// to a non-zero value, plus one engine-only field (StateDir) to prove engine
// fields are excluded from the persisted form. It is the shared fixture for the
// golden meta.json characterization and the SessionConfig<->ConfigSnapshot
// converter-fidelity test below.
func fullSessionConfig() SessionConfig {
	disableLoop := false
	return SessionConfig{
		MaxToolRoundsPerInput:   150,
		MaxTurns:                20,
		DefaultCommandTimeoutMS: 5000,
		MaxCommandTimeoutMS:     300000,
		MaxSubagentDepth:        2,
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"shell": {MaxChars: 1000, MaxLines: 50, Strategy: schema.TruncHeadTail},
		},
		UserInstructionOverride:     "be concise",
		AgentName:                   "reviewer",
		ReasoningEffort:             "high",
		SkillsDirs:                  []string{"/a/skills"},
		MCPConfigFiles:              []string{"/a/mcp.json"},
		MCPInline:                   []string{"srv:cmd --flag"},
		PluginDirs:                  []string{"/a/plugins"},
		SystemPromptFile:            "/a/prompt.txt",
		SystemPromptAppend:          []string{"/a/append.txt"},
		NoProjectPrompts:            true,
		NonInteractive:              true,
		ContextStrategy:             "compact",
		ShareTasksWithChildren:      true,
		ResultToolName:              "respond",
		EnableLoopDetection:         &disableLoop,
		LoopDetectionWindow:         15,
		SystemPromptAsUser:          true,
		ModelFallbacks:              []string{"openai/gpt-5", "anthropic/claude"},
		OpenAIResponsesContinuation: "auto",
		StateDir:                    "/engine/state", // engine-only: must NOT reach the persisted form
	}
}

// goldenMeta builds a SessionMeta with every serializable field set to a
// non-zero value, so marshaling it exercises the complete meta.json wire format
// — all 24 SessionConfig wire fields (projected through ConfigSnapshot), the
// full EnvironmentInfo (including the nested WorkspaceInfo), and every
// SessionMeta field. It is the fixture for the persistence-carve
// characterization tests below.
func goldenMeta() schema.SessionMeta {
	return schema.SessionMeta{
		ID:        "01JTESTGOLDEN0000000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    fullSessionConfig().toSnapshot(),
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir:            "/work",
			Platform:              "darwin",
			OSVersion:             "Darwin 25.5.0",
			Today:                 "2026-06-02",
			KnowledgeCutoff:       "2026-01-01",
			IsGitRepo:             true,
			GitBranch:             "main",
			GitOriginURL:          "git@github.com:x/y.git",
			GitModifiedFiles:      3,
			GitUntrackedFiles:     1,
			GitRecentCommitTitles: []string{"fix a", "feat b"},
			Workspace:             schema.WorkspaceInfo{Tree: "root/\n  a.go", BuildInfo: "Go module"},
		},
		CreatedAt:       time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount:       7,
		LastInputTokens: 12345,
		Name:            "Golden Session",
		NameSource:      "prompt",
		NameUpdatedAt:   time.Date(2026, 1, 15, 10, 4, 0, 0, time.UTC),
		OriginalPrompt:  "do the thing",
		ParentSessionID: "01PARENT",
		DivergenceTurn:  4,
		ForkLabel:       "before TDD",
		IsSubagent:      true,
		Origin:          "test",
		Goal: &schema.GoalSnapshot{
			Objective:        "write all the tests",
			Status:           "active",
			Iterations:       3,
			NoProgressStreak: 1,
			MadeProgressOnce: true,
			StopReason:       "no progress",
			CreatedAt:        time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			UpdatedAt:        time.Date(2026, 1, 15, 10, 3, 0, 0, time.UTC),
		},
	}
}

// goldenMetaJSON is the frozen meta.json wire format produced by goldenMeta().
// The persistence carve (relocating SessionMeta/EnvironmentInfo/WorkspaceInfo to
// schema and splitting SessionConfig's wire fields into schema.ConfigSnapshot)
// MUST preserve this byte-for-byte. A changed, dropped, or reordered field here
// means an older binary's meta.json no longer round-trips — a corrupt resume.
const goldenMetaJSON = `{"id":"01JTESTGOLDEN0000000000001","profile_id":"openai","model":"gpt-5.2","config":{"max_tool_rounds_per_input":150,"max_turns":20,"default_command_timeout_ms":5000,"max_command_timeout_ms":300000,"max_subagent_depth":2,"tool_output_limits":{"shell":{"max_chars":1000,"max_lines":50,"strategy":"head_tail"}},"user_instruction_override":"be concise","agent_name":"reviewer","reasoning_effort":"high","skills_dirs":["/a/skills"],"mcp_config_files":["/a/mcp.json"],"mcp_inline":["srv:cmd --flag"],"plugin_dirs":["/a/plugins"],"system_prompt_file":"/a/prompt.txt","system_prompt_append":["/a/append.txt"],"no_project_prompts":true,"non_interactive":true,"context_strategy":"compact","share_tasks_with_children":true,"result_tool_name":"respond","enable_loop_detection":false,"loop_detection_window":15,"model_fallbacks":["openai/gpt-5","anthropic/claude"],"system_prompt_as_user":true,"openai_responses_continuation":"auto"},"env_info":{"working_dir":"/work","platform":"darwin","os_version":"Darwin 25.5.0","today":"2026-06-02","knowledge_cutoff":"2026-01-01","is_git_repo":true,"git_branch":"main","git_origin_url":"git@github.com:x/y.git","git_modified_files":3,"git_untracked_files":1,"git_recent_commit_titles":["fix a","feat b"],"workspace":{"tree":"root/\n  a.go","build_info":"Go module"}},"created_at":"2026-01-15T10:00:00Z","updated_at":"2026-01-15T10:05:00Z","turn_count":7,"last_input_tokens":12345,"name":"Golden Session","name_source":"prompt","name_updated_at":"2026-01-15T10:04:00Z","original_prompt":"do the thing","parent_session_id":"01PARENT","divergence_turn":4,"fork_label":"before TDD","is_subagent":true,"origin":"test","goal":{"objective":"write all the tests","status":"active","iterations":3,"no_progress_streak":1,"made_progress_once":true,"stop_reason":"no progress","created_at":"2026-01-15T10:00:00Z","updated_at":"2026-01-15T10:03:00Z"}}`

func TestSessionMeta_GoldenWireFormat(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(goldenMeta())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != goldenMetaJSON {
		t.Fatalf("meta.json wire format drift:\n got: %s\nwant: %s", data, goldenMetaJSON)
	}
}

// TestSessionMeta_GoldenRoundTrip verifies the golden meta.json unmarshals and
// re-marshals to identical bytes: every persisted field survives a load/save cycle.
func TestSessionMeta_GoldenRoundTrip(t *testing.T) {
	t.Parallel()
	var meta schema.SessionMeta
	if err := json.Unmarshal([]byte(goldenMetaJSON), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(data) != goldenMetaJSON {
		t.Fatalf("round-trip drift:\n got: %s\nwant: %s", data, goldenMetaJSON)
	}
}

// TestConfigSnapshot_ConverterFidelity proves the SessionConfig<->ConfigSnapshot
// converters carry every persisted wire field with no drops or swaps. Because the
// two structs share identical json tags, a faithful conversion marshals to
// identical bytes; a dropped or misrouted field surfaces as a byte difference.
// Engine-only json:"-" fields (e.g. StateDir) are excluded from both sides.
func TestConfigSnapshot_ConverterFidelity(t *testing.T) {
	t.Parallel()
	cfg := fullSessionConfig()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	snapJSON, err := json.Marshal(cfg.toSnapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if !bytes.Equal(cfgJSON, snapJSON) {
		t.Fatalf("toSnapshot dropped or misrouted a field:\n cfg:  %s\n snap: %s", cfgJSON, snapJSON)
	}

	backJSON, err := json.Marshal(configFromSnapshot(cfg.toSnapshot()))
	if err != nil {
		t.Fatalf("marshal round-trip: %v", err)
	}
	if !bytes.Equal(backJSON, cfgJSON) {
		t.Fatalf("configFromSnapshot dropped or misrouted a field:\n want: %s\n got:  %s", cfgJSON, backJSON)
	}
}
