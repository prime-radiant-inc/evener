//go:build darwin

package sandbox_test

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

// TestSeatbeltLiveContractOnHost re-runs M1's exported contract suite against the
// live host's real facts, confirming the resolver agrees with the Mac it runs on.
func TestSeatbeltLiveContractOnHost(t *testing.T) {
	if os.Getenv("EVENER_SEATBELT_LIVE") != "1" {
		t.Skip("live seatbelt test: set EVENER_SEATBELT_LIVE=1 to run on macOS")
	}
	const seatbeltBin = "/usr/bin/sandbox-exec"
	if st, err := os.Stat(seatbeltBin); err != nil || st.IsDir() {
		t.Skipf("live seatbelt test: %s not available", seatbeltBin)
	}
	sandboxtest.AssertResolve(t, sandbox.Resolve)
}
