package hub

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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
)

// navigationResourceBounds documents the common recursive resource limits.
type navigationResourceBounds struct {
	TopLevelRows int
	Children     int
	Nodes        int
	Depth        int
	Bytes        int
}

var navigationSessionResourceBounds = navigationResourceBounds{
	TopLevelRows: maxNavigationSectionRows,
	Children:     maxNavigationChildren,
	Nodes:        maxNavigationNodes,
	Depth:        maxNavigationDepth,
	Bytes:        maxNavigationResponseBytes,
}

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
	inputs      navigationBuildInputs
	manifest    hubapi.NavigationManifest
	live        []hubcore.TreeNode
	needsYou    []hubcore.TreeNode
	pinSections []navigationPinSection
	projects    map[string]hubcore.TreeProject
	catalogs    map[navigationResourceKind][]hubcore.TreeProject
	locations   map[string]hubapi.NavigationSessionLocation
}

type navigationPinSection struct {
	id   string
	name string
	rows []hubcore.TreeNode
}

// buildNavigationProjection has no ambient dependencies. The supplied tree is
// already ordered and tiered by hubcore; this function only adds wire shaping,
// pin/favorite decorations, bounds, and indexes.
func buildNavigationProjection(inputs navigationBuildInputs) (navigationProjection, error) {
	if err := validateNavigationInputs(inputs); err != nil {
		return navigationProjection{}, err
	}
	p := navigationProjection{
		inputs:    cloneNavigationInputs(inputs),
		live:      append([]hubcore.TreeNode(nil), inputs.Tree.Live...),
		needsYou:  append([]hubcore.TreeNode(nil), inputs.Tree.NeedsYou...),
		projects:  make(map[string]hubcore.TreeProject),
		catalogs:  make(map[navigationResourceKind][]hubcore.TreeProject),
		locations: make(map[string]hubapi.NavigationSessionLocation),
	}

	buckets := navigationProjectBuckets(inputs.Tree)
	p.catalogs[navigationResourceProjects] = append([]hubcore.TreeProject(nil), buckets.active...)
	p.catalogs[navigationResourceArchivedProjects] = append([]hubcore.TreeProject(nil), buckets.archived...)
	p.catalogs[navigationResourceTestRuns] = append([]hubcore.TreeProject(nil), buckets.testRuns...)
	for _, project := range buckets.all() {
		p.projects[project.Key] = project
	}
	p.pinSections = p.buildPinSections()
	p.manifest = hubapi.NavigationManifest{
		GenerationID:     inputs.GenerationID,
		Revision:         inputs.Revision,
		Sources:          navigationSources(inputs.Sources),
		AttentionSummary: inputs.AttentionSummary,
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
	p.indexLocations()
	return p, nil
}

func cloneNavigationInputs(in navigationBuildInputs) navigationBuildInputs {
	out := in
	out.Sources = append([]hubapi.Source(nil), in.Sources...)
	out.Live = cloneNavigationBoolMap(in.Live)
	out.Renameable = cloneNavigationBoolMap(in.Renameable)
	out.SessionFavorite = cloneNavigationBoolMap(in.SessionFavorite)
	out.ProjectFavorite = cloneNavigationBoolMap(in.ProjectFavorite)
	out.PinSectionBySession = cloneNavigationStringMap(in.PinSectionBySession)
	out.PinSections = append([]hubcore.PinSection(nil), in.PinSections...)
	if in.PinAssignments != nil {
		out.PinAssignments = make(map[string]hubcore.SessionPin, len(in.PinAssignments))
		for id, assignment := range in.PinAssignments {
			out.PinAssignments[id] = assignment
		}
	}
	return out
}

func cloneNavigationBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cloneNavigationStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func validateNavigationInputs(inputs navigationBuildInputs) error {
	if err := validateNavigationIdentity("generation", inputs.GenerationID, false); err != nil {
		return err
	}
	if len(inputs.Sources) > 64 {
		return fmt.Errorf("navigation has %d sources, maximum is 64", len(inputs.Sources))
	}
	for _, source := range inputs.Sources {
		if err := validateNavigationIdentity("source ID", source.ID, false); err != nil {
			return err
		}
		if err := validateNavigationString("source label", source.Label, maxNavigationLabelRunes); err != nil {
			return err
		}
	}
	for _, section := range inputs.PinSections {
		if err := validateNavigationIdentity("pin section ID", section.ID, false); err != nil {
			return err
		}
		if err := validateNavigationString("pin section name", section.Name, maxNavigationLabelRunes); err != nil {
			return err
		}
	}
	for _, project := range append(append([]hubcore.TreeProject(nil), inputs.Tree.Projects...), inputs.Tree.ArchivedProjects...) {
		if err := validateNavigationIdentity("project key", project.Key, false); err != nil {
			return err
		}
		if err := validateNavigationString("project name", project.Name, maxNavigationLabelRunes); err != nil {
			return err
		}
		if err := validateNavigationIdentity("working directory", project.WorkingDir, true); err != nil {
			return err
		}
		for _, tier := range []string{"current", "recent", "archived"} {
			rows, ok := project.TierRows(tier)
			if !ok {
				return fmt.Errorf("project %q has invalid %s tier", project.Key, tier)
			}
			if err := validateNavigationNodes(rows); err != nil {
				return err
			}
		}
	}
	if err := validateNavigationNodes(inputs.Tree.Live); err != nil {
		return err
	}
	return validateNavigationNodes(inputs.Tree.NeedsYou)
}

func navigationSources(sources []hubapi.Source) hubapi.NavigationArray[hubapi.Source] {
	out := make(hubapi.NavigationArray[hubapi.Source], 0, len(sources))
	for _, source := range sources {
		source.Label = truncateNavigationRunes(source.Label, maxNavigationLabelRunes)
		source.Kind = truncateNavigationRunes(source.Kind, maxNavigationLabelRunes)
		out = append(out, source)
	}
	return out
}

func validateNavigationNodes(rows []hubcore.TreeNode) error {
	for _, node := range rows {
		if _, err := navigationRef(node.ID); err != nil {
			return err
		}
		if err := validateNavigationString("title", node.Title, maxNavigationTitleRunes); err != nil {
			return err
		}
		if err := validateNavigationString("project label", node.Project, maxNavigationLabelRunes); err != nil {
			return err
		}
		if err := validateNavigationString("branch", node.Branch, maxNavigationLabelRunes); err != nil {
			return err
		}
		if err := validateNavigationNodes(node.Children); err != nil {
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
	limit := maxNavigationIdentityBytes
	if kind == "working directory" {
		limit = maxNavigationWorkingDirBytes
	}
	if len(value) > limit {
		return fmt.Errorf("navigation %s exceeds %d bytes", kind, limit)
	}
	return nil
}
func validateNavigationString(kind, value string, limit int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("navigation %s is not valid UTF-8", kind)
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

func (p navigationProjection) Manifest() hubapi.NavigationManifest { return p.manifest }

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
	return hubapi.NavigationSectionResource{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Sessions: sessions, Remaining: remaining, Truncated: projector.truncated}
}

func (p navigationProjection) PinCatalogPage(offset uint32, limit int) hubapi.NavigationPinSectionCatalog {
	limit = navigationLimit(limit, maxNavigationCatalogRows)
	start, end := navigationRange(len(p.pinSections), offset, limit)
	rows := make(hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor], 0, end-start)
	for _, section := range p.pinSections[start:end] {
		candidate := hubapi.NavigationPinSectionDescriptor{ID: section.id, Name: truncateNavigationRunes(section.name, maxNavigationLabelRunes), Count: len(section.rows)}
		if !navigationAppendWithin(&rows, candidate, maxNavigationCatalogBytes-4*1024) {
			break
		}
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
		if !navigationAppendWithin(&rows, candidate, maxNavigationCatalogBytes-4*1024) {
			break
		}
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
	return hubapi.NavigationProjectResource{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: key, Current: hubapi.NavigationTier{Sessions: current, Remaining: currentRemaining}, Recent: hubapi.NavigationTier{Sessions: recent, Remaining: recentRemaining}, Archived: hubapi.NavigationTier{Sessions: archived, Remaining: archivedRemaining}, Truncated: projector.truncated}, true
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
	return hubapi.NavigationProjectPage{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: key, Tier: tier, Offset: offset, Sessions: sessions, Remaining: remaining, Truncated: projector.truncated}, nil
}

func (p navigationProjection) Location(ref string) (hubapi.NavigationSessionLocation, bool) {
	location, ok := p.locations[ref]
	return location, ok
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
	encoded, err := json.Marshal(resource)
	if err != nil {
		return nil, navigationFingerprint{}, fmt.Errorf("encode navigation resource: %w", err)
	}
	maxBytes := maxNavigationResponseBytes
	switch key.Kind {
	case navigationResourceManifest:
		maxBytes = maxNavigationManifestBytes
	case navigationResourcePinCatalog, navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		maxBytes = maxNavigationCatalogBytes
	}
	if len(encoded) > maxBytes {
		return nil, navigationFingerprint{}, fmt.Errorf("navigation resource exceeds %d bytes", maxBytes)
	}
	return resource, sha256.Sum256(encoded), nil
}

func (p navigationProjection) projectSummary(project hubcore.TreeProject) hubapi.NavigationProjectSummary {
	return hubapi.NavigationProjectSummary{Key: project.Key, Name: truncateNavigationRunes(project.Name, maxNavigationLabelRunes), WorkingDir: truncateNavigationBytes(project.WorkingDir, maxNavigationWorkingDirBytes), RollupState: project.RollupState, RollupLive: project.RollupLive, RollupAttn: project.RollupAttn, DefaultExpanded: project.Expanded, MoreCurrent: project.MoreCurrent, MoreRecent: project.MoreRecent, MoreArchived: project.MoreArchived, Worktrees: project.Worktrees, IsArchived: project.IsArchived, Favorite: p.inputs.ProjectFavorite[project.Key], SessionCount: project.TotalSessionCount()}
}

func (p navigationProjection) buildPinSections() []navigationPinSection {
	byID := make(map[string]navigationPinSection, len(p.inputs.PinSections))
	for _, section := range p.inputs.PinSections {
		byID[section.ID] = navigationPinSection{id: section.ID, name: section.Name}
	}
	assignment := cloneNavigationStringMap(p.inputs.PinSectionBySession)
	for sessionID, pin := range p.inputs.PinAssignments {
		if assignment[sessionID] == "" {
			assignment[sessionID] = pin.SectionID
		}
	}
	for _, node := range p.inputs.Tree.PinCandidates() {
		ref, err := navigationRef(node.ID)
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
		if len(section.rows) != 0 {
			out = append(out, section)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].name), strings.ToLower(out[j].name)
		if left == right {
			return out[i].id < out[j].id
		}
		return left < right
	})
	return out
}

func (p navigationProjection) indexLocations() {
	indexRows := func(rows []hubcore.TreeNode, projectKey, tier string) {
		for _, root := range rows {
			p.indexLocationNode(root, root, projectKey, tier, true)
		}
	}
	for _, kind := range []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns} {
		for _, project := range p.catalogs[kind] {
			for _, tier := range []string{"current", "recent", "archived"} {
				rows, _ := project.TierRows(tier)
				indexRows(rows, project.Key, tier)
			}
		}
	}
	indexRows(p.live, "", "live")
	indexRows(p.needsYou, "", "needs_you")
}

