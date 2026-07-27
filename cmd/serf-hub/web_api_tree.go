package main

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
)

var (
	hubBuildNavigationTree       = hubcore.BuildTreeWithProjects
	hubDeriveNavigationAttention = hubcore.DeriveAttention
	hubNormalizeTreeState        = hubcore.NormalizeState
	hubAppThreadRef              = hubRefFromAppThread
	hubNavigationInputs          = (*WebServer).navigationSnapshotInputs
	hubLiveTreeTitle             = liveTitle
	hubIsSessionLive             = (*WebServer).isLive
	hubTreeWorkspaceData         = (*WebServer).workspaceData
	hubTreeAttentionRank         = hubapi.AttentionRank
)

type navigationSnapshot struct {
	metas               []schema.SessionMeta
	live                []hubcore.LiveEntry
	projects            map[string]identifier.Project
	remoteOwnership     map[string]favoriteRemoteOwnership
	remoteSources       map[string]hubcore.RemoteSourceSnapshot
	remoteIncompleteIDs map[string]struct{}
	remoteGeneration    uint64
}

type favoriteRemoteOwnership struct {
	sourceID string
	complete bool
}

type remoteThreadFetch struct {
	threads    []appwire.Thread
	complete   bool
	sources    map[string]hubcore.RemoteSourceSnapshot
	generation uint64
}

// notifyTreeChanged broadcasts serf/tree/changed to every connected client so
// the sidebar refetches /api/tree (spec §7.3, debounced client-side). It is
// wired as (part of) the onChange hook for Roster and PastIndex — both
// already gate their callback on an actual content-fingerprint delta (a
// daemon appeared/disappeared/changed liveness; a session appeared/ended/
// changed in the past index), so this fires only on a real change, never on
// a no-op probe/rebuild cycle.
//
// The invariant across every caller of this function (directly or via
// notifyMutation) is: a successful user-initiated mutation broadcasts
// exactly once, never zero, never two; a failed or genuinely no-op request
// broadcasts zero. Archive and favorite call it unconditionally via
// WebServer.notifyMutation, since ArchiveStore/FavoriteStore never route
// through PastIndex at all. Rename and project-delete edit PastIndex
// directly and normally get their one broadcast for free from its composed
// hook — but PastIndex.UpdateMeta/Rebuild report whether that hook actually
// fired, and those handlers call this directly, conditionally, as a
// compensating broadcast on the paths where it didn't (see
// refreshRenamedMeta, handleAPIRename's ended-session path, and
// handleAPIProjectDelete) — never unconditionally, which would double-fire
// on top of the hook.
func notifyTreeChanged(server *appserver.Server) {
	server.BroadcastAll(appwire.NotifySerfTreeChanged, map[string]string{})
}

