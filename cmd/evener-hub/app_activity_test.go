package hub

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

var activityReadNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

var activityReadMinutes = []int{0, 0, 3, 1, 0, 0, 0}

func silentFor(d time.Duration) *appwire.ThreadActivity {
	return &appwire.ThreadActivity{Minutes: append([]int(nil), activityReadMinutes...), LastActivityAt: activityReadNow.Add(-d).UnixMilli()}
}

func liveActivityEntry(pid int, id, status string, activity *appwire.ThreadActivity) hubcore.LiveEntry {
	return hubcore.LiveEntry{Entry: rendezvous.Entry{PID: pid, SessionID: id}, SessionID: id, Status: status, Activity: activity}
}

func quietMillis(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

// Jesse's ruling: an agent waiting on subagents is never stuck. A subagent
// inside one long model call emits nothing for minutes, so the read withholds
// the quiet time outright while one runs, and while the session is not
// working at all. A session with nothing running reports how long its tree
// has been silent.
func TestActivityReadWithholdsQuietWhileASubagentRuns(t *testing.T) {
	waiting := liveActivityEntry(2, "01WAITING", appwire.ThreadStatusActive, silentFor(15*time.Minute))
	waiting.RunningSubagentIDs = []string{"child-running", "child-settled", "child-unknown"}
	waiting.RunningSubagentStates = map[string]string{"child-running": appwire.ThreadStatusActive, "child-settled": appwire.ThreadStatusIdle}
	crashed := liveActivityEntry(5, "01CRASHED", "errored", silentFor(time.Minute))
	crashed.Crashed = true
	roster := hubcore.NewRosterWithEntries(
		liveActivityEntry(1, "01ALONE", appwire.ThreadStatusActive, silentFor(15*time.Minute)),
		waiting,
		liveActivityEntry(3, "01FINISHED", appwire.ThreadStatusAwaiting, silentFor(15*time.Minute)),
		liveActivityEntry(4, "01OLDDAEMON", appwire.ThreadStatusActive, nil),
		crashed,
	)
	got, err := hubActivityRead(t.Context(), hubcore.WebConfig{Roster: roster}, nil, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	want := []appwire.SessionActivity{
		{Ref: "local:01ALONE", Minutes: activityReadMinutes, QuietForMS: quietMillis(15 * time.Minute)},
		{Ref: "local:01FINISHED", Minutes: activityReadMinutes},
		{Ref: "local:01WAITING", Minutes: activityReadMinutes, RunningSubagents: 1},
	}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", got.Sessions, want)
	}
}

// A refs filter reads only the sessions it names, spelled as their Live rows
// spell them; a malformed or oversized filter is refused before anything is read.
func TestActivityReadFiltersToTheRefsAsked(t *testing.T) {
	cleared := liveActivityEntry(1, "01CURRENT", appwire.ThreadStatusActive, silentFor(time.Minute))
	cleared.SourceID, cleared.WorkspaceRef = "local", "local:01WORKSPACE"
	roster := hubcore.NewRosterWithEntries(cleared, liveActivityEntry(2, "01OTHER", appwire.ThreadStatusActive, silentFor(time.Minute)))
	cfg := hubcore.WebConfig{Roster: roster}

	got, err := hubActivityRead(t.Context(), cfg, nil, appwire.ActivityReadParams{Refs: []string{"local:01WORKSPACE"}}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Ref != "local:01WORKSPACE" {
		t.Fatalf("sessions = %+v, want only the session whose Live row is local:01WORKSPACE", got.Sessions)
	}
	if _, err := hubActivityRead(t.Context(), cfg, nil, appwire.ActivityReadParams{Refs: []string{"not a ref"}}, activityReadNow); err == nil || !strings.Contains(err.Error(), "refs must be session refs") {
		t.Fatalf("malformed ref error = %v, want an invalid-params refusal", err)
	}
	many := make([]string, maxActivityReadRefs+1)
	for i := range many {
		many[i] = "local:01OTHER"
	}
	if _, err := hubActivityRead(t.Context(), cfg, nil, appwire.ActivityReadParams{Refs: many}, activityReadNow); err == nil {
		t.Fatal("an oversized refs filter was accepted")
	}
}

// No roster means no local sessions, and the response still carries an empty
// list, never null.
func TestActivityReadWithoutARosterIsAnEmptyList(t *testing.T) {
	got, err := hubActivityRead(t.Context(), hubcore.WebConfig{}, nil, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil || got.Sessions == nil || len(got.Sessions) != 0 {
		t.Fatalf("read = %+v (%v), want an empty, non-nil list", got, err)
	}
}
