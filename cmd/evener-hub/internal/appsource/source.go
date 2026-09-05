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

func (s *LocalDaemonSource) ItemCandidatesFromRead(
	ctx context.Context,
	params appwire.ThreadReadParams,
	response appwire.ThreadReadResponse,
) (ItemCandidateResult, error) {
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	resolved, err := s.resolveLocalDaemonItemThread(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	candidates, err := localDaemonInitialItemCandidates(response)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return ItemCandidateResult{}, err
	}
	unlock := s.itemPagingLocks.lock(resolved.pagingRef)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	daemonIdentity := localDaemonItemDaemonIdentity(resolved.entry)
	previous, exists := s.itemSnapshots.peek(resolved.pagingRef)
	incarnation := previous.Incarnation
	observed := candidates
	complete := response.OlderCursor == ""
	// A bounded window alone cannot authenticate a formerly complete prefix,
	// even when it overlaps the retained tail. Follow its actual continuation.
	if exists && previous.SourceIdentity == daemonIdentity && previous.Prefix && previous.NativeCursor == "" && !complete {
		observed, err = s.localDaemonHiddenHistory(ctx, resolved, params.ItemsView, response.OlderCursor, candidates, previous.ItemCount)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		complete = true
	}
	next, extends, err := s.advanceLocalDaemonItemSnapshot(ctx, resolved, params.ItemsView, previous, observed, complete)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	rotated := !exists || incarnation == "" || previous.ThreadRef != resolved.pagingRef || previous.SourceIdentity != daemonIdentity || !extends
	if !rotated {
		if response.OlderCursor == "" {
			// The shared advance proved both the retained span and native history
			// before replacing a bounded observation with this complete snapshot.
			next.NativeCursor = ""
		} else if previous.NativeCursor == "" {
			next.NativeCursor = response.OlderCursor
		} else {
			if len(candidates) == 0 {
				return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
			}
			previousNativeCursor, err := appitempaging.RebaseCursor(previous.NativeCursor, candidates[0].Position)
			if err != nil {
				return ItemCandidateResult{}, err
			}
			responseNativeCursor, err := appitempaging.RebaseCursor(response.OlderCursor, candidates[0].Position)
			if err != nil {
				return ItemCandidateResult{}, err
			}
			rotated = previousNativeCursor != responseNativeCursor
			if !rotated {
				next.NativeCursor = previous.NativeCursor
			}
		}
	}
	if rotated {
		incarnation = fmt.Sprintf("local-daemon-incarnation-%d", localDaemonItemIncarnationSequence.Add(1))
		next = itemSnapshotStateForCandidates(resolved.pagingRef, incarnation, daemonIdentity, observed, complete)
		next.NativeCursor = response.OlderCursor
	}
	next.ThreadRef = resolved.pagingRef
	next.Incarnation = incarnation
	next.SourceIdentity = daemonIdentity
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	if err := s.itemSnapshots.putContext(ctx, resolved.pagingRef, next); err != nil {
		return ItemCandidateResult{}, err
	}
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

const localDaemonItemCursorProjectionVersion uint16 = 1

var localDaemonItemIncarnationSequence atomic.Uint64

type localDaemonItemSnapshot struct {
	Candidates []appitempaging.TranscriptItemCandidate
	state      itemSnapshotState
}

// ReadItemCandidates materializes the daemon's authenticated legacy turn view
// and projects it into the private positioned-candidate source contract.
// The daemon's native item cursor is intentionally never exposed to the hub:
// the source owns the identity and validates every continuation against its
// current snapshot.
func (s *LocalDaemonSource) ReadItemCandidates(ctx context.Context, params appwire.ThreadReadParams) (ItemCandidateResult, error) {
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	resolved, err := s.resolveLocalDaemonItemThread(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	unlock := s.itemPagingLocks.lock(resolved.pagingRef)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}

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
	if err := s.itemSnapshots.putContext(ctx, resolved.pagingRef, snapshot.state); err != nil {
		return ItemCandidateResult{}, err
	}
	return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: !hasOlder}, nil
}

