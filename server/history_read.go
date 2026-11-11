package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/transcriptindex"
)

const (
	// historyWindowTransferMessageReserveBytes is held back from
	// historyWindowTransferPageBytes for the response envelope a windowed read
	// carries beside its turns (the thread projection, snapshot and history
	// identity).
	historyWindowTransferMessageReserveBytes = 8 << 10

	// historyWindowTransferAppliedBy names the daemon as the source of a
	// partial window page. The hub overwrites the field with the arrangement
	// that packed the response it serves.
	historyWindowTransferAppliedBy = "serving-daemon"
)

// historyWindowTransferPageBytes bounds the projected turns one windowed read
// response carries. A window larger than this transfers in pages the hub walks
// before packing, so a selection larger than AppWire's message limit cannot
// disconnect the transport. Tests shrink it to exercise the page boundary.
var historyWindowTransferPageBytes = 8 << 20

// A deterministic test barrier after a read is admitted, before disk work.
var historyReadAfterBeginHook atomic.Pointer[func(*threadHistory)]

// answerContinuation reports the verdict together with the execution frontier
// of the snapshot it answered from: an execution recorded after the caller's
// cut makes the captured row stale, and the response must advertise what its
// own snapshot shows.
func (h *threadHistory) answerContinuation(ctx context.Context, params appwire.ThreadReadParams) (appwire.AnswerContinuationVerdict, appwire.SnapshotIdentity, string, error) {
	query := params.AnswerContinuation
	verdict := appwire.AnswerContinuationVerdict{AncestorTurnID: query.AncestorTurnID, CandidateTurnID: query.CandidateTurnID}
	var snapshot appwire.SnapshotIdentity
	var frontier string
	if err := ctx.Err(); err != nil {
		return verdict, snapshot, frontier, err
	}
	err := h.read(func(idx *transcriptindex.Snapshot) error {
		var err error
		verdict.Matches, snapshot, err = idx.AnswerContinuation(ctx, query.AncestorTurnID, query.CandidateTurnID, params.HeldSnapshot)
		if err != nil {
			return err
		}
		frontier, err = idx.ExecutionFrontier()
		return err
	})
	return verdict, snapshot, frontier, err
}

