package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// OutstandingDelegateWork is the itemized set of obligations that can wake or
// resume this session without anyone acting on it. It is a snapshot, not a
// claim: an empty inventory means no known obligation exists right now, and
// Unknown records a store that could not be read so callers never treat a read
// failure as absence of work.
type OutstandingDelegateWork struct {
	Jobs            []string
	Watches         []string
	Notifications   []string
	Inputs          []string
	ChildRuns       []string
	ChildDeliveries []string
	Attention       []string
	Goals           []string
	Unknown         []string
}

// Empty reports whether the snapshot found no outstanding work and no
// unreadable store. Unknown counts as work: a failure to read evidence is not
// evidence that there is nothing to do.
func (w OutstandingDelegateWork) Empty() bool {
	return len(w.Jobs) == 0 &&
		len(w.Watches) == 0 &&
		len(w.Notifications) == 0 &&
		len(w.Inputs) == 0 &&
		len(w.ChildRuns) == 0 &&
		len(w.ChildDeliveries) == 0 &&
		len(w.Attention) == 0 &&
		len(w.Goals) == 0 &&
		len(w.Unknown) == 0
}

// IDs returns a stable, deduplicated list of "<category>:<id>" strings. It is
// the form steering and tests use; the original slices stay separate so a
// caller can name the category without parsing.
func (w OutstandingDelegateWork) IDs() []string {
	var ids []string
	add := func(category string, values []string) {
		for _, id := range values {
			if id == "" {
				continue
			}
			ids = append(ids, category+":"+id)
		}
	}
	add("job", w.Jobs)
	add("watch", w.Watches)
	add("notification", w.Notifications)
	add("input", w.Inputs)
	add("child-run", w.ChildRuns)
	add("child-delivery", w.ChildDeliveries)
	add("attention", w.Attention)
	add("goal", w.Goals)
	add("unknown", w.Unknown)
	sort.Strings(ids)
	return slices.Compact(ids)
}

// outstandingDelegateWork itemizes every local obligation that can wake this
// session. Each unreadable store becomes an Unknown entry and contributes to
// the returned error; the snapshot is still returned so a caller can report
// what it did see.
func (s *Session) outstandingDelegateWork() (OutstandingDelegateWork, error) {
	return s.outstandingDelegateWorkInventory(true)
}

// outstandingDelegateWorkForAdmission omits the attention fold. Finalization
// resolves attention under its own claim, and reading the fold here would make
// admission race that resolution.
func (s *Session) outstandingDelegateWorkForAdmission() (OutstandingDelegateWork, error) {
	return s.outstandingDelegateWorkInventory(false)
}

func (s *Session) outstandingDelegateWorkInventory(includeAttention bool) (OutstandingDelegateWork, error) {
	var work OutstandingDelegateWork
	if s == nil {
		return work, nil
	}
	var errs []error

	if s.jobManager != nil {
		jobs, watches, notifications, unknown, err := s.jobManager.outstandingWork()
		work.Jobs = jobs
		work.Watches = watches
		work.Notifications = append(work.Notifications, notifications...)
		work.Unknown = append(work.Unknown, unknown...)
		if err != nil {
			errs = append(errs, err)
		}
	}

	work.Notifications = append(work.Notifications, s.pendingJobNotificationIDs()...)
	work.Inputs = s.pendingInputIDs()
	work.ChildRuns = s.pendingChildRunIDs()
	work.ChildDeliveries = s.pendingChildDeliveryIDs()

	if includeAttention {
		attention, err := s.pendingAttentionIDs()
		work.Attention = s.dropOwnedShellAttention(attention)
		if err != nil {
			work.Unknown = append(work.Unknown, "attention-fold")
			errs = append(errs, err)
		}
	}

	work.Goals = s.activeGoalIDs()

	return work, errors.Join(errs...)
}

// dropOwnedShellAttention removes attention that names a stable delegate's
// owned shell: the run's finalization resolves that attention, so it is not a
// waker that can leave the run idle. A shell the run does not own stays.
func (s *Session) dropOwnedShellAttention(ids []string) []string {
	if s == nil || s.jobManager == nil || len(ids) == 0 {
		return ids
	}
	s.jobManager.mu.Lock()
	defer s.jobManager.mu.Unlock()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, "shell:") {
			rest := strings.TrimPrefix(id, "shell:")
			jobID, _, _ := strings.Cut(rest, ":")
			if job := s.jobManager.running[jobID]; job != nil && job.delegateShell != nil {
				continue
			}
		}
		out = append(out, id)
	}
	return out
}

