package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// The resource limits are protocol limits, rather than UI preferences. Keeping
// them next to the projector makes every representation use the same guard.
const (
	maxNavigationSectionRows   = 50
	maxNavigationCatalogRows   = 100
	maxNavigationChildren      = 50
	maxNavigationNodes         = 2_000
	maxNavigationDepth         = 32
	maxNavigationResponseBytes = 2 * 1024 * 1024
	maxNavigationManifestBytes = 256 * 1024
	maxNavigationCatalogBytes  = 512 * 1024

	maxNavigationTitleRunes = 200
	maxNavigationLabelRunes = 512
	// maxNavigationFullCommandRunes bounds FullCommand, the tooltip-sized
	// untruncated command. Generous rather than label-tight: a tooltip can
	// wrap, but it must not lie — a cut-off "full" command is worse than a
	// long one. Still bounded so one pathological command cannot dominate
	// the response's byte budget (navigationJSONFits).
	maxNavigationFullCommandRunes = 4_096
	maxNavigationIdentityBytes    = 1_024
	maxNavigationWorkingDirBytes  = 4_096
	// maxNavigationWatches caps the live-watch rows a single session summary
	// carries. It mirrors the daemon's watchDeliveryTimeCap of 32: the per-watch
	// delivery ring is already bounded to 32 instants, so capping the row list to
	// the same number keeps a watch-heavy session's payload bounded without
	// needing a second, larger bound. Rows dropped here are counted in
	// OmittedWatches, and the projector keeps armed rows ahead of inert ones so
	// the rail's armed count survives the cut.
	maxNavigationWatches = 32
	// maxNavigationProjectSources bounds a project summary's owning-source
	// list: the navigation inputs already cap configured sources at 64, and the
	// controller's own source is the one extra entry a merged project adds.
	maxNavigationProjectSources = 65
)

type navigationResourceKind string

const (
	navigationResourceManifest         navigationResourceKind = "manifest"
	navigationResourceLive             navigationResourceKind = "live"
	navigationResourceNeedsYou         navigationResourceKind = "needs_you"
	navigationResourcePinCatalog       navigationResourceKind = "pin_catalog"
	navigationResourcePinSection       navigationResourceKind = "pin_section"
	navigationResourceProjects         navigationResourceKind = "projects"
	navigationResourceArchivedProjects navigationResourceKind = "archived_projects"
	navigationResourceTestRuns         navigationResourceKind = "test_runs"
	navigationResourceProject          navigationResourceKind = "project"
	navigationResourceProjectPage      navigationResourceKind = "project_page"
	navigationResourceLocation         navigationResourceKind = "location"
)

// navigationResourceKey describes one immutable navigation representation. It
// contains decoded, validated values only; HTTP parsing belongs to its handler.
type navigationResourceKey struct {
	Kind       navigationResourceKind
	ID         string
	SectionID  string
	ProjectKey string
	Tier       string
	Offset     uint32
	Limit      uint32
	Generation string
	Revision   uint64
}

// navigationFingerprint is the semantic content fingerprint used by the
// service/cache layer. It intentionally excludes no projection fields: it is
// computed from the exact resource payload returned by Resource.
type navigationFingerprint [sha256.Size]byte

// navigationBuildInputs is the complete immutable decoration boundary for the
// pure projector. Store reads, project resolution, clock reads, and WebServer
// methods must happen before this value is assembled.
type navigationBuildInputs struct {
	GenerationID     string
	Revision         uint64
	Tree             hubcore.Tree
	LiveEntries      []hubcore.LiveEntry
	Sources          []hubapi.Source
	AttentionSummary hubapi.AttentionSummary

	// These maps are decorations captured with Tree. IDs may be a node ID or its
	// canonical ref; projection checks both without consulting a live roster.
	Live                map[string]bool
	Renameable          map[string]bool
	SessionFavorite     map[string]bool
	ProjectFavorite     map[string]bool
	PinSectionBySession map[string]string

	// PinSections and PinAssignments are used when callers retain the durable
	// pin snapshot instead of precomputing PinSectionBySession.
	PinSections    []hubcore.PinSection
	PinAssignments map[string]hubcore.SessionPin
}

type navigationProjection struct {
	inputs        navigationBuildInputs
	manifest      hubapi.NavigationManifest
	live          []hubcore.TreeNode
	needsYou      []hubcore.TreeNode
	pinCandidates []hubcore.TreeNode
	pinSections   []navigationPinSection
	pinSectionIDs map[string]bool
	projects      map[string]hubcore.TreeProject
	catalogs      map[navigationResourceKind][]hubcore.TreeProject
	locations     map[string]hubapi.NavigationSessionLocation
}

type navigationPinSection struct {
	id          string
	name        string
	memberCount int
	rows        []hubcore.TreeNode
}

// buildNavigationProjection has no ambient dependencies. The supplied tree is
// already ordered and tiered by hubcore; this function only adds wire shaping,
// pin/favorite decorations, bounds, and indexes.
func buildNavigationProjection(inputs navigationBuildInputs) (navigationProjection, error) {
	return buildNavigationProjectionContext(context.Background(), inputs)
}

// buildNavigationProjectionContext is the bounded form used by the service.
// Every potentially large collection and recursive walk checks ctx.
func buildNavigationProjectionContext(ctx context.Context, inputs navigationBuildInputs) (navigationProjection, error) {
	if err := validateNavigationInputsContext(ctx, inputs); err != nil {
		return navigationProjection{}, err
	}
	cloned, err := cloneNavigationInputsContext(ctx, inputs)
	if err != nil {
		return navigationProjection{}, err
	}
	p := navigationProjection{inputs: cloned, pinSectionIDs: make(map[string]bool), projects: make(map[string]hubcore.TreeProject), catalogs: make(map[navigationResourceKind][]hubcore.TreeProject), locations: make(map[string]hubapi.NavigationSessionLocation)}
	p.live = p.inputs.Tree.Live
	p.needsYou = p.inputs.Tree.NeedsYou
	p.pinCandidates, err = navigationPinCandidatesContext(ctx, p.inputs.Tree)
	if err != nil {
		return navigationProjection{}, err
	}
	for _, section := range p.inputs.PinSections {
		if err := ctx.Err(); err != nil {
			return navigationProjection{}, err
		}
		p.pinSectionIDs[section.ID] = true
	}

	buckets, err := navigationMergeProjectBucketsContext(ctx, navigationProjectBuckets(p.inputs.Tree))
	if err != nil {
		return navigationProjection{}, err
	}
	p.catalogs[navigationResourceProjects] = append([]hubcore.TreeProject(nil), buckets.active...)
	p.catalogs[navigationResourceArchivedProjects] = append([]hubcore.TreeProject(nil), buckets.archived...)
	p.catalogs[navigationResourceTestRuns] = append([]hubcore.TreeProject(nil), buckets.testRuns...)
	for _, project := range buckets.all() {
		if err := ctx.Err(); err != nil {
			return navigationProjection{}, err
		}
		p.projects[project.Key] = project
	}
	p.pinSections, err = p.buildPinSectionsContext(ctx)
	if err != nil {
		return navigationProjection{}, err
	}
	p.manifest = hubapi.NavigationManifest{
		GenerationID:     p.inputs.GenerationID,
		Revision:         p.inputs.Revision,
		Sources:          navigationSources(p.inputs.Sources),
		AttentionSummary: p.inputs.AttentionSummary,
		Sections: hubapi.NavigationSections{
			Live:        hubapi.NavigationResourceDescriptor{Count: len(p.live)},
			NeedsYou:    hubapi.NavigationResourceDescriptor{Count: len(p.needsYou)},
			PinSections: hubapi.NavigationResourceDescriptor{Count: len(p.pinSections)},
		},
		Catalogs: hubapi.NavigationCatalogs{
			Projects:         hubapi.NavigationResourceDescriptor{Count: len(p.catalogs[navigationResourceProjects])},
			ArchivedProjects: hubapi.NavigationResourceDescriptor{Count: len(p.catalogs[navigationResourceArchivedProjects])},
			TestRuns:         hubapi.NavigationResourceDescriptor{Count: len(p.catalogs[navigationResourceTestRuns])},
		},
	}
	if err := navigationJSONWithin(p.manifest, maxNavigationManifestBytes); err != nil {
		return navigationProjection{}, fmt.Errorf("navigation manifest: %w", err)
	}
	if err := p.indexLocationsContext(ctx); err != nil {
		return navigationProjection{}, err
	}
	return p, nil
}

func cloneNavigationInputsContext(ctx context.Context, in navigationBuildInputs) (navigationBuildInputs, error) {
	if err := ctx.Err(); err != nil {
		return navigationBuildInputs{}, err
	}
	out := in
	out.Sources = append([]hubapi.Source(nil), in.Sources...)
	out.LiveEntries = cloneNavigationLiveEntries(in.LiveEntries)
	cloneBool := func(values map[string]bool) (map[string]bool, error) {
		result := make(map[string]bool, len(values))
		for key, value := range values {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result[key] = value
		}
		return result, nil
	}
	var err error
	if out.Live, err = cloneBool(in.Live); err != nil {
		return navigationBuildInputs{}, err
	}
	if out.Renameable, err = cloneBool(in.Renameable); err != nil {
		return navigationBuildInputs{}, err
	}
	if out.SessionFavorite, err = cloneBool(in.SessionFavorite); err != nil {
		return navigationBuildInputs{}, err
	}
	if out.ProjectFavorite, err = cloneBool(in.ProjectFavorite); err != nil {
		return navigationBuildInputs{}, err
	}
	out.PinSectionBySession = make(map[string]string, len(in.PinSectionBySession))
	for key, value := range in.PinSectionBySession {
		if err := ctx.Err(); err != nil {
			return navigationBuildInputs{}, err
		}
		out.PinSectionBySession[key] = value
	}
	out.PinSections = append([]hubcore.PinSection(nil), in.PinSections...)
	out.PinAssignments = make(map[string]hubcore.SessionPin, len(in.PinAssignments))
	for id, assignment := range in.PinAssignments {
		if err := ctx.Err(); err != nil {
			return navigationBuildInputs{}, err
		}
		out.PinAssignments[id] = assignment
	}
	out.Tree, err = in.Tree.SnapshotContext(ctx)
	if err != nil {
		return navigationBuildInputs{}, err
	}
	return out, nil
}

