package hub

import (
	"context"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// handleSession is the router for public /s/<id>[/<sub>] routes.
// Session fragments live under /_partials/s/... so direct navigation always
// lands in the app shell instead of a standalone workspace fragment.
// handleSession routes the public /s/<id>[/<sub>] paths. The bare page route
// serves the SPA shell (client routing owns the path); /s/<id>/images/<sha> is
// the sha-addressed image fetch the SPA consumes directly. Every legacy sub-
// route (the /_partials fragments and the /s/<id>/<action> form-POSTs) is gone.
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

	switch {
	case sub == "":
		serveSPAIndex(w, r, distFS())
	case strings.HasPrefix(sub, "images/"):
		sha := strings.TrimPrefix(sub, "images/")
		s.handleSessionImage(w, r, id, sha)
	default:
		http.NotFound(w, r)
	}
}

func (s *WebServer) handleThreadDocument(w http.ResponseWriter, r *http.Request) {
	serveSPAIndex(w, r, distFS())
}

func (s *WebServer) workspaceData(id string) WorkspaceData {
	if !isLocalRouteID(id) {
		ref := appRefFromRouteID(id)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		source, err := sourceForThreadWithDeletionFence(s.cfg, s.sources, ref, "")
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
				SourceLabel:  "evener",
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
				// The daemon prices the live session from its own registry;
				// the web renders that figure rather than re-deriving it.
				data.Cost = status.Cost
				// Populate the context gauge here too so the lean /state poll
				// (B3) needs no separate turns fetch.
				data.ContextPercent = int(status.ContextPressure * 100)
				data.ContextWindow = status.ContextWindow
				data.ContextNumbers = formatContextNumbers(status.ContextUsed, status.ContextWindow, status.ContextRemaining)
				data.CompactContextNumbers = formatCompactContextNumbers(status.ContextUsed, status.ContextWindow)
			}
			// Branch isn't on the rendezvous entry or daemon AppWire snapshot — fall
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
			if s.cfg.Roster != nil {
				if subState, live := s.cfg.Roster.SubagentState(id); live {
					// "" means the daemon carried no per-descendant state (old
					// daemon): keep the historical listed-means-working
					// fallback rather than misreading liveness as settled.
					state = "active"
					if strings.TrimSpace(subState) != "" {
						state = hubcore.NormalizeState(subState)
					}
				}
			}
			data := WorkspaceData{
				ID:           id,
				SourceLabel:  "evener",
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
				Usage:      evenerUsageFromCumulative(pe.Meta.CumulativeUsage),
			}
			// One session's own workspace, so this path can afford the same
			// full-transcript recovery the single-thread read does: a fork child's
			// meta carries no CumulativeUsage at all (stampDerivedTotals).
			if data.Usage == nil {
				data.Usage = derivedWorkspaceUsage(pe)
			}
			data.Cost = appwire.EstimateCost(pastEntryCost(s.cfg, pe), data.Usage)
			s.fillForkLineage(&data, pe.Meta)
			s.fillSubagentLineage(&data, pe.Meta)
			s.fillObserverLink(&data, pe.Meta)
			return data
		}
	}
	return WorkspaceData{}
}

// evenerUsageFromCumulative maps a persisted SessionMeta.CumulativeUsage
// (an ended session's WS2 token totals) to the wire appwire.EvenerUsage shown
// in its workspace data. Returns nil when every total (including
// CacheReadTokens) is zero — a session that never accumulated usage, or a
// meta written before WS2 — so the usage cluster hides rather than rendering
// ↑0 ↓0 (mirrors evenerUsageFromLLM in cmd/evener/serve.go).
func evenerUsageFromCumulative(u schema.CumulativeUsage) *appwire.EvenerUsage {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &appwire.EvenerUsage{
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
	source, err := sourceForThreadWithDeletionFence(s.cfg, s.sources, ref, "")
	if err != nil {
		return fallback, ""
	}
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
	if err != nil {
		return fallback, ""
	}
	caps := hubCapabilitiesFromAppwire(resp.Thread.Evener.Capabilities)
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
// SessionMeta.ObservedBy is the sole source of this append-only UI relationship.
func (s *WebServer) fillObserverLink(data *WorkspaceData, m schema.SessionMeta) {
	if len(m.ObservedBy) == 0 {
		return
	}
	var observers []string
	seen := make(map[string]bool, len(m.ObservedBy))
	for _, observerID := range m.ObservedBy {
		if observerID == "" || seen[observerID] {
			continue
		}
		seen[observerID] = true
		observers = append(observers, canonicalRouteID(observerID))
	}
	data.ObserverRouteIDs = observers
}
