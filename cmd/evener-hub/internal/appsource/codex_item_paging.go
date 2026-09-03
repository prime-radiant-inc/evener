package appsource

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

const codexItemCursorProjectionVersion uint16 = 1

var codexItemIncarnationSequence atomic.Uint64

// codexItemSnapshot is the provider-independent view used by item-mode reads.
// Codex's native cursors never leave the adapter: callers page this complete,
// positioned candidate sequence with the shared AppWire cursor codec instead.
type codexItemSnapshot struct {
	ThreadRef   string
	Incarnation string
	Candidates  []appitempaging.TranscriptItemCandidate
}

// latestItemWindow materializes the native Codex history and returns the newest
// projected items. The native thread/turns/list cursor is deliberately scoped to
// materializeCodexTurns and is never used as an AppWire continuation cursor.
func (s *CodexSource) latestItemWindow(
	ctx context.Context,
	threadID string,
	limit int,
	itemsView string,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	unlock := s.itemPagingLocks.lock(threadID)
	defer unlock()
	return s.latestItemWindowLocked(ctx, threadID, limit, itemsView)
}

func (s *CodexSource) latestItemWindowLocked(
	ctx context.Context,
	threadID string,
	limit int,
	itemsView string,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	snapshot, err := s.refreshCodexItemSnapshot(ctx, threadID, itemsView)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	identity := codexItemSnapshotIdentity(snapshot)
	selected, hasOlder, err := appitempaging.SelectCandidates(snapshot.Candidates, nil, limit)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if hasOlder && len(selected) > 0 {
		window.OlderCursor, err = appitempaging.EncodeCursor(identity, selected[0].Position)
		if err != nil {
			return appitempaging.TranscriptItemWindow{}, identity, err
		}
	}
	return window, identity, nil
}

// previousItemWindow refreshes the provider snapshot before validating the
// cursor. This makes a changed/reordered/removed native prefix fail closed
// instead of silently applying an old position to a new history.
func (s *CodexSource) previousItemWindow(
	ctx context.Context,
	threadID string,
	cursor string,
	limit int,
	itemsView string,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	unlock := s.itemPagingLocks.lock(threadID)
	defer unlock()

	snapshot, err := s.refreshCodexItemSnapshot(ctx, threadID, itemsView)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	identity := codexItemSnapshotIdentity(snapshot)
	before, err := appitempaging.DecodeCursor(cursor, identity)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	selected, hasOlder, err := appitempaging.SelectCandidates(snapshot.Candidates, &before, limit)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if hasOlder && len(selected) > 0 {
		window.OlderCursor, err = appitempaging.EncodeCursor(identity, selected[0].Position)
		if err != nil {
			return appitempaging.TranscriptItemWindow{}, identity, err
		}
	}
	return window, identity, nil
}

func codexItemSnapshotIdentity(snapshot codexItemSnapshot) appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{
		ThreadRef:         snapshot.ThreadRef,
		Incarnation:       snapshot.Incarnation,
		ProjectionVersion: codexItemCursorProjectionVersion,
	}
}

func (s *CodexSource) refreshCodexItemSnapshot(ctx context.Context, threadID, itemsView string) (codexItemSnapshot, error) {
	turns, err := s.materializeCodexTurns(ctx, threadID, itemsView)
	if err != nil {
		return codexItemSnapshot{}, err
	}
	candidates, err := codexItemCandidates(turns)
	if err != nil {
		return codexItemSnapshot{}, err
	}
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return codexItemSnapshot{}, err
	}
	threadRef := appwire.Ref{SourceID: s.sourceID, ThreadID: threadID}.String()

	previous, exists := s.itemSnapshots.get(threadID)
	incarnation := previous.Incarnation
	next, extends := itemSnapshotStateAdvance(previous, candidates, true)
	if !exists || incarnation == "" || previous.ThreadRef != threadRef || !extends {
		incarnation = newCodexItemIncarnation()
		next = itemSnapshotStateForCandidates(threadRef, incarnation, "", candidates, true)
	}
	next.ThreadRef = threadRef
	next.Incarnation = incarnation
	next.SourceIdentity = ""
	s.itemSnapshots.put(threadID, next)
	current := codexItemSnapshot{
		ThreadRef:   threadRef,
		Incarnation: incarnation,
		Candidates:  candidates,
	}
	return cloneCodexItemSnapshot(current), nil
}

