package appsource

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// The hub's voice bridge keeps speech when a read is cut by a replaced daemon
// connection. That classification relies on these read errors carrying the
// automatic retry marker; a plain SessionUnavailable is fatal there.
func TestRelayConnectionReplacedIsAutomaticallyRetryable(t *testing.T) {
	err := relayConnectionReplaced("relay connection ended before route publication")
	if err.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want unavailable", err.Code)
	}
	data, ok := appwire.DecodedErrorData(err.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable || data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("data = %+v, want sessionUnavailable with automatic retry", data)
	}
}
