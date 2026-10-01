package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

type sessionActivityRead struct {
	context  appwire.SessionActivityContext
	scope    appwire.SessionActivityScope
	index    *sessionActivityIndex
	root     *Session
	stateDir string
	rootID   string
	budget   int
	bytes    int64
}

func normalizeSessionActivity(ref string, scope appwire.SessionActivityScope, limit int) (string, appwire.SessionActivityScope, int, error) {
	project, id, err := decodeRef(ref)
	if err != nil || project != "" || schema.ValidateSessionID(id) != nil {
		return "", scope, 0, appwire.InvalidParams("explicit resolved local session ref required")
	}
	if scope == "" {
		scope = appwire.SessionActivityScopeSession
	}
	if scope != appwire.SessionActivityScopeSession && scope != appwire.SessionActivityScopeSubtree {
		return "", scope, 0, appwire.InvalidParams("invalid session activity scope")
	}
	if limit < 0 {
		return "", scope, 0, appwire.InvalidParams("negative session activity limit")
	}
	if limit == 0 {
		limit = 50
	}
	return id, scope, min(limit, 200), nil
}
func (s *Session) activityRead(ctx context.Context, params appwire.SessionActivityReadParams) (*sessionActivityRead, error) {
	id, scope, _, err := normalizeSessionActivity(params.Ref, params.Scope, 0)
	if err != nil {
		return nil, err
	}
	controller := s.delegateController
	rootID := s.ID()
	root := s
	if controller != nil {
		rootID = controller.rootSessionID
		root = controller.rootRuntime
	}
	if id != s.ID() && controller == nil {
		return nil, appwire.InvalidParams("session is outside the activity controller")
	}
	index, err := acquireSessionActivityIndex(ctx, s.stateDir+"\x00"+rootID, controller)
	if err != nil {
		return nil, err
	}
	read := &sessionActivityRead{scope: scope, index: index, root: root, stateDir: s.stateDir, rootID: rootID, budget: activityMaxWorkUnits, bytes: 4 << 20}
	read.context = appwire.SessionActivityContext{Ref: params.Ref, SessionID: id, RootRef: encodeRef("", rootID), Epoch: index.epoch, Availability: "live"}
	if controller == nil {
		read.context.AncestryKnown = true
		return read.withBoundedContext()
	}
	controller.mu.Lock()
	read.context.AncestryKnown = activityContextFromDelegates(&read.context, rootID, controller.durable)
	live := id == rootID
	if !live {
		if delegateID := read.context.DelegateID; delegateID != "" {
			live = controller.live[delegateID] != nil && controller.live[delegateID].runtime != nil
		}
	}
	controller.mu.Unlock()
	if !read.context.AncestryKnown {
		index.release()
		return nil, appwire.InvalidParams("session is outside the activity controller")
	}
	if !live {
		read.context.Availability = "retained"
	}
	return read.withBoundedContext()
}

// Breadcrumb identities stay intact; an unrepresentable context is an explicit
// source failure rather than an oversized response or a stagnant continuation.
func (read *sessionActivityRead) withBoundedContext() (*sessionActivityRead, error) {
	encoded, err := json.Marshal(read.context)
	if err != nil || len(encoded) > sessionActivityPageBytes-2048 {
		read.index.release()
		return nil, appwire.Unavailable("session activity context exceeds response budget")
	}
	return read, nil
}

// Filesystem access can recover without replacing activity authority. Corrupt
// or unrepresentable stored data remains unavailable until the source changes.
func sessionActivitySourceReadError(message string, cause error) appwire.WireError {
	if _, pathError := errors.AsType[*os.PathError](cause); pathError && !errors.Is(cause, os.ErrNotExist) {
		return sessionActivitySourceUnavailable(message)
	}
	return appwire.Unavailable(message)
}
func sessionActivitySourceUnavailable(message string) appwire.WireError {
	return appwire.WireError{Code: appwire.CodeUnavailable, Message: message, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorActionUnavailable, RetryDisposition: appwire.RetryDispositionAutomatic}}
}

