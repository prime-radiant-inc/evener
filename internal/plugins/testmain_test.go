package plugins

import (
	"fmt"
	"os"
	"testing"

	"primeradiant.com/evener/gittest"
)

// TestMain isolates every git these tests spawn, the plugin clones, fetches
// and merges under test as much as their fixtures, from host config and
// automatic maintenance.
func TestMain(m *testing.M) {
	removeGitConfig, err := gittest.IsolateProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugins TestMain: %v\n", err)
		os.Exit(2)
	}
	code := m.Run()
	removeGitConfig()
	os.Exit(code)
}
