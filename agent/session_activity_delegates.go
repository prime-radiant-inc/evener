package agent

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"time"

	"primeradiant.com/evener/agent/activitybound"
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
	return withActivityList(ctx, params, s.activityRead, (*sessionActivityRead).delegatesPage)
}

// LoadSessionActivityDelegates advances bounded retained delegate reconstruction.
func LoadSessionActivityDelegates(ctx context.Context, stateDir, sessionID string, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	return withActivityList(ctx, params, func(ctx context.Context, read appwire.SessionActivityReadParams) (*sessionActivityRead, error) {
		return retainedActivityRead(ctx, stateDir, sessionID, read)
	}, (*sessionActivityRead).delegatesPage)
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
	}
	candidates, complete, err := read.captureDelegateCandidates(ctx, params, token, walk)
	if err != nil {
		return result, err
	}
	pageBudget := newSessionActivityPageBudget(result)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if candidate.included {
			projected := candidate.project()
			result.Delegates = append(result.Delegates, projected)
			// Reserve room for the opaque continuation; never consume an excluded row.
			if !pageBudget.fits(projected, result) {
				result.Delegates = result.Delegates[:len(result.Delegates)-1]
				complete = false
				// Later captured rows must not change the excluded row's budget result.
				read.budget = candidate.remainingBudget
				break
			}
		}
		token.After = candidate.key
	}
	result.Page.Complete = complete
	if !complete {
		if len(result.Delegates) == 0 && read.budget > 0 {
			return result, appwire.Unavailable("session activity context exceeds response budget")
		}
		result.Page.NextCursor = read.index.encode(token)
	}
	return result, nil
}

// activityMaxReportPreviewBytes is the raw-byte window agent/activitybound
// bounds a reported packet's preview with.
const activityMaxReportPreviewBytes = activitybound.MaxReportPreviewBytes

// Stop capture after reaching this owned-report allowance, with at most one
// already-bounded candidate overshoot. Other candidate facts have separate bounds.
const activityReportCaptureBudgetBytes = sessionActivityPageBytes

type sessionActivityDelegateCandidate struct {
	key             sessionActivityKey
	included        bool
	remainingBudget int
	row             appwire.SessionDelegate
	report          json.RawMessage
	reportComplete  bool
	// metadata is the terminal packet's raw metadata, captured with the row and
	// projected by project() — after the controller mutex is released. It is here
	// as bytes rather than as decoded fields on purpose: the document can carry a
	// task brief and a tool-name list of hundreds of entries, and decoding it
	// inside the capture loop held the delegate controller mutex across both a
	// copy and a 16 KiB measurement for every row on the page.
	metadata json.RawMessage
}

