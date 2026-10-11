package hubcore

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// Sessions with identical timestamps order by their displayed (truncated)
// title, case-folded, then by raw title, then by ID. Titles that differ only
// past the truncation limit are equal for ordering, so the ID decides.
func TestBuildTreeTitleTiebreakOrder(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("a", maxTitleRunes+50)
	metas := []schema.SessionMeta{
		{ID: "id-1", Name: "Zed"},
		{ID: "id-2", Name: "apple"},
		{ID: "id-3", Name: "abc"},
		{ID: "id-4", Name: "ABC"},
		{ID: "id-5", Name: long + "Y"},
		{ID: "id-6", Name: long + "X"},
		{ID: "id-7", Name: "  padded  "},
	}
	for i := range metas {
		metas[i].CreatedAt = now
		metas[i].UpdatedAt = now
		metas[i].EnvInfo.WorkingDir = "/workspace"
	}
	projects := map[string]identifier.Project{"/workspace": {ID: "project", CanonicalPath: "/workspace"}}

	tree := BuildTreeAtWithProjects(metas, nil, nil, now, projects)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects = %#v, want one", tree.Projects)
	}
	var got []string
	for _, n := range allSessions(tree.Projects[0]) {
		got = append(got, n.ID)
	}
	// Truncated long titles sort first ("aaa..." < "abc"); their tie breaks by ID.
	want := []string{"id-5", "id-6", "id-4", "id-3", "id-2", "id-7", "id-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// BenchmarkBuildTreeLongTitles builds a tree shaped like a real hub: many roots
// with long prompt-derived titles, each carrying many subagents.
func BenchmarkBuildTreeLongTitles(b *testing.B) {
	const roots, subagentsPerRoot = 150, 26
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	prompt := strings.Repeat("investigate the flaky navigation build and report ", 20)
	var metas []schema.SessionMeta
	for r := range roots {
		rootID := fmt.Sprintf("root-%04d", r)
		meta := func(id, parent string, sub bool, i int) schema.SessionMeta {
			m := schema.SessionMeta{
				ID: id, ParentSessionID: parent, IsSubagent: sub,
				OriginalPrompt: fmt.Sprintf("%s %d", prompt, i),
				CreatedAt:      now.Add(-time.Duration(i) * time.Minute),
				UpdatedAt:      now.Add(-time.Duration(i%7) * time.Minute),
			}
			m.EnvInfo.WorkingDir = fmt.Sprintf("/workspace/project-%d", r%20)
			return m
		}
		metas = append(metas, meta(rootID, "", false, r))
		for s := range subagentsPerRoot {
			m := meta(fmt.Sprintf("%s-sub-%03d", rootID, s), rootID, true, r+s)
			m.JobTreeRootSessionID = rootID
			metas = append(metas, m)
		}
	}
	projects := map[string]identifier.Project{}
	for p := range 20 {
		path := fmt.Sprintf("/workspace/project-%d", p)
		projects[path] = identifier.Project{ID: fmt.Sprintf("project-%d", p), CanonicalPath: path}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		input := slices.Clone(metas)
		BuildTreeAtWithProjects(input, nil, nil, now, projects)
	}
}