func cloneNavigationInputs(in navigationBuildInputs) navigationBuildInputs {
	out := in
	out.Sources = append([]hubapi.Source(nil), in.Sources...)
	out.LiveEntries = cloneNavigationLiveEntries(in.LiveEntries)
	out.Live = cloneNavigationBoolMap(in.Live)
	out.Renameable = cloneNavigationBoolMap(in.Renameable)
	out.SessionFavorite = cloneNavigationBoolMap(in.SessionFavorite)
	out.ProjectFavorite = cloneNavigationBoolMap(in.ProjectFavorite)
	out.PinSectionBySession = cloneNavigationStringMap(in.PinSectionBySession)
	out.PinSections = append([]hubcore.PinSection(nil), in.PinSections...)
	out.PinAssignments = make(map[string]hubcore.SessionPin, len(in.PinAssignments))
	maps.Copy(out.PinAssignments, in.PinAssignments)
	return out
}

func cloneNavigationLiveEntries(in []hubcore.LiveEntry) []hubcore.LiveEntry {
	if in == nil {
		return nil
	}
	out := make([]hubcore.LiveEntry, len(in))
	for i, entry := range in {
		out[i] = entry
		out[i].ActiveFlags = append([]string(nil), entry.ActiveFlags...)
		out[i].RunningSubagentIDs = append([]string(nil), entry.RunningSubagentIDs...)
		out[i].RunningJobs = appwire.CloneEvenerJobs(entry.RunningJobs)
		out[i].CompletedJobs = appwire.CloneEvenerJobs(entry.CompletedJobs)
		out[i].Watches = appwire.CloneEvenerWatches(entry.Watches)
		if entry.ChildWatches != nil {
			out[i].ChildWatches = make(map[string][]appwire.EvenerWatchInfo, len(entry.ChildWatches))
			for childID, watches := range entry.ChildWatches {
				out[i].ChildWatches[childID] = appwire.CloneEvenerWatches(watches)
			}
		}
		if entry.RunningSubagentStates != nil {
			out[i].RunningSubagentStates = make(map[string]string, len(entry.RunningSubagentStates))
			maps.Copy(out[i].RunningSubagentStates, entry.RunningSubagentStates)
		}
	}
	return out
}

func cloneNavigationBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	maps.Copy(out, in)
	return out
}

func cloneNavigationStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// navigationMergeProjectBucketsContext collapses tree projects that present
// the same wire Key onto the single catalog entry that Key can address.
//
// A project's Key is its address on the wire: the client reads the project
// detail, and archives, favorites, or deletes the project, by (source, Key).
// The tree groups sessions by canonical project identity, but its Key is the
// canonical identifier.Project.ID only when the working directory resolved;
// sessions that resolve to no project all present the shared "no-project" key
// while keeping their own grouping path, so one tree can hand the catalogs
// several distinct projects that present one Key.
//
// The invariant is one entity key per catalog PAGE, not one per tree. Two rows
// in one page with one Key collapse onto a single entity key, and
// hubapi.NavigationSnapshot.Validate then rejects the graph with "duplicate
// navigation entity key" (the root container's repeated child would read as
// multiple parents next), which validateNavigationResourceSnapshot reports as
// the "graph" category. Live, that is what one attached host triggered: the
// host's threads live in directories the controller cannot resolve, each
// minting a "no-project" group, so the manifest read succeeded while
// archived_projects answered an internal error.
//
// The buckets are not merely a display list, and that is why the duplicate
// groups must be MERGED rather than discarded: they are the sole source of the
// catalog slices (:171-179) and manifest counts (:184-199), of the p.projects
// map built from buckets.all() (:174-179), and of the location index that
// indexLocationsContext walks to mint a hubapi.NavigationSessionLocation per
// session (:1370-1399). Dropping a duplicate group therefore does not just trim
// a row: its sessions vanish from the catalog and from their project entry, and
// a location lookup for one of them answers "not found" - a silent session loss
// that is worse than the visible error it replaces.
//
// Each Key is therefore merged within its own bucket, in the tree's own
// deterministic order (active, then archived, then test runs): the first group
// keeps the row's identity and position, and every later group with that Key is
// folded into it, carrying every session from every group. Merging is per
// bucket, never across them, because each catalog page is validated on its own:
// a Key repeated in active and archived is two independent, individually valid
// pages, and collapsing across buckets would empty an unrelated catalog. The
// fold rule is documented on navigationMergeProjectContext; this is the
// general "one addressable project per Key per page" rule, not a branch on how
// many sources exist.
//
// This is the bounded form the projection build runs: every loop the merge
// walks checks ctx, so a canceled build stops inside the merge instead of
// folding a large duplicate-Key tree to completion.
func navigationMergeProjectBucketsContext(ctx context.Context, buckets navigationProjectBucket) (navigationProjectBucket, error) {
	if err := ctx.Err(); err != nil {
		return navigationProjectBucket{}, err
	}
	active, err := navigationMergeProjectGroupsContext(ctx, buckets.active)
	if err != nil {
		return navigationProjectBucket{}, err
	}
	archived, err := navigationMergeProjectGroupsContext(ctx, buckets.archived)
	if err != nil {
		return navigationProjectBucket{}, err
	}
	testRuns, err := navigationMergeProjectGroupsContext(ctx, buckets.testRuns)
	if err != nil {
		return navigationProjectBucket{}, err
	}
	return navigationProjectBucket{active: active, archived: archived, testRuns: testRuns}, nil
}

// navigationMergeProjectGroupsContext merges the projects that share a Key
// inside one bucket, keeping the position of each Key's first appearance. A
// project whose Key is unique is returned unchanged, so the common case keeps
// the tree's own value - including the private uncapped tier slices that the
// merge below cannot carry across a rebuilt value.
//
// The fold is single-pass: all of a Key's colliding groups reach ONE
// navigationMergeProjectContext call, which unions each tier across every
// group at once. Folding pair by pair instead re-read, re-appended, and
// re-sorted the accumulated row's whole tier on every collision, so a Key
// with k groups paid k re-sorts of a tier that kept growing - quadratic in
// the colliding groups, which the live "no-project" shape makes unbounded.
// The two agree on the result: every merged field combines associatively in
// the tree's own group order, and the tier sort is stable (see
// navigationMergeProjectContext).
func navigationMergeProjectGroupsContext(ctx context.Context, projects []hubcore.TreeProject) ([]hubcore.TreeProject, error) {
	at := make(map[string]int, len(projects))
	var collisions map[int][]hubcore.TreeProject
	out := make([]hubcore.TreeProject, 0, len(projects))
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index, ok := at[project.Key]; ok {
			if collisions == nil {
				collisions = make(map[int][]hubcore.TreeProject)
			}
			collisions[index] = append(collisions[index], project)
			continue
		}
		at[project.Key] = len(out)
		out = append(out, project)
	}
	for index, groups := range collisions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		merged, err := navigationMergeProjectContext(ctx, out[index], groups)
		if err != nil {
			return nil, err
		}
		out[index] = merged
	}
	return out, nil
}

// navigationMergeProjectContext folds the groups in next - every one of which
// presents first.Key - into first, and returns the single row they collapse to.
//
// The first group owns the row's rendered identity - Name, WorkingDir, Key, and
// the IsArchived / IsTestRun flags, which are uniform inside a bucket because
// navigationProjectBuckets routes on them - so a merged row keeps one stable
// label and address. Everything else is combined the way the projection's
// consumers read the struct:
//
//   - Current / Recent / Archived are the union of every group's rows, ordered
//     the way hubcore orders its own tiers (most recent first: UpdatedAt desc,
//     CreatedAt desc, then the title and id tie-break sessionMetaLess uses).
//     The per-tier overflow counts (MoreCurrent / MoreRecent / MoreArchived)
//     are recomputed against maxSidebarSessionsPerTier rather than summed:
//     More* is how many rows of the union lie beyond the cap a tree-built
//     project keeps public, so a merged row states the same overflow a
//     tree-built project holding the same sessions would. Summing the groups'
//     counts was wrong because a group's own More* is zero whenever that group
//     fits the cap, even when the merged union does not - the counts then
//     described rows already present in the very slice they claimed to be
//     beyond.
//   - Each tier is read through TierRows, so a group whose private uncapped
//     slice holds more than maxSidebarSessionsPerTier rows contributes all of
//     them, and the union is kept whole in the rebuilt value's public tier:
//     the merge must not cap it away. TierRows falls back to the public tier
//     once the merge cannot carry the private slices forward, and both the
//     project detail's paging and the service's logical fingerprints read
//     TierRows, so a capped public tier would drop every row past the cap from
//     paging and from change detection - the silent session loss this merge
//     exists to prevent.
//   - Each tier's synthetic cluster rows are then reconciled across the merged
//     project by navigationMergeClusterRowsContext, because groups that share
//     one Key can mint one cluster id each (see that function): the union
//     keeps one row per identity, carrying every folded member, instead of
//     concatenating two rows the client would address as one.
//   - LastActivity takes the later moment, and Age comes with it.
//   - RollupState takes the higher hubapi.RollupRank, and RollupLive / RollupAttn
//     add, so the merged header still counts every working and awaiting session.
//   - Sources is the distinct union, sorted like hubcore's own
//     sortedDecisionSources, because archive and favorite decisions are keyed by
//     (source, Key) and the read path must consult every contributing source.
//   - Expanded is the OR, matching its own "RollupLive > 0 || RollupAttn > 0"
//     rule.
//   - Worktrees adds: the delete confirmation's distinct-worktree count can only
//     grow when more sessions join the project. It is a distinct-PATH count and
//     the struct carries the count alone, not the paths, so a worktree path two
//     merged groups share is counted once per group: the merged value is an
//     upper bound on the distinct paths, not the exact count.
//
// The scalar folds and the tier unions are computed in one pass over the
// groups, in the tree's own order; folding the same groups pair by pair
// produces the same value, because every field combines associatively and the
// stable tier sort keeps tied rows in the order the groups were read in.
func navigationMergeProjectContext(ctx context.Context, first hubcore.TreeProject, next []hubcore.TreeProject) (hubcore.TreeProject, error) {
	lastActivity, age := first.LastActivity, first.Age
	rollupState := first.RollupState
	rollupLive, rollupAttn := first.RollupLive, first.RollupAttn
	worktrees, expanded, sources := first.Worktrees, first.Expanded, first.Sources
	for _, group := range next {
		if err := ctx.Err(); err != nil {
			return hubcore.TreeProject{}, err
		}
		if group.LastActivity.After(lastActivity) {
			lastActivity, age = group.LastActivity, group.Age
		}
		if hubapi.RollupRank(group.RollupState) > hubapi.RollupRank(rollupState) {
			rollupState = group.RollupState
		}
		rollupLive += group.RollupLive
		rollupAttn += group.RollupAttn
		worktrees += group.Worktrees
		expanded = expanded || group.Expanded
		sources = navigationMergeProjectSources(sources, group.Sources)
	}
	current, err := navigationMergeProjectTierContext(ctx, first, next, "current")
	if err != nil {
		return hubcore.TreeProject{}, err
	}
	recent, err := navigationMergeProjectTierContext(ctx, first, next, "recent")
	if err != nil {
		return hubcore.TreeProject{}, err
	}
	archived, err := navigationMergeProjectTierContext(ctx, first, next, "archived")
	if err != nil {
		return hubcore.TreeProject{}, err
	}
	mergedTiers, err := navigationMergeClusterRowsContext(ctx, current, recent, archived)
	if err != nil {
		return hubcore.TreeProject{}, err
	}
	current, recent, archived = mergedTiers[0], mergedTiers[1], mergedTiers[2]
	return hubcore.TreeProject{
		Name:         first.Name,
		Key:          first.Key,
		WorkingDir:   first.WorkingDir,
		Current:      current,
		Recent:       recent,
		Archived:     archived,
		IsArchived:   first.IsArchived,
		IsTestRun:    first.IsTestRun,
		LastActivity: lastActivity,
		RollupState:  rollupState,
		RollupLive:   rollupLive,
		RollupAttn:   rollupAttn,
		Sources:      sources,
		Expanded:     expanded,
		// The overflow is derived after navigationMergeClusterRowsContext, not
		// per group: folding colliding cluster rows shortens a tier, and More*
		// must describe the rows the tier actually holds.
		MoreCurrent:  navigationTierOverflow(len(current), hubcore.SidebarSessionPageSize),
		MoreRecent:   navigationTierOverflow(len(recent), hubcore.SidebarSessionPageSize),
		MoreArchived: navigationTierOverflow(len(archived), hubcore.SidebarSessionPageSize),
		Age:          age,
		Worktrees:    worktrees,
	}, nil
}

