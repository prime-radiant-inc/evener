package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestStampAppNotificationTargetWithStruct covers the happy path: a
// projector-produced params struct is restamped in place of its own view of
// the target, and the concrete type survives the trip (no JSON round trip).
func TestStampAppNotificationTargetWithStruct(t *testing.T) {
	params := appwire.ThreadStatusChangedParams{ThreadID: "old-id", Ref: "old-ref", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}}
	got := stampAppNotificationTarget(params, "thread-1", "local:thread-1")
	stamped, ok := got.(appwire.ThreadStatusChangedParams)
	if !ok {
		t.Fatalf("got type = %T, want appwire.ThreadStatusChangedParams", got)
	}
	if stamped.ThreadID != "thread-1" || stamped.Ref != "local:thread-1" {
		t.Fatalf("target = (%q, %q), want (thread-1, local:thread-1)", stamped.ThreadID, stamped.Ref)
	}
	if stamped.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("status = %q, want the original payload preserved", stamped.Status.Type)
	}
	if params.ThreadID != "old-id" {
		t.Fatal("the caller's params value must not be mutated")
	}
}

// TestStampAppNotificationTargetWithMap covers the projector's map-shaped
// params (warning, turn/completed, thread/closed, steering): the server's
// target overwrites the projector's own threadId/ref keys.
func TestStampAppNotificationTargetWithMap(t *testing.T) {
	params := map[string]any{"threadId": "old-id", "ref": "old-ref", "message": "boom"}
	got := stampAppNotificationTarget(params, "thread-1", "local:thread-1")
	stamped, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got type = %T, want map[string]any", got)
	}
	if stamped["threadId"] != "thread-1" || stamped["ref"] != "local:thread-1" {
		t.Fatalf("target = (%v, %v), want (thread-1, local:thread-1)", stamped["threadId"], stamped["ref"])
	}
	if stamped["message"] != "boom" {
		t.Fatalf("message = %v, want the original payload preserved", stamped["message"])
	}
}

// TestStampAppNotificationTargetPassesThroughUnknownParams pins the fallback:
// params that neither implement appwire.NotificationTargeted nor are maps
// (nil included) pass through untouched rather than being force-stamped.
func TestStampAppNotificationTargetPassesThroughUnknownParams(t *testing.T) {
	if got := stampAppNotificationTarget(nil, "thread-1", "ref-1"); got != nil {
		t.Fatalf("nil params = %v, want nil pass-through", got)
	}
	if got := stampAppNotificationTarget(struct{}{}, "thread-1", "ref-1"); got != struct{}{} {
		t.Fatalf("unknown params = %v, want unchanged pass-through", got)
	}
}

// TestCloneJSONCompatibleNil covers the nil path.
func TestCloneJSONCompatibleNil(t *testing.T) {
	if got := cloneJSONCompatible(nil); got != nil {
		t.Fatalf("cloneJSONCompatible(nil) = %v, want nil", got)
	}
}

// TestCloneJSONCompatibleStruct covers the struct path.
func TestCloneJSONCompatibleStruct(t *testing.T) {
	type S struct {
		Name string `json:"name"`
	}
	got := cloneJSONCompatible(S{Name: "test"})
	s, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got type = %T, want map[string]any", got)
	}
	if s["name"] != "test" {
		t.Fatalf("name = %v, want test", s["name"])
	}
}

// TestCloneJSONCompatibleMarshalError covers the marshal-error path.
func TestCloneJSONCompatibleMarshalError(t *testing.T) {
	ch := make(chan int)
	got := cloneJSONCompatible(ch)
	// Should return the original value.
	if got == nil {
		t.Fatal("should return original value, not nil")
	}
}

// TestCloneJSONCompatibleUnmarshalError covers the unmarshal-error path. This
// is hard to trigger because if Marshal succeeds, Unmarshal usually does too.
// We can use json.RawMessage with invalid JSON content.
func TestCloneJSONCompatibleUnmarshalErrorUnreachable(t *testing.T) {
	t.Skip("the Unmarshal-error path in cloneJSONCompatible is unreachable: if Marshal succeeds, Unmarshal into any{} also succeeds")
}

// TestStampFailureCountOnStatusChangeNotStatusChanged covers the path where
// the method is not ThreadStatusChanged.
func TestStampFailureCountOnStatusChangeNotStatusChanged(t *testing.T) {
	s := NewServer(ServerConfig{})
	params := appwire.AgentMessageDeltaParams{ThreadID: "th_1"}
	got := s.stampFailureCountOnStatusChange("some.other.method", params)
	if got != params {
		t.Fatal("should return params unchanged for non-status-changed method")
	}
}

// TestStampFailureCountOnStatusChangeWrongType covers the path where params
// is not ThreadStatusChangedParams.
func TestStampFailureCountOnStatusChangeWrongType(t *testing.T) {
	s := NewServer(ServerConfig{})
	got := s.stampFailureCountOnStatusChange(appwire.NotifyThreadStatusChanged, "wrong type")
	if got != "wrong type" {
		t.Fatal("should return params unchanged for wrong type")
	}
}

// TestStampCapabilitiesOnStatusChangeNotStatusChanged covers the non-status-changed path.
func TestStampCapabilitiesOnStatusChangeNotStatusChanged(t *testing.T) {
	s := NewServer(ServerConfig{})
	params := appwire.AgentMessageDeltaParams{ThreadID: "th_1"}
	got := s.stampCapabilitiesOnStatusChange("some.other.method", params)
	if got != params {
		t.Fatal("should return params unchanged for non-status-changed method")
	}
}

// TestStampCapabilitiesOnStatusChangeWrongType covers the wrong-type path.
func TestStampCapabilitiesOnStatusChangeWrongType(t *testing.T) {
	s := NewServer(ServerConfig{})
	got := s.stampCapabilitiesOnStatusChange(appwire.NotifyThreadStatusChanged, "wrong type")
	if got != "wrong type" {
		t.Fatal("should return params unchanged for wrong type")
	}
}
