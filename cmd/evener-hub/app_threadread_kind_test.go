package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// A live daemon's projection omits EvenerThread.kind, so the hub fills it from
// the session meta it already indexes. Fork children need it to keep their
// inherited transcript prefix out of their own token total.
func TestStampHubThreadKindNamesLiveForksAndSubagents(t *testing.T) {
	newID := func() string {
		id, err := identifier.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	rootID, forkID, delegateID := newID(), newID(), newID()
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: rootID},
		{ID: forkID, ParentSessionID: rootID},
		{ID: delegateID, ParentSessionID: rootID, IsSubagent: true},
	})
	cfg := hubcore.WebConfig{Past: past}
	local := func(id string) appwire.Thread {
		return appwire.Thread{ID: id, Evener: appwire.EvenerThread{
			Ref: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: id}.String(),
		}}
	}
	for _, tc := range []struct {
		name   string
		thread appwire.Thread
		want   string
	}{
		{"root", local(rootID), ""},
		{"fork child", local(forkID), "fork"},
		{"delegate", local(delegateID), "subagent"},
		{"existing kind", func() appwire.Thread {
			thread := local(forkID)
			thread.Evener.Kind = "session"
			return thread
		}(), "session"},
		{"remote ref", appwire.Thread{ID: forkID, Evener: appwire.EvenerThread{
			Ref: appwire.Ref{SourceID: "host-a", ThreadID: forkID}.String(),
		}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stampHubThreadKind(cfg, tc.thread).Evener.Kind; got != tc.want {
				t.Fatalf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStampHubThreadKindWithoutPastIndexIsANoOp(t *testing.T) {
	thread := appwire.Thread{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Evener: appwire.EvenerThread{
		Ref: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}.String(),
	}}
	if got := stampHubThreadKind(hubcore.WebConfig{}, thread).Evener.Kind; got != "" {
		t.Fatalf("kind = %q with no past index, want empty", got)
	}
}