// navigationMergeProjectTierContext returns one tier across every group that
// shares a Key: the full union in the tree's own order.
//
// It reads TierRows rather than the public slice because a tree-built project
// keeps its overflow in the private uncapped tier, and the merged project must
// carry that overflow too. The union must then be re-ordered: each group is
// internally most-recent-first, but appending one group after the other is not
// globally ordered, so the merge sorts by the field and tie-break the tree and
// capTier rely on (navigationTreeNodeLess). Sorting the concatenated union
// once - rather than re-sorting the accumulated rows after each group folds
// in - keeps the tier linear in its rows, and matches what folding pair by
// pair produces, because the sort is stable and the groups concatenate in the
// tree's own order. The rows are returned whole - the caller keeps every one
// in the public tier, which is what TierRows falls back to - so the caller
// derives the tier's overflow with navigationTierOverflow once
// navigationMergeClusterRowsContext has folded any colliding cluster rows
// away.
func navigationMergeProjectTierContext(ctx context.Context, first hubcore.TreeProject, next []hubcore.TreeProject, tier string) ([]hubcore.TreeNode, error) {
	firstRows, _ := first.TierRows(tier)
	size := len(firstRows)
	for _, group := range next {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, _ := group.TierRows(tier)
		size += len(rows)
	}
	rows := make([]hubcore.TreeNode, 0, size)
	rows = append(rows, firstRows...)
	for _, group := range next {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		groupRows, _ := group.TierRows(tier)
		rows = append(rows, groupRows...)
	}
	// The sort below is the tier's dominant step and cannot observe ctx, so
	// surface a cancellation that arrived during the concatenation before
	// starting it, the way the entry checks of the bounded forms do.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return navigationTreeNodeLess(rows[i], rows[j]) })
	return rows, nil
}

// navigationMergeClusterRowsContext folds the synthetic cluster rows a merged
// project can carry more than once onto one row per identity, across the
// three tiers.
//
// hubcore mints a cluster id from the owning project's display name and the
// folded title (hubcore tree.go clusterID), and an unresolved group's display
// name is the basename of its working directory (hubcore tree.go, the
// "filepath.Base(displayPath)" its accumulator is created with). Two unresolved
// groups whose directories share a basename therefore mint ONE cluster id for
// their repeated title, and navigationMergeProjectGroupsContext folds those
// two groups into one project row: concatenating their tiers would leave that
// id twice.
//
// The id is an address, not a local label: navigationNodeRef advertises it as
// the summary Ref, and the normalizer keys the session entity by that ref. Two
// rows with one id therefore make the project's normalized graph carry two
// entities with one key, which hubapi.NavigationSnapshot.Validate rejects with
// "duplicate navigation entity key" - the same "graph" failure
// navigationMergeProjectBucketsContext exists to prevent, merely moved
// from the catalog page to the project detail. The check spans tiers, not
// just one tier: the project resource normalizes current, recent, and
// archived into a single document under one resource key, so a pair whose
// groups classified into different tiers (one group's members older than
// hubcore's archive window, the other's inside it) collides exactly the same.
//
// Colliding rows are merged, never dropped, on the identity they share: their
// children combine, their counts add, and the union takes the identity's most
// recent moment. That moment decides the tier the union is kept in, which is
// the tier a tree-built project holding the same members would put its one
// cluster in. Reusing the id keeps the client's addressing stable across the
// merge; a per-merge id would rename an addressable row on every merge.
//
// When no identity collides - the common case - the three slices are returned
// exactly as passed in, so the ordinary merge path allocates nothing new.
func navigationMergeClusterRowsContext(ctx context.Context, tiers ...[]hubcore.TreeNode) ([][]hubcore.TreeNode, error) {
	merged := make(map[string]hubcore.TreeNode)
	winner := make(map[string]hubcore.TreeNode)
	winnerTier := make(map[string]int)
	winnerIndex := make(map[string]int)
	children := make(map[string][]hubcore.TreeNode)
	counts := make(map[string]int)
	collided := false
	for tierIndex, rows := range tiers {
		for rowIndex, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if row.Kind != "cluster" || row.ID == "" {
				continue
			}
			previous, seen := merged[row.ID]
			if !seen {
				merged[row.ID] = row
				winner[row.ID] = row
				winnerTier[row.ID] = tierIndex
				winnerIndex[row.ID] = rowIndex
				continue
			}
			collided = true
			// The members accumulate in encounter order and are ordered once,
			// when the surviving row is finalized below: folding the rows
			// pair by pair instead would copy and re-sort the accumulated
			// members on every collision - quadratic in the groups that can
			// mint one cluster id (unresolved directories sharing a basename).
			if children[row.ID] == nil {
				children[row.ID] = append(make([]hubcore.TreeNode, 0, len(previous.Children)+len(row.Children)), previous.Children...)
				counts[row.ID] = previous.ClusterCount
			}
			children[row.ID] = append(children[row.ID], row.Children...)
			counts[row.ID] += row.ClusterCount
			if row.UpdatedAt.After(winner[row.ID].UpdatedAt) {
				winner[row.ID] = row
				winnerTier[row.ID] = tierIndex
				winnerIndex[row.ID] = rowIndex
			}
		}
	}
	if !collided {
		return tiers, nil
	}
	out := make([][]hubcore.TreeNode, len(tiers))
	for tierIndex, rows := range tiers {
		kept := make([]hubcore.TreeNode, 0, len(rows))
		for rowIndex, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if row.Kind == "cluster" && row.ID != "" {
				if winnerTier[row.ID] != tierIndex || winnerIndex[row.ID] != rowIndex {
					continue // folded into the identity's one surviving row
				}
				if children[row.ID] != nil {
					finalized, err := navigationFinalizeClusterRowContext(ctx, merged[row.ID], winner[row.ID], children[row.ID], counts[row.ID])
					if err != nil {
						return nil, err
					}
					row = finalized
				}
			}
			kept = append(kept, row)
		}
		out[tierIndex] = kept
	}
	return out, nil
}

// navigationFinalizeClusterRow assembles the one row that an identity's
// colliding cluster occurrences collapse to: every member of every occurrence
// folds under the first occurrence's row, the member count is the occurrences'
// sum, and the row keeps the identity's most recent moment - the winner
// occurrence's UpdatedAt and Age, with ties keeping the first occurrence to
// reach it. The surviving id is the caller's to keep - it is the row's
// address - and this function never mints one.
//
// The members arrive in the encounter order the reconciliation scan collected
// them in, and are ordered by navigationTreeNodeLess once, here, so the union
// is most-recent-first the way a single tree-built cluster's members are -
// instead of one cluster's members followed by another's, or a re-sort of the
// accumulated members on every collision. The ordering cannot observe ctx, so
// it checks before it starts, surfacing a cancellation that arrived during the
// scan rather than after the union has been ordered.
//
// Children are unioned rather than deduplicated, matching the rest of the
// merge: a session belongs to exactly one tree group, so two groups' cluster
// members cannot share a session id.
func navigationFinalizeClusterRowContext(ctx context.Context, first, winner hubcore.TreeNode, members []hubcore.TreeNode, count int) (hubcore.TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return hubcore.TreeNode{}, err
	}
	union := first
	union.Children = members
	sort.SliceStable(union.Children, func(i, j int) bool { return navigationTreeNodeLess(union.Children[i], union.Children[j]) })
	union.ClusterCount = count
	union.UpdatedAt, union.Age = winner.UpdatedAt, winner.Age
	return union, nil
}

// navigationTierOverflow is the per-tier overflow a tree-built project reports
// for n rows: how many lie beyond the cap it keeps public. It mirrors hubcore's
// capTier overflow return (capTier is unexported) without slicing the rows
// away, because a merged tier must keep every row for TierRows.
func navigationTierOverflow(n, capacity int) int {
	if n <= capacity {
		return 0
	}
	return n - capacity
}

