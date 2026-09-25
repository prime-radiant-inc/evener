package server

import (
	"fmt"
	"os"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/transcriptindex"
)

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
// it is left in both.
func (h *threadHistory) latest(c historyCapture, threadRef string, limit int) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, overlay []appwire.OverlayItem, err error) {
	var window transcriptindex.Window
	err = h.read(func(idx *transcriptindex.Index) (err error) {
		window, err = idx.Latest(limit)
		return err
	})
	if err != nil {
		return nil, "", appwire.SnapshotIdentity{}, nil, err
	}
	turns, olderCursor, snapshot, err = h.page(window, threadRef)
	if err != nil {
		return nil, "", appwire.SnapshotIdentity{}, nil, err
	}
	overlay = make([]appwire.OverlayItem, 0, len(c.overlay))
	for _, item := range c.overlay {
		if h.overlay.Contains(item.Key) {
			overlay = append(overlay, item)
		}
	}
	return turns, olderCursor, snapshot, overlay, nil
}

// before pages the limit items before cursor from the transcript. A cursor
// naming another thread or an incarnation other than the current one is
// appwire.TranscriptItemCursorStale(): the client re-reads the latest window.
func (h *threadHistory) before(threadRef, cursor string, limit int) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, err error) {
	var window transcriptindex.Window
	err = h.read(func(idx *transcriptindex.Index) error {
		incarnation, err := idx.Incarnation()
		if err != nil {
			return err
		}
		position, err := appitempaging.DecodeCursor(cursor, historyCursorIdentity(threadRef, incarnation))
		if err != nil {
			return err
		}
		window, err = idx.Before(position, limit)
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
		return nil, "", appwire.SnapshotIdentity{}, err
	}
	return h.page(window, threadRef)
}

// read runs fn on the thread's index extended to the recorded length, after
// applying the entries queued within that length to the overlay.
//
// A failed thread's hook no longer tracks the recorded length, so its read
// takes the length from the append lock. If the read fails it is
// appwire.TranscriptHistoryFailed, naming the entry; if it succeeds, the
// transcript is readable again and the history is asked to recover.
func (h *threadHistory) read(fn func(*transcriptindex.Index) error) error {
	h.mu.Lock()
	failed := h.failed
	h.mu.Unlock()
	var length int64
	var err error
	if failed != nil {
		length, err = h.boundaryLength()
	} else {
		length, err = h.RecordedLength()
		h.applyQueued(length)
	}
	if err == nil {
		err = h.readIndex(length, fn)
	}
	switch {
	case failed == nil:
		return err
	case err != nil:
		return appwire.TranscriptHistoryFailed(failed.Ordinal)
	}
	h.requestRecovery()
	return nil
}

// boundaryLength is the transcript's recorded length, read under its append
// lock, or its size when no writer in this process has it open.
func (h *threadHistory) boundaryLength() (int64, error) {
	var length int64
	found, err := transcript.AtRecordedBoundary(h.path, func(recordedLength int64) { length = recordedLength })
	if err != nil || found {
		return length, err
	}
	info, err := os.Stat(h.path)
	if err != nil {
		return 0, fmt.Errorf("stat transcript: %w", err)
	}
	return info.Size(), nil
}

func (h *threadHistory) readIndex(length int64, fn func(*transcriptindex.Index) error) error {
	idx, err := h.cache.Acquire(h.path)
	if err != nil {
		return err
	}
	defer h.cache.Release(idx)
	if err := idx.CatchUpTo(length); err != nil {
		return err
	}
	return fn(idx)
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
