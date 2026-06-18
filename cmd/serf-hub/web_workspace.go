package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	case "meta":
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

func (s *WebServer) renderWorkspacePartial(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceData(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	if data.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			data.HomeDir = home
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceTmpl.ExecuteTemplate(w, "workspace", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderDetailsPanel returns a side-panel with the session's verbose
// metadata: full session id, working dir, branch + sha, model, turn count,
// last input tokens, and (for forks) the parent session id and divergence
// turn. Triggered by clicking the "details" link in the workspace header.
func (s *WebServer) renderDetailsPanel(w http.ResponseWriter, r *http.Request, id string) {
	type detailsRow struct{ Label, Value string }
	var rows []detailsRow
	rows = append(rows,
		detailsRow{"source", sourceLabelFromRefText(appRefFromRouteID(id))},
		detailsRow{"session id", id},
	)

	addMeta := func(m schema.SessionMeta) {
		if m.OriginalPrompt != "" {
			rows = append(rows, detailsRow{"prompt", m.OriginalPrompt})
		}
		if m.EnvInfo.WorkingDir != "" {
			rows = append(rows, detailsRow{"working dir", m.EnvInfo.WorkingDir})
		}
		if m.EnvInfo.GitBranch != "" {
			rows = append(rows, detailsRow{"branch", m.EnvInfo.GitBranch})
		}
		if m.Model != "" {
			rows = append(rows, detailsRow{"model", m.ProfileID + " · " + m.Model})
		}
		if m.TurnCount > 0 {
			rows = append(rows, detailsRow{"turns", strconv.Itoa(m.TurnCount)})
		}
		if m.LastInputTokens > 0 {
			rows = append(rows, detailsRow{"last input tokens", strconv.Itoa(m.LastInputTokens)})
		}
		if m.ParentSessionID != "" {
			rows = append(rows,
				detailsRow{"forked from", m.ParentSessionID},
				detailsRow{"divergence turn", strconv.Itoa(m.DivergenceTurn)},
			)
		}
		if m.IsSubagent {
			rows = append(rows, detailsRow{"kind", "subagent"})
		}
	}

	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			rows = append(rows,
				detailsRow{"daemon", le.Address},
				detailsRow{"pid", strconv.Itoa(le.PID)},
			)
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			addMeta(pe.Meta)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintln(w, `<header class="details-panel-header"><span>details</span><button class="details-panel-close" aria-label="close panel" onclick="document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))">✕</button></header>`)
	_, _ = fmt.Fprintln(w, `<dl class="details-list">`)
	for _, row := range rows {
		_, _ = fmt.Fprintf(w, `<dt>%s</dt><dd>%s</dd>`, htmlEscape(row.Label), htmlEscape(row.Value))
	}
	_, _ = fmt.Fprintln(w, `</dl>`)
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

// renderWorkspaceMeta returns the title-bar meta partial — status pill,
// branch, turn count. Polled every 2s by the workspace header so live
// state changes (idle → processing → ended) reflect promptly.
func (s *WebServer) renderWorkspaceMeta(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceData(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	if detail, ok := s.apiSessionDetail(id); ok {
		data.State = detail.State
		data.StateLabel = stateLabel(detail.State)
		data.TurnCount = detail.TurnCount
		if detail.Model != "" {
			data.Model = detail.Model
		}
		if detail.WorkingDir != "" {
			data.WorkingDir = detail.WorkingDir
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceTmpl.ExecuteTemplate(w, "workspace_meta", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
				StateLabel:   stateLabel(state),
				Model:        le.Model,
				WorkingDir:   le.WorkingDir,
				Capabilities: s.apiSessionCapabilities(id, true),
			}
			if status := s.fetchStatus(le); status != nil {
				if status.State != "" {
					data.State = hubcore.NormalizeState(status.State)
					data.StateLabel = stateLabel(data.State)
				}
				if status.Model != "" {
					data.Model = status.Model
				}
				if status.WorkingDir != "" {
					data.WorkingDir = status.WorkingDir
				}
				data.TurnCount = status.Turns
			}
			// Branch isn't on the rendezvous entry or daemon /status — fall
			// back to the past index where the agent persists EnvInfo.
			if s.cfg.Past != nil {
				if pe, ok := s.cfg.Past.Find(id); ok {
					data.Branch = pe.Meta.EnvInfo.GitBranch
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
			data := WorkspaceData{
				ID:           id,
				SourceLabel:  "serf",
				Title:        pastTitle(pe),
				State:        "ended",
				StateLabel:   stateLabel("ended"),
				TurnCount:    pe.Meta.TurnCount,
				Model:        pe.Meta.Model,
				WorkingDir:   pe.Meta.EnvInfo.WorkingDir,
				Branch:       pe.Meta.EnvInfo.GitBranch,
				Capabilities: s.apiSessionCapabilities(id, false),
			}
			s.fillForkLineage(&data, pe.Meta)
			s.fillSubagentLineage(&data, pe.Meta)
			s.fillObserverLink(&data, pe.Meta)
			return data
		}
	}
	return WorkspaceData{}
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
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
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
// observer subagents, filtered to observers whose session is still LIVE. The
// renderer auto-opens each as a side pane beside the worker. Ended observers are
// dropped server-side so we never auto-open a dead session (they remain manually
// openable via the ⇲ button).
//
// Two sources union (deduped): the forward SessionMeta.ObservedBy stamp (set on
// fresh local watches) and the durable grant-history index built from every
// local session's jobs.jsonl watch-read-grants during the past-index rebuild
// (PastIndex.ObserversOf). The stamp is empty on existing data, so the grant
// history is what makes auto-open work on the sessions already on disk. Local
// sources only: both sources derive from local meta/jobstore state, so remote/
// codex workspaces never reach here with an observer.
func (s *WebServer) fillObserverLink(data *WorkspaceData, m schema.SessionMeta) {
	if s.cfg.Roster == nil {
		return
	}
	var historical []string
	if s.cfg.Past != nil {
		historical = s.cfg.Past.ObserversOf(m.ID)
	}
	if len(m.ObservedBy) == 0 && len(historical) == 0 {
		return
	}
	var live []string
	seen := make(map[string]bool, len(m.ObservedBy)+len(historical))
	for _, observerID := range append(append([]string(nil), m.ObservedBy...), historical...) {
		if observerID == "" || seen[observerID] {
			continue
		}
		seen[observerID] = true
		if _, ok := s.cfg.Roster.Find(observerID); !ok {
			continue // ended observer: not in the live roster
		}
		live = append(live, canonicalRouteID(observerID))
	}
	data.ObserverRouteIDs = live
}

func (s *WebServer) renderInputStrip(w http.ResponseWriter, r *http.Request, id string) {
	// Seed from workspaceData so WorkingDir/Branch are populated for both
	// live and past sessions, then refresh dynamic fields from /status when
	// the daemon is reachable.
	wd := s.workspaceData(id)
	data := map[string]any{
		"SourceLabel":    wd.SourceLabel,
		"Model":          wd.Model,
		"WorkingDir":     wd.WorkingDir,
		"Branch":         wd.Branch,
		"ContextWindow":  0,
		"ContextPercent": 0,
		"ContextNumbers": "",
		"Cost":           wd.Cost,
		"State":          wd.State,
		"StateLabel":     wd.StateLabel,
		"TurnCount":      wd.TurnCount,
		"RunningFor":     wd.RunningFor,
		"GoalStatus":     "",
		"GoalIterations": 0,
	}
	if data["Model"] == "" {
		data["Model"] = "—"
	}
	if detail, ok := s.apiSessionDetail(id); ok {
		if detail.Model != "" {
			data["Model"] = detail.Model
		}
		data["State"] = detail.State
		data["StateLabel"] = stateLabel(detail.State)
		data["TurnCount"] = detail.TurnCount
		data["ContextPercent"] = int(detail.ContextPressure * 100)
		data["ContextWindow"] = detail.ContextWindow
		data["ContextNumbers"] = formatContextNumbers(detail.ContextUsed, detail.ContextWindow, detail.ContextRemaining)
		if detail.WorkingDir != "" {
			data["WorkingDir"] = detail.WorkingDir
		}
		data["GoalStatus"] = detail.GoalStatus
		data["GoalIterations"] = detail.GoalIterations
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.inputStripTmpl.ExecuteTemplate(w, "input_status", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
