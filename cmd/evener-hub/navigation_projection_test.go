package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
		live[i] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d", i), Title: strings.Repeat("界", maxNavigationTitleRunes+1), Kind: "session", State: "idle"}
	}
	for i := range projects {
		projects[i] = hubcore.TreeProject{Key: fmt.Sprintf("project-%03d", i), Name: strings.Repeat("界", maxNavigationLabelRunes+1)}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: live, Projects: projects}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	if got, want := len(section.Sessions), maxNavigationSectionRows; got != want || section.Remaining != 1 {
		t.Fatalf("section rows=%d remaining=%d, want %d and 1", got, section.Remaining, want)
	}
	if got := len([]rune(section.Sessions[0].Title)); got != maxNavigationTitleRunes {
		t.Fatalf("title runes=%d, want %d", got, maxNavigationTitleRunes)
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(catalog.Projects), maxNavigationCatalogRows; got != want || catalog.Remaining != 1 {
		t.Fatalf("catalog rows=%d remaining=%d, want %d and 1", got, catalog.Remaining, want)
	}
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
	max := 0
	var visit func([]hubapi.NavigationSessionSummary, int)
	visit = func(nodes []hubapi.NavigationSessionSummary, depth int) {
		for _, node := range nodes {
			if depth > max {
				max = depth
			}
			visit(node.Children, depth+1)
		}
	}
	visit(rows, 1)
	return max
}