// navigationTreeNodeLess orders two tree rows the way hubcore orders its own
// session rows (sessionOrderLess over sessionMetaOrderKey): most recently
// updated first, then most recently created, then the trimmed, case-insensitive
// title, then the id. A TreeNode's UpdatedAt / CreatedAt / Title are already the
// normalized values the tree's order key is built from, so a merged tier sorted
// with this comparator interleaves correctly with the rows capTier keeps and
// the rows ProjectPage serves.
func navigationTreeNodeLess(a, b hubcore.TreeNode) bool {
	au := hubcore.OrderUpdatedAt(a.UpdatedAt, a.CreatedAt)
	bu := hubcore.OrderUpdatedAt(b.UpdatedAt, b.CreatedAt)
	if !au.Equal(bu) {
		return au.After(bu)
	}
	ac := hubcore.OrderCreatedAt(a.CreatedAt, a.UpdatedAt)
	bc := hubcore.OrderCreatedAt(b.CreatedAt, b.UpdatedAt)
	if !ac.Equal(bc) {
		return ac.After(bc)
	}
	if cmp := navigationCompareOrderText(a.Title, b.Title); cmp != 0 {
		return cmp < 0
	}
	return navigationCompareOrderText(a.ID, b.ID) < 0
}

// navigationCompareOrderText matches hubcore's compareOrderText: trimmed,
// case-insensitive text compares first, and the raw text breaks a
// case-insensitive tie.
func navigationCompareOrderText(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	af := strings.ToLower(a)
	bf := strings.ToLower(b)
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// navigationMergeProjectSources is the distinct, sorted union of two projects'
// owning sources. The result is nil when neither group names a source, so a
// controller-local project keeps the zero value that navigationProjectSources
// spells as "no sources".
func navigationMergeProjectSources(first, next []string) []string {
	if len(first) == 0 && len(next) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(first)+len(next))
	out := make([]string, 0, len(first)+len(next))
	for _, sources := range [][]string{first, next} {
		for _, source := range sources {
			if seen[source] {
				continue
			}
			seen[source] = true
			out = append(out, source)
		}
	}
	sort.Strings(out)
	return out
}

func cloneNavigationNodesContext(ctx context.Context, nodes []hubcore.TreeNode) ([]hubcore.TreeNode, error) {
	out := make([]hubcore.TreeNode, len(nodes))
	for index, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[index] = node
		children, err := cloneNavigationNodesContext(ctx, node.Children)
		if err != nil {
			return nil, err
		}
		out[index].Children = children
	}
	return out, nil
}

func navigationPinCandidatesContext(ctx context.Context, tree hubcore.Tree) ([]hubcore.TreeNode, error) {
	// Tree.PinCandidates reads hubcore's retained uncapped slices. Snapshot
	// fixtures and deserialized trees may only have exported tier fields, so use
	// TierRows and retain the same session/cluster eligibility here.
	seen := make(map[string]bool)
	out := make([]hubcore.TreeNode, 0)
	appendNode := func(node hubcore.TreeNode) {
		if node.ID == "" || node.Kind != "session" || seen[node.ID] {
			return
		}
		seen[node.ID] = true
		out = append(out, node)
	}
	appendRows := func(rows []hubcore.TreeNode) error {
		for _, node := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch node.Kind {
			case "session":
				appendNode(node)
			case "cluster":
				for _, child := range node.Children {
					if err := ctx.Err(); err != nil {
						return err
					}
					appendNode(child)
				}
			}
		}
		return nil
	}
	if err := appendRows(tree.PinCandidates()); err != nil {
		return nil, err
	}
	for _, project := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, tier := range []string{"current", "recent", "archived"} {
			rows, _ := project.TierRows(tier)
			if err := appendRows(rows); err != nil {
				return nil, err
			}
		}
	}
	cloned, err := cloneNavigationNodesContext(ctx, out)
	return cloned, err
}

func validateNavigationInputsContext(ctx context.Context, inputs navigationBuildInputs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNavigationIdentity("generation", inputs.GenerationID, false); err != nil {
		return err
	}
	if len(inputs.Sources) > 64 {
		return fmt.Errorf("navigation has %d sources, maximum is 64", len(inputs.Sources))
	}
	for _, source := range inputs.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateNavigationIdentity("source ID", source.ID, false); err != nil {
			return err
		}
		if err := validateNavigationIdentity("source kind", source.Kind, false); err != nil {
			return err
		}
	}
	for _, section := range inputs.PinSections {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateNavigationIdentity("pin section ID", section.ID, false); err != nil {
			return err
		}
	}
	for _, project := range append(append([]hubcore.TreeProject(nil), inputs.Tree.Projects...), inputs.Tree.ArchivedProjects...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateNavigationIdentity("project key", project.Key, false); err != nil {
			return err
		}
		for _, tier := range []string{"current", "recent", "archived"} {
			rows, ok := project.TierRows(tier)
			if !ok {
				return fmt.Errorf("project %q has invalid %s tier", project.Key, tier)
			}
			if err := validateNavigationNodesContext(ctx, rows); err != nil {
				return err
			}
		}
	}
	if err := validateNavigationNodesContext(ctx, inputs.Tree.Live); err != nil {
		return err
	}
	return validateNavigationNodesContext(ctx, inputs.Tree.NeedsYou)
}

func navigationSources(sources []hubapi.Source) hubapi.NavigationArray[hubapi.Source] {
	out := make(hubapi.NavigationArray[hubapi.Source], 0, len(sources))
	for _, source := range sources {
		source.Label = truncateNavigationRunes(source.Label, maxNavigationLabelRunes)
		out = append(out, source)
	}
	return out
}

func validateNavigationNodesContext(ctx context.Context, rows []hubcore.TreeNode) error {
	for _, node := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := navigationNodeRef(node); err != nil {
			return err
		}
		for _, jobs := range [2][]appwire.EvenerJobInfo{node.RunningJobs, node.CompletedJobs} {
			for _, job := range jobs {
				if err := validateNavigationIdentity("job ID", job.JobID, false); err != nil {
					return err
				}
			}
		}
		if err := validateNavigationNodesContext(ctx, node.Children); err != nil {
			return err
		}
	}
	return nil
}

func validateNavigationIdentity(kind, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" {
		return fmt.Errorf("navigation %s is empty", kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("navigation %s is not valid UTF-8", kind)
	}
	if len(value) > maxNavigationIdentityBytes {
		return fmt.Errorf("navigation %s exceeds %d bytes", kind, maxNavigationIdentityBytes)
	}
	return nil
}
func navigationRef(id string) (hubapi.Ref, error) {
	if err := validateNavigationIdentity("session ID", id, false); err != nil {
		return hubapi.Ref{}, err
	}
	refText := id
	if !strings.Contains(id, ":") {
		refText = hubapi.LocalRef(id).String()
	}
	ref, err := hubapi.ParseRef(refText)
	if err != nil {
		return hubapi.Ref{}, fmt.Errorf("malformed navigation session identity %q: %w", id, err)
	}
	if len(ref.String()) > maxNavigationIdentityBytes {
		return hubapi.Ref{}, fmt.Errorf("navigation ref exceeds %d bytes", maxNavigationIdentityBytes)
	}
	return ref, nil
}

func navigationNodeRef(node hubcore.TreeNode) (hubapi.Ref, error) {
	if node.Ref != "" {
		return navigationRef(node.Ref)
	}
	return navigationRef(node.ID)
}

func (p navigationProjection) Manifest() hubapi.NavigationManifest {
	manifest := p.manifest
	manifest.Sources = append(hubapi.NavigationArray[hubapi.Source](nil), p.manifest.Sources...)
	return manifest
}

func (p navigationProjection) LivePage(offset uint32, limit int) hubapi.NavigationSectionResource {
	return p.sectionPage(p.live, offset, limit)
}
func (p navigationProjection) NeedsYouPage(offset uint32, limit int) hubapi.NavigationSectionResource {
	return p.sectionPage(p.needsYou, offset, limit)
}
func (p navigationProjection) PinSectionPage(id string, offset uint32, limit int) (hubapi.NavigationSectionResource, bool) {
	for _, section := range p.pinSections {
		if section.id == id {
			return p.sectionPage(section.rows, offset, limit), true
		}
	}
	return hubapi.NavigationSectionResource{}, false
}

func (p navigationProjection) sectionPage(rows []hubcore.TreeNode, offset uint32, limit int) hubapi.NavigationSectionResource {
	page, sourceRemaining := navigationPage(rows, offset, limit, maxNavigationSectionRows)
	projector := navigationProjector{projection: p}
	sessions := projector.projectNodes(page, maxNavigationSectionRows)
	remaining := sourceRemaining + len(page) - len(sessions)
	resource := hubapi.NavigationSectionResource{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Sessions: sessions, Remaining: remaining, Truncated: projector.truncated}
	fitNavigationSection(&resource)
	return resource
}

func (p navigationProjection) PinCatalogPage(offset uint32, limit int) hubapi.NavigationPinSectionCatalog {
	limit = navigationLimit(limit, maxNavigationCatalogRows)
	start, end := navigationRange(len(p.pinSections), offset, limit)
	rows := make(hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor], 0, end-start)
	for _, section := range p.pinSections[start:end] {
		candidate := hubapi.NavigationPinSectionDescriptor{ID: section.id, Name: truncateNavigationRunes(section.name, maxNavigationLabelRunes), Count: section.memberCount}
		response := hubapi.NavigationPinSectionCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, PinSections: append(append(hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor](nil), rows...), candidate), Remaining: len(p.pinSections) - start - len(rows) - 1}
		if !navigationJSONFits(response, maxNavigationCatalogBytes) {
			break
		}
		rows = append(rows, candidate)
	}
	return hubapi.NavigationPinSectionCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, PinSections: rows, Remaining: len(p.pinSections) - start - len(rows)}
}

func (p navigationProjection) CatalogPage(kind navigationResourceKind, offset uint32, limit int) (hubapi.NavigationProjectCatalog, error) {
	projects, ok := p.catalogs[kind]
	if !ok {
		return hubapi.NavigationProjectCatalog{}, fmt.Errorf("unknown navigation catalog %q", kind)
	}
	limit = navigationLimit(limit, maxNavigationCatalogRows)
	start, end := navigationRange(len(projects), offset, limit)
	rows := make(hubapi.NavigationArray[hubapi.NavigationProjectSummary], 0, end-start)
	for _, project := range projects[start:end] {
		candidate := p.projectSummary(project)
		response := hubapi.NavigationProjectCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Projects: append(append(hubapi.NavigationArray[hubapi.NavigationProjectSummary](nil), rows...), candidate), Remaining: len(projects) - start - len(rows) - 1}
		if !navigationJSONFits(response, maxNavigationCatalogBytes) {
			break
		}
		rows = append(rows, candidate)
	}
	remaining := len(projects) - start - len(rows)
	return hubapi.NavigationProjectCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Projects: rows, Remaining: remaining}, nil
}

