package server

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/appserver"
)

func (s *Server) AppServer() *appserver.Server {
	return s.appServer
}

// PreparedAppIdentity is a validated AppWire identity nothing has published
// yet: the thread, its ref and event projector, and the transcript its history
// is projected from. Every fallible step -- header validation -- happens while
// building one, so installing it cannot fail and cannot leave the server
// half-switched.
type PreparedAppIdentity struct {
	sourceID       string
	threadID       string
	ref            string
	projector      *appprojector.AppEventProjector
	transcriptPath string
	recordedLength int64
	bootGeneration string
}

// WithBootGeneration returns the identity served at bootGeneration, the
// daemon's boot counter for the session (schema.NextBootGeneration, as
// appwire.BootGeneration spells it). Every history message about the thread
// and its descendants carries it.
func (p PreparedAppIdentity) WithBootGeneration(bootGeneration string) PreparedAppIdentity {
	p.bootGeneration = bootGeneration
	return p
}

// WithRecordedLength returns the identity with the length its session's
// transcript writer has recorded (Session.TranscriptRecordedLength): the
// thread's history covers those bytes from the start, and the recorded-entry
// hook serve installs extends it from there. Without it, the history covers
// the file up to its first hooked entry.
func (p PreparedAppIdentity) WithRecordedLength(length int64) PreparedAppIdentity {
	p.recordedLength = length
	return p
}

// PrepareAppIdentity validates transcriptPath's header against threadID and
// builds the matching event projector. It touches no Server state, so a caller
// can abandon the result on any later failure.
//
// An empty transcript path gives the thread no history. A transcript that does
// not exist yet is not an error: the session simply has nothing recorded. A
// transcript whose header names a DIFFERENT session is an error -- serving one
// thread from another thread's history would publish a conversation that never
// happened.
func PrepareAppIdentity(sourceID, threadID, transcriptPath string) (PreparedAppIdentity, error) {
	if sourceID == "" {
		sourceID = "local"
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	return PrepareAppIdentityForRef(sourceID, threadID, ref, transcriptPath)
}

// PrepareAppIdentityForRef is PrepareAppIdentity for a replacement that keeps
// the same stable workspace ref while advancing the live session instance.
// The projector must use that ref too: otherwise the first event after clear
// would be published to the replacement session's derived ref and existing
// subscribers would miss it.
func PrepareAppIdentityForRef(sourceID, threadID, ref, transcriptPath string) (PreparedAppIdentity, error) {
	if sourceID == "" {
		sourceID = "local"
	}
	if strings.TrimSpace(threadID) == "" {
		return PreparedAppIdentity{}, errors.New("thread id is required")
	}
	parsedRef, err := appwire.ParseRef(strings.TrimSpace(ref))
	if err != nil {
		return PreparedAppIdentity{}, fmt.Errorf("invalid app identity ref: %w", err)
	}
	if parsedRef.SourceID != sourceID {
		return PreparedAppIdentity{}, fmt.Errorf("app identity ref belongs to source %s, not %s", parsedRef.SourceID, sourceID)
	}
	path := strings.TrimSpace(transcriptPath)
	if path != "" {
		header, err := transcriptHeader(path, transcript.DefaultMaxLineBytes)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Nothing is recorded: the thread has no history to serve.
			path = ""
		case err != nil:
			return PreparedAppIdentity{}, fmt.Errorf("transcript %s: %w", path, err)
		case header.SessionID != "" && header.SessionID != threadID:
			return PreparedAppIdentity{}, fmt.Errorf("transcript %s belongs to session %s, not %s", path, header.SessionID, threadID)
		}
	}
	return PreparedAppIdentity{
		sourceID:       sourceID,
		threadID:       threadID,
		ref:            parsedRef.String(),
		projector:      appprojector.NewAppEventProjector(threadID, parsedRef.String()),
		transcriptPath: path,
	}, nil
}

