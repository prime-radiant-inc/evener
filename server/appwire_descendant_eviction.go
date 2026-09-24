package server

import (
	"os"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// appResidentQuiescentDescendants bounds how many quiescent descendants keep
// their turn snapshots in memory. A snapshot costs about as much as the
// delegate's transcript (median ~0.5 MB, p90 ~1.6 MB in a long-lived state
// dir), and a daemon otherwise keeps one for every delegate it ever ran. Sixteen
// covers the delegate rows a transcript view watches at once plus a preview,
// so browsing a session does not re-parse transcripts on every open, while the
// resident history of finished delegates stays in the tens of megabytes.
const appResidentQuiescentDescendants = 16

// appTurnsEviction is what rebuilding an evicted descendant snapshot needs.
type appTurnsEviction struct {
	// transcriptBytes is the transcript's length at eviction. The snapshot then
	// held exactly what that prefix persisted, and a session persists before it
	// emits, so anything past it arrives again as live events after the
	// rebuild. Rebuilding from the whole file would show those twice.
	transcriptBytes int64
	// nextEntry is the evicted snapshot's next live entry. A rebuilt snapshot
	// allocates no entry below it, so items streamed after a resume order after
	// everything a subscriber already holds.
	nextEntry uint64
}

// quiescent reports that the descendant has settled with no turn in flight.
// Its snapshot then holds nothing its transcript cannot rebuild except
// live-only diagnostics, so it may be evicted.
func (p *appDescendantProjection) quiescent() bool {
	if p.activeTurnID != "" {
		return false
	}
	switch p.thread.Status.Type {
	case appwire.ThreadStatusIdle, appwire.ThreadStatusAwaiting, appwire.ThreadStatusClosed:
		return true
	default:
		return false
	}
}

func (s *Server) touchDescendantLocked(projection *appDescendantProjection) {
	s.appDescendantUseSerial++
	projection.lastUsed = s.appDescendantUseSerial
}

func (s *Server) descendantTranscriptPathLocked(threadID string) string {
	if s.appDescendantTranscriptPathFunc == nil {
		return ""
	}
	return strings.TrimSpace(s.appDescendantTranscriptPathFunc(threadID))
}

// evictQuiescentDescendantsLocked drops the turn snapshots of all but the keep
// most recently used quiescent descendants. It never evicts a descendant with
// a turn in flight, one a read has pinned, or one with no transcript to rebuild
// from.
//
// Callers hold s.mu inside a projection commit: a commit captures snapshot
// pointers under s.mu and applies to them after releasing it, so an eviction
// outside the gate could strand an apply on a snapshot nobody reads.
func (s *Server) evictQuiescentDescendantsLocked(keep int) {
	type candidate struct {
		threadID   string
		projection *appDescendantProjection
	}
	var resident []candidate
	for threadID, projection := range s.appDescendants {
		if projection.turns != nil && projection.quiescent() {
			resident = append(resident, candidate{threadID: threadID, projection: projection})
		}
	}
	if len(resident) <= keep {
		return
	}
	sort.Slice(resident, func(i, j int) bool { return resident[i].projection.lastUsed < resident[j].projection.lastUsed })
	// A pinned descendant still counts against keep; the next evictable one
	// goes in its place.
	excess := len(resident) - keep
	for _, c := range resident {
		if excess == 0 {
			return
		}
		if c.projection.readers > 0 {
			continue
		}
		path := s.descendantTranscriptPathLocked(c.threadID)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		excess--
		c.projection.turns.mu.Lock()
		c.projection.eviction = appTurnsEviction{transcriptBytes: info.Size(), nextEntry: c.projection.turns.nextLiveEntry}
		c.projection.turns.mu.Unlock()
		c.projection.turns = nil
	}
}

// rebuiltDescendantTurns is a descendant snapshot rebuilt from its transcript,
// and the persisted turn count its projector must be fenced above.
type rebuiltDescendantTurns struct {
	snapshot         *appTurnSnapshot
	persistedEntries int
}

// rebuildDescendantTurns projects a descendant's transcript into a new
// snapshot: the prefix the evicted snapshot held, or the whole file for a
// descendant never seen before (a zero eviction). It reads the file, so it
// never runs under the subscription cut.
//
// The rebuilt snapshot cannot reproduce the live one it replaces: live-only
// items (prompt_loaded, round_timings) are never persisted, so the same
// position names a different item after a rebuild. A new snapshot mints a new
// cursor incarnation, so a cursor from before the eviction fails as stale and
// the client re-reads rather than paging the wrong items.
func rebuildDescendantTurns(threadID, path string, eviction appTurnsEviction) (rebuiltDescendantTurns, error) {
	persisted, err := appTurnProjectionFromTranscriptPrefix(path, eviction.transcriptBytes)
	if err != nil {
		return rebuiltDescendantTurns{}, err
	}
	snapshot := &appTurnSnapshot{threadID: threadID}
	snapshot.Seed(appTurnSeed{Turns: persisted.turns, NextEntry: max(persisted.nextEntry, eviction.nextEntry)})
	return rebuiltDescendantTurns{snapshot: snapshot, persistedEntries: persisted.persistedEntries}, nil
}

// adoptLocked installs the rebuilt snapshot and fences the projector above the
// transcript's persisted turn ids, so a live turn cannot reuse one.
func (r rebuiltDescendantTurns) adoptLocked(projection *appDescendantProjection) {
	projection.turns = r.snapshot
	projection.projector.SeedPersistedTurns(r.persistedEntries)
}

// installDescendantTurnsLocked gives a descendant with no resident snapshot one
// seeded from its transcript: on its first observation, and when an event
// arrives for an evicted descendant. With nothing to rebuild from it starts
// empty.
func (s *Server) installDescendantTurnsLocked(threadID string, projection *appDescendantProjection) {
	if path := s.descendantTranscriptPathLocked(threadID); path != "" {
		if rebuilt, err := rebuildDescendantTurns(threadID, path, projection.eviction); err == nil {
			rebuilt.adoptLocked(projection)
			return
		}
	}
	projection.turns = &appTurnSnapshot{threadID: threadID, nextLiveEntry: projection.eviction.nextEntry}
}

// pinDescendantTurns keeps threadID's turn snapshot resident until release,
// rebuilding it first if it was evicted. It is a no-op for a thread that is not
// a descendant. A read calls it before entering the subscription cut, which
// must not open a file.
func (s *Server) pinDescendantTurns(threadID string) (release func(), err error) {
	s.mu.Lock()
	projection := s.appDescendants[threadID]
	if projection == nil {
		s.mu.Unlock()
		return func() {}, nil
	}
	projection.readers++
	s.touchDescendantLocked(projection)
	evicted := projection.turns == nil
	eviction := projection.eviction
	path := s.descendantTranscriptPathLocked(threadID)
	s.mu.Unlock()
	release = func() {
		s.mu.Lock()
		projection.readers--
		s.mu.Unlock()
	}
	if !evicted {
		return release, nil
	}
	rebuilt, err := rebuildDescendantTurns(threadID, path, eviction)
	if err != nil {
		release()
		return func() {}, err
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		defer s.mu.Unlock()
		// A resume may have rebuilt it first, and applied events since.
		if projection.turns == nil {
			rebuilt.adoptLocked(projection)
		}
		s.evictQuiescentDescendantsLocked(appResidentQuiescentDescendants)
		return nil
	})
	return release, nil
}