func (h *threadHistory) fetch(requestRef, encoded string) (appwire.SessionFetchResponse, error) {
	cursor, identity, err := appitempaging.DecodeFetchCursorForRef(encoded, h.ref)
	if err != nil && requestRef != "" && requestRef != h.ref {
		// A window read admitted through the current-session ref mints its cursors
		// for that ref, which differs from the stable workspace ref this history
		// records once Clear has kept the workspace and changed the session. The
		// request already resolved to this history, so the admitted ref names the
		// same thread and its cursor is valid here.
		cursor, identity, err = appitempaging.DecodeFetchCursorForRef(encoded, requestRef)
	}
	if err != nil || cursor.Origin != appitempaging.FetchCursorOriginServingDaemon {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	sourceIncarnation, ok := appwire.ThreadWindowSourceIncarnation(identity.Incarnation)
	if !ok {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	var response appwire.SessionFetchResponse
	err = h.read(func(idx *transcriptindex.Snapshot) error {
		incarnation, err := idx.Incarnation()
		if err != nil {
			return err
		}
		if incarnation != sourceIncarnation {
			return appwire.TranscriptItemCursorStale()
		}
		candidate, err := idx.CandidateAt(cursor.Position)
		if err != nil {
			return err
		}
		response, err = appitempaging.BuildFetchResponse(candidate.Item, identity, cursor)
		return err
	})
	return response, err
}

// historyCapture is what a history read takes inside the subscription cut:
// the overlay and the resync epoch the read's notifications follow. History
// itself is projected after the cut.
type historyCapture struct {
	epoch   uint64
	overlay []appwire.OverlayItem
}

// capture runs inside the subscription cut, so it does no I/O.
func (h *threadHistory) capture() historyCapture {
	return historyCapture{epoch: h.Epoch(), overlay: h.overlay.Snapshot()}
}

// latest projects the newest limit items after the cut, up to the recorded
// length, and returns the captured overlay minus the items the overlay no
// longer holds: a stream, preview or tool state an entry recorded since the
// cut covered is history in this response. The read applies every queued
// entry within the projected length to the overlay first, so nothing within
// it is left in both, except while the overlay gap is open (a dropped queue
// not yet replayed, or a failed thread): then the overlay may still hold
// state the returned history covers, and the client drops it by key and
// version as it does for a notification that races a read.
func (h *threadHistory) latest(c historyCapture, threadRef string, limit int) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, latestExecutionTurnID string, overlay []appwire.OverlayItem, err error) {
	var window transcriptindex.Window
	err = h.read(func(idx *transcriptindex.Snapshot) (err error) {
		window, err = idx.Latest(limit)
		return err
	})
	if err != nil {
		return nil, "", appwire.SnapshotIdentity{}, "", nil, err
	}
	turns, olderCursor, snapshot, err = h.page(window, threadRef)
	if err != nil {
		return nil, "", appwire.SnapshotIdentity{}, "", nil, err
	}
	overlay = make([]appwire.OverlayItem, 0, len(c.overlay))
	for _, item := range c.overlay {
		if h.overlay.Contains(item.Key) {
			overlay = append(overlay, item)
		}
	}
	return turns, olderCursor, snapshot, window.LatestExecutionTurnID, overlay, nil
}

// turnWindow projects a turn-addressed read from the same index snapshot used
// by legacy item paging. Turn selectors never mint legacy item cursors.
//
// The returned turns are one bounded transfer page: the newest items of a
// latest/before selection or the oldest items of a since selection, whichever
// fit historyWindowTransferPageBytes. resume, when set, continues a page that
// was cut inside one turn: it names that turn and the position the previous
// page ended at. transfer is nil when the whole selection fit, and otherwise
// names what the page withheld so the hub can request the next page. A tiny
// window keeps the exact response it had before paging.
func (h *threadHistory) turnWindow(c historyCapture, threadRef string, selector appwire.ThreadTurnWindow, resume *appwire.ThreadWindowTransfer) (turns []appwire.Turn, snapshot appwire.SnapshotIdentity, latestExecutionTurnID string, transfer *appwire.ThreadWindowResult, overlay []appwire.OverlayItem, err error) {
	var resolved transcriptindex.ResolvedTurnWindow
	var latest transcriptindex.Window
	err = h.read(func(idx *transcriptindex.Snapshot) error {
		var err error
		resolved, err = idx.ResolveTurnWindow(selector)
		if err != nil {
			return err
		}
		latest, err = idx.Latest(1)
		return err
	})
	if err != nil {
		if missing, ok := errors.AsType[*transcriptindex.TurnNotFoundError](err); ok {
			return nil, appwire.SnapshotIdentity{}, "", nil, nil, appwire.TranscriptTurnNotFound(missing.TurnID, missing.Frontier.OldestTurnID, missing.Frontier.NewestTurnID, missing.Frontier.TurnCount)
		}
		return nil, appwire.SnapshotIdentity{}, "", nil, nil, err
	}
	candidates, servedTurnIDs, transfer, err := boundTurnWindowTransfer(resolved, selector, resume)
	if err != nil {
		return nil, appwire.SnapshotIdentity{}, "", nil, nil, err
	}
	turns, _, snapshot, err = h.page(transcriptindex.Window{
		Candidates:  candidates,
		Incarnation: resolved.Incarnation,
		Length:      resolved.Length,
	}, threadRef)
	if err != nil {
		return nil, appwire.SnapshotIdentity{}, "", nil, nil, err
	}
	turns = appitempaging.PreserveSelectedTurnShells(turns, servedTurnIDs)
	overlay = make([]appwire.OverlayItem, 0, len(c.overlay))
	for _, item := range c.overlay {
		if h.overlay.Contains(item.Key) {
			overlay = append(overlay, item)
		}
	}
	return turns, snapshot, latest.LatestExecutionTurnID, transfer, overlay, nil
}

// boundTurnWindowTransfer cuts a resolved selection down to one transfer page.
// A latest or before selection keeps its newest turns, because those are the
// ones the packer serves first and the withheld older ones resume behind them.
// A since selection keeps its oldest turns, because it is read oldest-first.
//
// Turns, not items, are the unit of a page: a turn can be reopened, so its
// items need not be adjacent in the transcript, and page counts by turn order.
// A page therefore carries every item of the turns it serves and none of the
// turns it withholds, which keeps the served/withheld accounting honest for an
// interleaved selection. Only a turn larger than the page itself transfers in
// pieces: the piece's shell carries HasEarlierItems (or HasLaterItems) when its
// own turn continues past the page, which is how the hub knows to resume inside
// that turn. resume continues such a piece: it names the turn and the exclusive
// position the previous piece ended at.
func boundTurnWindowTransfer(
	resolved transcriptindex.ResolvedTurnWindow,
	selector appwire.ThreadTurnWindow,
	resume *appwire.ThreadWindowTransfer,
) ([]appitempaging.TranscriptItemCandidate, []string, *appwire.ThreadWindowResult, error) {
	limit := historyWindowTransferPageBytes - historyWindowTransferMessageReserveBytes
	turnIDs := resolved.TurnIDs
	if limit <= 0 || len(turnIDs) == 0 {
		return resolved.Candidates, turnIDs, nil, nil
	}
	candidates := resolved.Candidates
	if resume != nil {
		filtered, found := transferWindowCandidates(candidates, *resume)
		if !found {
			return nil, nil, nil, appwire.TranscriptItemCursorStale()
		}
		candidates = filtered
		if len(candidates) == 0 {
			return nil, nil, &appwire.ThreadWindowResult{AppliedBy: historyWindowTransferAppliedBy}, nil
		}
	}
	if len(candidates) == 0 {
		return candidates, turnIDs, nil, nil
	}
	sizes := make([]int, len(candidates))
	total := 0
	for index, candidate := range candidates {
		encoded, err := json.Marshal(candidate.Item)
		if err != nil {
			encoded = []byte(candidate.Item.Text)
		}
		sizes[index] = len(encoded)
		total += len(encoded)
	}
	if total <= limit {
		return candidates, turnIDs, nil, nil
	}
	if resume != nil {
		served, servedTurnIDs, transfer := boundResumeTransfer(candidates, sizes, *resume, limit)
		return served, servedTurnIDs, transfer, nil
	}
	served, servedTurnIDs, transfer := boundTurnOrderTransfer(candidates, sizes, turnIDs, selector, limit)
	return served, servedTurnIDs, transfer, nil
}

// boundResumeTransfer serves one item piece of the turn a previous page cut.
// It returns the item slice, marks the piece's open end, and reports the turn
// as the page's single served turn.
func boundResumeTransfer(
	candidates []appitempaging.TranscriptItemCandidate,
	sizes []int,
	resume appwire.ThreadWindowTransfer,
	limit int,
) ([]appitempaging.TranscriptItemCandidate, []string, *appwire.ThreadWindowResult) {
	start, end := transferItemRange(sizes, limit, resume.Before)
	served := append([]appitempaging.TranscriptItemCandidate(nil), candidates[start:end]...)
	markTransferOpenEnds(served, start > 0, end < len(candidates), resume.Before)
	return served, []string{resume.TurnID}, &appwire.ThreadWindowResult{
		AppliedBy:         historyWindowTransferAppliedBy,
		ServedTurns:       1,
		FirstServedTurnID: resume.TurnID,
		LastServedTurnID:  resume.TurnID,
		TrimmedEnd:        transferTrimmedEnd(resume.Before),
	}
}

// boundTurnOrderTransfer serves whole logical turns from the edge the selector
// reads first. A turn that alone exceeds the page transfers as an item piece of
// that one turn, so every other served turn is complete and every withheld turn
// is absent rather than half-present.
func boundTurnOrderTransfer(
	candidates []appitempaging.TranscriptItemCandidate,
	sizes []int,
	turnIDs []string,
	selector appwire.ThreadTurnWindow,
	limit int,
) ([]appitempaging.TranscriptItemCandidate, []string, *appwire.ThreadWindowResult) {
	keepNewest := selector.Since == ""
	turnSizes := make(map[string]int, len(turnIDs))
	for index, candidate := range candidates {
		turnSizes[candidate.TurnID] += sizes[index]
	}
	servedCount, used := 0, 0
	for offset := range len(turnIDs) {
		turnID := turnIDs[offset]
		if keepNewest {
			turnID = turnIDs[len(turnIDs)-1-offset]
		}
		size := turnSizes[turnID]
		if used+size > limit {
			break
		}
		used += size
		servedCount++
	}
	if servedCount == 0 {
		// The first turn the selector serves is larger than a whole page.
		boundary := turnIDs[0]
		withheld := turnIDs[1:]
		if keepNewest {
			boundary = turnIDs[len(turnIDs)-1]
			withheld = turnIDs[:len(turnIDs)-1]
		}
		served, servedTurnIDs, transfer := boundBoundaryTurnTransfer(candidates, sizes, boundary, withheld, limit, keepNewest)
		if transfer == nil {
			return candidates, turnIDs, nil
		}
		return served, servedTurnIDs, transfer
	}
	servedTurnIDs := turnIDs[:servedCount]
	withheld := turnIDs[servedCount:]
	if keepNewest {
		servedTurnIDs = turnIDs[len(turnIDs)-servedCount:]
		withheld = turnIDs[:len(turnIDs)-servedCount]
	}
	servedSet := make(map[string]bool, len(servedTurnIDs))
	for _, turnID := range servedTurnIDs {
		servedSet[turnID] = true
	}
	served := make([]appitempaging.TranscriptItemCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if servedSet[candidate.TurnID] {
			served = append(served, candidate)
		}
	}
	return served, append([]string(nil), servedTurnIDs...), transferWindowResult(servedTurnIDs, withheld, keepNewest)
}

// boundBoundaryTurnTransfer serves the items of one turn that alone exceeds a
// page, keeping the end the selector reads first and marking the other end as
// the piece's continuation.
func boundBoundaryTurnTransfer(
	candidates []appitempaging.TranscriptItemCandidate,
	sizes []int,
	boundary string,
	withheld []string,
	limit int,
	keepNewest bool,
) ([]appitempaging.TranscriptItemCandidate, []string, *appwire.ThreadWindowResult) {
	items := make([]appitempaging.TranscriptItemCandidate, 0, len(candidates))
	itemSizes := make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		if candidate.TurnID != boundary {
			continue
		}
		items = append(items, candidate)
		itemSizes = append(itemSizes, sizes[index])
	}
	if len(items) == 0 {
		// A surface that does not name the boundary turn cannot page safely;
		// transfer the selection whole as before.
		return candidates, nil, nil
	}
	start, end := transferItemRange(itemSizes, limit, keepNewest)
	served := append([]appitempaging.TranscriptItemCandidate(nil), items[start:end]...)
	markTransferOpenEnds(served, start > 0, end < len(items), keepNewest)
	servedTurnIDs := []string{boundary}
	return served, servedTurnIDs, transferWindowResult(servedTurnIDs, withheld, keepNewest)
}

