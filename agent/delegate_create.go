package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func (c *delegateTreeController) delegateOwnerRuntime(lease delegateLease) *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, _, err := c.exactLeaseLocked(lease)
	if err != nil {
		return nil
	}
	if aggregate.Descriptor.ParentDelegateID == "" {
		return c.rootRuntime
	}
	parent := c.live[aggregate.Descriptor.ParentDelegateID]
	if parent == nil {
		return nil
	}
	return parent.runtime
}

func delegateInputWasPreseeded(ctx context.Context, sessionID, input string) bool {
	preseeded, ok := ctx.Value(delegatePreseededInputContextKey{}).(delegatePreseededInput)
	return ok && preseeded.sessionID == sessionID && preseeded.input == input
}

func (s *Session) createDelegate(ctx context.Context, args delegateArgs) delegateResult {
	return (delegateRuntime{owner: s}).create(ctx, args)
}

// requestedDelegateSandbox resolves the sandbox policy a delegate create asks
// for: a read-only tool scope derives its own, an explicit request is
// narrowed against the parent's, and no request leaves the policy to the
// descriptor's defaults (nil).
func (s *Session) requestedDelegateSandbox(args delegateArgs, toolNameCeiling []string) (*sandbox.SandboxPolicy, error) {
	explicitSandbox := strings.TrimSpace(args.Sandbox) != "" || args.SandboxNet != nil
	if explicitSandbox && args.SandboxNet != nil && strings.TrimSpace(args.Sandbox) == "" && !delegateSandboxBackendAvailable(s.sandboxHostFacts()) {
		return nil, newDelegateSandboxRequestError(
			errors.New("invalid_request: sandbox cannot be enforced on this host because no sandbox backend is available; omit the sandbox parameter"),
			"sandbox",
		)
	}
	if subagentToolScopeIsReadOnly(false, toolNameCeiling) {
		return s.resolveReadOnlyDelegateSandboxRequest(args.Sandbox, args.SandboxNet)
	}
	if !explicitSandbox {
		return nil, nil
	}
	parentMode, parentNetwork := s.parentSandboxModeNet()
	requested, err := resolveDelegateSandboxRequest(args.Sandbox, args.SandboxNet, parentMode, parentNetwork)
	if err != nil {
		return nil, err
	}
	return s.applyParentWriteBlockedFloor(args.Sandbox, requested)
}

