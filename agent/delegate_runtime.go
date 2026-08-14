package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

type delegateRuntime struct {
	owner *Session
}

type delegateRunLeaseContextKey struct{}

type delegatePreseededInput struct {
	sessionID string
	input     string
}

type delegatePreseededInputContextKey struct{}

type delegateChildSessionIDContextKey struct{}

type delegateIsolation struct {
	env             execenv.ExecutionEnvironment
	ownsFreshEnv    bool
	worktreePath    string
	worktreeProject identifier.Project
}

func delegateInputWasPreseeded(ctx context.Context, sessionID, input string) bool {
	preseeded, ok := ctx.Value(delegatePreseededInputContextKey{}).(delegatePreseededInput)
	return ok && preseeded.sessionID == sessionID && preseeded.input == input
}

func (s *Session) createDelegate(ctx context.Context, args delegateArgs) delegateResult {
	return (delegateRuntime{owner: s}).create(ctx, args)
}

func (runtime delegateRuntime) create(ctx context.Context, args delegateArgs) delegateResult {
	s := runtime.owner
	if s == nil || s.delegateController == nil {
		return delegateStartFailed(errors.New("delegate controller is unavailable"))
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return delegateStartFailed(errors.New("invalid_request: task is required"))
	}
	isolationName := strings.TrimSpace(args.Isolation)
	if isolationName != "" && isolationName != "worktree" {
		return delegateStartFailed(fmt.Errorf("invalid_request: isolation %q is not supported (expected \"worktree\")", isolationName))
	}
	if strings.TrimSpace(s.stateDir) == "" {
		return delegateStartFailed(errors.New("delegate creation requires a durable state directory"))
	}
	s.mu.Lock()
	ownAllowance := s.delegationAllowance
	s.mu.Unlock()
	if ok, validRange := validateDelegateGrant(args.DelegationAllowance, ownAllowance); !ok {
		return delegateStartFailed(fmt.Errorf("invalid_request: delegation_allowance must be less than your own allowance (%d); valid grants: %s", ownAllowance, validRange))
	}
	if err := llm.ValidateReasoningEffort(args.ReasoningEffort); err != nil {
		return delegateStartFailed(err)
	}
	selection, err := s.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		return delegateStartFailed(err)
	}
	if selection.warning != nil {
		s.emitDiagnosticWarning(*selection.warning)
	}
	var requestedSandbox *sandbox.SandboxPolicy
	if strings.TrimSpace(args.Sandbox) != "" || args.SandboxNet != nil {
		parentMode, parentNetwork := s.parentSandboxModeNet()
		requestedSandbox, err = resolveDelegateSandboxRequest(args.Sandbox, args.SandboxNet, parentMode, parentNetwork)
		if err != nil {
			return delegateStartFailed(err)
		}
	}
	descriptor, worktreeProject, err := runtime.describe(ctx, args, task, isolationName, requestedSandbox, selection)
	if err != nil {
		return delegateStartFailed(err)
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return delegateStartFailed(err)
	}
	reservation, err := s.delegateController.ReserveCreate(actor, descriptor)
	if err != nil {
		return delegateStartFailed(err)
	}
	isolation, err := runtime.prepareIsolation(ctx, reservation, worktreeProject, requestedSandbox)
	if err != nil {
		abortErr := s.delegateController.AbortStart(reservation)
		isolation.cleanup(s, reservation.delegateID)
		return delegateStartFailed(errors.Join(err, abortErr))
	}
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		isolation.cleanup(s, reservation.delegateID)
		return delegateStartFailed(err)
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	prepared, err := runtime.construct(ctx, args, selection, started, isolation)
	if err != nil {
		return runtime.failCommittedStart(started, isolation, nil, false, err, "construction_failed")
	}
	if err := s.delegateController.AttachRuntime(started.lease, prepared.sub.sess); err != nil {
		return runtime.failCommittedStart(started, isolation, prepared, false, err, "construction_failed")
	}
	if err := runtime.adopt(prepared); err != nil {
		return runtime.failCommittedStart(started, isolation, prepared, true, err, "construction_failed")
	}
	claim, err := s.delegateController.BeginStartInput(started.lease)
	if err != nil {
		return runtime.failAdoptedStart(started, isolation, prepared, err, "input_admission_failed")
	}
	preseedErr := runtime.preseedInput(prepared.sub.sess, task, started.transcriptPath)
	if preseedErr != nil {
		finish := delegatePermanentStartFailure(preseedErr, "input_persist_failed")
		plans, completeErr := s.delegateController.CompleteStartInput(claim, false, finish)
		s.delegateController.emitDelegateUpdates(plans)
		if completeErr != nil {
			runtime.retainAdoptedWithoutLaunch(prepared)
			return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(preseedErr, completeErr))
		}
		runtime.retainAdoptedWithoutLaunch(prepared)
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, preseedErr)
	}
	plans, err := s.delegateController.CompleteStartInput(claim, true, delegateFinish{})
	s.delegateController.emitDelegateUpdates(plans)
	if err != nil {
		runtime.retainAdoptedWithoutLaunch(prepared)
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, err)
	}
	s.launchSubagentRun(prepared.runCtx, prepared.sub, prepared.runCancel, prepared.input, started.descriptor.Provenance)
	return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, nil)
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

