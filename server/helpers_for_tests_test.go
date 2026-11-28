package server

import (
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// recordTranscriptEntry forwards rec to threadID's registered history.
func (s *Server) recordTranscriptEntry(threadID string, rec transcript.Record) {
	if h := s.appHistories.get(threadID); h != nil {
		h.recorded(rec)
	}
}

// appThreadReadSnapshot answers a read with no subscription, ignoring errors.
func (s *Server) appThreadReadSnapshot(params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	response, _ := s.appThreadReadSnapshotChecked(params)
	return response
}

// appThreadReadSnapshotChecked answers a read with no subscription.
func (s *Server) appThreadReadSnapshotChecked(params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return s.finishAppThreadRead(params, s.appThreadReadCutForTarget(params, s.appThreadIDForRead(params)))
}

// before pages items before params.Cursor or params.Before without returning the execution frontier.
func (h *threadHistory) before(threadRef string, params appwire.ThreadTurnsListParams) (turns []appwire.Turn, olderCursor string, snapshot appwire.SnapshotIdentity, err error) {
	turns, olderCursor, snapshot, _, err = h.beforeWithFrontier(threadRef, params)
	return
}

// drop detaches and closes threadID's history and overlay if registered.
func (r *threadHistories) drop(threadID string) {
	if h := r.detach(threadID); h != nil {
		closeHistories([]*threadHistory{h})
	}
}
