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
		if _, managed := managedLaunchSourceIDForRef(s.cfg, ref); managed {
			source, err := sourceForThreadWithManagedLaunch(r.Context(), s.cfg, s.sources, ref, "")
			if err != nil {
				writeSessionActionError(w, r, err)
				return
			}
			if err := ensureThreadActionAvailable(r.Context(), source, ref, "", "send"); err != nil {
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
			if le, ok := s.cfg.Roster.Find(id); ok {
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
		if le, ok := s.cfg.Roster.Find(id); ok && !forceResume {
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
		le := waitForRosterMatch(s.cfg.Roster, id, entry.PID, 5*time.Second)
		if le.Address == "" {
			return hubcore.LiveEntry{}, errors.New("daemon not in roster after resume")
		}
		return le, nil
	}

	turnParams := appwire.TurnStartParams{Ref: ref, Input: inputItemsForText(body.Text)}
	turnParams.Input = append(turnParams.Input, body.Items...)
	startTurn := func(forceResume bool) error {
		if forceResume {
			if !isLocalRouteID(id) {
				return errors.New("remote source session is not resumable by local spawner")
			}
			if _, rerr := resolve(forceResume); rerr != nil {
				return rerr
			}
		} else if _, err := sourceForThread(s.sources, ref, ""); err != nil {
			if !isLocalRouteID(id) {
				return err
			}
			if _, rerr := resolve(forceResume); rerr != nil {
				return rerr
			}
		}
		source, err := sourceForThread(s.sources, ref, "")
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

// handleSteer forwards a steering message to the live daemon for the given
// session. Steer requires the session to already have a live daemon — we do
// not auto-resume on steer, since steering an ended session has no useful
// meaning (the model isn't running).
func (s *WebServer) handleSteer(w http.ResponseWriter, r *http.Request, id string) {
	var body steerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, "steer"); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := source.SteerTurn(r.Context(), appwire.TurnSteerParams{Ref: ref, ExpectedTurnID: strings.TrimSpace(body.TurnID), Input: inputItemsForText(body.Text)}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleQueue forwards a turn/queue request to the live daemon for the given
// session. Unlike /send, queueing requires the session to be processing — the
// daemon returns Conflict when idle, which we surface as 409.
func (s *WebServer) handleQueue(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, hubcore.SendMaxRequestBytes)
	var body queueRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
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
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, "queue"); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := source.QueueTurn(r.Context(), appwire.TurnQueueParams{Ref: ref, Input: append(inputItemsForText(body.Text), body.Items...)}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDrainAsSteer forwards a turn/drainAsSteer request (kata 0bq1 force-
// steer combined action). Drains the daemon's input queue into a single
// STEERING injection on the in-flight turn. Rides on the Steer capability;
// the daemon returns Conflict when idle or when the queue is empty.
func (s *WebServer) handleDrainAsSteer(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, hubcore.SendMaxRequestBytes)
	var body drainAsSteerRequest
	// Empty bodies are valid (legacy classic drain). json.NewDecoder errors
	// only when the body has content that can't be parsed — silently
	// tolerate EOF / empty so the no-body path keeps working.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			// Only reject if the body wasn't empty. io.EOF from a zero
			// Content-Length-but-present body is normal; surface anything
			// else as a 400.
			if err.Error() != "EOF" {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
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
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, "steer"); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := source.DrainAsSteer(r.Context(), appwire.TurnDrainAsSteerParams{
		Ref:   ref,
		Input: append(inputItemsForText(strings.TrimSpace(body.Text)), body.Items...),
	}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, action); err != nil {
		writeSessionActionError(w, r, err)
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
		err = source.InterruptTurn(r.Context(), appwire.TurnInterruptParams{Ref: ref, ExpectedTurnID: strings.TrimSpace(body.TurnID)})
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

func (s *WebServer) handleFork(w http.ResponseWriter, r *http.Request, parentID string) {
	var body forkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	childID, err := s.forkSession(parentID, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"child_session_id": childID}) //nolint:errcheck
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

	childID, err := s.forkSession(parentID, body)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ref := hubapi.LocalRef(childID)
	writeAPIJSON(w, http.StatusOK, hubapi.RefResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func (s *WebServer) forkSession(parentID string, body forkRequest) (string, error) {
	// Resolve the state dir for the parent session. Forks must write into
	// the same project's state-dir as the parent (so they appear in the
	// project tree). Past index knows the per-project state-dir; cfg.StateDir
	// is the parent of all projects and would point ForkSession at the wrong
	// directory.
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

	childID, err := agent.ForkSession(stateDir, parentID, body.Turn, body.EditedMessage, body.Label)
	if err != nil {
		return "", err
	}
	// Refresh past index so the new session shows up immediately in the sidebar.
	if s.cfg.Past != nil {
		_ = s.cfg.Past.Rebuild()
	}
	return childID, nil
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
