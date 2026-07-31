package main

import (
	"errors"
	"net/http"
	"strings"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

// handleAPISessionDelete removes one ended or confirmed-crashed LOCAL session
// (kata n15j) without touching any project sibling. It is the single-target
// counterpart of handleAPIProjectDelete (web_api_project_delete.go) and
// deliberately reuses that file's machinery rather than re-implementing it:
// the same per-session lock + crash-vs-live predicate + API-log ownership
// reservation (acquireProjectDeletionOwnership, which already applies kata
// 8at6's projectSessionLive fix), and the same raw artifact removal
// (cleanupProjectDeletionTargetAndDecisions, which wraps
// cleanupProjectDeletionTarget). A whole project and a lone session are thus
// deleted by one cleanup contract, not two.
//
// Never offered for a remote-source thread: the id must resolve to a local
// route (isLocalRouteID) before anything else runs. Never infers a
// filesystem path from the caller-supplied id either: the state directory
// always comes from the trusted Past index (pe.StateDir), never from string
// concatenation over the id.
//
// The response envelope mirrors handleAPIProjectDelete's own
// {"deleted":[...],"skipped":[...]} shape for a target set of at most one,
// so the frontend's existing ProjectDeleteResult parsing/toast logic applies
// unchanged. A session no longer present in the Past index (never existed,
// or a previous call already deleted it) reports a clean no-op rather than
// an error, matching handleAPIProjectDelete's own "nothing to do" path -
// that is what keeps a repeated delete idempotent.
func (s *WebServer) handleAPISessionDelete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !isLocalRouteID(id) {
		writeAPIError(w, http.StatusBadRequest, "only local sessions can be deleted")
		return
	}
	threadID := canonicalRouteID(id)
	if err := identifier.ValidateSessionID(threadID); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid session ID: "+err.Error())
		return
	}
	if s.cfg.Past == nil {
		writeAPIError(w, http.StatusInternalServerError, "past index not configured")
		return
	}
	pe, ok := s.cfg.Past.Find(threadID)
	if !ok {
		writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "skipped": []projectDeleteSkip{}})
		return
	}

	target := hubcore.DeletionTarget{Ref: localAppRef(threadID), ThreadID: threadID}
	record := hubcore.DeletionRecord{Targets: []hubcore.DeletionTarget{target}}
	stateDirs := map[string]string{threadID: pe.StateDir}
	release, ownerErr := s.acquireProjectDeletionOwnership(record, stateDirs)
	if ownerErr != nil {
		var skipped []projectDeleteSkip
		if errors.Is(ownerErr.Err, llm.ErrAPILogTargetLocked) || ownerErr.Live {
			skipped = appendProjectDeleteLiveSkip(nil, threadID)
		} else {
			skipped = []projectDeleteSkip{{ID: threadID, Reason: ownerErr.Error()}}
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "skipped": skipped})
		return
	}
	defer release()

	deleted, skip, decisionErrors := s.cleanupProjectDeletionTargetAndDecisions(pe.StateDir, threadID)
	if !deleted {
		writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "skipped": []projectDeleteSkip{*skip}})
		return
	}

	rebuilt := false
	if s.cfg.Past != nil {
		var err error
		rebuilt, err = rebuildProjectDeletionPast(s.cfg.Past)
		if err != nil {
			decisionErrors = append(decisionErrors, "past index rebuild error: "+err.Error())
		}
	}
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	if !rebuilt {
		notifyTreeChanged(s.appRPC)
	}
	if len(decisionErrors) > 0 {
		writeAPIError(w, http.StatusInternalServerError, strings.Join(decisionErrors, "; "))
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{threadID}, "skipped": []projectDeleteSkip{}})
}
