package server

import (
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
	var resident []*appDescendantProjection
	for threadID, projection := range s.appDescendants {
		if projection.turns != nil && projection.quiescent() && s.descendantTranscriptPathLocked(threadID) != "" {
			resident = append(resident, projection)
		}
	}
	if len(resident) <= keep {
		return
	}
	sort.Slice(resident, func(i, j int) bool { return resident[i].lastUsed < resident[j].lastUsed })
	// A pinned descendant still counts against keep; the next unpinned one
	// goes in its place.
	excess := len(resident) - keep
	for _, projection := range resident {
		if excess == 0 {
			return
		}
		if projection.readers > 0 {
			continue
		}
		excess--
		projection.turns.mu.Lock()
		projection.evictedNextEntry = projection.turns.nextLiveEntry
		projection.turns.mu.Unlock()
		projection.turns = nil
	}
}

// descendantTurnsFromTranscript projects a descendant's transcript into a new
// snapshot whose live entries continue at or after floor. It reads the whole
// file, so it never runs under the subscription cut.
//
// The rebuilt snapshot cannot reproduce the live one it replaces: live-only
// items (prompt_loaded, round_timings) are never persisted, so the same
// position names a different item after a rebuild. A new snapshot mints a new
// cursor incarnation, so a cursor from before the eviction fails as stale and
// the client re-reads rather than paging the wrong items.
func descendantTurnsFromTranscript(threadID, path string, floor uint64) (*appTurnSnapshot, int, error) {
	persisted, err := appTurnProjectionFromTranscriptFile(path)
	if err != nil {
		return nil, 0, err
	}
	snapshot := &appTurnSnapshot{threadID: threadID}
	snapshot.Seed(appTurnSeed{Turns: persisted.turns, NextEntry: max(persisted.nextEntry, floor)})
	return snapshot, persisted.persistedEntries, nil
}

// installDescendantTurnsLocked gives a descendant with no resident snapshot one
// seeded from its transcript: on its first observation, and when an event
// arrives for an evicted descendant. The projector is fenced above the
// transcript's persisted turn ids either way, so a live turn cannot reuse one.
func (s *Server) installDescendantTurnsLocked(threadID string, projection *appDescendantProjection) {
	projection.turns = &appTurnSnapshot{threadID: threadID, nextLiveEntry: projection.evictedNextEntry}
	path := s.descendantTranscriptPathLocked(threadID)
	if path == "" {
		return
	}
	snapshot, persistedEntries, err := descendantTurnsFromTranscript(threadID, path, projection.evictedNextEntry)
	if err != nil {
		return
	}
	projection.turns = snapshot
	projection.projector.SeedPersistedTurns(persistedEntries)
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
	floor := projection.evictedNextEntry
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
	snapshot, persistedEntries, err := descendantTurnsFromTranscript(threadID, path, floor)
	if err != nil {
		release()
		return func() {}, err
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		defer s.mu.Unlock()
		// A resume may have rebuilt it first, and applied events since.
		if projection.turns == nil {
			projection.turns = snapshot
			projection.projector.SeedPersistedTurns(persistedEntries)
		}
		s.evictQuiescentDescendantsLocked(appResidentQuiescentDescendants)
		return nil
	})
	return release, nil
}
