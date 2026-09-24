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
	// transcriptBytes is the transcript's length when the evicted snapshot last
	// settled (settleDescendantLocked). The snapshot then held exactly what that
	// prefix persisted, and a session persists before it emits, so anything
	// past it arrives again as live events after the rebuild. Rebuilding from
	// the whole file would show those twice.
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

// transcriptSize is the length of the transcript at path, or zero when there
// is none to rebuild from.
func transcriptSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// settleDescendantLocked records that a descendant's own event left it
// quiescent, with transcriptBytes its transcript's length sampled in that
// event's commit, then runs an eviction pass. A snapshot whose rebuild failed
// is evicted outright, so the next read rebuilds the history it lacks.
//
// The length must come from the descendant's own commit. A session persists
// before it emits, and it emits synchronously, so while its event is being
// committed it can append nothing: the length is exactly what the snapshot has
// applied. Sampled anywhere else, it can include an entry the session has
// persisted but not yet emitted, which the rebuild would project and the event
// would then deliver again.
func (s *Server) settleDescendantLocked(projection *appDescendantProjection, transcriptBytes int64) {
	projection.eviction.transcriptBytes = transcriptBytes
	s.touchDescendantLocked(projection)
	if projection.rebuildFailed && projection.readers == 0 && transcriptBytes > 0 {
		evictDescendantTurnsLocked(projection)
	}
	s.evictQuiescentDescendantsLocked(appResidentQuiescentDescendants)
}

// evictDescendantTurnsLocked drops a resident descendant's turn snapshot,
// recording the entry its rebuild must continue from.
func evictDescendantTurnsLocked(projection *appDescendantProjection) {
	projection.turns.mu.Lock()
	projection.eviction.nextEntry = projection.turns.nextLiveEntry
	projection.turns.mu.Unlock()
	projection.turns = nil
}

// evictQuiescentDescendantsLocked drops the turn snapshots of all but the keep
// most recently used quiescent descendants. It never evicts a descendant with
// a turn in flight, one a read has pinned, or one with no settled transcript
// to rebuild from.
//
// Callers hold s.mu inside a projection commit: a commit captures snapshot
// pointers under s.mu and applies to them after releasing it, so an eviction
// outside the gate could strand an apply on a snapshot nobody reads.
func (s *Server) evictQuiescentDescendantsLocked(keep int) {
	var resident []*appDescendantProjection
	for _, projection := range s.appDescendants {
		if projection.turns != nil && projection.quiescent() {
			resident = append(resident, projection)
		}
	}
	if len(resident) <= keep {
		return
	}
	sort.Slice(resident, func(i, j int) bool { return resident[i].lastUsed < resident[j].lastUsed })
	// A pinned descendant still counts against keep; the next evictable one
	// goes in its place.
	excess := len(resident) - keep
	for _, projection := range resident {
		if excess == 0 {
			return
		}
		if projection.readers > 0 || projection.eviction.transcriptBytes == 0 {
			continue
		}
		excess--
		evictDescendantTurnsLocked(projection)
	}
}

// rebuiltDescendantTurns is a descendant snapshot rebuilt from its transcript
// for eviction, and the persisted turn count its projector must be fenced
// above; or the error that kept it from being rebuilt.
type rebuiltDescendantTurns struct {
	eviction         appTurnsEviction
	snapshot         *appTurnSnapshot
	persistedEntries int
	err              error
}

// rebuildDescendantTurns projects a descendant's transcript into a new
// snapshot: the prefix the evicted snapshot held, or the whole file for a
// descendant never seen before (a zero eviction). It returns nil when there is
// no transcript to rebuild from. It reads the file, so callers run it outside
// the projection gate but for installDescendantTurnsLocked's rare fallback.
//
// The rebuilt snapshot cannot reproduce the live one it replaces: live-only
// items (prompt_loaded, round_timings) are never persisted, so the same
// position names a different item after a rebuild. A new snapshot mints a new
// cursor incarnation, so a cursor from before the eviction fails as stale and
// the client re-reads rather than paging the wrong items.
func rebuildDescendantTurns(threadID, path string, eviction appTurnsEviction) *rebuiltDescendantTurns {
	if path == "" {
		return nil
	}
	persisted, err := appTurnProjectionFromTranscriptPrefix(path, eviction.transcriptBytes)
	if err != nil {
		return &rebuiltDescendantTurns{eviction: eviction, err: err}
	}
	snapshot := &appTurnSnapshot{threadID: threadID}
	snapshot.Seed(appTurnSeed{Turns: persisted.turns, NextEntry: max(persisted.nextEntry, eviction.nextEntry)})
	return &rebuiltDescendantTurns{eviction: eviction, snapshot: snapshot, persistedEntries: persisted.persistedEntries}
}

// prepareDescendantTurns rebuilds, outside the projection gate, the snapshot
// the commit of an event for threadID will need: when the descendant of
// ownerThreadID is new or evicted. It returns nil when there is nothing to
// rebuild.
func (s *Server) prepareDescendantTurns(ownerThreadID, threadID string) *rebuiltDescendantTurns {
	s.mu.RLock()
	projection := s.appDescendants[threadID]
	if s.appThreadID != ownerThreadID || (projection != nil && projection.turns != nil) {
		s.mu.RUnlock()
		return nil
	}
	var eviction appTurnsEviction
	if projection != nil {
		eviction = projection.eviction
	}
	path := s.descendantTranscriptPathLocked(threadID)
	s.mu.RUnlock()
	return rebuildDescendantTurns(threadID, path, eviction)
}

// installDescendantTurnsLocked gives a descendant with no resident snapshot one
// seeded from its transcript: on its first observation, and when an event or a
// read arrives for an evicted descendant. It adopts rebuilt when that was
// rebuilt for the current eviction. When the descendant was installed and
// evicted again since, which takes a racing read or event, it rebuilds here,
// under the gate, rather than install a prefix that may miss what the snapshot
// applied in between. With nothing to rebuild from it starts empty; when the
// rebuild fails it starts empty and marked so it is evicted as it settles.
func (s *Server) installDescendantTurnsLocked(threadID string, projection *appDescendantProjection, rebuilt *rebuiltDescendantTurns) {
	if rebuilt == nil || rebuilt.eviction != projection.eviction {
		rebuilt = rebuildDescendantTurns(threadID, s.descendantTranscriptPathLocked(threadID), projection.eviction)
	}
	projection.rebuildFailed = rebuilt != nil && rebuilt.err != nil
	if rebuilt != nil && rebuilt.err == nil {
		// Fence the projector above the transcript's persisted turn ids, so a
		// live turn cannot reuse one.
		projection.turns = rebuilt.snapshot
		projection.projector.SeedPersistedTurns(rebuilt.persistedEntries)
		return
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
	rebuilt := rebuildDescendantTurns(threadID, path, eviction)
	if rebuilt != nil && rebuilt.err != nil {
		release()
		return func() {}, rebuilt.err
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		defer s.mu.Unlock()
		// A resume may have rebuilt it first, and applied events since.
		if projection.turns == nil {
			s.installDescendantTurnsLocked(threadID, projection, rebuilt)
		}
		s.evictQuiescentDescendantsLocked(appResidentQuiescentDescendants)
		return nil
	})
	return release, nil
}