func (runtime delegateRuntime) describe(ctx context.Context, args delegateArgs, task, isolationName string, requestedSandbox *sandbox.SandboxPolicy, selection subagentModelSelection) (delegatestore.Descriptor, identifier.Project, error) {
	s := runtime.owner
	agentType := strings.TrimSpace(args.AgentType)
	if agentType == "" {
		agentType = "default"
	}
	agentName, rolePrompt := stableDelegateRole(selection, args.DelegationAllowance > 0, s)
	reasoningEffort := strings.TrimSpace(args.ReasoningEffort)
	if reasoningEffort == "" {
		s.mu.Lock()
		reasoningEffort = strings.TrimSpace(s.cfg.ReasoningEffort)
		s.mu.Unlock()
	}
	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(selection.agent, args.DelegationAllowance > 0)
	if !allTools {
		allowedTools = ensureRecoveryReader(allowedTools, s.reg)
	}
	frozenTools := frozenStableDelegateToolNames(s.reg, s.resultToolName(), allTools, allowedTools, deniedTools, args.DelegationAllowance > 0, args.WatchParent, isolationName)
	var frozenSkillNames, frozenSkillBodies []string
	if selection.agent != nil {
		for _, name := range selection.agent.Skills {
			body, err := skill.ResolveSkillContent(s.skills, name)
			if err == nil && strings.TrimSpace(body) != "" {
				frozenSkillNames = append(frozenSkillNames, name)
				frozenSkillBodies = append(frozenSkillBodies, body)
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
	sandboxSnapshot := stableDelegateSandboxSnapshot(requestedSandbox)
	if requestedSandbox == nil {
		if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok && local.Sandbox != nil && local.Sandbox.Enforced() {
			inherited := local.Sandbox.Inputs()
			sandboxSnapshot = stableDelegateSandboxSnapshot(&inherited)
		}
	}
	descriptor := delegatestore.Descriptor{
		VisibleSessionID:    s.id,
		Task:                task,
		Description:         task,
		AgentType:           agentType,
		RequestedModel:      selection.requestedModel,
		ResolvedProfileID:   selection.profile.ID(),
		ResolvedModel:       selection.profile.Model(),
		ReasoningEffort:     reasoningEffort,
		AgentName:           agentName,
		FrozenRolePrompt:    rolePrompt,
		FrozenToolNames:     frozenTools,
		FrozenSkillNames:    frozenSkillNames,
		FrozenSkillBodies:   frozenSkillBodies,
		LocalEnvPolicy:      localEnvPolicyName(s.currentEnv()),
		ResultSchema:        resultSchema,
		DelegationAllowance: args.DelegationAllowance,
		WorkingDir:          s.currentEnv().WorkingDirectory(),
		Isolation:           isolationName,
		Sandbox:             sandboxSnapshot,
		Provenance:          s.activeCausalProvenance(),
		Resumable:           true,
	}
	if selection.agent != nil && len(selection.agent.Tasks) > 0 {
		descriptor.FrozenTaskPrompt = selection.agent.Tasks[0].Prompt
	}
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
		project, err = resolveWorktreeProject(local, local.WorkingDirectory())
		if err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("delegate isolation: resolve project: %w", err)
		}
		root, err := s.worktreeRootForProject(s.currentStateDir(), project)
		if err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, err
		}
		descriptor.WorkingDir = filepath.Join(root, project.ID)
	}
	return descriptor, project, nil
}

