package main

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/rendezvous"
)

// archiveDecisions returns the current set of user-explicit archive decisions.
// Returns an empty map (never nil) when cfg.Archive is nil or Decisions() fails.
func (s *WebServer) archiveDecisions() map[hubcore.ArchiveKey]bool {
	if s.cfg.Archive == nil {
		return map[hubcore.ArchiveKey]bool{}
	}
	decisions, err := s.cfg.Archive.Decisions()
	if err != nil {
		return map[hubcore.ArchiveKey]bool{}
	}
	return decisions
}

func (s *WebServer) handleAPITree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	_, live := s.navigationTreeInputs(r.Context())
	favs := s.favoriteDecisions()
	tree, attentionSummary := s.memoTree(r.Context())
	resp := hubapi.TreeResponse{
		GeneratedAt:      time.Now().UTC(),
		Sources:          s.apiTreeSources(),
		AttentionSummary: hubAttentionSummaryFromCore(attentionSummary),
	}
	for _, n := range tree.Live {
		if !treeNodeCanActLive(n) {
			continue
		}
		resp.Live = append(resp.Live, s.apiTreeNode("live", "", n, true))
	}
	seenProjectRefs := map[string]bool{}
	projectIndexes := map[string]int{}
	// TestRuns takes precedence over ArchivedProjects (round-2 B6): a project
	// where every session carries Origin=="test" is routed there even if it
	// would otherwise also qualify as archived. Every branch marks
	// seenProjectRefs so none of its sessions re-surface as an orphan-live
	// "project" below; only the active (non-archived, non-test) branch
	// populates projectIndexes, since that's the only bucket the orphan-live
	// loop can append into (it indexes into resp.Projects specifically).
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.IsTestRun {
			for _, n := range projectSessions(p) {
				seenProjectRefs[n.ID] = true
			}
			resp.TestRuns = append(resp.TestRuns, s.apiTreeProject("project", favs, p))
			continue
		}
		if p.IsArchived {
			for _, n := range projectSessions(p) {
				seenProjectRefs[n.ID] = true
			}
			resp.ArchivedProjects = append(resp.ArchivedProjects, s.apiTreeProject("project", favs, p))
			continue
		}
		projectIndexes[p.Key] = len(resp.Projects)
		ap := s.apiTreeProject("project", favs, p)
		for _, n := range projectSessions(p) {
			seenProjectRefs[n.ID] = true
		}
		resp.Projects = append(resp.Projects, ap)
	}
	for _, n := range tree.NeedsYou {
		resp.NeedsYou = append(resp.NeedsYou, s.apiTreeNodeTier("needsyou", "", "needsyou", favs, n))
	}
	for _, le := range live {
		if le.SessionID == "" || seenProjectRefs[le.SessionID] {
			continue
		}
		project := filepath.Base(le.WorkingDir)
		if le.WorkingDir == "" || project == "." {
			project = "(no project)"
		}
		key := hubcore.ProjectSlug(le.WorkingDir)
		node := hubcore.TreeNode{
			ID:        le.SessionID,
			Title:     liveTitle(le.SessionID, le, s.cfg.Past),
			Project:   project,
			State:     hubcore.NormalizeState(le.Status),
			Kind:      "session",
			CreatedAt: le.StartedAt,
			UpdatedAt: le.StartedAt,
			Age:       hubcore.AgeString(le.StartedAt),
		}
		apiNode := s.apiTreeNode("project", key, node, true)
		if idx, ok := projectIndexes[key]; ok {
			p := &resp.Projects[idx]
			p.Sessions = append(p.Sessions, apiNode)
			if hubcore.AttentionRank(node.State) > hubcore.AttentionRank(p.RollupState) {
				p.RollupState = node.State
			}
			continue
		}
		projectIndexes[key] = len(resp.Projects)
		resp.Projects = append(resp.Projects, hubapi.TreeProject{
			Key:         key,
			Name:        project,
			WorkingDir:  le.WorkingDir,
			RollupState: node.State,
			Sessions:    []hubapi.TreeNode{apiNode},
		})
	}

	// Pinned tier: favorited, unarchived sessions across all projects,
	// excluding anything already surfaced in NeedsYou, most-recently-updated
	// first.
	needsYouIDs := map[string]bool{}
	for _, n := range resp.NeedsYou {
		needsYouIDs[n.SessionID] = true
	}
	for _, p := range tree.Projects {
		for _, n := range append(append([]hubcore.TreeNode{}, p.Current...), p.Recent...) {
			if n.Kind != "session" || !favs[hubcore.ArchiveKey{Kind: "session", ID: n.ID}] {
				continue
			}
			if needsYouIDs[hubRefFromTreeNodeID(n.ID).SessionID] {
				continue
			}
			resp.Favorites = append(resp.Favorites, s.apiTreeNodeTier("pinned", "", "pinned", favs, n))
		}
	}
	sort.SliceStable(resp.Favorites, func(i, j int) bool {
		return resp.Favorites[i].UpdatedAt.After(resp.Favorites[j].UpdatedAt)
	})

	writeAPIJSON(w, http.StatusOK, resp)
}