// Capture bounded row facts and the immutable settled packet together. Report
// decoding and response byte admission happen after releasing the controller.
func (read *sessionActivityRead) captureDelegateCandidates(ctx context.Context, params appwire.SessionActivityListParams, token sessionActivityToken, walk *sessionActivityWalk) ([]sessionActivityDelegateCandidate, bool, error) {
	controller := read.index.controller
	if controller != nil {
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
	candidates := make([]sessionActivityDelegateCandidate, 0, params.Limit)
	matched := 0
	capturedReportBytes := 0
	for ; start >= 0 && read.budget > 0; start-- {
		if err := ctx.Err(); err != nil {
			return nil, false, err
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
			candidates = append(candidates, sessionActivityDelegateCandidate{key: key})
			continue
		}
		candidate := captureSessionActivityDelegate(read.rootID, state, row, time.Now().UTC())
		candidate.key = key
		candidate.included = true
		candidate.remainingBudget = read.budget
		if controller != nil {
			if live := controller.live[key.ID]; live != nil {
				if live.activityAt.After(row.LatestActivityAt) {
					candidate.row.LatestActivityAt = live.activityAt.UTC().Format(time.RFC3339Nano)
				}
				// The frame builder's join, shared with the capture above: the
				// read and a pushed frame agree for every row state.
				candidate.row.LatestIntent = delegateDisplayIntent(live.intent, candidate.row.LatestIntent)
			}
		}
		candidates = append(candidates, candidate)
		matched++
		capturedReportBytes += len(candidate.report)
		if matched >= params.Limit || capturedReportBytes >= activityReportCaptureBudgetBytes {
			start--
			break
		}
	}
	return candidates, start < 0, nil
}

// The compact projection reads only already-owned fields, without cloning the
// immutable frozen prompt or traversing child transcripts and worktrees.
func captureSessionActivityDelegate(rootID string, state delegatestore.State, aggregate *delegatestore.Aggregate, now time.Time) sessionActivityDelegateCandidate {
	snapshot := delegateSnapshot{id: aggregate.DelegateID, parentID: aggregate.Descriptor.ParentDelegateID, descriptor: aggregate.Descriptor, generation: aggregate.Generation, phase: aggregate.Phase, currentRunOpen: aggregate.CurrentRunOpen, runStartedAt: aggregate.RunStartedAt, resumable: aggregate.Resumable, needsAttention: aggregate.NeedsAttention, notResumableReason: aggregate.NotResumableReason, latestActivityAt: aggregate.LatestActivityAt, lastOutcome: aggregate.LatestOutcome}
	snapshot.lifecycle = delegateLifecycleRunning
	if aggregate.Phase == delegatestore.PhaseIdle || aggregate.Phase == delegatestore.PhaseClosed {
		snapshot.lifecycle = delegateLifecycleIdle
	}
	// Timing only: this row carries clocks, not availability.
	status := projectDelegateStatusWithoutTools(now, snapshot)
	d := aggregate.Descriptor
	row := appwire.SessionDelegate{Name: activitybound.Truncate(d.Name, activityMaxLabelRunes), DelegateID: aggregate.DelegateID, RunGeneration: aggregate.Generation, ProjectionRevision: aggregate.ProjectionRevision, OwnerRef: encodeRef("", sessionActivityDelegateOwner(state, aggregate)), RootRef: encodeRef("", rootID), ChildRef: encodeRef("", d.ChildSessionID), ParentDelegateID: d.ParentDelegateID, Description: activitybound.Truncate(d.Description, activityMaxDelegateProseRunes), Task: activitybound.Truncate(d.Task, activityMaxDelegateProseRunes), Type: "delegate", Lifecycle: string(snapshot.lifecycle), Phase: string(aggregate.Phase), Status: string(snapshot.lifecycle), Resumable: aggregate.Resumable, NeedsAttention: aggregate.NeedsAttention, NotResumableReason: activitybound.Truncate(aggregate.NotResumableReason, activityMaxDelegateProseRunes), Model: activitybound.Truncate(d.ResolvedModel, activityMaxLabelRunes), ReasoningEffort: d.Config.ReasoningEffort, RunStartedAt: status.RunStartedAt, LatestActivityAt: status.LatestActivityAt, LatestIntent: appwire.Excerpt(aggregate.LatestIntent, appwire.MaxIntentRunes)}
	// LatestOutcome remains the immutable prior settlement while a new run is
	// open. Its ending must not be attributed to the current run generation.
	if outcome := aggregate.LatestOutcome; delegateRunTerminal(outcome, aggregate.CurrentRunOpen) {
		row.Reason = activitybound.Truncate(outcome.Reason, activityMaxDelegateProseRunes)
		row.Error = activitybound.Truncate(outcome.Error, activityMaxDelegateProseRunes)
		row.Outcome = string(outcome.Status)
		row.Terminal = true
		if !outcome.EndedAt.IsZero() {
			row.RunEndedAt = outcome.EndedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	candidate := sessionActivityDelegateCandidate{}
	// RunFinished replaces the immutable packet for the exact open generation
	// and closes that run atomically. A resumed run still owns the old packet.
	if packet := aggregate.LatestPacket; !aggregate.CurrentRunOpen && aggregate.LatestOutcome != nil && packet != nil && packet.Kind == delegatestore.PacketReported {
		candidate.report, candidate.reportComplete = activitybound.BoundMessage(packet.Message)
	}
	// Only a SETTLED generation reports usage and worktree from the packet: a
	// resumed run still owns the previous settlement's packet, and its HEAD and
	// dirty flag describe where that run left the worktree, not where this one
	// is. The guard lives in one helper so all three surfaces apply it.
	var metadata json.RawMessage
	if packet := aggregate.LatestPacket; packet != nil && !aggregate.CurrentRunOpen {
		// Take a REFERENCE, not a copy. The packet is durable and immutable — the
		// fold replaces packets rather than editing them, and its clone deep-copies
		// this slice — so the bytes cannot change under the projection that runs
		// after the unlock. Copying them here would allocate the whole document,
		// tool lists and briefs included, in exactly the critical section this
		// capture exists to keep short: the 16 KiB measurement that decides the
		// document is unusable happens later, so a copy taken here may be discarded
		// having already been paid for.
		metadata = packet.Metadata
	}
	candidate.row = row
	candidate.metadata = metadata
	return candidate
}

func (candidate sessionActivityDelegateCandidate) project() appwire.SessionDelegate {
	row := candidate.row
	// The same projection the Activity tree and the compact delegate read use, so
	// a live frame keeps the usage and worktree details those surfaces keep rather
	// than dropping them whenever a long tool list pushed the raw document over the
	// cap. It runs here, after the controller mutex is released, because it drops
	// and measures a document that can be far larger than the row it produces.
	if metadata, ok, _ := delegateTerminalMetadataProjection(candidate.metadata); ok {
		row.Usage = activityUsageFromCumulative(metadata.CumulativeUsage)
		if wt := metadata.Worktree; wt != nil {
			row.Worktree = &appwire.JobActivityWorktree{Path: activitybound.Truncate(wt.Path, activityMaxDelegateProseRunes), Branch: activitybound.Truncate(wt.Branch, activityMaxLabelRunes), HeadSHA: activitybound.Truncate(wt.HeadSHA, activityMaxLabelRunes), Ahead: wt.Ahead, Dirty: wt.Dirty}
		}
	}
	preview, truncated := activitybound.Preview(candidate.report, candidate.reportComplete)
	row.ReportPreview = preview
	row.ReportPreviewTruncated = truncated
	return row
}
