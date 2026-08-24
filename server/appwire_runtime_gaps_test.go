package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestStampAppNotificationTargetWithStruct covers the happy path where params
// is a struct that can be marshaled and have fields added.
func TestStampAppNotificationTargetWithStruct(t *testing.T) {
	params := appwire.ThreadStatusChangedParams{ThreadID: "old-id"}
	got := stampAppNotificationTarget(params, "thread-1", "local:thread-1")
	raw, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("got type = %T, want json.RawMessage", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tid, _ := json.Marshal("thread-1"); string(fields["threadId"]) != string(tid) {
		t.Fatalf("threadId = %q, want %q", fields["threadId"], tid)
	}
	if ref, _ := json.Marshal("local:thread-1"); string(fields["ref"]) != string(ref) {
		t.Fatalf("ref = %q, want %q", fields["ref"], ref)
	}
}

// TestStampAppNotificationTargetWithNil covers the nil-params path.
func TestStampAppNotificationTargetWithNil(t *testing.T) {
	got := stampAppNotificationTarget(nil, "thread-1", "ref-1")
	// nil marshals to "null", which is not "null" string check — it's the
	// bytes "null". The function checks `string(data) != "null"`, so nil
	// params skip the Unmarshal but still add threadId and ref fields.
	raw, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("got type = %T, want json.RawMessage", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := fields["threadId"]; !ok {
		t.Fatal("threadId should be present")
	}
}

// TestStampAppNotificationTargetWithEmptyParams covers the path where params
// is an empty struct (marshals to "{}").
func TestStampAppNotificationTargetWithEmptyParams(t *testing.T) {
	got := stampAppNotificationTarget(struct{}{}, "thread-1", "ref-1")
	raw, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("got type = %T, want json.RawMessage", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := fields["threadId"]; !ok {
		t.Fatal("threadId should be present")
	}
}

// TestStampAppNotificationTargetMarshalError covers the marshal-error path by
// passing a channel (which cannot be marshaled to JSON).
func TestStampAppNotificationTargetMarshalError(t *testing.T) {
	ch := make(chan int)
	got := stampAppNotificationTarget(ch, "thread-1", "ref-1")
	// Should return the original value since Marshal fails.
	if got == nil {
		t.Fatal("should return original value, not nil")
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
