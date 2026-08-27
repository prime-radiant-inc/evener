package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

func TestNavigationSectionAppliesRecursiveBounds(t *testing.T) {
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Revision:     7,
		Tree:         hubcore.Tree{Live: []hubcore.TreeNode{deepNavigationNode(40)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	if !section.Truncated {
		t.Fatal("deep section must report truncation")
	}
	if got := countNavigationNodes(section.Sessions); got > maxNavigationNodes {
		t.Fatalf("nodes=%d, max=%d", got, maxNavigationNodes)
	}
	if got := navigationDepth(section.Sessions); got > maxNavigationDepth {
		t.Fatalf("depth=%d, max=%d", got, maxNavigationDepth)
	}
}

func TestNavigationProjectPagePreservesOrderAndUint32Offset(t *testing.T) {
	rows := make([]hubcore.TreeNode, 51)
	for i := range rows {
		rows[i] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d", i), Title: fmt.Sprintf("row %03d", i), Kind: "session", State: "idle", UpdatedAt: time.Unix(int64(i), 0).UTC()}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation", Revision: 3,
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", Current: rows}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := projection.ProjectPage("project", "current", 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(page.Sessions), 1; got != want {
		t.Fatalf("rows=%d, want %d", got, want)
	}
	if got, want := page.Sessions[0].SessionID, "session-050"; got != want {
		t.Fatalf("session=%q, want %q", got, want)
	}
	empty, err := projection.ProjectPage("project", "current", ^uint32(0), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Sessions) != 0 || empty.Remaining != 0 {
		t.Fatalf("large offset returned %#v", empty)
	}
}

func TestNavigationManifestHasNoRowsAndLocationHasSummary(t *testing.T) {
	project := hubcore.TreeProject{Key: "project", Name: "project", Current: []hubcore.TreeNode{{ID: "session-parent", Title: "parent", Kind: "session", State: "idle", Children: []hubcore.TreeNode{{ID: "session-child", Title: "child", Kind: "subagent", State: "ended"}}}}}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Revision: 2, Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := projection.Manifest()
	if got := manifest.Catalogs.Projects.Count; got != 1 {
		t.Fatalf("project count=%d", got)
	}
	location, ok := projection.Location("local:session-child")
	if !ok || location.Session == nil || location.TopLevel || location.TopLevelRef != "local:session-parent" || location.ProjectKey != "project" {
		t.Fatalf("location=%#v, found=%v", location, ok)
	}
	if _, ok := any(manifest).(hubapi.NavigationSessionSummary); ok {
		t.Fatal("manifest must not contain navigation rows")
	}
}

func TestNavigationProjectionRejectsMalformedIdentity(t *testing.T) {
	_, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{{ID: "bad ref", Title: "bad", Kind: "session", State: "idle"}}}})
	if err == nil {
		t.Fatal("malformed identity accepted")
	}
}

