package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

func (s *Session) bootstrapDelegateResources() error {
	if inherited := s.cfg.spawn.delegateController; inherited != nil {
		s.delegateController = inherited
		s.delegateRootSessionID = s.cfg.spawn.delegateRootSessionID
		s.owningDelegateID = s.cfg.spawn.owningDelegateID
		return nil
	}
	if warning, ok := legacyDelegateStateWarning(s.stateDir, s.id); ok {
		s.pendingHookWarnings = append(s.pendingHookWarnings, warning)
	}
	path := filepath.Join(jobsDir(s.stateDir, s.id), "delegates.jsonl")
	store, err := delegatestore.Open(path)
	if err != nil {
		return fmt.Errorf("open delegate store: %w", err)
	}
	controller, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:               store,
		rootRuntime:         s,
		rootSessionID:       s.id,
		stateDir:            s.stateDir,
		worktreeRoot:        filepath.Join(jobsDir(s.stateDir, s.id), "worktrees"),
		turnLimit:           s.cfg.MaxConcurrentDelegateTurns,
		driveLimit:          defaultMaxConcurrentDriveTurns,
		maxRetainedTerminal: s.cfg.MaxRetainedTerminal,
		now:                 s.sclock().Now,
		attentionOpen:       s.cfg.testOnly.delegateAttentionOpenWriter,
	})
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open delegate controller: %w", err)
	}
	if err := repairStableWatchDeliveriesForBootstrap(controller); err != nil {
		_ = store.Close()
		return fmt.Errorf("repair stable watch deliveries: %w", err)
	}
	reconcileDeliveries, err := reconcileDelegateResourcesForBootstrap(controller)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("reconcile delegate resources: %w", err)
	}
	if err := repairStableShellAttentionForBootstrap(controller); err != nil {
		_ = store.Close()
		return fmt.Errorf("repair stable shell attention: %w", err)
	}
	missingInputs, err := missingDelegateRestoreInputs(s.stateDir, controller, s.delegateRestoreStat, s.delegateRestoreReadFile)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("inspect delegate restore inputs: %w", err)
	}
	missingPlans, err := controller.closeMissingRestoreInputs(missingInputs)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("close delegates with missing restore inputs: %w", err)
	}
	if err := repairPermanentlyUnreachableDelegateAttention(controller); err != nil {
		_ = store.Close()
		return fmt.Errorf("repair unreachable delegate attention: %w", err)
	}
	deliveryPlans := append([]delegateDeliveryPlan(nil), reconcileDeliveries...)
	deliveryPlans = append(deliveryPlans, missingPlans.deliveries...)
	deliveryPlans = append(deliveryPlans, controller.ReplayDeliveries()...)
	pendingDeliveries, err := prepareColdDelegateDeliveryReplay(controller, deliveryPlans)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("replay committed delegate deliveries: %w", err)
	}
	s.delegateController = controller
	s.delegateRootSessionID = s.id
	s.ownsDelegateController = true
	controller.emitUpdate = s.emitStableDelegateUpdate
	s.pendingDelegateDeliveries = append(s.pendingDelegateDeliveries, pendingDeliveries...)
	return nil
}

