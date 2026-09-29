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

// projectKeyForStateDir names the project a PastEntry.StateDir belongs to. The
// state dir is the project bucket itself (<state>/projects/<id>), so its base
// is the project ID that keys navigation project resources.
func projectKeyForStateDir(stateDir string) string {
	return filepath.Base(stateDir)
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
	remoteRoots         *hubcore.RootIndex
	remoteSources       map[string]hubcore.RemoteSourceSnapshot
	remoteIncompleteIDs map[string]struct{}
	remoteGeneration    uint64
	// resolveDuration is the time ResolveProjectMap took inside this capture.
	resolveDuration time.Duration
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
	// tombstones names the sources whose rows in this fetch are tombstone-
	// sourced: the removed hosts whose retained projections the refresh
	// re-applied (spec 08 §15). The rows keep their metadata and their
	// tombstone's name as their Source tag, but the tree/action logic must
	// force them non-live with capabilities disabled — never through the
	// `appThreadTreeLive` + `sourceOnline` predicate, whose documented
	// fail-open on unknown source IDs would promote a removed host's rows
	// straight back to live (web.go's sourceOnline comment names exactly that
	// defect).
	tombstones map[string]struct{}
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

func (s *WebServer) pinSectionAssignments() (map[hubcore.ArchiveKey]hubcore.SessionPin, error) {
	if s.cfg.PinSections == nil {
		return map[hubcore.ArchiveKey]hubcore.SessionPin{}, nil
	}
	return s.cfg.PinSections.Assignments()
}

func (s *WebServer) pinSections() ([]hubcore.PinSection, error) {
	if s.cfg.PinSections == nil {
		return []hubcore.PinSection{}, nil
	}
	return s.cfg.PinSections.Sections()
}

// pinDecisionKey is the decision identity a stored pin resolves under: the
// authority index and the decision stores name a controller row by its bare ID
// (every local decision predates the ref spelling) and a remote row by its
// host-qualified ref.
func pinDecisionKey(key hubcore.ArchiveKey) hubcore.ArchiveKey {
	if key.Source == "" {
		return hubcore.ArchiveKey{Kind: "session", ID: key.ID}
	}
	return hubcore.ArchiveKey{Kind: "session", ID: hubapi.Ref{HostID: key.Source, SessionID: key.ID}.String()}
}

func classifySessionPins(assignments map[hubcore.ArchiveKey]hubcore.SessionPin, authority hubcore.FavoriteAuthority) hubcore.FavoriteRevalidation {
	decisions := make(map[hubcore.ArchiveKey]bool, len(assignments))
	for key := range assignments {
		decisions[pinDecisionKey(key)] = true
	}
	return hubcore.ClassifyFavoriteDecisions(decisions, authority)
}

func canonicalPinAssignments(assignments map[hubcore.ArchiveKey]hubcore.SessionPin, classified hubcore.FavoriteRevalidation) map[hubcore.ArchiveKey]hubcore.SessionPin {
	out := make(map[hubcore.ArchiveKey]hubcore.SessionPin, len(assignments)*2)
	for key, assignment := range assignments {
		out[key] = assignment
		classification := classified.Classifications[pinDecisionKey(key)]
		if classification.State == hubcore.FavoriteDecisionValid && classification.CanonicalKey.ID != "" {
			// A canonical identity lands on the owning source's pin, never a
			// bare-ID key that a second source could also claim.
			out[hubcore.SessionPinIdentity(classification.CanonicalKey.ID)] = assignment
		}
	}
	return out
}

// dropSubagentPins removes the assignments whose target is a known subagent
// from what navigation presents, and takes them out of their section's member
// count. Favorites and pins never target a subagent; a stale row is inert here
// and stays in the store until the user unpins it.
func dropSubagentPins(assignments map[hubcore.ArchiveKey]hubcore.SessionPin, sections []hubcore.PinSection, subagents map[string]bool) (map[hubcore.ArchiveKey]hubcore.SessionPin, []hubcore.PinSection) {
	if len(subagents) == 0 {
		return assignments, sections
	}
	kept := make(map[hubcore.ArchiveKey]hubcore.SessionPin, len(assignments))
	dropped := make(map[string]int)
	for key, assignment := range assignments {
		if subagents[pinDecisionKey(key).ID] {
			dropped[assignment.SectionID]++
			continue
		}
		kept[key] = assignment
	}
	adjusted := append([]hubcore.PinSection(nil), sections...)
	for i := range adjusted {
		adjusted[i].MemberCount -= dropped[adjusted[i].ID]
	}
	return kept, adjusted
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
		// This authority serves alias lookup, which falls back to the id's own
		// spellings, so it carries no session facts: the stores are not read.
		authority, _ := s.favoriteAuthorityForReferences(snapshot, t, nil)
		return hubcore.TreeCacheValue{
			Tree:              t,
			AttentionSummary:  sum,
			Live:              snapshot.live,
			FavoriteAuthority: authority,
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
func navigationBuildInputsFromTreeSnapshot(generationID string, revision uint64, tree hubcore.Tree, sources []hubapi.Source, attention hubapi.AttentionSummary, live []hubcore.LiveEntry, sessionFavorites, projectFavorites map[hubcore.ArchiveKey]bool, pinSections []hubcore.PinSection, pinAssignments map[hubcore.ArchiveKey]hubcore.SessionPin) navigationBuildInputs {
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

// sessionsUnderIncompatibleDaemons selects the past sessions whose ownership
// a restartRequired or unconfirmed daemon can decide: those daemons' own
// sessions and everything reachable from them through parent or job-tree-root
// links. Any other session has no path to such a daemon, so verifying it
// could only report ancestry gaps that no daemon could account for. An
// unconfirmed claim without a usable identity cannot exclude any session.
func sessionsUnderIncompatibleDaemons(past []hubcore.PastEntry, live []hubcore.LiveEntry, unconfirmed []rendezvous.Entry) []hubcore.PastEntry {
	var pending []string
	addDaemon := func(entry rendezvous.Entry, sessionID string) {
		pending = append(pending, sessionID, entry.SessionID, entry.ThreadID)
		if ref, err := appwire.ParseRef(localSpawnWorkspaceRef(entry)); err == nil {
			pending = append(pending, ref.ThreadID)
		}
	}
	for _, entry := range live {
		if !entry.Crashed && entry.Status == appwire.ThreadStatusRestartRequired {
			addDaemon(entry.Entry, entry.SessionID)
		}
	}
	for _, entry := range unconfirmed {
		if _, err := appwire.ParseRef(localSpawnWorkspaceRef(entry)); err != nil {
			return past
		}
		addDaemon(entry, "")
	}
	linked := make(map[string][]string, len(past))
	for _, entry := range past {
		for _, owner := range []string{entry.Meta.ParentSessionID, entry.Meta.JobTreeRootSessionID} {
			if owner != "" {
				linked[owner] = append(linked[owner], entry.Meta.ID)
			}
		}
	}
	reached := make(map[string]bool)
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if id == "" || reached[id] {
			continue
		}
		reached[id] = true
		pending = append(pending, linked[id]...)
	}
	var affected []hubcore.PastEntry
	for _, entry := range past {
		if reached[entry.Meta.ID] {
			affected = append(affected, entry)
		}
	}
	return affected
}

func (s *WebServer) navigationSnapshotInputs(ctx context.Context) navigationSnapshot {
	var live []hubcore.LiveEntry
	var unconfirmedOwnership bool
	var ownershipErr error
	var unconfirmed []rendezvous.Entry
	if s.cfg.Roster != nil {
		roster := s.cfg.Roster.Snapshot()
		unconfirmed = roster.Unconfirmed
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
		// Persisted job-tree roots and fork continuations have no rendezvous of
		// their own. Preserve the authenticated owner's restart restriction in
		// every navigation projection. The walk still passes through subagents
		// to reach them, but a subagent has no row to carry the state.
		roots, running := hubcore.NewRootIndex(nil), hubcore.RunningSubagentIDs(live)
		if s.cfg.Past != nil {
			roots = s.cfg.Past.RootIndex()
		}
		for _, past := range sessionsUnderIncompatibleDaemons(pastEntries, live, unconfirmed) {
			if roots.IsSubagent(past.Meta.ID) || running[past.Meta.ID] {
				continue
			}
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
	tombstoned := func(sourceID string) bool {
		_, isTombstone := fetch.tombstones[strings.TrimSpace(sourceID)]
		return isTombstone
	}
	carriedProjectCandidates := make(map[string]map[string]identifier.Project)
	var remoteMetas []schema.SessionMeta
	for _, thread := range fetch.threads {
		meta, entry, ok := appThreadTreeEntries(thread)
		if !ok {
			continue
		}
		metas = append(metas, meta)
		remoteMetas = append(remoteMetas, meta)
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
		//
		// A tombstone-sourced row is forced non-live BEFORE the predicate: the
		// tombstone check must never route through sourceOnline, whose
		// fail-open on an unknown source ID would promote a removed host's
		// active rows straight back to live (spec 08 §15 names exactly that
		// defect). The row keeps its metadata for display; only liveness and
		// capabilities are withheld.
		if !tombstoned(thread.Source) && appThreadTreeLive(thread) && s.sourceOnline(thread.Source) {
			live = append(live, entry)
		}
	}
	// Resolve live working directories once at ingestion. BuildTree and the
	// orphan-live projection reuse this carried identity rather than resolving
	// in grouping or rendering loops.
	resolveStart := time.Now()
	resolvedProjects := hubcore.ResolveProjectMap(metas, live)
	resolveDuration := time.Since(resolveStart)
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
		if id, ok := stateDirProjectID(entry.StateDir); ok {
			addNavigationProjectCandidate(projectCandidates, path, identifier.Project{ID: id})
		}
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
		remoteRoots:         hubcore.NewRootIndex(remoteMetas),
		remoteSources:       fetch.sources,
		remoteIncompleteIDs: incompleteIDs,
		remoteGeneration:    fetch.generation,
		resolveDuration:     resolveDuration,
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

// stateDirProjectID reports the project ID a past entry's state directory is
// named by. ok is false when the basename is not a well-formed project ID, so
// every reader skips the same malformed basename instead of one of them
// applying a project decision the others would not (#2775). The navigation
// tree and search share it.
func stateDirProjectID(stateDir string) (string, bool) {
	id := filepath.Base(stateDir)
	if identifier.ValidateProjectID(id) != nil {
		return "", false
	}
	return id, true
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
	snapshot := s.navigationSnapshot(ctx)
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
			tombstones: tombstonedSourceNames(snapshot.Sources),
		}
	}
	fetch := s.refreshRemoteThreadSnapshot(ctx)
	fetch.generation = s.remoteFetchGeneration.Add(1)
	return fetch
}

// tombstonedSourceNames is the tombstone tag a published snapshot carries: the
// names whose per-source entry the refresh re-applied from a durable tombstone.
// The tree's live predicate consumes it to force those rows non-live.
func tombstonedSourceNames(sources map[string]hubcore.RemoteSourceSnapshot) map[string]struct{} {
	var names map[string]struct{}
	for sourceID, source := range sources {
		if !source.Tombstoned {
			continue
		}
		if names == nil {
			names = make(map[string]struct{})
		}
		names[sourceID] = struct{}{}
	}
	return names
}

// retainedTombstoneSources returns the removed hosts' retained rows the tree
// merge re-applies, one entry per unexpired (or remnant-gated) tombstone. Nil
// when no manager is wired or no tombstone is visible.
func (s *WebServer) retainedTombstoneSources() map[string]hubcore.RemoteSourceSnapshot {
	if s.hostManage == nil {
		return nil
	}
	return s.hostManage.retainedTombstoneSources()
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
	tombstoneSources := s.retainedTombstoneSources()
	if s.sources == nil {
		// No source registry: the publication is the tombstone merge alone.
		var threads []appwire.Thread
		sources := make(map[string]hubcore.RemoteSourceSnapshot, len(tombstoneSources))
		tombstoneNames := make(map[string]struct{}, len(tombstoneSources))
		for name, snapshot := range tombstoneSources {
			threads = append(threads, snapshot.Threads...)
			sources[name] = snapshot
			tombstoneNames[name] = struct{}{}
		}
		return remoteThreadFetch{
			complete:          true,
			threads:           threads,
			sources:           sources,
			sourceGenerations: map[string]uint64{},
			tombstones:        tombstoneNames,
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
	// Snapshot merge (spec §15): AFTER enumerating the registered sources,
	// re-apply each unexpired tombstone's last-known-good rows (marked
	// stale, non-actionable, plus the truncation indicator), so a removed
	// host's rows survive every subsequent refresh until its tombstone is
	// re-added or pruned. The rows carry the tombstone's name as their
	// Source tag and the fetch's tombstone set carries the identity the
	// tree consumes; a concurrent re-add wins by the generation rule at
	// publish time (the re-registration's generation makes the cache's
	// staleness rule drop tombstone-sourced rows, and its own
	// new-generation walk publishes the live rows).
	//
	// A name the walk just enumerated as a registered source — a re-add that
	// landed mid-walk — is skipped: its freshly read live rows belong to the
	// current registration, and the tombstone merge must not shadow them with
	// the removed incarnation's rows or tag the source tombstoned.
	mergedTombstones := make(map[string]struct{}, len(tombstoneSources))
	for name, snapshot := range tombstoneSources {
		if _, enumerated := sources[name]; enumerated {
			continue
		}
		threads = append(threads, snapshot.Threads...)
		sources[name] = snapshot
		mergedTombstones[name] = struct{}{}
	}
	return remoteThreadFetch{threads: threads, complete: complete, sources: sources, sourceGenerations: readGenerations, tombstones: mergedTombstones}
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
		// The row's last message reaches the tree through the meta too, so an
		// offline host's last-known row, which is not live, keeps it (S1d).
		LastMessage: thread.Evener.LastMessage,
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
		Project:   project,
	}
	// The remote row reads its thread-row facts through the one shared reader,
	// so it cannot drift from the local probe (#2638). Capabilities and
	// ActiveFlags stay absent on a remote row; only a local probe answers them.
	entry = hubcore.LiveEntryThreadFacts(entry, hubcore.ProbeResultFromThread(thread))
	return meta, entry, true
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

// referencedSessionIDs names every session the favorite and pin stores refer
// to, in the spelling each store keeps.
func referencedSessionIDs(favorites map[hubcore.ArchiveKey]bool, assignments map[hubcore.ArchiveKey]hubcore.SessionPin) []string {
	ids := make([]string, 0, len(favorites)+len(assignments))
	for key := range favorites {
		if key.Kind == "session" {
			ids = append(ids, key.ID)
		}
	}
	for key := range assignments {
		ids = append(ids, pinDecisionKey(key).ID)
	}
	return ids
}

// localSessionIndex is the past-index view the referenced-ID authority reads:
// lineage from the root index and a membership check that never probes disk.
type localSessionIndex struct {
	roots *hubcore.RootIndex
	known func(id string) bool
}

func (s *WebServer) localSessionIndex() localSessionIndex {
	if s.cfg.Past == nil {
		return localSessionIndex{roots: hubcore.NewRootIndex(nil), known: func(string) bool { return false }}
	}
	past := s.cfg.Past
	return localSessionIndex{roots: past.RootIndex(), known: func(id string) bool {
		_, ok := past.Lookup(id)
		return ok
	}}
}

// favoriteAuthorityForReferences collects authority for the sessions the
// stores reference, not for every session the hub holds. The second result is
// the set of referenced canonical ids that are known subagents: no favorite or
// pin may target one, so navigation drops them.
func (s *WebServer) favoriteAuthorityForReferences(snapshot navigationSnapshot, tree hubcore.Tree, ids []string) (hubcore.FavoriteAuthority, map[string]bool) {
	sessions, subagents := referencedSessionAuthorities(ids, snapshot, s.localSessionIndex())
	return hubcore.FavoriteAuthority{
		Sessions: sessions,
		Projects: favoriteProjectAuthorities(snapshot),
	}, subagents
}

// referencedSessionAuthorities builds the session authority for each distinct
// referenced id from the local root index and the remote thread snapshot. An id
// no source knows gets no authority and classifies Dormant. A known subagent
// (persisted meta, a live entry's running child, or a remote subagent thread)
// is not top level and classifies ConfirmedInvalid. Rootness comes from the
// shared nesting rule inside RootIndex, so lineage is always complete here.
func referencedSessionAuthorities(ids []string, snapshot navigationSnapshot, local localSessionIndex) ([]hubcore.FavoriteSessionAuthority, map[string]bool) {
	remoteRoots := snapshot.remoteRoots
	if remoteRoots == nil {
		remoteRoots = hubcore.NewRootIndex(nil)
	}
	running := hubcore.RunningSubagentIDs(snapshot.live)
	liveIDs := make(map[string]bool, len(snapshot.live))
	for _, entry := range snapshot.live {
		if entry.SessionID != "" {
			liveIDs[entry.SessionID] = true
		}
	}
	var sessions []hubcore.FavoriteSessionAuthority
	subagents := make(map[string]bool)
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id, remote := canonicalSessionDecisionID(raw)
		if seen[id] {
			continue
		}
		seen[id] = true
		roots, inIndex := local.roots, false
		if remote {
			roots = remoteRoots
			_, inIndex = snapshot.remoteOwnership[id]
		}
		isSubagent := roots.IsSubagent(id) || running[id]
		if !isSubagent && !remote {
			inIndex = local.known(id)
		}
		if !isSubagent && !inIndex && !liveIDs[id] {
			continue
		}
		if isSubagent {
			subagents[id] = true
		}
		// A live session whose meta has not reached an index is a root.
		topLevel := !isSubagent && (!inIndex || roots.TopLevel(id))
		sessions = append(sessions, hubcore.FavoriteSessionAuthority{
			ID:       id,
			Aliases:  favoriteSessionAliases(id),
			TopLevel: topLevel,
			Lineage:  hubcore.FavoriteAuthorityComplete,
			Source:   favoriteSessionSourceQuality(id, snapshot.remoteOwnership, snapshot.remoteSources, snapshot.remoteIncompleteIDs),
		})
	}
	return sessions, subagents
}

// canonicalSessionDecisionID maps a decision's session spelling to the id the
// authority is keyed by: the bare id for the controller's own sessions, the
// host-qualified ref for a remote one.
func canonicalSessionDecisionID(raw string) (id string, remote bool) {
	ref, err := hubapi.ParseRef(raw)
	switch {
	case err != nil:
		return raw, false
	case ref.HostID == "local":
		return ref.SessionID, false
	default:
		return ref.String(), true
	}
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

func favoriteProjectAuthorities(snapshot navigationSnapshot) []hubcore.FavoriteProjectAuthority {
	claims := make(map[string]hubcore.FavoriteProjectAuthority)
	projectIdentities := snapshot.projectIdentities
	if projectIdentities == nil {
		projectIdentities = make(map[string][]identifier.Project, len(snapshot.projects))
		for path, project := range snapshot.projects {
			projectIdentities[path] = []identifier.Project{project}
		}
	}
	// Group session IDs by working directory once; each project then reads
	// its own group instead of rescanning every session.
	idsByDir := make(map[string][]string)
	for _, meta := range snapshot.metas {
		dir := hubcore.EffectiveWorkingDir(meta)
		idsByDir[dir] = append(idsByDir[dir], meta.ID)
	}
	for _, entry := range snapshot.live {
		idsByDir[entry.WorkingDir] = append(idsByDir[entry.WorkingDir], entry.SessionID)
	}
	for path, projects := range projectIdentities {
		owners := make(map[string]favoriteProjectOwnerEvidence)
		for _, id := range idsByDir[path] {
			favoriteProjectOwnerEvidenceAdd(owners, id, snapshot)
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