func stableDelegateRole(selection subagentModelSelection, childCanDelegate bool, s *Session) (string, string) {
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

func stableDelegateSandboxSnapshot(policy *sandbox.SandboxPolicy) *delegatestore.SandboxSnapshot {
	if policy == nil || policy.Mode == sandbox.ModeOff {
		return nil
	}
	result := &delegatestore.SandboxSnapshot{
		Mode:               policy.Mode.String(),
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
	if workingDir != "" {
		path, _, _, _, createdProject, err := s.createDelegateWorktree(ctx, reservation.delegateID)
		if err != nil {
			return isolation, err
		}
		isolation.worktreePath = path
		isolation.worktreeProject = createdProject
		if filepath.Clean(path) != filepath.Clean(workingDir) {
			isolation.cleanup(s, reservation.delegateID)
			return delegateIsolation{}, fmt.Errorf("delegate isolation path %q does not match reserved path %q", path, workingDir)
		}
	}
	env, ownsFresh, err := s.prepareSubagentEnvironment(workingDir, requestedSandbox)
	if err != nil {
		isolation.cleanup(s, reservation.delegateID)
		return delegateIsolation{}, err
	}
	isolation.env = env
	isolation.ownsFreshEnv = ownsFresh
	return isolation, nil
}

func (isolation delegateIsolation) cleanup(s *Session, delegateID string) {
	if isolation.ownsFreshEnv {
		if local, ok := isolation.env.(*execenv.LocalExecutionEnvironment); ok {
			local.DisposeSandboxScratch()
		}
	}
	if isolation.worktreePath != "" {
		s.rollbackFreshDelegateWorktree(delegateID, isolation.worktreePath, isolation.worktreeProject)
	}
}

func (runtime delegateRuntime) construct(ctx context.Context, args delegateArgs, selection subagentModelSelection, started delegateStartCommit, isolation delegateIsolation) (*preparedSubagentRun, error) {
	s := runtime.owner
	sourceContext := ctx
	ctx = started.ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if callID, ok := sourceContext.Value(ctxToolCallID).(string); ok {
		ctx = context.WithValue(ctx, ctxToolCallID, callID)
	}
	if itemID, ok := sourceContext.Value(ctxToolItemID).(string); ok {
		ctx = context.WithValue(ctx, ctxToolItemID, itemID)
	}
	ctx = context.WithValue(ctx, ctxParentJobID, "")
	ctx = context.WithValue(ctx, ctxParentDelegateID, started.lease.delegateID)
	ctx = context.WithValue(ctx, ctxDelegationAllowance, started.descriptor.DelegationAllowance)
	ctx = context.WithValue(ctx, delegateChildSessionIDContextKey{}, started.descriptor.ChildSessionID)
	ctx = context.WithValue(ctx, delegatePreparedEnvironmentContextKey{}, delegatePreparedEnvironment{
		env:              isolation.env,
		ownsFresh:        isolation.ownsFreshEnv,
		stableController: true,
	})
	if requested := started.descriptor.Sandbox; requested != nil {
		ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, sandboxPolicyFromStableSnapshot(requested))
	}
	if started.descriptor.Isolation != "" {
		ctx = context.WithValue(ctx, ctxIsolation, started.descriptor.Isolation)
	}
	if args.WatchParent {
		ctx = context.WithValue(ctx, ctxWatchParent, true)
	}
	prepared, err := s.prepareStableDelegateRun(ctx, started.descriptor, args.WatchParent, selection)
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
	prepared.sub.mu.Unlock()
	return prepared, nil
}

func sandboxPolicyFromStableSnapshot(snapshot *delegatestore.SandboxSnapshot) *sandbox.SandboxPolicy {
	if snapshot == nil {
		return nil
	}
	mode, err := sandbox.ParseMode(snapshot.Mode)
	if err != nil {
		return nil
	}
	policy := &sandbox.SandboxPolicy{
		Mode:               mode,
		DenylistAdd:        append([]string(nil), snapshot.DenylistAdd...),
		DenylistRemove:     append([]string(nil), snapshot.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), snapshot.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), snapshot.ExtraReadRoots...),
	}
	if snapshot.Network != nil {
		network := *snapshot.Network
		policy.Network = &network
	}
	return policy
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

