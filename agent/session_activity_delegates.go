package agent

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

func insertSessionActivityKey(keys []sessionActivityKey, key sessionActivityKey) []sessionActivityKey {
	at := sort.Search(len(keys), func(i int) bool { return !keys[i].before(key) })
	if at < len(keys) && keys[at] == key {
		return keys
	}
	keys = append(keys, sessionActivityKey{})
	copy(keys[at+1:], keys[at:])
	keys[at] = key
	return keys
}
func deriveSessionActivityDelegateKeys(state delegatestore.State) []sessionActivityKey {
	keys := make([]sessionActivityKey, 0, len(state))
	for id, row := range state {
		if row != nil {
			keys = append(keys, sessionActivityCreationKey(row.CreatedAt, id))
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].before(keys[j]) })
	return keys
}

// ListActivityDelegates pages stable logical delegate owners within this controller.
func (s *Session) ListActivityDelegates(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	_, scope, limit, err := normalizeSessionActivity(params.Ref, params.Scope, params.Limit)
	if err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	params.Scope = scope
	params.Limit = limit
	read, err := s.activityRead(ctx, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
	if err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	defer read.index.release()
	return read.delegatesPage(ctx, params)
}

// LoadSessionActivityDelegates advances bounded retained delegate reconstruction.
func LoadSessionActivityDelegates(ctx context.Context, stateDir, sessionID string, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	_, scope, limit, err := normalizeSessionActivity(params.Ref, params.Scope, params.Limit)
	if err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	params.Scope = scope
	params.Limit = limit
	read, err := retainedActivityRead(ctx, stateDir, sessionID, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
	if err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	defer read.index.release()
	return read.delegatesPage(ctx, params)
}
func (read *sessionActivityRead) delegatesPage(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	result := appwire.SessionDelegatesResponse{Context: read.context, Scope: read.scope}
	token, walk, err := read.index.token(params, appwire.SessionActivityResourceDelegates, read.context.SessionID)
	if err != nil {
		return result, err
	}
	controller := read.index.controller
	if controller == nil {
		if _, ok := walk.Cutoffs[read.rootID]; !ok {
			info, statErr := os.Stat(read.delegatePath())
			if statErr != nil && !os.IsNotExist(statErr) {
				return result, sessionActivitySourceReadError("delegate source unavailable", statErr)
			}
			if info != nil {
				walk.Cutoffs[read.rootID] = info.Size()
			} else {
				walk.Cutoffs[read.rootID] = 0
			}
		}
		complete, scanErr := read.advanceDelegates(ctx)
		if scanErr != nil {
			return result, scanErr
		}
		if !complete || !read.context.AncestryKnown {
			result.Page.NextCursor = read.index.encode(token)
			return result, nil
		}
	} else {
		controller.mu.Lock()
		defer controller.mu.Unlock()
	}
	keys := read.index.delegateKeys
	if controller != nil {
		keys = controller.activityKeys
	}
	if !walk.Ready {
		if controller != nil {
			walk.Admission = controller.activityAdmission
		}
		if len(keys) > 0 {
			walk.Highwater = keys[len(keys)-1]
		}
		walk.Ready = true
	}
	owners := read.owners()
	state := read.state()
	start := len(keys) - 1
	if token.After.ID != "" {
		start = sort.Search(len(keys), func(i int) bool { return !keys[i].before(token.After) }) - 1
	}
	pageBudget := newSessionActivityPageBudget(result)
	for ; start >= 0 && read.budget > 0; start-- {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		key := keys[start]
		read.budget--
		if walk.Highwater.before(key) {
			continue
		}
		row := state[key.ID]
		if row == nil || !owners[sessionActivityDelegateOwner(state, row)] ||
			(controller != nil && controller.activityAdmissions[key.ID] > walk.Admission) ||
			(controller == nil && read.index.delegateOffsets[key.ID] > walk.Cutoffs[read.rootID]) {
			token.After = key
			continue
		}
		projected := projectSessionActivityDelegate(read.rootID, state, row, time.Now().UTC())
		if controller != nil {
			if live := controller.live[key.ID]; live != nil && live.activityAt.After(row.LatestActivityAt) {
				projected.LatestActivityAt = live.activityAt.UTC().Format(time.RFC3339Nano)
			}
		}
		result.Delegates = append(result.Delegates, projected)
		// Reserve room for the opaque continuation; never consume an excluded row.
		if !pageBudget.fits(projected, result) {
			result.Delegates = result.Delegates[:len(result.Delegates)-1]
			break
		}
		token.After = key
		if len(result.Delegates) >= params.Limit {
			start--
			break
		}
	}
	result.Page.Complete = start < 0
	if !result.Page.Complete {
		if len(result.Delegates) == 0 && read.budget > 0 {
			return result, appwire.Unavailable("session activity context exceeds response budget")
		}
		result.Page.NextCursor = read.index.encode(token)
	}
	return result, nil
}

// The compact projection reads only already-owned fields, without cloning the
// immutable frozen prompt or traversing child transcripts and worktrees.
func projectSessionActivityDelegate(rootID string, state delegatestore.State, aggregate *delegatestore.Aggregate, now time.Time) appwire.SessionDelegate {
	snapshot := delegateSnapshot{id: aggregate.DelegateID, parentID: aggregate.Descriptor.ParentDelegateID, descriptor: aggregate.Descriptor, generation: aggregate.Generation, phase: aggregate.Phase, currentRunOpen: aggregate.CurrentRunOpen, runStartedAt: aggregate.RunStartedAt, resumable: aggregate.Resumable, needsAttention: aggregate.NeedsAttention, notResumableReason: aggregate.NotResumableReason, latestActivityAt: aggregate.LatestActivityAt, lastOutcome: aggregate.LatestOutcome}
	snapshot.lifecycle = delegateLifecycleRunning
	if aggregate.Phase == delegatestore.PhaseIdle || aggregate.Phase == delegatestore.PhaseClosed {
		snapshot.lifecycle = delegateLifecycleIdle
	}
	status := projectStableDelegateStatus(now, snapshot)
	d := aggregate.Descriptor
	row := appwire.SessionDelegate{DelegateID: aggregate.DelegateID, RunGeneration: aggregate.Generation, OwnerRef: encodeRef("", sessionActivityDelegateOwner(state, aggregate)), RootRef: encodeRef("", rootID), ChildRef: encodeRef("", d.ChildSessionID), ParentDelegateID: d.ParentDelegateID, Description: truncateActivityText(d.Description, activityMaxDelegateProseRunes), Task: truncateActivityText(d.Task, activityMaxDelegateProseRunes), Type: "delegate", Lifecycle: string(snapshot.lifecycle), Phase: string(aggregate.Phase), Status: string(snapshot.lifecycle), Resumable: aggregate.Resumable, NotResumableReason: truncateActivityText(aggregate.NotResumableReason, activityMaxDelegateProseRunes), Model: truncateActivityText(d.ResolvedModel, activityMaxLabelRunes), ReasoningEffort: d.Config.ReasoningEffort, RunStartedAt: status.RunStartedAt, LatestActivityAt: status.LatestActivityAt}
	if outcome := aggregate.LatestOutcome; outcome != nil {
		row.Reason = truncateActivityText(outcome.Reason, activityMaxDelegateProseRunes)
		row.Error = truncateActivityText(outcome.Error, activityMaxDelegateProseRunes)
		row.Outcome = string(outcome.Status)
		row.Terminal = delegateRunTerminal(outcome, aggregate.CurrentRunOpen)
		if !outcome.EndedAt.IsZero() {
			row.RunEndedAt = outcome.EndedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	// RunFinished replaces the packet for the exact open generation and closes
	// that run atomically. A resumed or settling run still owns the old packet.
	if packet := aggregate.LatestPacket; !aggregate.CurrentRunOpen && aggregate.LatestOutcome != nil && packet != nil && packet.Kind == delegatestore.PacketReported {
		var report string
		if json.Unmarshal(packet.Message, &report) == nil {
			row.ReportPreview = truncateActivityText(report, activityMaxDelegateProseRunes)
			row.ReportPreviewTruncated = row.ReportPreview != report
		}
	}
	if packet := aggregate.LatestPacket; packet != nil && len(packet.Metadata) <= activityMaxDelegatePayloadBytes {
		var metadata delegateTerminalPacketMetadata
		if json.Unmarshal(packet.Metadata, &metadata) == nil {
			row.Usage = activityUsageFromCumulative(metadata.CumulativeUsage)
			if wt := metadata.Worktree; wt != nil {
				row.Worktree = &appwire.JobActivityWorktree{Path: truncateActivityText(wt.Path, activityMaxDelegateProseRunes), Branch: truncateActivityText(wt.Branch, activityMaxLabelRunes), HeadSHA: truncateActivityText(wt.HeadSHA, activityMaxLabelRunes), Ahead: wt.Ahead, Dirty: wt.Dirty}
			}
		}
	}
	return row
}
