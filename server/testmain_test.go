package server

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

// TestMain collects the session scratch and temp containers the sessions these
// tests run retain at close, which only the 24h crashed-scratch sweep would
// otherwise reclaim, and removes them when the run ends. It also makes a
// thread history created without a boot generation panic.
func TestMain(m *testing.M) {
	threadHistoryRequireBootGeneration = true
	os.Exit(sandboxtest.Run(m, "evener-server-test-"))
}