func (runtime delegateRuntime) preseedInput(child *Session, input, transcriptPath string) error {
	child.maybeAppendEnvironmentContext()
	message := buildUserInputMessage(input, nil)
	if err := child.appendTurnWithDurableTranscriptMessage(schema.TurnUserInput, message, message); err != nil {
		return err
	}
	data, err := readStrictChildTranscript(transcriptPath, child.ID(), child.strictTranscriptMaxLineBytes)
	if err != nil {
		return fmt.Errorf("read back child input transcript: %w", err)
	}
	for index := len(data.Entries) - 1; index >= 0; index-- {
		turn := data.Entries[index].Turn
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
		if prepared != nil && (!controllerAttached || claimedForClose) {
			prepared.runCancel()
			prepared.disposeUnadopted()
		}
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, constructionErr)
	}
	if finishErr != nil {
		retainErr := runtime.retainFailedStartCandidate(prepared)
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(constructionErr, finishErr, retainErr))
	}
	if prepared != nil {
		prepared.runCancel()
		prepared.disposeUnadopted()
	}
	isolation.cleanup(runtime.owner, started.lease.delegateID)
	return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, constructionErr)
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
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(startErr, finishErr))
	}
	runtime.discardAdopted(prepared)
	isolation.cleanup(runtime.owner, started.lease.delegateID)
	return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, startErr)
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

func stableDelegateResult(descriptor delegatestore.Descriptor, delegateID string, committed delegateUpdatePlan, plans delegateMutationPlans, err error) delegateResult {
	snapshot := latestDelegateMutationSnapshot(delegateID, committed, plans)
	resumable := snapshot.resumable
	result := delegateResult{
		DelegateID:          delegateID,
		ChildSessionID:      descriptor.ChildSessionID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		Resumable:           &resumable,
		RunningInBackground: true,
		TranscriptRef:       descriptor.TranscriptRef,
		Model:               descriptor.ResolvedProfileID + "/" + descriptor.ResolvedModel,
		Err:                 err,
	}
	if snapshot.lastOutcome != nil {
		result.Status = jobstore.Status(snapshot.lastOutcome.Status)
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

func (s *Session) bootstrapDelegateResources() error {
	if inherited := s.cfg.spawn.delegateController; inherited != nil {
		s.delegateController = inherited
		s.delegateRootSessionID = s.cfg.spawn.delegateRootSessionID
		s.owningDelegateID = s.cfg.spawn.owningDelegateID
		return nil
	}
	if err := rejectLegacyDelegateState(s.stateDir, s.id); err != nil {
		return err
	}
	path := filepath.Join(jobsDir(s.stateDir, s.id), "delegates.jsonl")
	store, err := delegatestore.Open(path)
	if err != nil {
		return fmt.Errorf("open delegate store: %w", err)
	}
	controller, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: s.id,
		stateDir:      s.stateDir,
		worktreeRoot:  filepath.Join(jobsDir(s.stateDir, s.id), "worktrees"),
		turnLimit:     s.cfg.MaxConcurrentDelegateTurns,
		driveLimit:    defaultMaxConcurrentDriveTurns,
		now:           s.sclock().Now,
	})
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open delegate controller: %w", err)
	}
	evidence, err := collectDelegateReconcileEvidence(s.stateDir, controller.ReconcileRequirements())
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("collect delegate reconcile evidence: %w", err)
	}
	if _, err := controller.Reconcile(evidence); err != nil {
		_ = store.Close()
		return fmt.Errorf("reconcile delegate resources: %w", err)
	}
	missingInputs, err := missingDelegateRestoreInputs(s.stateDir, controller)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("inspect delegate restore inputs: %w", err)
	}
	if _, err := controller.closeMissingRestoreInputs(missingInputs); err != nil {
		_ = store.Close()
		return fmt.Errorf("close delegates with missing restore inputs: %w", err)
	}
	s.delegateController = controller
	s.delegateRootSessionID = s.id
	s.ownsDelegateController = true
	return nil
}

