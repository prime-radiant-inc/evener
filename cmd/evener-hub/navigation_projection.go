package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"math"
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

	maxNavigationTitleRunes      = 200
	maxNavigationLabelRunes      = 512
	maxNavigationIdentityBytes   = 1_024
	maxNavigationWorkingDirBytes = 4_096
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

// navigationCatalogOrder is the order a key held by several catalogs resolves
// in: a read of the key names the first catalog holding it. Only an
// unresolved directory's "no-project" key can be in more than one. Each call
// returns a fresh slice, so no caller can change the order for another.
func navigationCatalogOrder() []navigationResourceKind {
	return []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns}
}

// navigationResourceKey describes one immutable navigation representation. It
// contains decoded, validated values only; HTTP parsing belongs to its handler.
type navigationResourceKey struct {
	Kind       navigationResourceKind
	ID         string
	SectionID  string
	ProjectKey string
	// Catalog narrows a project or project page read to one catalog's
	// project; empty reads the first catalog holding the key.
	Catalog    navigationResourceKind
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
	Live            map[string]bool
	Renameable      map[string]bool
	SessionFavorite map[string]bool
	ProjectFavorite map[string]bool
	// PinSections and PinAssignments are used when callers retain the durable
	// pin snapshot. PinAssignments is keyed by the (source, session id) pair a
	// pin is stored under, so one source's pin never decorates another source's
	// row that shares its bare ID.
	PinSections    []hubcore.PinSection
	PinAssignments map[hubcore.ArchiveKey]hubcore.SessionPin
	// SessionSeen is the hub's seen-through markers and their epoch, captured
	// with Tree. A live row's unseen flag is computed against it (S4).
	SessionSeen hubcore.SessionSeenSnapshot
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
	// alias, when set on a per-request copy, is the location served for a ref
	// that is not an indexed row. See NavigationService.aliasProjectionLocked.
	alias *hubapi.NavigationSessionLocation
	// offlineSources is the set of manifest source IDs whose connection state
	// is down, indexed once per projection so every row can answer "is my
	// source unreachable?" from the same capture the manifest serves.
	offlineSources map[string]bool
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
	p := navigationProjection{inputs: cloned, pinSectionIDs: make(map[string]bool), projects: make(map[string]hubcore.TreeProject), catalogs: make(map[navigationResourceKind][]hubcore.TreeProject), locations: make(map[string]hubapi.NavigationSessionLocation), offlineSources: offlineSourceIDs(cloned.Sources)}
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
	// A same-key project of a later catalog is shadowed: its sessions are
	// still located under the key, but a read of the key does not return them.
	for _, kind := range navigationCatalogOrder() {
		for _, project := range p.catalogs[kind] {
			if err := ctx.Err(); err != nil {
				return navigationProjection{}, err
			}
			if _, claimed := p.projects[project.Key]; !claimed {
				p.projects[project.Key] = project
			}
		}
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
	out.PinSections = append([]hubcore.PinSection(nil), in.PinSections...)
	out.PinAssignments = make(map[hubcore.ArchiveKey]hubcore.SessionPin, len(in.PinAssignments))
	for key, assignment := range in.PinAssignments {
		if err := ctx.Err(); err != nil {
			return navigationBuildInputs{}, err
		}
		out.PinAssignments[key] = assignment
	}
	out.SessionSeen = in.SessionSeen.Clone()
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
	out.PinSections = append([]hubcore.PinSection(nil), in.PinSections...)
	out.PinAssignments = make(map[hubcore.ArchiveKey]hubcore.SessionPin, len(in.PinAssignments))
	maps.Copy(out.PinAssignments, in.PinAssignments)
	out.SessionSeen = in.SessionSeen.Clone()
	return out
}

func cloneNavigationLiveEntries(in []hubcore.LiveEntry) []hubcore.LiveEntry {
	if in == nil {
		return nil
	}
	out := make([]hubcore.LiveEntry, len(in))
	for i, entry := range in {
		out[i] = hubcore.CloneLiveEntry(entry)
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
// catalog slices and manifest counts, of the p.projects map (all built in
// buildNavigationProjectionContext), and of the location index that
// indexLocationsContext walks to mint a hubapi.NavigationSessionLocation per
// session. Dropping a duplicate group therefore does not just trim
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
	// Merging a Key's groups can raise the merged row's LastActivity above a
	// later row's instant, while the merge keeps each Key's first-appearance
	// position. The test-runs bucket is the live shape: it concatenates the
	// active and archived test-run projects, each LastActivity-ordered on its
	// own, so an archived test run newer than an active one lifts the merged row
	// past rows it should precede. Re-sort each bucket by LastActivity desc -
	// hubcore's own project order - so a merged row's position agrees with its
	// recency. SliceStable keeps the tree's order as the tiebreak, the way
	// hubcore's stable project sort does.
	navigationSortProjectsByLastActivity(active)
	navigationSortProjectsByLastActivity(archived)
	navigationSortProjectsByLastActivity(testRuns)
	return navigationProjectBucket{active: active, archived: archived, testRuns: testRuns}, nil
}

// navigationSortProjectsByLastActivity orders projects newest-first by
// LastActivity, matching hubcore's project order (BuildTree's byLastActivityDesc).
// SliceStable keeps the input order for equal instants, the way hubcore's
// stable sort does.
func navigationSortProjectsByLastActivity(projects []hubcore.TreeProject) {
	sort.SliceStable(projects, func(i, j int) bool {
		return projects[i].LastActivity.After(projects[j].LastActivity)
	})
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
// the IsArchived / IsTestRun flags - so a merged row keeps one stable label and
// address. IsTestRun is uniform inside a bucket because navigationProjectBuckets
// routes every test-run project to its own bucket. IsArchived is uniform in the
// active and archived buckets but NOT in the test-runs bucket, which takes both
// archived and unarchived test runs: a merged test-run row keeps the first
// group's IsArchived, the same first-group-owns-identity rule that fixes Name
// and Key. Everything else is combined the way the projection's consumers read
// the struct:
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
		// The overflow is derived from the merged tiers, not per group: More*
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
// capTier rely on (hubcore.TreeNodeLess). Sorting the concatenated union
// once - rather than re-sorting the accumulated rows after each group folds
// in - keeps the tier linear in its rows, and matches what folding pair by
// pair produces, because the sort is stable and the groups concatenate in the
// tree's own order. The rows are returned whole - the caller keeps every one
// in the public tier, which is what TierRows falls back to - so the caller
// derives the tier's overflow with navigationTierOverflow.
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
	sort.SliceStable(rows, func(i, j int) bool { return hubcore.TreeNodeLess(rows[i], rows[j]) })
	return rows, nil
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
	// TierRows and retain the same session eligibility here.
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
			appendNode(node)
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
		rows = append(rows, hubapi.NavigationPinSectionDescriptor{ID: section.id, Name: truncateNavigationRunes(section.name, maxNavigationLabelRunes), Count: section.memberCount})
	}
	rows = rows[:navigationCatalogRowsThatFit(rows, func(kept int) any {
		return hubapi.NavigationPinSectionCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, PinSections: hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor]{}, Remaining: len(p.pinSections) - start - kept}
	})]
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
		rows = append(rows, p.projectSummary(project))
	}
	rows = rows[:navigationCatalogRowsThatFit(rows, func(kept int) any {
		return hubapi.NavigationProjectCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Projects: hubapi.NavigationArray[hubapi.NavigationProjectSummary]{}, Remaining: len(projects) - start - kept}
	})]
	remaining := len(projects) - start - len(rows)
	return hubapi.NavigationProjectCatalog{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Projects: rows, Remaining: remaining}, nil
}

