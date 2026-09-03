package appsource

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/rendezvous"
)

type Source interface {
	ID() string
	ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error)
	ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
	ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)
	StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error)
	ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error)
	ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error)
	StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error)
	ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error
	InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
	QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error)
	DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error)
	PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error)
	CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error)
	CompactThread(context.Context, appwire.ThreadCompactStartParams) error
	ShutdownThread(context.Context, appwire.ThreadShutdownParams) error
	SetThreadModel(context.Context, appwire.ThreadModelSetParams) error
	SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error
	SetThreadVisionModel(context.Context, appwire.ThreadVisionModelSetParams) error
	SetThreadName(context.Context, appwire.ThreadNameSetParams) error
	GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error)
	ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error)
	ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error)
	ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error)
	ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error)
	JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error)
	SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error)
}

// ItemCandidateResult is the private source-to-hub item page contract. The
// candidate window is positioned and chronological; cursor identity is kept
// outside the public AppWire response.
type ItemCandidateResult struct {
	Candidates appitempaging.TranscriptItemWindow
	Identity   appitempaging.CursorIdentity
	Exhausted  bool
}

// ItemCandidateSource exposes positioned item candidates without changing the
// legacy Source methods used by turn-mode callers.
type ItemCandidateSource interface {
	ReadItemCandidates(context.Context, appwire.ThreadReadParams) (ItemCandidateResult, error)
	ListItemCandidates(context.Context, appwire.ThreadTurnsListParams) (ItemCandidateResult, error)
}

// ItemReadCandidateSource converts the already-materialized result of an
// item-mode thread/read into the private candidate contract. Implementations
// must not issue another transcript read: this seam exists so the hub can keep
// source-owned cursor identity while packing and enriching the first response.
type ItemReadCandidateSource interface {
	ItemCandidatesFromRead(context.Context, appwire.ThreadReadParams, appwire.ThreadReadResponse) (ItemCandidateResult, error)
}

// CombinedItemReadSource returns an item-mode read and the private candidate
// identity derived from that exact response in one source-owned operation.
type CombinedItemReadSource interface {
	ReadThreadWithItemCandidates(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, ItemCandidateResult, error)
}

func (s *CodexSource) ItemCandidatesFromRead(
	_ context.Context,
	params appwire.ThreadReadParams,
	response appwire.ThreadReadResponse,
) (ItemCandidateResult, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	candidates, err := localDaemonItemCandidates(response.Thread.Turns)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return ItemCandidateResult{}, err
	}
	state, ok := s.itemSnapshots.get(threadID)
	if !ok {
		return ItemCandidateResult{}, errors.New("codex item read did not publish cursor identity")
	}
	identity := appitempaging.CursorIdentity{
		ThreadRef:         state.ThreadRef,
		Incarnation:       state.Incarnation,
		ProjectionVersion: codexItemCursorProjectionVersion,
	}
	if response.OlderCursor != "" {
		if _, err := appitempaging.DecodeCursor(response.OlderCursor, identity); err != nil {
			return ItemCandidateResult{}, err
		}
	} else {
		responseState := itemSnapshotStateForCandidates(state.ThreadRef, state.Incarnation, "", candidates, true)
		if state.ItemCount != responseState.ItemCount || state.TranscriptDigest != responseState.TranscriptDigest {
			return ItemCandidateResult{}, errors.New("codex item read cursor identity disagrees with response")
		}
	}
	return ItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates, OlderCursor: response.OlderCursor},
		Identity:   identity,
		Exhausted:  response.OlderCursor == "",
	}, nil
}

func (s *LocalDaemonSource) ItemCandidatesFromRead(
	_ context.Context,
	params appwire.ThreadReadParams,
	response appwire.ThreadReadResponse,
) (ItemCandidateResult, error) {
	resolved, err := s.resolveLocalDaemonItemThread(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	candidates, err := localDaemonItemCandidates(response.Thread.Turns)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return ItemCandidateResult{}, err
	}
	unlock := s.itemPagingLocks.lock(resolved.pagingRef)
	defer unlock()
	daemonIdentity := localDaemonItemDaemonIdentity(resolved.entry)
	previous, exists := s.itemSnapshots.get(resolved.pagingRef)
	incarnation := previous.Incarnation
	next, extends := itemSnapshotStateAdvance(previous, candidates, response.OlderCursor == "")
	if !exists || incarnation == "" || previous.ThreadRef != resolved.pagingRef || previous.SourceIdentity != daemonIdentity || !extends {
		incarnation = fmt.Sprintf("local-daemon-incarnation-%d", localDaemonItemIncarnationSequence.Add(1))
		next = itemSnapshotStateForCandidates(resolved.pagingRef, incarnation, daemonIdentity, candidates, response.OlderCursor == "")
	}
	next.ThreadRef = resolved.pagingRef
	next.Incarnation = incarnation
	next.SourceIdentity = daemonIdentity
	s.itemSnapshots.put(resolved.pagingRef, next)
	return ItemCandidateResult{
		// The daemon cursor has its own identity. Exhausted retains its truth, but
		// the hub mints a source-owned cursor from Identity below.
		Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates},
		Identity: appitempaging.CursorIdentity{
			ThreadRef:         resolved.pagingRef,
			Incarnation:       incarnation,
			ProjectionVersion: localDaemonItemCursorProjectionVersion,
		},
		Exhausted: response.OlderCursor == "",
	}, nil
}

