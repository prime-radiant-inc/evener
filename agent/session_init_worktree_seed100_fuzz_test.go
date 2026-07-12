//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
)

// FuzzInitWorktreeSeed100 closes deterministic lifecycle/error branches that
// are awkward to select from model-generated tool programs. All filesystem and
// Git activity remains inside the scripted worktree harness and t.TempDir.
func FuzzInitWorktreeSeed100(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		fuzzInitWorktreePureEdges(t)
		fuzzInitWorktreeCloseUnlockFailure(t)
		fuzzInitW3RegressionPrograms(t)
		fuzzWorktreeCloseResumeRegressionPrograms(t)
		fuzzSubagentRegressionPrograms(t)
		fuzzWorktreeErrorRegressionPrograms(t)
	})
}

func fuzzWorktreeErrorRegressionPrograms(t *testing.T) {
	t.Helper()
	programs := []struct {
		name string
		run  func(*testing.T)
	}{
		{"control-policy", TestWorktreeControlEnvUsesControlPolicy},
		{"control-nonlocal", TestWorktreeControlEnv_NonLocalEnvErrors},
		{"create-nonrepo", TestWorktreeErrors_NotInGitRepo},
		{"create-parent", TestWorktreeCreate_WorktreeParentMkdirFailsWhenPathComponentIsAFile},
		{"create-meta", TestWorktreeCreate_MetaDirMkdirFailsWhenProjectDirIsAFile},
		{"create-race", TestWorktreeCreate_SidecarAlreadyExistsRace},
		{"create-enter", TestWorktreeCreate_RejectsInvalidName},
		{"rollback-nonlocal", TestWorktreeCreateCore_ControlEnvNonLocalEnvErrors},
		{"switch-nonrepo", TestWorktreeErrors_SwitchToNonexistentWorktree},
		{"switch-path", TestWorktreeErrors_SwitchByPathUnregistered},
		{"exit-lock", TestWorktreeExit_RelockLockCommandFailsOnPermissionDenied},
		{"exit-list", TestWorktreeExit_LeaveCurrentErrorsWhenGitUnavailable},
		{"remove-path", TestWorktreeErrors_RemoveTargetResolvesOutsideManagedDir},
		{"remove-active", TestWorktreeErrors_RemoveCurrentNoSafeRestoreEnv},
		{"remove-foreign", TestWorktreeErrors_RemoveForeignLockRefusesForceDoesNotOverride},
		{"prune-remove", TestWorktreePrune_Sweep1_BranchDeleteFailsDuringCollect},
		{"prune-merge", TestWorktreePrune_Sweep1_MergeCheckFails},
	}
	for _, program := range programs {
		t.Run(program.name, program.run)
	}
}

func fuzzSubagentRegressionPrograms(t *testing.T) {
	t.Helper()
	programs := []struct {
		name string
		run  func(*testing.T)
	}{
		{"cancel-running", TestCancelAgent_RunningChildBecomesCancelledAndResumable},
		{"cancel-not-running", TestCancelAgent_NotRunning},
		{"cancel-race", TestCancelAgent_GenuineFailureRacingCancelStaysFailed},
		{"snapshot-shape", TestResultSnapshot_CurrentShape},
		{"snapshot-status", TestResultSnapshot_CarriesAgentIDAndStatus},
		{"followup-provenance", TestSubagentFollowUpProvenanceUnionsLaunchActiveAndCompleted},
		{"timestamps", TestSubagentTimestamps_ResetOnResume},
		{"zero-allowance", TestPrepareSubagentRunRejectsZeroAllowance},
		{"recursion", TestPrepareSubagentRunAllowsRecursionWithAllowance},
		{"sandbox-failure", TestPrepareSubagentRun_PerDelegateSandboxCleansScratchOnSpawnFailure},
		{"sandbox-parent", TestPrepareSubagentRun_PerDelegateSandboxOverridesSandboxedParent},
		{"sandbox-no-isolation", TestPrepareSubagentRun_PerDelegateSandboxWithoutIsolationDoesNotMutateParent},
		{"spawn-default", TestSpawnAgent_DefaultSubagentGetsComposedPrompt},
		{"spawn-system-prompt", TestSpawnAgent_SystemPromptFileDoesNotOverrideSubagentPrompt},
		{"spawn-tools", TestSpawnAgent_BuiltinSubagentKeepsDelegateSendTool},
		{"untyped-role", TestUntypedDelegatingSubagentUsesDelegatingRolePrompt},
		{"max-turns", TestSubagent_MaxTurns_DefaultsTo500_NotInheritedFromParent},
		{"depth", TestSubagent_DepthSetFromConfig},
	}
	for _, program := range programs {
		t.Run(program.name, program.run)
	}
}

