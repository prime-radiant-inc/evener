package transcriptindex

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// Snapshot exposes reads at one recorded byte boundary. It is valid only
// inside ReadAt's callback. It cannot extend, rebuild, or close the sidecar.
type Snapshot struct{ index *Index }

func (s *Snapshot) Latest(limit int) (Window, error) { return s.index.Latest(limit) }
func (s *Snapshot) Before(before appwire.ThreadItemPosition, limit int) (Window, error) {
	return s.index.Before(before, limit)
}
func (s *Snapshot) CandidateAt(position appwire.ThreadItemPosition) (appitempaging.TranscriptItemCandidate, error) {
	return s.index.CandidateAt(position)
}
func (s *Snapshot) ResolveTurnWindow(selector appwire.ThreadTurnWindow) (ResolvedTurnWindow, error) {
	return s.index.ResolveTurnWindow(selector)
}
func (s *Snapshot) Incarnation() (string, error)               { return s.index.Incarnation() }
func (s *Snapshot) ChangedSince(length int64) (Changes, error) { return s.index.ChangedSince(length) }
func (s *Snapshot) LatestSince(limit int, held appwire.SnapshotIdentity) (Window, *Changes, error) {
	return s.index.LatestSince(limit, held)
}
func (s *Snapshot) DerivedTotals(from int) (*appwire.EvenerUsage, int, error) {
	return s.index.DerivedTotals(from)
}
func (s *Snapshot) AnswerContinuation(ctx context.Context, ancestor, candidate string, held *appwire.SnapshotIdentity) (bool, appwire.SnapshotIdentity, error) {
	return s.index.AnswerContinuation(ctx, ancestor, candidate, held)
}

// ExecutionFrontier reports the newest recorded execution turn at this
// snapshot. A read answers it together with its continuation verdict so the
// advertised serving view comes from the same view, never from a row captured
// before the read.
func (s *Snapshot) ExecutionFrontier() (string, error) {
	return s.index.latestExecutionTurnID()
}

// ReadAt reads as of length even when an independently opened handle has
// extended the sidecar past it. The shared sidecar is never rolled back and
// its incarnation is retained. At the current boundary records are borrowed
// under the shared lock. An older boundary reconstructs private records from
// the transcript prefix up to the cut, including in-place summaries, contributors,
// and pending communicates, using the same canonical builder as the sidecar.
func (x *Index) ReadAt(length int64, fn func(*Snapshot) error) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	_, err := x.readWindow(func(_ *reader) (Window, error) {
		return Window{}, x.readAtLocked(length, fn)
	})
	return err
}

// readAtLocked runs with both the handle and sidecar locks held.
func (x *Index) readAtLocked(length int64, fn func(*Snapshot) error) error {
	view := &Index{readView: true, transcript: x.transcript, path: x.path,
		meta: x.meta, items: x.items, turns: x.turns, updates: x.updates,
		strings: x.strings, prelude: x.prelude, builder: x.builder}
	if length >= x.meta.Length {
		x.cutView = nil
	} else if cached := x.cutView; cached != nil && x.cutLength == length && cached.meta.Incarnation == x.meta.Incarnation {
		view = cached
		view.transcript = x.transcript
	} else {
		view.meta = meta{Incarnation: x.meta.Incarnation}
		view.prelude = nil
		view.items = table{size: itemRecordSize, buf: []byte{}}
		view.turns = table{size: turnRecordSize, buf: []byte{}}
		view.updates = table{size: updateRecordSize, buf: []byte{}}
		view.strings = blob{buf: []byte{}}
		view.builder = newBuilder(view)
		view.builder.global = map[string]string{}
		if err := view.scan(length); err != nil {
			return err
		}
		x.cutView, x.cutLength = view, length
	}
	return fn(&Snapshot{index: view})
}
