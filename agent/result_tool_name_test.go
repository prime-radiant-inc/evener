package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// newResultToolSession builds a session exactly like newSession but returns
// NewSession's error instead of failing the test, so both a config-validation
// refusal and its passing counterpart can be observed.
func newResultToolSession(t *testing.T, profile *provider.Profile, cfg SessionConfig) (*Session, error) {
	t.Helper()
	return newResultToolSessionOn(t, profile, cfg, execenv.NewLocalExecutionEnvironment(t.TempDir()))
}

// newResultToolSessionOn is newResultToolSession over a caller-supplied
// environment, so a test can carry a sandbox policy.
func newResultToolSessionOn(t *testing.T, profile *provider.Profile, cfg SessionConfig, env execenv.ExecutionEnvironment) (*Session, error) {
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
	return NewSession(client, profile, env, cfg)
}

// resultToolWrapperEnv gives a real local environment a kernel-sandbox wrapper,
// so the session sees a resolved sandbox policy.
type resultToolWrapperEnv struct {
	execenv.ExecutionEnvironment
	wrapper *sandbox.Wrapper
}

func (e resultToolWrapperEnv) KernelWrapper() *sandbox.Wrapper { return e.wrapper }

// resultToolSandboxWrapper builds a kernel wrapper for the given network
// policy, so a test can drive providerWebSearchEnabled's net-off branch.
func resultToolSandboxWrapper(t *testing.T, network bool) *sandbox.Wrapper {
	t.Helper()
	wrapper, err := sandbox.NewWrapper(sandbox.ResolvedPolicy{
		Mode:    sandbox.ModeRestricted,
		Network: network,
		Backend: sandbox.BackendBwrap,
	}, "/fake/bwrap", "/fake/session-tmp")
	if err != nil {
		t.Fatalf("sandbox.NewWrapper: %v", err)
	}
	return wrapper
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
		// ask_user's name carries name-keyed dispatch (registry normalization)
		// and restore semantics, so it is unusable even where no question tool
		// is registered.
		{"ask_user with no question tool registered", SessionConfig{ResultToolName: "ask_user", NonInteractive: true}},
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

// TestNewSessionRejectsResultToolNameCollidingWithProviderWireName covers the
// provider-visible wire namespace: a name that exists only as a profile's
// rename of a registered tool (openai's exec_command for shell) is suppressed
// from the advertised list by rebuildToolDefsCache, so the session would have
// no usable result tool at all.
func TestNewSessionRejectsResultToolNameCollidingWithProviderWireName(t *testing.T) {
	cases := []struct {
		name    string
		profile *provider.Profile
		value   string
	}{
		{"openai shell alias", NewOpenAIProfile("gpt-5.2"), "exec_command"},
		{"openai grep alias", NewOpenAIProfile("gpt-5.2"), "grep_files"},
		{"google shell alias", newGeminiProfile("gemini-test"), "run_shell_command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireResultToolRejected(t, tc.profile, SessionConfig{ResultToolName: tc.value})
		})
	}
}

// TestNewSessionRejectsResultToolNameOfProfileDeclaredTool covers a native name
// that only some provider profiles declare: web_search rides with a
// google-protocol profile that serves provider-native web search.
func TestNewSessionRejectsResultToolNameOfProfileDeclaredTool(t *testing.T) {
	requireResultToolRejected(t, newGeminiProfile("gemini-test"), SessionConfig{ResultToolName: "web_search", StateDir: t.TempDir()})
}

// TestNewSessionRejectsResultToolNameOfProviderNativeWebSearch covers the name
// the protocol layer injects itself: a profile that serves provider-native web
// search makes the request carry a server-side web_search tool, and the
// anthropic protocol strips any function tool of that name first
// (llm/providers/anthropic/request.go), so a result tool renamed web_search is
// dropped from the request and the session cannot report.
func TestNewSessionRejectsResultToolNameOfProviderNativeWebSearch(t *testing.T) {
	cases := []struct {
		name    string
		profile *provider.Profile
	}{
		{"anthropic native search", withWebSearch(newAnthropicProfile("claude-test"), true)},
		{"responses native search", withWebSearch(NewOpenAIProfile("gpt-5.2"), true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireResultToolRejected(t, tc.profile, SessionConfig{ResultToolName: "web_search"})
		})
	}
}

// TestNewSessionAllowsProviderNativeWebSearchNameOnChatCompletions is the
// precision control for the protocol half of the native reservation: chat
// completions injects no server-side web_search, so the name is free even when
// the row reports the capability.
func TestNewSessionAllowsProviderNativeWebSearchNameOnChatCompletions(t *testing.T) {
	profile := withWebSearch(newOpenAICompatProfile("openrouter", "openai/gpt-test", 0), true)
	sess, err := newResultToolSession(t, profile, SessionConfig{ResultToolName: "web_search"})
	if err != nil {
		t.Fatalf("NewSession refused a name on a protocol that injects no native tool: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	requireResultToolShape(t, sess, "web_search")
}

// TestNewSessionAllowsResultToolNameNoToolHolds is the precision control: the
// check is derived from what this session actually uses, so a name whose
// reservation is conditional on this session's configuration stays usable, and
// the accepted result tool is still communicate underneath.
func TestNewSessionAllowsResultToolNameNoToolHolds(t *testing.T) {
	cases := []struct {
		name string
		cfg  SessionConfig
	}{
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

// TestNewSessionAllowsProviderNativeWebSearchNameWhenNetOff is the precision
// control for the provider-native reservation: with network egress denied,
// providerWebSearchEnabled (session_model_call.go) is false, so no server-side
// web_search is injected and the name is free.
func TestNewSessionAllowsProviderNativeWebSearchNameWhenNetOff(t *testing.T) {
	profile := withWebSearch(NewOpenAIProfile("gpt-5.2"), true)
	env := resultToolWrapperEnv{
		ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir()),
		wrapper:              resultToolSandboxWrapper(t, false),
	}
	sess, err := newResultToolSessionOn(t, profile, SessionConfig{ResultToolName: "web_search"}, env)
	if err != nil {
		t.Fatalf("NewSession refused a name with native web search disabled by net=off: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	requireResultToolShape(t, sess, "web_search")
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
			nameMap := tc.profile.ToolNameMap()
			for _, name := range names {
				if name == tool.DefaultResultToolName {
					continue
				}
				cfg.ResultToolName = name
				requireResultToolRejected(t, tc.profile, cfg)
				// A registered tool's provider-visible alias is taken too.
				if wire, ok := nameMap[name]; ok && wire != name {
					cfg.ResultToolName = wire
					requireResultToolRejected(t, tc.profile, cfg)
				}
			}
		})
	}
}
