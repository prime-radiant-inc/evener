package hub

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestStampClosedThreadCapabilitiesNonStatusNotification covers the early return
// for a notification that is not a ThreadStatusChanged method (line 160-161).
func TestStampClosedThreadCapabilitiesNonStatusNotification(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyEvenerThreadResync,
		Params: []byte(`{}`),
	}
	got := stampClosedThreadCapabilities(notification)
	if got.Method != notification.Method || string(got.Params) != string(notification.Params) {
		t.Fatalf("non-status notification should be passed through unchanged")
	}
}

// TestStampClosedThreadCapabilitiesEmptyParams covers the early return for empty
// params (line 164-165).
func TestStampClosedThreadCapabilitiesEmptyParams(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: nil,
	}
	got := stampClosedThreadCapabilities(notification)
	if string(got.Params) != "" {
		t.Fatalf("notification with nil params should be passed through unchanged")
	}
}

// TestStampClosedThreadCapabilitiesInvalidParamsJSON covers the early return for
// params that are not valid JSON (line 164-165).
func TestStampClosedThreadCapabilitiesInvalidParamsJSON(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: []byte(`{invalid json`),
	}
	got := stampClosedThreadCapabilities(notification)
	if string(got.Params) != `{invalid json` {
		t.Fatalf("notification with invalid JSON should be passed through unchanged")
	}
}

// TestStampClosedThreadCapabilitiesEmptyStatusField covers the early return when
// the status field is missing or empty (line 168-169).
func TestStampClosedThreadCapabilitiesEmptyStatusField(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: []byte(`{"threadID":"t1"}`),
	}
	got := stampClosedThreadCapabilities(notification)
	// No "status" key in params, so it should pass through
	var params map[string]json.RawMessage
	if err := json.Unmarshal(got.Params, &params); err != nil {
		t.Fatalf("output should still be valid JSON: %v", err)
	}
	if _, ok := params["capabilities"]; ok {
		t.Fatalf("should not have capabilities when status is missing")
	}
}

// TestStampClosedThreadCapabilitiesInvalidStatusJSON covers the early return
// when the status field has invalid JSON (line 168-169).
func TestStampClosedThreadCapabilitiesInvalidStatusJSON(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: []byte(`{"status":"not-an-object"}`),
	}
	got := stampClosedThreadCapabilities(notification)
	var params map[string]json.RawMessage
	if err := json.Unmarshal(got.Params, &params); err != nil {
		t.Fatalf("output should still be valid JSON: %v", err)
	}
	if _, ok := params["capabilities"]; ok {
		t.Fatalf("should not have capabilities when status is invalid")
	}
}

// TestStampClosedThreadCapabilitiesNonClosedStatus covers the early return when
// the status type is not "closed" (line 171-172).
func TestStampClosedThreadCapabilitiesNonClosedStatus(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: testRawJSON(t, appwire.ThreadStatusChangedParams{
			ThreadID: "t1",
			Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		}),
	}
	got := stampClosedThreadCapabilities(notification)
	var params map[string]json.RawMessage
	if err := json.Unmarshal(got.Params, &params); err != nil {
		t.Fatalf("output should be valid JSON: %v", err)
	}
	if _, ok := params["capabilities"]; ok {
		t.Fatalf("should not have capabilities for non-closed status")
	}
}

// TestStampClosedThreadCapabilitiesZeroLengthStatusRaw covers the path where
// params["status"] has length 0 (line 168).
func TestStampClosedThreadCapabilitiesZeroLengthStatusRaw(t *testing.T) {
	notification := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: []byte(`{"status":null}`),
	}
	got := stampClosedThreadCapabilities(notification)
	var params map[string]json.RawMessage
	if err := json.Unmarshal(got.Params, &params); err != nil {
		t.Fatalf("output should be valid JSON: %v", err)
	}
	if _, ok := params["capabilities"]; ok {
		t.Fatalf("should not have capabilities when status is null")
	}
}

// TestRelayRetryBackoffCappedAtMax covers the max-delay cap in
// relayRetryBackoff.Next (lines 112-113).
func TestRelayRetryBackoffCappedAtMax(t *testing.T) {
	var b relayRetryBackoff
	// Keep calling Next until delay exceeds max and gets capped
	for range 10 {
		b.Next()
	}
	if b.delay != relayRetryMaxDelay {
		t.Fatalf("delay should be capped at %v, got %v", relayRetryMaxDelay, b.delay)
	}
	// Next after cap should stay at max
	got := b.Next()
	if got != relayRetryMaxDelay {
		t.Fatalf("delay after cap should stay at %v, got %v", relayRetryMaxDelay, got)
	}
}

// TestRelayRetryBackoffFirstNextReturnsMin covers the first call to Next
// returning the minimum delay (lines 108-109).
func TestRelayRetryBackoffFirstNextReturnsMin(t *testing.T) {
	var b relayRetryBackoff
	got := b.Next()
	if got != relayRetryMinDelay {
		t.Fatalf("first Next should return %v, got %v", relayRetryMinDelay, got)
	}
}

// TestRelayRetryBackoffSecondNextDoubles covers the second call doubling the
// delay (lines 110-111).
func TestRelayRetryBackoffSecondNextDoubles(t *testing.T) {
	var b relayRetryBackoff
	b.Next() // min delay
	got := b.Next()
	if got != 2*relayRetryMinDelay {
		t.Fatalf("second Next should return %v, got %v", 2*relayRetryMinDelay, got)
	}
}