// memoTree returns the memoized full tree + attention summary, single-sourced
// so handleAPITree and handleAPITreeProject never diverge on what "the tree"
// is. Memoization key is the inputs version + a coarse time bucket (see
// treeCache.Get); callers that also need live/favs must fetch those
// themselves, since memoTree's return shape only carries what treeCache
// stores.
func (s *WebServer) memoTree(ctx context.Context) (hubcore.Tree, hubcore.AttentionSummary) {
	metas, live := s.navigationTreeInputs(ctx)
	decisions := s.archiveDecisions()
	var version uint64
	if s.cfg.Inputs != nil {
		version = s.cfg.Inputs.Load()
	}
	return s.treeCache.Get(version, time.Now(), func() (hubcore.Tree, hubcore.AttentionSummary) {
		t := hubcore.BuildTree(metas, live, decisions)
		_, sum := hubcore.DeriveAttention(metas, live, decisions)
		return t, sum
	})
}

// handleAPITreeProject serves a single project's node by indexing the
// memoized full tree (never a fresh full-meta scan — round-2 A4). The client
// resync uses this to re-request only the projects it has expanded.
func (s *WebServer) handleAPITreeProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "key is required")
		return
	}
	tree, _ := s.memoTree(r.Context())
	favs := s.favoriteDecisions()
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.Key == key {
			writeAPIJSON(w, http.StatusOK, s.apiTreeProject("project", favs, p))
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "project not found")
}

func (s *WebServer) navigationTreeInputs(ctx context.Context) ([]schema.SessionMeta, []hubcore.LiveEntry) {
	var live []hubcore.LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	var metas []schema.SessionMeta
	if s.cfg.Past != nil {
		metas = s.cfg.Past.AllMetas()
	}
	for _, thread := range s.remoteTreeThreads(ctx) {
		meta, entry, ok := appThreadTreeEntries(thread)
		if !ok {
			continue
		}
		metas = append(metas, meta)
		if appThreadTreeLive(thread) {
			live = append(live, entry)
		}
	}
	return metas, live
}

// remoteTreeThreads returns the remote-source thread list the tree walk
// folds into its metas/live inputs. When a RemoteThreadCache is configured
// (production — see main.go), it reads the cache instead of performing a
// synchronous network walk, so a tree render never blocks on a remote hop.
// Tests that construct a WebServer without a cache fall back to the old
// synchronous behavior via refreshRemoteThreads.
func (s *WebServer) remoteTreeThreads(ctx context.Context) []appwire.Thread {
	if s.cfg.RemoteThreadCache != nil {
		return s.cfg.RemoteThreadCache.Get()
	}
	return s.refreshRemoteThreads(ctx)
}

// refreshRemoteThreads performs the synchronous walk across every configured
// remote source: it lists each source's threads (via listThreadsWithFallback,
// which retains the last-known-good result across transient errors) and
// backfills each thread's Source and Serf.Ref. This used to run inline on
// every /api/tree request (as remoteTreeThreads); it now runs on a background
// ~30s ticker + poke (main.go), Storing its result into a RemoteThreadCache
// for remoteTreeThreads to read.
func (s *WebServer) refreshRemoteThreads(ctx context.Context) []appwire.Thread {
	if s.sources == nil {
		return nil
	}
	s.ensureManagedCodexSources(ctx)
	var threads []appwire.Thread
	for _, source := range s.sources.All() {
		if source.ID() == "local" {
			continue
		}
		for _, thread := range s.listThreadsWithFallback(ctx, source) {
			sourceID := threadListSourceID(source.ID(), thread)
			thread.Source = sourceID
			if thread.Serf.Ref == "" {
				threadID := strutil.FirstNonEmpty(thread.ID, thread.SessionID)
				if threadID != "" {
					thread.Serf.Ref = appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
				}
			}
			threads = append(threads, thread)
		}
	}
	return threads
}

