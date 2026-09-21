package hub

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

func (s *WebServer) emptyNavigationMutation() hubapi.NavigationMutation {
	return s.navigation.EmptyMutation()
}

func projectKeyForStateDir(stateDir string) string {
	return filepath.Base(filepath.Dir(stateDir))
}

var (
	hubBuildNavigationTree       = hubcore.BuildTreeWithProjects
	hubDeriveNavigationAttention = hubcore.DeriveAttention
	hubNavigationInputs          = (*WebServer).navigationSnapshotInputs
	hubTreeAttentionRank         = hubapi.AttentionRank
)

var _ = hubTreeAttentionRank

type navigationSnapshot struct {
	ownershipErr        error
	metas               []schema.SessionMeta
	live                []hubcore.LiveEntry
	projects            map[string]identifier.Project
	projectIdentities   map[string][]identifier.Project
	projectConflicts    map[string]bool
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
	threads  []appwire.Thread
	complete bool
	sources  map[string]hubcore.RemoteSourceSnapshot
	// sourceGenerations is the per-source identity generation the walk
	// captured immediately before it read each source — the read-time
	// capture the background refresher hands to StoreWalkSnapshot, so a
	// remove or remove/re-add between a source's read and the publish
	// moves the live generation and the read's rows drop. A source absent
	// from the map was unregistered when the
	// walk read it; the publish drops its rows as unowned.
	sourceGenerations map[string]uint64
	generation        uint64
}