func TestNavigationBoundsLimitRowsCatalogAndStrings(t *testing.T) {
	live := make([]hubcore.TreeNode, 51)
	projects := make([]hubcore.TreeProject, 101)
	for i := range live {
		live[i] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d", i), Title: strings.Repeat("a", maxNavigationTitleRunes), Kind: "session", State: "idle"}
	}
	for i := range projects {
		projects[i] = hubcore.TreeProject{Key: fmt.Sprintf("project-%03d", i), Name: strings.Repeat("b", maxNavigationLabelRunes)}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: live, Projects: projects}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	if got, want := len(section.Sessions), maxNavigationSectionRows; got != want || section.Remaining != 1 {
		t.Fatalf("section rows=%d remaining=%d, want %d and 1", got, section.Remaining, want)
	}
	if got := len(section.Sessions[0].Title); got != maxNavigationTitleRunes {
		t.Fatalf("title bytes=%d, want %d", got, maxNavigationTitleRunes)
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(catalog.Projects), maxNavigationCatalogRows; got != want || catalog.Remaining != 1 {
		t.Fatalf("catalog rows=%d remaining=%d, want %d and 1", got, catalog.Remaining, want)
	}
}

func TestNavigationProjectionPinsDecorationsAndOrder(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	rows := []hubcore.TreeNode{
		{ID: "session-z", Title: "z", Kind: "session", State: "idle", UpdatedAt: now},
		{ID: "session-a", Title: "a", Kind: "session", State: "idle", UpdatedAt: now},
		{ID: "session-ended", Title: "ended", Kind: "session", State: "ended", UpdatedAt: now},
		{ID: "session-dangling", Title: "dangling", Kind: "session", State: "idle", UpdatedAt: now},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree:         hubcore.Tree{Live: rows, Projects: []hubcore.TreeProject{{Key: "project", Name: "project", Current: rows}}},
		Live:         map[string]bool{"session-ended": true},
		SessionFavorite: map[string]bool{
			"session-a":        true,
			"session-dangling": true,
		},
		PinSections:    []hubcore.PinSection{{ID: "pin", Name: "Pinned"}},
		PinAssignments: map[string]hubcore.SessionPin{"session-a": {SectionID: "pin"}, "session-z": {SectionID: "pin"}, "session-dangling": {SectionID: "missing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pins, ok := projection.PinSectionPage("pin", 0, 50)
	if !ok || len(pins.Sessions) != 2 || pins.Sessions[0].Ref != "local:session-a" || pins.Sessions[1].Ref != "local:session-z" {
		t.Fatalf("pin order=%+v, found=%v", pins.Sessions, ok)
	}
	if pins.Sessions[0].Favorite {
		t.Fatal("named pin must clear legacy favorite")
	}
	live := projection.LivePage(0, 50)
	if live.Sessions[2].Live {
		t.Fatal("ended roster row must not be actionable live")
	}
	location, ok := projection.Location("local:session-dangling")
	if !ok || location.PinSectionID != "" || location.Session == nil || !location.Session.Favorite {
		t.Fatalf("dangling assignment leaked into location: %#v", location)
	}
}

func TestNavigationProjectionRetainsIndependentInputsAndReturnsCopies(t *testing.T) {
	tree := hubcore.Tree{Live: []hubcore.TreeNode{{ID: "session", Title: "before", Kind: "session", State: "idle", Children: []hubcore.TreeNode{{ID: "child", Title: "child", Kind: "subagent", State: "ended"}}}}}
	inputs := navigationBuildInputs{GenerationID: "generation", Sources: []hubapi.Source{{ID: "source", Label: "before", Kind: "remote"}}, Tree: tree}
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatal(err)
	}
	inputs.Sources[0].Label = "after"
	tree.Live[0].Title = "after"
	tree.Live[0].Children[0].Title = "after child"
	if got := projection.Manifest().Sources[0].Label; got != "before" {
		t.Fatalf("source aliased input: %q", got)
	}
	if got := projection.LivePage(0, 50).Sessions[0].Title; got != "before" {
		t.Fatalf("tree aliased input: %q", got)
	}
	location, ok := projection.Location("local:session")
	if !ok || location.Session == nil {
		t.Fatal("missing location")
	}
	location.Session.Title = "mutated"
	again, _ := projection.Location("local:session")
	if again.Session.Title != "before" {
		t.Fatalf("location return aliased cache: %q", again.Session.Title)
	}
}

func TestNavigationProjectionEnforcesExactEncodedCeilings(t *testing.T) {
	roots := make([]hubcore.TreeNode, 40)
	for root := range roots {
		children := make([]hubcore.TreeNode, 50)
		for child := range children {
			children[child] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d-%03d", root, child), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "subagent", State: "idle"}
		}
		roots[root] = hubcore.TreeNode{ID: fmt.Sprintf("session-root-%03d", root), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "session", State: "idle", Children: children}
	}
	projects := make([]hubcore.TreeProject, 100)
	for index := range projects {
		projects[index] = hubcore.TreeProject{Key: strings.Repeat(fmt.Sprintf("%03d", index), 342)[:maxNavigationIdentityBytes], Name: strings.Repeat("n", maxNavigationLabelRunes), WorkingDir: strings.Repeat("/", maxNavigationWorkingDirBytes)}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: roots, Projects: projects}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	encoded, _ := json.Marshal(section)
	if len(encoded) > maxNavigationResponseBytes || !section.Truncated || countNavigationNodes(section.Sessions) > maxNavigationNodes {
		t.Fatalf("section bytes=%d truncated=%v nodes=%d", len(encoded), section.Truncated, countNavigationNodes(section.Sessions))
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(catalog)
	if len(encoded) > maxNavigationCatalogBytes || catalog.Remaining == 0 {
		t.Fatalf("catalog bytes=%d remaining=%d", len(encoded), catalog.Remaining)
	}
	first, fingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	second, nextFingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil || fingerprint != nextFingerprint {
		t.Fatalf("fingerprints differ: %x %x (%v)", fingerprint, nextFingerprint, err)
	}
	if len(first.(hubapi.NavigationSectionResource).Sessions) != len(second.(hubapi.NavigationSectionResource).Sessions) {
		t.Fatal("resource output changed without input change")
	}
}

func TestNavigationProjectionValidatesIdentitiesAndTruncatesWorkingDir(t *testing.T) {
	badUTF8 := string([]byte{0xff})
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: badUTF8}); err == nil {
		t.Fatal("invalid UTF-8 generation accepted")
	}
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Sources: []hubapi.Source{{ID: "source", Label: "label", Kind: badUTF8}}}); err == nil {
		t.Fatal("invalid UTF-8 source kind accepted")
	}
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: strings.Repeat("g", maxNavigationIdentityBytes+1)}); err == nil {
		t.Fatal("overlength generation accepted")
	}
	// An over-limit working dir is now rejected by validateNavigationString.
	oversizedDir := strings.Repeat("界", maxNavigationWorkingDirBytes)
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", WorkingDir: oversizedDir}}}}); err == nil {
		t.Fatal("over-limit working directory accepted")
	}
	// A within-limit working dir passes validation and is safely bounded in the catalog.
	workingDir := strings.Repeat("/", maxNavigationWorkingDirBytes)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", WorkingDir: workingDir}}}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 1)
	if err != nil || len(catalog.Projects) != 1 || len(catalog.Projects[0].WorkingDir) > maxNavigationWorkingDirBytes {
		t.Fatalf("working dir was not safely truncated: %#v, %v", catalog, err)
	}
}

