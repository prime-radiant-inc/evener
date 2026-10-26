package hubcore

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// delegateStateCases are delegate lifecycles with the status the parent-side
// navigation shows for the child. wantListed is whether the child appears at
// all.
var delegateStateCases = []struct {
	name           string
	lifecycle      string
	needsAttention bool
	wantListed     bool
	want           string
}{
	{name: "idle", lifecycle: "idle", wantListed: true, want: appwire.ThreadStatusIdle},
	{name: "running", lifecycle: "running", wantListed: true, want: appwire.ThreadStatusActive},
	{name: "running needing attention", lifecycle: "running", needsAttention: true, wantListed: true, want: appwire.ThreadStatusAwaiting},
	{name: "closed", lifecycle: "closed", wantListed: false},
	{name: "unrecognised lifecycle", lifecycle: "starting", wantListed: true, want: ""},
}

// A freshly spawned daemon's own read publishes the delegate-derived states.
func TestRosterReadSpawnedThreadDerivesSubagentStatesFromDelegates(t *testing.T) {
	for _, tc := range delegateStateCases {
		t.Run(tc.name, func(t *testing.T) {
			r, entry := newSpawnedRoster(t)
			if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: "01SPAWNED", SessionID: "01SPAWNED",
					Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
					Evener: appwire.EvenerThread{Diagnostics: &appwire.EvenerDiagnostics{
						Delegates: []appwire.EvenerDelegateInfo{
							{ChildSessionID: "child", Lifecycle: tc.lifecycle, NeedsAttention: tc.needsAttention},
							{ChildSessionID: "", Lifecycle: "running"},
						},
					}},
				}}, nil
			}); err != nil {
				t.Fatal(err)
			}
			live, ok := r.Find("01SPAWNED")
			if !ok {
				t.Fatal("the confirmed daemon was not published")
			}
			want := map[string]string{}
			var wantIDs []string
			if tc.wantListed {
				want["child"] = tc.want
				wantIDs = []string{"child"}
			}
			if !reflect.DeepEqual(live.RunningSubagentStates, want) {
				t.Fatalf("states = %v, want %v", live.RunningSubagentStates, want)
			}
			if !reflect.DeepEqual(live.RunningSubagentIDs, wantIDs) {
				t.Fatalf("ids = %v, want %v", live.RunningSubagentIDs, wantIDs)
			}
		})
	}
}

// A spawned daemon's read with no diagnostics at all publishes no states,
// where diagnostics with no delegates publish an empty set.
func TestRosterReadSpawnedThreadWithoutDiagnosticsPublishesNoSubagentStates(t *testing.T) {
	r, entry := newSpawnedRoster(t)
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok || len(live.RunningSubagentStates) != 0 || len(live.RunningSubagentIDs) != 0 {
		t.Fatalf("published = %+v ok=%v, want no subagents", live, ok)
	}
}

// The full probe and the spawned read agree on the states of delegates whose
// children the daemon lists under the matching status.
func TestProbeAgreesWithSpawnedReadOnDelegateStates(t *testing.T) {
	for _, tc := range delegateStateCases {
		if !tc.wantListed || tc.want == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			listedAs := tc.want
			if tc.lifecycle == "idle" {
				// A retained idle delegate's child can still list as active.
				listedAs = appwire.ThreadStatusActive
			}
			prober, entry := startProbeDaemon(t, probeDaemonConfig{
				sessionID:   "th_agree",
				descendants: map[string]string{"child": listedAs},
				source: wireProbeEnvelopeSource{detailed: server.DetailedStatus{Delegates: []server.DelegateStatusInfo{{
					DelegateID: "dlg", ChildSessionID: "child",
					Lifecycle: tc.lifecycle, Phase: tc.lifecycle, Status: tc.lifecycle,
					NeedsAttention: tc.needsAttention,
				}}}},
			})
			got := prober.Probe(entry)
			if !got.OK {
				t.Fatal("expected ok=true probing a real server")
			}
			if got.RunningSubagentStates["child"] != tc.want {
				t.Fatalf("probe state = %q, want %q", got.RunningSubagentStates["child"], tc.want)
			}
		})
	}
}

func TestSubagentStatesFromDelegatesIgnoresPaddingAndNilDiagnostics(t *testing.T) {
	if got := subagentStatesFromDelegates(nil); got != nil {
		t.Fatalf("nil diagnostics = %v, want nil", got)
	}
	got := subagentStatesFromDelegates(&appwire.EvenerDiagnostics{Delegates: []appwire.EvenerDelegateInfo{
		{ChildSessionID: " child ", Lifecycle: " idle "},
		{ChildSessionID: "gone", Lifecycle: " closed "},
	}})
	want := map[string]string{"child": appwire.ThreadStatusIdle}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
}
