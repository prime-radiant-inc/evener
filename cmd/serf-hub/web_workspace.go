package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	case "promote-queued":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handlePromoteQueued(w, r, id)
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
	data := s.workspaceDataForRender(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	data.ShowSidebarToggle = true
	data.ThreadDocumentMode = false
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceTmpl.ExecuteTemplate(w, "workspace", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

// detailsRow is one <dt>/<dd> pair in the details panel. DataRow, when
// non-empty, renders as a data-row="..." attribute on both elements so CSS
// can gate specific rows (e.g. a "show cost" toggle) without touching every
// other row. Mono marks machine values (ids, paths, addresses) that render
// in the mono face; Wide spans the value across the full panel width (long
// paths); Copy adds a click-to-copy button carrying the raw value; HTML,
// when non-empty, is pre-escaped markup rendered instead of Value (the
// context meter).
type detailsRow struct {
	Label, Value, DataRow string
	Mono, Wide, Copy      bool
	HTML                  string
}

// detailsSection is one titled group of rows in the details panel.
type detailsSection struct {
	Title string
	Rows  []detailsRow
}

// renderDetailsRow writes a single detailsRow's <dt>/<dd> pair to w.
func renderDetailsRow(w io.Writer, row detailsRow) {
	attr := ""
	if row.DataRow != "" {
		attr = ` data-row="` + htmlEscape(row.DataRow) + `"`
	}
	dtClass, ddClasses := "", []string{}
	if row.Mono {
		ddClasses = append(ddClasses, "mono")
	}
	if row.Wide {
		dtClass = ` class="wide"`
		ddClasses = append(ddClasses, "wide")
	}
	ddClass := ""
	if len(ddClasses) > 0 {
		ddClass = ` class="` + strings.Join(ddClasses, " ") + `"`
	}
	value := row.HTML
	if value == "" {
		value = htmlEscape(row.Value)
	}
	if row.Copy {
		value += `<button class="details-copy" data-copy="` + htmlEscape(row.Value) + `" title="copy" aria-label="copy ` + htmlEscape(row.Label) + `">⧉</button>`
	}
	_, _ = fmt.Fprintf(w, `<dt%s%s>%s</dt><dd%s%s>%s</dd>`, attr, dtClass, htmlEscape(row.Label), attr, ddClass, value)
}

// tokensAndCostRows returns the "tokens" and (when estimable) "cost" rows
// for a session's usage, shared by the live-session and ended-session
// branches of renderDetailsPanel so both stay in sync. Returns nil when
// usage is nil.
func tokensAndCostRows(model string, usage *appwire.SerfUsage) []detailsRow {
	if usage == nil {
		return nil
	}
	rows := []detailsRow{{Label: "tokens", Value: fmt.Sprintf("↑%s ↓%s · cache-read %s · total %s",
		formatTokenCount(int(usage.InputTokens)), formatTokenCount(int(usage.OutputTokens)),
		formatTokenCount(int(usage.CacheReadTokens)), formatTokenCount(int(usage.TotalTokens)))}}
	if cost := appwire.EstimateCost(model, usage); cost != "" {
		rows = append(rows, detailsRow{Label: "cost", Value: cost, DataRow: "cost"})
	}
	return rows
}

// contextMeterHTML renders the context-usage row's value: a thin neutral
// meter (the task-card meter idiom) plus structured stat pieces
// ("42% used · 42k / 100k · 58k left") instead of a nested-parens sentence.
func contextMeterHTML(pressure float64, used, window, remaining int) string {
	pct := int(pressure * 100)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf(`<div class="details-context"><div class="details-meter"><div class="details-meter-fill" style="width:%d%%"></div></div><div class="details-context-stats"><span>%d%% used</span><span>%s / %s</span><span>%s left</span></div></div>`,
		pct, pct, formatTokenCount(used), formatTokenCount(window), formatTokenCount(remaining))
}

// detailsSections gathers the session's facts from the live daemon status
// and the persisted past-index meta, merged so each fact appears exactly
// once (live status wins over the persisted meta when both know a value),
// grouped into titled sections.
func (s *WebServer) detailsSections(id string) []detailsSection {
	var meta *schema.SessionMeta
	stateDir := ""
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			m := pe.Meta
			meta = &m
			stateDir = pe.StateDir
		}
	}
	var status *daemonStatus
	daemonAddr, daemonPID := "", 0
	live := false
	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			live = true
			daemonAddr, daemonPID = le.Address, le.PID
			status = s.fetchStatus(le)
		}
	}

	// Session — identity.
	session := []detailsRow{
		{Label: "source", Value: sourceLabelFromRefText(appRefFromRouteID(id))},
		{Label: "session id", Value: id, Mono: true, Copy: true},
	}
	switch {
	case meta != nil && meta.Model != "":
		if meta.ProfileID != "" {
			session = append(session, detailsRow{Label: "model", Value: meta.ProfileID + " · " + meta.Model, Mono: true})
		} else {
			session = append(session, detailsRow{Label: "model", Value: meta.Model, Mono: true})
		}
	case status != nil && status.Model != "":
		session = append(session, detailsRow{Label: "model", Value: status.Model, Mono: true})
	}
	if meta != nil {
		if meta.EnvInfo.GitBranch != "" {
			session = append(session, detailsRow{Label: "branch", Value: meta.EnvInfo.GitBranch, Mono: true})
		}
		if meta.ParentSessionID != "" {
			session = append(session,
				detailsRow{Label: "forked from", Value: meta.ParentSessionID, Mono: true},
				detailsRow{Label: "divergence turn", Value: strconv.Itoa(meta.DivergenceTurn)},
			)
		}
		if meta.IsSubagent {
			session = append(session, detailsRow{Label: "kind", Value: "subagent"})
		}
		if meta.OriginalPrompt != "" {
			session = append(session, detailsRow{Label: "prompt", Value: meta.OriginalPrompt, Wide: true})
		}
	}

	// Usage — context, tokens, cost, turns, work time. Live status wins.
	var usage []detailsRow
	if status != nil && status.ContextWindow > 0 {
		usage = append(usage, detailsRow{Label: "context", Wide: true,
			HTML: contextMeterHTML(status.ContextPressure, status.ContextUsed, status.ContextWindow, status.ContextRemaining)})
	}
	switch {
	case status != nil && status.Usage != nil:
		usage = append(usage, tokensAndCostRows(status.Model, status.Usage)...)
	case meta != nil:
		usage = append(usage, tokensAndCostRows(meta.Model, serfUsageFromCumulative(meta.CumulativeUsage))...)
	}
	if meta != nil && meta.TurnCount > 0 {
		usage = append(usage, detailsRow{Label: "turns", Value: strconv.Itoa(meta.TurnCount)})
	}
	switch {
	case status != nil && status.WorkMillis > 0:
		usage = append(usage, detailsRow{Label: "work time", Value: formatWorkMillis(status.WorkMillis)})
	case meta != nil && meta.WorkMillis > 0:
		usage = append(usage, detailsRow{Label: "work time", Value: formatWorkMillis(meta.WorkMillis)})
	}
	if meta != nil && meta.LastInputTokens > 0 {
		usage = append(usage, detailsRow{Label: "last input tokens", Value: strconv.Itoa(meta.LastInputTokens)})
	}

	// Runtime — where the session runs.
	var runtime []detailsRow
	if live {
		runtime = append(runtime, detailsRow{Label: "daemon", Value: daemonAddr, Mono: true})
		if daemonPID > 0 {
			runtime = append(runtime, detailsRow{Label: "pid", Value: strconv.Itoa(daemonPID), Mono: true})
		}
	}
	workingDir := ""
	if meta != nil && meta.EnvInfo.WorkingDir != "" {
		workingDir = meta.EnvInfo.WorkingDir
	} else if status != nil && status.WorkingDir != "" {
		workingDir = status.WorkingDir
	}
	if workingDir != "" {
		runtime = append(runtime, detailsRow{Label: "working dir", Value: workingDir, Mono: true, Wide: true, Copy: true})
	}

	// Files — on-disk artifacts.
	var files []detailsRow
	if meta != nil && stateDir != "" {
		transcript := filepath.Join(stateDir, "sessions", meta.ID+".transcript.jsonl")
		if _, err := os.Stat(transcript); err == nil {
			files = append(files, detailsRow{Label: "transcript", Value: transcript, Mono: true, Wide: true, Copy: true})
		}
		apiLog := filepath.Join(stateDir, "sessions", meta.ID+".api.jsonl")
		if _, err := os.Stat(apiLog); err == nil {
			files = append(files, detailsRow{Label: "api log", Value: apiLog, Mono: true, Wide: true, Copy: true})
		}
	}

	sections := []detailsSection{
		{Title: "Session", Rows: session},
		{Title: "Usage", Rows: usage},
		{Title: "Runtime", Rows: runtime},
		{Title: "Files", Rows: files},
	}
	out := sections[:0]
	for _, sec := range sections {
		if len(sec.Rows) > 0 {
			out = append(out, sec)
		}
	}
	return out
}

