package appsource

import (
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestCodexSourceSessionUnavailable(t *testing.T) {
	// A canonical SessionUnavailable wire error is recognised.
	if !codexSourceSessionUnavailable(appwire.SessionUnavailable("gone")) {
		t.Error("SessionUnavailable wire error should be recognised")
	}

	// The decoded-over-the-wire shape (map[string]any data) is also recognised.
	mapShape := appwire.WireError{
		Code:    appwire.CodeUnavailable,
		Message: "gone",
		Data:    map[string]any{"serfErrorInfo": string(appwire.ErrorSessionUnavailable)},
	}
	if !codexSourceSessionUnavailable(mapShape) {
		t.Error("map-shaped SessionUnavailable data should be recognised")
	}

	// Right code but a different serfErrorInfo is not a session-unavailable.
	wrongInfo := appwire.WireError{
		Code: appwire.CodeUnavailable,
		Data: appwire.ErrorData{SerfErrorInfo: appwire.ErrorProviderUnavailable},
	}
	if codexSourceSessionUnavailable(wrongInfo) {
		t.Error("provider-unavailable should not be a session-unavailable")
	}

	// Unavailable code but an unexpected data type falls through to false.
	badData := appwire.WireError{Code: appwire.CodeUnavailable, Data: 42}
	if codexSourceSessionUnavailable(badData) {
		t.Error("unexpected data type should not be a session-unavailable")
	}

	// A different code is never a session-unavailable.
	if codexSourceSessionUnavailable(appwire.InvalidParams("nope")) {
		t.Error("invalid-params should not be a session-unavailable")
	}

	// A plain (non-wire) error is not a session-unavailable.
	if codexSourceSessionUnavailable(errors.New("boom")) {
		t.Error("plain error should not be a session-unavailable")
	}
}