// pokeMutationAttention nudges the attention watcher (if configured). It exists for
// mutations whose store never routes through Roster/PastIndex's own composed
// onChange hook — archive and favorite decisions live in
// ArchiveStore/FavoriteStore — so they need an explicit nudge every time.
func pokeMutationAttention(cfg hubcore.WebConfig) {
	if cfg.PokeAttention != nil {
		cfg.PokeAttention()
	}
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

func (s *WebServer) pinSectionAssignments() (map[string]hubcore.SessionPin, error) {
	if s.cfg.PinSections == nil {
		return map[string]hubcore.SessionPin{}, nil
	}
	return s.cfg.PinSections.Assignments()
}

func (s *WebServer) pinSections() ([]hubcore.PinSection, error) {
	if s.cfg.PinSections == nil {
		return []hubcore.PinSection{}, nil
	}
	return s.cfg.PinSections.Sections()
}

func classifySessionPins(assignments map[string]hubcore.SessionPin, authority hubcore.FavoriteAuthority) hubcore.FavoriteRevalidation {
	decisions := make(map[hubcore.ArchiveKey]bool, len(assignments))
	for storedID := range assignments {
		decisions[hubcore.ArchiveKey{Kind: "session", ID: storedID}] = true
	}
	return hubcore.ClassifyFavoriteDecisions(decisions, authority)
}

func canonicalPinAssignments(assignments map[string]hubcore.SessionPin, classified hubcore.FavoriteRevalidation) map[string]hubcore.SessionPin {
	out := make(map[string]hubcore.SessionPin, len(assignments)*2)
	for storedID, assignment := range assignments {
		out[storedID] = assignment
		classification := classified.Classifications[hubcore.ArchiveKey{Kind: "session", ID: storedID}]
		if classification.State == hubcore.FavoriteDecisionValid && classification.CanonicalKey.ID != "" {
			out[classification.CanonicalKey.ID] = assignment
		}
	}
	return out
}

func projectFavoritePresentation(presentation map[hubcore.ArchiveKey]bool) map[hubcore.ArchiveKey]bool {
	projects := make(map[hubcore.ArchiveKey]bool)
	for key, favorite := range presentation {
		if key.Kind == "project" && favorite {
			projects[key] = true
		}
	}
	return projects
}

// projectFavoriteKey is the source-qualified key under which a project favorite
// is registered in navigationBuildInputs.ProjectFavorite. Controller-local
// projects keep their bare ID so existing consumers are unaffected; a remote
// source is prefixed with the host name, so two hosts' projects that share an
// ID do not collide.
func projectFavoriteKey(source, id string) string {
	if source == "" {
		return id
	}
	return source + "\x00" + id
}

// projectFavoriteForSources reports whether any source that owns the project
// holds a favorite decision for it. A project with no recorded sources is the
// controller's own, so it is checked under the bare key.
func projectFavoriteForSources(favorites map[string]bool, project hubcore.TreeProject) bool {
	sources := project.Sources
	if len(sources) == 0 {
		sources = []string{""}
	}
	for _, source := range sources {
		if favorites[projectFavoriteKey(source, project.Key)] {
			return true
		}
	}
	return false
}

// memoTree returns the memoized tree projection retained by mutation handlers
// that still need it. AppWire navigation reads are owned by NavigationService,
// whose webNavigationSource captures a fresh source snapshot.
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

// navigationBuildInputsFromTreeSnapshot is the handoff from request-owned
// snapshot assembly to the pure navigation projector. It accepts every row
// decoration explicitly: the projector never reaches back into WebServer,
// Roster, or a decision store while walking a node tree.
func navigationBuildInputsFromTreeSnapshot(generationID string, revision uint64, tree hubcore.Tree, sources []hubapi.Source, attention hubapi.AttentionSummary, live []hubcore.LiveEntry, sessionFavorites, projectFavorites map[hubcore.ArchiveKey]bool, pinSections []hubcore.PinSection, pinAssignments map[string]hubcore.SessionPin) navigationBuildInputs {
	liveBySession := make(map[string]bool, len(live))
	renameable := make(map[string]bool)
	for _, entry := range live {
		if entry.SessionID != "" {
			for _, alias := range favoriteSessionAliases(entry.SessionID) {
				liveBySession[alias] = true
			}
		}
	}
	sessionFavoriteByID := make(map[string]bool, len(sessionFavorites))
	for key, favorite := range sessionFavorites {
		if key.Kind == "session" && favorite {
			sessionFavoriteByID[key.ID] = true
		}
	}
	projectFavoriteByID := make(map[string]bool, len(projectFavorites))
	for key, favorite := range projectFavorites {
		if key.Kind == "project" && favorite {
			projectFavoriteByID[projectFavoriteKey(key.Source, key.ID)] = true
		}
	}
	var indexRenameable func([]hubcore.TreeNode)
	indexRenameable = func(rows []hubcore.TreeNode) {
		for _, row := range rows {
			if isLocalRouteID(row.ID) {
				renameable[row.ID] = true
			}
			indexRenameable(row.Children)
		}
	}
	indexRenameable(tree.Live)
	indexRenameable(tree.NeedsYou)
	for _, project := range navigationProjectBuckets(tree).all() {
		for _, tier := range []string{"current", "recent", "archived"} {
			rows, _ := project.TierRows(tier)
			indexRenameable(rows)
		}
	}
	// Live daemon constraints override every persisted navigation copy.
	for _, entry := range live {
		if entry.SessionID == "" || entry.Crashed {
			continue
		}
		aliases := favoriteSessionAliases(entry.SessionID)
		if entry.WorkspaceRef != "" {
			aliases = append(aliases, favoriteSessionAliases(entry.WorkspaceRef)...)
		}
		for _, alias := range aliases {
			renameable[alias] = isLocalRouteID(alias) && entry.Status != appwire.ThreadStatusRestartRequired
		}
	}
	return navigationBuildInputs{
		GenerationID:     generationID,
		Revision:         revision,
		Tree:             tree,
		LiveEntries:      append([]hubcore.LiveEntry(nil), live...),
		Sources:          sources,
		AttentionSummary: attention,
		Live:             liveBySession,
		Renameable:       renameable,
		SessionFavorite:  sessionFavoriteByID,
		ProjectFavorite:  projectFavoriteByID,
		PinSections:      pinSections,
		PinAssignments:   pinAssignments,
	}
}

func (s *WebServer) navigationSnapshotInputs(ctx context.Context) navigationSnapshot {
	var live []hubcore.LiveEntry
	var unconfirmedOwnership bool
	var ownershipErr error
	if s.cfg.Roster != nil {
		roster := s.cfg.Roster.Snapshot()
		ownershipErr = roster.OwnershipError
		live = roster.Live
		unconfirmedOwnership = len(roster.Unconfirmed) > 0
	}
	var metas []schema.SessionMeta
	var pastEntries []hubcore.PastEntry
	if s.cfg.Past != nil {
		pastEntries = s.cfg.Past.All()
		metas = make([]schema.SessionMeta, 0, len(pastEntries))
		for _, entry := range pastEntries {
			metas = append(metas, entry.Meta)
		}
	}
	// Healthy navigation snapshots need no persisted ownership scan.
	if unconfirmedOwnership || slices.ContainsFunc(live, func(entry hubcore.LiveEntry) bool {
		return !entry.Crashed && entry.Status == appwire.ThreadStatusRestartRequired
	}) {
		// Persisted delegates have no rendezvous of their own. Preserve the
		// authenticated owner's restart restriction in every navigation projection.
		for _, past := range pastEntries {
			owner, incompatible, err := restartRequiredDaemon(ctx, s.cfg, "", past.Meta.ID)
			if err != nil {
				if ownershipErr == nil {
					ownershipErr = err
				}
				continue
			}
			if !incompatible || owner.SessionID == past.Meta.ID || localSpawnWorkspaceRef(owner.Entry) == localAppRef(past.Meta.ID) {
				continue
			}
			entry := owner.Entry
			entry.ThreadID = past.Meta.ID
			entry.WorkspaceRef = localAppRef(past.Meta.ID)
			entry.WorkingDir = hubcore.EffectiveWorkingDir(past.Meta)
			child := hubcore.LiveEntry{Entry: entry, SessionID: past.Meta.ID, Status: owner.Status}
			live = append(live, child)
		}

	}
	fetch := s.remoteThreadFetch(ctx)
	carriedProjectCandidates := make(map[string]map[string]identifier.Project)
	for _, thread := range fetch.threads {
		meta, entry, ok := appThreadTreeEntries(thread)
		if !ok {
			continue
		}
		metas = append(metas, meta)
		if entry.Project.ID != "" && identifier.ValidateProjectID(entry.Project.ID) == nil && entry.WorkingDir != "" {
			addNavigationProjectCandidate(carriedProjectCandidates, entry.WorkingDir, entry.Project)
		}
		// An offline source keeps its last-known rows visible in metas, but
		// they are not live. apiTreeSources below carries each source's Online
		// flag for the fleet-view UI to derive the offline affordance; that
		// consumer ships with Component 06b (fleet-view UI), so within this
		// change an offline source's rows render as ordinary non-live rows
		// rather than being projected live. An empty Source is treated as online
		// (server-owned local rows), so only source-identified remote rows can
		// be excluded.
		if appThreadTreeLive(thread) && s.sourceOnline(thread.Source) {
			live = append(live, entry)
		}
	}
	// Resolve live working directories once at ingestion. BuildTree and the
	// orphan-live projection reuse this carried identity rather than resolving
	// in grouping or rendering loops.
	resolvedProjects := hubcore.ResolveProjectMap(metas, live)
	projectCandidates := make(map[string]map[string]identifier.Project, len(resolvedProjects)+len(carriedProjectCandidates))
	for path, project := range resolvedProjects {
		addNavigationProjectCandidate(projectCandidates, path, project)
	}
	// A session whose recorded working directory no longer resolves (its
	// worktree or checkout was deleted after the session ended) would
	// otherwise group under the dead path with an empty identity: named after
	// the dead directory's leaf and collapsing onto the shared "no-project"
	// key with every other unresolved path. The past index already knows the
	// canonical project — the state dir it loaded the meta from is named by
	// it — so carry that identity as a fallback for exactly the paths fresh
	// resolution failed on. A path that did resolve keeps its resolved
	// identity; the fallback never overrides it.
	for _, entry := range pastEntries {
		path := hubcore.EffectiveWorkingDir(entry.Meta)
		if path == "" {
			continue
		}
		if _, ok := resolvedProjects[path]; ok {
			continue
		}
		id := filepath.Base(entry.StateDir)
		if identifier.ValidateProjectID(id) != nil {
			continue
		}
		addNavigationProjectCandidate(projectCandidates, path, identifier.Project{ID: id})
	}
	for path, candidates := range carriedProjectCandidates {
		for _, project := range candidates {
			addNavigationProjectCandidate(projectCandidates, path, project)
		}
	}
	projects, projectIdentities, projectConflicts := selectNavigationProjects(projectCandidates)
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
		ownershipErr:        ownershipErr,
		metas:               metas,
		live:                live,
		projects:            projects,
		projectIdentities:   projectIdentities,
		projectConflicts:    projectConflicts,
		remoteOwnership:     favoriteRemoteOwnerships(fetch.threads),
		remoteSources:       fetch.sources,
		remoteIncompleteIDs: incompleteIDs,
		remoteGeneration:    fetch.generation,
	}
}