// sourceThreadLister is the minimal slice of appsource.Source that
// listThreadsWithFallback needs. Keeping it small makes the last-known-good
// retention straightforward to test without stubbing the whole Source surface.
type sourceThreadLister interface {
	ID() string
	ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error)
}

// listThreadsWithFallback lists a remote source's threads, retaining the last
// successful result when the source errors. A transient ListThreads failure
// (slow daemon, dial timeout) must not blank that source's sessions from the
// sidebar. An empty *successful* list does clear the cache, so a genuinely-gone
// source ages out instead of lingering forever.
func (s *WebServer) listThreadsWithFallback(ctx context.Context, source sourceThreadLister) []appwire.Thread {
	resp, err := source.ListThreads(ctx, appwire.ThreadListParams{IncludeSubagents: true})
	s.lastGoodMu.Lock()
	defer s.lastGoodMu.Unlock()
	if s.lastGoodThreads == nil {
		s.lastGoodThreads = map[string][]appwire.Thread{}
	}
	if err != nil {
		return s.lastGoodThreads[source.ID()]
	}
	s.lastGoodThreads[source.ID()] = resp.Data
	return resp.Data
}

func (s *WebServer) ensureManagedCodexSources(ctx context.Context) {
	_ = ensureManagedCodexSources(ctx, s.cfg, s.sources, appwire.ThreadListParams{})
}

func appThreadTreeEntries(thread appwire.Thread) (schema.SessionMeta, hubcore.LiveEntry, bool) {
	ref, ok := appThreadTreeRef(thread)
	if !ok {
		return schema.SessionMeta{}, hubcore.LiveEntry{}, false
	}
	refText := ref.String()
	title := strutil.FirstNonEmpty(thread.Name, thread.Preview, thread.SessionID, thread.ID, refText)
	createdAt := hubcore.UnixTime(thread.CreatedAt)
	updatedAt := hubcore.UnixTime(thread.UpdatedAt)
	meta := schema.SessionMeta{
		ID:             refText,
		ProfileID:      ref.SourceID,
		Model:          thread.ModelProvider,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		OriginalPrompt: title,
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir: thread.CWD,
		},
	}
	if thread.GitInfo != nil {
		meta.EnvInfo.GitBranch = thread.GitInfo.Branch
		meta.EnvInfo.GitOriginURL = thread.GitInfo.OriginURL
	}
	entry := hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			SourceID:   ref.SourceID,
			ThreadID:   ref.ThreadID,
			SessionID:  refText,
			WorkingDir: thread.CWD,
			Model:      thread.ModelProvider,
			StartedAt:  hubcore.OrderCreatedAt(createdAt, updatedAt),
		},
		SessionID: refText,
		Status:    thread.Status.Type,
	}
	return meta, entry, true
}

func appThreadTreeRef(thread appwire.Thread) (appwire.Ref, bool) {
	if thread.Serf.Ref != "" {
		if ref, err := appwire.ParseRef(thread.Serf.Ref); err == nil {
			return ref, true
		}
	}
	sourceID := strings.TrimSpace(thread.Source)
	threadID := strutil.FirstNonEmpty(thread.ID, thread.SessionID)
	if sourceID == "" || threadID == "" {
		return appwire.Ref{}, false
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}, true
}

func appThreadTreeLive(thread appwire.Thread) bool {
	switch thread.Status.Type {
	case appwire.ThreadStatusClosed, appwire.ThreadStatusNotLoaded:
		return false
	default:
		return true
	}
}

// hubAttentionSummaryFromCore maps hubcore's internal attention summary to
// hubapi's public wire type (hubapi cannot import the hub's internal package).
func hubAttentionSummaryFromCore(sum hubcore.AttentionSummary) hubapi.AttentionSummary {
	return hubapi.AttentionSummary{NeedsYou: sum.NeedsYou, Error: sum.Error, Working: sum.Working}
}

func (s *WebServer) apiTreeSources() []hubapi.Source {
	sources := []hubapi.Source{{
		ID:     "local",
		Label:  "this host",
		Kind:   "local",
		Online: true,
	}}
	if s.sources == nil {
		return sources
	}
	for _, source := range s.sources.All() {
		if source.ID() == "local" {
			continue
		}
		sources = append(sources, hubapi.Source{
			ID:     source.ID(),
			Label:  source.ID(),
			Kind:   "appwire",
			Online: true,
		})
	}
	return sources
}