// navigationCatalogRowsThatFit returns how many leading rows a catalog page
// keeps: the page takes rows in order until the next one would carry it past
// maxNavigationCatalogBytes. emptyPage returns the page with no rows and the
// remaining count for kept rows.
//
// A page's encoding is its empty page with the rows spliced into the empty
// array: encoding/json encodes each slice element on its own and joins them
// with commas, and the NavigationArray wrapper's own json.Marshal output passes
// through the outer encode byte for byte. So a page with kept rows is exactly
// len(empty page) + the rows' encoded lengths + kept-1 commas, and each row is
// encoded once rather than once per page it appears in.
func navigationCatalogRowsThatFit[T any](rows []T, emptyPage func(kept int) any) int {
	rowBytes := 0
	for index, row := range rows {
		kept := index + 1
		// A row or page that cannot be encoded fits no budget
		// (navigationEncodedSize is math.MaxInt). Each size is checked against
		// the room left before it is added, so rowBytes and pageBytes stay
		// within the budget and no sum below can wrap.
		size := navigationEncodedSize(row)
		if size > maxNavigationCatalogBytes-rowBytes {
			return index
		}
		rowBytes += size
		pageBytes := navigationEncodedSize(emptyPage(kept))
		if pageBytes > maxNavigationCatalogBytes || pageBytes+rowBytes+kept-1 > maxNavigationCatalogBytes {
			return index
		}
	}
	return len(rows)
}