func (p navigationProjection) indexLocationNode(node, root hubcore.TreeNode, projectKey, tier string, topLevel bool) {
	ref, err := navigationRef(node.ID)
	if err != nil {
		return
	}
	rootRef, err := navigationRef(root.ID)
	if err != nil {
		return
	}
	if _, exists := p.locations[ref.String()]; !exists {
		summary := navigationProjector{projection: p}.projectShallow(node)
		p.locations[ref.String()] = hubapi.NavigationSessionLocation{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Ref: ref.String(), TopLevelRef: rootRef.String(), ProjectKey: projectKey, TopLevel: topLevel, Tier: tier, PinSectionID: p.pinSectionFor(node.ID, ref.String()), Session: &summary}
	}
	for _, child := range node.Children {
		p.indexLocationNode(child, root, projectKey, tier, false)
	}
}

// navigationTraversal is shared by every recursive row projection in a single
// resource. A project root deliberately uses one traversal across all tiers.
type navigationTraversal struct {
	nodes     int
	bytes     int
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
	encoded, err := json.Marshal(summary)
	// Keep room for resource metadata and JSON separators. The per-node sum is
	// deliberately conservative (parents and children are both counted), so a
	// successful traversal is safely below the actual resource byte ceiling.
	if err != nil || p.bytes+len(encoded) > maxNavigationResponseBytes-4*1024 {
		p.truncated = true
		return hubapi.NavigationSessionSummary{}, false
	}
	p.nodes++
	p.bytes += len(encoded)
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
	ref, _ := navigationRef(node.ID)
	updated := node.UpdatedAt
	var updatedAt *time.Time
	if !updated.IsZero() {
		updatedAt = &updated
	}
	return hubapi.NavigationSessionSummary{Ref: ref.String(), HostID: ref.HostID, SessionID: ref.SessionID, Title: truncateNavigationRunes(node.Title, maxNavigationTitleRunes), Project: truncateNavigationRunes(node.Project, maxNavigationLabelRunes), State: node.State, Kind: node.Kind, Branch: truncateNavigationRunes(node.Branch, maxNavigationLabelRunes), ClusterCount: node.ClusterCount, Favorite: p.projection.sessionFavorite(node.ID, ref.String()), Rename: p.projection.renameable(node.ID, ref.String()), Live: p.projection.isLive(node.ID, ref.String()), AskPending: node.AskPending, Dormant: node.Dormant, UpdatedAt: updatedAt, MoreSubagents: node.MoreSubagents, Children: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}
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
		return value
	}
	if value := p.inputs.PinSectionBySession[ref]; value != "" {
		return value
	}
	if assignment, ok := p.inputs.PinAssignments[id]; ok {
		return assignment.SectionID
	}
	return ""
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
func navigationAppendWithin[T any](rows *hubapi.NavigationArray[T], candidate T, maxBytes int) bool {
	test := append(append(hubapi.NavigationArray[T](nil), (*rows)...), candidate)
	encoded, err := json.Marshal(test)
	if err != nil || len(encoded) > maxBytes {
		return false
	}
	*rows = append(*rows, candidate)
	return true
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
	if len([]rune(value)) <= limit {
		return value
	}
	if limit <= 0 {
		return ""
	}
	return string([]rune(value)[:limit-1]) + "…"
}
func truncateNavigationBytes(value string, limit int) string {
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
