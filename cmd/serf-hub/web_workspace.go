package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
)

// handleSession is the router for public /s/<id>[/<sub>] routes.
// Session fragments live under /_partials/s/... so direct navigation always
// lands in the app shell instead of a standalone workspace fragment.
func (s *WebServer) handleSession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/s/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	id = canonicalRouteID(id)
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	switch sub {
	case "":
		// Only the bare page GET (no sub-path) is a page route. Every other
		// case below — images and the POST actions — is either consumed by
		// the future SPA client directly (images) or legacy-client-only and
		// due to be replaced in a later wave (actions); neither should ever
		// see an HTML shell in place of its real response.
		if newWebEnabled() {
			serveSPAIndex(w, r, distFS())
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": "/_partials/s/" + id + "/workspace"}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "state":
		http.NotFound(w, r)
	case "details":
		http.NotFound(w, r)
	case "tasks":
		http.NotFound(w, r)
	case "send":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleSend(w, r, id)
	case "fork":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleFork(w, r, id)
	case "aside":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleAside(w, r, id)
	case "interrupt":
		s.handleSessionAction(w, r, id, "interrupt")
	case "compact":
		s.handleSessionAction(w, r, id, "compact")
	case "shutdown":
		s.handleSessionAction(w, r, id, "shutdown")
	case "clear":
		s.handleSessionAction(w, r, id, "clear")
	case "steer":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleSteer(w, r, id)
	case "queue":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleQueue(w, r, id)
	case "drain-as-steer":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleDrainAsSteer(w, r, id)
	case "promote-queued":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handlePromoteQueued(w, r, id)
	case "cancel-queued":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleCancelQueued(w, r, id)
	default:
		// /s/<id>/images/<sha> — sha-addressed image fetch for replay.
		if strings.HasPrefix(sub, "images/") {
			sha := strings.TrimPrefix(sub, "images/")
			s.handleSessionImage(w, r, id, sha)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *WebServer) workspaceDataForRender(id string) WorkspaceData {
	data := s.workspaceData(id)
	if data.ID == "" {
		return data
	}
	if data.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			data.HomeDir = home
		}
	}
	return data
}