func TestNavigationProjectionCapsChildrenAndPreservesRowFields(t *testing.T) {
	children := make([]hubcore.TreeNode, maxNavigationChildren+1)
	for index := range children {
		children[index] = hubcore.TreeNode{ID: fmt.Sprintf("session-child-%03d", index), Title: "child", Kind: "subagent", State: "ended"}
	}
	updated := time.Unix(123, 0).UTC()
	root := hubcore.TreeNode{ID: "session-root", Title: "title", Project: "project", Branch: "branch", State: "awaiting", Kind: "session", ClusterCount: 2, AskPending: true, Dormant: true, UpdatedAt: updated, MoreSubagents: 3, Children: children}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{root}}, Live: map[string]bool{"session-root": true}, Renameable: map[string]bool{"session-root": true}, SessionFavorite: map[string]bool{"session-root": true}})
	if err != nil {
		t.Fatal(err)
	}
	row := projection.LivePage(0, 50).Sessions[0]
	if len(row.Children) != maxNavigationChildren || row.OmittedDescendants != 1 {
		t.Fatalf("children=%d omitted=%d", len(row.Children), row.OmittedDescendants)
	}
	if row.Ref != "local:session-root" || row.HostID != "local" || row.SessionID != "session-root" || row.Title != root.Title || row.Project != root.Project || row.State != root.State || row.Kind != root.Kind || row.Branch != root.Branch || row.ClusterCount != root.ClusterCount || !row.Favorite || !row.Rename || !row.Live || !row.AskPending || !row.Dormant || row.UpdatedAt == nil || !row.UpdatedAt.Equal(updated) || row.MoreSubagents != root.MoreSubagents {
		t.Fatalf("row fields diverged: %#v", row)
	}
}

func TestNavigationProjectionSnapshotsAuthoritativeTierRows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	metas := make([]schema.SessionMeta, 0, 60)
	for index := range 60 {
		metas = append(metas, schema.SessionMeta{ID: fmt.Sprintf("session-snapshot-%03d", index), CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/snapshot"}})
	}
	tree := hubcore.BuildTreeAt(metas, nil, map[hubcore.ArchiveKey]bool{}, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	key := tree.Projects[0].Key
	before, fingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: key, Tier: "current", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := tree.Projects[0].TierRows("current")
	rows[0].Title = "mutated authoritative source"
	after, nextFingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: key, Tier: "current", Limit: 50})
	if err != nil || fingerprint != nextFingerprint || before.(hubapi.NavigationProjectPage).Sessions[0].Title != after.(hubapi.NavigationProjectPage).Sessions[0].Title {
		t.Fatalf("authoritative mutation changed projection: %x %x %v", fingerprint, nextFingerprint, err)
	}
	location, ok := projection.Location(before.(hubapi.NavigationProjectPage).Sessions[0].Ref)
	if !ok || location.Session == nil || location.Session.Title == "mutated authoritative source" {
		t.Fatalf("authoritative mutation changed location: %#v", location)
	}
}