func hubRefFromAppThread(thread appwire.Thread) hubapi.Ref {
	refText := thread.Serf.Ref
	if refText == "" {
		refText = appwire.Ref{SourceID: thread.Source, ThreadID: thread.ID}.String()
	}
	ref, err := hubapi.ParseRef(refText)
	if err != nil {
		return hubapi.LocalRef(thread.ID)
	}
	return ref
}

func hubCapabilitiesFromAppwire(caps appwire.ThreadCapabilities) hubapi.SessionCapabilities {
	return hubapi.SessionCapabilities{
		Send:        caps.Send,
		Steer:       caps.Steer,
		Interrupt:   caps.Interrupt,
		Compact:     caps.Compact,
		Clear:       caps.Clear,
		Fork:        caps.ForkFromTurn,
		Shutdown:    caps.Shutdown,
		ChangeModel: caps.ChangeModel,
		Queue:       caps.Queue,
	}
}

func hubDetailFromAppThread(thread appwire.Thread) hubapi.SessionDetail {
	ref := hubRefFromAppThread(thread)
	state := hubcore.NormalizeState(thread.Status.Type)
	if state == "" {
		state = "idle"
	}
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = thread.SessionID
	}
	project := filepath.Base(thread.CWD)
	if project == "" || project == "." {
		project = "(no project)"
	}
	live := state != "ended" && state != "closed"
	detail := hubapi.SessionDetail{
		Ref:              ref.String(),
		HostID:           ref.HostID,
		SessionID:        ref.SessionID,
		Title:            title,
		State:            state,
		Live:             live,
		Project:          project,
		WorkingDir:       thread.CWD,
		Model:            thread.ModelProvider,
		Profile:          thread.Serf.Profile,
		TurnCount:        completedTurnCount(thread.Turns),
		ActiveTurnID:     activeTurnIDFromAppwireThread(thread),
		ContextPressure:  thread.Serf.ContextPressure,
		ContextUsed:      thread.Serf.ContextUsed,
		ContextWindow:    thread.Serf.ContextWindow,
		ContextRemaining: thread.Serf.ContextRemaining,
		Capabilities:     hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
	}
	if detail.SessionID == "" {
		detail.SessionID = thread.ID
	}
	if goal := thread.Serf.Goal; goal != nil {
		detail.GoalStatus = goal.Status
		detail.GoalIterations = goal.Iterations
	}
	return detail
}

func (s *WebServer) isLive(sessionID string) bool {
	if !isLocalRouteID(sessionID) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, appRefFromRouteID(sessionID), "")
		return err == nil
	}
	if s.cfg.Roster == nil {
		return false
	}
	_, ok := s.cfg.Roster.Find(sessionID)
	return ok
}

func treeNodeCanActLive(n hubcore.TreeNode) bool {
	return hubcore.NormalizeState(n.State) != "ended"
}

// projectSessions flattens a project's tier-split sessions (Current, Recent,
// Archived) into one list for the /api/tree JSON endpoint, which is tier-blind.
func projectSessions(p hubcore.TreeProject) []hubcore.TreeNode {
	out := make([]hubcore.TreeNode, 0, len(p.Current)+len(p.Recent)+len(p.Archived))
	out = append(out, p.Current...)
	out = append(out, p.Recent...)
	out = append(out, p.Archived...)
	return out
}

// apiTreeProject projects a hubcore.TreeProject onto the wire TreeProject,
// carrying the rollup/overflow additive fields and stamping each session's
// tier (current/recent/archived) at projection time. favs is the
// once-per-request favorite-decisions map (see favoriteDecisions), forwarded
// so apiTreeNodeTier never has to open the favorite store per node.
func (s *WebServer) apiTreeProject(scope string, favs map[hubcore.ArchiveKey]bool, p hubcore.TreeProject) hubapi.TreeProject {
	ap := hubapi.TreeProject{
		Key:             p.Key,
		Name:            p.Name,
		WorkingDir:      p.WorkingDir,
		RollupState:     p.RollupState,
		RollupLive:      p.RollupLive,
		RollupAttn:      p.RollupAttn,
		DefaultExpanded: p.Expanded,
		MoreCurrent:     p.MoreCurrent,
		MoreRecent:      p.MoreRecent,
		MoreArchived:    p.MoreArchived,
		Worktrees:       p.Worktrees,
	}
	for _, n := range p.Current {
		ap.Sessions = append(ap.Sessions, s.apiTreeNodeTier(scope, p.Key, "current", favs, n))
	}
	for _, n := range p.Recent {
		ap.Sessions = append(ap.Sessions, s.apiTreeNodeTier(scope, p.Key, "recent", favs, n))
	}
	for _, n := range p.Archived {
		ap.Sessions = append(ap.Sessions, s.apiTreeNodeTier(scope, p.Key, "archived", favs, n))
	}
	return ap
}

