package hub

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

// TestSessionCapabilityAvailableSend covers the "send" case.
func TestSessionCapabilityAvailableSend(t *testing.T) {
	caps := hubapi.SessionCapabilities{Send: true}
	if !sessionCapabilityAvailable(caps, "send") {
		t.Fatal("send should be available when caps.Send is true")
	}
	caps.Send = false
	if sessionCapabilityAvailable(caps, "send") {
		t.Fatal("send should not be available when caps.Send is false")
	}
}

// TestSessionCapabilityAvailableSteer covers the "steer" case.
func TestSessionCapabilityAvailableSteer(t *testing.T) {
	caps := hubapi.SessionCapabilities{Steer: true}
	if !sessionCapabilityAvailable(caps, "steer") {
		t.Fatal("steer should be available when caps.Steer is true")
	}
}

// TestSessionCapabilityAvailableInterrupt covers the "interrupt" case.
func TestSessionCapabilityAvailableInterrupt(t *testing.T) {
	caps := hubapi.SessionCapabilities{Interrupt: true}
	if !sessionCapabilityAvailable(caps, "interrupt") {
		t.Fatal("interrupt should be available when caps.Interrupt is true")
	}
}

// TestSessionCapabilityAvailableCompact covers the "compact" case.
func TestSessionCapabilityAvailableCompact(t *testing.T) {
	caps := hubapi.SessionCapabilities{Compact: true}
	if !sessionCapabilityAvailable(caps, "compact") {
		t.Fatal("compact should be available when caps.Compact is true")
	}
}

// TestSessionCapabilityAvailableClear covers the "clear" case.
func TestSessionCapabilityAvailableClear(t *testing.T) {
	caps := hubapi.SessionCapabilities{Clear: true}
	if !sessionCapabilityAvailable(caps, "clear") {
		t.Fatal("clear should be available when caps.Clear is true")
	}
}

// TestSessionCapabilityAvailableFork covers the "fork" case.
func TestSessionCapabilityAvailableFork(t *testing.T) {
	caps := hubapi.SessionCapabilities{Fork: true}
	if !sessionCapabilityAvailable(caps, "fork") {
		t.Fatal("fork should be available when caps.Fork is true")
	}
}

// TestSessionCapabilityAvailableShutdown covers the "shutdown" case.
func TestSessionCapabilityAvailableShutdown(t *testing.T) {
	caps := hubapi.SessionCapabilities{Shutdown: true}
	if !sessionCapabilityAvailable(caps, "shutdown") {
		t.Fatal("shutdown should be available when caps.Shutdown is true")
	}
}

// TestSessionCapabilityAvailableModel covers the "model" case.
func TestSessionCapabilityAvailableModel(t *testing.T) {
	caps := hubapi.SessionCapabilities{ChangeModel: true}
	if !sessionCapabilityAvailable(caps, "model") {
		t.Fatal("model should be available when caps.ChangeModel is true")
	}
}

// TestSessionCapabilityAvailableQueue covers the "queue" case.
func TestSessionCapabilityAvailableQueue(t *testing.T) {
	caps := hubapi.SessionCapabilities{Queue: true}
	if !sessionCapabilityAvailable(caps, "queue") {
		t.Fatal("queue should be available when caps.Queue is true")
	}
}

// TestSessionCapabilityAvailableUnknown covers the default case.
func TestSessionCapabilityAvailableUnknown(t *testing.T) {
	caps := hubapi.SessionCapabilities{Send: true}
	if sessionCapabilityAvailable(caps, "unknown") {
		t.Fatal("unknown action should return false")
	}
}

// TestInputItemsForTextEmpty covers the empty text path.
func TestInputItemsForTextEmpty(t *testing.T) {
	if got := inputItemsForText(""); got != nil {
		t.Fatalf("empty text should return nil, got %v", got)
	}
	if got := inputItemsForText("   "); got != nil {
		t.Fatalf("whitespace text should return nil, got %v", got)
	}
}

// TestInputItemsForTextNonEmpty covers the non-empty text path.
func TestInputItemsForTextNonEmpty(t *testing.T) {
	got := inputItemsForText("hello")
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got))
	}
	if got[0].Type != "text" || got[0].Text != "hello" {
		t.Fatalf("expected text item 'hello', got %+v", got[0])
	}
}

// TestWriteSessionActionErrorAPIPath covers the /api/ path in
// writeSessionActionError (line 295-296).
func TestWriteSessionActionErrorAPIPath(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/x/send", nil)
	writeSessionActionError(w, r, errors.New("test error"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, w.Code)
	}
}

// TestWriteSessionActionErrorNonAPIPath covers the non-/api/ path in
// writeSessionActionError (lines 299-306).
func TestWriteSessionActionErrorNonAPIPath(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sessions/x/send", nil)
	writeSessionActionError(w, r, errors.New("test error"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, w.Code)
	}
}

// TestWriteSessionActionErrorWireError covers the wire error status mapping
// path.
func TestWriteSessionActionErrorWireError(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sessions/x/send", nil)
	writeSessionActionError(w, r, appwire.InvalidParams("bad params"))
	if w.Code == http.StatusBadGateway {
		t.Fatalf("wire error should map to a non-502 status, got %d", w.Code)
	}
}

// TestFavoriteSessionIDMatchesLocalNonLocal covers the case where actual is
// not a local ref.
func TestFavoriteSessionIDMatchesLocalNonLocal(t *testing.T) {
	if favoriteSessionIDMatches("local:s1", "remote:s2") {
		t.Fatal("different sessions should not match")
	}
}
