package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sort"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

type coldDelegateAttentionRef struct {
	delegateID    string
	transcriptRef string
}

// rearmDelegateAttentionFromTranscripts reconstructs the process-local wake
// cache from the receiver transcripts without constructing a child Session or
// touching a provider. The transcript fold remains the only durable authority.
func (c *delegateTreeController) rearmDelegateAttentionFromTranscripts() error {
	if c == nil {
		return errors.New("delegate attention controller is nil")
	}
	c.mu.Lock()
	stateDir := c.stateDir
	refs := make([]coldDelegateAttentionRef, 0, len(c.durable))
	for id, aggregate := range c.durable {
		if aggregate == nil || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || aggregate.Phase == delegatestore.PhaseClosed {
			continue
		}
		refs = append(refs, coldDelegateAttentionRef{delegateID: id, transcriptRef: aggregate.Descriptor.TranscriptRef})
	}
	c.mu.Unlock()
	sort.Slice(refs, func(i, j int) bool { return refs[i].delegateID < refs[j].delegateID })

	pending := make(map[string][]string, len(refs))
	for _, ref := range refs {
		path, sessionID, err := delegateTranscriptPathFromRef(stateDir, ref.transcriptRef)
		if err != nil {
			return err
		}
		ids, err := readPendingDelegateAttention(path, sessionID)
		if err != nil {
			return err
		}
		if len(ids) != 0 {
			pending[ref.delegateID] = ids
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.attentionWakeIDs = make(map[string]map[string]struct{}, len(pending))
	for _, ref := range refs {
		aggregate := c.durable[ref.delegateID]
		if aggregate == nil || aggregate.Descriptor.TranscriptRef != ref.transcriptRef || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || aggregate.Phase == delegatestore.PhaseClosed {
			continue
		}
		for _, attentionID := range pending[ref.delegateID] {
			c.noteDelegateAttentionLocked(ref.delegateID, attentionID)
		}
	}
	return nil
}

func (c *delegateTreeController) noteDelegateAttention(delegateID, attentionID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.noteDelegateAttentionLocked(delegateID, attentionID)
}

func (c *delegateTreeController) noteDelegateAttentionLocked(delegateID, attentionID string) bool {
	if delegateID == "" || attentionID == "" {
		return false
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || aggregate.Phase == delegatestore.PhaseClosed {
		return false
	}
	ids := c.attentionWakeIDs[delegateID]
	if ids == nil {
		ids = make(map[string]struct{})
		c.attentionWakeIDs[delegateID] = ids
	}
	if _, exists := ids[attentionID]; exists {
		return false
	}
	ids[attentionID] = struct{}{}
	return true
}

func (c *delegateTreeController) forgetDelegateAttentionLocked(delegateID, attentionID string) {
	ids := c.attentionWakeIDs[delegateID]
	delete(ids, attentionID)
	if len(ids) == 0 {
		delete(c.attentionWakeIDs, delegateID)
	}
	c.forgetColdDelegateAttentionArmLocked(delegateID, attentionID)
}

func (c *delegateTreeController) rememberColdDelegateAttentionArmLocked(delegateID, attentionID string) {
	if delegateID == "" || attentionID == "" {
		return
	}
	ids := c.coldAttentionArmIDs[delegateID]
	if ids == nil {
		ids = make(map[string]struct{})
		c.coldAttentionArmIDs[delegateID] = ids
	}
	ids[attentionID] = struct{}{}
}

func (c *delegateTreeController) forgetColdDelegateAttentionArmLocked(delegateID, attentionID string) {
	ids := c.coldAttentionArmIDs[delegateID]
	delete(ids, attentionID)
	if len(ids) == 0 {
		delete(c.coldAttentionArmIDs, delegateID)
	}
}

func (c *delegateTreeController) hasPendingDelegateAttention() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for delegateID, ids := range c.attentionWakeIDs {
		aggregate := c.durable[delegateID]
		if len(ids) != 0 && aggregate != nil && aggregate.Resumable && aggregate.PendingStopSeq == 0 && aggregate.Phase != delegatestore.PhaseClosed {
			return true
		}
	}
	for delegateID, ids := range c.coldAttentionArmIDs {
		aggregate := c.durable[delegateID]
		if len(ids) != 0 && aggregate != nil && aggregate.Resumable && aggregate.Phase != delegatestore.PhaseClosed {
			return true
		}
	}
	return false
}

// delegateAttentionWakeEligibleLocked reports whether delegateID could accept
// an attention wake or an escalation right now, before the ancestor fence is
// consulted. It is the shared eligibility predicate for ReserveAttention and
// every wake-cache scan, so the driver, the retry loop, and the escalation
// collector cannot disagree about the same delegate.
func (c *delegateTreeController) delegateAttentionWakeEligibleLocked(delegateID string) bool {
	aggregate := c.durable[delegateID]
	if c.closing || aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || c.reclamationCoversLocked(delegateID) {
		return false
	}
	if live := c.live[delegateID]; live != nil && (live.binding != nil || live.recoveryRequired) {
		return false
	}
	for _, record := range c.reservations {
		if record.delegateID == delegateID {
			return false
		}
	}
	return true
}

// hasRunnableDelegateAttention reports whether the root driver has actionable
// attention work: a wake it may commit, or attention it must escalate because
// a permanently closed ancestor fences the wake off forever. Attention parked
// under a transient ancestor stop is pending, not runnable.
func (c *delegateTreeController) hasRunnableDelegateAttention() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for delegateID, ids := range c.attentionWakeIDs {
		if len(ids) == 0 || !c.delegateAttentionWakeEligibleLocked(delegateID) {
			continue
		}
		blocked, closedAncestorID := c.ancestorFenceLocked(c.durable[delegateID].Descriptor.ParentDelegateID)
		if !blocked || closedAncestorID != "" {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) retryDelegateAttentionLater() {
	if c == nil {
		return
	}
	c.mu.Lock()
	root := c.rootRuntime
	c.mu.Unlock()
	if root != nil {
		root.scheduleStableDelegateAttentionRetry()
	}
}

func (c *delegateTreeController) nextIdleDelegateAttention() (string, string, bool) {
	if c == nil {
		return "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delegateIDs := make([]string, 0, len(c.attentionWakeIDs))
	for delegateID, ids := range c.attentionWakeIDs {
		if len(ids) == 0 || !c.delegateAttentionWakeEligibleLocked(delegateID) {
			continue
		}
		if blocked, _ := c.ancestorFenceLocked(c.durable[delegateID].Descriptor.ParentDelegateID); blocked {
			continue
		}
		delegateIDs = append(delegateIDs, delegateID)
	}
	if len(delegateIDs) == 0 {
		return "", "", false
	}
	sort.Strings(delegateIDs)
	delegateID := delegateIDs[0]
	attentionIDs := make([]string, 0, len(c.attentionWakeIDs[delegateID]))
	for attentionID := range c.attentionWakeIDs[delegateID] {
		attentionIDs = append(attentionIDs, attentionID)
	}
	sort.Strings(attentionIDs)
	return delegateID, attentionIDs[0], true
}

// delegateFencedAttentionEscalation is one delegate's pending attention that a
// permanently closed ancestor fences off forever. The root transfers each
// message to itself under the original identity and resolves the source.
type delegateFencedAttentionEscalation struct {
	delegateID    string
	transcriptRef string
	attentionIDs  []string
	runtime       *Session
}

// permanentlyFencedDelegateAttention lists pending attention wakes whose
// target sits under an ancestor that can never become resumable again. No
// generation can ever deliver them, so the root escalates and resolves them
// instead of retrying forever. Delegates that are not wake-eligible (in-flight
// generation, reservation, recovery, reclamation) are skipped and picked up on
// a later pass.
func (c *delegateTreeController) permanentlyFencedDelegateAttention() []delegateFencedAttentionEscalation {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	plans := make([]delegateFencedAttentionEscalation, 0)
	for delegateID, ids := range c.attentionWakeIDs {
		if len(ids) == 0 || !c.delegateAttentionWakeEligibleLocked(delegateID) {
			continue
		}
		aggregate := c.durable[delegateID]
		blocked, closedAncestorID := c.ancestorFenceLocked(aggregate.Descriptor.ParentDelegateID)
		if !blocked || closedAncestorID == "" {
			continue
		}
		attentionIDs := make([]string, 0, len(ids))
		for attentionID := range ids {
			attentionIDs = append(attentionIDs, attentionID)
		}
		sort.Strings(attentionIDs)
		var runtime *Session
		if live := c.live[delegateID]; live != nil {
			runtime = live.runtime
		}
		plans = append(plans, delegateFencedAttentionEscalation{
			delegateID:    delegateID,
			transcriptRef: aggregate.Descriptor.TranscriptRef,
			attentionIDs:  attentionIDs,
			runtime:       runtime,
		})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].delegateID < plans[j].delegateID })
	return plans
}

func (c *delegateTreeController) forgetDelegateAttention(delegateID string, attentionIDs ...string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, attentionID := range attentionIDs {
		c.forgetDelegateAttentionLocked(delegateID, attentionID)
	}
}

// armColdDelegateAttention admits a wake only after re-folding the exact cold
// receiver transcript. Callers invoke it after the matching source
// acknowledgement is durable.
func (c *delegateTreeController) armColdDelegateAttention(delegateID, attentionID string) error {
	armed, err := c.armColdDelegateAttentionOnce(delegateID, attentionID)
	c.mu.Lock()
	root := c.rootRuntime
	if err != nil {
		c.rememberColdDelegateAttentionArmLocked(delegateID, attentionID)
	} else {
		c.forgetColdDelegateAttentionArmLocked(delegateID, attentionID)
	}
	c.mu.Unlock()
	if err != nil && root != nil {
		root.scheduleStableDelegateAttentionRetry()
	} else if armed && root != nil {
		root.notify()
	}
	return err
}

func (c *delegateTreeController) armColdDelegateAttentionOnce(delegateID, attentionID string) (bool, error) {
	if c == nil || delegateID == "" || attentionID == "" {
		return false, errors.New("cold delegate attention identity is incomplete")
	}
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	stateDir := c.stateDir
	transcriptRef := ""
	if aggregate != nil {
		transcriptRef = aggregate.Descriptor.TranscriptRef
	}
	c.mu.Unlock()
	if transcriptRef == "" {
		return false, errDelegateNotControllable
	}
	path, sessionID, err := delegateTranscriptPathFromRef(stateDir, transcriptRef)
	if err != nil {
		return false, err
	}
	ids, err := readPendingDelegateAttention(path, sessionID)
	if err != nil {
		return false, err
	}
	if !slices.Contains(ids, attentionID) {
		return false, nil
	}
	return c.noteDelegateAttention(delegateID, attentionID), nil
}

func (c *delegateTreeController) retryColdDelegateAttentionArms() {
	if c == nil {
		return
	}
	type retryRef struct {
		delegateID  string
		attentionID string
	}
	c.mu.Lock()
	refs := make([]retryRef, 0)
	for delegateID, ids := range c.coldAttentionArmIDs {
		for attentionID := range ids {
			refs = append(refs, retryRef{delegateID: delegateID, attentionID: attentionID})
		}
	}
	c.mu.Unlock()
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].delegateID != refs[j].delegateID {
			return refs[i].delegateID < refs[j].delegateID
		}
		return refs[i].attentionID < refs[j].attentionID
	})
	resolved := make([]retryRef, 0, len(refs))
	for _, ref := range refs {
		_, err := c.armColdDelegateAttentionOnce(ref.delegateID, ref.attentionID)
		if err == nil {
			resolved = append(resolved, ref)
		}
	}
	c.mu.Lock()
	for _, ref := range resolved {
		c.forgetColdDelegateAttentionArmLocked(ref.delegateID, ref.attentionID)
	}
	c.mu.Unlock()
}