func (s *WebServer) handleThreadDocument(w http.ResponseWriter, r *http.Request) {
	if newWebEnabled() {
		serveSPAIndex(w, r, distFS())
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/thread/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	id = canonicalRouteID(id)
	s.renderThreadDocument(w, r, id)
}

func (s *WebServer) renderThreadDocument(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceDataForRender(id)
	if data.ID == "" {
		data = WorkspaceData{
			ID:           id,
			SourceLabel:  sourceLabelFromRefText(appRefFromRouteID(id)),
			Title:        id,
			State:        "idle",
			StateLabel:   stateLabel("idle", false),
			Capabilities: s.apiSessionCapabilities(id, false),
		}
	}
	data.ShowSidebarToggle = false
	data.ThreadDocumentMode = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.threadTmpl.ExecuteTemplate(w, "thread_document", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderSessionTasks returns the session's task list as JSON. For live
// sessions it proxies the daemon's GET /tasks; for ended sessions it reads
// the persisted <StateDir>/tasks/<id>.json. A missing file or absent
// session returns an empty array (200) so the UI doesn't have to special-
// case "no tasks yet".
func (s *WebServer) renderSessionTasks(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")

	ref := appRefFromRouteID(id)
	if source, err := sourceForThread(s.sources, ref, ""); err == nil {
		resp, err := source.ListTasks(r.Context(), appwire.TaskListParams{Ref: ref})
		if err == nil {
			_ = json.NewEncoder(w).Encode(resp.Data)
			return
		}
		// fall through to disk on daemon error
	}

	if isLocalRouteID(id) && s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok && pe.StateDir != "" {
			path := filepath.Join(pe.StateDir, "tasks", id+".json")
			if data, err := os.ReadFile(path); err == nil {
				_, _ = w.Write(data)
				return
			}
		}
	}

	_, _ = w.Write([]byte("[]\n"))
}

func (s *WebServer) workspaceData(id string) WorkspaceData {
	if !isLocalRouteID(id) {
		ref := appRefFromRouteID(id)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, ref, "")
		if err != nil {
			return WorkspaceData{}
		}
		resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			return WorkspaceData{}
		}
		return workspaceDataFromAppThread(resp.Thread)
	}
	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			state := hubcore.NormalizeState(le.Status)
			data := WorkspaceData{
				ID:           id,
				SourceLabel:  "serf",
				Title:        liveTitle(id, le, s.cfg.Past),
				State:        state,
				StateLabel:   stateLabel(state, false),
				Model:        le.Model,
				WorkingDir:   le.WorkingDir,
				Capabilities: s.apiSessionCapabilities(id, true),
			}
			if status := s.fetchStatus(le); status != nil {
				if status.State != "" {
					data.State = hubcore.NormalizeState(status.State)
					data.StateLabel = stateLabel(data.State, false)
				}
				if status.Model != "" {
					data.Model = status.Model
				}
				if status.WorkingDir != "" {
					data.WorkingDir = status.WorkingDir
				}
				data.TurnCount = status.Turns
				data.WorkMillis = status.WorkMillis
				data.Usage = status.Usage
				data.ActiveTurnStartedAt = status.ActiveTurnStartedAt
				data.Cost = appwire.EstimateCost(data.Model, data.Usage)
				// Populate the context gauge here too so the lean /state poll
				// (B3) needs no separate turns fetch.
				data.ContextPercent = int(status.ContextPressure * 100)
				data.ContextWindow = status.ContextWindow
				data.ContextNumbers = formatContextNumbers(status.ContextUsed, status.ContextWindow, status.ContextRemaining)
				data.CompactContextNumbers = formatCompactContextNumbers(status.ContextUsed, status.ContextWindow)
			}
			// Branch isn't on the rendezvous entry or daemon /status — fall
			// back to the past index where the agent persists EnvInfo.
			if s.cfg.Past != nil {
				if pe, ok := s.cfg.Past.Find(id); ok {
					data.Branch = pe.Meta.EnvInfo.GitBranch
					data.Worktree = worktreeLabel(pe.Meta.WorktreePath)
					if data.WorkingDir == "" {
						data.WorkingDir = pe.Meta.EnvInfo.WorkingDir
					}
					s.fillForkLineage(&data, pe.Meta)
					s.fillSubagentLineage(&data, pe.Meta)
					s.fillObserverLink(&data, pe.Meta)
				}
			}
			if state == "ended" && s.cfg.Past != nil {
				if _, ok := s.cfg.Past.Find(id); ok {
					data.Capabilities = s.apiSessionCapabilities(id, false)
					return data
				}
			}
			data.Capabilities, data.ActiveTurnID = s.liveWorkspaceSnapshot(id, data.Capabilities)
			return data
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			state := "ended"
			if s.cfg.Roster != nil && s.cfg.Roster.IsSubagentActive(id) {
				state = "active"
			}
			data := WorkspaceData{
				ID:           id,
				SourceLabel:  "serf",
				Title:        pastTitle(pe),
				State:        state,
				StateLabel:   stateLabel(state, false),
				TurnCount:    pe.Meta.TurnCount,
				Model:        pe.Meta.Model,
				WorkingDir:   pe.Meta.EnvInfo.WorkingDir,
				Branch:       pe.Meta.EnvInfo.GitBranch,
				Worktree:     worktreeLabel(pe.Meta.WorktreePath),
				Capabilities: s.apiSessionCapabilities(id, false),
				// ActiveTurnStartedAt stays 0: archived child metadata does not
				// expose the current turn's start time, even when the parent roster
				// projects this child as active.
				WorkMillis: pe.Meta.WorkMillis,
				Usage:      serfUsageFromCumulative(pe.Meta.CumulativeUsage),
			}
			data.Cost = appwire.EstimateCost(data.Model, data.Usage)
			s.fillForkLineage(&data, pe.Meta)
			s.fillSubagentLineage(&data, pe.Meta)
			s.fillObserverLink(&data, pe.Meta)
			return data
		}
	}
	return WorkspaceData{}
}

// serfUsageFromCumulative maps a persisted SessionMeta.CumulativeUsage
// (an ended session's WS2 token totals) to the wire appwire.SerfUsage shown
// in its workspace data. Returns nil when every total (including
// CacheReadTokens) is zero — a session that never accumulated usage, or a
// meta written before WS2 — so the usage cluster hides rather than rendering
// ↑0 ↓0 (mirrors serfUsageFromLLM in cmd/serf/serve.go).
func serfUsageFromCumulative(u schema.CumulativeUsage) *appwire.SerfUsage {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &appwire.SerfUsage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens,
		TotalTokens:     u.TotalTokens,
	}
}

