package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"primeradiant.com/serf/agent/internal/delegatestore"
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
		aggregate := c.durable[delegateID]
		if len(ids) != 0 && aggregate != nil && aggregate.Phase == delegatestore.PhaseIdle && aggregate.Resumable && aggregate.PendingStopSeq == 0 {
			delegateIDs = append(delegateIDs, delegateID)
		}
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

// armColdDelegateAttention admits a wake only after re-folding the exact cold
// receiver transcript. Callers invoke it after the matching source
// acknowledgement is durable.
func (c *delegateTreeController) armColdDelegateAttention(delegateID, attentionID string) error {
	if c == nil || delegateID == "" || attentionID == "" {
		return errors.New("cold delegate attention identity is incomplete")
	}
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	stateDir := c.stateDir
	transcriptRef := ""
	if aggregate != nil {
		transcriptRef = aggregate.Descriptor.TranscriptRef
	}
	root := c.rootRuntime
	c.mu.Unlock()
	if transcriptRef == "" {
		return errDelegateNotControllable
	}
	path, sessionID, err := delegateTranscriptPathFromRef(stateDir, transcriptRef)
	if err != nil {
		return err
	}
	ids, err := readPendingDelegateAttention(path, sessionID)
	if err != nil {
		return err
	}
	found := false
	for _, id := range ids {
		if id == attentionID {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if c.noteDelegateAttention(delegateID, attentionID) && root != nil {
		root.notify()
	}
	return nil
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