func addNavigationProjectCandidate(candidates map[string]map[string]identifier.Project, path string, project identifier.Project) {
	if path == "" || project.ID == "" {
		return
	}
	if candidates[path] == nil {
		candidates[path] = make(map[string]identifier.Project)
	}
	key := project.ID + "\x00" + project.CanonicalPath
	candidates[path][key] = project
}

func selectNavigationProjects(candidates map[string]map[string]identifier.Project) (map[string]identifier.Project, map[string][]identifier.Project, map[string]bool) {
	projects := make(map[string]identifier.Project, len(candidates))
	identities := make(map[string][]identifier.Project, len(candidates))
	conflicts := make(map[string]bool)
	for path, byKey := range candidates {
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			identities[path] = append(identities[path], byKey[key])
		}
		if len(keys) == 0 {
			continue
		}
		projects[path] = byKey[keys[0]]
		if len(keys) > 1 {
			conflicts[path] = true
		}
	}
	return projects, identities, conflicts
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
// backfills each thread's Source and Evener.Ref. This used to run inline on
// every navigation read (as remoteTreeThreads); it now runs on a background
// ~30s ticker + poke (main.go), storing its result into a RemoteThreadCache
// for remoteTreeThreads to read.
func (s *WebServer) refreshRemoteThreads(ctx context.Context) []appwire.Thread {
	return s.refreshRemoteThreadSnapshot(ctx).threads
}

