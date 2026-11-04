package appwire

import (
	"encoding/json"
	"testing"
)

func TestRosterUpdateRoundTrip(t *testing.T) {
	t.Parallel()
	want := SessionWatchUpdate{Roster: &SessionRosterUpdate{Revision: 9, Initial: true, Events: []RosterEvent{{Ref: "remote:thread", ID: "thread", Level: "needs_you", AskPending: true, Kind: "needs_you"}}}}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got SessionWatchUpdate
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Roster == nil || got.Roster.Revision != 9 || !got.Roster.Initial || len(got.Roster.Events) != 1 || got.Roster.Events[0].Ref != "remote:thread" || got.Roster.Events[0].Kind != "needs_you" || !got.Roster.Events[0].AskPending {
		t.Fatalf("decoded roster = %+v", got.Roster)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var rosterFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["roster"], &rosterFields); err != nil {
		t.Fatal(err)
	}
	if _, present := rosterFields["summary"]; present {
		t.Fatal("aggregate roster includes a summary")
	}
}

func TestScopeWatchRequestOmitsTargetRef(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(SessionWatchRequest{Scope: "top_level"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["ref"]; ok {
		t.Fatal("scope watch contains target ref")
	}
}
