package hub

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

// A controller reads a remote host's pending question off its list row, so
// the remote row names it exactly like a local one (S1b).
func TestNavigationRemoteRowCarriesItsPendingQuestion(t *testing.T) {
	projection := remoteNavigationProjection(t, []appwire.Thread{{
		ID: "asking", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
		Evener: appwire.EvenerThread{AskPending: true, PendingQuestion: &appwire.PendingQuestion{Question: "Which datastore?", Options: []string{"Postgres", "SQLite"}, Count: 2}},
	}})
	want := &hubapi.NavigationQuestion{Text: "Which datastore?", Options: []string{"Postgres", "SQLite"}, Count: 2}
	if row := navigationProjectedSummary(t, projection, "host-a:asking"); !row.AskPending || !reflect.DeepEqual(row.Question, want) {
		t.Fatalf("remote row = ask %v, question %+v; want %+v", row.AskPending, row.Question, want)
	}
}