func (runtime delegateRuntime) create(ctx context.Context, args delegateArgs) delegateResult {
	s := runtime.owner
	if s == nil || s.delegateController == nil {
		return delegateStartFailed(errors.New("delegate controller is unavailable"))
	}
	retirementRelease, retirementErr := s.beginRetirementMutation("delegate")
	if retirementErr != nil {
		return delegateStartFailed(retirementErr)
	}
	defer retirementRelease()
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return delegateStartFailed(errors.New("invalid_request: prompt is required"))
	}
	isolationName := strings.TrimSpace(args.Isolation)
	if isolationName != "" && isolationName != "worktree" {
		return delegateStartFailed(fmt.Errorf("invalid_request: isolation %q is not supported (expected \"worktree\")", isolationName))
	}
	if len(args.TaskList) > 0 && s.cfg.ShareTasksWithChildren {
		// A shared store already has the parent's tasks and is never
		// re-seeded, so the items would vanish; say so instead.
		return delegateStartFailed(errors.New("invalid_request: task_list cannot seed a delegate that shares your task store; add the steps to your own task_list instead"))
	}
	if strings.TrimSpace(s.stateDir) == "" {
		return delegateStartFailed(errors.New("delegate creation requires a durable state directory"))
	}
	s.mu.Lock()
	ownAllowance := s.delegationAllowance
	s.mu.Unlock()
	if args.DelegationAllowance == nil {
		args.DelegationAllowance = new(defaultDelegateGrant(ownAllowance))
	}
	if ok, validRange := validateDelegateGrant(args.grantedAllowance(), ownAllowance); !ok {
		return delegateStartFailed(fmt.Errorf("invalid_request: delegation_allowance must be less than your own allowance (%d); valid grants: %s", ownAllowance, validRange))
	}
	if err := llm.ValidateReasoningEffort(llm.NormalizeReasoningEffort(args.ReasoningEffort)); err != nil {
		return delegateStartFailed(err)
	}
	selection, err := s.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		return delegateStartFailed(err)
	}
	if args.ForkContext {
		parent := s.currentProfile()
		if selection.profile.ID() != parent.ID() || selection.profile.Model() != parent.Model() {
			return delegateStartFailed(errors.New("invalid_request: fork_context requires the parent's model and provider; use a clean session with a self-contained prompt for a different model"))
		}
	}
	if selection.warning != nil {
		s.emitDiagnosticWarning(*selection.warning)
	}
	toolNameCeiling := s.delegateEffectiveToolNameCeiling(selection, args, isolationName)
	requestedSandbox, err := s.requestedDelegateSandbox(args, toolNameCeiling)
	if err != nil {
		return delegateStartFailed(err)
	}
	descriptor, worktreeProject, err := runtime.describe(ctx, args, task, isolationName, requestedSandbox, selection, toolNameCeiling)
	if err != nil {
		return delegateStartFailed(err)
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return delegateStartFailed(err)
	}
	if err := s.reclaimDelegateRuntimeCapacity(1); err != nil {
		return delegateStartFailed(err)
	}
	// Read the overlap before this delegate is prepared or tracked, while the
	// siblings it would join are still the only running children.
	sharedWorkspaceAdvisory := s.sharedWorkspaceDelegateWarning(isolationName)
	reservation, err := s.delegateController.ReserveCreate(actor, descriptor)
	if err != nil {
		return delegateStartFailed(err)
	}
	// The lane this spawn is about to cut is taken back by every failure below,
	// and each of those rollbacks forks git on the PARENT's environment, whose
	// process table a close reaps. prepareIsolation admits its own create, but
	// that admission dies with it, and the rollbacks that matter run long after
	// it returns: CommitStart's failure, failCommittedStart's arms, and
	// failAdoptedStart. An admission asked for where one of those rollbacks
	// starts would be refused by the very close that caused it, so it is taken
	// HERE, before the lane exists, and released once by this defer — every exit
	// from this function passes through it, including the arms that retain a
	// candidate and roll nothing back, so no path leaks it.
	//
	// It overlaps prepareIsolation's create admission for the length of the
	// create. Two live admissions on the same fence are just two things the
	// close waits for; this one is named for the spawn, not the rollback: a
	// healthy spawn holds it for its whole run, and a fence warning printed
	// during that ordinary run must not read as a rollback in progress, which
	// is why worktreeCreate and prepareIsolation both rename theirs only once
	// an undo actually starts.
	//
	// A refusal needs no answer of its own: `closing` only ever goes false to
	// true, so prepareIsolation's own admission is refused too and the spawn
	// fails with no lane cut and nothing to take back.
	var (
		laneAdmission envWorkID
		laneFenced    bool
	)
	if reservation.worktreePath != "" {
		laneAdmission, laneFenced = s.beginEnvWork("delegate start on lane " + reservation.worktreePath)
		if laneFenced {
			defer s.endEnvWork(laneAdmission)
		}
	}
	isolation, err := runtime.prepareIsolation(ctx, reservation, worktreeProject, requestedSandbox)
	if err != nil {
		err = delegateSandboxFallbackHint(s, args, err)
		abortErr := s.delegateController.AbortStart(reservation)
		isolation.cleanup(s, reservation.delegateID)
		return delegateStartFailed(errors.Join(err, abortErr))
	}
	// The arms reached from here own the rename, because they are where a
	// rollback actually starts.
	isolation.laneAdmission, isolation.laneFenced = laneAdmission, laneFenced
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		isolation.cleanup(s, reservation.delegateID)
		return delegateStartFailed(err)
	}
	// Every delegation gets its own durable artifacts directory at creation,
	// under its own session state (<stateDir>/sessions/<childSessionID>) rather
	// than a /tmp scratch base, so a seat with write access has a private place
	// for report artifacts that survives it and is named in the creation result.
	// A failure here aborts the committed start through the same construction-
	// failure path as any later failure.
	var artifactsDir string
	// removeArtifacts takes back the just-created artifacts directory on an exit
	// that leaves no child session to dispose. A start that fails before a child
	// run exists (ensure or construct failing) reaches failCommittedStart with a
	// nil prepared run, so its disposeUnadopted arm never runs and nothing else
	// would remove the directory. If the removal fails, keep advertising the
	// path (so the residue stays discoverable) and surface the failure.
	var artifactsCleanupWarning string
	removeArtifacts := func() {
		if artifactsDir == "" {
			return
		}
		if err := removeDelegateArtifacts(s.stateDir, started.descriptor.ChildSessionID); err != nil {
			artifactsCleanupWarning = "delegate artifacts directory cleanup failed: " + err.Error()
			return
		}
		// The path is gone, so the failure result must not advertise it.
		artifactsDir = ""
	}
	createResult := func(result delegateResult) delegateResult {
		if selection.warning != nil {
			result.Warnings = []string{selection.warning.Message}
		}
		if artifactsCleanupWarning != "" {
			result.Warnings = append(result.Warnings, artifactsCleanupWarning)
		}
		result.Worktree = s.descriptorWorktreeReport(started.descriptor)
		// Advertise the artifacts directory only when it still exists: a failure
		// path that disposed the child removed it, and the result must not name a
		// path that is gone. A retained candidate keeps its directory and so keeps
		// advertising it.
		result.ArtifactsDir = advertiseArtifactsDir(artifactsDir)
		return result
	}
	// ReserveCreate always mints a child session id, so an empty one is a bug:
	// fail closed rather than silently skip the per-delegation artifacts dir.
	artifactsDir, err = ensureDelegateArtifactsDir(s.stateDir, started.descriptor.ChildSessionID)
	if err != nil {
		return createResult(runtime.failCommittedStart(started, isolation, nil, false, err, "artifacts_dir_failed"))
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	prepared, err := runtime.construct(ctx, args, selection, started, isolation)
	if err != nil {
		removeArtifacts()
		return createResult(runtime.failCommittedStart(started, isolation, nil, false, err, "construction_failed"))
	}
	if err := s.delegateController.AttachRuntime(started.lease, prepared.sub.sess); err != nil {
		return createResult(runtime.failCommittedStart(started, isolation, prepared, false, err, "construction_failed"))
	}
	if err := runtime.adopt(prepared); err != nil {
		return createResult(runtime.failCommittedStart(started, isolation, prepared, true, err, "construction_failed"))
	}
	claim, err := s.delegateController.BeginStartInput(started.lease)
	if err != nil {
		return createResult(runtime.failAdoptedStart(started, isolation, prepared, err, "input_admission_failed"))
	}
	preseedErr := runtime.preseedInput(prepared.sub.sess, task, started.transcriptPath, s.transcriptAuthor())
	if preseedErr != nil {
		finish := delegatePermanentStartFailure(preseedErr, "input_persist_failed")
		plans, completeErr := s.delegateController.CompleteStartInput(claim, false, finish)
		s.delegateController.emitDelegateUpdates(plans)
		if completeErr != nil {
			runtime.retainAdoptedWithoutLaunch(prepared)
			return createResult(startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(preseedErr, completeErr)))
		}
		runtime.retainAdoptedWithoutLaunch(prepared)
		return createResult(startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, preseedErr))
	}
	plans, err := s.delegateController.CompleteStartInput(claim, true, delegateFinish{})
	s.delegateController.emitDelegateUpdates(plans)
	if err != nil {
		runtime.retainAdoptedWithoutLaunch(prepared)
		return createResult(startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, err))
	}
	bindDelegateActivity(prepared.sub.sess, s.delegateController, started.lease)
	s.startDelegateQuietWatchdog(started.ctx, started.lease)
	degradedSandboxAdvisory := degradedReadOnlyDelegateAdvisory(prepared.sub.sess.currentEnv())
	s.launchSubagentRun(prepared.runCtx, prepared.sub, prepared.runCancel, prepared.input, started.descriptor.Provenance)
	result := createResult(startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, nil))
	// The advisories ride the launched delegate's own result only: they are
	// metadata for the caller's next isolation choice, not delegate output, not
	// an EventWarning, and not durable job state a later delegate_send replays.
	// The sandbox one fires when a derived read-only scope had to degrade on this
	// host, so the parent learns the boundary is advisory for the child's shell
	// rather than discovering it from a clobbered file.
	result.Warnings = appendUniqueStrings(result.Warnings, sharedWorkspaceAdvisory, degradedSandboxAdvisory)
	return result
}