// ReplaceAppIdentity publishes a prepared identity. It runs inside one
// projection commit so no notification can cross between the old authority and
// the new one, and it cannot fail: preparation already did everything that
// could.
//
// activate, when non-nil, runs first and must itself be infallible -- it is
// where a caller swaps whatever it owns alongside the daemon's identity (the
// live session on clear).
//
// The new root's history replaces the old root's and every descendant's. A
// replaced history may still be finishing a projection that waits on this
// commit, so it is detached here and closed after the commit; what it would
// publish meanwhile is for a thread no longer served and is dropped.
//
// Replacing a different, non-empty identity closes the old thread's stream with
// one thread/closed record targeted at the OLD thread and ref. That record is
// recorded through the notifier so it takes the same ordered delivery path as
// every other notification: it describes the thread that just ended. Under the
// same ref, a resync carrying the new history's epoch tells subscribers to
// replace what they hold; the new history starts one epoch past the old one so
// the ref's epochs only increase.
func (s *Server) ReplaceAppIdentity(prepared PreparedAppIdentity, activate func()) {
	var detached []*threadHistory
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		if activate != nil {
			activate()
		}
		s.mu.Lock()
		oldSourceID, oldThreadID := s.appSourceID, s.appThreadID
		oldRef := s.appRef
		if oldRef == "" && oldThreadID != "" {
			oldSource := oldSourceID
			if oldSource == "" {
				oldSource = "local"
			}
			oldRef = appwire.Ref{SourceID: oldSource, ThreadID: oldThreadID}.String()
		}
		newRef := prepared.ref
		if newRef == "" {
			newSource := prepared.sourceID
			if newSource == "" {
				newSource = "local"
			}
			newRef = appwire.Ref{SourceID: newSource, ThreadID: prepared.threadID}.String()
		}
		var epoch uint64
		if old := s.appHistories.get(oldThreadID); old != nil && oldRef == newRef && oldThreadID != prepared.threadID {
			epoch = old.Epoch() + 1
		}
		s.appSourceID = prepared.sourceID
		s.appThreadID = prepared.threadID
		s.appBootGeneration = prepared.bootGeneration
		// The descendant hook's view of the served root moves before any
		// history is detached, and the registry re-checks the owner when it
		// creates one, so an old tree's hook cannot recreate a history for it.
		s.storeDescendantHistorySourceLocked()
		detached = s.appHistories.detachExcept(prepared.threadID)
		s.appRef = newRef
		s.appProjector = prepared.projector
		var history *threadHistory
		if prepared.transcriptPath != "" {
			history = s.ensureHistory(prepared.threadID, newRef, prepared.transcriptPath, prepared.recordedLength, epoch, prepared.bootGeneration)
		}
		oldDescendantIDs := make([]string, 0, len(s.appDescendants))
		for threadID := range s.appDescendants {
			oldDescendantIDs = append(oldDescendantIDs, threadID)
		}
		s.appDescendants = make(map[string]*appDescendantProjection)
		s.appTaskPublications = make(map[string]taskPublicationCursor)
		s.appActiveTurnID = ""
		s.appTurnEndStated = false
		s.appPendingStableTurnID = ""
		s.appDeferredTerminalNotifications = nil
		s.appHeldSettledEffect = nil
		s.appPushedFailedToolCalls = nil
		// The envelope describes the session that just stopped being this
		// daemon's session, so it is replaced in the SAME commit as the identity
		// it belongs to. Zeroing the whole struct rather than clearing named
		// fields is what makes that structural: a field added to threadEnvelope
		// later is reset here for free, and cannot survive an identity change by
		// being forgotten. The caller re-seeds with RefreshThreadEnvelope once
		// the replacement session is the live one.
		s.appEnvelope = threadEnvelope{}
		s.appActivity.restart()
		s.status.SessionID = prepared.threadID
		s.mu.Unlock()

		if oldThreadID == "" || oldThreadID == prepared.threadID {
			return nil
		}
		if oldSourceID == "" {
			oldSourceID = "local"
		}
		var notifications []appserver.SequencedNotification
		if oldRef == newRef {
			resync := appwire.ThreadResyncParams{ThreadID: prepared.threadID, Ref: newRef, BootGeneration: prepared.bootGeneration}
			if history != nil {
				resync.Epoch = history.Epoch()
			}
			notifications = []appserver.SequencedNotification{s.appNotifier.Record(newRef, appwire.NotifyEvenerThreadResync, resync)}
		} else {
			notifications = []appserver.SequencedNotification{s.appNotifier.Record(oldRef, appwire.NotifyThreadClosed, appwire.ThreadClosedParams{
				ThreadID: oldThreadID,
				Ref:      oldRef,
				Reason:   "replaced",
			})}
		}
		sort.Strings(oldDescendantIDs)
		for _, threadID := range oldDescendantIDs {
			notifications = append(notifications, s.appNotifier.Record(threadID, appwire.NotifyThreadClosed, appwire.ThreadClosedParams{
				ThreadID: threadID,
				Ref:      appwire.Ref{SourceID: oldSourceID, ThreadID: threadID}.String(),
				Reason:   "replaced",
			}))
		}
		return notifications
	})
	closeHistories(detached)
}