// outstandingWork itemizes the job manager's own obligations. A missing or
// malformed journal is an Unknown entry, not an empty notification list.
func (jm *jobManager) outstandingWork() (jobs, watches, notifications, unknown []string, err error) {
	if jm == nil {
		return nil, nil, nil, nil, nil
	}

	jm.mu.Lock()
	for id, job := range jm.running {
		// A stable delegate's owned shell is settled by the run's own
		// finalization drain, so it is not a waker that can leave the run idle.
		if job != nil && job.delegateShell != nil {
			continue
		}
		jobs = append(jobs, id)
	}
	for key, cfg := range jm.watches {
		id := watchConfigID(cfg)
		if id == "" {
			id = key.Slot
		}
		watches = append(watches, id)
	}
	for cfg, flushing := range jm.terminalFlush {
		if !flushing || len(cfg.pending) == 0 {
			continue
		}
		for _, state := range cfg.pending {
			id := ""
			if state != nil {
				id = state.DeliveryID
			}
			if id == "" {
				id = watchConfigID(cfg)
			}
			notifications = append(notifications, id)
		}
	}
	for id := range jm.stableWatchReceipts {
		notifications = append(notifications, id)
	}
	for id := range jm.stableWatchSettlementRetries {
		notifications = append(notifications, id)
	}
	if jm.stableWatchSettlementRetrying {
		notifications = append(notifications, "settlement-retry")
	}
	jm.mu.Unlock()

	journal, readErr := retirementJobJournal(filepath.Join(jm.dir, "jobs.jsonl"))
	if readErr != nil {
		return jobs, watches, notifications, []string{"job-journal"}, readErr
	}
	for _, record := range jobstore.Fold(journal) {
		if record.OwnerSessionID == jm.sessionID && record.NotifyState == jobstore.NotifyPending {
			notifications = append(notifications, record.JobID)
		}
	}
	return jobs, watches, notifications, nil, nil
}

func watchConfigID(cfg *watchConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.watchID != "" {
		return cfg.watchID
	}
	return cfg.id
}

func (s *Session) pendingJobNotificationIDs() []string {
	if s == nil {
		return nil
	}
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	ids := make([]string, 0, len(s.pendingJobNotifs))
	for _, notification := range s.pendingJobNotifs {
		ids = append(ids, notification.JobID)
	}
	return ids
}

func (s *Session) pendingInputIDs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, input := range s.inputQueue {
		ids = append(ids, input.ID)
	}
	for _, steering := range s.steeringQueue {
		id := steering.StableTurnID
		if id == "" {
			id = steering.ClientMutationID
		}
		ids = append(ids, id)
	}
	for i := range s.followups {
		ids = append(ids, fmt.Sprintf("followup-%d", i))
	}
	return ids
}

func (s *Session) pendingChildRunIDs() []string {
	if s == nil || s.delegateController == nil {
		return nil
	}
	return s.delegateController.pendingChildRunIDs(s.owningDelegateID)
}

func (c *delegateTreeController) pendingChildRunIDs(owner string) []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for id := range c.delegateSubtreeIDsLocked(owner) {
		aggregate := c.durable[id]
		if aggregate == nil {
			continue
		}
		if aggregate.CurrentRunOpen ||
			aggregate.Phase == delegatestore.PhaseRunning ||
			aggregate.Phase == delegatestore.PhaseSettling ||
			aggregate.Phase == delegatestore.PhaseStopping {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Session) pendingChildDeliveryIDs() []string {
	if s == nil {
		return nil
	}
	var ids []string
	s.delegateDeliveryMu.Lock()
	for _, plan := range s.pendingDelegateDeliveries {
		ids = append(ids, plan.deliveryID)
	}
	s.delegateDeliveryMu.Unlock()
	if s.delegateController != nil {
		s.delegateController.mu.Lock()
		for id := range s.delegateController.delegateSubtreeIDsLocked(s.owningDelegateID) {
			aggregate := s.delegateController.durable[id]
			if aggregate == nil {
				continue
			}
			for _, delivery := range aggregate.PendingDeliveries {
				ids = append(ids, delivery.DeliveryID)
			}
		}
		s.delegateController.mu.Unlock()
	}
	return ids
}

// delegateSubtreeIDsLocked is every delegate under owner (all root-level
// delegates when owner is empty), excluding owner itself. The caller holds
// c.mu.
func (c *delegateTreeController) delegateSubtreeIDsLocked(owner string) map[string]struct{} {
	ids := map[string]struct{}{}
	if owner == "" {
		for id, aggregate := range c.durable {
			if aggregate != nil && aggregate.Descriptor.ParentDelegateID == "" {
				ids[id] = struct{}{}
			}
		}
		return ids
	}
	var walk func(string)
	walk = func(parent string) {
		for child := range c.delegateChildren[parent] {
			if _, seen := ids[child]; seen {
				continue
			}
			ids[child] = struct{}{}
			walk(child)
		}
	}
	walk(owner)
	return ids
}

func (s *Session) pendingAttentionIDs() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	ids, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		return nil, err
	}
	parked := s.rootAttentionRailParked()
	s.attentionMu.Lock()
	deferred := s.rootAttentionPaused && !s.rootAttentionWake && !s.rootAttentionRetry.active
	if len(s.rootAttentionWakeIDs) != 0 && !parked && !deferred {
		for id := range s.rootAttentionWakeIDs {
			ids = append(ids, id)
		}
	}
	for id := range s.delegateAttentionArmIDs {
		ids = append(ids, id)
	}
	s.attentionMu.Unlock()
	return ids, nil
}

func (s *Session) activeGoalIDs() []string {
	if s == nil {
		return nil
	}
	state, ok := s.getOrCreateGoalStore().Snapshot()
	if !ok || state.Status != goal.StatusActive {
		return nil
	}
	id := state.Objective
	if id == "" {
		id = "active"
	}
	return []string{id}
}