func (p navigationProjection) Project(key string) (hubapi.NavigationProjectResource, bool) {
	project, ok := p.projects[key]
	if !ok {
		return hubapi.NavigationProjectResource{}, false
	}
	projector := navigationProjector{projection: p}
	current, currentRemaining := projector.projectTier(project, "current", 0, maxNavigationSectionRows)
	recent, recentRemaining := projector.projectTier(project, "recent", 0, maxNavigationSectionRows)
	archived, archivedRemaining := projector.projectTier(project, "archived", 0, maxNavigationSectionRows)
	resource := hubapi.NavigationProjectResource{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: key, Current: hubapi.NavigationTier{Sessions: current, Remaining: currentRemaining}, Recent: hubapi.NavigationTier{Sessions: recent, Remaining: recentRemaining}, Archived: hubapi.NavigationTier{Sessions: archived, Remaining: archivedRemaining}, Truncated: projector.truncated}
	fitNavigationProject(&resource)
	return resource, true
}

func (p navigationProjection) ProjectPage(key, tier string, offset uint32, limit int) (hubapi.NavigationProjectPage, error) {
	project, ok := p.projects[key]
	if !ok {
		return hubapi.NavigationProjectPage{}, fmt.Errorf("navigation project %q not found", key)
	}
	if tier != "current" && tier != "recent" && tier != "archived" {
		return hubapi.NavigationProjectPage{}, fmt.Errorf("invalid navigation tier %q", tier)
	}
	projector := navigationProjector{projection: p}
	sessions, remaining := projector.projectTier(project, tier, offset, limit)
	resource := hubapi.NavigationProjectPage{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: key, Tier: tier, Offset: offset, Sessions: sessions, Remaining: remaining, Truncated: projector.truncated}
	fitNavigationProjectPage(&resource)
	return resource, nil
}

// navigationEnvelopeMarshal is a test seam for counting complete candidate
// probes. Production always uses encoding/json.Marshal.
var navigationEnvelopeMarshal = json.Marshal

// The fitters marshal complete candidate envelopes. When a resource is too
// large, they retain the largest deterministic left-to-right node prefix found
// by binary search; that is equivalent to pruning rightmost branches first but
// needs O(log n) full-envelope probes rather than one marshal per removed node.
func fitNavigationSection(resource *hubapi.NavigationSectionResource) {
	if navigationJSONFits(*resource, maxNavigationResponseBytes) {
		return
	}
	original := cloneNavigationSummaries(resource.Sessions)
	baseRemaining := resource.Remaining
	trim, budget := navigationFittingChoice(navigationSummaryNodes(original), func(trim navigationWatchPayloadTrim, budget int) bool {
		rows, dropped := limitNavigationSummaries(original, budget)
		trimNavigationWatchPayloads(rows, trim)
		candidate := *resource
		candidate.Sessions = rows
		candidate.Remaining = baseRemaining + dropped
		candidate.Truncated = true
		return navigationJSONFits(candidate, maxNavigationResponseBytes)
	})
	resource.Sessions, _ = limitNavigationSummaries(original, budget)
	trimNavigationWatchPayloads(resource.Sessions, trim)
	resource.Remaining = baseRemaining + len(original) - len(resource.Sessions)
	resource.Truncated = true
}

func fitNavigationProjectPage(resource *hubapi.NavigationProjectPage) {
	if navigationJSONFits(*resource, maxNavigationResponseBytes) {
		return
	}
	original := cloneNavigationSummaries(resource.Sessions)
	baseRemaining := resource.Remaining
	trim, budget := navigationFittingChoice(navigationSummaryNodes(original), func(trim navigationWatchPayloadTrim, budget int) bool {
		rows, dropped := limitNavigationSummaries(original, budget)
		trimNavigationWatchPayloads(rows, trim)
		candidate := *resource
		candidate.Sessions = rows
		candidate.Remaining = baseRemaining + dropped
		candidate.Truncated = true
		return navigationJSONFits(candidate, maxNavigationResponseBytes)
	})
	resource.Sessions, _ = limitNavigationSummaries(original, budget)
	trimNavigationWatchPayloads(resource.Sessions, trim)
	resource.Remaining = baseRemaining + len(original) - len(resource.Sessions)
	resource.Truncated = true
}

func fitNavigationProject(resource *hubapi.NavigationProjectResource) {
	if navigationJSONFits(*resource, maxNavigationResponseBytes) {
		return
	}
	original := cloneNavigationProjectResource(*resource)
	nodes := navigationSummaryNodes(original.Current.Sessions) + navigationSummaryNodes(original.Recent.Sessions) + navigationSummaryNodes(original.Archived.Sessions)
	trim, budget := navigationFittingChoice(nodes, func(trim navigationWatchPayloadTrim, budget int) bool {
		candidate := limitNavigationProject(original, budget)
		trimNavigationProjectWatchPayloads(&candidate, trim)
		return navigationJSONFits(candidate, maxNavigationResponseBytes)
	})
	limited := limitNavigationProject(original, budget)
	trimNavigationProjectWatchPayloads(&limited, trim)
	*resource = limited
}

// navigationFittingChoice finds the largest session-row budget that fits. It
// tries the full payload first, preserving the pre-existing answer and probe
// count. Only when even one untrimmed row cannot fit does it degrade optional
// watch payloads - delivery instants first, then whole watch rows - so a
// session whose watches are what overflowed the response is still listed with
// as much of its payload as fits, instead of being dropped and leaving the
// page with no rows while data remains (which validateNavigationPageProgress
// rejects outright). It returns the trim level and budget actually used.
func navigationFittingChoice(nodes int, fits func(navigationWatchPayloadTrim, int) bool) (navigationWatchPayloadTrim, int) {
	full := navigationFittingBudget(nodes, func(budget int) bool {
		return fits(navigationWatchPayloadFull, budget)
	})
	if full > 0 || nodes == 0 {
		return navigationWatchPayloadFull, full
	}
	for _, trim := range []navigationWatchPayloadTrim{navigationWatchPayloadNoDeliveryTimes, navigationWatchPayloadNoWatches} {
		budget := navigationFittingBudget(nodes, func(budget int) bool {
			return fits(trim, budget)
		})
		if budget > 0 {
			return trim, budget
		}
	}
	// No trim level retains a row: the overflow is not in the watch payload
	// (a job-heavy single row, for example). Report the untrimmed zero-budget
	// result so the caller keeps its existing irreducible-overflow behavior.
	return navigationWatchPayloadFull, 0
}

// navigationWatchPayloadTrim is a degradation level for the optional watch
// payload on session rows. The zero value leaves rows untouched.
type navigationWatchPayloadTrim int

const (
	navigationWatchPayloadFull navigationWatchPayloadTrim = iota
	navigationWatchPayloadNoDeliveryTimes
	navigationWatchPayloadNoWatches
)

// trimNavigationWatchPayloads strips optional watch payload from rows in place.
// The rows are always fitters' clones (limitNavigationSummaries clones every
// included row), so trimming never reaches the projector's original.
func trimNavigationWatchPayloads(rows []hubapi.NavigationSessionSummary, trim navigationWatchPayloadTrim) {
	if trim == navigationWatchPayloadFull {
		return
	}
	for i := range rows {
		switch trim {
		case navigationWatchPayloadNoDeliveryTimes:
			for j := range rows[i].Watches {
				rows[i].Watches[j].DeliveryTimes = nil
			}
		case navigationWatchPayloadNoWatches:
			// Shedding a whole row is still an omission the UI must be able to
			// report, so the counts move before the rows go. The armed subset
			// moves with the total, preserving 0 <= armed <= omitted. A later
			// trim level that drops no row (NoDeliveryTimes) must leave both
			// alone.
			rows[i].OmittedArmedWatches += countArmedNavigationWatches(rows[i].Watches)
			rows[i].OmittedWatches += len(rows[i].Watches)
			rows[i].Watches = nil
		}
		trimNavigationWatchPayloads(rows[i].Children, trim)
	}
}

// countArmedNavigationWatches counts the armed rows in a watch list. Used only
// when the fitter sheds a list, so the omitted armed count grows by exactly the
// armed rows that leave.
func countArmedNavigationWatches(watches hubapi.NavigationArray[hubapi.NavigationWatchSummary]) int {
	count := 0
	for _, watch := range watches {
		if watch.Active {
			count++
		}
	}
	return count
}

func trimNavigationProjectWatchPayloads(resource *hubapi.NavigationProjectResource, trim navigationWatchPayloadTrim) {
	trimNavigationWatchPayloads(resource.Current.Sessions, trim)
	trimNavigationWatchPayloads(resource.Recent.Sessions, trim)
	trimNavigationWatchPayloads(resource.Archived.Sessions, trim)
}

func navigationFittingBudget(nodes int, fits func(int) bool) int {
	low, high := 0, nodes+1 // nodes is known not to fit; zero always fits.
	for high-low > 1 {
		middle := low + (high-low)/2
		if fits(middle) {
			low = middle
		} else {
			high = middle
		}
	}
	return low
}

func navigationSummaryWeight(summary hubapi.NavigationSessionSummary) int {
	weight := 1 + summary.OmittedDescendants
	for _, child := range summary.Children {
		weight += navigationSummaryWeight(child)
	}
	return weight
}

func navigationJSONFits(value any, maxBytes int) bool {
	encoded, err := navigationEnvelopeMarshal(value)
	return err == nil && len(encoded) <= maxBytes
}

func navigationSummaryNodes(rows []hubapi.NavigationSessionSummary) int {
	count := 0
	for _, row := range rows {
		count++
		count += navigationSummaryNodes(row.Children)
	}
	return count
}

func cloneNavigationSummaries(rows []hubapi.NavigationSessionSummary) hubapi.NavigationArray[hubapi.NavigationSessionSummary] {
	clone := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], len(rows))
	for index, row := range rows {
		clone[index] = cloneNavigationSummary(row)
	}
	return clone
}

