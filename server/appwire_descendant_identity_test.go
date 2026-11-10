package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// Child refs name child sessions, but their serving instance is the current
// root session, as it is in the hub's live child list. Clear replaces that owner.
func TestDescendantIdentityAcrossStartAndOwnerReplacement(t *testing.T) {
	s := NewServer(ServerConfig{})
	t.Cleanup(s.Close)
	install := func(id, boot string) {
		t.Helper()
		prepared, err := PrepareAppIdentityForRef("local", id, "local:workspace", "")
		if err != nil {
			t.Fatal(err)
		}
		s.ReplaceAppIdentity(prepared.WithBootGeneration(boot), nil)
	}
	emit := func(owner, child string, data events.EventData) {
		t.Helper()
		event := events.New(data)
		event.SessionID = child
		s.RecordDescendantAppEvent(owner, event)
	}
	check := func(t *testing.T, child, owner, boot string) {
		t.Helper()
		read, err := s.handleAppThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + child})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.ID != child || read.Thread.SessionID != child || read.Thread.Evener.Ref != "local:"+child || read.Thread.Evener.InstanceID != owner || read.Thread.Evener.ParentRef != "local:workspace" || read.BootGeneration != boot+"@"+owner || read.Thread.Evener.Kind != "subagent" || read.Thread.Evener.Manager || read.Thread.Evener.MutationStateAuthoritative {
			t.Errorf("child identity: %+v, boot=%q, want child=%q owner=%q boot=%q", read.Thread, read.BootGeneration, child, owner, boot+"@"+owner)
		}
		list, err := s.handleAppThreadList(t.Context(), appwire.ThreadListParams{})
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range list.Data {
			if thread.ID == child {
				if thread.Evener.InstanceID != read.Thread.Evener.InstanceID || thread.Evener.Ref != read.Thread.Evener.Ref {
					t.Errorf("list/read disagree: list=%+v read=%+v", thread, read.Thread)
				}
				return
			}
		}
		t.Errorf("child %q absent from daemon list", child)
	}
	install("root-old", "3")
	emit("root-old", "child-old", events.ExecutionStartedData{TurnID: "turn-old"})
	t.Run("before SessionStart", func(t *testing.T) { check(t, "child-old", "root-old", "3") })
	emit("root-old", "child-old", events.SessionStartData{Profile: "child-profile"})
	t.Run("SessionStart replacement", func(t *testing.T) { check(t, "child-old", "root-old", "3") })
	install("root-new", "4")
	emit("root-old", "child-old", events.SessionStartData{})
	emit("root-old", "late-child", events.ExecutionStartedData{TurnID: "late"})
	for _, child := range []string{"child-old", "late-child"} {
		if _, found := s.appThreadForID(child); found {
			t.Errorf("old owner republished %q after replacement", child)
		}
	}
	emit("root-new", "child-new", events.ExecutionStartedData{TurnID: "turn-new"})
	t.Run("new owner", func(t *testing.T) { check(t, "child-new", "root-new", "4") })
	emit("root-new", "child-new", events.SessionStartData{})
	t.Run("new owner SessionStart", func(t *testing.T) { check(t, "child-new", "root-new", "4") })
}