func fuzzWorktreeCloseResumeRegressionPrograms(t *testing.T) {
	t.Helper()
	programs := []struct {
		name string
		run  func(*testing.T)
	}{
		{"resume-gone", TestResumeWorktreeReentry_WorktreeGone_RestoresRootAndNotices},
		{"resume-no-repo", TestResumeWorktreeReentry_UnresolvableMainRootNoticesAndRestoresRoot},
		{"resume-list-fails", TestResumeWorktreeReentry_WorktreeListFailsNoticesAndRestoresRoot},
		{"resume-unregistered", TestResumeWorktreeReentry_NotRegisteredAtPathNoticesAndRestoresRoot},
		{"resume-lock-state", TestResumeWorktreeReentry_ManagedLockStateUnverifiableNoticesAndRestoresRoot},
		{"resume-relock-fails", TestResumeWorktreeReentry_ManagedRelockFailsNoticesAndRestoresRoot},
		{"resume-foreign", TestResumeWorktreeReentry_ManagedForeign_RestoresRootAndNotices},
		{"resume-foreign-empty", TestResumeWorktreeReentry_ManagedForeignBareLockUnknownOwnerNotice},
		{"resume-unlocked", TestResumeWorktreeReentry_ManagedUnlocked_LocksAndRootsEnv},
		{"resume-own", TestResumeWorktreeReentry_ManagedOwnMarkerStale_Adopts},
		{"resume-external", TestResumeWorktreeReentry_NonManagedPathEntered_ReentersNoLock},
		{"dispose-main-root", TestDisposeOneDelegateLane_UnresolvableMainRootLeavesLane},
		{"dispose-sidecar", TestDisposeOneDelegateLane_MissingSidecarLeavesLane},
		{"dispose-lock-state", TestDisposeOneDelegateLane_LockStateUnverifiableLeavesLane},
		{"dispose-check", TestDisposeOneDelegateLane_UnchangedCheckFailsKeepsAndUnlocks},
		{"dispose-changed-lock", TestDisposeOneDelegateLane_ChangedLaneUnlockFailsLeavesLocked},
		{"dispose-changed-foreign", TestDisposeOneDelegateLane_ChangedForeignLockDeclinedNotTouched},
		{"dispose-unchanged-lock", TestDisposeOneDelegateLane_UnchangedUnlockFailsLeavesLocked},
		{"dispose-race", TestDisposeRacingDirtyWrite_DowngradesToKeep},
		{"dispose-mark", TestDisposeOneDelegateLane_DisposedMarkAppendFailureWarnsButStillRemoves},
		{"dispose-branch", TestDisposeOneDelegateLane_BranchDeleteFailureWarnsButLaneStillGone},
		{"close-unlock-fails", TestUnlockOwnManagedWorktreeAtClose_LeaveFailsWarns},
		{"close-unlock-nonlocal", TestUnlockOwnManagedWorktreeAtClose_NonLocalEnvNoOp},
		{"close-unlock-root", TestUnlockOwnManagedWorktreeAtClose_UnresolvableMainRootNoOp},
		{"init-nonlocal", TestInitInside_NonLocalEnvNoOp},
		{"init-no-repo", TestInitInside_UnresolvableMainRootNoOp},
		{"init-outside", TestInitInside_NotInWorktree_NoOp},
		{"init-lock-state", TestInitInside_LockStateUnverifiableWarns},
		{"init-lock-fails", TestInitInside_RelockFailsWarns},
		{"init-unlocked", TestInitInside_ManagedUnlocked_LocksAtSessionStart},
		{"init-foreign", TestInitInside_ManagedForeign_WarnsAndContinuesCoOccupying},
		{"init-foreign-empty", TestInitInside_ForeignBareLockUnknownOwnerWarns},
	}
	for _, program := range programs {
		t.Run(program.name, program.run)
	}
}

func fuzzInitW3RegressionPrograms(t *testing.T) {
	t.Helper()
	programs := []struct {
		name string
		run  func(*testing.T)
	}{
		{"new-nil", TestW3Init_NewSession_NilArgGuards},
		{"new-env", TestW3Init_NewSession_EnvInitializeError},
		{"new-prompt", TestW3Init_NewSession_SystemPromptFileReadError},
		{"new-strategy-tool", TestW3Init_NewSession_StrategyToolRegisterError},
		{"restore-env", TestW3Init_Restore_EnvInitializeError},
		{"restore-job-manager", TestW3Init_Restore_JobManagerError},
		{"restore-init", TestW3Init_Restore_InitSessionStateError},
		{"restore-strategy", TestW3Init_Restore_SelectStrategyError},
		{"pending-nil-receiver", TestW3Init_PendingSessionStart_NilReceiver},
		{"pending-nil-context", TestW3Init_PendingSessionStart_NilContext},
		{"pending-cancel", TestW3Init_PendingSessionStart_CtxCancelledInLoop},
		{"pending-broadcast", TestW3Init_PendingSessionStart_AfterFuncBroadcast},
		{"child-error", TestW3Init_PrepareSubagentRun_ChildSessionError},
		{"skill-skip", TestW3Init_PrepareSubagentRun_SkillResolveSkipped},
		{"clone-maps", TestS5Cov_CloneMaps},
		{"env-policy-name", TestS5Cov_LocalEnvPolicyName},
		{"env-policy-parse", TestS5Cov_LocalEnvPolicyFromName},
		{"frozen-tools", TestS5Cov_FrozenSubagentToolNames},
		{"frozen-skills", TestS5Cov_RestoreFrozenSkillBodies},
		{"communicate-nudge", TestS5Cov_SubagentNeedsCommunicateNudge},
		{"strategy-all", TestS2Cov_SelectStrategy_AllNamedStrategies},
		{"strategy-unknown", TestS2Cov_SelectStrategy_UnknownStrategyFails},
		{"fallback-cross-provider", TestS2Cov_ValidateModelFallbacks_RejectsCrossProvider},
	}
	for _, program := range programs {
		t.Run(program.name, program.run)
	}
}

