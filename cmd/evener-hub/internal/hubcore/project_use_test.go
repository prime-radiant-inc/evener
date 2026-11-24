package hubcore

import (
	"slices"
	"testing"
	"time"
)

// ProjectsToUnarchive is the launch rule: a top-level session created after
// its project's archive decision means the user is using the project again,
// so the project decision that would file that session as archived is
// cleared. Everything else keeps its decision.
func TestProjectsToUnarchiveNeedsASessionCreatedAfterTheDecision(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	archived := ArchiveDecision{Archived: true, DecidedAt: decided}
	decisions := map[ArchiveKey]ArchiveDecision{
		ProjectDecisionKey("", "p-archived"):           archived,
		ProjectDecisionKey("host-a", "p-remote"):       archived,
		ProjectDecisionKey("", "p-unarchived"):         {Archived: false, DecidedAt: decided},
		ProjectDecisionKey("host-b", "p-other-source"): archived,
	}
	for _, tc := range []struct {
		name    string
		session SessionProject
		want    []ArchiveKey
	}{
		{
			name:    "launched after the archive",
			session: SessionProject{ProjectID: "p-archived", CreatedAt: decided.Add(time.Second)},
			want:    []ArchiveKey{{Kind: "project", ID: "p-archived"}},
		},
		{
			name:    "launched before the archive",
			session: SessionProject{ProjectID: "p-archived", CreatedAt: decided.Add(-time.Second)},
		},
		{
			name:    "launched at the decision instant",
			session: SessionProject{ProjectID: "p-archived", CreatedAt: decided},
		},
		{
			name:    "launched later in the decision's own second",
			session: SessionProject{ProjectID: "p-archived", CreatedAt: decided.Add(900 * time.Millisecond)},
		},
		{
			name:    "subagent of a session launched after the archive",
			session: SessionProject{ProjectID: "p-archived", CreatedAt: decided.Add(time.Hour), Subagent: true},
		},
		{
			name:    "another project",
			session: SessionProject{ProjectID: "p-missing", CreatedAt: decided.Add(time.Hour)},
		},
		{
			name:    "a project its user already unarchived",
			session: SessionProject{ProjectID: "p-unarchived", CreatedAt: decided.Add(time.Hour)},
		},
		{
			name:    "another source's project",
			session: SessionProject{ProjectID: "p-remote", Source: "host-b", CreatedAt: decided.Add(time.Hour)},
		},
		{
			name:    "the owning source's project",
			session: SessionProject{ProjectID: "p-remote", Source: "host-a", CreatedAt: decided.Add(time.Hour)},
			want:    []ArchiveKey{{Kind: "project", ID: "p-remote", Source: "host-a"}},
		},
		{
			name:    "no project identity",
			session: SessionProject{CreatedAt: decided.Add(time.Hour)},
		},
		{
			name:    "no creation time",
			session: SessionProject{ProjectID: "p-archived"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectsToUnarchive(decisions, []SessionProject{tc.session})
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ProjectsToUnarchive = %v, want %v", got, tc.want)
			}
		})
	}
}

// Two sessions launched into the same archived project clear its decision
// once, and the result is deterministic regardless of the session order.
func TestProjectsToUnarchiveDeduplicatesAndSorts(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	archived := ArchiveDecision{Archived: true, DecidedAt: decided}
	decisions := map[ArchiveKey]ArchiveDecision{
		ProjectDecisionKey("", "b"):       archived,
		ProjectDecisionKey("", "a"):       archived,
		ProjectDecisionKey("host-a", "a"): archived,
	}
	sessions := []SessionProject{
		{ProjectID: "b", CreatedAt: decided.Add(time.Hour)},
		{ProjectID: "a", CreatedAt: decided.Add(time.Hour)},
		{ProjectID: "a", Source: "host-a", CreatedAt: decided.Add(time.Hour)},
		{ProjectID: "a", CreatedAt: decided.Add(2 * time.Hour)},
	}
	got := ProjectsToUnarchive(decisions, sessions)
	want := []ArchiveKey{
		{Kind: "project", ID: "a"},
		{Kind: "project", ID: "a", Source: "host-a"},
		{Kind: "project", ID: "b"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ProjectsToUnarchive = %v, want %v", got, want)
	}
}
