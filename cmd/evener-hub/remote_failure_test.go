package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A controller reads a remote host's failure summary off its list row, so the
// remote Failed row says why exactly like a local one (S1c).
func TestNavigationRemoteRowCarriesItsFailure(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "failed", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusSystemError},
		Evener: appwire.EvenerThread{Failure: &appwire.ThreadFailure{Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 429}}},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	want := &hubapi.NavigationFailure{Title: "Usage limit reached", CauseKind: "provider", Provider: "codex-jesse-fsck.com", Status: 429}
	if row := navigationProjectedSummary(t, projection, "host-a:failed"); row.State != "errored" || !reflect.DeepEqual(row.Failure, want) {
		t.Fatalf("remote row = state %q, failure %+v; want errored with %+v", row.State, row.Failure, want)
	}
}