func TestNavigationProjectionFittingUsesLogarithmicEnvelopeProbes(t *testing.T) {
	roots := oversizeNavigationRoots()
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: roots}})
	if err != nil {
		t.Fatal(err)
	}
	originalMarshal := navigationEnvelopeMarshal
	probes := 0
	navigationEnvelopeMarshal = func(value any) ([]byte, error) {
		probes++
		return json.Marshal(value)
	}
	defer func() { navigationEnvelopeMarshal = originalMarshal }()
	section := projection.LivePage(0, 50)
	encoded, err := json.Marshal(section)
	if err != nil || len(encoded) > maxNavigationResponseBytes || !section.Truncated {
		t.Fatalf("invalid bounded section bytes=%d truncated=%v err=%v", len(encoded), section.Truncated, err)
	}
	if probes > 14 {
		t.Fatalf("full-envelope probes=%d, want logarithmic bound <=14", probes)
	}
}

func TestNavigationProjectionCutsTwoThousandNodesBeforeByteLimit(t *testing.T) {
	roots := make([]hubcore.TreeNode, 40)
	for root := range roots {
		children := make([]hubcore.TreeNode, 50)
		for child := range children {
			children[child] = hubcore.TreeNode{ID: fmt.Sprintf("session-node-%03d-%03d", root, child), Title: "small", Kind: "subagent", State: "idle"}
		}
		roots[root] = hubcore.TreeNode{ID: fmt.Sprintf("session-node-root-%03d", root), Title: "small", Kind: "session", State: "idle", Children: children}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: roots}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	encoded, _ := json.Marshal(section)
	if got := countNavigationNodes(section.Sessions); got != maxNavigationNodes || !section.Truncated || len(encoded) >= maxNavigationResponseBytes {
		t.Fatalf("nodes=%d truncated=%v bytes=%d", got, section.Truncated, len(encoded))
	}
}

func TestNavigationProjectionFingerprintSurvivesReturnedOutputMutation(t *testing.T) {
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{{ID: "session", Title: "before", Kind: "session", State: "idle"}}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, fingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	resource.(hubapi.NavigationSectionResource).Sessions[0].Title = "mutated returned output"
	_, nextFingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil || fingerprint != nextFingerprint {
		t.Fatalf("returned output changed fingerprint: %x %x %v", fingerprint, nextFingerprint, err)
	}
}

func oversizeNavigationRoots() []hubcore.TreeNode {
	roots := make([]hubcore.TreeNode, 40)
	for root := range roots {
		children := make([]hubcore.TreeNode, 50)
		for child := range children {
			children[child] = hubcore.TreeNode{ID: fmt.Sprintf("session-log-%03d-%03d", root, child), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "subagent", State: "idle"}
		}
		roots[root] = hubcore.TreeNode{ID: fmt.Sprintf("session-log-root-%03d", root), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "session", State: "idle", Children: children}
	}
	return roots
}

func deepNavigationNode(depth int) hubcore.TreeNode {
	node := hubcore.TreeNode{ID: fmt.Sprintf("session-%02d", depth), Title: "node", Kind: "subagent", State: "idle"}
	if depth > 1 {
		node.Children = []hubcore.TreeNode{deepNavigationNode(depth - 1)}
	}
	return node
}

func countNavigationNodes(rows []hubapi.NavigationSessionSummary) int {
	count := 0
	var visit func([]hubapi.NavigationSessionSummary)
	visit = func(nodes []hubapi.NavigationSessionSummary) {
		for _, node := range nodes {
			count++
			visit(node.Children)
		}
	}
	visit(rows)
	return count
}

func navigationDepth(rows []hubapi.NavigationSessionSummary) int {
	maxDepth := 0
	var visit func([]hubapi.NavigationSessionSummary, int)
	visit = func(nodes []hubapi.NavigationSessionSummary, depth int) {
		for _, node := range nodes {
			if depth > maxDepth {
				maxDepth = depth
			}
			visit(node.Children, depth+1)
		}
	}
	visit(rows, 1)
	return maxDepth
}