func retainedActivityRead(ctx context.Context, stateDir, sessionID string, params appwire.SessionActivityReadParams) (*sessionActivityRead, error) {
	id, scope, _, err := normalizeSessionActivity(params.Ref, params.Scope, 0)
	if err != nil {
		return nil, err
	}
	if id != sessionID {
		return nil, appwire.InvalidParams("ref does not name the retained session")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, appwire.ResourceNotFound("session activity source missing")
		}
		return nil, sessionActivitySourceReadError("session activity metadata unavailable", err)
	}
	rootID := activityRootIDFromMeta(id, meta)
	if schema.ValidateSessionID(rootID) != nil {
		return nil, appwire.Unavailable("invalid retained activity root")
	}
	index, err := acquireSessionActivityIndex(ctx, stateDir+"\x00"+rootID, nil)
	if err != nil {
		return nil, err
	}
	read := &sessionActivityRead{scope: scope, index: index, stateDir: stateDir, rootID: rootID, budget: activityMaxWorkUnits, bytes: 4 << 20}
	read.context = appwire.SessionActivityContext{Ref: params.Ref, SessionID: id, RootRef: encodeRef("", rootID), Epoch: index.epoch, Availability: "retained", AncestryKnown: id == rootID}
	if id != rootID {
		read.context.AncestryKnown = activityContextFromDelegates(&read.context, rootID, index.delegates)
		if !read.context.AncestryKnown {
			_, err = read.advanceDelegates(ctx)
			if err != nil {
				index.release()
				return nil, err
			}
			read.context.AncestryKnown = activityContextFromDelegates(&read.context, rootID, index.delegates)
			if !read.context.AncestryKnown && index.delegateComplete {
				index.release()
				return nil, appwire.Unavailable("retained delegate ancestry unavailable")
			}
		}
	}

	return read.withBoundedContext()
}

// ParentDelegateID identifies logical ownership; OwnerSessionID can name the
// physical root that admitted a nested delegate's runtime.
func sessionActivityDelegateOwner(state delegatestore.State, row *delegatestore.Aggregate) string {
	if row == nil {
		return ""
	}
	parentID := row.Descriptor.ParentDelegateID
	if parentID == "" {
		return row.Descriptor.OwnerSessionID
	}
	if parent := state[parentID]; parent != nil {
		return parent.Descriptor.ChildSessionID
	}
	return ""
}

