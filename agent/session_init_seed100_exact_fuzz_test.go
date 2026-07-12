//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzSessionInitSeed100Exact replays the deterministic constructor and
// initialization regressions whose production statements live in
// session_init.go. The individual programs use temporary files, scripted
// providers, and in-process MCP fixtures only.
func FuzzSessionInitSeed100Exact(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		programs := []struct {
			name string
			run  func(*testing.T)
		}{
			{"plugin-unknown-hook", TestInitPlugins_UnknownHookEventWarnsLoudly},
			{"plugin-unsupported-hook", TestInitPlugins_UnsupportedHookEventWarns},
			{"plugin-unsupported-handler", TestInitPlugins_UnsupportedHandlerTypeWarns},
			{"plugin-supported-handlers", TestInitPlugins_SupportedHandlerTypesNoTypeWarning},
			{"plugin-valid-hooks", TestInitPlugins_ValidHooksProduceNoWarning},
			{"plugin-command-model", TestInitPlugins_CommandModelOverrideWarnsUnenforced},
			{"plugin-command-tools", TestInitPlugins_CommandAllowedToolsWarnsUnenforced},
			{"plugin-command-plain", TestInitPlugins_CommandWithoutOverridesNoWarning},
			{"plugin-broken", TestInitPlugins_BrokenPluginDirSkippedWithWarning},
			{"plugin-duplicate", TestInitPlugins_DuplicatePluginNameSkippedWithWarning},
			{"plugin-broken-healthy", TestInitPlugins_BrokenPluginDoesNotBlockHealthyPlugins},
			{"fallback-cross-tag", TestValidateModelFallbacks_CrossTag_Errors},
			{"fallback-same-tag", TestValidateModelFallbacks_SameTag_Allowed},
			{"restore-resolver", TestRestoreSessionFromMetaWithConfig_InstallsResolveProfile},
			{"restore-fallbacks", TestRestoreSessionFromMetaWithConfig_LayersModelFallbacks},
			{"restore-continuation", TestRestoreSessionFromMetaWithConfig_LayersOpenAIResponsesContinuation},
			{"restore-clock", TestRestoreSessionConfigUsesInjectedClock},
			{"restore-goal", TestGoalRestoreOnlyActive},
			{"git-snapshot-seam", TestNewSession_TestConfigCanSkipGitSnapshot},
			{"mcp-inline", TestIntg_InitMCP_InlineServer},
			{"mcp-plugin", TestIntg_InitMCP_PluginProvidedServerMerges},
			{"mcp-bad-plugin", TestIntg_InitMCP_PluginBadInlineMCPServersSurvives},
			{"mcp-register-error", TestIntg_InitMCP_RegisterToolsError},
			{"mcp-connect-error", TestIntg_InitMCP_ConnectError},
			{"mcp-discover-error", TestIntg_InitMCP_DiscoverError},
			{"mcp-global-warning", TestIntg_InitMCP_GlobalConfigParseErrorSurvives},
			{"mcp-new-close", TestIntg_NewSession_LateErrorClosesMCPManager},
			{"mcp-restore-close", TestIntg_RestoreSession_LateErrorClosesMCPManager},
			{"pending-nil", TestW3Init_PendingSessionStart_NilReceiver},
			{"pending-context", TestW3Init_PendingSessionStart_NilContext},
			{"pending-cancel", TestW3Init_PendingSessionStart_CtxCancelledInLoop},
			{"pending-broadcast", TestW3Init_PendingSessionStart_AfterFuncBroadcast},
			{"resume-hooks-deferred", TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput},
			{"resume-hooks-notification", TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks},
			{"resume-hooks-watch", TestRestoreSessionWatchDeliveryDoesNotDrainResumeSessionStartHooks},
			{"resume-hooks-once", TestRestoreSessionStartHooksDrainOnlyOnce},
			{"strategy-all", TestS2Cov_SelectStrategy_AllNamedStrategies},
			{"strategy-unknown", TestS2Cov_SelectStrategy_UnknownStrategyFails},
			{"new-nil-guards", TestW3Init_NewSession_NilArgGuards},
			{"new-env-error", TestW3Init_NewSession_EnvInitializeError},
			{"new-prompt-error", TestW3Init_NewSession_SystemPromptFileReadError},
			{"new-strategy-tool-error", TestW3Init_NewSession_StrategyToolRegisterError},
			{"restore-env-error", TestW3Init_Restore_EnvInitializeError},
			{"restore-job-error", TestW3Init_Restore_JobManagerError},
			{"restore-init-error", TestW3Init_Restore_InitSessionStateError},
			{"restore-strategy-error", TestW3Init_Restore_SelectStrategyError},
			{"restore-sandbox", TestRestoreProvisionsPersistedSandbox},
			{"restore-sandbox-off", TestRestoreOffSandboxUnchanged},
			{"root-task-populate", TestTaskWorkflow_RootSessionPopulatesTasks},
			{"root-task-select", TestTaskWorkflow_NewSessionPopulatesCorrectTasks},
			{"git-snapshot", TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo},
			{"restore-transcript", TestRestoreSession_FromMetaAndTranscript},
			{"invalid-hook-matcher", TestNewSession_InvalidMatcherWarnsOnce},
			{"deferred-nested-terminal", TestDeferredRestoreSideEffectsRecoverNestedTerminalForward},
			{"deferred-runtime-lost", TestDeferredRestoreSideEffectsForwardsReconciledNestedRuntimeLost},
			{"deferred-forward-failed", TestDeferredRestoreSideEffectsSkipsStartForwardFailedNestedTerminal},
		}
		for _, program := range programs {
			t.Run(program.name, program.run)
		}
		t.Run("pure-helpers", func(t *testing.T) {
			if cacheReadPtr(0) != nil || *cacheReadPtr(7) != 7 {
				t.Fatal("cacheReadPtr conversion mismatch")
			}
			if modelFallbackEligible(errors.New("retryable")) || !modelFallbackEligible(context.Canceled) {
				t.Fatal("fallback eligibility classification mismatch")
			}
			if !strings.Contains(unsupportedHandlerTypeWarning("plugin", "event", ""), "(empty)") {
				t.Fatal("empty hook handler type was not displayed")
			}
			if got := reconnectRecoveryWarning("server"); got.Source != "mcp" || !strings.Contains(got.Message, "server") {
				t.Fatalf("reconnect warning = %+v", got)
			}

			override := &spyStrategy{}
			gotStrategy, err := selectStrategy(SessionConfig{testOnly: testConfig{contextStrategyOverride: override}}, nil, nil)
			if err != nil || gotStrategy != override {
				t.Fatalf("strategy override = %T, %v", gotStrategy, err)
			}

			s := &Session{pluginAgents: map[string]plugin.Agent{
				"custom":  {PluginName: "fixture", SystemPrompt: " role prompt "},
				"builtin": {PluginName: "builtin", SystemPrompt: "ignored"},
			}}
			s.applyAgentRolePromptOverride()
			s.cfg.AgentName = "missing"
			s.applyAgentRolePromptOverride()
			s.cfg.AgentName = "builtin"
			s.applyAgentRolePromptOverride()
			s.cfg.AgentName = "custom"
			s.applyAgentRolePromptOverride()
			if s.cfg.spawn.rolePromptOverride != "role prompt" {
				t.Fatalf("role prompt override = %q", s.cfg.spawn.rolePromptOverride)
			}
			s.applyAgentRolePromptOverride()

			var nilSession *Session
			nilSession.finishPendingSessionStartHooksForRestoreSideEffects(plugin.SessionStartKindResume, hooks.RunResult{})
			pending := &Session{}
			pending.finishPendingSessionStartHooksForRestoreSideEffects(plugin.SessionStartKindResume, hooks.RunResult{})
			pending.deferSessionStartHooks(plugin.SessionStartKindResume)
			kind, ok := pending.beginPendingSessionStartHooksForRestoreSideEffects()
			if !ok {
				t.Fatal("pending restore hook did not begin")
			}
			pending.finishPendingSessionStartHooksForRestoreSideEffects(kind, hooks.RunResult{})
			if _, _, have, run := pending.pendingSessionStartForUserTurn(context.Background()); !have || run {
				t.Fatalf("stored restore result: have=%v run=%v", have, run)
			}
			pending.deferSessionStartHooks("")
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			pending.pendingSessionStartForUserTurn(cancelled)
			pending.deferSessionStartHooks(plugin.SessionStartKindResume)
			pending.runPendingSessionStartHooksForRestoreSideEffects(nil)
			if _, ok := nilSession.beginPendingSessionStartHooksForRestoreSideEffects(); ok {
				t.Fatal("nil session began pending hooks")
			}
			if err := nilSession.runDeferredRestoreSideEffects(); err != nil {
				t.Fatalf("nil deferred restore side effects: %v", err)
			}

			hookSession := newSession(t)
			hookSession.hookRunner = hooks.NewRunner(hookSession.client, hookSession.profile.Model())
			hookSession.runSessionStartHooksWithContext(nil, plugin.SessionStartKindStartup)
			nilSession.runSessionStartHooksWithContext(nil, plugin.SessionStartKindStartup)

			blocked := t.TempDir()
			logDir := filepath.Join(blocked, sessionsSubdir)
			if err := os.MkdirAll(logDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(logDir, "log.log.jsonl"), 0o700); err != nil {
				t.Fatal(err)
			}
			logSession := &Session{id: "log", stateDir: blocked}
			logSession.logPluginLoadDiag(plugin.Instance{})
			logSession.logSessionStartHookDispatch("", 0)

			client := sierClient()
			profile := NewOpenAIProfile("gpt-5.2")
			env := pifNewDenyEnv(t.TempDir(), 1)
			for mode := byte(0); mode < 3; mode++ {
				sierRestoreGuards(t, &sierReader{data: []byte{mode}}, client, profile, env, agenttest.NewFakeClock())
				sierFallbackValidation(t, &sierReader{data: []byte{mode}}, client, profile, env, sierConfig(agenttest.NewFakeClock()))
			}
			for cacheMode := byte(0); cacheMode < 2; cacheMode++ {
				sierRestoreProjection(t, &sierReader{data: []byte{2, 3, 4, cacheMode}}, client, profile, env, t.TempDir(), agenttest.NewFakeClock())
			}

			meta := sierMeta()
			malformedAsk := stmAssistantTurn(llm.ToolCallData{
				ID: "bad-ask", Name: "ask_user", Type: "function", Arguments: json.RawMessage(`{`),
			})
			malformedResult := stmToolResultsTurn(stmToolResult("bad-ask", "ask_user", false))
			restoreCfg := sierRestoreConfig(t.TempDir(), agenttest.NewFakeClock())
			restoreCfg.resumeHistory = []schema.Turn{malformedAsk, malformedResult}
			restored, err := RestoreSessionFromMetaWithConfig(client, profile, env, meta, restoreCfg)
			if err != nil {
				t.Fatalf("restore transcript failure fixture: %v", err)
			}
			restored.Close()

			skillDir := filepath.Join(t.TempDir(), "skills", "exact-skill")
			if err := os.MkdirAll(skillDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: exact-skill\ndescription: exact\n---\nbody\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			policyCfg := sierConfig(agenttest.NewFakeClock())
			policyCfg.SkillsDirs = []string{filepath.Dir(skillDir)}
			policyCfg.spawn.deniedToolNames = []string{"read_file"}
			policyCfg.spawn.allowedToolNames = []string{"read_file", "job_watch"}
			policyCfg.spawn.isolation = "worktree"
			policyCfg.spawn.parentSessionID = "parent"
			policyCfg.spawn.parentWatchGranted = true
			policyCfg.spawn.delegationAllowance = 0
			policy, err := NewSession(client, profile, env, policyCfg)
			if err != nil {
				t.Fatalf("policy fixture: %v", err)
			}
			policy.Close()

			promptPath := filepath.Join(t.TempDir(), "system.md")
			if err := os.WriteFile(promptPath, []byte("exact system prompt"), 0o600); err != nil {
				t.Fatal(err)
			}
			promptCfg := sierConfig(agenttest.NewFakeClock())
			promptCfg.SystemPromptFile = promptPath
			promptSession, err := NewSession(client, profile, env, promptCfg)
			if err != nil {
				t.Fatalf("system prompt success fixture: %v", err)
			}
			promptSession.Close()

			restoreStrategyCfg := sierRestoreConfig(t.TempDir(), agenttest.NewFakeClock())
			restoreStrategyCfg.testOnly.contextStrategyOverride = w3init_badToolStrategy{}
			if sess, err := RestoreSessionFromMetaWithConfig(client, profile, env, sierMeta(), restoreStrategyCfg); err == nil {
				sess.Close()
				t.Fatal("restore strategy tool collision succeeded")
			}

			delivery := newSession(t)
			delivery.deliverSessionStartHookResult(hooks.RunResult{ModelContext: []string{"context"}, UserMessages: []string{"message"}})
			goodLog := &Session{id: "good", stateDir: t.TempDir(), history: []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("prior")}}}
			goodLog.logPluginLoadDiag(plugin.Instance{})
			goodLog.logSessionStartHookDispatch("", 1)
			if err := (&Session{}).runDeferredRestoreSideEffects(); err != nil {
				t.Fatalf("nil job manager deferred effects: %v", err)
			}

			spawnRestore := sierRestoreConfig(t.TempDir(), agenttest.NewFakeClock())
			spawnRestore.spawn.parentSessionID = "parent"
			spawnRestore.spawn.treeCounter = newTreeCounter()
			spawnRestore.resumeHistory = []schema.Turn{}
			spawned, err := RestoreSessionFromMetaWithConfig(client, profile, env, sierMeta(), spawnRestore)
			if err != nil {
				t.Fatalf("spawn restore fixture: %v", err)
			}
			spawned.Close()

			badSandboxMeta := sierMeta()
			badSandboxMeta.Config = (SessionConfig{Sandbox: "restricted"}).toSnapshot()
			if sess, err := RestoreSessionFromMetaWithConfig(client, profile, env, badSandboxMeta, sierRestoreConfig(t.TempDir(), agenttest.NewFakeClock())); err == nil {
				sess.Close()
				t.Fatal("unsupported restored sandbox succeeded")
			}

			pluginSession := newSession(t)
			pluginSession.cfg.PluginDirs = []string{makePluginDir(t, "no-hook-run")}
			if err := pluginSession.initPlugins(plugin.SessionStartKindStartup, false); err != nil {
				t.Fatal(err)
			}

			mcpCfg := sierConfig(agenttest.NewFakeClock())
			mcpCfg.MCPInline = []string{"missing:/definitely/not/a/program"}
			mcpSession, err := NewSession(client, profile, env, mcpCfg)
			if err != nil {
				t.Fatalf("mcp reconnect fixture: %v", err)
			}
			if mcpSession.mcpMgr == nil || mcpSession.mcpMgr.OnReconnect == nil {
				t.Fatal("mcp reconnect callback missing")
			}
			mcpSession.mcpMgr.OnReconnect("missing")
			mcpSession.Close()

		})
	})
}