// SetAppIdentity installs an identity with no history. Production serve
// prepares from the session's transcript instead; this is the shorthand for a
// thread that has none. It runs the same validation, so an identity
// PrepareAppIdentity would reject is not installed through this door either --
// it is rejected here and nothing changes.
func (s *Server) SetAppIdentity(sourceID, threadID string) {
	prepared, err := PrepareAppIdentity(sourceID, threadID, "")
	if err != nil {
		return
	}
	s.ReplaceAppIdentity(prepared, nil)
}

// SetDescendantTranscriptPathFunc installs the resolver that names a
// descendant's transcript, which its history is projected from (ledger
// #110/#111). fn may return "" for a thread with no backing transcript; nil
// gives descendants no history. fn must not block or take locks: the
// descendants' recorded-entry hook calls it under a transcript append lock.
func (s *Server) SetDescendantTranscriptPathFunc(fn func(threadID string) string) {
	s.mu.Lock()
	s.appDescendantTranscriptPathFunc = fn
	s.storeDescendantHistorySourceLocked()
	s.mu.Unlock()
}

// SetDescendantLiveWatchesFunc installs the resolver the thread LIST path
// consults to sample the live watches that belong on each returned row -- the
// root's row as well as a descendant's. Like SetDescendantTranscriptPathFunc it
// is the appwire server's reach into the agent tree across the
// delegate-controller boundary; unlike it, fn answers a whole page at once,
// because the list path holds every row ID before it samples any of them and a
// per-ID lookup searched the agent's live tree once per row.
//
// fn's answer carries an entry only for an ID it can answer for. An absent ID
// leaves that row's cached projection in place, exactly as the per-ID nil did; a
// present entry with an empty non-nil slice is a real answer (that row has no
// watches), so a watch cleared since the last diagnostics refresh leaves the
// row. nil disables the projection entirely (the historical behavior, where only
// the root's cached envelope is shown). The single thread READ path deliberately
// does not consult this seam: it runs under the subscription cut and must stay
// cheap (see appThreadForID).
func (s *Server) SetDescendantLiveWatchesFunc(fn func(threadIDs []string) map[string][]agent.WatchStatusInfo) {
	s.mu.Lock()
	s.appDescendantLiveWatchesFunc = fn
	s.mu.Unlock()
}

// SetSubagentTallyFunc installs the seam the thread LIST path reads the root
// session's subagent tally through (S3). Like SetDescendantLiveWatchesFunc it
// reaches across the delegate-controller boundary, so the list calls it after
// releasing s.mu. fn reports false for a session with no delegate tree of its
// own; nil disables the tally.
func (s *Server) SetSubagentTallyFunc(fn func() (appwire.SubagentTally, bool)) {
	s.mu.Lock()
	s.appSubagentTallyFunc = fn
	s.mu.Unlock()
}