// Immutable Created descriptors prove lineage independently of status history.
func activityContextFromDelegates(result *appwire.SessionActivityContext, rootID string, state delegatestore.State) bool {
	if result.SessionID == rootID {
		return true
	}
	target := result
	proven := *result
	result = &proven
	child := result.SessionID
	seen := make(map[string]bool)
	var chain []appwire.SessionActivityAncestor
	for child != rootID {
		if seen[child] {
			return false
		}
		seen[child] = true
		var aggregate *delegatestore.Aggregate
		for _, candidate := range state {
			if candidate != nil && candidate.Descriptor.ChildSessionID == child {
				aggregate = candidate
				break
			}
		}
		if aggregate == nil {
			return false
		}
		d := aggregate.Descriptor
		owner := sessionActivityDelegateOwner(state, aggregate)
		if owner == "" {
			return false
		}
		if result.DelegateID == "" {
			result.DelegateID = aggregate.DelegateID
			result.ParentRef = encodeRef("", owner)
		}
		parentDelegateID := d.ParentDelegateID
		chain = append(chain, appwire.SessionActivityAncestor{Ref: encodeRef("", owner), SessionID: owner, DelegateID: parentDelegateID, Title: truncateActivityText(owner, activityMaxLabelRunes)})
		if parentDelegateID != "" {
			parent := state[parentDelegateID]
			if parent == nil || parent.Descriptor.ChildSessionID != owner {
				return false
			}
			chain[len(chain)-1].Title = truncateActivityText(parent.Descriptor.Description, activityMaxLabelRunes)
		} else if owner != rootID {
			return false
		}
		child = owner
	}
	slices.Reverse(chain)
	result.Ancestors = chain
	*target = *result
	return true
}
func (read *sessionActivityRead) delegatePath() string {
	return filepath.Join(jobsDir(read.stateDir, read.rootID), "delegates.jsonl")
}
func (read *sessionActivityRead) advanceDelegates(ctx context.Context) (bool, error) {
	if read.index.controller != nil {
		return true, nil
	}
	index := read.index
	path := read.delegatePath()
	if read.bytes <= 128 {
		return false, nil
	}
	read.bytes -= int64(len(index.delegateSource.Tail))
	info, err := index.checkSource(path, &index.delegateSource)
	if err != nil {
		return false, err
	}
	if info == nil {
		index.delegateComplete = true
		return true, nil
	}
	if index.delegateComplete && index.delegateCursor.Journal.Offset == info.Size() {
		return true, nil
	}
	if read.budget == 0 || read.bytes <= 128 {
		return false, nil
	}
	reserved := false
	if len(index.delegatePending) == 0 {
		before := index.delegateCursor.Journal.Offset
		cursor := index.delegateCursor
		index.scanCalls++
		events, complete, scanErr := delegatestore.ReadPage(ctx, path, &cursor, read.bytes-128, read.budget)
		if scanErr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, sessionActivitySourceReadError("retained delegate journal unavailable", scanErr)
		}
		used := cursor.Journal.Offset - before
		if cursor.Journal.Info != nil && !os.SameFile(index.delegateSource.Info, cursor.Journal.Info) {
			index.reset()
			return false, appwire.SessionActivityCursorStale()
		}
		if _, sourceErr := index.checkSource(path, &index.delegateSource); sourceErr != nil {
			return false, sourceErr
		}
		index.rawBytes += uint64(cursor.Journal.ReadBytes)
		read.bytes -= cursor.Journal.ReadBytes + 128
		read.budget -= max(len(events), cursor.Journal.ReadLines)
		index.progress += uint64(used) + uint64(len(events))
		if err := read.acceptDelegatePage(cursor, events, complete); err != nil {
			return false, err
		}
		if !complete && used == 0 && len(events) == 0 {
			return false, sessionActivitySourceUnavailable("retained delegate journal append incomplete")
		}
		reserved = true
	}
	for index.delegatePendingPosition < len(index.delegatePending) {
		i := index.delegatePendingPosition
		event := index.delegatePending[i]
		if !reserved {
			if read.budget == 0 {
				return false, nil
			}
			read.budget--
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if event.Seq != index.delegateSequence+1 {
			return false, appwire.Unavailable("retained delegate sequence invalid")
		}
		if err = delegatestore.Apply(index.delegates, event); err != nil {
			return false, appwire.Unavailable("retained delegate journal invalid")
		}
		if event.Created != nil {
			index.delegateOffsets[event.DelegateID] = index.delegatePendingEnds[i]
			index.delegateKeys = insertSessionActivityKey(index.delegateKeys, sessionActivityCreationKey(event.TS, event.DelegateID))
		}
		index.delegatePendingPosition++
		index.delegateSequence++
	}

	index.delegateComplete = index.delegatePendingComplete
	index.delegatePending = nil
	index.delegatePendingEnds = nil
	index.delegatePendingPosition = 0
	return index.delegateComplete, nil
}
func (read *sessionActivityRead) acceptDelegatePage(cursor delegatestore.PageCursor, events []delegatestore.Event, complete bool) error {
	index := read.index
	// Capture the candidate fingerprint before accepting its cursor and buffered events.
	if err := read.captureTail(read.delegatePath(), &index.delegateSource, cursor.Journal.Offset); err != nil {
		return err
	}
	index.delegateCursor = cursor
	index.delegateComplete = false
	index.delegatePending = events
	index.delegatePendingEnds = cursor.EventEnds
	index.delegatePendingPosition = 0
	index.delegatePendingComplete = complete
	return nil
}

func (read *sessionActivityRead) state() delegatestore.State {
	if read.index.controller != nil {
		return read.index.controller.durable
	}
	return read.index.delegates
}
func (read *sessionActivityRead) owners() map[string]bool {
	owners := map[string]bool{read.context.SessionID: true}
	if read.scope == appwire.SessionActivityScopeSession {
		return owners
	}
	state := read.state()
	// Membership inspection is in-memory; Created parentage is the authority.
	changed := true
	for changed {
		changed = false
		for _, row := range state {
			if row != nil && owners[sessionActivityDelegateOwner(state, row)] && !owners[row.Descriptor.ChildSessionID] {
				owners[row.Descriptor.ChildSessionID] = true
				changed = true
			}
		}
	}
	return owners
}

// ActivitySummary returns scoped counts from cheap live authority or warm indexes.
func (s *Session) ActivitySummary(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	read, err := s.activityRead(ctx, params)
	if err != nil {
		return appwire.SessionActivitySummary{}, err
	}
	defer read.index.release()
	return read.summary(ctx)
}

