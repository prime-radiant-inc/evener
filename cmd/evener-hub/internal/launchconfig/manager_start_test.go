package launchconfig

import (
	"strings"
	"testing"
)

// TestToArgsManagerIsStartOnly pins the launch contract for the Manager flag.
//
// Manager is a start-time property: the Hub renders it for a fresh spawn and
// never for a resume, because a resumed session restores it from its own
// persisted meta. If ToArgs rendered it unconditionally like the idle deadline,
// a resume that left the field zero would silently demote a Manager session.
func TestToArgsManagerIsStartOnly(t *testing.T) {
	ordinary := ToArgs(Resolved{})
	for _, arg := range ordinary {
		if arg == "--manager" {
			t.Fatalf("an ordinary launch rendered --manager: %v", ordinary)
		}
	}
	manager := ToArgs(Resolved{Manager: true})
	found := false
	for _, arg := range manager {
		if arg == "--manager" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a Manager launch did not render --manager: %v", manager)
	}
	// It is a bare boolean, never a key/value pair: a following token would be
	// read as a positional argument by the flag parser.
	for i, arg := range manager {
		if arg == "--manager" && i+1 < len(manager) {
			if next := manager[i+1]; !strings.HasPrefix(next, "--") {
				t.Fatalf("--manager took %q as a value instead of standing alone", next)
			}
		}
	}
}
