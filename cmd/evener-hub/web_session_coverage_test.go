package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

func TestFavoriteSessionIDMatchesExact(t *testing.T) {
	if !favoriteSessionIDMatches("local:s1", "local:s1") {
		t.Fatal("exact match should return true")
	}
}

func TestFavoriteSessionIDMatchesByRef(t *testing.T) {
	// requested parses as a Ref that equals hubRefFromTreeNodeID(actual)
	actual := "local:abc123"
	// hubRefFromTreeNodeID("local:abc123") parses to {HostID:"local", SessionID:"abc123"}
	// so requesting "local:abc123" should match
	if !favoriteSessionIDMatches("local:abc123", actual) {
		t.Fatal("ref match should return true")
	}
}

func TestFavoriteSessionIDMatchesLocalShortForm(t *testing.T) {
	// When actual's ref HostID is "local" and requested equals the SessionID
	actual := "local:session-xyz"
	if !favoriteSessionIDMatches("session-xyz", actual) {
		t.Fatal("local short-form match should return true")
	}
}

func TestFavoriteSessionIDMatchesNoMatch(t *testing.T) {
	if favoriteSessionIDMatches("local:s1", "local:s2") {
		t.Fatal("different sessions should not match")
	}
}

func TestFavoriteSessionIDMatchesClusterPrefix(t *testing.T) {
	// topLevelFavoriteSessionID returns ("", false) for cluster: prefix
	s := &WebServer{}
	id, ok := s.topLevelFavoriteSessionID(context.Background(), "cluster:foo")
	if ok || id != "" {
		t.Fatalf("cluster: prefix should return empty/false, got %q %v", id, ok)
	}
}

func TestValidateForkRequestDeferInputWithEditedMessage(t *testing.T) {
	err := validateForkRequest(forkRequest{DeferInput: true, EditedMessage: "hello"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("defer+edited should error, got %v", err)
	}
}

func TestValidateForkRequestNoDeferNoEditedMessage(t *testing.T) {
	err := validateForkRequest(forkRequest{DeferInput: false, EditedMessage: ""})
	if err == nil || !strings.Contains(err.Error(), "edited_message is required") {
		t.Fatalf("no defer + no edited should error, got %v", err)
	}
}

func TestValidateForkRequestDeferOnly(t *testing.T) {
	err := validateForkRequest(forkRequest{DeferInput: true, EditedMessage: ""})
	if err != nil {
		t.Fatalf("defer only should pass, got %v", err)
	}
}

func TestValidateForkRequestEditedMessageOnly(t *testing.T) {
	err := validateForkRequest(forkRequest{DeferInput: false, EditedMessage: "edited text"})
	if err != nil {
		t.Fatalf("edited only should pass, got %v", err)
	}
}

func TestIsActionUnavailableNotUnavailable(t *testing.T) {
	if isActionUnavailable(errors.New("some other error")) {
		t.Fatal("non-wire error should not be unavailable")
	}
}

func TestIsActionUnavailableTrue(t *testing.T) {
	err := appwire.Unavailable(string(appwire.ErrorActionUnavailable))
	if !isActionUnavailable(err) {
		t.Fatal("Unavailable with ActionUnavailable info should be true")
	}
}

func TestIsActionUnavailableWrongInfo(t *testing.T) {
	// SessionUnavailable has CodeUnavailable but a different ErrorInfo.
	err := appwire.SessionUnavailable("session unavailable")
	if isActionUnavailable(err) {
		t.Fatal("SessionUnavailable should not match isActionUnavailable")
	}
}

func TestSessionCapabilityAvailableUnknownAction(t *testing.T) {
	if sessionCapabilityAvailable(hubapi.SessionCapabilities{}, "bogus") {
		t.Fatal("unknown action should return false")
	}
}

func TestSessionCapabilityAvailableAllTrue(t *testing.T) {
	caps := hubapi.SessionCapabilities{
		Send: true, Steer: true, Interrupt: true, Compact: true,
		Clear: true, Fork: true, Shutdown: true, ChangeModel: true, Queue: true,
	}
	for _, action := range []string{"send", "steer", "interrupt", "compact", "clear", "fork", "shutdown", "model", "queue"} {
		if !sessionCapabilityAvailable(caps, action) {
			t.Fatalf("action %q with caps all-true should return true", action)
		}
	}
}
