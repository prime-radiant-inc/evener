package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// newResultToolSession builds a session exactly like newSession but returns
// NewSession's error instead of failing the test, so both a config-validation
// refusal and its passing counterpart can be observed.
func newResultToolSession(t *testing.T, profile *provider.Profile, cfg SessionConfig) (*Session, error) {
	t.Helper()
	if cfg.MaxSubagentDepth == 0 {
		cfg.MaxSubagentDepth = 1
	}
	if cfg.AgentsDocPath == "" {
		cfg.AgentsDocPath = filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
	}
	cfg.testOnly.skipGitSnapshot = true
	cfg.testOnly.minimalSystemPrompt = true
	cfg.testOnly.noSyncJobStore = true
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	if profile.ConfiguredCheapModel() == "" {
		profile = withTestSessionNamer(client, profile)
	}
	return NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
}

// requireResultToolRejected asserts NewSession refuses cfg's ResultToolName with
// the collision refusal specifically: an error that names both the flag and the
// offending value. Asserting the message, not merely a non-nil error, keeps a
// probe from passing on an unrelated construction failure.
func requireResultToolRejected(t *testing.T, profile *provider.Profile, cfg SessionConfig) {
	t.Helper()
	sess, err := newResultToolSession(t, profile, cfg)
	if err == nil {
		sess.Close()
		t.Fatalf("NewSession accepted the colliding ResultToolName %q", cfg.ResultToolName)
	}
	if !strings.Contains(err.Error(), "--result-tool-name") {
		t.Fatalf("error %q does not name the --result-tool-name flag", err)
	}
	if !strings.Contains(err.Error(), cfg.ResultToolName) {
		t.Fatalf("error %q does not name the offending value %q", err, cfg.ResultToolName)
	}
}

// requireResultToolShape asserts the tool registered under name is the result
// tool — communicate's parameters survived the rename or adoption.
func requireResultToolShape(t *testing.T, sess *Session, name string) {
	t.Helper()
	reg := sess.reg.Get(name)
	if reg == nil {
		t.Fatalf("result tool %q not registered", name)
	}
	props, _ := reg.Definition.Parameters["properties"].(map[string]any)
	for _, want := range []string{"message", "end_turn"} {
		if _, ok := props[want]; !ok {
			t.Fatalf("result tool %q lacks communicate's %q parameter: %v", name, want, props)
		}
	}
}

// TestNewSessionRejectsResultToolNameCollidingWithNativeTool pins issue #4218:
// a ResultToolName that names one of Evener's own tools must be refused at
// session construction, so no adoption, overwrite, or strip path can leave the
// session without a usable result tool. The cases cover one name from each
// registration group, including the groups that register around the result tool
// (ask_user, use_skill, the transcript and doctor tools) and the memory_ prefix
// that filterUnavailableMemoryTools strips.
func TestNewSessionRejectsResultToolNameCollidingWithNativeTool(t *testing.T) {
	memoryDir := t.TempDir()
	cases := []struct {
		name string
		cfg  SessionConfig
	}{
		{"file tool", SessionConfig{ResultToolName: "read_file"}},
		{"memory tool registered", SessionConfig{ResultToolName: "memory_write", MemoryStateRoot: memoryDir}},
		{"memory tool under disabled memory", SessionConfig{ResultToolName: "memory_write", DisableMemory: true}},
		{"reserved memory prefix, unregistered tool", SessionConfig{ResultToolName: "memory_future", DisableMemory: true}},
		{"job tool", SessionConfig{ResultToolName: "job_list"}},
		{"delegate", SessionConfig{ResultToolName: "delegate"}},
		{"worktree tool", SessionConfig{ResultToolName: "manage_worktree"}},
		{"task tool", SessionConfig{ResultToolName: "task_list"}},
		{"notes tool", SessionConfig{ResultToolName: "notes_read"}},
		{"ask_user on an interactive root", SessionConfig{ResultToolName: "ask_user"}},
		{"skill tool", SessionConfig{ResultToolName: "use_skill"}},
		{"transcript tool", SessionConfig{ResultToolName: "doctor_evener", StateDir: t.TempDir()}},
		{"compact tool", SessionConfig{ResultToolName: "compact_context"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireResultToolRejected(t, NewOpenAIProfile("gpt-5.2"), tc.cfg)
		})
	}
}

