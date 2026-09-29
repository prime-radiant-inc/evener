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

// fillEveryFieldNonZero reflectively sets every exported field of v (an
// addressable struct) to a representative non-zero value, recursing through
// pointers, slices, maps and nested structs. time.Time is filled directly
// (its own fields are unexported, so reflection can't reach them).
//
// A test that copies only SOME of a source struct's fields into a derived
// value, then compares the derived value against an explicit "want", cannot
// tell "this field was correctly left out" from "this field happened to be
// the zero value on both sides" unless every field starts non-zero. Filling
// every field first is what lets TestLocalDaemonEntriesFromRosterAliasCarriesOnlyItsOwnFields's
// single whole-struct comparison stand in for a field-by-field
// "only on the root" test for every current AND future field, with no
// fixture update required when a new field is added to hubcore.LiveEntry.
func fillEveryFieldNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.UnixMilli(1_700_000_000_000)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillEveryFieldNonZero(t, elem)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillEveryFieldNonZero(t, key)
		val := reflect.New(v.Type().Elem()).Elem()
		fillEveryFieldNonZero(t, val)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillEveryFieldNonZero(t, p.Elem())
		v.Set(p)
	case reflect.Struct:
		for _, field := range v.Fields() {
			if field.CanSet() {
				fillEveryFieldNonZero(t, field)
			}
		}
	default:
		t.Fatalf("fillEveryFieldNonZero: unhandled kind %v (type %v) — teach it this shape", v.Kind(), v.Type())
	}
}

// The hub's own list rows carry a root's task progress, pending question and
// failure summary, and an in-process subagent alias carries only the fields
// it owns. Every field of live starts non-zero (fillEveryFieldNonZero), so
// comparing the WHOLE alias entry against an explicit "want" of only the
// fields the alias is meant to carry means a field added to the root later (a
// last message) needs no field-specific "only on the root" test of its own,
// and no update to this fixture: any root-only field the alias literal in
// localDaemonEntriesFromRoster accidentally starts copying makes the
// comparison fail the moment it stops being nil/zero on one side only (S13b
// and S1b; fixes #2589).
func TestLocalDaemonEntriesFromRosterAliasCarriesOnlyItsOwnFields(t *testing.T) {
	var live hubcore.LiveEntry
	fillEveryFieldNonZero(t, reflect.ValueOf(&live).Elem())
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
