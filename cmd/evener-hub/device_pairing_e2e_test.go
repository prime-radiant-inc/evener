package hub

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Explicit opt-in: this launches the local browser and C toolchain. Ordinary Go
// tests exercise the authority without depending on those workstation tools.
func TestDevicePairingBrowserRoundTrip(t *testing.T) {
	if os.Getenv("EVENER_PAIRING_E2E") != "1" {
		t.Skip("set EVENER_PAIRING_E2E=1 for browser/native interoperability")
	}
	web := NewWebServer(hubcore.WebConfig{AuthToken: "synthetic-pairing-token", HubAddr: "100.64.1.2:9180"})
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	cmd := exec.Command("node", "scripts/check-device-pairing-browser.mjs")
	cmd.Dir = "frontend"
	cmd.Env = append(os.Environ(), "EVENER_PAIRING_E2E_URL="+server.URL)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