func (p navigationProjection) Project(key string) (hubapi.NavigationProjectResource, bool) {
	return p.ProjectIn("", key)
}

// projectIn finds key's project in catalog, or, with no catalog, in the first
// catalog holding it (p.projects). A key can be in several catalogs, and a
// session's location names the one whose project holds it.
func (p navigationProjection) projectIn(catalog navigationResourceKind, key string) (hubcore.TreeProject, bool) {
	if catalog == "" {
		project, ok := p.projects[key]
		return project, ok
	}
	for _, project := range p.catalogs[catalog] {
		if project.Key == key {
			return project, true
		}
	}
	return hubcore.TreeProject{}, false
}

// ProjectIn is the project resource for key in catalog, or in the first
// catalog holding it when catalog is empty.
func (p navigationProjection) ProjectIn(catalog navigationResourceKind, key string) (hubapi.NavigationProjectResource, bool) {
	project, ok := p.projectIn(catalog, key)
	if !ok {
		return hubapi.NavigationProjectResource{}, false
	}
	projector := navigationProjector{projection: p}
	current, currentRemaining := projector.projectTier(project, "current", 0, maxNavigationSectionRows)
	recent, recentRemaining := projector.projectTier(project, "recent", 0, maxNavigationSectionRows)
	resource := hubapi.NavigationProjectResource{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: key, Current: hubapi.NavigationTier{Sessions: current, Remaining: currentRemaining}, Recent: hubapi.NavigationTier{Sessions: recent, Remaining: recentRemaining}, Archived: hubapi.NavigationTier{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}, Truncated: projector.truncated}
	fitNavigationProject(&resource)
	return resource, true
}

func (p navigationProjection) ProjectPage(key, tier string, offset uint32, limit int) (hubapi.NavigationProjectPage, error) {
	return p.ProjectPageIn("", key, tier, offset, limit)
}

