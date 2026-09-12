package fspaths

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubtestenv"
)

var testEnv *hubtestenv.Env

// TestMain redirects HOME, the XDG bases and CODEX_HOME into a throwaway root
// and clears Evener's own EVENER_* configuration, so a test in this package that
// reaches a HOME/XDG-derived default without its own t.Setenv lands in the
// throwaway root rather than in the developer's real state, and a value exported
// in the developer's shell configures nothing here.
func TestMain(m *testing.M) {
	testEnv = hubtestenv.Redirect("evener-hub-fspaths-test-env-")
	code := m.Run()
	testEnv.Discard()
	os.Exit(code)
}

// TestFspathsDefaultRootsStayInsideTheTestEnvironment pins this package's half
// of the isolation the main hub package already has: the home directory and the
// three XDG bases every Evener default derives from resolve inside the
// throwaway root, never in the developer's own home, for the whole run and not
// only at startup.
//
// Without it, a test added here that reaches a HOME/XDG-derived default without
// its own t.Setenv reads and writes the developer's real ~/.config/evener and
// ~/.local/state/evener.
func TestFspathsDefaultRootsStayInsideTheTestEnvironment(t *testing.T) {
	if escaped := testEnv.PathsOutside(hubtestenv.BaseRoots()); len(escaped) > 0 {
		t.Fatalf("home and XDG bases resolve outside the throwaway test root %q; a default derived from them would read or write there for real:\n  %s", testEnv.Root, strings.Join(escaped, "\n  "))
	}
}
