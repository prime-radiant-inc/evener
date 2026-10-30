package hub

import (
	"flag"
	"testing"
)

var nestedSessionFocusBrowser = flag.Bool("nested-session-focus-browser", false, "run isolated phone nested-session focus browser baseline")

func TestNestedSessionFocusBrowser(t *testing.T) {
	if !*nestedSessionFocusBrowser {
		t.Skip("run with -nested-session-focus-browser")
	}
	runDocumentFileLinksBrowser(t, "frontend/scripts/nestedfocusguard/run.mjs")
}
