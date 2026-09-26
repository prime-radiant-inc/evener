package hub

import (
	"context"
	"reflect"
	"testing"
)

func TestSessionRefMatchesIDExact(t *testing.T) {
	if !sessionRefMatchesID("local:s1", "local:s1") {
		t.Fatal("exact match should return true")
	}
}

func TestSessionRefMatchesIDByRef(t *testing.T) {
	// requested parses as a Ref that equals hubRefFromTreeNodeID(actual)
	actual := "local:abc123"
	// hubRefFromTreeNodeID("local:abc123") parses to {HostID:"local", SessionID:"abc123"}
	// so requesting "local:abc123" should match
	if !sessionRefMatchesID("local:abc123", actual) {
		t.Fatal("ref match should return true")
	}
}

func TestSessionRefMatchesIDLocalShortForm(t *testing.T) {
	// When actual's ref HostID is "local" and requested equals the SessionID
	actual := "local:session-xyz"
	if !sessionRefMatchesID("session-xyz", actual) {
		t.Fatal("local short-form match should return true")
	}
}

func TestSessionRefMatchesIDNoMatch(t *testing.T) {
	if sessionRefMatchesID("local:s1", "local:s2") {
		t.Fatal("different sessions should not match")
	}
}

func TestSessionRefMatchesIDClusterPrefix(t *testing.T) {
	// The top-level session resolver refuses a cluster prefix outright.
	s := &WebServer{}
	session, err := s.resolveTopLevelSessionRef(context.Background(), "cluster:foo")
	if err == nil || session != (pinSession{}) {
		t.Fatalf("cluster: prefix should be refused, got %+v %v", session, err)
	}
}

// TestDaemonStatusCarriesNoSharedNotesMirrors pins the review cleanup:
// fetchStatus used to copy the shared-notes snapshot into daemonStatus fields
// that no consumer read. Re-adding mirrored state without a reader must fail
// here rather than silently restoring dead fields.
func TestDaemonStatusCarriesNoSharedNotesMirrors(t *testing.T) {
	typ := reflect.TypeFor[daemonStatus]()
	for _, name := range []string{"HumanNote", "AgentNote", "SessionURLs", "SharedNotes"} {
		if _, found := typ.FieldByName(name); found {
			t.Errorf("daemonStatus declares unread shared-notes field %s", name)
		}
	}
}
