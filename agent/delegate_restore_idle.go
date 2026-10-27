package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
)

func (runtime delegateRuntime) restoreIdle(started delegateStartCommit) (*subagent, bool, error) {
	s := runtime.owner
	if s == nil || started.lease.delegateID == "" {
		return nil, false, errors.New("delegate restore reservation is unavailable")
	}
	release, err := s.beginRetirementMutation("delegate_restore")
	if err != nil {
		return nil, false, err
	}
	defer release()
	descriptor := cloneDelegateStartDescriptor(started.descriptor)
	if retained := s.subagents.get(descriptor.ChildSessionID); retained != nil && retained.sess != nil {
		return retained, false, nil
	}
	policy, err := s.restoreDelegateSandboxFloor(&descriptor)
	if err != nil {
		return nil, false, err
	}
	if err := s.reclaimDelegateRuntimeCapacity(1); err != nil {
		return nil, false, err
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, descriptor.ChildSessionID)
	if err != nil {
		return nil, false, fmt.Errorf("load committed delegate session metadata: %w", err)
	}
	if meta.ID != descriptor.ChildSessionID {
		return nil, false, errors.New("committed delegate session metadata has the wrong identity")
	}
	meta.Config = descriptor.Config.Clone()
	profile, err := s.resolveDelegateRestoreProfileRef(s.currentProfile(), descriptor.ResolvedProfileID, descriptor.ResolvedModel)
	if err != nil {
		return nil, false, fmt.Errorf("resolve committed delegate profile: %w", err)
	}
	resultSchema := make(map[string]any)
	if len(descriptor.ResultSchema) != 0 {
		if err := json.Unmarshal(descriptor.ResultSchema, &resultSchema); err != nil {
			return nil, false, fmt.Errorf("decode committed delegate result schema: %w", err)
		}
		if len(resultSchema) != 0 {
			profile = provider.WithCommunicateOutputSchema(profile, resultSchema)
		}
	}
	// A shared child (no worktree isolation, no per-delegate sandbox) ran on its
	// parent's own environment object; reconstruct it on that SAME parent object
	// rather than a fresh clone. Plan 654: "shared consumers of one binding reuse
	// one environment, not fresh scratch" — a clone would be a second owned
	// environment carrying the parent's binding, and (for a parked root) would
	// not be the root's restored worktreeRestoreEnv object.
	var childEnv execenv.ExecutionEnvironment
	var ownsFresh bool
	if shared := s.sharedRestoreEnvironment(descriptor); shared != nil {
		childEnv = shared
	} else {
		childEnv, ownsFresh, err = s.prepareSubagentEnvironmentFor(descriptor.WorkingDir, policy, descriptor.ChildSessionID)
		if err != nil {
			return nil, false, err
		}
	}
	discardEnv := true
	defer func() {
		// The construction below runs the child's git snapshot, which is what
		// mints an unsandboxed environment's scratch, so a failure after that
		// point has one to drop as surely as a sandboxed restore has its owned one.
		// A shared environment belongs to the live parent, which keeps its scratch.
		if discardEnv && ownsFresh {
			endEnvironmentScratch(childEnv)
		}
	}()
	if childEnv == nil || childEnv.WorkingDirectory() != descriptor.WorkingDir || localEnvPolicyName(childEnv) != descriptor.LocalEnvPolicy || !frozenStableDelegateSandboxMatches(childEnv, descriptor.Sandbox) {
		return nil, false, errors.New("committed delegate environment is unavailable")
	}
	activatedSkillBodies, err := restoreFrozenSkillBodies(descriptor.FrozenSkillNames, descriptor.FrozenSkillBodies)
	if err != nil {
		return nil, false, err
	}
	var sharedStore *task.TaskStore
	if descriptor.Config.ShareTasksWithChildren {
		sharedStore, err = s.resolveStableSharedTaskStore(descriptor.SharedTaskStoreOwnerSessionID)
		if err != nil {
			return nil, false, err
		}
	}
	restoreCfg := RestoreSessionConfig{
		MemoryStateRoot:         s.cfg.MemoryStateRoot,
		DisableMemory:           s.cfg.DisableMemory || descriptor.Config.DisableMemory,
		parentMemoryProjectID:   s.cfg.MemoryProjectID,
		LifetimeContext:         s.cfg.LifetimeContext,
		StateDir:                s.stateDir,
		Project:                 s.cfg.Project,
		ResolveProfile:          s.resolveProfile,
		AcquireSessionOwnership: s.cfg.AcquireSessionOwnership,
		ModelFallbacks:          append([]string(nil), descriptor.Config.ModelFallbacks...),
		LLMRetryPolicy:          s.cfg.LLMRetryPolicy,
		LLMSleep:                s.cfg.LLMSleep,
		clock:                   s.clock,
		testOnly:                s.cfg.testOnly,
		// The personal doc belongs to whoever runs the tree now, not to the run that froze this delegate.
		AgentsDocPath:           s.cfg.AgentsDocPath,
		TurnEndsProcess:         s.cfg.TurnEndsProcess,
		ForceRealIO:             s.cfg.ForceRealIO,
		artifactStore:           s.artifactStore,
		artifactStoreInherited:  true,
		deferRestoreSideEffects: true,
		sandboxProvisioned:      true,
		spawn: spawnConfig{
			delegateController:            s.delegateController,
			retirementController:          s.retirementController.Load(),
			delegateRootSessionID:         s.delegateRootSessionID,
			owningDelegateID:              started.lease.delegateID,
			subscriberCount:               s.subscriberCountFn,
			parentSessionID:               s.id,
			parentToolCallID:              descriptor.OriginToolCallID,
			parentItemID:                  descriptor.OriginItemID,
			parentDelegateID:              started.lease.delegateID,
			forwardJobEvent:               s.jobManager.forwardEvent,
			descendantEvent:               s.cfg.spawn.descendantEvent,
			descendantRecorded:            s.cfg.spawn.descendantRecorded,
			parentSteer:                   s.SteerWithProvenance,
			parentSystemNotification:      s.routeSystemNotification,
			subagentTask:                  descriptor.Task,
			depth:                         s.depth + 1,
			delegationAllowance:           descriptor.DelegationAllowance,
			sharedTaskStore:               sharedStore,
			sharedTaskStoreOwnerSessionID: descriptor.SharedTaskStoreOwnerSessionID,
			rolePromptOverride:            descriptor.FrozenRolePrompt,
			activatedSkillNames:           append([]string(nil), descriptor.FrozenSkillNames...),
			activatedSkillBodies:          activatedSkillBodies,
			frozenSkillMetadata:           append([]schema.FrozenSkillPreload(nil), descriptor.FrozenSkillMetadata...),
			toolNameCeiling:               append([]string(nil), descriptor.ToolNameCeiling...),
			deniedToolNames:               s.unavailableMemoryToolNames(),
			isolation:                     descriptor.Isolation,
			communicateOutputSchema:       cloneMap(resultSchema),
			parentWatchGranted:            descriptor.ParentWatchGranted,
			jobActivityClock:              s.jobActivityClock,
		},
	}
	child, err := RestoreSessionFromMetaWithConfig(s.client, profile, childEnv, meta, restoreCfg)
	if err != nil {
		return nil, false, err
	}
	child.recordEnvironmentOwnership(childEnv, ownsFresh)
	discardEnv = false
	if child.delegateController != s.delegateController || child.owningDelegateID != started.lease.delegateID {
		child.discardRestoredCandidate()
		return nil, false, errors.New("restored delegate did not inherit the exact controller binding")
	}
	for name := range child.reg.RegisteredNames() {
		if !hasString(descriptor.ToolNameCeiling, name) {
			child.discardRestoredCandidate()
			return nil, false, fmt.Errorf("restored delegate tool %q exceeds the committed ceiling", name)
		}
	}
	if len(descriptor.TaskTemplates) != 0 {
		childStore := child.getOrCreateTaskStore()
		// seeded records that THIS restore populated the store. A spawned
		// delegate always writes its own task file, so a normal restore finds
		// the tasks already there and the persisted queue redelivers the plan.
		// Only a store that lost its tasks needs the announcement back.
		seeded := false
		if err := childStore.MutateAndPublish(func(epoch, revision uint64) error {
			if len(childStore.View()) != 0 {
				return nil
			}
			if err := childStore.PopulateFromTemplates(descriptor.TaskTemplates, nil); err != nil {
				return err
			}
			seeded = true
			summary := task.Summarize(childStore.View())
			child.emit(taskUpdatedData(summary, child.taskStoreOwnerSessionID(), epoch, revision))
			return nil
		}); err != nil {
			child.discardRestoredCandidate()
			return nil, false, fmt.Errorf("restore committed delegate tasks: %w", err)
		}
		// A shared store is the parent's list, not this child's plan, so it is
		// never announced. Keep the single current-task reminder for it.
		if descriptor.SharedTaskStoreOwnerSessionID != "" {
			if current, ok := childStore.CurrentInProgress(); ok {
				if err := child.SteerKind(formatCurrentTaskSteering(current, child.canInstructTool("task_list")), events.SteeringKindCurrentTask); err != nil {
					child.discardRestoredCandidate()
					return nil, false, err
				}
			}
		} else if seeded || !child.hasTaskListAnnouncement() {
			// Tasks present is not proof the plan was announced: the store is
			// written before the steering, so a crash between them leaves tasks
			// with nothing queued and nothing delivered. The transcript is the
			// durable record of delivery, so announce when it shows no plan.
			if err := steerDelegatePlan(child, childStore); err != nil {
				child.discardRestoredCandidate()
				return nil, false, err
			}
		}
	}
	now := s.sclock().Now()
	stableDescriptor := cloneDelegateStartDescriptor(descriptor)
	sub := &subagent{
		id:               descriptor.ChildSessionID,
		sess:             child,
		status:           SubagentCompleted,
		nudgeEnabled:     descriptor.Config.AgentName == "subagent",
		agentType:        descriptor.AgentType,
		createdAt:        now,
		startedAt:        now,
		endedAt:          &now,
		stableDescriptor: &stableDescriptor,
	}
	child.SetNotifyFunc(func() { s.driveChildIfNotStopGated(sub) })
	return sub, true, nil
}