func (s *Session) emitStableDelegateUpdate(plan delegateUpdatePlan) {
	if s == nil || s.delegateController == nil {
		return
	}
	now := s.sclock().Now().UTC()
	rootID := s.delegateController.rootSessionID
	for _, row := range plan.rows {
		ownerID := row.descriptor.OwnerSessionID
		s.delegateController.mu.Lock()
		logicalOwner := sessionActivityDelegateOwner(s.delegateController.durable, s.delegateController.durable[row.id])
		var ancestry []string
		if logicalOwner != "" {
			ancestry = s.delegateController.sessionAncestryLocked(logicalOwner)
		}
		ancestors, ancestorRuntimes := s.delegateController.ancestorSubagentRuntimesLocked(ancestry)
		ownerRuntime := s.delegateController.runtimeForDelegateOwnerLocked(row)
		s.delegateController.mu.Unlock()
		s.publishSessionActivityChanged(logicalOwner, ancestry, appwire.SessionActivityResourceDelegates)
		data := delegateUpdatedDataFromStatus(delegateFrameStatusInfo(now, rootID, row))
		data.AncestorSessionIDs = ancestors
		// A subagent's own thread lists its subtree, so each live ancestor
		// subagent's stream carries the update too. A released ancestor gets
		// none: forwarding for it would reopen its closed thread's history, and
		// its next thread read lists the row afresh. The owner is the root for
		// every current row, so the guard only matters for a row a parent
		// session owns, whose owner stream already carries it.
		for _, runtime := range ancestorRuntimes {
			if runtime != ownerRuntime {
				runtime.emitWithProvenance(data, row.descriptor.Provenance)
			}
		}
		if ownerRuntime != nil {
			ownerRuntime.emitWithProvenance(data, row.descriptor.Provenance)
			continue
		}
		s.mu.Lock()
		forward := s.cfg.spawn.descendantEvent
		s.mu.Unlock()
		if ownerID == "" || forward == nil {
			continue
		}
		event := events.New(data)
		event.Timestamp = now
		event.SessionID = ownerID
		event.Provenance = provenance.Clone(row.descriptor.Provenance)
		forward(event)
	}
}

// ancestorSubagentRuntimesLocked lists the subagent sessions of ancestry (a
// sessionAncestryLocked chain), nearest first and without the root, and the
// live runtime of each one that has one.
func (c *delegateTreeController) ancestorSubagentRuntimesLocked(ancestry []string) ([]string, []*Session) {
	var ancestors []string
	var runtimes []*Session
	for _, sessionID := range ancestry {
		if sessionID == c.rootSessionID {
			break
		}
		ancestors = append(ancestors, sessionID)
		if live := c.live[c.delegateOwnerOfSessionLocked(sessionID)]; live != nil && live.runtime != nil && live.runtime.ID() == sessionID {
			runtimes = append(runtimes, live.runtime)
		}
	}
	return ancestors, runtimes
}

func (c *delegateTreeController) runtimeForDelegateOwnerLocked(row delegateSnapshot) *Session {
	if c == nil {
		return nil
	}
	if row.descriptor.OwnerSessionID == c.rootSessionID {
		return c.rootRuntime
	}
	if live := c.live[row.parentID]; live != nil && live.runtime != nil && live.runtime.ID() == row.descriptor.OwnerSessionID {
		return live.runtime
	}
	return nil
}

func reconcileDelegateResourcesForBootstrap(controller *delegateTreeController) ([]delegateDeliveryPlan, error) {
	var deliveries []delegateDeliveryPlan
	// Each Reconcile hands back a retirement-mutation lease, and bootstrap
	// deliberately holds every one until this function returns. Collect them
	// and release all on return — LIFO, as the per-iteration defers did —
	// rather than deferring inside the loop.
	var retirementReleases []func()
	defer func() {
		for _, retirementRelease := range slices.Backward(retirementReleases) {
			retirementRelease()
		}
	}()
	for {
		evidence, err := collectDelegateReconcileEvidence(controller.stateDir, controller.ReconcileRequirements())
		if err != nil {
			return nil, fmt.Errorf("collect evidence: %w", err)
		}
		plans, err := controller.Reconcile(evidence)
		if plans.retirementRelease != nil {
			retirementReleases = append(retirementReleases, plans.retirementRelease)
		}
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, plans.deliveries...)
		for _, plan := range plans.shellRepairs {
			if err := executeDelegateShellRepair(plan, controller.now()); err != nil {
				return nil, err
			}
		}
		for _, plan := range plans.attention {
			if err := controller.executeDelegateAttentionCleanup(plan); err != nil {
				return nil, err
			}
		}

		controller.mu.Lock()
		stopPending := controller.stop != nil
		controller.mu.Unlock()
		if !stopPending {
			return deliveries, nil
		}
		if len(plans.updates) == 0 && len(plans.deliveries) == 0 && len(plans.shellRepairs) == 0 && len(plans.attention) == 0 {
			return nil, errors.New("restored delegate stop made no reconciliation progress")
		}
	}
}