func (s *Session) delegateActor(ctx context.Context) (delegateActor, error) {
	if lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok {
		if s.owningDelegateID == "" || lease.delegateID != s.owningDelegateID {
			return delegateActor{}, errDelegateStaleLease
		}
		return delegateActor{lease: &lease}, nil
	}
	if s.owningDelegateID != "" {
		return delegateActor{}, errDelegateStaleLease
	}
	return rootDelegateActor(s.delegateRootSessionID), nil
}

func (s *Session) delegateEffectiveToolNameCeiling(selection subagentModelSelection, args delegateArgs, isolationName string) []string {
	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(selection.agent, args.grantsDelegation())
	return delegateToolNameCeiling(s.reg, s.resultToolName(), allTools, allowedTools, deniedTools, args.grantsDelegation(), isolationName)
}

func (runtime delegateRuntime) describe(ctx context.Context, args delegateArgs, brief, isolationName string, requestedSandbox *sandbox.SandboxPolicy, selection subagentModelSelection, toolNameCeiling []string) (delegatestore.Descriptor, identifier.Project, error) {
	s := runtime.owner
	s.mu.Lock()
	childConfig := s.cfg.toSnapshot().Clone()
	sharedTaskStoreOwnerSessionID := s.cfg.spawn.sharedTaskStoreOwnerSessionID
	s.mu.Unlock()
	agentType := strings.TrimSpace(args.AgentType)
	if agentType == "" {
		agentType = "default"
	}
	if err := validateDelegateLabel(args.Name); err != nil {
		return delegatestore.Descriptor{}, identifier.Project{}, err
	}
	agentName, rolePrompt := delegateRole(selection, args.grantsDelegation(), s)
	reasoningEffort := llm.NormalizeReasoningEffort(args.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = llm.NormalizeReasoningEffort(childConfig.ReasoningEffort)
	}
	var frozenSkillNames, frozenSkillBodies []string
	var frozenSkillMetadata []schema.FrozenSkillPreload
	if selection.agent != nil {
		for _, name := range selection.agent.Skills {
			// Fresh role preloads use the shared loader/renderer; the frozen
			// descriptor keeps the complete rendered bodies plus their typed
			// provenance for the delegate's lifetime.
			invocationID, err := s.mintSkillOperationID()
			if err != nil {
				// As in prepareSubagentRunFromSelection: a persistence failure is
				// not a resolution failure, and continuing would commit a
				// descriptor whose role preloads are silently missing.
				return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("persisting the skill operation identity for role preload %q: %w", name, err)
			}
			batch, err := s.prepareSkillActivations(ctx, []skillInvocation{{
				Name: name, Route: "role_preload",
				InvocationID: invocationID, AtomicGroupID: invocationID,
			}})
			if err != nil || len(batch.Items) == 0 {
				continue
			}
			item := batch.Items[0]
			if strings.TrimSpace(item.Rendered.Content) != "" {
				frozenSkillNames = append(frozenSkillNames, item.Loaded.Descriptor.CatalogName)
				frozenSkillBodies = append(frozenSkillBodies, item.Rendered.Content)
				frozenSkillMetadata = append(frozenSkillMetadata, frozenPreloadRecord(item))
			}
		}
	}
	resultSchema, err := json.Marshal(args.ResultSchema)
	if err != nil {
		return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("invalid result schema: %w", err)
	}
	if len(args.ResultSchema) == 0 {
		resultSchema = nil
	}
	sandboxSnapshot := delegateSandboxSnapshot(requestedSandbox)
	if requestedSandbox == nil {
		if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok && local.Sandbox != nil && local.Sandbox.Enforced() {
			inherited := local.Sandbox.Inputs()
			sandboxSnapshot = delegateSandboxSnapshot(&inherited)
		}
	}
	childConfig.MaxTurns = 500
	childConfig.AgentName = agentName
	childConfig.ReasoningEffort = reasoningEffort
	childConfig.MCPConfigFiles = nil
	childConfig.MCPInline = nil
	childConfig.Sandbox = ""
	childConfig.SandboxNet = nil
	if sandboxSnapshot != nil {
		childConfig.Sandbox = sandboxSnapshot.Mode
		if sandboxSnapshot.Network != nil {
			network := *sandboxSnapshot.Network
			childConfig.SandboxNet = &network
		}
	}
	if childConfig.ShareTasksWithChildren {
		if sharedTaskStoreOwnerSessionID == "" {
			sharedTaskStoreOwnerSessionID = s.id
		}
	} else {
		sharedTaskStoreOwnerSessionID = ""
	}
	descriptor := delegatestore.Descriptor{
		VisibleSessionID:              s.id,
		Task:                          brief,
		Description:                   brief,
		AgentType:                     agentType,
		RequestedModel:                selection.requestedModel,
		ResolvedProfileID:             selection.profile.ID(),
		ResolvedModel:                 selection.profile.Model(),
		FrozenRolePrompt:              rolePrompt,
		ToolNameCeiling:               append([]string(nil), toolNameCeiling...),
		FrozenSkillNames:              frozenSkillNames,
		FrozenSkillBodies:             frozenSkillBodies,
		FrozenSkillMetadata:           frozenSkillMetadata,
		LocalEnvPolicy:                localEnvPolicyName(s.currentEnv()),
		ResultSchema:                  resultSchema,
		DelegationAllowance:           args.grantedAllowance(),
		WorkingDir:                    s.currentEnv().WorkingDirectory(),
		Isolation:                     isolationName,
		Name:                          args.Name,
		Sandbox:                       sandboxSnapshot,
		Config:                        childConfig,
		SharedTaskStoreOwnerSessionID: sharedTaskStoreOwnerSessionID,
		ParentWatchGranted:            args.WatchParent,
		Provenance:                    s.activeCausalProvenance(),
		Resumable:                     true,
	}
	var roleTasks []task.TaskTemplate
	if selection.agent != nil {
		roleTasks = selection.agent.Tasks
	}
	descriptor.TaskTemplates = task.ExpandParentTasks(roleTasks, args.TaskList)
	if callID, ok := ctx.Value(ctxToolCallID).(string); ok {
		descriptor.OriginToolCallID = callID
	}
	if itemID, ok := ctx.Value(ctxToolItemID).(string); ok {
		descriptor.OriginItemID = itemID
	}
	var project identifier.Project
	if isolationName == "worktree" {
		local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
		if !ok {
			return delegatestore.Descriptor{}, identifier.Project{}, errors.New(`delegate isolation:"worktree" requires a local execution environment`)
		}
		// A dead session anchor cannot cut an honest lane: createDelegateWorktree
		// -> worktreeCreateCore re-resolves the project AND the base SHA from the
		// active root, so refuse here with the named recovery rather than leak the
		// resolve-project lstat (issue #4671).
		if err := s.worktreeAnchorGone(); err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("delegate isolation: %w", err)
		}
		project, err = resolveWorktreeProject(local, local.WorkingDirectory())
		if err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("delegate isolation: resolve project: %w", err)
		}
		root, err := s.worktreeRootForProject(s.currentStateDir(), project)
		if err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, err
		}
		descriptor.WorkingDir = filepath.Join(root, project.ID)
		// Only a worktree lane has a branch to name; everywhere else the name
		// stays the display label the descriptor already carries.
		descriptor.WorktreeBranch = args.Name
	}
	return descriptor, project, nil
}

