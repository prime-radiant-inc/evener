package hub

import (
	"context"
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
	// The top-level session resolver returns ("", false) for a cluster prefix.
	s := &WebServer{}
	id, ok := s.resolveTopLevelSessionRef(context.Background(), "cluster:foo")
	if ok || id != "" {
		t.Fatalf("cluster: prefix should return empty/false, got %q %v", id, ok)
	}
}
