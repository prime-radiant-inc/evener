package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
)

var (
	webManagedLaunchSourceIDForRef      = managedLaunchSourceIDForRef
	webSourceForThreadWithManagedLaunch = sourceForThreadWithManagedLaunch
	webEnsureThreadActionAvailable      = ensureThreadActionAvailable
	webRosterFind                       = func(r *hubcore.Roster, id string) (hubcore.LiveEntry, bool) { return r.Find(id) }
	webSourceForThread                  = sourceForThread
	webWaitForRosterMatch               = waitForRosterMatch
)

func (s *WebServer) handleSend(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, hubcore.SendMaxRequestBytes)
	var body sendRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Text == "" && len(body.Items) == 0 {
		http.Error(w, "text or items required", http.StatusBadRequest)
		return
	}
	if err := validateAppWireInputItems(body.Items); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	for i, it := range body.Items {
		if len(it.Data) > hubcore.SendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, hubcore.SendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
		}
	}
	ref := appRefFromRouteID(id)
	if !isLocalRouteID(id) {
		if _, managed := webManagedLaunchSourceIDForRef(s.cfg, ref); managed {
			source, err := webSourceForThreadWithManagedLaunch(r.Context(), s.cfg, s.sources, ref, "")
			if err != nil {
				writeSessionActionError(w, r, err)
				return
			}
			if err := webEnsureThreadActionAvailable(r.Context(), source, ref, "", "send"); err != nil {
				writeSessionActionError(w, r, err)
				return
			}
		} else {
			if err := s.ensureSessionActionAvailable(id, "send"); err != nil {
				writeSessionActionError(w, r, err)
				return
			}
		}
	}

	resolve := func(forceResume bool) (hubcore.LiveEntry, error) {
		if s.cfg.Roster == nil {
			return hubcore.LiveEntry{}, errors.New("spawner not configured")
		}
		if !forceResume {
			if le, ok := webRosterFind(s.cfg.Roster, id); ok {
				return le, nil
			}
		}
		// Resume path: spawn the daemon and wait for it to register.
		if s.cfg.Spawner == nil {
			return hubcore.LiveEntry{}, errors.New("spawner not configured")
		}
		lock := s.lockForSession(id)
		lock.Lock()
		defer lock.Unlock()
		if err := deletionFenceError(s.cfg, ref, id, ""); err != nil {
			return hubcore.LiveEntry{}, err
		}
		if le, ok := webRosterFind(s.cfg.Roster, id); ok && !forceResume {
			return le, nil
		}
		resumeReq, err := s.resumeRequestFor(id)
		if err != nil {
			return hubcore.LiveEntry{}, fmt.Errorf("resume: %w", err)
		}
		entry, err := s.cfg.Spawner.Resume(r.Context(), resumeReq)
		if err != nil {
			return hubcore.LiveEntry{}, fmt.Errorf("resume: %w", err)
		}
		le := webWaitForRosterMatch(s.cfg.Roster, id, entry.PID, 5*time.Second)
		if le.Address == "" {
			return hubcore.LiveEntry{}, errors.New("daemon not in roster after resume")
		}
		return le, nil
	}

	clientMutationID, err := identifier.NewSessionID()
	if err != nil {
		writeSessionActionError(w, r, appwire.InternalError("create turn mutation id: "+err.Error()))
		return
	}
	turnParams := appwire.TurnStartParams{Ref: ref, ClientMutationID: clientMutationID, Input: inputItemsForText(body.Text)}
	turnParams.Input = append(turnParams.Input, body.Items...)
	startTurn := func(forceResume bool) error {
		if forceResume {
			if !isLocalRouteID(id) {
				return errors.New("remote source session is not resumable by local spawner")
			}
			if _, rerr := resolve(forceResume); rerr != nil {
				return rerr
			}
		} else if _, err := webSourceForThread(s.sources, ref, ""); err != nil {
			if !isLocalRouteID(id) {
				return err
			}
			if _, rerr := resolve(forceResume); rerr != nil {
				return rerr
			}
		}
		unlockDeletionTarget := lockDeletionTarget(s.cfg, ref, id)
		defer unlockDeletionTarget()
		if err := deletionFenceError(s.cfg, ref, id, clientMutationID); err != nil {
			return err
		}
		source, err := webSourceForThread(s.sources, ref, "")
		if err != nil {
			return err
		}
		if !forceResume {
			if err := ensureThreadActionAvailable(r.Context(), source, ref, "", "send"); err != nil {
				return err
			}
		}
		_, err = source.StartTurn(r.Context(), turnParams)
		return err
	}

	if err := startTurn(false); err != nil {
		if isActionUnavailable(err) {
			writeSessionActionError(w, r, err)
			return
		}
		if strings.Contains(err.Error(), "spawner not configured") {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if !shouldResumeAfterTurnStartError(err) {
			writeSessionActionError(w, r, err)
			return
		}
		if rerr := startTurn(true); rerr != nil {
			if strings.Contains(rerr.Error(), "spawner not configured") {
				http.Error(w, rerr.Error(), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "daemon unreachable: "+err.Error()+" (resume failed: "+rerr.Error()+")", http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
}

func (s *WebServer) resumeRequestFor(id string) (hubcore.ResumeRequest, error) {
	return resumeRequestForConfig(s.cfg, id)
}

func inputItemsForText(text string) []appwire.InputItem {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []appwire.InputItem{{Type: "text", Text: text}}
}

// handleSessionAction forwards imperative actions to a daemon. Interrupt,
// clear, and shutdown remain live-only; compact can resume a known past
// session because it is a session-level maintenance action rather than an
// in-flight turn action.
func (s *WebServer) handleSessionAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ref := appRefFromRouteID(id)
	if action == "compact" {
		if !s.isLive(id) && !hubKnowsRef(s.cfg, ref) {
			http.NotFound(w, r)
			return
		}
		if err := compactThreadWithResume(r.Context(), s.cfg, s.sources, appwire.ThreadCompactStartParams{Ref: ref}); err != nil {
			writeSessionActionError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	live := s.isLive(id)
	if live {
		if err := s.ensureSessionActionAvailable(id, action); err != nil {
			writeSessionActionError(w, r, err)
			return
		}
	}
	unlockDeletionTarget := lockDeletionTarget(s.cfg, ref, "")
	defer unlockDeletionTarget()
	if err := deletionFenceError(s.cfg, ref, "", ""); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	if !live {
		http.NotFound(w, r)
		return
	}
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body sessionActionRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch action {
	case "interrupt":
		clientMutationID, mutationIDErr := identifier.NewSessionID()
		if mutationIDErr != nil {
			err = appwire.InternalError("create interrupt mutation id: " + mutationIDErr.Error())
			break
		}
		_, err = source.InterruptTurn(r.Context(), appwire.TurnInterruptParams{
			Ref:              ref,
			ClientMutationID: clientMutationID,
			ExpectedTurnID:   strings.TrimSpace(body.TurnID),
		})
	case "clear":
		_, err = source.ClearThread(r.Context(), appwire.ThreadClearParams{Ref: ref})
	case "shutdown":
		err = source.ShutdownThread(r.Context(), appwire.ThreadShutdownParams{Ref: ref})
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *WebServer) ensureSessionActionAvailable(id, action string) error {
	detail, ok := s.apiSessionDetail(id)
	if !ok {
		return appwire.Unavailable("session action is not available")
	}
	if sessionCapabilityAvailable(detail.Capabilities, action) {
		return nil
	}
	return appwire.Unavailable(action + " is not available for this session")
}

func sessionCapabilityAvailable(caps hubapi.SessionCapabilities, action string) bool {
	switch action {
	case "send":
		return caps.Send
	case "steer":
		return caps.Steer
	case "interrupt":
		return caps.Interrupt
	case "compact":
		return caps.Compact
	case "clear":
		return caps.Clear
	case "fork":
		return caps.Fork
	case "shutdown":
		return caps.Shutdown
	case "model":
		return caps.ChangeModel
	case "queue":
		return caps.Queue
	default:
		return false
	}
}

func writeSessionActionError(w http.ResponseWriter, r *http.Request, err error) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	status := http.StatusBadGateway
	if wire, ok := wireErrorFromError(err); ok {
		status = statusForWireError(wire, status)
		if info := serfErrorInfoFromData(wire.Data); info != "" {
			w.Header().Set("X-Serf-Error-Info", info)
		}
	}
	http.Error(w, err.Error(), status)
}

func isActionUnavailable(err error) bool {
	wire, ok := wireErrorFromError(err)
	return ok && wire.Code == appwire.CodeUnavailable && serfErrorInfoFromData(wire.Data) == string(appwire.ErrorActionUnavailable)
}

// validateForkRequest enforces the same defer_input + edited_message
// validation as the RPC thread/fork path so the REST endpoints stay at
// parity: the two are mutually exclusive, and a non-deferred fork requires
// edited_message.
func validateForkRequest(body forkRequest) error {
	if body.DeferInput && strings.TrimSpace(body.EditedMessage) != "" {
		return errors.New("edited_message and defer_input are mutually exclusive")
	}
	if !body.DeferInput && strings.TrimSpace(body.EditedMessage) == "" {
		return errors.New("edited_message is required")
	}
	return nil
}

func (s *WebServer) handleAPIFork(w http.ResponseWriter, r *http.Request, parentID string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body forkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateForkRequest(body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	childID, originalInput, err := s.forkSession(parentID, body)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ref := hubapi.LocalRef(childID)
	writeAPIJSON(w, http.StatusOK, hubapi.ForkResponse{
		Ref:           ref.String(),
		HostID:        ref.HostID,
		SessionID:     ref.SessionID,
		OriginalInput: originalInput,
	})
}

// forkSession forks the parent session at body.Turn. When body.DeferInput is
// set the fork carries only the entries before the turn (no replacement turn,
// so opening the fork never auto-runs) and the turn's original text is
// returned for the caller to stage for editing (issue #42).
func (s *WebServer) forkSession(parentID string, body forkRequest) (string, string, error) {
	stateDir, err := s.stateDirForSession(parentID)
	if err != nil {
		return "", "", err
	}

	var childID, originalInput string
	if body.DeferInput {
		childID, originalInput, err = agent.ForkSessionAtUserTurn(stateDir, parentID, body.Turn, body.Label)
	} else {
		childID, err = agent.ForkSession(stateDir, parentID, body.Turn, body.EditedMessage, body.Label)
	}
	if err != nil {
		return "", "", err
	}
	// Refresh past index so the new session shows up immediately in the sidebar.
	if s.cfg.Past != nil {
		_, _ = s.cfg.Past.Rebuild()
	}
	return childID, originalInput, nil
}

// stateDirForSession resolves the state dir for a session. Branches must write
// into the same project's state-dir as the parent (so they appear in the
// project tree). Past index knows the per-project state-dir; cfg.StateDir is
// the parent of all projects and would point the fork at the wrong directory.
func (s *WebServer) stateDirForSession(parentID string) (string, error) {
	var stateDir string
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(parentID); ok {
			stateDir = pe.StateDir
		}
	}
	if stateDir == "" {
		stateDir = s.cfg.StateDir
	}
	if stateDir == "" {
		return "", errors.New("state dir not resolvable for parent session")
	}
	return stateDir, nil
}

// waitForRosterMatch polls the roster until it sees a daemon with the given PID
// and session ID, or until timeout. Returns the matched hubcore.LiveEntry (Address == "" on timeout).
func waitForRosterMatch(r *hubcore.Roster, sessionID string, pid int, timeout time.Duration) hubcore.LiveEntry {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.Refresh()
		if le, ok := r.Find(sessionID); ok && le.PID == pid {
			return le
		}
		time.Sleep(150 * time.Millisecond)
	}
	return hubcore.LiveEntry{}
}

// fetchStatus reads /status from the daemon at le.Address, returning nil on any error.
func (s *WebServer) fetchStatus(le hubcore.LiveEntry) *daemonStatus {
	client := &http.Client{Timeout: 1 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+le.Address+"/status", nil) //nolint:gosec
	if err != nil {
		return nil
	}
	hubcore.SetDaemonAuthorization(req.Header, le.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	var info daemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}
	return &info
}