func delegateRole(selection subagentModelSelection, childCanDelegate bool, s *Session) (string, string) {
	if selection.agent != nil && strings.TrimSpace(selection.agent.SystemPrompt) != "" {
		return selection.agent.Name, selection.agent.SystemPrompt
	}
	if selection.agent == nil && childCanDelegate {
		return "subagent", defaultDelegatingSubagentInstructions
	}
	if subagentAgent, ok := s.pluginAgents["subagent"]; ok {
		return "subagent", subagentAgent.SystemPrompt
	}
	return "subagent", defaultSubagentInstructions
}

func delegateSandboxSnapshot(policy *sandbox.SandboxPolicy) *delegatestore.SandboxSnapshot {
	if policy == nil || policy.Mode == sandbox.ModeOff {
		return nil
	}
	result := &delegatestore.SandboxSnapshot{
		Mode:               policy.Mode.String(),
		WriteBlocked:       policy.WriteBlocked,
		DenylistAdd:        append([]string(nil), policy.DenylistAdd...),
		DenylistRemove:     append([]string(nil), policy.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), policy.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), policy.ExtraReadRoots...),
	}
	if policy.Network != nil {
		network := *policy.Network
		result.Network = &network
	}
	return result
}

func (runtime delegateRuntime) prepareIsolation(ctx context.Context, reservation *delegateStartReservation, project identifier.Project, requestedSandbox *sandbox.SandboxPolicy) (delegateIsolation, error) {
	s := runtime.owner
	isolation := delegateIsolation{worktreeProject: project}
	workingDir := reservation.worktreePath
	var (
		laneAdmission envWorkID
		laneFenced    bool
	)
	// The lane's admission is named for the create until an undo actually
	// starts: a create still in flight must not read as a rollback in a fence
	// warning (worktreeCreate renames its own for the same reason). It renames
	// itself to the lane BEING UNDONE, which is the created path, not the
	// reserved one the admission was opened under — the mismatch arm below is
	// the case where those differ, and it is the created lane the rollback
	// removes.
	rollback := func() {
		if laneFenced {
			s.relabelEnvWork(laneAdmission, "delegate lane rollback for "+isolation.worktreePath)
		}
		isolation.cleanup(s, reservation.delegateID)
	}
	if workingDir != "" {
		// Cutting the lane forks git on the PARENT's environment, whose process
		// table a close reaps, and so does the rollback every failure below owes
		// it. Neither is a manage_worktree operation, so the dispatch's close
		// fence never sees them; admit them here as ONE span, for the reason
		// worktreeCreate's create/rollback admission is one span — an admission
		// asked for where the rollback starts would be refused by the very close
		// that made the rollback necessary, and an unfenced rollback races the
		// environment cleanup it exists to keep residue out of.
		//
		// A refused admission refuses the spawn, which is the dispatch's answer
		// for the dispatch's reason: no lane has been cut yet, and cutting one
		// against an environment being reaped and stores being closed leaves
		// exactly the locked worktree, branch and sidecar this admission is here
		// to prevent.
		laneAdmission, laneFenced = s.beginEnvWork("create delegate lane " + workingDir)
		if !laneFenced {
			return isolation, fmt.Errorf(`delegate isolation:"worktree": %w`, errWorktreeOpWhileClosing)
		}
		defer s.endEnvWork(laneAdmission)
		// createDelegateWorktree owns the empty-means-id default and returns
		// the branch it actually cut, so the isolation record never resolves
		// the rule itself.
		path, laneBranch, _, _, createdProject, err := s.createDelegateWorktree(ctx, reservation.delegateID, reservation.descriptor.WorktreeBranch)
		if err != nil {
			return isolation, err
		}
		isolation.laneBranch = laneBranch
		isolation.worktreePath = path
		isolation.worktreeProject = createdProject
		if filepath.Clean(path) != filepath.Clean(workingDir) {
			rollback()
			return delegateIsolation{}, fmt.Errorf("delegate isolation path %q does not match reserved path %q", path, workingDir)
		}
	}
	env, ownsFresh, err := s.prepareSubagentEnvironmentFor(workingDir, requestedSandbox, reservation.descriptor.ChildSessionID)
	if err != nil {
		rollback()
		return delegateIsolation{}, err
	}
	isolation.env = env
	isolation.ownsFreshEnv = ownsFresh
	return isolation, nil
}