func (s *WebServer) refreshRemoteThreadSnapshot(ctx context.Context) remoteThreadFetch {
	if s.sources == nil {
		return remoteThreadFetch{
			complete:          true,
			sources:           map[string]hubcore.RemoteSourceSnapshot{},
			sourceGenerations: map[string]uint64{},
		}
	}
	var threads []appwire.Thread
	complete := true
	sources := make(map[string]hubcore.RemoteSourceSnapshot)
	// readGenerations is the walk's read-time capture: one entry per source,
	// taken immediately before the walk reads it.
	readGenerations := make(map[string]uint64)
	remoteHosts := remoteHostNames(s.cfg)
	for _, source := range s.sources.All() {
		if source.ID() == "local" {
			continue
		}
		// Row ownership follows the generation at READ time, not at walk
		// start: a host that registers mid-walk is
		// captured here, under the registration that owns the rows about to
		// be read, so it still publishes on this tick — while a remove or
		// remove/re-add that lands between this capture and the publish moves
		// the live generation, and the publish drops these rows instead of
		// letting the old registration's rows outlive it or publish under a
		// re-added identity. A source with no generation here was already
		// removed when the walk reached it (the registry snapshot lags the
		// removal); it stays uncaptured, and the publish drops its rows as
		// unowned. Unattached sources are captured too — the last-known-good
		// rows below publish like a live read's.
		var readGeneration uint64
		var captured bool
		if s.cfg.RemoteThreadCache != nil {
			if generation, ok := s.cfg.RemoteThreadCache.SourceGeneration(source.ID()); ok {
				readGenerations[source.ID()] = generation
				readGeneration, captured = generation, true
			}
		}
		// The background walk must not force attachment: an unattached remote host
		// is skipped without a call, so the 30s ticker cannot dial every configured
		// host (component 06, §"What already exists"). The gate is the attached-only
		// lookup, not a check-then-Ensure: a host that drops between the check and
		// the source's own (attached-only) resolution is skipped, never re-attached.
		// A skipped host contributes no fresh rows but keeps the last-known-good
		// rows it already contributed (none for a never-attached host), so its
		// sessions do not blink out of the tree between ticks.
		var listed remoteSourceFetch
		var listComplete bool
		if remoteSourceAttached(s.cfg, remoteHosts, source.ID()) {
			// The retention store joins the read-time generation rule the
			// publish already follows: listRemoteSourceWithFallbackState
			// stores only after ListThreads returns, so a remove whose
			// forgetLastGoodThreads already dropped this source's retained
			// rows — the removal commits while the list is in flight — must
			// not be undone by the late store; the source is gone from the
			// registry, so no later walk would ever prune the entry.
			// Without a cache the store fences on the source instance the
			// walk read under — the read-time identity token the nil-cache
			// shape captures instead of a generation.
			listed, listComplete = s.listRemoteSourceWithFallbackState(ctx, source, func(rows []appwire.Thread) {
				s.storeLastGoodThreadsIfCurrent(source, readGeneration, captured, rows)
			})
		} else {
			listed = remoteSourceFetch{threads: s.lastGoodThreadsForSource(source.ID())}
		}
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
			if thread.Evener.Ref != "" {
				if _, err := appwire.ParseRef(thread.Evener.Ref); err != nil && rowRefOK {
					incompleteIDs[rowRef.String()] = struct{}{}
				}
			}
			thread.Source = source.ID()
			if thread.Evener.Ref == "" {
				threadID := envvars.FirstNonEmpty(thread.ID, thread.SessionID)
				if threadID != "" {
					thread.Evener.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
					if sourceConflict {
						incompleteIDs[thread.Evener.Ref] = struct{}{}
					}
				}
			}
			if rawParent := strings.TrimSpace(thread.Evener.ParentRef); rawParent != "" {
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
	return remoteThreadFetch{threads: threads, complete: complete, sources: sources, sourceGenerations: readGenerations}
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
	// nil retain: this direct-lister seam carries no read-time capture
	// to fence the store with, so a completed list retains unconditionally.
	result, complete := s.listRemoteSourceWithFallbackState(ctx, source, nil)
	return result.threads, complete
}

type remoteSourceFetch struct {
	threads []appwire.Thread
}

// listRemoteSourceWithFallbackState lists source's threads across cursor
// pages, retaining the complete result as last-known-good: through retain
// when the walk supplies one — a store fenced on the registration the read
// happened under, storeLastGoodThreadsIfCurrent — or unconditionally when
// retain is nil. The store runs only after the final page's ListThreads
// returns, which is the window the fence exists for: a removal can commit
// mid-round-trip, after the walk captured its generation but before the
// list came back.
func (s *WebServer) listRemoteSourceWithFallbackState(ctx context.Context, source sourceThreadLister, retain func(rows []appwire.Thread)) (remoteSourceFetch, bool) {
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
			if retain != nil {
				retain(threads)
			} else {
				s.storeLastGoodThreads(source.ID(), threads)
			}
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
	s.setLastGoodThreadsLocked(sourceID, threads)
}

// storeLastGoodThreadsIfCurrent retains threads as source's last-known-good
// rows only while the registration the walk read under still owns the name.
// The walk's read-time identity capture is the cache's SourceGeneration when a
// cache is configured — the same generation the publish compares under the
// cache's lock — and the source instance itself when one is
// not: the nil-cache shape has no generation store to compare against, and
// the instance is the identity the registry hands out before the name becomes
// enumerable (registerSource and newHubSourceRegistry build the source, then
// Add), so "the name still maps to the instance the walk read" makes the same
// ownership claim a generation match makes. The
// check and the write share lastGoodMu, so a retention store and a removal's
// forgetLastGoodThreads serialize: the removal drops the identity first —
// RemoveSource and the source registration's own Remove both run before the
// forget in the remove finish phase — so a store that runs after the removal
// sees the moved or missing identity and skips, while a store that wins the
// race is deleted by the forget that follows. Either order leaves no entry,
// so a host removed while its list was in flight cannot leave its rows
// retained under a name no later walk enumerates — nothing else ever deletes
// an entry, so they would otherwise sit for the process lifetime, and
// across a re-add they would render as the re-added
// host's sessions until its own first successful list.
func (s *WebServer) storeLastGoodThreadsIfCurrent(source appsource.Source, generation uint64, captured bool, threads []appwire.Thread) {
	s.lastGoodMu.Lock()
	defer s.lastGoodMu.Unlock()
	if !s.lastGoodRegistrationOwned(source, generation, captured) {
		return
	}
	s.setLastGoodThreadsLocked(source.ID(), threads)
}

// lastGoodRegistrationOwned reports whether the read-time capture still
// matches the source's live registration. Callers hold lastGoodMu; the
// identity lookups nest inside it, which is safe because no path takes the
// two locks in the reverse order — the removal calls RemoveSource,
// sources.Remove, and forgetLastGoodThreads sequentially, never nested.
func (s *WebServer) lastGoodRegistrationOwned(source appsource.Source, generation uint64, captured bool) bool {
	if s.cfg.RemoteThreadCache == nil {
		// No cache means no generation store to compare against; the source
		// instance the walk enumerated is the identity token. A removed host
		// has no registration to match, and a re-added name maps to the fresh
		// instance its re-add registered — both fail the comparison exactly
		// the way a moved or missing generation does on the cached path.
		current, ok := s.sources.Source(source.ID())
		return ok && sameSourceInstance(current, source)
	}
	if !captured {
		return false
	}
	current, ok := s.cfg.RemoteThreadCache.SourceGeneration(source.ID())
	return ok && current == generation
}

// sameSourceInstance reports whether two registrations are the same source
// instance without comparing the interface values head-on: an interface
// comparison panics when both hold the same uncomparable dynamic type, and a
// Source implemented as a struct carrying a slice or a map is free to be one.
// Every source the hub registers itself is a pointer, where the dynamic type is
// comparable and the comparison is the address — the identity this fence is
// after. A dynamic type with no comparable identity reports "not the same
// instance", the conservative answer: the fence drops the stale retention
// rather than panicking on a tree read, and a registration whose rows were
// captured under a different instance is exactly what it must refuse.
func sameSourceInstance(a, b appsource.Source) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	if !reflect.TypeOf(a).Comparable() {
		return false
	}
	return a == b
}

func (s *WebServer) setLastGoodThreadsLocked(sourceID string, threads []appwire.Thread) {
	if s.lastGoodThreads == nil {
		s.lastGoodThreads = map[string][]appwire.Thread{}
	}
	s.lastGoodThreads[sourceID] = append([]appwire.Thread(nil), threads...)
}

// forgetLastGoodThreads drops sourceID's retained last-known-good rows. The
// host manager's remove finish phase calls it beside
// remoteCache.RemoveSource, so a removed host's last successful list leaves
// with every other piece of its per-name state instead of sitting in the map
// for the process lifetime — nothing else ever deleted an entry, so churning
// distinct host names grew the map without bound.
// A missing key — a host whose walk never completed a list — is a no-op, as
// is a nil map: delete treats both alike. A walk parked inside ListThreads
// while the remove commits cannot resurrect the entry afterwards: the walk's
// store carries the read-time identity capture and skips names whose
// registration the removal dropped (storeLastGoodThreadsIfCurrent), so the
// late store leaves the forgotten entry forgotten.
func (s *WebServer) forgetLastGoodThreads(sourceID string) {
	s.lastGoodMu.Lock()
	defer s.lastGoodMu.Unlock()
	delete(s.lastGoodThreads, sourceID)
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
	title := envvars.FirstNonEmpty(thread.Name, thread.Preview, thread.SessionID, thread.ID, refText)
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
		IsSubagent:      thread.Evener.Kind == "subagent",
	}
	// Issue #152: a hub-synthesized fork (parent set, not a subagent) must
	// stamp DivergenceTurn so the 96cp invariant (ParentSessionID != "" &&
	// DivergenceTurn == 0 implies a spawned delegate) does not misclassify a
	// remote fork as a delegate. Value 1 is the minimum legal fork turn.
	if meta.ParentSessionID != "" && !meta.IsSubagent {
		meta.DivergenceTurn = 1
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
	entry.RunningJobs, entry.CompletedJobs = hubcore.SplitNonAgentJobs(diagnosticsJobs(thread.Evener.Diagnostics))
	entry.Watches = diagnosticsWatches(thread.Evener.Diagnostics)
	return meta, entry, true
}

func diagnosticsJobs(diagnostics *appwire.EvenerDiagnostics) []appwire.EvenerJobInfo {
	if diagnostics == nil {
		return nil
	}
	return diagnostics.Jobs
}

// diagnosticsWatches returns a remote thread's own live-watch rows. A thread
// with no diagnostics (old daemon, or one that listed nothing) and a
// diagnostics that omits Watches both yield an empty list — absence is never
// an error. It delegates to hubcore's shared projection so the local and remote
// tree code cannot drift.
func diagnosticsWatches(diagnostics *appwire.EvenerDiagnostics) []appwire.EvenerWatchInfo {
	return hubcore.DiagnosticsWatches(diagnostics)
}

// appThreadTreeParentSessionID translates the remote thread lineage into the
// same ref-valued metadata used by the local tree. ParentRef is authoritative
// for Evener children; ForkedFromID is the Codex fork lineage fallback.
func appThreadTreeParentSessionID(thread appwire.Thread, childRef appwire.Ref) string {
	raw := strings.TrimSpace(thread.Evener.ParentRef)
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
	if thread.Evener.Ref != "" {
		if ref, err := appwire.ParseRef(thread.Evener.Ref); err == nil {
			return ref, true
		}
	}
	sourceID := strings.TrimSpace(thread.Source)
	threadID := envvars.FirstNonEmpty(thread.ID, thread.SessionID)
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
			Online: s.sourceOnline(source.ID()),
		})
	}
	return sources
}