func (s *CodexSource) materializeCodexTurns(ctx context.Context, threadID, itemsView string) ([]codexTurn, error) {
	if itemsView == "" {
		itemsView = "full"
	}
	var pages [][]codexTurn
	cursor := ""
	seenCursors := map[string]struct{}{}
	err := s.withClient(ctx, func(client *appwire.Client) error {
		for {
			if cursor != "" {
				if _, seen := seenCursors[cursor]; seen {
					return errors.New("codex thread/turns/list cursor cycle detected")
				}
				seenCursors[cursor] = struct{}{}
			}
			var out codexTurnsListResponse
			req := map[string]any{
				"threadId":  threadID,
				"itemsView": itemsView,
			}
			if cursor != "" {
				req["cursor"] = cursor
			}
			if err := client.Request(ctx, appwire.MethodThreadTurnsList, req, &out); err != nil {
				return err
			}
			pages = append(pages, out.Data)
			if out.NextCursor == "" {
				return nil
			}
			if out.NextCursor == cursor {
				return errors.New("codex thread/turns/list repeated cursor")
			}
			cursor = out.NextCursor
		}
	})
	if err != nil {
		return nil, err
	}
	var turns []codexTurn
	for pageIndex := range slices.Backward(pages) {
		turns = append(turns, pages[pageIndex]...)
	}
	return turns, nil
}

func codexItemCandidates(turns []codexTurn) ([]appitempaging.TranscriptItemCandidate, error) {
	candidates := make([]appitempaging.TranscriptItemCandidate, 0)
	for entryOrdinal, nativeTurn := range turns {
		turn := mapCodexTurn(nativeTurn)
		if turn.ID == "" {
			return nil, fmt.Errorf("codex thread/turns/list returned a turn without an id at ordinal %d", entryOrdinal)
		}
		if uint64(entryOrdinal) > uint64(^uint32(0)) {
			return nil, errors.New("codex turn ordinal exceeds supported range")
		}
		for itemOrdinal := range turn.Items {
			if uint64(itemOrdinal) > uint64(^uint32(0)) {
				return nil, fmt.Errorf("codex item ordinal exceeds supported range in turn %q", turn.ID)
			}
			position := appwire.ThreadItemPosition{Entry: uint64(entryOrdinal), Item: uint32(itemOrdinal)}
			item := turn.Items[itemOrdinal]
			item.TurnID = turn.ID
			item.TranscriptEntryIndex = entryOrdinal
			item.Position = &position
			item.TranscriptKey = codexTranscriptItemKey(turn.ID, position)
			turn.Items[itemOrdinal] = item
			candidates = append(candidates, appitempaging.TranscriptItemCandidate{
				TurnID:          turn.ID,
				Turn:            turn,
				Item:            item,
				Position:        position,
				HasEarlierItems: itemOrdinal > 0,
				HasLaterItems:   itemOrdinal+1 < len(turn.Items),
			})
		}
	}
	return candidates, nil
}

func codexTranscriptItemKey(turnID string, position appwire.ThreadItemPosition) string {
	return fmt.Sprintf("codex-item-v%d:%s:%d:%d", codexItemCursorProjectionVersion, turnID, position.Entry, position.Item)
}

func newCodexItemIncarnation() string {
	return fmt.Sprintf("codex-incarnation-%d", codexItemIncarnationSequence.Add(1))
}

func cloneCodexItemSnapshot(snapshot codexItemSnapshot) codexItemSnapshot {
	clone := codexItemSnapshot{
		ThreadRef:   snapshot.ThreadRef,
		Incarnation: snapshot.Incarnation,
		Candidates:  make([]appitempaging.TranscriptItemCandidate, len(snapshot.Candidates)),
	}
	turns := make(map[string]appwire.Turn)
	for i, candidate := range snapshot.Candidates {
		cloned := candidate
		turn, ok := turns[candidate.TurnID]
		if !ok {
			turn = cloneCodexCachedTurn(candidate.Turn)
			turns[candidate.TurnID] = turn
		}
		cloned.Turn = turn
		cloned.Item = cloneCodexCachedItem(candidate.Item)
		position := candidate.Position
		cloned.Position = position
		itemPosition := position
		cloned.Item.Position = &itemPosition
		clone.Candidates[i] = cloned
	}
	return clone
}

// codexItemTurns is kept as a small adapter wrapper so the source's per-item
// completeness bits become fragment-boundary bits before shared regrouping.
func codexItemTurns(candidates []appitempaging.TranscriptItemCandidate) ([]appwire.Turn, error) {
	return appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(candidates))
}