// ProjectPageIn is one tier page of key's project in catalog, or in the
// first catalog holding it when catalog is empty.
func (p navigationProjection) ProjectPageIn(catalog navigationResourceKind, key, tier string, offset uint32, limit int) (hubapi.NavigationProjectPage, error) {
	project, ok := p.projectIn(catalog, key)
	if !ok {
		return hubapi.NavigationProjectPage{}, fmt.Errorf("navigation project %q not found", key)
	}
	if tier != "current" && tier != "recent" && tier != "archived" {
		return hubapi.NavigationProjectPage{}, fmt.Errorf("invalid navigation tier %q", tier)
	}
	// Archived rows are read through evener/archived/list; navigation carries
	// only their count (the summary's more_archived).
	if tier == "archived" {
		return hubapi.NavigationProjectPage{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: key, Tier: tier, Offset: offset, Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}, nil
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
// large, they retain the largest deterministic left-to-right node prefix that
// fits (navigationFittingBudget); that is equivalent to pruning rightmost
// branches first but needs a few full-envelope probes rather than one marshal
// per removed node.
func fitNavigationSection(resource *hubapi.NavigationSectionResource) {
	fullBytes := navigationEncodedSize(*resource)
	if fullBytes <= maxNavigationResponseBytes {
		return
	}
	original := cloneNavigationSummaries(resource.Sessions)
	baseRemaining := resource.Remaining
	budget := navigationFittingChoice(navigationSummaryNodes(original), fullBytes, func(budget int) int {
		rows, dropped := limitNavigationSummaries(original, budget)
		candidate := *resource
		candidate.Sessions = rows
		candidate.Remaining = baseRemaining + dropped
		candidate.Truncated = true
		return navigationEncodedSize(candidate)
	})
	resource.Sessions, _ = limitNavigationSummaries(original, budget)
	resource.Remaining = baseRemaining + len(original) - len(resource.Sessions)
	resource.Truncated = true
}

func fitNavigationProjectPage(resource *hubapi.NavigationProjectPage) {
	fullBytes := navigationEncodedSize(*resource)
	if fullBytes <= maxNavigationResponseBytes {
		return
	}
	original := cloneNavigationSummaries(resource.Sessions)
	baseRemaining := resource.Remaining
	budget := navigationFittingChoice(navigationSummaryNodes(original), fullBytes, func(budget int) int {
		rows, dropped := limitNavigationSummaries(original, budget)
		candidate := *resource
		candidate.Sessions = rows
		candidate.Remaining = baseRemaining + dropped
		candidate.Truncated = true
		return navigationEncodedSize(candidate)
	})
	resource.Sessions, _ = limitNavigationSummaries(original, budget)
	resource.Remaining = baseRemaining + len(original) - len(resource.Sessions)
	resource.Truncated = true
}

func fitNavigationProject(resource *hubapi.NavigationProjectResource) {
	fullBytes := navigationEncodedSize(*resource)
	if fullBytes <= maxNavigationResponseBytes {
		return
	}
	original := cloneNavigationProjectResource(*resource)
	nodes := navigationSummaryNodes(original.Current.Sessions) + navigationSummaryNodes(original.Recent.Sessions) + navigationSummaryNodes(original.Archived.Sessions)
	budget := navigationFittingChoice(nodes, fullBytes, func(budget int) int {
		candidate := limitNavigationProject(original, budget)
		return navigationEncodedSize(candidate)
	})
	limited := limitNavigationProject(original, budget)
	*resource = limited
}

// navigationFittingChoice retains the largest deterministic session prefix that fits.
func navigationFittingChoice(nodes, fullBytes int, size func(int) int) int {
	budget, _ := navigationFittingBudget(nodes, maxNavigationResponseBytes, fullBytes, func(budget int) (int, error) { return size(budget), nil })
	return budget
}

func navigationFittingBudget(nodes, maxBytes, fullBytes int, size func(budget int) (int, error)) (int, error) {
	if nodes <= 0 {
		return 0, nil
	}
	guess := nodes
	if fullBytes > 0 {
		guess = int(int64(nodes) * int64(maxBytes) / int64(fullBytes))
	}
	guess = min(max(guess, 1), nodes)
	fits := func(budget int) (bool, error) {
		bytes, err := size(budget)
		return err == nil && bytes <= maxBytes, err
	}
	// low always fits and high never does; nodes+1 stands for past the end.
	low, high := 0, nodes+1
	fit, err := fits(guess)
	if err != nil {
		return 0, err
	}
	if fit {
		low = guess
		for step := 1; low+step < high; step *= 2 {
			fit, err := fits(low + step)
			if err != nil {
				return 0, err
			}
			if !fit {
				high = low + step
				break
			}
			low += step
		}
	} else {
		high = guess
		for step := 1; high-step > low; step *= 2 {
			fit, err := fits(high - step)
			if err != nil {
				return 0, err
			}
			if fit {
				low = high - step
				break
			}
			high -= step
		}
	}
	for high-low > 1 {
		middle := low + (high-low)/2
		fit, err := fits(middle)
		if err != nil {
			return 0, err
		}
		if fit {
			low = middle
		} else {
			high = middle
		}
	}
	return low, nil
}

func navigationSummaryWeight(summary hubapi.NavigationSessionSummary) int {
	weight := 1 + summary.OmittedDescendants
	for _, child := range summary.Children {
		weight += navigationSummaryWeight(child)
	}
	return weight
}

// navigationSummarySubagentWeight is the subagent-kind share of a shed
// subtree's weight - the MoreSubagents counterpart of
// navigationSummaryWeight, already-accounted MoreSubagents included so
// sheds-of-sheds never double count.
func navigationSummarySubagentWeight(summary hubapi.NavigationSessionSummary) int {
	weight := summary.MoreSubagents
	if summary.Kind == "subagent" {
		weight++
	}
	for _, child := range summary.Children {
		weight += navigationSummarySubagentWeight(child)
	}
	return weight
}

// navigationEncodedSize is the length of value's JSON encoding. A value that
// cannot be encoded fits no budget.
func navigationEncodedSize(value any) int {
	encoded, err := navigationEnvelopeMarshal(value)
	if err != nil {
		return math.MaxInt
	}
	return len(encoded)
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
				limited.MoreSubagents += navigationSummarySubagentWeight(omitted)
			}
			return limited, true, false
		}
		limited.Children = append(limited.Children, candidate)
		if !complete {
			for _, omitted := range row.Children[index+1:] {
				limited.OmittedDescendants += navigationSummaryWeight(omitted)
				limited.MoreSubagents += navigationSummarySubagentWeight(omitted)
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

// navigationAliasLocation is the location of a ref that is not an indexed row,
// routed through its top-level row's indexed location. Nothing in it is read
// from anywhere but that location and the ref, so it is coherent with the core
// the root came from. Live state and the title are not carried: the pane gets
// them from thread/read.
func navigationAliasLocation(id, kind string, root hubapi.NavigationSessionLocation) (hubapi.NavigationSessionLocation, bool) {
	ref, err := navigationRef(id)
	if err != nil {
		return hubapi.NavigationSessionLocation{}, false
	}
	summary := hubapi.NavigationSessionSummary{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
		State:     "ended",
		Kind:      kind,
		Children:  hubapi.NavigationArray[hubapi.NavigationSessionSummary]{},
	}
	if root.Session != nil {
		summary.Project = root.Session.Project
		summary.Offline = root.Session.Offline
	}
	return hubapi.NavigationSessionLocation{
		GenerationID: root.GenerationID,
		Revision:     root.Revision,
		Ref:          ref.String(),
		TopLevelRef:  root.TopLevelRef,
		Catalog:      root.Catalog,
		ProjectKey:   root.ProjectKey,
		Tier:         root.Tier,
		Session:      &summary,
	}, true
}

func (p navigationProjection) Location(ref string) (hubapi.NavigationSessionLocation, bool) {
	if p.alias != nil && p.alias.Ref == ref {
		location := *p.alias
		summary := cloneNavigationSummary(*location.Session)
		location.Session = &summary
		return location, true
	}
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
		resource, ok = p.ProjectIn(key.Catalog, key.ProjectKey)
		if !ok {
			err = fmt.Errorf("navigation project %q not found", key.ProjectKey)
		}
	case navigationResourceProjectPage:
		resource, err = p.ProjectPageIn(key.Catalog, key.ProjectKey, key.Tier, key.Offset, int(key.Limit))
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
	return hubapi.NavigationProjectSummary{Key: project.Key, Name: truncateNavigationRunes(project.Name, maxNavigationLabelRunes), WorkingDir: truncateNavigationBytes(project.WorkingDir, maxNavigationWorkingDirBytes), RollupState: project.RollupState, RollupLive: project.RollupLive, RollupAttn: project.RollupAttn, DefaultExpanded: project.Expanded, MoreCurrent: project.MoreCurrent, MoreRecent: project.MoreRecent, MoreArchived: archivedCount(project), Worktrees: project.Worktrees, IsArchived: project.IsArchived, Favorite: projectFavoriteForSources(p.inputs.ProjectFavorite, project), Sources: navigationProjectSources(project.Sources), SessionCount: project.TotalSessionCount()}
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
	for _, node := range p.pinCandidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref, err := navigationNodeRef(node)
		if err != nil {
			continue
		}
		sectionID := p.pinSectionIDFor(ref)
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
	indexRows := func(rows []hubcore.TreeNode, catalog navigationResourceKind, projectKey, tier string) {
		for _, root := range rows {
			if ctx.Err() != nil {
				return
			}
			_ = p.indexLocationNodeContext(ctx, root, root, navigationLocationAt{catalog: catalog, projectKey: projectKey, tier: tier}, true)
		}
	}
	for _, kind := range navigationCatalogOrder() {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, project := range p.catalogs[kind] {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, tier := range []string{"current", "recent", "archived"} {
				rows, _ := project.TierRows(tier)
				indexRows(rows, kind, project.Key, tier)
			}
		}
	}
	indexRows(p.live, "", "", "live")
	if err := ctx.Err(); err != nil {
		return err
	}
	indexRows(p.needsYou, "", "", "needs_you")
	return ctx.Err()
}

// navigationLocationAt is where indexLocationsContext found a top-level row: its catalog
// and project key (both empty outside a project) and its tier.
type navigationLocationAt struct {
	catalog    navigationResourceKind
	projectKey string
	tier       string
}

func (p navigationProjection) indexLocationNodeContext(ctx context.Context, node, root hubcore.TreeNode, at navigationLocationAt, topLevel bool) error {
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
		// Deep links carry only the selected session's compact summary.
		summary := navigationProjector{projection: p}.projectShallow(node)
		p.locations[ref.String()] = hubapi.NavigationSessionLocation{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Ref: ref.String(), TopLevelRef: rootRef.String(), Catalog: string(at.catalog), ProjectKey: at.projectKey, TopLevel: topLevel, Tier: at.tier, PinSectionID: p.pinSectionIDFor(ref), Session: &summary}
	}
	for _, child := range node.Children {
		if err := p.indexLocationNodeContext(ctx, child, root, at, false); err != nil {
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

// projectNode projects one flat list row with its own activity counts and root tally.
func (p *navigationProjector) projectNode(node hubcore.TreeNode, depth int) (hubapi.NavigationSessionSummary, bool) {
	p.depth = max(p.depth, depth)
	if p.nodes >= maxNavigationNodes || depth > maxNavigationDepth {
		p.truncated = true
		return hubapi.NavigationSessionSummary{}, false
	}
	summary := p.projectShallow(node)
	p.nodes++
	return summary, true
}

// navigationModelName is the name a row shows for a session's model (S17):
// the name model/list gives a model the registry leaves unnamed
// (withDisplayNames), so a row and the model picker agree. An unknown model
// names nothing.
func navigationModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return truncateNavigationRunes(prettifyModelDisplayName(model), maxNavigationLabelRunes)
}

func (p navigationProjector) projectShallow(node hubcore.TreeNode) hubapi.NavigationSessionSummary {
	ref, _ := navigationNodeRef(node)
	pinned := p.projection.pinSectionIDFor(ref) != ""
	armedWatchCount := 0
	for _, watch := range node.Watches {
		if watch.Active {
			armedWatchCount++
		}
	}
	runningCommand := ""
	for _, job := range node.RunningJobs {
		if job.Command != "" {
			runningCommand = truncateNavigationRunes(job.Command, maxNavigationLabelRunes)
			break
		}
	}
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
		Project:           truncateNavigationBytes(node.Project, maxNavigationIdentityBytes),
		State:             node.State,
		Kind:              node.Kind,
		Branch:            truncateNavigationRunes(node.Branch, maxNavigationLabelRunes),
		Favorite:          !pinned && p.projection.sessionFavorite(node.ID, ref.String()),
		Rename:            p.projection.renameable(node.ID, ref.String()),
		Live:              p.projection.isLive(node.ID, ref.String()) && hubcore.NormalizeState(node.State) != "ended",
		AskPending:        node.AskPending,
		ApprovalPending:   node.ApprovalPending,
		ApprovalTool:      truncateNavigationBytes(node.ApprovalTool, maxNavigationIdentityBytes),
		ApprovalTarget:    truncateNavigationRunes(node.ApprovalTarget, maxNavigationLabelRunes),
		Question:          navigationQuestion(node.Question),
		Failure:           navigationFailure(node.Failure),
		LastMessage:       appwire.Excerpt(node.LastMessage, appwire.MaxMessageExcerptRunes),
		ModelName:         navigationModelName(node.Model),
		Dormant:           node.Dormant,
		Offline:           p.projection.sourceOffline(ref.HostID),
		UpdatedAt:         optionalTime(node.UpdatedAt),
		Subagents:         navigationSubagentTally(node.Subagents),
		TurnEndedAt:       optionalTime(node.TurnEndedAt),
		Unseen:            p.projection.unseen(ref, node.TurnEndedAt),
		RunningJobCount:   len(node.RunningJobs),
		RunningJobCommand: runningCommand,
		WatchCount:        len(node.Watches),
		ArmedWatchCount:   armedWatchCount,
		Tasks:             navigationTaskProgress(node.Tasks),
		Children:          hubapi.NavigationArray[hubapi.NavigationSessionSummary]{},
	}
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// navigationTaskProgress is the row's task line: a session's task-list progress
// when its list has at least one task, else nil. The current task's
// description is cut to the label bound. Progress the schema would refuse (a
// negative count, or more tasks done and cancelled than exist) is dropped:
// one invalid summary would make every row in the resource unreadable.
func navigationTaskProgress(tasks *appwire.TaskAggregate) *hubapi.NavigationTaskProgress {
	if tasks == nil || tasks.Total == 0 {
		return nil
	}
	progress := &hubapi.NavigationTaskProgress{Total: tasks.Total, Done: tasks.Done, Cancelled: tasks.Cancelled}
	if current := tasks.Current; current != nil {
		progress.CurrentID = current.ID
		progress.Current = truncateNavigationRunes(current.Description, maxNavigationLabelRunes)
	}
	if !navigationTaskProgressValid(*progress) {
		return nil
	}
	return progress
}

// navigationQuestion is a row's pending question on the wire: re-cut to the
// wire's bounds (appwire.BoundedPendingQuestion), so a remote host or an older
// daemon cannot widen a row, and dropped when the schema would refuse it (no
// text left, or no question counted), the way navigationTaskProgress drops bad
// progress rather than fail the resource.
func navigationQuestion(question *appwire.PendingQuestion) *hubapi.NavigationQuestion {
	if question == nil {
		return nil
	}
	bounded := appwire.BoundedPendingQuestion(question.Question, question.Options, question.Count)
	wire := hubapi.NavigationQuestion{Text: bounded.Question, Options: bounded.Options, Count: bounded.Count}
	if !navigationQuestionValid(wire) {
		return nil
	}
	return &wire
}

// navigationFailure is a Failed row's why on the wire (S1c): the failure's
// headline re-cut to the wire's bound, and its cause's kind, provider and HTTP
// status, identities cut to the identity bound. It is dropped when the schema
// would refuse it (nothing left to say), the way navigationTaskProgress drops
// bad progress rather than fail the resource.
func navigationFailure(failure *appwire.ThreadFailure) *hubapi.NavigationFailure {
	if failure == nil {
		return nil
	}
	wire := hubapi.NavigationFailure{Title: appwire.Excerpt(failure.Title, appwire.MaxFailureTitleRunes)}
	if cause := failure.Cause; cause != nil {
		wire.CauseKind = truncateNavigationBytes(cause.Kind, maxNavigationIdentityBytes)
		wire.Provider = truncateNavigationBytes(cause.Provider, maxNavigationIdentityBytes)
		wire.Status = cause.Status
	}
	if !navigationFailureValid(wire) {
		return nil
	}
	return &wire
}

// navigationSubagentTally is a root row's tally on the wire: absent when the
// tree has no subagent, and dropped when the schema would refuse it (a
// negative count, which only a malformed daemon answer can carry), the way
// navigationTaskProgress drops bad progress rather than fail the resource.
func navigationSubagentTally(tally appwire.SubagentTally) *hubapi.NavigationSubagentTally {
	wire := hubapi.NavigationSubagentTally{Running: tally.Running, Failed: tally.Failed, Done: tally.Done}
	if wire == (hubapi.NavigationSubagentTally{}) || !navigationSubagentTallyValid(wire) {
		return nil
	}
	return &wire
}

// offlineSourceIDs indexes the manifest sources whose connection state is
// down, keyed by source ID so a row can ask "is my source unreachable?" from
// the same enumeration the manifest serves.
//
// A source absent from the manifest is not offline, matching sourceOnline's
// fail-open answer for an unregistered source.
func offlineSourceIDs(sources []hubapi.Source) map[string]bool {
	var offline map[string]bool
	for _, source := range sources {
		if source.ID == "" || source.Online {
			continue
		}
		if offline == nil {
			offline = make(map[string]bool)
		}
		offline[source.ID] = true
	}
	return offline
}

// sourceOffline reports whether the row's owning source is unreachable. It is
// a property of the source, not the row: a quiet row is as offline as an
// active one when its host is down.
//
// "Owning source" is the row's canonical source identity — the ref host that
// HostID carries to the frontend — so the marker and the row's host label
// always name the same source. A row whose advertised ref names a different
// source than the one that listed it is a conflicting row (the ingestion
// marks it incomplete); it still marks by the host it names.
func (p navigationProjection) sourceOffline(hostID string) bool {
	return p.offlineSources[hostID]
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

// pinSectionIDFor is the one pin lookup every consumer shares: the section a
// row's own (source, session id) pair is assigned to, or "" when no durable
// section holds it. Callers pass the row's canonical ref, which already
// carries the source, so no lookup can fall back to a bare ID.
func (p navigationProjection) pinSectionIDFor(ref hubapi.Ref) string {
	assignment, ok := p.inputs.PinAssignments[hubcore.SessionPinKey(ref.HostID, ref.SessionID)]
	if !ok || !p.pinSectionIDs[assignment.SectionID] {
		return ""
	}
	return assignment.SectionID
}

// unseen reports whether a row's last turn ended after the hub's seen-through
// marker for it (S4). The marker is keyed by the row's own ref, the one a
// client marks it by, so a live session's Live, project and pin rows, which
// share that ref, agree.
func (p navigationProjection) unseen(ref hubapi.Ref, turnEndedAt time.Time) bool {
	return p.inputs.SessionSeen.Unseen(hubcore.SessionPinKey(ref.HostID, ref.SessionID), turnEndedAt)
}

func cloneNavigationSummary(summary hubapi.NavigationSessionSummary) hubapi.NavigationSessionSummary {
	clone := summary
	clone.UpdatedAt = clonePointer(summary.UpdatedAt)
	clone.TurnEndedAt = clonePointer(summary.TurnEndedAt)
	clone.Tasks = clonePointer(summary.Tasks)
	clone.Subagents = clonePointer(summary.Subagents)
	clone.Failure = clonePointer(summary.Failure)
	if summary.Question != nil {
		question := *summary.Question
		question.Options = append([]string(nil), summary.Question.Options...)
		clone.Question = &question
	}
	clone.Children = make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], len(summary.Children))
	for index, child := range summary.Children {
		clone.Children[index] = cloneNavigationSummary(child)
	}
	return clone
}

// clonePointer returns a pointer to a shallow copy of *value; nil stays nil.
// It is for the summaries' pointers to values (a time, the task progress, the
// subagent tally, the failure), so the copy shares nothing that can change.
func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
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
