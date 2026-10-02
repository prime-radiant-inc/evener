package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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

// activityHost scripts a remote hub (newScriptedRemoteHub) that answers every
// evener/activity/read with reply: a response, or an appwire.WireError it
// sends back as an error.
func activityHost(reply any) func(string, json.RawMessage) any {
	return scriptedRemoteHubReplying(appwire.MethodEvenerActivityRead, reply)
}

// activityHostReads decodes the evener/activity/read requests a scripted host
// received, in order.
func activityHostReads(t *testing.T, calls []remoteHubCall) []appwire.ActivityReadParams {
	t.Helper()
	return scriptedRemoteHubParams[appwire.ActivityReadParams](t, calls, appwire.MethodEvenerActivityRead)
}

func activityHostRegistry(name string, client *appwire.Client, attached bool) *appsource.Registry {
	source := appsource.NewRemoteHubSource(name, nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) { return client, attached && host == name })
	registry := appsource.NewRegistry()
	registry.Add(source)
	return registry
}

// An attached host answers for its own sessions: its refs come back in the
// controller's namespace, a row the controller cannot address (the host's own
// host) is dropped, and a filter reaches the host in the host's namespace.
func TestActivityReadFansOutToAttachedHosts(t *testing.T) {
	quiet := int64(1_000)
	client, calls := newScriptedRemoteHub(t, activityHost(appwire.ActivityReadResponse{Sessions: []appwire.SessionActivity{
		{Ref: "local:r1", Minutes: activityReadMinutes, QuietForMS: &quiet},
		{Ref: "nested-host:x", Minutes: activityReadMinutes},
	}}))
	cfg := hubcore.WebConfig{
		Roster:                     hubcore.NewRosterWithEntries(liveActivityEntry(1, "01LOCAL", appwire.ThreadStatusAwaiting, silentFor(time.Minute))),
		RemoteHosts:                []hostreg.Host{{Name: "h1"}},
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) { return client, host == "h1" },
	}
	registry := activityHostRegistry("h1", client, true)

	got, err := hubActivityRead(t.Context(), cfg, registry, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	want := []appwire.SessionActivity{
		{Ref: "h1:r1", Minutes: activityReadMinutes, QuietForMS: &quiet},
		{Ref: "local:01LOCAL", Minutes: activityReadMinutes},
	}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", got.Sessions, want)
	}

	remoteOnly, err := hubActivityRead(t.Context(), cfg, registry, appwire.ActivityReadParams{Refs: []string{"h1:r1"}}, activityReadNow)
	if err != nil {
		t.Fatalf("filtered read: %v", err)
	}
	if !reflect.DeepEqual(remoteOnly.Sessions, want[:1]) {
		t.Fatalf("remote-only sessions = %+v, want only h1:r1", remoteOnly.Sessions)
	}
	if reads := activityHostReads(t, calls()); len(reads) != 2 || reads[0].Refs != nil || !reflect.DeepEqual(reads[1].Refs, []string{"local:r1"}) {
		t.Fatalf("host requests = %+v, want an unfiltered read, then local:r1", reads)
	}
	// A filter naming only local sessions never calls the host.
	localOnly, err := hubActivityRead(t.Context(), cfg, registry, appwire.ActivityReadParams{Refs: []string{"local:01LOCAL"}}, activityReadNow)
	if err != nil {
		t.Fatalf("local-only read: %v", err)
	}
	if !reflect.DeepEqual(localOnly.Sessions, want[1:]) {
		t.Fatalf("local-only sessions = %+v, want only local:01LOCAL", localOnly.Sessions)
	}
	if reads := activityHostReads(t, calls()); len(reads) != 2 {
		t.Fatalf("host requests = %d, want still 2", len(reads))
	}
}