// renderDetailsPanel returns a side-panel with the session's verbose
// metadata grouped into titled sections (Session / Usage / Runtime / Files).
// Triggered by clicking the "details" link in the workspace header.
func (s *WebServer) renderDetailsPanel(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintln(w, `<header class="details-panel-header"><span>details</span><button class="details-panel-close" aria-label="close panel" onclick="document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))">✕</button></header>`)
	for _, sec := range s.detailsSections(id) {
		_, _ = fmt.Fprintf(w, `<section class="details-section"><h3 class="details-section-title">%s</h3><dl class="details-list">`, htmlEscape(sec.Title))
		for _, row := range sec.Rows {
			renderDetailsRow(w, row)
		}
		_, _ = fmt.Fprintln(w, `</dl></section>`)
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

// appwireUsageFromHub maps hubapi's flattened Usage type back to
// appwire.SerfUsage — the inverse of hubUsageFromAppwire — so
// appwire.EstimateCost (which only knows the appwire wire type) can be
// called from the hubapi.SessionDetail data renderInputStrip already has.
// Returns nil when u is nil.
func appwireUsageFromHub(u *hubapi.Usage) *appwire.SerfUsage {
	if u == nil {
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

func (s *WebServer) renderInputStrip(w http.ResponseWriter, r *http.Request, id string) {
	// apiSessionState seeds Title/State/TurnCount/WorkingDir/Branch/context/
	// goal/work-time/token fields from workspaceData in a single call, plus
	// (for a live session) one lean ReadThread — replacing the former
	// workspaceData + apiSessionDetail pair, which called workspaceData
	// twice and fetched the full transcript just to refresh this status row.
	// Title/OOBTitle ride along on this same poll: the response carries an
	// out-of-band swap of the header's #workspace-session-title span,
	// keeping it fresh as the session's generated name changes.
	detail, _ := s.apiSessionState(id)
	data := map[string]any{
		"SourceLabel":           sourceLabelFromRefText(appRefFromRouteID(id)),
		"ThreadDocumentMode":    r.URL.Query().Get("thread_document") == "1",
		"Title":                 detail.Title,
		"OOBTitle":              true,
		"Model":                 detail.Model,
		"WorkingDir":            detail.WorkingDir,
		"Branch":                detail.Branch,
		"ContextPercent":        int(detail.ContextPressure * 100),
		"ContextWindow":         detail.ContextWindow,
		"ContextNumbers":        formatContextNumbers(detail.ContextUsed, detail.ContextWindow, detail.ContextRemaining),
		"CompactContextNumbers": formatCompactContextNumbers(detail.ContextUsed, detail.ContextWindow),
		"Worktree":              "",
		"Cost":                  appwire.EstimateCost(detail.Model, appwireUsageFromHub(detail.Usage)),
		"State":                 detail.State,
		"StateLabel":            stateLabel(detail.State, false),
		"TurnCount":             detail.TurnCount,
		"GoalStatus":            detail.GoalStatus,
		"GoalIterations":        detail.GoalIterations,
		"WorkMillis":            detail.WorkMillis,
		"Usage":                 detail.Usage,
		"ActiveTurnStartedAt":   detail.ActiveTurnStartedAt,
	}
	if isLocalRouteID(id) && s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			data["Worktree"] = worktreeLabel(pe.Meta.WorktreePath)
		}
	}
	if data["Model"] == "" {
		data["Model"] = "—"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.inputStripTmpl.ExecuteTemplate(w, "input_status", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
