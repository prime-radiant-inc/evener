package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's task progress, and an in-process
// subagent alias carries only the fields it owns. Comparing the WHOLE alias
// entry -- rather than asserting each root-only field is cleared one at a
// time -- means a field added to the root later (a pending question, a
// failure summary, a last message) needs no field-specific "only on the
// root" test of its own: it is covered the moment it is missing from the
// alias literal in localDaemonEntriesFromRoster (S13b; fixes #2589).
func TestLocalDaemonEntriesFromRosterAliasCarriesOnlyItsOwnFields(t *testing.T) {
	tasks := &appwire.TaskAggregate{Total: 2, Done: 1, Remaining: 1}
	card := appwire.SandboxEscalationRequested{ThreadID: "sess_root", Ref: "local:sess_root", EscalationID: "esc_1", Tool: "write_file", Kind: "file", DeniedPath: "/srv/docs/a.md"}
	caps := appwire.ThreadCapabilities{Send: true}
	rootEntry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "sess_root", SessionID: "sess_root"}
	live := hubcore.LiveEntry{
		Entry:                 rootEntry,
		SessionID:             "sess_root",
		Status:                appwire.ThreadStatusActive,
		PendingAsk:            true,
		PendingEscalation:     true,
		PendingEscalations:    []appwire.SandboxEscalationRequested{card},
		RunningJobs:           []appwire.EvenerJobInfo{{JobID: "job_1"}},
		CompletedJobs:         []appwire.EvenerJobInfo{{JobID: "job_0"}},
		Watches:               []appwire.EvenerWatchInfo{{ID: "watch_root"}},
		Capabilities:          caps,
		CapabilitiesKnown:     true,
		Subagents:             appwire.SubagentTally{Running: 1},
		LastTurnEndedAt:       time.UnixMilli(1_700_000_000_000),
		Tasks:                 tasks,
		RunningSubagentIDs:    []string{"sess_child"},
		RunningSubagentStates: map[string]string{"sess_child": "working"},
		ChildWatches:          map[string][]appwire.EvenerWatchInfo{"sess_child": {{ID: "watch_child"}}},
	}

	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{live})
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the root and its one alias", entries)
	}
	if !reflect.DeepEqual(entries[0].Tasks, tasks) {
		t.Fatalf("root entry tasks = %+v, want %+v", entries[0].Tasks, tasks)
	}
	want := appsource.LocalDaemonEntry{
		Entry:             rootEntry,
		SessionID:         "sess_child",
		OwnerSessionID:    "sess_root",
		Status:            "working",
		Watches:           []appwire.EvenerWatchInfo{{ID: "watch_child"}},
		Capabilities:      caps,
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
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "planned", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Tasks: &appwire.TaskAggregate{Total: 7, Done: 3, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"}}},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	want := &hubapi.NavigationTaskProgress{Total: 7, Done: 3, CurrentID: 4, Current: "Fix the settle/drain race"}
	if row := navigationProjectedSummary(t, projection, "host-a:planned"); !reflect.DeepEqual(row.Tasks, want) {
		t.Fatalf("remote row tasks = %+v, want %+v", row.Tasks, want)
	}
}
