package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func findNode(nodes []TreeNode, id string) (TreeNode, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
	}
	return TreeNode{}, false
}

// The three places a live session becomes a row (its project row, the flat
// Live-tier leaf, the Needs-you row) overlay the same live facts and then set
// their own identity, kind, title and times. This pins what each path sets so
// the shared live overlay cannot drop one of them.
func TestBuildTreeLiveOverlayPaths(t *testing.T) {
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	created := now.Add(-3 * time.Hour)
	updated := now.Add(-2 * time.Hour)
	started := now.Add(-time.Hour)
	metas := []schema.SessionMeta{{
		ID:             "01WITHMETA",
		OriginalPrompt: "prompt",
		CreatedAt:      created,
		UpdatedAt:      updated,
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/evener", GitBranch: "feature/x"},
	}}
	live := []LiveEntry{
		{SessionID: "01WITHMETA", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, CurrentModel: "model-a", LastMessage: "hello"},
		{SessionID: "01NOMETA", Status: appwire.ThreadStatusSystemError, CurrentModel: "model-b", StartedAt: started},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)

	t.Run("project row of a session with a meta", func(t *testing.T) {
		var node TreeNode
		var found bool
		for i := range tree.Projects {
			node, found = findNode(allSessions(tree.Projects[i]), "01WITHMETA")
			if found {
				break
			}
		}
		if !found {
			t.Fatal("session missing from its project")
		}
		if node.Kind != "session" || node.Title != nodeTitle(metas[0], "session") || node.Title == "" || node.Branch != "feature/x" || node.Project == "" {
			t.Fatalf("project row identity = %+v", node)
		}
		if !node.CreatedAt.Equal(created) || !node.UpdatedAt.Equal(updated) || node.Age == "" {
			t.Fatalf("project row times = created %v updated %v age %q", node.CreatedAt, node.UpdatedAt, node.Age)
		}
		if node.State != "awaiting" || !node.AskPending || node.Model != "model-a" || node.LastMessage != "hello" {
			t.Fatalf("project row live facts = %+v", node)
		}
	})

	t.Run("live leaf of a session with no meta", func(t *testing.T) {
		node, ok := findNode(tree.Live, "01NOMETA")
		if !ok {
			t.Fatal("meta-less session missing from the Live tier")
		}
		if node.Kind != "session" || node.Title != ShortID("01NOMETA") || node.Project != "" || node.Branch != "" {
			t.Fatalf("live leaf identity = %+v", node)
		}
		if !node.CreatedAt.Equal(started) || !node.UpdatedAt.Equal(started) || node.Age == "" {
			t.Fatalf("live leaf times = created %v updated %v age %q", node.CreatedAt, node.UpdatedAt, node.Age)
		}
		if node.State != "errored" || node.Model != "model-b" {
			t.Fatalf("live leaf live facts = %+v", node)
		}
	})

	t.Run("needs-you row of a session with no meta", func(t *testing.T) {
		node, ok := findNode(tree.NeedsYou, "01NOMETA")
		if !ok {
			t.Fatal("errored meta-less session missing from Needs you")
		}
		leaf, _ := findNode(tree.Live, "01NOMETA")
		if node.Kind != "session" || node.Title != leaf.Title || node.State != leaf.State || node.Model != leaf.Model {
			t.Fatalf("needs-you row = %+v, live leaf = %+v", node, leaf)
		}
		if !node.CreatedAt.Equal(started) || !node.UpdatedAt.Equal(started) || node.Age == "" {
			t.Fatalf("needs-you times = created %v updated %v age %q", node.CreatedAt, node.UpdatedAt, node.Age)
		}
	})

	t.Run("needs-you row of a session with a meta", func(t *testing.T) {
		node, ok := findNode(tree.NeedsYou, "01WITHMETA")
		if !ok {
			t.Fatal("awaiting session with a pending ask missing from Needs you")
		}
		if node.Kind != "session" || node.Title != nodeTitle(metas[0], "session") || node.Title == "" || node.Branch != "feature/x" || node.Project == "" {
			t.Fatalf("needs-you identity = %+v", node)
		}
		if !node.CreatedAt.Equal(created) || !node.UpdatedAt.Equal(updated) || node.Age == "" {
			t.Fatalf("needs-you times = created %v updated %v age %q", node.CreatedAt, node.UpdatedAt, node.Age)
		}
		if node.State != "awaiting" || !node.AskPending || node.Model != "model-a" || node.LastMessage != "hello" {
			t.Fatalf("needs-you live facts = %+v", node)
		}
	})
}
