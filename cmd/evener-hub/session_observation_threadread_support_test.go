package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubThreadReadReportsHostingDaemonSessionTools(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported bool
	}{
		{name: "supported daemon", supported: true},
		{name: "older daemon", supported: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHubRelayProgressFixtureWithSessionTools(t, hubcore.RelayLifecycleHooks{}, tc.supported)
			client := fixture.open()
			response, err := client.ThreadRead(fixture.ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
			if err != nil {
				t.Fatalf("ThreadRead: %v", err)
			}
			if response.TargetSessionToolsSupported == nil || *response.TargetSessionToolsSupported != tc.supported {
				t.Fatalf("target session-tools support = %v, want %t", response.TargetSessionToolsSupported, tc.supported)
			}
		})
	}
}