func limitNavigationSummaries(rows []hubapi.NavigationSessionSummary, budget int) (hubapi.NavigationArray[hubapi.NavigationSessionSummary], int) {
	remaining := budget
	limited := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], 0, len(rows))
	for index, row := range rows {
		candidate, included, complete := limitNavigationSummary(row, &remaining)
		if !included {
			return limited, len(rows) - index
		}
		limited = append(limited, candidate)
		if !complete {
			return limited, len(rows) - index - 1
		}
	}
	return limited, 0
}

func limitNavigationSummary(row hubapi.NavigationSessionSummary, budget *int) (hubapi.NavigationSessionSummary, bool, bool) {
	if *budget == 0 {
		return hubapi.NavigationSessionSummary{}, false, false
	}
	*budget--
	limited := cloneNavigationSummary(row)
	limited.Children = hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}
	for index, child := range row.Children {
		candidate, included, complete := limitNavigationSummary(child, budget)
		if !included {
			for _, omitted := range row.Children[index:] {
				limited.OmittedDescendants += navigationSummaryWeight(omitted)
			}
			return limited, true, false
		}
		limited.Children = append(limited.Children, candidate)
		if !complete {
			for _, omitted := range row.Children[index+1:] {
				limited.OmittedDescendants += navigationSummaryWeight(omitted)
			}
			return limited, true, false
		}
	}
	return limited, true, true
}

func cloneNavigationProjectResource(resource hubapi.NavigationProjectResource) hubapi.NavigationProjectResource {
	clone := resource
	clone.Current.Sessions = cloneNavigationSummaries(resource.Current.Sessions)
	clone.Recent.Sessions = cloneNavigationSummaries(resource.Recent.Sessions)
	clone.Archived.Sessions = cloneNavigationSummaries(resource.Archived.Sessions)
	return clone
}

func limitNavigationProject(resource hubapi.NavigationProjectResource, budget int) hubapi.NavigationProjectResource {
	resource.Truncated = true
	resource.Current.Sessions, resource.Current.Remaining = limitNavigationTier(resource.Current, budget)
	budget -= navigationSummaryNodes(resource.Current.Sessions)
	resource.Recent.Sessions, resource.Recent.Remaining = limitNavigationTier(resource.Recent, budget)
	budget -= navigationSummaryNodes(resource.Recent.Sessions)
	resource.Archived.Sessions, resource.Archived.Remaining = limitNavigationTier(resource.Archived, budget)
	return resource
}

func limitNavigationTier(tier hubapi.NavigationTier, budget int) (hubapi.NavigationArray[hubapi.NavigationSessionSummary], int) {
	rows, dropped := limitNavigationSummaries(tier.Sessions, budget)
	return rows, tier.Remaining + dropped
}

func (p navigationProjection) Location(ref string) (hubapi.NavigationSessionLocation, bool) {
	location, ok := p.locations[ref]
	if !ok {
		return hubapi.NavigationSessionLocation{}, false
	}
	if location.Session != nil {
		summary := cloneNavigationSummary(*location.Session)
		location.Session = &summary
	}
	return location, true
}

type navigationPageProgressInvariantError struct {
	kind navigationResourceKind
}

func (err navigationPageProgressInvariantError) Error() string {
	return fmt.Sprintf("navigation page progress invariant: %s returned no rows with remaining data", err.kind)
}

// navigationSessionLocationInvariantError reports a location fit that dropped the
// session the location exists to serve.
type navigationSessionLocationInvariantError struct {
	kind navigationResourceKind
}

func (err navigationSessionLocationInvariantError) Error() string {
	return fmt.Sprintf("navigation location invariant: %s was fitted without its session", err.kind)
}

func validateNavigationPageProgress(kind navigationResourceKind, resource any) error {
	var rows, remaining int
	switch value := resource.(type) {
	case hubapi.NavigationSectionResource:
		rows, remaining = len(value.Sessions), value.Remaining
	case hubapi.NavigationPinSectionCatalog:
		rows, remaining = len(value.PinSections), value.Remaining
	case hubapi.NavigationProjectCatalog:
		rows, remaining = len(value.Projects), value.Remaining
	case hubapi.NavigationProjectResource:
		rows = len(value.Current.Sessions) + len(value.Recent.Sessions) + len(value.Archived.Sessions)
		remaining = value.Current.Remaining + value.Recent.Remaining + value.Archived.Remaining
	case hubapi.NavigationProjectPage:
		rows, remaining = len(value.Sessions), value.Remaining
	case hubapi.NavigationSessionLocation:
		// A location IS its session: a deep link renders that summary and nothing
		// else. The fitter's last resort for this kind drops the session to keep the
		// envelope, which would answer 200 with nothing to render where an
		// irreducible overflow used to be reported, so a session-less location never
		// passes as a fit.
		if value.Session == nil {
			return navigationSessionLocationInvariantError{kind: kind}
		}
		return nil
	default:
		return nil
	}
	if rows == 0 && remaining > 0 {
		return navigationPageProgressInvariantError{kind: kind}
	}
	return nil
}

func (p navigationProjection) Resource(key navigationResourceKey) (any, navigationFingerprint, error) {
	var resource any
	var err error
	switch key.Kind {
	case navigationResourceManifest:
		resource = p.Manifest()
	case navigationResourceLive:
		resource = p.LivePage(key.Offset, int(key.Limit))
	case navigationResourceNeedsYou:
		resource = p.NeedsYouPage(key.Offset, int(key.Limit))
	case navigationResourcePinCatalog:
		resource = p.PinCatalogPage(key.Offset, int(key.Limit))
	case navigationResourcePinSection:
		var ok bool
		sectionID := key.SectionID
		if sectionID == "" {
			sectionID = key.ID
		}
		resource, ok = p.PinSectionPage(sectionID, key.Offset, int(key.Limit))
		if !ok {
			err = fmt.Errorf("navigation pin section %q not found", sectionID)
		}
	case navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		resource, err = p.CatalogPage(key.Kind, key.Offset, int(key.Limit))
	case navigationResourceProject:
		var ok bool
		resource, ok = p.Project(key.ProjectKey)
		if !ok {
			err = fmt.Errorf("navigation project %q not found", key.ProjectKey)
		}
	case navigationResourceProjectPage:
		resource, err = p.ProjectPage(key.ProjectKey, key.Tier, key.Offset, int(key.Limit))
	case navigationResourceLocation:
		var ok bool
		resource, ok = p.Location(key.ID)
		if !ok {
			err = fmt.Errorf("navigation session %q not found", key.ID)
		}
	default:
		err = fmt.Errorf("unknown navigation resource %q", key.Kind)
	}
	if err != nil {
		return nil, navigationFingerprint{}, err
	}
	resource = navigationResourceWithRevision(resource, key.Revision)
	if err := validateNavigationPageProgress(key.Kind, resource); err != nil {
		return nil, navigationFingerprint{}, err
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		return nil, navigationFingerprint{}, fmt.Errorf("encode navigation resource: %w", err)
	}
	if key.Kind == navigationResourceManifest && len(encoded) > maxNavigationManifestBytes {
		return nil, navigationFingerprint{}, fmt.Errorf("navigation manifest exceeds %d bytes", maxNavigationManifestBytes)
	}
	return resource, sha256.Sum256(encoded), nil
}

// navigationResourceWithRevision stamps the service-owned semantic revision on
// a detached response value. A retained projection is built at revision zero
// for fingerprinting; rebuilding it for every cache miss would reopen the
// source/coherence boundary and is both wasteful and unsafe.
func navigationResourceWithRevision(resource any, revision uint64) any {
	switch value := resource.(type) {
	case hubapi.NavigationManifest:
		value.Revision = revision
		return value
	case hubapi.NavigationSectionResource:
		value.Revision = revision
		return value
	case hubapi.NavigationPinSectionCatalog:
		value.Revision = revision
		return value
	case hubapi.NavigationProjectCatalog:
		value.Revision = revision
		return value
	case hubapi.NavigationProjectResource:
		value.Revision = revision
		return value
	case hubapi.NavigationProjectPage:
		value.Revision = revision
		return value
	case hubapi.NavigationSessionLocation:
		value.Revision = revision
		return value
	default:
		return resource
	}
}

func (p navigationProjection) projectSummary(project hubcore.TreeProject) hubapi.NavigationProjectSummary {
	return hubapi.NavigationProjectSummary{Key: project.Key, Name: truncateNavigationRunes(project.Name, maxNavigationLabelRunes), WorkingDir: truncateNavigationBytes(project.WorkingDir, maxNavigationWorkingDirBytes), RollupState: project.RollupState, RollupLive: project.RollupLive, RollupAttn: project.RollupAttn, DefaultExpanded: project.Expanded, MoreCurrent: project.MoreCurrent, MoreRecent: project.MoreRecent, MoreArchived: project.MoreArchived, Worktrees: project.Worktrees, IsArchived: project.IsArchived, Favorite: projectFavoriteForSources(p.inputs.ProjectFavorite, project), Sources: navigationProjectSources(project.Sources), SessionCount: project.TotalSessionCount()}
}

// navigationProjectSources spells a tree project's owning sources for the wire.
// The tree's own spelling uses "" for the controller's sessions (the decision
// store's key) and the configured host name for a remote host's; the wire says
// "local" for the controller so a client never has to interpret an empty
// string, matching the source vocabulary the mutation params accept. A project
// whose sessions all belong to the controller returns nil, so the common local
// catalog entry keeps the zero value and the field is omitted: the same "no
// sources means the controller" default the decision readers apply. A merged
// project (the same canonical ID and path owned by the controller and one or
// more hosts) therefore always carries "local" next to its host names, which is
// what lets a caller refuse a mutation that cannot name a single owner.
func navigationProjectSources(sources []string) hubapi.NavigationArray[string] {
	if len(sources) == 0 {
		return nil
	}
	out := make(hubapi.NavigationArray[string], 0, len(sources))
	remote := false
	for _, source := range sources {
		if source == "" {
			out = append(out, "local")
			continue
		}
		remote = true
		out = append(out, source)
	}
	if !remote {
		return nil
	}
	return out
}

