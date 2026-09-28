package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// appStatus maps the daemon's raw state string (plus a live "processing" flag)
// to the appwire thread status, defaulting unknown states to idle.
func TestAppStatus(t *testing.T) {
	// A processing session is always Active regardless of the recorded state.
	if got := appStatus(appwire.ThreadStatusIdle, true, false); got != appwire.ThreadStatusActive {
		t.Fatalf("processing session status=%q, want active", got)
	}

	cases := map[string]string{
		appwire.ThreadStatusIdle:        appwire.ThreadStatusIdle,
		appwire.ThreadStatusActive:      appwire.ThreadStatusActive,
		appwire.ThreadStatusAwaiting:    appwire.ThreadStatusAwaiting,
		appwire.ThreadStatusWarning:     appwire.ThreadStatusWarning,
		appwire.ThreadStatusSystemError: appwire.ThreadStatusSystemError,
		appwire.ThreadStatusClosed:      appwire.ThreadStatusClosed,
		appwire.ThreadStatusNotLoaded:   appwire.ThreadStatusNotLoaded,
		"  idle  ":                      appwire.ThreadStatusIdle, // trimmed
		"something-unknown":             appwire.ThreadStatusIdle, // default
		"":                              appwire.ThreadStatusIdle, // default
	}
	for state, want := range cases {
		if got := appStatus(state, false, false); got != want {
			t.Errorf("appStatus(%q,false)=%q, want %q", state, got, want)
		}
	}
}