func missingDelegateRestoreInputs(
	stateDir string,
	controller *delegateTreeController,
	stat func(string) (os.FileInfo, error),
	readFile func(string) ([]byte, error),
) (map[string]string, error) {
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
		reason, err := missingDelegateRestoreInputReason(stateDir, descriptors[id], stat, readFile)
		if err != nil {
			return nil, fmt.Errorf("delegate %s: %w", id, err)
		}
		if reason != "" {
			reasons[id] = reason
		}
	}
	return reasons, nil
}

func missingDelegateRestoreInputReason(
	stateDir string,
	descriptor delegatestore.Descriptor,
	stat func(string) (os.FileInfo, error),
	readFile func(string) ([]byte, error),
) (string, error) {
	childID := strings.TrimSpace(descriptor.ChildSessionID)
	if childID == "" || strings.TrimSpace(descriptor.Task) == "" || strings.TrimSpace(descriptor.AgentType) == "" || strings.TrimSpace(descriptor.ResolvedProfileID) == "" || strings.TrimSpace(descriptor.ResolvedModel) == "" {
		return notResumableMissingDelegateResumeMetadata, nil
	}
	_, transcriptChildID, err := decodeRef(descriptor.TranscriptRef)
	if err != nil || transcriptChildID != childID {
		return notResumableParentLinkageUnavailable, nil //nolint:nilerr // malformed durable linkage is a classified non-resumable state, not an operational failure
	}
	if strings.TrimSpace(stateDir) == "" {
		return notResumableMissingChildSessionMeta, nil
	}
	if workingDir := strings.TrimSpace(descriptor.WorkingDir); workingDir != "" {
		if _, err := stat(workingDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return notResumableWorkingDirMissing, nil
			}
			return "", fmt.Errorf("stat working directory %s: %w", workingDir, err)
		}
	}
	metaPath := schema.SessionMetaPath(stateDir, childID)
	metaBytes, err := readFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notResumableMissingChildSessionMeta, nil
		}
		return "", fmt.Errorf("read child session metadata %s: %w", childID, err)
	}
	var meta schema.SessionMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return notResumableCorruptChildSessionMeta, nil //nolint:nilerr // malformed durable metadata is a classified non-resumable state, not an operational failure
	}
	if strings.TrimSpace(meta.ID) != childID {
		return notResumableCorruptChildSessionMeta, nil
	}
	path := schema.SessionTranscriptPath(stateDir, childID)
	if _, err := stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notResumableMissingChildTranscript, nil
		}
		return "", fmt.Errorf("stat child transcript %s: %w", childID, err)
	}
	if _, err := validateStrictChildTranscript(path, stateDir, childID, 0); err != nil {
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

func (s *Session) delegateRestoreStat(path string) (os.FileInfo, error) {
	if stat := s.cfg.testOnly.delegateRestoreStat; stat != nil {
		return stat(path)
	}
	return os.Stat(path)
}

func (s *Session) delegateRestoreReadFile(path string) ([]byte, error) {
	if readFile := s.cfg.testOnly.delegateRestoreReadFile; readFile != nil {
		return readFile(path)
	}
	return os.ReadFile(path)
}

func delegateRestoreOperationalIOError(err error) bool {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return false
	}
	if _, ok := errors.AsType[*os.PathError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*os.SyscallError](err); ok {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno)
}

func (s *Session) closeOwnedDelegateStore() error {
	return s.closeOwnedDelegateStoreWithContext(context.Background())
}

func (s *Session) closeOwnedDelegateStoreWithContext(ctx context.Context) error {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil || s.delegateController.store == nil {
		return nil
	}
	return s.delegateController.closeStoreAfterStopReconcileDriver(ctx)
}

func (s *Session) closeOwnedDelegateRuntimeTree(ctx context.Context) error {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil {
		return nil
	}
	return s.delegateController.closeRuntimeTree(ctx)
}