// ListItemCandidates resolves a hub-owned cursor against the bounded native
// daemon snapshot retained by ItemCandidatesFromRead. The browser cursor is
// decoded against the hub identity, then its boundary is rebased onto the
// retained opaque daemon cursor. Native state lives in the bounded item snapshot
// cache, so eviction safely turns a continuation into a typed stale error.
func (s *LocalDaemonSource) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (ItemCandidateResult, error) {
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	resolved, err := s.resolveLocalDaemonItemThread(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	unlock := s.itemPagingLocks.lock(resolved.pagingRef)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}

	if params.Cursor == "" {
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
		if err := s.itemSnapshots.putContext(ctx, resolved.pagingRef, snapshot.state); err != nil {
			return ItemCandidateResult{}, err
		}
		return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: !hasOlder}, nil
	}

	state, ok := s.itemSnapshots.peek(resolved.pagingRef)
	daemonIdentity := localDaemonItemDaemonIdentity(resolved.entry)
	if !ok || state.ThreadRef != resolved.pagingRef || state.Incarnation == "" || state.SourceIdentity != daemonIdentity {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	if state.NativeCursor == "" {
		if !state.Prefix {
			return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
		}
		snapshot, err := s.refreshLocalDaemonItemSnapshot(ctx, resolved, params.ItemsView)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		identity := localDaemonItemSnapshotIdentity(snapshot)
		before, err := appitempaging.DecodeCursor(params.Cursor, identity)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		selected, hasOlder, err := appitempaging.SelectCandidates(snapshot.Candidates, &before, params.ItemLimit)
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
		if err := s.itemSnapshots.putContext(ctx, resolved.pagingRef, snapshot.state); err != nil {
			return ItemCandidateResult{}, err
		}
		return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: !hasOlder}, nil
	}
	identity := appitempaging.CursorIdentity{
		ThreadRef:         state.ThreadRef,
		Incarnation:       state.Incarnation,
		ProjectionVersion: localDaemonItemCursorProjectionVersion,
	}
	before, err := appitempaging.DecodeCursor(params.Cursor, identity)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	nativeCursor, err := appitempaging.RebaseCursor(state.NativeCursor, before)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	response, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{
		Ref:       resolved.routeRef,
		ThreadID:  resolved.threadID,
		Cursor:    nativeCursor,
		ItemsView: params.ItemsView,
		PageUnit:  appwire.TranscriptPageUnitItem,
		ItemLimit: itemLimit,
	})
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if err := appwire.ValidateThreadTurnsListItemResponse(response); err != nil {
		return ItemCandidateResult{}, err
	}
	candidates, err := localDaemonItemCandidates(response.Data)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return ItemCandidateResult{}, err
	}
	if response.NextCursor != "" {
		if len(candidates) == 0 {
			return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
		}
		boundary := candidates[0].Position
		requestCanonical, err := appitempaging.RebaseCursor(nativeCursor, boundary)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		nextCanonical, err := appitempaging.RebaseCursor(response.NextCursor, boundary)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		if requestCanonical != nextCanonical {
			return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
		}
	}
	selected := localDaemonCandidatesBefore(candidates, before, itemLimit)
	if len(selected) == 0 {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	if response.NextCursor != "" {
		state.NativeCursor = response.NextCursor
	}
	if err := s.itemSnapshots.putContext(ctx, resolved.pagingRef, state); err != nil {
		return ItemCandidateResult{}, err
	}
	return ItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{Candidates: selected},
		Identity:   identity,
		Exhausted:  response.NextCursor == "",
	}, nil
}

// localDaemonHiddenHistory reconstructs chronological history to the native
// beginning. Counts alone cannot identify an anchored prefix across an append gap.
func (s *LocalDaemonSource) localDaemonHiddenHistory(ctx context.Context, resolved localDaemonItemThread, itemsView, nativeCursor string, current []appitempaging.TranscriptItemCandidate, remaining int) ([]appitempaging.TranscriptItemCandidate, error) {
	pages := [][]appitempaging.TranscriptItemCandidate{current}
	count := len(current)
	seen := make(map[string]bool)
	for nativeCursor != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[nativeCursor] {
			return nil, errors.New("local daemon hidden item prefix cursor cycle detected")
		}
		seen[nativeCursor] = true
		response, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: resolved.routeRef, ThreadID: resolved.threadID, Cursor: nativeCursor, ItemsView: itemsView, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: min(max(remaining, 1), appwire.TranscriptItemPageLimit)})
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := appwire.ValidateThreadTurnsListItemResponse(response); err != nil {
			return nil, err
		}
		candidates, err := localDaemonItemCandidates(response.Data)
		if err != nil {
			return nil, err
		}
		if err := appitempaging.ValidateCandidates(candidates); err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return nil, errors.New("local daemon hidden item prefix continuation made no progress")
		}
		// Require strict backward progress, including across the current window.
		newer := pages[len(pages)-1]
		if len(newer) > 0 {
			last, first := candidates[len(candidates)-1].Position, newer[0].Position
			if last.Entry > first.Entry || (last.Entry == first.Entry && last.Item >= first.Item) {
				return nil, appwire.TranscriptItemCursorStale()
			}
		}
		if response.NextCursor != "" {
			boundary := candidates[0].Position
			requestCanonical, err := appitempaging.RebaseCursor(nativeCursor, boundary)
			if err != nil {
				return nil, err
			}
			nextCanonical, err := appitempaging.RebaseCursor(response.NextCursor, boundary)
			if err != nil {
				return nil, err
			}
			if requestCanonical != nextCanonical {
				return nil, appwire.TranscriptItemCursorStale()
			}
		}
		remaining -= len(candidates)
		if remaining <= 0 {
			remaining = appwire.TranscriptItemPageLimit
		}
		pages = append(pages, candidates)
		count += len(candidates)
		nativeCursor = response.NextCursor
	}
	var history []appitempaging.TranscriptItemCandidate
	if count > 0 {
		history = make([]appitempaging.TranscriptItemCandidate, 0, count)
	}
	for i := range slices.Backward(pages) {
		history = append(history, pages[i]...)
	}
	if err := appitempaging.ValidateCandidates(history); err != nil {
		return nil, err
	}
	return history, ctx.Err()
}