// transferItemRange keeps the newest (keepNewest) or oldest items that fit the
// page, always with at least one item: the hub bounds what it forwards and
// AppWire's per-message limit is far larger.
func transferItemRange(sizes []int, limit int, keepNewest bool) (int, int) {
	if keepNewest {
		start, used := len(sizes), 0
		for start > 0 {
			if used+sizes[start-1] > limit {
				break
			}
			used += sizes[start-1]
			start--
		}
		if start == len(sizes) && len(sizes) > 0 {
			start = len(sizes) - 1
		}
		return start, len(sizes)
	}
	end, used := 0, 0
	for end < len(sizes) {
		if end > 0 && used+sizes[end] > limit {
			break
		}
		used += sizes[end]
		end++
	}
	if end == 0 && len(sizes) > 0 {
		end = 1
	}
	return 0, end
}

// markTransferOpenEnds flags the served piece's cut end: earlier when older
// items of the same turn are withheld, later when newer ones are.
func markTransferOpenEnds(served []appitempaging.TranscriptItemCandidate, hasEarlier, hasLater, keepNewest bool) {
	if len(served) == 0 {
		return
	}
	if hasEarlier && keepNewest {
		served[0].HasEarlierItems = true
	}
	if hasLater && !keepNewest {
		served[len(served)-1].HasLaterItems = true
	}
}