// apiTreeNodeTier wraps apiTreeNode and stamps the row-level fields a tiered
// projection (project sessions, NeedsYou, Pinned) carries but a bare Live row
// doesn't. favs is the once-per-request favorite-decisions map computed by
// handleAPITree (see favoriteDecisions) — never opens the store itself, so
// this stays O(1) DB opens per /api/tree regardless of node count.
func (s *WebServer) apiTreeNodeTier(scope, projectKey, tier string, favs map[hubcore.ArchiveKey]bool, n hubcore.TreeNode) hubapi.TreeNode {
	out := s.apiTreeNode(scope, projectKey, n, treeNodeCanActLive(n) && s.isLive(n.ID))
	out.Tier = tier
	out.Branch = n.Branch
	out.ClusterCount = n.ClusterCount
	out.Favorite = favs[hubcore.ArchiveKey{Kind: "session", ID: n.ID}]
	out.Rename = s.rowRenameable(n.ID)
	return out
}

// favoriteDecisions returns the current set of user-explicit favorite
// decisions. Returns an empty map (never nil) when cfg.Favorite is nil or
// Favorites() fails. Computed once per /api/tree request and threaded through
// apiTreeProject/apiTreeNodeTier so a node-count-sized page never opens the
// favorite store more than once.
func (s *WebServer) favoriteDecisions() map[hubcore.ArchiveKey]bool {
	if s.cfg.Favorite == nil {
		return map[hubcore.ArchiveKey]bool{}
	}
	f, err := s.cfg.Favorite.Favorites()
	if err != nil {
		return map[hubcore.ArchiveKey]bool{}
	}
	return f
}

// rowRenameable reports whether a tree row exposes the rename menu item. Local
// rows are always renameable (ended via the hub meta-edit path, live via the
// daemon method); Codex-bridged rows are not. Derived from the ref's host, not
// a per-thread probe.
func (s *WebServer) rowRenameable(id string) bool { return isLocalRouteID(id) }

func (s *WebServer) apiTreeNode(scope, projectKey string, n hubcore.TreeNode, live bool) hubapi.TreeNode {
	ref := hubRefFromTreeNodeID(n.ID)
	refText := ref.String()
	rowID := scope + ":" + refText
	if projectKey != "" {
		rowID = scope + ":" + projectKey + ":" + refText
	}
	out := hubapi.TreeNode{
		RowID:     rowID,
		Ref:       refText,
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
		Title:     n.Title,
		Project:   n.Project,
		State:     n.State,
		Kind:      n.Kind,
		Live:      live,
		UpdatedAt: n.UpdatedAt,
		Age:       n.Age,
	}
	if le, ok := s.liveEntry(n.ID); ok {
		out.Model = le.Model
	}
	for _, child := range n.Children {
		out.Children = append(out.Children, s.apiTreeNode("project", projectKey, child, treeNodeCanActLive(child) && s.isLive(child.ID)))
	}
	return out
}

func hubRefFromTreeNodeID(id string) hubapi.Ref {
	if ref, err := hubapi.ParseRef(id); err == nil {
		return ref
	}
	return hubapi.LocalRef(id)
}

func (s *WebServer) liveEntry(sessionID string) (hubcore.LiveEntry, bool) {
	if s.cfg.Roster == nil {
		return hubcore.LiveEntry{}, false
	}
	return s.cfg.Roster.Find(sessionID)
}