func localDaemonCandidatesBefore(candidates []appitempaging.TranscriptItemCandidate, before appwire.ThreadItemPosition, limit int) []appitempaging.TranscriptItemCandidate {
	older := make([]appitempaging.TranscriptItemCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Position.Entry > before.Entry || (candidate.Position.Entry == before.Entry && candidate.Position.Item >= before.Item) {
			continue
		}
		older = append(older, candidate)
	}
	if len(older) > limit {
		older = older[len(older)-limit:]
	}
	return older
}

func localDaemonItemSnapshotIdentity(snapshot localDaemonItemSnapshot) appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{
		ThreadRef:         snapshot.state.ThreadRef,
		Incarnation:       snapshot.state.Incarnation,
		ProjectionVersion: localDaemonItemCursorProjectionVersion,
	}
}

// advanceLocalDaemonItemSnapshot authenticates a bounded-to-complete transition
// before either complete caller can discard its retained native generation.
func (s *LocalDaemonSource) advanceLocalDaemonItemSnapshot(ctx context.Context, resolved localDaemonItemThread, itemsView string, previous itemSnapshotState, candidates []appitempaging.TranscriptItemCandidate, complete bool) (itemSnapshotState, bool, error) {
	next, extends := itemSnapshotStateAdvance(previous, candidates, complete)
	if !extends || !complete || previous.NativeCursor == "" || previous.SourceIdentity != localDaemonItemDaemonIdentity(resolved.entry) {
		return next, extends, nil
	}
	// Rebase at the retained last item: native pages authenticate everything
	// older, while the span digest above authenticates that last item itself.
	boundary := slices.IndexFunc(candidates, func(candidate appitempaging.TranscriptItemCandidate) bool {
		return candidate.Position == previous.LastPosition
	})
	if boundary < 0 {
		return next, false, nil
	}
	cursor, err := appitempaging.RebaseCursor(previous.NativeCursor, previous.LastPosition)
	if err != nil {
		return next, false, err
	}
	history, err := s.localDaemonHiddenHistory(ctx, resolved, itemsView, cursor, candidates[boundary:], previous.ItemCount)
	if ctx.Err() != nil {
		return next, false, ctx.Err()
	}
	if err != nil {
		var wire appwire.WireError
		if errors.As(err, &wire) {
			data, ok := wire.Data.(appwire.ErrorData)
			if ok && data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale {
				return next, false, nil
			}
		}
		return next, false, err
	}
	return next, itemSnapshotStateMatchesCompleteCandidates(next, history), nil
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
	if err := ctx.Err(); err != nil {
		return localDaemonItemSnapshot{}, err
	}
	turns, err := s.materializeLocalDaemonTurns(ctx, resolved.routeRef, resolved.threadID, itemsView)
	if err != nil {
		return localDaemonItemSnapshot{}, err
	}
	candidates, err := localDaemonMaterializedItemCandidates(turns)
	if err != nil {
		return localDaemonItemSnapshot{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return localDaemonItemSnapshot{}, err
	}

	daemonIdentity := localDaemonItemDaemonIdentity(resolved.entry)
	previous, exists := s.itemSnapshots.peek(resolved.pagingRef)
	incarnation := previous.Incarnation
	next, extends, err := s.advanceLocalDaemonItemSnapshot(ctx, resolved, itemsView, previous, candidates, true)
	if err != nil {
		return localDaemonItemSnapshot{}, err
	}
	if !exists || incarnation == "" || previous.ThreadRef != resolved.pagingRef || previous.SourceIdentity != daemonIdentity || !extends {
		incarnation = fmt.Sprintf("local-daemon-incarnation-%d", localDaemonItemIncarnationSequence.Add(1))
		next = itemSnapshotStateForCandidates(resolved.pagingRef, incarnation, daemonIdentity, candidates, true)
	}
	next.ThreadRef = resolved.pagingRef
	next.Incarnation = incarnation
	next.SourceIdentity = daemonIdentity
	if err := ctx.Err(); err != nil {
		return localDaemonItemSnapshot{}, err
	}
	current := localDaemonItemSnapshot{
		Candidates: candidates,
		state:      next,
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

// localDaemonMaterializedItemCandidates keeps the daemon's absolute transcript
// positions. A complete turn-shaped transcript still cannot recover positions
// omitted by legacy responses because decoded entries may project no items (or
// no turn), so materialization must use the same strict identity validation as
// a native item response.
func localDaemonMaterializedItemCandidates(turns []appwire.Turn) ([]appitempaging.TranscriptItemCandidate, error) {
	return localDaemonItemCandidates(turns)
}

// localDaemonInitialItemCandidates preserves positions supplied by the daemon
// and rejects every unpositioned item, including complete legacy turn reads.
func localDaemonInitialItemCandidates(response appwire.ThreadReadResponse) ([]appitempaging.TranscriptItemCandidate, error) {
	return localDaemonItemCandidates(response.Thread.Turns)
}

// RelaySessionSource is implemented by the retained local-daemon source, which
// preserves one ordered upstream snapshot-to-live stream.
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