func hubCapabilitiesFromAppwire(caps appwire.ThreadCapabilities) hubapi.SessionCapabilities {
	return hubapi.SessionCapabilities{
		Send:        caps.Send,
		Steer:       caps.Steer,
		Interrupt:   caps.Interrupt,
		Compact:     caps.Compact,
		Clear:       caps.Clear,
		Shutdown:    caps.Shutdown,
		ChangeModel: caps.ChangeModel,
		Queue:       caps.Queue,
	}
}

func (s *WebServer) isLive(sessionID string) bool {
	if !isLocalRouteID(sessionID) {
		// A tree projection has no request context of its own; bound the
		// ownership wait the same way the relay publication guard does, so a
		// pending Resume cannot block the render indefinitely.
		ctx, cancel := context.WithTimeout(context.Background(), hubTransientOwnershipBudget)
		defer cancel()
		_, err := sourceForThreadWithDeletionFence(ctx, s.cfg, s.sources, appRefFromRouteID(sessionID), "")
		return err == nil
	}
	if s.cfg.Roster == nil {
		return false
	}
	_, ok := s.cfg.Roster.Find(sessionID)
	return ok
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
		if rawRef := strings.TrimSpace(thread.Evener.Ref); rawRef != "" {
			parsed, err := appwire.ParseRef(rawRef)
			if err != nil || parsed != ref {
				complete = false
			}
		}
		candidate := favoriteRemoteOwnership{sourceID: sourceID, complete: complete}
		if previous, exists := ownerships[ref.String()]; exists {
			if previous.sourceID == candidate.sourceID && previous.sourceID != "" {
				candidate = favoriteRemoteOwnership{sourceID: candidate.sourceID}
			} else {
				candidate = favoriteRemoteOwnership{}
			}
		}
		ownerships[ref.String()] = candidate
	}
	return ownerships
}