// delegateSandboxFallbackHint closes the host-capability repair loop in the
// first rejection. When no backend can enforce a requested sandbox and the
// caller also supplied sandbox_net, the only enforceable fallback is
// sandbox="off" with sandbox_net omitted. The explicit value is never dropped;
// this only makes the rejection actionable before a retry.
func delegateSandboxFallbackHint(s *Session, args delegateArgs, err error) error {
	if err == nil || s == nil || args.SandboxNet == nil || delegateSandboxBackendAvailable(s.sandboxHostFacts()) {
		return err
	}
	if _, ok := errors.AsType[*sandbox.RefusalError](err); !ok {
		return err
	}
	return newDelegateSandboxRequestError(
		fmt.Errorf(`%w; change sandbox to "off" and omit sandbox_net`, err),
		"sandbox", "sandbox_net",
	)
}

// cleanup runs every rollback the isolation step owes: prepareIsolation's own
// rollback, and the four arms reached after it returns (CommitStart's
// failure, failCommittedStart's arms, and failAdoptedStart). Every one of
// those callers runs inside the lane admission delegateRuntime.create holds
// across the whole spawn, so this takes none of its own.
//
// The rename lives here because every post-prepareIsolation arm funnels
// through this method, and the retain arms that roll nothing back never
// reach it, so the admission reads "rollback" only when one is actually
// running.
func (isolation delegateIsolation) cleanup(s *Session, delegateID string) {
	if isolation.laneFenced && isolation.worktreePath != "" {
		s.relabelEnvWork(isolation.laneAdmission, "delegate lane rollback for "+isolation.worktreePath)
	}
	if isolation.ownsFreshEnv {
		// prepareSubagentRunFromSelection leaves a PREPARED environment alone (it
		// belongs to this isolation step), so this is the only rollback for the
		// scratch the construction's git snapshot minted on an unsandboxed lane,
		// as well as for a sandboxed lane's owned one.
		endEnvironmentScratch(isolation.env)
	}
	if isolation.worktreePath != "" {
		s.rollbackFreshDelegateWorktree(delegateID, isolation.laneBranch, isolation.worktreePath, isolation.worktreeProject)
	}
}

