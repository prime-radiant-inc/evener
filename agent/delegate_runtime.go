package agent

import (
	"fmt"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

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
	s.delegateController = controller
	s.delegateRootSessionID = s.id
	s.ownsDelegateController = true
	return nil
}

func (s *Session) closeOwnedDelegateStore() error {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil || s.delegateController.store == nil {
		return nil
	}
	return s.delegateController.store.Close()
}