func favoriteRemoteThreadRef(thread appwire.Thread) (appwire.Ref, bool) {
	if strings.TrimSpace(thread.Evener.Ref) != "" {
		ref, err := appwire.ParseRef(thread.Evener.Ref)
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
	projectIdentities := snapshot.projectIdentities
	if projectIdentities == nil {
		projectIdentities = make(map[string][]identifier.Project, len(snapshot.projects))
		for path, project := range snapshot.projects {
			projectIdentities[path] = []identifier.Project{project}
		}
	}
	for path, projects := range projectIdentities {
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
		for _, project := range projects {
			if project.ID == "" {
				continue
			}
			canonicalPath := project.CanonicalPath
			if canonicalPath == "" {
				canonicalPath = path
			}
			for source, evidence := range owners {
				quality := evidence.quality
				if !evidence.hasIdentity || snapshot.projectConflicts[path] {
					quality = mergeFavoriteAuthorityQuality(quality, hubcore.FavoriteAuthorityIncomplete)
				}
				claimKey := canonicalPath + "\x00" + source
				key := project.ID + "\x00" + claimKey
				if previous, ok := claims[key]; ok {
					previous.Quality = mergeFavoriteAuthorityQuality(previous.Quality, quality)
					claims[key] = previous
				} else {
					claims[key] = hubcore.FavoriteProjectAuthority{
						ID:       project.ID,
						Quality:  quality,
						ClaimKey: claimKey,
						Source:   hubcore.NormalizeDecisionSource(source),
					}
				}
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
	source := favoriteProjectSourceClaim(id, snapshot)
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

func favoriteProjectSourceClaim(id string, snapshot navigationSnapshot) string {
	if ownership, ok := snapshot.remoteOwnership[id]; ok {
		if ownership.sourceID != "" {
			return ownership.sourceID
		}
		return "remote-incomplete"
	}
	return "local"
}

// rowRenameable reports whether a tree row exposes the rename menu item. Local
// rows are always renameable (ended via the hub meta-edit path, live via the
// daemon method); non-local rows are not. Derived from the ref's host, not
// a per-thread probe.
func (s *WebServer) rowRenameable(id string) bool { return isLocalRouteID(id) }

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

func (s *WebServer) apiSessionCapabilities(id string, live bool) hubapi.SessionCapabilities {
	pastExists := false
	if s.cfg.Past != nil {
		_, pastExists = s.cfg.Past.Find(id)
	}
	var caps hubapi.SessionCapabilities
	if !live && s.cfg.Spawner != nil && pastExists {
		caps.Send = true
	}
	if !caps.Send && !caps.Steer && !caps.Interrupt && !caps.Compact && !caps.Clear && !caps.Shutdown && !caps.ChangeModel {
		if live {
			caps.ReadOnlyReason = "live session source is unavailable"
		} else {
			caps.ReadOnlyReason = "session is not live and cannot be resumed"
		}
	}
	return caps
}