func (s *WebServer) liveWorkspaceCapabilities(id string, fallback hubapi.SessionCapabilities) hubapi.SessionCapabilities {
	caps, _ := s.liveWorkspaceSnapshot(id, fallback)
	return caps
}

func (s *WebServer) liveWorkspaceSnapshot(id string, fallback hubapi.SessionCapabilities) (hubapi.SessionCapabilities, string) {
	ref := appRefFromRouteID(id)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, ref, "")
	if err != nil {
		return fallback, ""
	}
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
	if err != nil {
		return fallback, ""
	}
	caps := hubCapabilitiesFromAppwire(resp.Thread.Serf.Capabilities)
	caps.Resume = fallback.Resume
	return caps, activeTurnIDFromAppwireThread(resp.Thread)
}

// fillForkLineage populates the WorkspaceData fork-banner fields for the
// preserved-original side of a fork. The dim original is the meta with a
// non-empty ForkLabel; the new branch is the meta whose ParentSessionID
// equals this session's ID. ForkOfTitle is best-effort — if the new branch
// isn't in the past index, we leave it empty and the template falls back to
// "fork at turn N".
func (s *WebServer) fillForkLineage(data *WorkspaceData, m schema.SessionMeta) {
	if m.ForkLabel == "" {
		return
	}
	data.ForkLabel = m.ForkLabel
	data.DivergenceTurn = m.DivergenceTurn
	if s.cfg.Past == nil {
		return
	}
	for _, candidate := range s.cfg.Past.AllMetas() {
		if candidate.ParentSessionID == m.ID && !candidate.IsSubagent && candidate.ForkLabel == "" {
			data.ForkOfTitle = schema.SessionDisplayName(candidate)
			if data.ForkOfTitle == "" {
				data.ForkOfTitle = hubcore.ShortID(candidate.ID)
			}
			return
		}
	}
}

// fillSubagentLineage populates the WorkspaceData parent-breadcrumb fields for a
// subagent's workspace (mockup #9). Without this, opening a subagent via
// "view →" was a one-way hard nav to /s/<ref> with no way back. The crumb links
// to the parent's workspace; ParentTitle is the parent's display name (best-
// effort from the past index, falling back to a short id).
func (s *WebServer) fillSubagentLineage(data *WorkspaceData, m schema.SessionMeta) {
	if !m.IsSubagent || m.ParentSessionID == "" {
		return
	}
	data.ParentRouteID = canonicalRouteID(m.ParentSessionID)
	title := ""
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(m.ParentSessionID); ok {
			title = schema.SessionDisplayName(pe.Meta)
		}
	}
	if title == "" {
		title = hubcore.ShortID(m.ParentSessionID)
	}
	data.ParentTitle = title
}

// fillObserverLink populates WorkspaceData.ObserverRouteIDs with this worker's
// observer subagents — live OR ended. The renderer auto-opens each as a side
// pane beside the worker (observers auto-open by design). Ended observers are
// included on purpose so the relationship surfaces on sessions already on disk;
// a worker with many past observers is bounded on the client by the side-pane
// cap + closed-pane suppression (a dismissed observer pane stays shut).
//
// Two sources union (deduped): the forward SessionMeta.ObservedBy stamp (set on
// fresh local watches) and the durable grant-history index built from every
// local session's jobs.jsonl watch-read-grants during the past-index rebuild
// (PastIndex.ObserversOf). The stamp is empty on existing data, so the grant
// history is what makes auto-open work on the sessions already on disk. Local
// sources only: both sources derive from local meta/jobstore state, so remote/
// codex workspaces never reach here with an observer.
func (s *WebServer) fillObserverLink(data *WorkspaceData, m schema.SessionMeta) {
	var historical []string
	if s.cfg.Past != nil {
		historical = s.cfg.Past.ObserversOf(m.ID)
	}
	if len(m.ObservedBy) == 0 && len(historical) == 0 {
		return
	}
	var observers []string
	seen := make(map[string]bool, len(m.ObservedBy)+len(historical))
	for _, observerID := range append(append([]string(nil), m.ObservedBy...), historical...) {
		if observerID == "" || seen[observerID] {
			continue
		}
		seen[observerID] = true
		observers = append(observers, canonicalRouteID(observerID))
	}
	data.ObserverRouteIDs = observers
}
