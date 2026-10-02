package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestRESTCapabilitiesDoNotAdvertiseForkOrResume verifies the hub capability
// adapter leaves fork/resume to AppWire's ForkFromTurn capability.
func TestRESTCapabilitiesDoNotAdvertiseForkOrResume(t *testing.T) {
	caps := hubCapabilitiesFromAppwire(appwire.ThreadCapabilities{Send: true, ForkFromTurn: true})
	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"fork"`, `"resume"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("appwire fork-bearing capabilities still advertise %s: %s", key, data)
		}
	}
}