func (runtime delegateRuntime) construct(_ context.Context, args delegateArgs, selection subagentModelSelection, started delegateStartCommit, isolation delegateIsolation) (*preparedSubagentRun, error) {
	s := runtime.owner
	ctx := started.ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if started.descriptor.OriginToolCallID != "" {
		ctx = context.WithValue(ctx, ctxToolCallID, started.descriptor.OriginToolCallID)
	}
	if started.descriptor.OriginItemID != "" {
		ctx = context.WithValue(ctx, ctxToolItemID, started.descriptor.OriginItemID)
	}
	ctx = context.WithValue(ctx, ctxParentDelegateID, started.lease.delegateID)
	ctx = context.WithValue(ctx, ctxDelegationAllowance, started.descriptor.DelegationAllowance)
	ctx = context.WithValue(ctx, delegateChildSessionIDContextKey{}, started.descriptor.ChildSessionID)
	ctx = context.WithValue(ctx, delegatePreparedEnvironmentContextKey{}, delegatePreparedEnvironment{
		env:       isolation.env,
		ownsFresh: isolation.ownsFreshEnv,
	})
	if requested := started.descriptor.Sandbox; requested != nil {
		ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, sandboxPolicyFromStableSnapshot(requested))
	}
	if started.descriptor.Isolation != "" {
		ctx = context.WithValue(ctx, ctxIsolation, started.descriptor.Isolation)
	}
	if started.descriptor.ParentWatchGranted {
		ctx = context.WithValue(ctx, ctxWatchParent, true)
	}
	var inheritedContext []transcript.Entry
	if args.ForkContext {
		var err error
		inheritedContext, err = s.snapshotDelegateContext()
		if err != nil {
			return nil, err
		}
	}
	prepared, err := s.prepareDelegateRun(ctx, started.descriptor, started.descriptor.ParentWatchGranted, selection, inheritedContext)
	if err != nil {
		return nil, err
	}
	if err := started.ctx.Err(); err != nil {
		prepared.disposeUnadopted()
		return nil, err
	}
	if prepared.sub.id != started.descriptor.ChildSessionID {
		prepared.disposeUnadopted()
		return nil, fmt.Errorf("constructed child session %q does not match durable descriptor %q", prepared.sub.id, started.descriptor.ChildSessionID)
	}
	prepared.runCancel()
	runContext, runCancel := context.WithCancel(started.ctx)
	runContext = context.WithValue(runContext, delegateRunLeaseContextKey{}, started.lease)
	runContext = context.WithValue(runContext, delegatePreseededInputContextKey{}, delegatePreseededInput{sessionID: prepared.sub.id, input: prepared.input})
	prepared.runCtx = runContext
	prepared.runCancel = runCancel
	prepared.sub.mu.Lock()
	prepared.sub.cancel = runCancel
	prepared.sub.startedAt = started.startedAt
	prepared.sub.mu.Unlock()
	return prepared, nil
}