func (p navigationProjection) buildPinSectionsContext(ctx context.Context) ([]navigationPinSection, error) {
	byID := make(map[string]navigationPinSection, len(p.inputs.PinSections))
	for _, section := range p.inputs.PinSections {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		byID[section.ID] = navigationPinSection{id: section.ID, name: section.Name, memberCount: section.MemberCount}
	}
	assignment := cloneNavigationStringMap(p.inputs.PinSectionBySession)
	for sessionID, pin := range p.inputs.PinAssignments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if assignment[sessionID] == "" {
			assignment[sessionID] = pin.SectionID
		}
	}
	for _, node := range p.pinCandidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref, err := navigationNodeRef(node)
		if err != nil {
			continue
		}
		sectionID := assignment[node.ID]
		if sectionID == "" {
			sectionID = assignment[ref.String()]
		}
		section, ok := byID[sectionID]
		if !ok || sectionID == "" {
			continue
		}
		section.rows = append(section.rows, node)
		byID[sectionID] = section
	}
	out := make([]navigationPinSection, 0, len(byID))
	for _, section := range byID {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, section)
	}
	for index := range out {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.SliceStable(out[index].rows, func(left, right int) bool {
			leftNode, rightNode := out[index].rows[left], out[index].rows[right]
			if !leftNode.UpdatedAt.Equal(rightNode.UpdatedAt) {
				return leftNode.UpdatedAt.After(rightNode.UpdatedAt)
			}
			leftRef, _ := navigationNodeRef(leftNode)
			rightRef, _ := navigationNodeRef(rightNode)
			if leftRef.String() != rightRef.String() {
				return leftRef.String() < rightRef.String()
			}
			return leftNode.ID < rightNode.ID
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].name), strings.ToLower(out[j].name)
		if left == right {
			return out[i].id < out[j].id
		}
		return left < right
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (p navigationProjection) indexLocationsContext(ctx context.Context) error {
	indexRows := func(rows []hubcore.TreeNode, projectKey, tier string) {
		for _, root := range rows {
			if ctx.Err() != nil {
				return
			}
			_ = p.indexLocationNodeContext(ctx, root, root, projectKey, tier, true)
		}
	}
	for _, kind := range []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns} {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, project := range p.catalogs[kind] {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, tier := range []string{"current", "recent", "archived"} {
				rows, _ := project.TierRows(tier)
				indexRows(rows, project.Key, tier)
			}
		}
	}
	indexRows(p.live, "", "live")
	if err := ctx.Err(); err != nil {
		return err
	}
	indexRows(p.needsYou, "", "needs_you")
	return ctx.Err()
}

func (p navigationProjection) indexLocationNodeContext(ctx context.Context, node, root hubcore.TreeNode, projectKey, tier string, topLevel bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ref, err := navigationNodeRef(node)
	if err != nil {
		return err
	}
	rootRef, err := navigationNodeRef(root)
	if err != nil {
		return err
	}
	if _, exists := p.locations[ref.String()]; !exists {
		summary := navigationProjector{projection: p}.projectShallow(node)
		p.locations[ref.String()] = hubapi.NavigationSessionLocation{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Ref: ref.String(), TopLevelRef: rootRef.String(), ProjectKey: projectKey, TopLevel: topLevel, Tier: tier, PinSectionID: p.pinSectionFor(node.ID, ref.String()), Session: &summary}
	}
	for _, child := range node.Children {
		if err := p.indexLocationNodeContext(ctx, child, root, projectKey, tier, false); err != nil {
			return err
		}
	}
	return nil
}

// navigationTraversal is shared by every recursive row projection in a single
// resource. A project root deliberately uses one traversal across all tiers.
type navigationTraversal struct {
	nodes     int
	depth     int
	truncated bool
}

type navigationProjector struct {
	navigationTraversal
	projection navigationProjection
}

func (p *navigationProjector) projectNodes(rows []hubcore.TreeNode, limit int) hubapi.NavigationArray[hubapi.NavigationSessionSummary] {
	limit = navigationLimit(limit, maxNavigationSectionRows)
	out := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], 0, min(limit, len(rows)))
	for _, row := range rows {
		if len(out) >= limit {
			p.truncated = true
			break
		}
		node, ok := p.projectNode(row, 1)
		if !ok {
			break
		}
		out = append(out, node)
	}
	return out
}

func (p *navigationProjector) projectTier(project hubcore.TreeProject, tier string, offset uint32, limit int) (hubapi.NavigationArray[hubapi.NavigationSessionSummary], int) {
	rows, ok := project.TierRows(tier)
	if !ok {
		return hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}, 0
	}
	page, sourceRemaining := navigationPage(rows, offset, limit, maxNavigationSectionRows)
	sessions := p.projectNodes(page, maxNavigationSectionRows)
	return sessions, sourceRemaining + len(page) - len(sessions)
}

func (p *navigationProjector) projectNode(node hubcore.TreeNode, depth int) (hubapi.NavigationSessionSummary, bool) {
	p.depth = max(p.depth, depth)
	if p.nodes >= maxNavigationNodes || depth > maxNavigationDepth {
		p.truncated = true
		return hubapi.NavigationSessionSummary{}, false
	}
	summary := p.projectShallow(node)
	p.nodes++
	if depth == maxNavigationDepth {
		if omitted := countTreeNodes(node.Children); omitted != 0 {
			summary.OmittedDescendants = omitted
			p.truncated = true
		}
		return summary, true
	}
	for index, child := range node.Children {
		if index >= maxNavigationChildren {
			summary.OmittedDescendants += countTreeNodes(node.Children[index:])
			p.truncated = true
			break
		}
		projected, ok := p.projectNode(child, depth+1)
		if !ok {
			summary.OmittedDescendants += countTreeNodes(node.Children[index:])
			break
		}
		summary.Children = append(summary.Children, projected)
	}
	return summary, true
}

func (p navigationProjector) projectShallow(node hubcore.TreeNode) hubapi.NavigationSessionSummary {
	ref, _ := navigationNodeRef(node)
	updated := node.UpdatedAt
	var updatedAt *time.Time
	if !updated.IsZero() {
		updatedAt = &updated
	}
	pinned := p.projection.pinSectionFor(node.ID, ref.String()) != ""
	watches, omittedWatches, omittedArmedWatches := navigationWatches(node.Watches)
	return hubapi.NavigationSessionSummary{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
		Title:     truncateNavigationRunes(node.Title, maxNavigationTitleRunes),
		// project is an IDENTITY on the wire, not a rendered label: the web codec
		// validates it with identity(value.project, true) and the hub schema mirrors
		// that, both capping it at maxNavigationIdentityBytes BYTES. The rune-bounded
		// label helper capped it at 512 runes, which is up to ~2 KiB of multibyte
		// text -- a summary the codec rejects, failing the whole navigation response.
		// Bound it in bytes for the same limit.
		Project:             truncateNavigationBytes(node.Project, maxNavigationIdentityBytes),
		State:               node.State,
		Kind:                node.Kind,
		Branch:              truncateNavigationRunes(node.Branch, maxNavigationLabelRunes),
		ClusterCount:        node.ClusterCount,
		Favorite:            !pinned && p.projection.sessionFavorite(node.ID, ref.String()),
		Rename:              p.projection.renameable(node.ID, ref.String()),
		Live:                p.projection.isLive(node.ID, ref.String()) && hubcore.NormalizeState(node.State) != "ended",
		AskPending:          node.AskPending,
		Dormant:             node.Dormant,
		UpdatedAt:           updatedAt,
		MoreSubagents:       node.MoreSubagents,
		RunningJobs:         navigationJobs(node.RunningJobs),
		CompletedJobs:       navigationJobs(node.CompletedJobs),
		Watches:             watches,
		OmittedWatches:      omittedWatches,
		OmittedArmedWatches: omittedArmedWatches,
		Children:            hubapi.NavigationArray[hubapi.NavigationSessionSummary]{},
	}
}

func navigationJobs(jobs []appwire.EvenerJobInfo) hubapi.NavigationArray[hubapi.NavigationJobSummary] {
	out := make(hubapi.NavigationArray[hubapi.NavigationJobSummary], 0, len(jobs))
	for _, job := range jobs {
		summary := hubapi.NavigationJobSummary{
			// job_type and status are identities to the codec and the hub schema
			// (identity() at maxNavigationIdentityBytes BYTES), and unlike job_id
			// they are not guarded by the build's own identity validation. Bound
			// them in bytes: a value within the limit passes through unchanged, and a
			// pathological one is cut instead of failing the whole navigation
			// response. job_id stays raw: it is a key clients address jobs by, and an
			// over-long one is rejected by validateNavigationNodesContext rather than
			// silently rewritten to a different id.
			JobID:   job.JobID,
			JobType: truncateNavigationBytes(job.JobType, maxNavigationIdentityBytes),
			Status:  truncateNavigationBytes(job.Status, maxNavigationIdentityBytes),
			Command: truncateNavigationRunes(job.Command, maxNavigationLabelRunes),
			Task:    truncateNavigationRunes(job.Task, maxNavigationLabelRunes),
			Reason:  truncateNavigationRunes(job.Reason, maxNavigationLabelRunes),
			Intent:  truncateNavigationRunes(job.Intent, maxNavigationLabelRunes),
		}
		// Emit FullCommand only when the label bound actually cut the
		// command, so a short command isn't duplicated and a tooltip never
		// shows less than the label.
		if summary.Command != job.Command {
			summary.FullCommand = truncateNavigationRunes(job.Command, maxNavigationFullCommandRunes)
		}
		out = append(out, summary)
	}
	return out
}