func (s *CodexSource) ReadItemCandidates(ctx context.Context, params appwire.ThreadReadParams) (ItemCandidateResult, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	window, identity, err := s.latestItemWindow(ctx, threadID, params.ItemLimit, params.ItemsView)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	return ItemCandidateResult{
		Candidates: window,
		Identity:   identity,
		Exhausted:  window.OlderCursor == "",
	}, nil
}

func (s *CodexSource) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (ItemCandidateResult, error) {
	threadID, err := s.threadID(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	var window appitempaging.TranscriptItemWindow
	var identity appitempaging.CursorIdentity
	if params.Cursor == "" {
		window, identity, err = s.latestItemWindow(ctx, threadID, params.ItemLimit, params.ItemsView)
	} else {
		window, identity, err = s.previousItemWindow(ctx, threadID, params.Cursor, params.ItemLimit, params.ItemsView)
	}
	if err != nil {
		return ItemCandidateResult{}, err
	}
	return ItemCandidateResult{
		Candidates: window,
		Identity:   identity,
		Exhausted:  window.OlderCursor == "",
	}, nil
}

const localDaemonItemCursorProjectionVersion uint16 = 1

var localDaemonItemIncarnationSequence atomic.Uint64

type localDaemonItemSnapshot struct {
	ThreadRef   string
	Incarnation string
	Candidates  []appitempaging.TranscriptItemCandidate
}

// ReadItemCandidates materializes the daemon's authenticated legacy turn view
// and projects it into the same positioned candidate contract used by Codex.
// The daemon's native item cursor is intentionally never exposed to the hub:
// the source owns the identity and validates every continuation against its
// current snapshot.
func (s *LocalDaemonSource) ReadItemCandidates(ctx context.Context, params appwire.ThreadReadParams) (ItemCandidateResult, error) {
	resolved, err := s.resolveLocalDaemonItemThread(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	unlock := s.itemPagingLocks.lock(resolved.pagingRef)
	defer unlock()

	snapshot, err := s.refreshLocalDaemonItemSnapshot(ctx, resolved, params.ItemsView)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	identity := localDaemonItemSnapshotIdentity(snapshot)
	selected, hasOlder, err := appitempaging.SelectCandidates(snapshot.Candidates, nil, params.ItemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if hasOlder && len(selected) > 0 {
		window.OlderCursor, err = appitempaging.EncodeCursor(identity, selected[0].Position)
		if err != nil {
			return ItemCandidateResult{}, err
		}
	}
	return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: !hasOlder}, nil
}

// ListItemCandidates resolves a hub-owned cursor against a freshly
// materialized authenticated daemon snapshot. Browser cursors are never passed
// to the daemon's native turns/list endpoint.
func (s *LocalDaemonSource) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (ItemCandidateResult, error) {
	resolved, err := s.resolveLocalDaemonItemThread(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	unlock := s.itemPagingLocks.lock(resolved.pagingRef)
	defer unlock()

	snapshot, err := s.refreshLocalDaemonItemSnapshot(ctx, resolved, params.ItemsView)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	identity := localDaemonItemSnapshotIdentity(snapshot)
	var before *appwire.ThreadItemPosition
	if params.Cursor != "" {
		decoded, decodeErr := appitempaging.DecodeCursor(params.Cursor, identity)
		if decodeErr != nil {
			return ItemCandidateResult{}, decodeErr
		}
		before = &decoded
	}
	selected, hasOlder, err := appitempaging.SelectCandidates(snapshot.Candidates, before, params.ItemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if hasOlder && len(selected) > 0 {
		window.OlderCursor, err = appitempaging.EncodeCursor(identity, selected[0].Position)
		if err != nil {
			return ItemCandidateResult{}, err
		}
	}
	return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: !hasOlder}, nil
}

func localDaemonItemSnapshotIdentity(snapshot localDaemonItemSnapshot) appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{
		ThreadRef:         snapshot.ThreadRef,
		Incarnation:       snapshot.Incarnation,
		ProjectionVersion: localDaemonItemCursorProjectionVersion,
	}
}

type localDaemonItemThread struct {
	entry     rendezvous.Entry
	threadID  string
	routeRef  string
	pagingRef string
}

func (s *LocalDaemonSource) resolveLocalDaemonItemThread(rawRef, threadID string) (localDaemonItemThread, error) {
	entry, err := s.entryForReadRef(rawRef, threadID)
	if err != nil {
		return localDaemonItemThread{}, err
	}
	threadID = firstLocalNonEmpty(entry.SessionID, entry.ThreadID, threadID)
	return localDaemonItemThread{
		entry:     entry,
		threadID:  threadID,
		routeRef:  localDaemonWorkspaceRef(s.sourceID, entry, threadID),
		pagingRef: appwire.Ref{SourceID: s.sourceID, ThreadID: threadID}.String(),
	}, nil
}

func (s *LocalDaemonSource) refreshLocalDaemonItemSnapshot(
	ctx context.Context,
	resolved localDaemonItemThread,
	itemsView string,
) (localDaemonItemSnapshot, error) {
	turns, err := s.materializeLocalDaemonTurns(ctx, resolved.routeRef, resolved.threadID, itemsView)
	if err != nil {
		return localDaemonItemSnapshot{}, err
	}
	candidates, err := localDaemonItemCandidates(turns)
	if err != nil {
		return localDaemonItemSnapshot{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return localDaemonItemSnapshot{}, err
	}

	daemonIdentity := localDaemonItemDaemonIdentity(resolved.entry)
	previous, exists := s.itemSnapshots.get(resolved.pagingRef)
	incarnation := previous.Incarnation
	next, extends := itemSnapshotStateAdvance(previous, candidates, true)
	if !exists || incarnation == "" || previous.ThreadRef != resolved.pagingRef || previous.SourceIdentity != daemonIdentity || !extends {
		incarnation = fmt.Sprintf("local-daemon-incarnation-%d", localDaemonItemIncarnationSequence.Add(1))
		next = itemSnapshotStateForCandidates(resolved.pagingRef, incarnation, daemonIdentity, candidates, true)
	}
	next.ThreadRef = resolved.pagingRef
	next.Incarnation = incarnation
	next.SourceIdentity = daemonIdentity
	s.itemSnapshots.put(resolved.pagingRef, next)
	current := localDaemonItemSnapshot{
		ThreadRef:   resolved.pagingRef,
		Incarnation: incarnation,
		Candidates:  candidates,
	}
	return current, nil
}

func (s *LocalDaemonSource) materializeLocalDaemonTurns(ctx context.Context, ref, threadID, itemsView string) ([]appwire.Turn, error) {
	if itemsView == "" {
		itemsView = string(appwire.TurnItemsViewFull)
	}
	var pages [][]appwire.Turn
	cursor := ""
	seenCursors := map[string]struct{}{}
	for {
		if cursor != "" {
			if _, seen := seenCursors[cursor]; seen {
				return nil, errors.New("local daemon thread/turns/list cursor cycle detected")
			}
			seenCursors[cursor] = struct{}{}
		}
		response, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{
			Ref:       ref,
			ThreadID:  threadID,
			Cursor:    cursor,
			ItemsView: itemsView,
			PageUnit:  appwire.TranscriptPageUnitTurn,
		})
		if err != nil {
			return nil, err
		}
		pages = append(pages, response.Data)
		if response.NextCursor == "" {
			var turns []appwire.Turn
			for pageIndex := range slices.Backward(pages) {
				turns = append(turns, pages[pageIndex]...)
			}
			return turns, nil
		}
		if response.NextCursor == cursor {
			return nil, errors.New("local daemon thread/turns/list repeated cursor")
		}
		cursor = response.NextCursor
	}
}

func localDaemonItemCandidates(turns []appwire.Turn) ([]appitempaging.TranscriptItemCandidate, error) {
	candidates, err := appitempaging.CandidatesFromTurns(turns)
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		if candidates[index].Item.TurnID == "" {
			candidates[index].Item.TurnID = candidates[index].TurnID
		}
	}
	return candidates, nil
}

// RelaySessionSource is implemented by sources that can preserve one ordered
// upstream snapshot-to-live stream. Codex deliberately does not implement this
// interface because it converges through authoritative full-state replacement.
type RelaySessionSource interface {
	ResolveRelaySession(appwire.ThreadReadParams) (appwire.Ref, error)
	AcquireRelaySession(appwire.Ref) (RelaySessionRoutePublicationLease, error)
}

type RelaySessionLease interface {
	Read(context.Context, appwire.ThreadReadParams) (RelayReadResult, error)
	Listen(context.Context) (<-chan RelayDelivery, error)
	Close()
}

// RelaySessionRoutePublicationLease lets the hub publish the response's
// authoritative routing identity before the session waits for pre-cut delivery
// acknowledgements. The ordinary Read method remains available to callers that
// do not own a routing index.
type RelaySessionRoutePublicationLease interface {
	RelaySessionLease
	ReadWithRoutePublication(context.Context, appwire.ThreadReadParams, func(context.Context, appwire.Thread) error) (RelayReadResult, error)
}

type RelayReadResult struct {
	Response appwire.ThreadReadResponse
	Handoff  RelayHandoff
}

type RelayHandoff interface {
	Prepare() bool
	Commit() bool
	Abort() bool
}

type RelayDelivery struct {
	Notification appwire.Notification
	Acknowledge  func()
	// Proceed transfers bounded pending ownership to the listener without
	// acknowledging the delivery. It permits later ordered publications to
	// reach that listener; capture barriers still wait for Acknowledge.
	Proceed func()
}