func (runtime delegateRuntime) adopt(prepared *preparedSubagentRun) error {
	s := runtime.owner
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("session is closed")
	}
	s.subagents.track(prepared.sub)
	s.sendersWG.Add(1)
	s.mu.Unlock()
	return nil
}

func (runtime delegateRuntime) preseedInput(child *Session, input, transcriptPath string, author schema.TranscriptAuthor) error {
	if err := child.maybeAppendEnvironmentContext(); err != nil {
		return fmt.Errorf("append environment context: %w", err)
	}
	message := buildUserInputMessage(input, nil)
	if observer := child.cfg.testOnly.delegateInitialInputAppend; observer != nil {
		observer(child)
	}
	turn := schema.NewTurn(schema.TurnUserInput, message)
	turn.Authorship = &schema.TranscriptAuthorship{Author: author}
	if err := child.appendTurnAfterTranscriptWrite(turn,
		func() error { return child.writeTranscriptSyncedLocked(turn) },
		func() { child.history = append(child.history, turn) },
	); err != nil {
		return err
	}
	data, err := readStrictChildTranscript(transcriptPath, child.stateDir, child.ID(), child.strictTranscriptMaxLineBytes)
	if err != nil {
		return fmt.Errorf("read back child input transcript: %w", err)
	}
	for _, entry := range slices.Backward(data.Entries) {
		turn := entry.Turn
		if turn.Kind == schema.TurnUserInput {
			if turn.Message.Text() != input {
				return errors.New("read back child input transcript: latest user input differs")
			}
			return nil
		}
	}
	return errors.New("read back child input transcript: user input is absent")
}

func (runtime delegateRuntime) failCommittedStart(started delegateStartCommit, isolation delegateIsolation, prepared *preparedSubagentRun, controllerAttached bool, constructionErr error, reason string) delegateResult {
	finish := delegatePermanentStartFailure(constructionErr, reason)
	var runtimeForClose *Session
	if controllerAttached && prepared != nil {
		runtimeForClose = prepared.sub.sess
	}
	plans, claimedForClose, finishErr := runtime.owner.delegateController.FailCommittedStart(started.lease, finish, reason, runtimeForClose)
	runtime.owner.delegateController.emitDelegateUpdates(plans)
	if committedStartFailureDisposition(finishErr) == delegateCommittedStartFailureStopWon {
		// The stop settled the generation, so this exit owns the rollback of
		// whatever nobody else holds: the unadopted child, unless the controller
		// kept its runtime for its own close, and with it the isolation the
		// construction was building on. The lane and its clone's scratch have no
		// other owner once the start is dead, whether or not a child was built.
		if prepared == nil || !controllerAttached || claimedForClose {
			if prepared != nil {
				prepared.runCancel()
				prepared.disposeUnadopted()
			}
			isolation.cleanup(runtime.owner, started.lease.delegateID)
		}
		return startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, constructionErr)
	}
	if finishErr != nil {
		retainErr := runtime.retainFailedStartCandidate(prepared)
		return startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(constructionErr, finishErr, retainErr))
	}
	if prepared != nil {
		prepared.runCancel()
		prepared.disposeUnadopted()
	}
	isolation.cleanup(runtime.owner, started.lease.delegateID)
	return startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, constructionErr)
}

