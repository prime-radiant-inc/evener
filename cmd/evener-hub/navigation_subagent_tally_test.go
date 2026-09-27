package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A root's row carries its whole tree's tally even when the row cannot carry
// every child: 60 subagents are more than the row's children cap, and nested
// ones never appear as its children at all. The tally is the daemon's count,
// not the rows' (S3, Review Focus 5). A root with no subagents carries no key.
func TestNavigationRowsCarryTheWholeTreesSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01QUIET", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01CHILD%02d", i), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Duration(i) * time.Second),
			ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"},
		})
	}
	tally := appwire.SubagentTally{Running: 2, Failed: 1, Done: 57}
	live := []hubcore.LiveEntry{
		{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive, Subagents: tally},
		{PID: 2, SessionID: "01QUIET", Status: appwire.ThreadStatusIdle},
	}
	tree := hubcore.BuildTreeAt(metas, live, nil, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]hubapi.NavigationSessionSummary{}
	for _, row := range projection.LivePage(0, 0).Sessions {
		rows[row.SessionID] = row
	}
	root := rows["01ROOT"]
	if len(root.Children) >= 60 {
		t.Fatalf("root row carries %d children; the fixture must exceed the row's cap", len(root.Children))
	}
	if want := (hubapi.NavigationSubagentTally{Running: 2, Failed: 1, Done: 57}); root.Subagents == nil || *root.Subagents != want {
		t.Fatalf("root tally = %+v, want %+v", root.Subagents, want)
	}
	for _, child := range root.Children {
		if child.Subagents != nil {
			t.Fatalf("subagent row %s carries a tally %+v", child.SessionID, child.Subagents)
		}
	}
	if _, present := navigationSummaryJSONFields(t, rows["01QUIET"])["subagents"]; present {
		t.Fatal("a root with no subagents carries the subagents key")
	}
}

// A tally the schema would refuse, which only a malformed daemon answer can
// carry, is dropped from the row instead of failing the whole resource.
func TestNavigationRowsDropAMalformedSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []hubcore.LiveEntry{{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive, Subagents: appwire.SubagentTally{Running: 2, Failed: -1}}}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.BuildTreeAt(metas, live, nil, now)})
	if err != nil {
		t.Fatalf("a malformed tally failed the build: %v", err)
	}
	if rows := projection.LivePage(0, 0).Sessions; len(rows) != 1 || rows[0].Subagents != nil {
		t.Fatalf("rows = %+v, want the root with no tally", rows)
	}
}

// The hub schema refuses a tally the codec would refuse.
func TestNavigationSchemaRefusesANegativeSubagentCount(t *testing.T) {
	valid := hubapi.NavigationSessionSummary{Ref: "local:01A", HostID: "local", SessionID: "01A", State: "active", Kind: "session", Subagents: &hubapi.NavigationSubagentTally{Running: 1}}
	if !navigationSessionValueValid(valid) {
		t.Fatal("a valid tally was refused")
	}
	valid.Subagents = &hubapi.NavigationSubagentTally{Failed: -1}
	if navigationSessionValueValid(valid) {
		t.Fatal("a negative subagent count was accepted")
	}
}

// A cloned summary owns its tally: a row handed to one reader cannot change
// another's.
func TestCloneNavigationSummaryOwnsTheSubagentTally(t *testing.T) {
	original := hubapi.NavigationSessionSummary{Subagents: &hubapi.NavigationSubagentTally{Running: 1, Failed: 2, Done: 3}}
	clone := cloneNavigationSummary(original)
	original.Subagents.Failed = 9
	if clone.Subagents == nil || *clone.Subagents != (hubapi.NavigationSubagentTally{Running: 1, Failed: 2, Done: 3}) {
		t.Fatalf("clone tally = %+v, want its own copy", clone.Subagents)
	}
}
