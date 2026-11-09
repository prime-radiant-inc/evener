package execenv

import (
	"fmt"
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/gittest"
)

// TestMain collects the session scratch and temp containers these tests'
// environments retain at close, which only the 24h crashed-scratch sweep would
// otherwise reclaim, and removes them when the run ends. It isolates the git
// every test spawns, through RunGit and the project resolver as much as through
// fixtures, from host config and automatic maintenance.
func TestMain(m *testing.M) {
	removeGitConfig, err := gittest.IsolateProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "execenv TestMain: %v\n", err)
		os.Exit(2)
	}
	code := sandboxtest.Run(m, "evener-execenv-test-")
	removeGitConfig()
	os.Exit(code)
}
