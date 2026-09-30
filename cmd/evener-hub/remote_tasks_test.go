package hub

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/fuzz/reflectfill"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's task progress, pending question and
// failure summary, and an in-process subagent alias carries only the fields
// it owns. Every field of live starts non-zero (reflectfill.Fill), so
// comparing the WHOLE alias entry against an explicit "want" of only the
// fields the alias is meant to carry means a field added to the root later (a
// last message) needs no field-specific "only on the root" test of its own,
// and no update to this fixture: any root-only field the alias literal in
// localDaemonEntriesFromRoster accidentally starts copying makes the
// comparison fail the moment it stops being nil/zero on one side only (S13b
// and S1b; fixes #2589).
func TestLocalDaemonEntriesFromRosterAliasCarriesOnlyItsOwnFields(t *testing.T) {
	var live hubcore.LiveEntry
	reflectfill.Fill(t, reflect.ValueOf(&live).Elem(), "hubcore.LiveEntry")
	// Fields that drive the alias's construction, rather than being carried
	// or withheld verbatim, need specific, mutually consistent values instead
	// of the filler's arbitrary ones.
	rootEntry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "sess_root", SessionID: "sess_root"}
	live.Entry = rootEntry
	live.SessionID = "sess_root"
	live.Crashed = false // a crashed entry is skipped entirely; must not be filled true
	live.RunningSubagentIDs = []string{"sess_child"}
	live.RunningSubagentStates = map[string]string{"sess_child": "working"}
	childWatches := []appwire.EvenerWatchInfo{{ID: "watch_child"}}
	live.ChildWatches = map[string][]appwire.EvenerWatchInfo{"sess_child": childWatches}

	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{live})
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the root and its one alias", entries)
	}
	if !reflect.DeepEqual(entries[0].Tasks, live.Tasks) {
		t.Fatalf("root entry tasks = %+v, want %+v", entries[0].Tasks, live.Tasks)
	}
	if !reflect.DeepEqual(entries[0].PendingQuestion, live.PendingQuestion) {
		t.Fatalf("root entry question = %+v, want %+v", entries[0].PendingQuestion, live.PendingQuestion)
	}
	if !reflect.DeepEqual(entries[0].Failure, live.Failure) {
		t.Fatalf("root entry failure = %+v, want %+v", entries[0].Failure, live.Failure)
	}
	if entries[0].LastMessage != live.LastMessage {
		t.Fatalf("root entry last message = %q, want %q", entries[0].LastMessage, live.LastMessage)
	}
	want := appsource.LocalDaemonEntry{
		Entry:             rootEntry,
		SessionID:         "sess_child",
		OwnerSessionID:    "sess_root",
		Status:            "working",
		Watches:           childWatches,
		Capabilities:      live.Capabilities,
		CapabilitiesKnown: true,
		ReadOnlyAlias:     true,
	}
	if !reflect.DeepEqual(entries[1], want) {
		t.Fatalf("alias entry = %+v, want only its own fields %+v", entries[1], want)
	}
}

// A controller reads a remote host's task progress off its list row, so the
// remote live row carries its task line like a local one (S13b).
func TestNavigationRemoteLiveRowCarriesItsTaskProgress(t *testing.T) {
	projection := remoteNavigationProjection(t, []appwire.Thread{{
		ID: "planned", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Tasks: &appwire.TaskAggregate{Total: 7, Done: 3, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"}}},
	}})
	want := &hubapi.NavigationTaskProgress{Total: 7, Done: 3, CurrentID: 4, Current: "Fix the settle/drain race"}
	if row := navigationProjectedSummary(t, projection, "host-a:planned"); !reflect.DeepEqual(row.Tasks, want) {
		t.Fatalf("remote row tasks = %+v, want %+v", row.Tasks, want)
	}
}
