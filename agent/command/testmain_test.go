package command

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

// TestMain collects the session scratch and temp containers these tests'
// environments retain at close, which only the 24h crashed-scratch sweep would
// otherwise reclaim, and removes them when the run ends.
func TestMain(m *testing.M) {
	os.Exit(sandboxtest.Run(m, "evener-command-test-"))
}