func missingDelegateRestoreInputs(stateDir string, controller *delegateTreeController) (map[string]string, error) {
	controller.mu.Lock()
	ids := make([]string, 0, len(controller.durable))
	descriptors := make(map[string]delegatestore.Descriptor, len(controller.durable))
	for id, aggregate := range controller.durable {
		if aggregate == nil || !aggregate.Resumable || aggregate.Phase != delegatestore.PhaseIdle {
			continue
		}
		ids = append(ids, id)
		descriptors[id] = cloneDelegateStartDescriptor(aggregate.Descriptor)
	}
	controller.mu.Unlock()
	sort.Strings(ids)
	reasons := make(map[string]string)
	for _, id := range ids {
		reason, err := missingDelegateRestoreInputReason(stateDir, descriptors[id])
		if err != nil {
			return nil, fmt.Errorf("delegate %s: %w", id, err)
		}
		if reason != "" {
			reasons[id] = reason
		}
	}
	return reasons, nil
}

func missingDelegateRestoreInputReason(stateDir string, descriptor delegatestore.Descriptor) (string, error) {
	childID := strings.TrimSpace(descriptor.ChildSessionID)
	if childID == "" || strings.TrimSpace(descriptor.Task) == "" || strings.TrimSpace(descriptor.AgentType) == "" || strings.TrimSpace(descriptor.ResolvedProfileID) == "" || strings.TrimSpace(descriptor.ResolvedModel) == "" {
		return notResumableMissingDelegateResumeMetadata, nil
	}
	_, transcriptChildID, err := decodeRef(descriptor.TranscriptRef)
	if err != nil || transcriptChildID != childID {
		return notResumableParentLinkageUnavailable, nil
	}
	if strings.TrimSpace(stateDir) == "" {
		return notResumableMissingChildSessionMeta, nil
	}
	if workingDir := strings.TrimSpace(descriptor.WorkingDir); workingDir != "" {
		if _, err := os.Stat(workingDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return notResumableWorkingDirMissing, nil
			}
			return "", fmt.Errorf("stat working directory %s: %w", workingDir, err)
		}
	}
	metaPath := filepath.Join(stateDir, sessionsSubdir, childID+".meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notResumableMissingChildSessionMeta, nil
		}
		return "", fmt.Errorf("read child session metadata %s: %w", childID, err)
	}
	var meta schema.SessionMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return notResumableCorruptChildSessionMeta, nil
	}
	if strings.TrimSpace(meta.ID) != childID {
		return notResumableCorruptChildSessionMeta, nil
	}
	path := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notResumableMissingChildTranscript, nil
		}
		return "", fmt.Errorf("stat child transcript %s: %w", childID, err)
	}
	if _, err := validateStrictChildTranscript(path, childID, 0); err != nil {
		if delegateRestoreOperationalIOError(err) {
			return "", fmt.Errorf("validate child transcript %s: %w", childID, err)
		}
		if errors.Is(err, errStrictChildTranscriptSessionMismatch) {
			return notResumableTranscriptSessionMismatch, nil
		}
		if errors.Is(err, errStrictChildTranscriptCorrupt) || errors.Is(err, transcript.ErrUnsupportedFormat) {
			return notResumableCorruptChildTranscript, nil
		}
		return "", fmt.Errorf("validate child transcript %s: %w", childID, err)
	}
	return "", nil
}

func delegateRestoreOperationalIOError(err error) bool {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return false
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return true
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno)
}

func (s *Session) closeOwnedDelegateStore() error {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil || s.delegateController.store == nil {
		return nil
	}
	return s.delegateController.store.Close()
}
