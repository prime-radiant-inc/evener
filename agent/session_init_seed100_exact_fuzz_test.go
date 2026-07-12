//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
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
		})
	})
}