// LoadSessionActivitySummary reads retained context without badge-driven journal scans.
func LoadSessionActivitySummary(ctx context.Context, stateDir, sessionID string, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	read, err := retainedActivityRead(ctx, stateDir, sessionID, params)
	if err != nil {
		return appwire.SessionActivitySummary{}, err
	}
	defer read.index.release()
	return read.summary(ctx)
}
func (read *sessionActivityRead) summary(ctx context.Context) (appwire.SessionActivitySummary, error) {
	result := appwire.SessionActivitySummary{Context: read.context, Scope: read.scope}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Issue admission reserves the optional array envelope. The shared page
	// allowance leaves 2048 bytes for late count, pending-flag and epoch growth.
	pageBudget := newSessionActivityPageBudget(result)
	pageBudget.bytes += len(`,"issues":[]`)
	if pageBudget.bytes > sessionActivityPageBytes-2048 {
		return result, appwire.Unavailable("session activity summary context exceeds response budget")
	}
	controller := read.index.controller
	if controller != nil {
		controller.mu.Lock()
	}
	if !read.context.AncestryKnown {
		if controller != nil {
			controller.mu.Unlock()
		}
		return result, nil
	}
	owners := read.owners()
	if controller != nil || read.index.delegateComplete {
		result.Delegates.Known = true
		for _, row := range read.state() {
			if row != nil && owners[sessionActivityDelegateOwner(read.state(), row)] {
				result.Delegates.Total++
				if !delegateRunTerminal(row.LatestOutcome, row.CurrentRunOpen) {
					result.Delegates.Active++
				} else if activityDelegateOutcome(string(row.LatestOutcome.Status)) == "failure" {
					result.Delegates.Failed++
				} else {
					result.Delegates.Completed++
				}
			}
		}
	}
	if controller != nil {
		controller.mu.Unlock()
	}
	// Receiver watches can be held by any physical descendant source.
	sourceOwners := map[string]bool{read.rootID: true}
	if controller != nil {
		controller.mu.Lock()
	}
	for _, row := range read.state() {
		if row != nil {
			sourceOwners[row.Descriptor.ChildSessionID] = true
		}
	}
	if controller != nil {
		controller.mu.Unlock()
	}
	var err error
	result.RefreshPending, result.Issues, err = read.refreshWarmSources(ctx, sourceOwners, &pageBudget)
	if err != nil {
		return result, err
	}
	// Counts and bounded recovery demand describe one observed revision.
	// An invalidation after this capture belongs to the next summary response.
	version := read.index.revision.Load()
	unavailable := make(map[string]bool, len(result.Issues))
	for _, issue := range result.Issues {
		unavailable[issue.Ref] = true
	}
	for owner := range sourceOwners {
		source := read.index.jobs[owner]
		if source != nil && source.Established && (!source.Complete || source.Version != version) && !unavailable[encodeRef("", owner)] {
			result.RefreshPending = true
		}
	}
	result.Context.Epoch = read.index.epoch
	if controller == nil && !read.index.delegateComplete {
		result.Delegates = appwire.SessionActivityCounts{}
	}
	// Cold sources have no complete authority to catch up from.
	result.Jobs.Known = true
	for owner := range owners {
		index := read.index.jobs[owner]
		if index == nil || !index.Complete || index.Version != version {
			result.Jobs = appwire.SessionActivityCounts{}
			break
		}
		for _, job := range index.Jobs {
			if string(job.Type) != "shell" {
				continue
			}
			terminal, outcome := activityOutcome(job.Status)
			result.Jobs.Total++
			if !terminal {
				result.Jobs.Active++
			} else if outcome == "failure" {
				result.Jobs.Failed++
			} else {
				result.Jobs.Completed++
			}
		}
	}
	// Receiver watches may be physically held by a descendant; their complete
	// counts require the same shared-root watch index used by the collection.
	result.Watches = appwire.SessionActivityCounts{Known: true}
	for owner := range sourceOwners {
		source := read.index.jobs[owner]
		if source == nil || !source.Complete || source.Version != version {
			result.Watches = appwire.SessionActivityCounts{}
			break
		}
		for id, record := range source.Watches {
			receiver := record.ReceiverSessionID
			if receiver == "" {
				receiver = owner
			}
			if !owners[receiver] || (!record.Active && !slices.Contains(source.Ended, id)) {
				continue
			}
			result.Watches.Total++
			if !record.Active {
				result.Watches.Completed++
				continue
			}
			proven := false
			if runtime := read.runtime(owner); runtime != nil && runtime.jobManager != nil {
				jm := runtime.jobManager
				jm.mu.Lock()
				for _, cfg := range jm.watches {
					if cfg != nil && cfg.id == id {
						proven = true
						if watchOneShotHasFired(cfg) {
							result.Watches.Completed++
						} else {
							result.Watches.Active++
						}
						break
					}
				}
				jm.mu.Unlock()
			}
			if !proven {
				result.Watches.Known = false
			}

		}
	}
	return result, nil
}