// A host that fails contributes nothing, and a host that is not attached is
// never called; the local sessions are still read.
func TestActivityReadSkipsAFailingOrUnattachedHost(t *testing.T) {
	failing, failedCalls := newScriptedRemoteHub(t, activityHost(appwire.InternalError("host failed")))
	cfg := hubcore.WebConfig{
		Roster:      hubcore.NewRosterWithEntries(liveActivityEntry(1, "01LOCAL", appwire.ThreadStatusAwaiting, silentFor(time.Minute))),
		RemoteHosts: []hostreg.Host{{Name: "h1"}},
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			return failing, host == "h1"
		},
	}
	got, err := hubActivityRead(t.Context(), cfg, activityHostRegistry("h1", failing, true), appwire.ActivityReadParams{}, activityReadNow)
	if err != nil || len(got.Sessions) != 1 || got.Sessions[0].Ref != "local:01LOCAL" {
		t.Fatalf("read with a failing host = %+v (%v), want only the local session", got, err)
	}
	if reads := activityHostReads(t, failedCalls()); len(reads) != 1 {
		t.Fatalf("failing host calls = %d, want 1", len(reads))
	}

	idle, idleCalls := newScriptedRemoteHub(t, activityHost(appwire.ActivityReadResponse{Sessions: []appwire.SessionActivity{{Ref: "local:r1", Minutes: activityReadMinutes}}}))
	cfg.RemoteHostClientIfAttached = func(string) (*appwire.Client, bool) { return nil, false }
	got, err = hubActivityRead(t.Context(), cfg, activityHostRegistry("h1", idle, false), appwire.ActivityReadParams{}, activityReadNow)
	if reads := activityHostReads(t, idleCalls()); err != nil || len(got.Sessions) != 1 || len(reads) != 0 {
		t.Fatalf("read with an unattached host = %+v (%v), %d host calls; want only the local session and no call", got, err, len(reads))
	}
}

// A Working row says what the session last set out to do, so the read carries
// each daemon's latest tool intent, cut again here: a row carries the words its
// own daemon sent and no more, whatever a remote host or an older daemon
// claimed.
func TestActivityReadCarriesTheLatestToolIntent(t *testing.T) {
	named := silentFor(time.Minute)
	named.LatestIntent = "Reading the board's row tests."
	shouty := silentFor(time.Minute)
	shouty.LatestIntent = strings.Repeat("界", appwire.MaxIntentRunes+50)
	roster := hubcore.NewRosterWithEntries(
		liveActivityEntry(1, "01NAMES", appwire.ThreadStatusActive, named),
		liveActivityEntry(2, "01SHOUTY", appwire.ThreadStatusActive, shouty),
		liveActivityEntry(3, "01SILENT", appwire.ThreadStatusActive, silentFor(time.Minute)),
	)
	got, err := hubActivityRead(t.Context(), hubcore.WebConfig{Roster: roster}, nil, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	want := []appwire.SessionActivity{
		{Ref: "local:01NAMES", Minutes: activityReadMinutes, QuietForMS: quietMillis(time.Minute), LatestIntent: "Reading the board's row tests."},
		{Ref: "local:01SHOUTY", Minutes: activityReadMinutes, QuietForMS: quietMillis(time.Minute), LatestIntent: appwire.Excerpt(shouty.LatestIntent, appwire.MaxIntentRunes)},
		{Ref: "local:01SILENT", Minutes: activityReadMinutes, QuietForMS: quietMillis(time.Minute)},
	}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("read = %+v, want %+v", got.Sessions, want)
	}
	if got := got.Sessions[1].LatestIntent; utf8.RuneCountInString(got) > appwire.MaxIntentRunes {
		t.Fatalf("intent runs %d runes, want at most %d", utf8.RuneCountInString(got), appwire.MaxIntentRunes)
	}
}

// A remote host's answer is relayed, not trusted: its rows are cut to the same
// bound a local one's are, so a host running older or different code cannot
// widen a row through this controller.
func TestActivityReadBoundsARemoteHostsIntent(t *testing.T) {
	long := strings.Repeat("界", appwire.MaxIntentRunes+50)
	client, _ := newScriptedRemoteHub(t, activityHost(appwire.ActivityReadResponse{Sessions: []appwire.SessionActivity{
		{Ref: "local:r1", Minutes: activityReadMinutes, LatestIntent: long},
	}}))
	cfg := hubcore.WebConfig{
		RemoteHosts:                []hostreg.Host{{Name: "h1"}},
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) { return client, host == "h1" },
	}
	got, err := hubActivityRead(t.Context(), cfg, activityHostRegistry("h1", client, true), appwire.ActivityReadParams{}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	want := []appwire.SessionActivity{{Ref: "h1:r1", Minutes: activityReadMinutes, LatestIntent: appwire.Excerpt(long, appwire.MaxIntentRunes)}}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", got.Sessions, want)
	}
}