// notifyMutation nudges the attention watcher (if configured) and
// unconditionally broadcasts serf/tree/changed. It exists for mutations
// whose store never routes through Roster/PastIndex's own composed onChange
// hook — archive and favorite decisions live in ArchiveStore/FavoriteStore —
// so they need an explicit, unconditional broadcast every time. Rename and
// project-delete do NOT use this: they edit PastIndex directly and call
// notifyTreeChanged conditionally instead (see its doc comment) — calling
// this unconditionally would double-broadcast whenever PastIndex's own hook
// already fired.
func (s *WebServer) notifyMutation() {
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	notifyTreeChanged(s.appRPC)
}

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
	// summary=1: badge counts only. Notification clients poll this on init and
	// reconnect; serializing the full tree (megabytes on large hubs) just to
	// read attentionSummary was the single biggest avoidable transfer.
	if r.URL.Query().Get("summary") == "1" {
		_, attentionSummary := s.memoTree(r.Context())
		writeAPIJSON(w, http.StatusOK, struct {
			GeneratedAt time.Time `json:"generated_at"`
			// serf:naming-ignore
			AttentionSummary hubapi.AttentionSummary `json:"attentionSummary"` // camelCase: see hubapi.AttentionSummary's doc
		}{time.Now().UTC(), hubAttentionSummaryFromCore(attentionSummary)})
		return
	}
	tree, attentionSummary, live, authority := s.memoTreeWithAuthority(r.Context())
	decisions, err := s.favoriteDecisions()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store error: "+err.Error())
		return
	}
	favs := hubcore.ClassifyFavoriteDecisions(decisions, authority).Presentation
	resp := hubapi.TreeResponse{
		GeneratedAt:      time.Now().UTC(),
		Sources:          s.apiTreeSources(),
		AttentionSummary: hubAttentionSummaryFromCore(attentionSummary),
	}
	for _, n := range tree.Live {
		if !treeNodeCanActLive(n) {
			continue
		}
		resp.Live = append(resp.Live, s.apiTreeNodeTier("live", "", "live", favs, n))
	}
	seenProjectRefs := map[string]bool{}
	projectIndexes := map[string]int{}
	buckets := navigationProjectBuckets(tree)
	// TestRuns takes precedence over ArchivedProjects (round-2 B6): a project
	// where every session carries Origin=="test" is routed there even if it
	// would otherwise also qualify as archived. Every branch marks
	// seenProjectRefs so none of its sessions re-surface as an orphan-live
	// "project" below; only the active (non-archived, non-test) branch
	// populates projectIndexes, since that's the only bucket the orphan-live
	// loop can append into (it indexes into resp.Projects specifically).
	for _, p := range buckets.testRuns {
		for _, n := range projectSessions(p) {
			markTreeNodeIDs(seenProjectRefs, n)
		}
		resp.TestRuns = append(resp.TestRuns, s.apiTreeProject("project", favs, p))
	}
	for _, p := range buckets.archived {
		for _, n := range projectSessions(p) {
			markTreeNodeIDs(seenProjectRefs, n)
		}
		// Archived projects ship as stubs: the archive is unbounded, so its
		// sessions never ride in the snapshot. Sessions stays nil (wire:
		// null) and SessionCount carries the row count; the sidebar
		// lazy-loads the full project from /api/tree/project?key= on expand.
		stub := s.apiTreeProject("project", favs, p)
		stub.SessionCount = p.TotalSessionCount()
		stub.Sessions = nil
		resp.ArchivedProjects = append(resp.ArchivedProjects, stub)
	}
	for _, p := range buckets.active {
		projectIndexes[p.Key] = len(resp.Projects)
		ap := s.apiTreeProject("project", favs, p)
		for _, n := range projectSessions(p) {
			markTreeNodeIDs(seenProjectRefs, n)
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
		projectName := "(no project)"
		key := "no-project"
		workingDir := ""
		if le.Project.ID != "" {
			key = le.Project.ID
			workingDir = le.Project.CanonicalPath
			projectName = filepath.Base(workingDir)
		}
		node := hubcore.TreeNode{
			ID:        le.SessionID,
			Title:     hubLiveTreeTitle(le.SessionID, le, s.cfg.Past),
			Project:   projectName,
			State:     hubNormalizeTreeState(le.Status),
			Kind:      "session",
			CreatedAt: le.StartedAt,
			UpdatedAt: le.StartedAt,
			Age:       hubcore.AgeString(le.StartedAt),
		}
		apiNode := s.apiTreeNodeTier("project", key, "live", favs, node)
		if idx, ok := projectIndexes[key]; ok {
			p := &resp.Projects[idx]
			p.Sessions = append(p.Sessions, apiNode)
			if hubTreeAttentionRank(node.State) > hubTreeAttentionRank(p.RollupState) {
				p.RollupState = node.State
			}
			continue
		}
		projectIndexes[key] = len(resp.Projects)
		resp.Projects = append(resp.Projects, hubapi.TreeProject{
			Key:         key,
			Name:        projectName,
			WorkingDir:  workingDir,
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
	for _, n := range tree.FavoriteCandidates() {
		if n.Kind != "session" && n.Kind != "fork" || !favs[hubcore.ArchiveKey{Kind: "session", ID: n.ID}] {
			continue
		}
		if needsYouIDs[hubRefFromTreeNodeID(n.ID).SessionID] {
			continue
		}
		resp.Favorites = append(resp.Favorites, s.apiTreeNodeTier("pinned", "", "pinned", favs, n))
	}
	sort.SliceStable(resp.Favorites, func(i, j int) bool {
		return resp.Favorites[i].UpdatedAt.After(resp.Favorites[j].UpdatedAt)
	})

	writeAPIJSON(w, http.StatusOK, resp)
}

// memoTree returns the memoized full tree + attention summary, single-sourced
// so callers never diverge on what "the tree" is. The full tree endpoint uses
// memoTreeWithAuthority below when it also needs the exact raw snapshot used
// to build this value.
func (s *WebServer) memoTree(ctx context.Context) (hubcore.Tree, appwire.AttentionSummary) {
	tree, summary, _, _ := s.memoTreeWithAuthority(ctx)
	return tree, summary
}

func (s *WebServer) memoTreeWithAuthority(ctx context.Context) (hubcore.Tree, appwire.AttentionSummary, []hubcore.LiveEntry, hubcore.FavoriteAuthority) {
	inputsVersion := uint64(0)
	if s.cfg.Inputs != nil {
		inputsVersion = s.cfg.Inputs.Load()
	}
	snapshot := s.navigationSnapshot(ctx)
	decisions := s.archiveDecisions()
	key := hubcore.TreeCacheKey{InputsVersion: inputsVersion, RemoteGeneration: snapshot.remoteGeneration}
	value := s.treeCache.Get(key, time.Now(), func() hubcore.TreeCacheValue {
		t := hubBuildNavigationTree(snapshot.metas, snapshot.live, decisions, snapshot.projects)
		_, sum := hubDeriveNavigationAttention(snapshot.metas, snapshot.live, decisions)
		return hubcore.TreeCacheValue{
			Tree:              t,
			AttentionSummary:  sum,
			Live:              snapshot.live,
			FavoriteAuthority: favoriteAuthorityForNavigation(snapshot, t),
		}
	})
	return value.Tree, value.AttentionSummary, value.Live, value.FavoriteAuthority
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
	query := r.URL.Query()
	pageRequested := query.Has("tier") || query.Has("offset") || query.Has("limit")
	tier := query.Get("tier")
	offset, limit := 0, hubcore.SidebarSessionPageSize
	if pageRequested {
		switch tier {
		case "current", "recent", "archived":
		default:
			writeAPIError(w, http.StatusBadRequest, "tier must be current, recent, or archived")
			return
		}
		if raw := query.Get("offset"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeAPIError(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			offset = parsed
		}
		if raw := query.Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > hubcore.SidebarSessionPageSize {
				writeAPIError(w, http.StatusBadRequest, "limit must be between 1 and 50")
				return
			}
			limit = parsed
		}
	}
	tree, _, _, authority := s.memoTreeWithAuthority(r.Context())
	decisions, err := s.favoriteDecisions()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store error: "+err.Error())
		return
	}
	favs := hubcore.ClassifyFavoriteDecisions(decisions, authority).Presentation
	projects := navigationProjectBuckets(tree).all()
	for _, p := range projects {
		if p.Key == key {
			if pageRequested {
				rows, remaining, ok := p.Page(tier, offset, limit)
				if !ok {
					writeAPIError(w, http.StatusBadRequest, "invalid project page")
					return
				}
				writeAPIJSON(w, http.StatusOK, s.apiTreeProjectPage("project", favs, p, tier, offset, rows, remaining))
				return
			}
			writeAPIJSON(w, http.StatusOK, s.apiTreeProject("project", favs, p))
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "project not found")
}

type navigationProjectBucket struct {
	active   []hubcore.TreeProject
	archived []hubcore.TreeProject
	testRuns []hubcore.TreeProject
}

func navigationProjectBuckets(tree hubcore.Tree) navigationProjectBucket {
	buckets := navigationProjectBucket{}
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		switch {
		case p.IsTestRun:
			buckets.testRuns = append(buckets.testRuns, p)
		case p.IsArchived:
			buckets.archived = append(buckets.archived, p)
		default:
			buckets.active = append(buckets.active, p)
		}
	}
	return buckets
}

func (b navigationProjectBucket) all() []hubcore.TreeProject {
	projects := make([]hubcore.TreeProject, 0, len(b.active)+len(b.archived)+len(b.testRuns))
	projects = append(projects, b.active...)
	projects = append(projects, b.archived...)
	projects = append(projects, b.testRuns...)
	return projects
}

func (s *WebServer) navigationSnapshot(ctx context.Context) navigationSnapshot {
	return hubNavigationInputs(s, ctx)
}

func (s *WebServer) navigationSnapshotInputs(ctx context.Context) navigationSnapshot {
	var live []hubcore.LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	var metas []schema.SessionMeta
	if s.cfg.Past != nil {
		metas = s.cfg.Past.AllMetas()
	}
	fetch := s.remoteThreadFetch(ctx)
	carriedProjects := make(map[string]identifier.Project)
	for _, thread := range fetch.threads {
		meta, entry, ok := appThreadTreeEntries(thread)
		if !ok {
			continue
		}
		metas = append(metas, meta)
		if entry.Project.ID != "" && identifier.ValidateProjectID(entry.Project.ID) == nil && entry.WorkingDir != "" {
			carriedProjects[entry.WorkingDir] = entry.Project
		}
		if appThreadTreeLive(thread) {
			live = append(live, entry)
		}
	}
	// Resolve live working directories once at ingestion. BuildTree and the
	// orphan-live projection reuse this carried identity rather than resolving
	// in grouping or rendering loops.
	projects := hubcore.ResolveProjectMap(metas, live)
	maps.Copy(projects, carriedProjects)
	for i := range live {
		if project, ok := projects[live[i].WorkingDir]; ok {
			live[i].Project = project
		}
	}
	incompleteIDs := make(map[string]struct{})
	for _, source := range fetch.sources {
		for _, id := range source.IncompleteIDs {
			incompleteIDs[id] = struct{}{}
		}
	}
	return navigationSnapshot{
		metas:               metas,
		live:                live,
		projects:            projects,
		remoteOwnership:     favoriteRemoteOwnerships(fetch.threads),
		remoteSources:       fetch.sources,
		remoteIncompleteIDs: incompleteIDs,
		remoteGeneration:    fetch.generation,
	}
}

func (s *WebServer) navigationTreeInputs(ctx context.Context) ([]schema.SessionMeta, []hubcore.LiveEntry, map[string]identifier.Project) {
	snapshot := s.navigationSnapshotInputs(ctx)
	return snapshot.metas, snapshot.live, snapshot.projects
}

// remoteTreeThreads returns the remote-source thread list the tree walk
// folds into its metas/live inputs. When a RemoteThreadCache is configured
// (production — see main.go), it reads the cache instead of performing a
// synchronous network walk, so a tree render never blocks on a remote hop.
// Tests that construct a WebServer without a cache fall back to the old
// synchronous behavior via refreshRemoteThreads.
func (s *WebServer) remoteThreadFetch(ctx context.Context) remoteThreadFetch {
	if s.cfg.RemoteThreadCache != nil {
		snapshot := s.cfg.RemoteThreadCache.Snapshot()
		return remoteThreadFetch{
			threads:    snapshot.Threads,
			complete:   snapshot.Complete,
			sources:    snapshot.Sources,
			generation: snapshot.Generation,
		}
	}
	fetch := s.refreshRemoteThreadSnapshot(ctx)
	fetch.generation = s.remoteFetchGeneration.Add(1)
	return fetch
}

func (s *WebServer) remoteTreeThreads(ctx context.Context) []appwire.Thread {
	return s.remoteThreadFetch(ctx).threads
}

// refreshRemoteThreads performs the synchronous walk across every configured
// remote source: it lists each source's threads (via listThreadsWithFallback,
// which retains the last-known-good result across transient errors) and
// backfills each thread's Source and Serf.Ref. This used to run inline on
// every /api/tree request (as remoteTreeThreads); it now runs on a background
// ~30s ticker + poke (main.go), Storing its result into a RemoteThreadCache
// for remoteTreeThreads to read.
func (s *WebServer) refreshRemoteThreads(ctx context.Context) []appwire.Thread {
	return s.refreshRemoteThreadSnapshot(ctx).threads
}

func (s *WebServer) refreshRemoteThreadSnapshot(ctx context.Context) remoteThreadFetch {
	if s.sources == nil {
		return remoteThreadFetch{complete: true, sources: map[string]hubcore.RemoteSourceSnapshot{}}
	}
	s.ensureManagedCodexSources(ctx)
	var threads []appwire.Thread
	complete := true
	sources := make(map[string]hubcore.RemoteSourceSnapshot)
	for _, source := range s.sources.All() {
		if source.ID() == "local" {
			continue
		}
		listed, listComplete := s.listRemoteSourceWithFallbackState(ctx, source)
		if !listComplete {
			complete = false
		}
		incompleteIDs := make(map[string]struct{})
		normalized := make([]appwire.Thread, 0, len(listed.threads))
		for _, thread := range listed.threads {
			rowRef, rowRefOK := appThreadTreeRef(thread)
			sourceConflict := thread.Source != "" && thread.Source != source.ID()
			if sourceConflict && rowRefOK {
				incompleteIDs[rowRef.String()] = struct{}{}
			}
			if rowRefOK && rowRef.SourceID != source.ID() {
				incompleteIDs[rowRef.String()] = struct{}{}
			}
			if thread.Serf.Ref != "" {
				if _, err := appwire.ParseRef(thread.Serf.Ref); err != nil && rowRefOK {
					incompleteIDs[rowRef.String()] = struct{}{}
				}
			}
			thread.Source = source.ID()
			if thread.Serf.Ref == "" {
				threadID := strutil.FirstNonEmpty(thread.ID, thread.SessionID)
				if threadID != "" {
					thread.Serf.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
					if sourceConflict {
						incompleteIDs[thread.Serf.Ref] = struct{}{}
					}
				}
			}
			if rawParent := strings.TrimSpace(thread.Serf.ParentRef); rawParent != "" {
				parent, err := appwire.ParseRef(rawParent)
				if err != nil || parent.SourceID != source.ID() {
					if ref, ok := appThreadTreeRef(thread); ok {
						incompleteIDs[ref.String()] = struct{}{}
					}
				}
			}
			if _, _, ok := appThreadTreeEntries(thread); !ok {
				if ref, ok := appThreadTreeRef(thread); ok {
					incompleteIDs[ref.String()] = struct{}{}
				}
			}
			normalized = append(normalized, thread)
			threads = append(threads, thread)
		}
		invalid := make([]string, 0, len(incompleteIDs))
		for id := range incompleteIDs {
			invalid = append(invalid, id)
		}
		sort.Strings(invalid)
		sources[source.ID()] = hubcore.RemoteSourceSnapshot{
			Threads:       append([]appwire.Thread(nil), normalized...),
			Complete:      listComplete,
			IncompleteIDs: invalid,
		}
	}
	return remoteThreadFetch{threads: threads, complete: complete, sources: sources}
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
	threads, _ := s.listThreadsWithFallbackState(ctx, source)
	return threads
}

func (s *WebServer) listThreadsWithFallbackState(ctx context.Context, source sourceThreadLister) ([]appwire.Thread, bool) {
	result, complete := s.listRemoteSourceWithFallbackState(ctx, source)
	return result.threads, complete
}

type remoteSourceFetch struct {
	threads []appwire.Thread
}

func (s *WebServer) listRemoteSourceWithFallbackState(ctx context.Context, source sourceThreadLister) (remoteSourceFetch, bool) {
	var threads []appwire.Thread
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		resp, err := source.ListThreads(ctx, appwire.ThreadListParams{IncludeSubagents: true, Cursor: cursor})
		if err != nil {
			return remoteSourceFetch{threads: s.lastGoodThreadsForSource(source.ID())}, false
		}
		threads = append(threads, resp.Data...)
		next := strings.TrimSpace(resp.NextCursor)
		if next == "" {
			s.storeLastGoodThreads(source.ID(), threads)
			return remoteSourceFetch{threads: append([]appwire.Thread(nil), threads...)}, true
		}
		if _, repeated := seenCursors[next]; repeated {
			return remoteSourceFetch{threads: s.lastGoodThreadsForSource(source.ID())}, false
		}
		seenCursors[next] = struct{}{}
		cursor = next
	}
}

func (s *WebServer) lastGoodThreadsForSource(sourceID string) []appwire.Thread {
	s.lastGoodMu.Lock()
	defer s.lastGoodMu.Unlock()
	if s.lastGoodThreads == nil {
		s.lastGoodThreads = map[string][]appwire.Thread{}
	}
	return append([]appwire.Thread(nil), s.lastGoodThreads[sourceID]...)
}

func (s *WebServer) storeLastGoodThreads(sourceID string, threads []appwire.Thread) {
	s.lastGoodMu.Lock()
	defer s.lastGoodMu.Unlock()
	if s.lastGoodThreads == nil {
		s.lastGoodThreads = map[string][]appwire.Thread{}
	}
	s.lastGoodThreads[sourceID] = append([]appwire.Thread(nil), threads...)
}

func (s *WebServer) ensureManagedCodexSources(ctx context.Context) {
	_ = ensureManagedCodexSources(ctx, s.cfg, s.sources, appwire.ThreadListParams{})
}

func appThreadTreeEntries(thread appwire.Thread) (schema.SessionMeta, hubcore.LiveEntry, bool) {
	ref, ok := appThreadTreeRef(thread)
	if !ok {
		return schema.SessionMeta{}, hubcore.LiveEntry{}, false
	}
	project := identifier.Project{}
	if thread.ProjectPath != "" && identifier.ValidateProjectID(thread.ProjectID) == nil {
		project = identifier.Project{ID: thread.ProjectID, CanonicalPath: thread.ProjectPath}
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
		ParentSessionID: appThreadTreeParentSessionID(thread, ref),
		IsSubagent:      thread.Serf.Kind == "subagent",
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
		Project:   project,
	}
	return meta, entry, true
}

// appThreadTreeParentSessionID translates the remote thread lineage into the
// same ref-valued metadata used by the local tree. ParentRef is authoritative
// for Serf children; ForkedFromID is the Codex fork lineage fallback.
func appThreadTreeParentSessionID(thread appwire.Thread, childRef appwire.Ref) string {
	raw := strings.TrimSpace(thread.Serf.ParentRef)
	if raw == "" {
		raw = strings.TrimSpace(thread.ForkedFromID)
	}
	if raw == "" {
		return ""
	}
	if parentRef, err := appwire.ParseRef(raw); err == nil {
		return parentRef.String()
	}
	return appwire.Ref{SourceID: childRef.SourceID, ThreadID: raw}.String()
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
func hubAttentionSummaryFromCore(sum appwire.AttentionSummary) hubapi.AttentionSummary {
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

// hubUsageFromAppwire maps appwire.SerfUsage to hubapi's flattened Usage type
// so hubapi need not depend on appwire (mirrors the GoalStatus flattening
// precedent on hubapi.SessionDetail). Returns nil when u is nil.
func hubUsageFromAppwire(u *appwire.SerfUsage) *hubapi.Usage {
	if u == nil {
		return nil
	}
	return &hubapi.Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens,
		TotalTokens:     u.TotalTokens,
	}
}

func hubDetailFromAppThread(thread appwire.Thread) hubapi.SessionDetail {
	ref := hubAppThreadRef(thread)
	state := hubNormalizeTreeState(thread.Status.Type)
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
		Ref:                 ref.String(),
		HostID:              ref.HostID,
		SessionID:           ref.SessionID,
		Title:               title,
		State:               state,
		Live:                live,
		Project:             project,
		WorkingDir:          thread.CWD,
		Model:               thread.ModelProvider,
		Profile:             thread.Serf.Profile,
		TurnCount:           completedTurnCount(thread.Turns),
		ActiveTurnID:        activeTurnIDFromAppwireThread(thread),
		ContextPressure:     thread.Serf.ContextPressure,
		ContextUsed:         thread.Serf.ContextUsed,
		ContextWindow:       thread.Serf.ContextWindow,
		ContextRemaining:    thread.Serf.ContextRemaining,
		Capabilities:        hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
		WorkMillis:          thread.Serf.WorkMillis,
		Usage:               hubUsageFromAppwire(thread.Serf.Usage),
		ActiveTurnStartedAt: thread.Serf.ActiveTurnStartedAt,
		FailedToolCalls:     thread.Serf.FailedToolCalls,
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

// markTreeNodeIDs records a top-level row and every direct descendant already
// projected inside it. Live child daemons are not independently routable, and
// must not be re-added by the orphan-live fallback as separate project rows.
func markTreeNodeIDs(seen map[string]bool, n hubcore.TreeNode) {
	if n.ID != "" {
		seen[n.ID] = true
	}
	for _, child := range n.Children {
		markTreeNodeIDs(seen, child)
	}
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
		IsArchived:      p.IsArchived,
		Favorite:        favs[hubcore.ArchiveKey{Kind: "project", ID: p.Key}],
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

func (s *WebServer) apiTreeProjectPage(
	scope string,
	favs map[hubcore.ArchiveKey]bool,
	p hubcore.TreeProject,
	tier string,
	offset int,
	rows []hubcore.TreeNode,
	remaining int,
) hubapi.TreeProjectPage {
	page := hubapi.TreeProjectPage{Key: p.Key, Tier: tier, Offset: offset, Remaining: remaining}
	for _, n := range rows {
		page.Sessions = append(page.Sessions, s.apiTreeNodeTier(scope, p.Key, tier, favs, n))
	}
	return page
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
// decisions. A store read failure is returned to the request instead of being
// turned into an empty decision set. The returned map is computed once per
// tree request and threaded through apiTreeProject/apiTreeNodeTier so a
// node-count-sized page never opens the favorite store more than once.
func (s *WebServer) favoriteDecisions() (map[hubcore.ArchiveKey]bool, error) {
	if s.cfg.Favorite == nil {
		return map[hubcore.ArchiveKey]bool{}, nil
	}
	f, err := s.cfg.Favorite.Favorites()
	if err != nil {
		return nil, err
	}
	return f, nil
}

func favoriteAuthorityForNavigation(snapshot navigationSnapshot, tree hubcore.Tree) hubcore.FavoriteAuthority {
	topLevel := hubcore.TopLevelSessionIDs(snapshot.metas)
	lineage := favoriteLineageQualities(snapshot.metas)
	metaIDs := make(map[string]struct{}, len(snapshot.metas))
	authority := hubcore.FavoriteAuthority{}
	for _, meta := range snapshot.metas {
		if meta.ID == "" {
			continue
		}
		metaIDs[meta.ID] = struct{}{}
		_, isTopLevel := topLevel[meta.ID]
		authority.Sessions = append(authority.Sessions, hubcore.FavoriteSessionAuthority{
			ID:       meta.ID,
			Aliases:  favoriteSessionAliases(meta.ID),
			TopLevel: isTopLevel,
			Lineage:  lineage[meta.ID],
			Source:   favoriteSessionSourceQuality(meta.ID, snapshot.remoteOwnership, snapshot.remoteSources, snapshot.remoteIncompleteIDs),
		})
	}
	for _, entry := range snapshot.live {
		if entry.SessionID == "" {
			continue
		}
		if _, exists := metaIDs[entry.SessionID]; exists {
			continue
		}
		authority.Sessions = append(authority.Sessions, hubcore.FavoriteSessionAuthority{
			ID:       entry.SessionID,
			Aliases:  favoriteSessionAliases(entry.SessionID),
			TopLevel: true,
			Lineage:  hubcore.FavoriteAuthorityComplete,
			Source:   favoriteSessionSourceQuality(entry.SessionID, snapshot.remoteOwnership, snapshot.remoteSources, snapshot.remoteIncompleteIDs),
		})
	}
	authority.Projects = favoriteProjectAuthorities(snapshot)
	authority.Nodes = tree.FavoriteNodeAuthorities()
	return authority
}

func favoriteSessionAliases(id string) []string {
	aliases := []string{id}
	if ref, err := hubapi.ParseRef(id); err == nil && ref.HostID == "local" {
		aliases = append(aliases, ref.SessionID, "local:"+ref.SessionID)
	} else if !strings.Contains(id, ":") {
		aliases = append(aliases, "local:"+id)
	}
	return uniqueStrings(aliases)
}

func favoriteSessionSourceQuality(id string, remoteOwnership map[string]favoriteRemoteOwnership, remoteSources map[string]hubcore.RemoteSourceSnapshot, incompleteIDs map[string]struct{}) hubcore.FavoriteAuthorityQuality {
	if strings.TrimSpace(id) == "" {
		return hubcore.FavoriteAuthorityIncomplete
	}
	if _, incomplete := incompleteIDs[id]; incomplete {
		return hubcore.FavoriteAuthorityIncomplete
	}
	ownership, isRemote := remoteOwnership[id]
	if !isRemote {
		return hubcore.FavoriteAuthorityComplete
	}
	if !ownership.complete {
		return hubcore.FavoriteAuthorityIncomplete
	}
	ref, err := appwire.ParseRef(id)
	if err != nil {
		return hubcore.FavoriteAuthorityIncomplete
	}
	if ownership.sourceID != ref.SourceID {
		return hubcore.FavoriteAuthorityIncomplete
	}
	source, sourceKnown := remoteSources[ownership.sourceID]
	if !sourceKnown || !source.Complete {
		return hubcore.FavoriteAuthorityIncomplete
	}
	for _, thread := range source.Threads {
		if sourceID := strings.TrimSpace(thread.Source); sourceID != "" && sourceID != ref.SourceID {
			continue
		}
		threadRef, ok := favoriteRemoteThreadRef(thread)
		if ok && threadRef == ref {
			return hubcore.FavoriteAuthorityComplete
		}
	}
	return hubcore.FavoriteAuthorityIncomplete
}

func favoriteRemoteOwnerships(threads []appwire.Thread) map[string]favoriteRemoteOwnership {
	ownerships := make(map[string]favoriteRemoteOwnership)
	for _, thread := range threads {
		ref, ok := appThreadTreeRef(thread)
		if !ok || ref.SourceID == "local" {
			continue
		}
		sourceID := strings.TrimSpace(thread.Source)
		if sourceID == "" {
			sourceID = ref.SourceID
		}
		complete := sourceID == ref.SourceID
		if rawRef := strings.TrimSpace(thread.Serf.Ref); rawRef != "" {
			parsed, err := appwire.ParseRef(rawRef)
			if err != nil || parsed != ref {
				complete = false
			}
		}
		candidate := favoriteRemoteOwnership{sourceID: sourceID, complete: complete}
		if previous, exists := ownerships[ref.String()]; exists {
			if previous.sourceID != candidate.sourceID || !previous.complete || !candidate.complete {
				candidate = favoriteRemoteOwnership{complete: false}
			}
		}
		ownerships[ref.String()] = candidate
	}
	return ownerships
}

func favoriteRemoteThreadRef(thread appwire.Thread) (appwire.Ref, bool) {
	if strings.TrimSpace(thread.Serf.Ref) != "" {
		ref, err := appwire.ParseRef(thread.Serf.Ref)
		if err != nil {
			return appwire.Ref{}, false
		}
		return ref, true
	}
	return appThreadTreeRef(thread)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func favoriteLineageQualities(metas []schema.SessionMeta) map[string]hubcore.FavoriteAuthorityQuality {
	qualities := make(map[string]hubcore.FavoriteAuthorityQuality, len(metas))
	byID := make(map[string]int, len(metas))
	children := make(map[string][]string)
	for _, meta := range metas {
		if meta.ID == "" {
			continue
		}
		byID[meta.ID]++
		qualities[meta.ID] = hubcore.FavoriteAuthorityComplete
		if meta.ParentSessionID != "" && !meta.IsSubagent {
			children[meta.ParentSessionID] = append(children[meta.ParentSessionID], meta.ID)
		}
	}
	markIncomplete := func(id string) {
		if id != "" {
			qualities[id] = hubcore.FavoriteAuthorityIncomplete
		}
	}
	for _, meta := range metas {
		if meta.ID == "" {
			continue
		}
		if meta.IsSubagent && meta.ParentSessionID == "" {
			markIncomplete(meta.ID)
		}
		if meta.ParentSessionID != "" {
			if meta.ParentSessionID == meta.ID || byID[meta.ParentSessionID] != 1 {
				markIncomplete(meta.ID)
			}
		}
	}
	for parentID, childIDs := range children {
		if len(uniqueStrings(childIDs)) > 1 {
			markIncomplete(parentID)
			for _, childID := range childIDs {
				markIncomplete(childID)
			}
		}
	}
	for _, meta := range metas {
		if meta.ID == "" || meta.ParentSessionID == "" {
			continue
		}
		seen := map[string]struct{}{}
		current := meta.ID
		for current != "" {
			if _, ok := seen[current]; ok {
				markIncomplete(current)
				for id := range seen {
					markIncomplete(id)
				}
				break
			}
			seen[current] = struct{}{}
			parent, ok := findMetaByID(metas, current)
			if !ok {
				break
			}
			current = parent.ParentSessionID
		}
	}
	return qualities
}

func findMetaByID(metas []schema.SessionMeta, id string) (schema.SessionMeta, bool) {
	for _, meta := range metas {
		if meta.ID == id {
			return meta, true
		}
	}
	return schema.SessionMeta{}, false
}

func favoriteProjectAuthorities(snapshot navigationSnapshot) []hubcore.FavoriteProjectAuthority {
	claims := make(map[string]hubcore.FavoriteProjectAuthority)
	for path, project := range snapshot.projects {
		if project.ID == "" {
			continue
		}
		canonicalPath := project.CanonicalPath
		if canonicalPath == "" {
			canonicalPath = path
		}
		owners := make(map[string]favoriteProjectOwnerEvidence)
		for _, meta := range snapshot.metas {
			if hubcore.EffectiveWorkingDir(meta) == path {
				favoriteProjectOwnerEvidenceAdd(owners, meta.ID, snapshot)
			}
		}
		for _, entry := range snapshot.live {
			if entry.WorkingDir == path {
				favoriteProjectOwnerEvidenceAdd(owners, entry.SessionID, snapshot)
			}
		}
		if len(owners) == 0 {
			owners["local"] = favoriteProjectOwnerEvidence{quality: hubcore.FavoriteAuthorityIncomplete}
		}
		for source, evidence := range owners {
			quality := evidence.quality
			if !evidence.hasIdentity {
				quality = mergeFavoriteAuthorityQuality(quality, hubcore.FavoriteAuthorityIncomplete)
			}
			claimKey := canonicalPath + "\x00" + source
			key := project.ID + "\x00" + claimKey
			if previous, ok := claims[key]; ok {
				previous.Quality = mergeFavoriteAuthorityQuality(previous.Quality, quality)
				claims[key] = previous
			} else {
				claims[key] = hubcore.FavoriteProjectAuthority{ID: project.ID, Quality: quality, ClaimKey: claimKey}
			}
		}
	}
	projects := make([]hubcore.FavoriteProjectAuthority, 0, len(claims))
	for _, claim := range claims {
		projects = append(projects, claim)
	}
	return projects
}

type favoriteProjectOwnerEvidence struct {
	quality     hubcore.FavoriteAuthorityQuality
	hasEvidence bool
	hasIdentity bool
}

func favoriteProjectOwnerEvidenceAdd(owners map[string]favoriteProjectOwnerEvidence, id string, snapshot navigationSnapshot) {
	source := favoriteProjectSourceClaim(id)
	evidence := owners[source]
	quality := favoriteSessionSourceQuality(id, snapshot.remoteOwnership, snapshot.remoteSources, snapshot.remoteIncompleteIDs)
	if evidence.hasEvidence {
		evidence.quality = mergeFavoriteAuthorityQuality(evidence.quality, quality)
	} else {
		evidence.quality = quality
		evidence.hasEvidence = true
	}
	evidence.hasIdentity = evidence.hasIdentity || strings.TrimSpace(id) != ""
	owners[source] = evidence
}

func mergeFavoriteAuthorityQuality(left, right hubcore.FavoriteAuthorityQuality) hubcore.FavoriteAuthorityQuality {
	if left == hubcore.FavoriteAuthorityAmbiguous || right == hubcore.FavoriteAuthorityAmbiguous {
		return hubcore.FavoriteAuthorityAmbiguous
	}
	if left == hubcore.FavoriteAuthorityIncomplete || right == hubcore.FavoriteAuthorityIncomplete {
		return hubcore.FavoriteAuthorityIncomplete
	}
	if left == hubcore.FavoriteAuthorityComplete && right == hubcore.FavoriteAuthorityComplete {
		return hubcore.FavoriteAuthorityComplete
	}
	return hubcore.FavoriteAuthorityIncomplete
}

func favoriteProjectSourceClaim(id string) string {
	ref, err := appwire.ParseRef(id)
	if err == nil && ref.SourceID != "" {
		return ref.SourceID
	}
	return "local"
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
		RowID:         rowID,
		Ref:           refText,
		HostID:        ref.HostID,
		SessionID:     ref.SessionID,
		Title:         n.Title,
		Project:       n.Project,
		State:         n.State,
		Kind:          n.Kind,
		Live:          live,
		UpdatedAt:     n.UpdatedAt,
		Age:           n.Age,
		AskPending:    n.AskPending,
		Dormant:       n.Dormant,
		MoreSubagents: n.MoreSubagents,
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
	wd := hubTreeWorkspaceData(s, id)
	if wd.ID == "" {
		return hubapi.SessionDetail{}, false
	}
	// appRefFromRouteID canonicalizes malformed route IDs to a local ref.
	ref, _ := hubapi.ParseRef(appRefFromRouteID(id))
	live := hubIsSessionLive(s, id)
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
		// Seeded from wd here so the ended path (no live source to override
		// them below) still carries these; the live branch's hubDetailFromAppThread
		// replacement keeps its own values from thread.Serf — no clobber.
		WorkMillis:          wd.WorkMillis,
		Usage:               hubUsageFromAppwire(wd.Usage),
		ActiveTurnStartedAt: wd.ActiveTurnStartedAt,
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
		// A live rename lands in the persisted meta before the daemon thread
		// reports the new Name; if the live thread carried no name (detail.Title
		// fell back to the session id), prefer the resolved meta name so the
		// session-detail endpoint agrees with /api/tree (WS3 T25 Bug 2).
		if detail.Title == "" || detail.Title == detail.SessionID {
			if s.cfg.Roster != nil {
				le, _ := s.cfg.Roster.Find(canonicalRouteID(id))
				if resolved := hubLiveTreeTitle(id, le, s.cfg.Past); resolved != "" {
					detail.Title = resolved
				}
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

// apiSessionState is a lean counterpart to apiSessionDetail for the polled
// /state input-strip render. It calls workspaceData once — the polled render
// previously called workspaceData directly AND again transitively through
// apiSessionDetail — and, for a live session, refreshes context/goal/usage/
// work-time fields via a ReadThread that skips the turns array (IncludeTurns:
// false): the input strip never needs the transcript, only the SerfThread
// status fields that ride alongside it regardless of IncludeTurns.
// completedTurnCount(thread.Turns) is always 0 without turns, so TurnCount is
// always taken from wd (which the roster path sets from daemonStatus.Turns,
// and the ended path from the persisted SessionMeta) rather than from the
// lean thread. Branch, ForkLabel, and DivergenceTurn are carried over from
// the wd-seed across the live replacement too: hubDetailFromAppThread never
// sets them from a thread, unlike Model/WorkingDir, which it does set and
// which only fall back to the wd-seed when the live thread's value is empty.
//
// The shared apiSessionDetail (and its IncludeTurns: true) is untouched by
// this function — its JSON-API/action callers keep fetching the full
// transcript and so keep a correct TurnCount independent of this lean path.
func (s *WebServer) apiSessionState(id string) (hubapi.SessionDetail, bool) {
	wd := hubTreeWorkspaceData(s, id)
	if wd.ID == "" {
		return hubapi.SessionDetail{}, false
	}
	// appRefFromRouteID canonicalizes malformed route IDs to a local ref.
	ref, _ := hubapi.ParseRef(appRefFromRouteID(id))
	live := hubIsSessionLive(s, id)
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
		// Seeded from wd here so the ended path (no live source to override
		// them below) still carries these; the live branch's
		// hubDetailFromAppThread replacement keeps its own values from
		// thread.Serf — no clobber.
		WorkMillis:          wd.WorkMillis,
		Usage:               hubUsageFromAppwire(wd.Usage),
		ActiveTurnStartedAt: wd.ActiveTurnStartedAt,
	}
	if detail.Project == "" || detail.Project == "." {
		detail.Project = "(no project)"
	}
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		appRef := appRefFromRouteID(id)
		if source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, appRef, ""); err == nil {
			// Lean read: skip the turns array. completedTurnCount is always 0
			// on an empty Turns slice, so TurnCount below always comes from wd.
			if resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: appRef, IncludeTurns: false}); err == nil {
				appDetail := hubDetailFromAppThread(resp.Thread)
				appDetail.TurnCount = wd.TurnCount
				appDetail.Branch = detail.Branch
				appDetail.ForkLabel = detail.ForkLabel
				appDetail.DivergenceTurn = detail.DivergenceTurn
				if appDetail.Model == "" {
					appDetail.Model = detail.Model
				}
				if appDetail.WorkingDir == "" {
					appDetail.WorkingDir = detail.WorkingDir
				}
				detail = appDetail
			}
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