// TestNewSessionRejectsResultToolNameOfProfileDeclaredTool covers a native name
// that only some provider profiles declare: web_search rides with a
// google-protocol profile that serves provider-native web search.
func TestNewSessionRejectsResultToolNameOfProfileDeclaredTool(t *testing.T) {
	requireResultToolRejected(t, newGeminiProfile("gemini-test"), SessionConfig{ResultToolName: "web_search", StateDir: t.TempDir()})
}

// TestNewSessionAllowsResultToolNameNoToolHolds is the precision control: the
// check is derived from what this session actually registers, so a name that is
// native to some other session but absent here is still usable, and the
// accepted result tool is still communicate underneath.
func TestNewSessionAllowsResultToolNameNoToolHolds(t *testing.T) {
	cases := []struct {
		name string
		cfg  SessionConfig
	}{
		// evener run is non-interactive, so ask_user is never registered.
		{"ask_user in a non-interactive session", SessionConfig{ResultToolName: "ask_user", NonInteractive: true}},
		// web_search is google-only; this session's profile does not declare it.
		{"web_search on an openai profile", SessionConfig{ResultToolName: "web_search"}},
		// memory_foo is usable while this session has memory.
		{"memory prefix while memory is bound", SessionConfig{ResultToolName: "memory_foo", MemoryStateRoot: t.TempDir()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := newResultToolSession(t, NewOpenAIProfile("gpt-5.2"), tc.cfg)
			if err != nil {
				t.Fatalf("NewSession refused a name this session does not use: %v", err)
			}
			t.Cleanup(func() { sess.Close() })
			requireResultToolShape(t, sess, tc.cfg.ResultToolName)
		})
	}
}

// TestNewSessionAllowsNonNativeResultToolName is the rename positive control: a
// ResultToolName that is not one of Evener's tools still renames the result
// tool, and the native tools stay intact.
func TestNewSessionAllowsNonNativeResultToolName(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		ResultToolName:   "respond",
		MemoryStateRoot:  t.TempDir(),
	}))
	requireResultToolShape(t, s, "respond")
	if s.reg.Get("memory_write") == nil {
		t.Fatal("renaming the result tool removed the native memory_write tool")
	}
}

// TestRestoreRefusesPersistedCollidingResultToolName pins the fail-closed
// restore behavior: a session persisted (before this fix) with a
// ResultToolName that collides is refused rather than resumed into a session
// with no usable result tool.
func TestRestoreRefusesPersistedCollidingResultToolName(t *testing.T) {
	meta := schema.SessionMeta{
		ID:        "restore-colliding-result-tool",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{ResultToolName: "memory_write", MemoryStateRoot: t.TempDir()}).toSnapshot(),
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir: t.TempDir(),
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err == nil {
		restored.Close()
		t.Fatal("restore accepted a persisted colliding ResultToolName")
	}
	if !strings.Contains(err.Error(), "--result-tool-name") || !strings.Contains(err.Error(), "memory_write") {
		t.Fatalf("restore error %q does not name the flag and the offending value", err)
	}
}

// TestNewSessionRefusesEveryNameItsOwnRegistryRegisters is the drift guard. It
// derives the native name set from the real registration path — a session built
// with no override — rather than a hand-maintained list, then proves a session
// carrying each of those names as its ResultToolName is refused with the
// collision refusal. If a future intrinsic tool registers after the result
// tool, the corresponding probe succeeds and this test fails, so the ordering
// the fix relies on cannot rot silently.
func TestNewSessionRefusesEveryNameItsOwnRegistryRegisters(t *testing.T) {
	cases := []struct {
		name    string
		profile *provider.Profile
		memory  bool
	}{
		{"openai with memory", NewOpenAIProfile("gpt-5.2"), true},
		{"google", newGeminiProfile("gemini-test"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := SessionConfig{StateDir: t.TempDir()}
			if tc.memory {
				cfg.MemoryStateRoot = t.TempDir()
			}
			ref, err := newResultToolSession(t, tc.profile, cfg)
			if err != nil {
				t.Fatalf("reference session: %v", err)
			}
			t.Cleanup(func() { ref.Close() })
			names := ref.reg.Names()
			if len(names) < 20 {
				t.Fatalf("reference registry looks too small to be a drift guard: %v", names)
			}
			for _, name := range names {
				if name == tool.DefaultResultToolName {
					continue
				}
				cfg.ResultToolName = name
				requireResultToolRejected(t, tc.profile, cfg)
			}
		})
	}
}