// sharedRestoreEnvironment resolves the parent environment a shared child
// occupied before a restart. A delegate created without worktree isolation and
// without a per-delegate sandbox runs on its parent's own environment object
// (prepareSubagentEnvironment returns s.currentEnv() for an empty working-dir
// override), so its cold restore must hand it that SAME reconstructed object
// rather than a fresh clone — plan 654's "shared consumers of one binding reuse
// one environment, not fresh scratch", and the reason a parked root's restored
// worktreeRestoreEnv can be the child's environment. A clone would be a second
// owned environment carrying the parent's binding identity and would fail the
// plan's "same reconstructed E0 object" checkpoint. It returns nil for a
// non-shared child, or when no held environment matches the committed working
// directory (the caller then constructs one).
func (s *Session) sharedRestoreEnvironment(descriptor delegatestore.Descriptor) *execenv.LocalExecutionEnvironment {
	if descriptor.Isolation != "" || descriptor.Sandbox != nil {
		return nil
	}
	want := filepath.Clean(descriptor.WorkingDir)
	if want == "" || want == "." {
		return nil
	}
	s.mu.Lock()
	candidates := make([]*execenv.LocalExecutionEnvironment, 0, 3+len(s.abandonedEnvs))
	if local, ok := s.env.(*execenv.LocalExecutionEnvironment); ok {
		candidates = append(candidates, local)
	}
	candidates = append(candidates, s.worktreeRestoreEnv)
	if local, ok := s.parentSharedEnv.(*execenv.LocalExecutionEnvironment); ok {
		candidates = append(candidates, local)
	}
	candidates = append(candidates, s.abandonedEnvs...)
	s.mu.Unlock()
	for _, candidate := range candidates {
		if candidate != nil && filepath.Clean(candidate.WorkingDirectory()) == want {
			return candidate
		}
	}
	return nil
}