func (runtime delegateRuntime) retainFailedStartCandidate(prepared *preparedSubagentRun) error {
	if prepared == nil {
		return nil
	}
	prepared.runCancel()
	existing, retained, err := runtime.owner.subagents.trackIfAbsent(prepared.sub)
	if err != nil {
		prepared.disposeUnadopted()
		return err
	}
	if retained || existing == prepared.sub {
		return nil
	}
	prepared.disposeUnadopted()
	return errDelegateTargetBusy
}

func (runtime delegateRuntime) failAdoptedStart(started delegateStartCommit, isolation delegateIsolation, prepared *preparedSubagentRun, startErr error, reason string) delegateResult {
	finish := delegatePermanentStartFailure(startErr, reason)
	plans, _, finishErr := runtime.owner.delegateController.FailCommittedStart(started.lease, finish, reason, nil)
	runtime.owner.delegateController.emitDelegateUpdates(plans)
	if finishErr != nil {
		runtime.retainAdoptedWithoutLaunch(prepared)
		return startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(startErr, finishErr))
	}
	runtime.discardAdopted(prepared)
	isolation.cleanup(runtime.owner, started.lease.delegateID)
	return startedDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, startErr)
}

func (runtime delegateRuntime) retainAdoptedWithoutLaunch(prepared *preparedSubagentRun) {
	prepared.runCancel()
	runtime.owner.sendersWG.Done()
}

func (runtime delegateRuntime) discardAdopted(prepared *preparedSubagentRun) {
	runtime.owner.subagents.remove(prepared.sub.id)
	prepared.runCancel()
	prepared.disposeUnadopted()
	runtime.owner.sendersWG.Done()
}

func delegatePermanentStartFailure(err error, reason string) delegateFinish {
	message := "delegate start failed"
	if err != nil {
		message = err.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	raw, _ := json.Marshal(message)
	packet := &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: raw}
	return delegateFinish{
		outcome:     delegatestore.OutcomeFailed,
		disposition: delegatestore.DispositionTerminalError,
		reason:      reason,
		packet:      packet,
	}
}

func startedDelegateResult(descriptor delegatestore.Descriptor, delegateID string, committed delegateUpdatePlan, plans delegateMutationPlans, err error) delegateResult {
	snapshot := latestDelegateMutationSnapshot(delegateID, committed, plans)
	resumable := snapshot.resumable
	status := jobstore.Status(snapshot.lifecycle)
	if status == "" {
		status = jobstore.StatusRunning
	}
	result := delegateResult{
		DelegateID:          delegateID,
		ChildSessionID:      descriptor.ChildSessionID,
		Type:                delegateResourceType,
		Status:              status,
		Name:                descriptor.Name,
		AgentType:           descriptor.AgentType,
		Tools:               append([]string(nil), descriptor.ToolNameCeiling...),
		Resumable:           &resumable,
		RunningInBackground: true,
		TranscriptRef:       descriptor.TranscriptRef,
		Model:               descriptor.ResolvedProfileID + "/" + descriptor.ResolvedModel,
		Err:                 err,
	}
	if snapshot.lastOutcome != nil {
		result.Reason = snapshot.lastOutcome.Reason
	}
	if descriptor.Sandbox != nil {
		network := true
		if descriptor.Sandbox.Network != nil {
			network = *descriptor.Sandbox.Network
		}
		result.Sandbox = &delegateSandboxReport{Mode: descriptor.Sandbox.Mode, Network: network}
	}
	return result
}

func latestDelegateMutationSnapshot(delegateID string, committed delegateUpdatePlan, plans delegateMutationPlans) delegateSnapshot {
	var latest delegateSnapshot
	for _, row := range committed.rows {
		if row.id == delegateID {
			latest = row
		}
	}
	if latest.id == "" && len(committed.rows) > 0 {
		latest = committed.rows[len(committed.rows)-1]
	}
	for _, update := range plans.updates {
		for _, row := range update.rows {
			if row.id == delegateID {
				latest = row
			}
		}
	}
	return latest
}