// navigationWatches projects a session's own live-watch rows onto the wire
// summary. Rows are never merged across sessions, so a receiver watch that two
// daemons report stays on each session's own summary and a rollup over the
// subtree counts it once per owning row.
//
// The list is capped at maxNavigationWatches. Armed (active) rows are ordered
// ahead of inert ones before the cut, so a session with a lot of stale rows
// still reports as many armed watches as the cap allows. The second return is
// the exact number of rows the caller did NOT receive: rows past the cap plus
// any row dropped because its created_at is not a representable instant. No row
// leaves this function uncounted.
//
// The third return is how many of those omitted rows were still armed. Once a
// session holds more armed watches than the cap, the retained list alone
// understates its armed total, so the count is taken from every skipped row
// rather than inferred from the armed-first order: an unrepresentable armed row
// can be dropped before the cap fills, and the cap boundary can itself fall
// inside the armed run.
func navigationWatches(watches []appwire.EvenerWatchInfo) (hubapi.NavigationArray[hubapi.NavigationWatchSummary], int, int) {
	ordered := append([]appwire.EvenerWatchInfo(nil), watches...)
	// Stable: armed rows keep their wire order ahead of inert ones, and rows
	// within each class keep theirs.
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Active && !ordered[j].Active })
	out := make(hubapi.NavigationArray[hubapi.NavigationWatchSummary], 0, min(len(ordered), maxNavigationWatches))
	omittedArmed := 0
	for index, watch := range ordered {
		if len(out) == maxNavigationWatches {
			// Every remaining row is omitted wholesale. Count the armed ones
			// directly: the armed-first order does not make the whole tail inert.
			for _, skipped := range ordered[index:] {
				if skipped.Active {
					omittedArmed++
				}
			}
			break
		}
		// created_at is a REQUIRED watch field and the web codec validates it as
		// strict RFC3339. Truncating a malformed or oversized value (the generic
		// label cap) produced an ellipsized string the codec rejects, which
		// silently failed the whole watch-carrying navigation snapshot. A watch
		// whose created_at cannot be represented is dropped rather than poisoning
		// every other row in the resource; the omitted count below accounts for it.
		if !validNavigationTimestamp(watch.CreatedAt) {
			if watch.Active {
				omittedArmed++
			}
			continue
		}
		cadence := make([]hubapi.NavigationWatchCadence, 0, len(watch.Cadence))
		for _, step := range watch.Cadence {
			// A derived instant the codec cannot decode is dropped (absent), the
			// same way an unrepresentable delivery instant is: one missing dot is
			// honest, a rejected snapshot is not.
			nextFireAt := ""
			if validNavigationTimestamp(step.DerivedNextFireAt) {
				nextFireAt = step.DerivedNextFireAt
			}
			cadence = append(cadence, hubapi.NavigationWatchCadence{
				// kind is an IDENTITY on the wire, not a rendered label: the codec
				// validates it with identity(value.kind) and the hub schema mirrors
				// that, both capping it at maxNavigationIdentityBytes BYTES. The
				// rune-bounded label helper capped it at 512 runes, which is up to
				// ~2 KiB of multibyte text -- a summary the codec rejects, failing the
				// whole navigation response. Bound it in bytes for the same limit.
				Kind:              truncateNavigationBytes(step.Kind, maxNavigationIdentityBytes),
				Seconds:           step.Seconds,
				DerivedNextFireAt: nextFireAt,
				Every:             navigationBoundEvery(step.Every),
				Filter:            truncateNavigationRunes(step.Filter, maxNavigationLabelRunes),
			})
		}
		events := make([]string, 0, len(watch.Events))
		for _, event := range watch.Events {
			events = append(events, truncateNavigationRunes(event, maxNavigationLabelRunes))
		}
		deliveryTimes := make([]string, 0, len(watch.DeliveryTimes))
		for _, at := range watch.DeliveryTimes {
			// An instant the codec cannot decode is dropped: one missing dot is
			// honest, a rejected snapshot is not.
			if validNavigationTimestamp(at) {
				deliveryTimes = append(deliveryTimes, at)
			}
		}
		// id and source are IDENTITIES to the codec, the hub schema and the rail's
		// row keys: the rail and the panel key their rows by watch.id. A value over
		// the identity bound is DROPPED rather than cut, because cutting trades a
		// missing row for a wrong one -- two distinct long ids that share a prefix
		// collapse to the same key, and every consumer that keys by it then sees one
		// row where there were two. Every identity within the bound still passes
		// through untouched; the omitted count below accounts for the dropped row
		// exactly like a row whose created_at cannot be represented.
		if !navigationSchemaIdentity(watch.ID, false) || !navigationSchemaIdentity(watch.Source, false) {
			if watch.Active {
				omittedArmed++
			}
			continue
		}
		row := hubapi.NavigationWatchSummary{
			ID:             watch.ID,
			Source:         watch.Source,
			Target:         truncateNavigationRunes(watch.Target, maxNavigationLabelRunes),
			SendTo:         truncateNavigationRunes(watch.SendTo, maxNavigationLabelRunes),
			Note:           truncateNavigationRunes(watch.Note, maxNavigationLabelRunes),
			Cadence:        cadence,
			OutputMatch:    truncateNavigationRunes(watch.OutputMatch, maxNavigationLabelRunes),
			Events:         events,
			WildcardEvents: watch.WildcardEvents,
			Deliveries:     watch.Deliveries,
			DeliveryTimes:  deliveryTimes,
			CreatedAt:      watch.CreatedAt,
			Active:         watch.Active,
			EndReason:      truncateNavigationRunes(watch.EndReason, maxNavigationLabelRunes),
		}
		// The bounding above keeps a representable value representable, but it
		// cannot rescue a value that was never valid (an empty ID or Source, a
		// negative or over-range deliveries count, a cadence with an empty kind
		// or negative/non-finite seconds). Carrying one such row makes
		// navigationSessionValueValid reject the WHOLE session -- and through it
		// every session in the resource. The schema's own predicate is the
		// backstop: drop any row it would reject, so the omitted count below
		// accounts for it exactly like an unrepresentable created_at.
		if !navigationWatchValueValid(row) {
			if watch.Active {
				omittedArmed++
			}
			continue
		}
		out = append(out, row)
	}
	return out, len(watches) - len(out), omittedArmed
}

// navigationBoundEvery clamps the events cadence's every-Nth throttle to the
// codec's safe integer range. `every` is caller-supplied and the daemon bounds
// it only to the platform int range, so on 64-bit it can sit above 2^53-1;
// projecting it verbatim fails navigationSessionValueValid and makes the whole
// resource -- every session in it -- unreadable. Clamping rather than dropping
// is the honest choice: the wire spells an absent/zero every as "no throttle"
// (the codec reads every > 0 as a throttle), so dropping would misstate a
// throttled watch as firing on every matching event. A clamped value is still a
// throttle and still passes the schema.
func navigationBoundEvery(value int) int {
	if value > int(maxNavigationSafeInteger) {
		return int(maxNavigationSafeInteger)
	}
	return value
}

// navigationTimestampPattern matches exactly the grammar the web codec's
// rfc3339Timestamp accepts: a four-digit year, capital T, seconds, an optional
// 1-9 digit fraction, and Z or a numeric offset. The offset's hour and minute
// are captured so validNavigationTimestamp can bound them like the codec does.
// Go's time.Parse additionally enforces the calendar/clock ranges, so together
// they reject the truncated "…"-suffixed strings the label cap used to produce.
var navigationTimestampPattern = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$`,
)

func validNavigationTimestamp(value string) bool {
	match := navigationTimestampPattern.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return false
	}
	// The codec rejects an offset whose hour exceeds 23 or minute exceeds 59;
	// Go's time.Parse accepts e.g. +24:00, so parity needs this explicit bound.
	if match[1] != "" {
		hour := int(match[1][0]-'0')*10 + int(match[1][1]-'0')
		minute := int(match[2][0]-'0')*10 + int(match[2][1]-'0')
		if hour > 23 || minute > 59 {
			return false
		}
	}
	return true
}

func (p navigationProjection) isLive(id, ref string) bool {
	return p.inputs.Live[id] || p.inputs.Live[ref]
}
func (p navigationProjection) renameable(id, ref string) bool {
	return p.inputs.Renameable[id] || p.inputs.Renameable[ref]
}
func (p navigationProjection) sessionFavorite(id, ref string) bool {
	return p.inputs.SessionFavorite[id] || p.inputs.SessionFavorite[ref]
}
func (p navigationProjection) pinSectionFor(id, ref string) string {
	if value := p.inputs.PinSectionBySession[id]; value != "" {
		if p.pinSectionIDs[value] {
			return value
		}
	}
	if value := p.inputs.PinSectionBySession[ref]; value != "" {
		if p.pinSectionIDs[value] {
			return value
		}
	}
	if assignment, ok := p.inputs.PinAssignments[id]; ok {
		if p.pinSectionIDs[assignment.SectionID] {
			return assignment.SectionID
		}
	}
	if assignment, ok := p.inputs.PinAssignments[ref]; ok {
		if p.pinSectionIDs[assignment.SectionID] {
			return assignment.SectionID
		}
	}
	return ""
}

func cloneNavigationSummary(summary hubapi.NavigationSessionSummary) hubapi.NavigationSessionSummary {
	clone := summary
	if summary.UpdatedAt != nil {
		updated := *summary.UpdatedAt
		clone.UpdatedAt = &updated
	}
	clone.RunningJobs = append(hubapi.NavigationArray[hubapi.NavigationJobSummary](nil), summary.RunningJobs...)
	clone.CompletedJobs = append(hubapi.NavigationArray[hubapi.NavigationJobSummary](nil), summary.CompletedJobs...)
	clone.Watches = append(hubapi.NavigationArray[hubapi.NavigationWatchSummary](nil), summary.Watches...)
	for index, watch := range summary.Watches {
		clone.Watches[index].Cadence = append([]hubapi.NavigationWatchCadence(nil), watch.Cadence...)
		clone.Watches[index].Events = append([]string(nil), watch.Events...)
		clone.Watches[index].DeliveryTimes = append([]string(nil), watch.DeliveryTimes...)
	}
	clone.Children = make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], len(summary.Children))
	for index, child := range summary.Children {
		clone.Children[index] = cloneNavigationSummary(child)
	}
	return clone
}

func navigationPage[T any](rows []T, offset uint32, limit, maximum int) ([]T, int) {
	limit = navigationLimit(limit, maximum)
	start, end := navigationRange(len(rows), offset, limit)
	return rows[start:end], len(rows) - end
}
func navigationLimit(limit, maximum int) int {
	if limit < 1 {
		return maximum
	}
	return min(limit, maximum)
}
func navigationRange(length int, offset uint32, limit int) (int, int) {
	if uint64(offset) >= uint64(length) {
		return length, length
	}
	start := int(offset)
	return start, min(start+limit, length)
}

func navigationJSONWithin(value any, maxBytes int) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > maxBytes {
		return fmt.Errorf("exceeds %d bytes", maxBytes)
	}
	return nil
}
func countTreeNodes(rows []hubcore.TreeNode) int {
	count := 0
	for _, row := range rows {
		count++
		count += countTreeNodes(row.Children)
	}
	return count
}
func truncateNavigationRunes(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
func truncateNavigationBytes(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	if limit <= len("…") {
		return ""
	}
	cut := value[:limit-len("…")]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}
