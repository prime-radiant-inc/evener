package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// SessionArchived is the rail's rule (S14 reports it as a search result's
// archived flag): an explicit decision wins over age; with none, a session two
// weeks without activity is archived; and a project its source archived
// archives its sessions whatever their own decision.
func TestSessionArchivedFollowsTheRail(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour)
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: "archived"}:              true,
		{Kind: "session", ID: "unarchived"}:            false,
		{Kind: "project", ID: "p-archived"}:            true,
		{Kind: "project", ID: "p-remote", Source: "h"}: true,
	}
	for _, tc := range []struct {
		name, id, project, source string
		lastActivity              time.Time
		want                      bool
	}{
		{"recent", "s", "p", "", now, false},
		{"idle two weeks", "s", "p", "", old, true},
		{"explicitly archived", "archived", "p", "", now, true},
		{"explicitly unarchived though idle", "unarchived", "p", "", old, false},
		{"in an archived project", "unarchived", "p-archived", "", now, true},
		{"another source's project decision", "s", "p-remote", "", now, false},
		{"its own source's project decision", "s", "p-remote", "h", now, true},
	} {
		if got := SessionArchived(decisions, tc.id, tc.project, tc.source, tc.lastActivity, now); got != tc.want {
			t.Errorf("%s: SessionArchived = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// LiveSessionArchived asks the rail's question of a live entry, consulting the
// project decision of the source that owns it: a remote host's archive of its
// project archives its own live session, never this hub's session in a
// project of the same ID.
func TestLiveSessionArchivedConsultsTheOwningSource(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	decisions := map[ArchiveKey]bool{{Kind: "project", ID: "p", Source: "h"}: true}
	project := identifier.Project{ID: "p"}
	for _, tc := range []struct {
		name  string
		entry LiveEntry
		want  bool
	}{
		{"remote entry", LiveEntry{SourceID: "h", SessionID: "h:s", Project: project}, true},
		{"remote identity", LiveEntry{SessionID: "h:s", Project: project}, true},
		{"this hub's entry", LiveEntry{SourceID: "local", SessionID: "s", Project: project}, false},
	} {
		if got := LiveSessionArchived(decisions, tc.entry, now, now); got != tc.want {
			t.Errorf("%s: LiveSessionArchived = %t, want %t", tc.name, got, tc.want)
		}
	}
}