func transferWindowResult(servedTurnIDs, withheldTurnIDs []string, keepNewest bool) *appwire.ThreadWindowResult {
	trimmedEnd := "oldest"
	if !keepNewest {
		trimmedEnd = "newest"
	}
	transfer := &appwire.ThreadWindowResult{
		AppliedBy:         historyWindowTransferAppliedBy,
		ServedTurns:       len(servedTurnIDs),
		WithheldTurns:     len(withheldTurnIDs),
		FirstServedTurnID: servedTurnIDs[0],
		LastServedTurnID:  servedTurnIDs[len(servedTurnIDs)-1],
		TrimmedEnd:        trimmedEnd,
	}
	if len(withheldTurnIDs) > 0 {
		transfer.FirstWithheldTurnID = withheldTurnIDs[0]
		transfer.LastWithheldTurnID = withheldTurnIDs[len(withheldTurnIDs)-1]
	}
	return transfer
}

// transferWindowCandidates keeps the items a resume page requests: strictly
// older or strictly newer than the position it names. found is false when the
// resolved window no longer holds that position, which means the transcript
// moved under the transfer.
func transferWindowCandidates(candidates []appitempaging.TranscriptItemCandidate, resume appwire.ThreadWindowTransfer) ([]appitempaging.TranscriptItemCandidate, bool) {
	found := false
	for _, candidate := range candidates {
		if candidate.Position == resume.Position {
			found = true
			break
		}
	}
	if !found {
		return nil, false
	}
	kept := make([]appitempaging.TranscriptItemCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		order := appitempaging.ComparePosition(candidate.Position, resume.Position)
		if resume.Before && order < 0 {
			kept = append(kept, candidate)
		}
		if !resume.Before && order > 0 {
			kept = append(kept, candidate)
		}
	}
	return kept, true
}