func (c *delegateTreeController) idleDelegateRestoreCommit(delegateID string) (delegateStartCommit, string, error) {
	if c == nil || delegateID == "" {
		return delegateStartCommit{}, "", errDelegateNotControllable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if c.closing || aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || c.reclamationCoversLocked(delegateID) {
		return delegateStartCommit{}, "", errDelegateTargetBusy
	}
	descriptor := cloneDelegateStartDescriptor(aggregate.Descriptor)
	worktreePath := ""
	if descriptor.Isolation == "worktree" {
		worktreePath = descriptor.WorkingDir
	}
	return delegateStartCommit{
		lease:          delegateLease{delegateID: delegateID, generation: aggregate.Generation},
		ctx:            context.Background(),
		descriptor:     descriptor,
		transcriptPath: filepath.Join(c.stateDir, sessionsSubdir, descriptor.ChildSessionID+".transcript.jsonl"),
		worktreePath:   worktreePath,
	}, descriptor.ParentDelegateID, nil
}

func (c *delegateTreeController) residentDelegateRuntime(delegateID string) *Session {
	if c == nil || delegateID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	live := c.live[delegateID]
	if aggregate == nil || aggregate.Phase == delegatestore.PhaseClosed || aggregate.PendingStopSeq != 0 || live == nil {
		return nil
	}
	return live.runtime
}

// AttachIdleRuntime installs only the exact lazily restored runtime identity.
// It creates no generation and writes no durable controller event.
func (c *delegateTreeController) AttachIdleRuntime(delegateID string, runtime *Session) error {
	if c == nil || runtime == nil || delegateID == "" {
		return errDelegateTargetBusy
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if c.closing || aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || runtime.delegateController != c || runtime.owningDelegateID != delegateID || runtime.ID() != aggregate.Descriptor.ChildSessionID {
		return errDelegateTargetBusy
	}
	live := c.live[delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[delegateID] = live
	}
	if live.binding != nil || live.recoveryRequired || live.runtime != nil && live.runtime != runtime {
		return errDelegateTargetBusy
	}
	ownerID, owner, err := c.runtimeOwnerLocked(runtime)
	if err != nil || owner != nil && owner != live || ownerID != "" && ownerID != delegateID {
		return errDelegateTargetBusy
	}
	live.runtime = runtime
	c.evidenceVersion++
	return nil
}