func (s *WebServer) handleAPISession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	refText, sub, _ := strings.Cut(path, "/")
	if unescaped, err := url.PathUnescape(refText); err == nil {
		refText = unescaped
	}
	ref, err := hubapi.ParseRef(refText)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	routeID := ref.SessionID
	if ref.HostID != "local" {
		routeID = ref.String()
	}

	switch sub {
	case "":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		detail, ok := s.apiSessionDetail(routeID)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "session not found")
			return
		}
		writeAPIJSON(w, http.StatusOK, detail)
	case "details":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		detail, ok := s.apiSessionDetail(routeID)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "session not found")
			return
		}
		writeAPIJSON(w, http.StatusOK, detail)
	case "send":
		s.handleSend(w, r, routeID)
	case "tasks":
		s.renderSessionTasks(w, r, routeID)
	case "fork":
		s.handleAPIFork(w, r, routeID)
	case "clear":
		s.handleAPIClear(w, r, routeID)
	case "model":
		s.handleAPIModel(w, r, routeID)
	case "reasoning-effort":
		s.handleAPIReasoningEffort(w, r, routeID)
	case "rename":
		s.handleAPIRename(w, r, routeID)
	case "interrupt", "compact", "shutdown":
		s.handleSessionAction(w, r, routeID, sub)
	default:
		writeAPIError(w, http.StatusNotFound, "session not found")
	}
}

func (s *WebServer) apiSessionDetail(id string) (hubapi.SessionDetail, bool) {
	wd := s.workspaceData(id)
	if wd.ID == "" {
		return hubapi.SessionDetail{}, false
	}
	ref, err := hubapi.ParseRef(appRefFromRouteID(id))
	if err != nil {
		ref = hubapi.LocalRef(id)
	}
	live := s.isLive(id)
	detail := hubapi.SessionDetail{
		Ref:            ref.String(),
		HostID:         ref.HostID,
		SessionID:      ref.SessionID,
		Title:          wd.Title,
		State:          wd.State,
		Live:           live,
		Project:        filepath.Base(wd.WorkingDir),
		WorkingDir:     wd.WorkingDir,
		Branch:         wd.Branch,
		Model:          wd.Model,
		TurnCount:      wd.TurnCount,
		ForkLabel:      wd.ForkLabel,
		DivergenceTurn: wd.DivergenceTurn,
		Capabilities:   s.apiSessionCapabilities(id, live),
	}
	if detail.Project == "" || detail.Project == "." {
		detail.Project = "(no project)"
	}
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		appRef := appRefFromRouteID(id)
		if source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, appRef, ""); err == nil {
			if resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: appRef, IncludeTurns: true, ItemsView: "full"}); err == nil {
				appDetail := hubDetailFromAppThread(resp.Thread)
				if isLocalRouteID(id) && detail.TurnCount > 0 {
					appDetail.TurnCount = detail.TurnCount
				}
				appDetail.ParentSessionID = detail.ParentSessionID
				appDetail.DivergenceTurn = detail.DivergenceTurn
				appDetail.ForkLabel = detail.ForkLabel
				appDetail.IsSubagent = detail.IsSubagent
				detail = appDetail
			}
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			if detail.Title == "" {
				detail.Title = pastTitle(pe)
			}
			if detail.Model == "" {
				detail.Model = pe.Meta.Model
			}
			if detail.Profile == "" {
				detail.Profile = pe.Meta.ProfileID
			}
			if detail.TurnCount == 0 {
				detail.TurnCount = pe.Meta.TurnCount
			}
			detail.ParentSessionID = pe.Meta.ParentSessionID
			detail.DivergenceTurn = pe.Meta.DivergenceTurn
			detail.ForkLabel = pe.Meta.ForkLabel
			detail.IsSubagent = pe.Meta.IsSubagent
		}
	}
	return detail, true
}

func (s *WebServer) apiSessionCapabilities(id string, live bool) hubapi.SessionCapabilities {
	pastExists := false
	if s.cfg.Past != nil {
		_, pastExists = s.cfg.Past.Find(id)
	}
	caps := hubapi.SessionCapabilities{
		Fork:   pastExists,
		Resume: pastExists,
	}
	if !live && s.cfg.Spawner != nil && pastExists {
		caps.Send = true
	}
	if !caps.Send && !caps.Steer && !caps.Interrupt && !caps.Compact && !caps.Clear && !caps.Fork && !caps.Resume && !caps.Shutdown && !caps.ChangeModel {
		if live {
			caps.ReadOnlyReason = "live session source is unavailable"
		} else {
			caps.ReadOnlyReason = "session is not live and cannot be resumed"
		}
	}
	return caps
}