func transferTrimmedEnd(keepNewest bool) string {
	if keepNewest {
		return "oldest"
	}
	return "newest"
}

// before pages the request's ItemLimit items before its cursor from the
// transcript, or before its Before boundary when it has no cursor, taken under
// the current incarnation (appitempaging.Boundary). A
// cursor naming another thread or an incarnation other than the current one is
// appwire.TranscriptItemCursorStale(): the client re-reads the latest window.
func (h *threadHistory) before(threadRef string, params appwire.ThreadTurnsListParams) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, err error) {
	turns, olderCursor, snapshot, _, err = h.beforeWithFrontier(threadRef, params)
	return
}

func (h *threadHistory) beforeWithFrontier(threadRef string, params appwire.ThreadTurnsListParams) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, frontier string, err error) {
	var window transcriptindex.Window
	err = h.read(func(idx *transcriptindex.Snapshot) error {
		incarnation, err := idx.Incarnation()
		if err != nil {
			return err
		}
		position, err := appitempaging.Boundary(params, historyCursorIdentity(threadRef, incarnation))
		if err != nil {
			return err
		}
		window, err = idx.Before(position, params.ItemLimit)
		if err != nil {
			return err
		}
		// Another handle may rebuild between the two reads.
		if window.Incarnation != incarnation {
			return appwire.TranscriptItemCursorStale()
		}
		return nil
	})
	if err != nil {
		return nil, "", appwire.SnapshotIdentity{}, "", err
	}
	turns, olderCursor, snapshot, err = h.page(window, threadRef)
	return turns, olderCursor, snapshot, window.LatestExecutionTurnID, err
}

// read runs fn on the thread's index extended to the recorded length, after
// applying the entries queued within that length to the overlay.
//
// A read of a failed thread first tries to recover it by rebuilding
// (recoverForRead); if that fails the read is appwire.TranscriptHistoryFailed,
// naming the entry, and pushes nothing. A read that starts once the history
// began closing is refused with errThreadHistoryClosed.
func (h *threadHistory) read(fn func(*transcriptindex.Snapshot) error) error {
	if err := h.beginRead(); err != nil {
		return err
	}
	defer h.reads.Done()
	if hook := historyReadAfterBeginHook.Load(); hook != nil {
		(*hook)(h)
	}
	if err := h.recoverForRead(); err != nil {
		return err
	}
	length, err := h.RecordedLength()
	if err != nil {
		return err
	}
	h.applyQueued(length)
	return h.readIndex(length, fn)
}

func (h *threadHistory) readIndex(length int64, fn func(*transcriptindex.Snapshot) error) error {
	idx, err := h.cache.Acquire(h.path)
	if err != nil {
		return err
	}
	defer h.cache.Release(idx)
	if err := idx.CatchUpTo(length); err != nil {
		return err
	}
	return idx.ReadAt(length, fn)
}

// page regroups a window into turn fragments priced by their model, with the
// cursor to the items before it.
func (h *threadHistory) page(window transcriptindex.Window, threadRef string) ([]appwire.Turn, string, appwire.SnapshotIdentity, error) {
	turns, err := appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(window.Candidates))
	if err != nil {
		return nil, "", appwire.SnapshotIdentity{}, err
	}
	if h.cost != nil {
		models := make(map[string]string, len(turns))
		for _, candidate := range window.Candidates {
			models[candidate.TurnID] = candidate.Model
		}
		for i := range turns {
			if model := models[turns[i].ID]; model != "" {
				turns[i].Cost = appwire.EstimateCost(h.cost(model), turns[i].Usage)
			}
		}
	}
	olderCursor := ""
	if window.HasOlder {
		olderCursor, err = appitempaging.EncodeCursor(historyCursorIdentity(threadRef, window.Incarnation), window.Candidates[0].Position)
		if err != nil {
			return nil, "", appwire.SnapshotIdentity{}, err
		}
	}
	return turns, olderCursor, appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length}, nil
}

func historyCursorIdentity(threadRef, incarnation string) appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{ThreadRef: threadRef, Incarnation: incarnation, ProjectionVersion: appitempaging.TranscriptItemProjectionVersion}
}
