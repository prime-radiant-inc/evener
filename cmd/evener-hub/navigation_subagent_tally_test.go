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

// A root's row carries its whole tree's tally though subagents have no rows:
// the tally is the daemon's count, not the rows' (S3, Review Focus 5). A root
// with no subagents carries no key.
func TestNavigationRowsCarryTheWholeTreesSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01QUIET", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	var childIDs []string
	for i := range 60 {
		childID := fmt.Sprintf("01CHILD%02d", i)
		childIDs = append(childIDs, childID)
		metas = append(metas, schema.SessionMeta{
			ID: childID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Duration(i) * time.Second),
			ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"},
		})
	}
	tally := appwire.SubagentTally{Running: 2, Failed: 1, Done: 57}
	live := []hubcore.LiveEntry{
		{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive, RunningSubagentIDs: childIDs, Subagents: tally},
		{PID: 2, SessionID: "01QUIET", Status: appwire.ThreadStatusIdle},
	}
	rows := liveNavigationRows(t, hubcore.BuildTreeAt(metas, live, nil, now).Live)
	root := rows["01ROOT"]
	if len(root.Children) != 0 {
		t.Fatalf("root row carries %d children, want none: subagents have no rows", len(root.Children))
	}
	if want := (hubapi.NavigationSubagentTally{Running: 2, Failed: 1, Done: 57}); root.Subagents == nil || *root.Subagents != want {
		t.Fatalf("root tally = %+v, want %+v", root.Subagents, want)
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
	if root, listed := liveNavigationRows(t, hubcore.BuildTreeAt(metas, live, nil, now).Live)["01ROOT"]; !listed || root.Subagents != nil {
		t.Fatalf("root row = %+v (listed %v), want it listed with no tally", root, listed)
	}
}

// The hub schema refuses a tally the codec would refuse.
func TestNavigationSchemaRefusesANegativeSubagentCount(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.Subagents = &hubapi.NavigationSubagentTally{Running: 1}
	if !navigationSessionValueValid(session) {
		t.Fatal("a valid tally was refused")
	}
	session.Subagents = &hubapi.NavigationSubagentTally{Failed: -1}
	if navigationSessionValueValid(session) {
		t.Fatal("a negative subagent count was accepted")
	}
	session.Subagents = &hubapi.NavigationSubagentTally{Done: int(maxNavigationSafeInteger)}
	if !navigationSessionValueValid(session) {
		t.Fatal("a count at the largest safe integer was refused")
	}
	session.Subagents = &hubapi.NavigationSubagentTally{Done: int(maxNavigationSafeInteger) + 1}
	if navigationSessionValueValid(session) {
		t.Fatal("a count past the largest safe integer was accepted")
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
