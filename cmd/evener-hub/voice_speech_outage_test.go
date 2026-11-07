package hub

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A read killed by a replaced daemon connection must be treated as a relay
// outage, not as a fatal speech error: the bridge keeps the delivery and waits
// for recovery's resync (#5696). The relay marks that read automatically
// retryable; other session-unavailable errors, which have no recovery read,
// stay fatal. The message carries the client's method prefix.
func TestVoiceRelayOutageRecognizesReplacedConnection(t *testing.T) {
	replaced := appwire.WireError{
		Code:    appwire.CodeUnavailable,
		Message: "relay connection ended before route publication",
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorSessionUnavailable,
			RetryDisposition: appwire.RetryDispositionAutomatic,
		},
	}
	replaced.Message = "appwire thread/read: " + replaced.Message
	if !voiceRelayOutage(replaced) {
		t.Fatal("a read that lost its daemon connection was not classified as a relay outage")
	}
	if voiceRelayOutage(appwire.SessionUnavailable("thread not found")) {
		t.Fatal("a terminal session-unavailable error was classified as a relay outage")
	}
	if voiceRelayOutage(errors.New("boom")) {
		t.Fatal("an ordinary error was classified as a relay outage")
	}
	if voiceRelayOutage(appwire.InvalidParams("bad cursor")) {
		t.Fatal("an invalid-params refusal was classified as a relay outage")
	}
}
