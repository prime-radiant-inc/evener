package appwire

import (
	"testing"
)

// The hub's voice bridge keeps speech when a read is cut by a replaced daemon
// connection. That classification relies on these read errors carrying the
// automatic retry marker; a plain SessionUnavailable is fatal there.
func TestRetryableSessionUnavailableIsAutomaticallyRetryable(t *testing.T) {
	err := RetryableSessionUnavailable("relay connection ended before route publication")
	if err.Code != CodeUnavailable {
		t.Fatalf("code = %d, want unavailable", err.Code)
	}
	data, ok := DecodedErrorData(err.Data)
	if !ok || data.EvenerErrorInfo != ErrorSessionUnavailable || data.RetryDisposition != RetryDispositionAutomatic {
		t.Fatalf("data = %+v, want sessionUnavailable with automatic retry", data)
	}
}