func (runtime delegateRuntime) restoreIdleForSend(started delegateStartCommit) (*subagent, bool, func(*subagent, error), error) {
	s := runtime.owner
	finish := func(*subagent, error) {}
	if s == nil {
		return nil, false, finish, errors.New("delegate restore reservation is unavailable")
	}
	release, err := s.beginRetirementMutation("delegate_restore")
	if err != nil {
		return nil, false, finish, err
	}
	// Every caller finishes after installation and deferred effects or rollback.
	finish = func(*subagent, error) { release() }
	childID := strings.TrimSpace(started.descriptor.ChildSessionID)
	if childID == "" {
		return nil, false, finish, errors.New("delegate restore child identity is unavailable")
	}
	existing, pending, leader, err := s.subagents.beginReconstruction(childID)
	if err != nil {
		return nil, false, finish, err
	}
	if !leader {
		if existing != nil {
			return existing, false, finish, nil
		}
		reconstructed, waitErr := pending.wait()
		return reconstructed, false, finish, waitErr
	}
	finish = func(sub *subagent, restoreErr error) {
		defer release()
		s.subagents.finishReconstruction(childID, pending, sub, restoreErr)
	}
	sub, restored, restoreErr := runtime.restoreIdle(started)
	return sub, restored, finish, restoreErr
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
		WriteBlocked:       snapshot.WriteBlocked,
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

func (s *Session) resolveStableSharedTaskStore(ownerSessionID string) (*task.TaskStore, error) {
	if s == nil || s.delegateController == nil || strings.TrimSpace(ownerSessionID) == "" {
		return nil, errors.New("committed shared task store owner is unavailable")
	}
	c := s.delegateController
	c.mu.Lock()
	var owner *Session
	if c.rootRuntime != nil && c.rootRuntime.id == ownerSessionID {
		owner = c.rootRuntime
	}
	if owner == nil {
		for _, live := range c.live {
			if live != nil && live.runtime != nil && live.runtime.id == ownerSessionID {
				owner = live.runtime
				break
			}
		}
	}
	c.mu.Unlock()
	if owner == nil {
		return nil, fmt.Errorf("committed shared task store owner %q is not resident", ownerSessionID)
	}
	return owner.getOrCreateTaskStore(), nil
}