func fuzzInitWorktreePureEdges(t *testing.T) {
	t.Helper()

	if got := cloneMap(map[string]any{"unencodable": func() {}}); got["unencodable"] == nil {
		t.Fatal("cloneMap marshal fallback lost value")
	}
	if err := (*Session)(nil).trackAndLaunchPreparedSubagent(nil); err == nil {
		t.Fatal("nil prepared run succeeded")
	}
	if _, err := (&Session{}).installParentSourceWatchForChild("", "", watchArgs{}); err == nil {
		t.Fatal("empty parent-watch observer succeeded")
	}
	if _, err := (&Session{}).clearParentSourceWatchForChild("", "", ""); err == nil {
		t.Fatal("empty parent-watch observer clear succeeded")
	}
	missingSub := &Session{subagents: newSubagentManager(func(events.EventKind, events.EventData) {})}
	if _, err := missingSub.sendInput(context.Background(), "missing", "input"); err == nil {
		t.Fatal("unknown subagent send succeeded")
	}

	var nilSession *Session
	nilSession.deferSessionStartHooks(plugin.SessionStartKindStartup)
	if _, _, have, run := nilSession.pendingSessionStartForUserTurn(nil); have || run {
		t.Fatal("nil pending hook session returned work")
	}
	if _, ok := nilSession.beginPendingSessionStartHooksForRestoreSideEffects(); ok {
		t.Fatal("nil pending restore hook began")
	}
	nilSession.finishPendingSessionStartHooksForRestoreSideEffects(plugin.SessionStartKindStartup, hooksRunResultZero())
	if got := nilSession.runSessionStartHooksWithContext(nil, plugin.SessionStartKindStartup); len(got.ModelContext)+len(got.UserMessages) != 0 {
		t.Fatal("nil hook runner returned messages")
	}
	nilSession.logPluginLoadDiag(plugin.Instance{})
	nilSession.logSessionStartHookDispatch(plugin.SessionStartKindStartup, 1)
	if err := nilSession.runDeferredRestoreSideEffects(); err != nil {
		t.Fatalf("nil deferred side effects: %v", err)
	}

	if _, err := NewSession(nil, nil, nil, SessionConfig{}); err == nil {
		t.Fatal("nil client accepted")
	}
	// Non-local worktree lifecycle paths must remain inert.
	s := &Session{env: &worktreeFaultExecEnv{}, cfg: SessionConfig{}}
	s.resumeWorktreeReentry(schema.SessionMeta{WorktreePath: "/tmp/not-local"})
	s.disposeDelegateLanesAtClose()
	s.unlockOwnManagedWorktreeAtClose()
	if _, kept := s.disposeOneDelegateLane(&execenv.LocalExecutionEnvironment{}, isolationLane{delegateID: "gone", path: t.TempDir()}); kept {
		t.Fatal("missing lane was kept")
	}
}

func hooksRunResultZero() hooks.RunResult { return hooks.RunResult{} }

func fuzzInitWorktreeCloseUnlockFailure(t *testing.T) {
	t.Helper()
	h, faults := newWorktreeFaultSession(t)
	name := "dispose-unlock-failure"
	path := scriptedCreate(t, h, name)
	h.exitToRoot(t)
	h.setLock(path, worktree.FormatDelegateMarker(name, h.s.id))
	faults.unlockErr[path] = errors.New("scripted dispose unlock failure")
	local := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if _, kept := h.s.disposeOneDelegateLane(local, isolationLane{delegateID: name, path: path}); kept {
		t.Fatal("unlock failure reported lane kept")
	}

	h2, _ := newWorktreeFaultSession(t)
	foreignName := "dispose-unchanged-foreign"
	foreignPath := scriptedCreate(t, h2, foreignName)
	h2.exitToRoot(t)
	h2.setLock(foreignPath, "serf:foreign")
	if _, kept := h2.s.disposeOneDelegateLane(h2.s.currentEnv().(*execenv.LocalExecutionEnvironment), isolationLane{delegateID: foreignName, path: foreignPath}); kept {
		t.Fatal("foreign unchanged lane reported kept")
	}
}
