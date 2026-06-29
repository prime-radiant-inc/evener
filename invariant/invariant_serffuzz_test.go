//go:build serffuzz

package invariant

import (
	"strings"
	"testing"
)

// Under -tags serffuzz invariants are live: Enabled is true, a satisfied
// invariant is a no-op, and a violated one panics with the formatted message so
// the never-panic fuzz oracle catches it.
func TestFuzzBuildEnforces(t *testing.T) {
	if !Enabled {
		t.Fatal("Enabled must be true under -tags serffuzz")
	}

	// A true condition does not panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Hold(true) panicked: %v", r)
			}
		}()
		Hold(true, "always holds")
	}()

	// A false condition panics, and the message carries the formatted detail so
	// a triaged crasher names the offending value.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Hold(false) did not panic under serffuzz")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is %T, want string", r)
		}
		if !strings.Contains(msg, "invariant violated:") {
			t.Errorf("panic message %q lacks the invariant prefix", msg)
		}
		if !strings.Contains(msg, "status went backwards: terminal -> running") {
			t.Errorf("panic message %q dropped the formatted detail", msg)
		}
	}()
	Hold(false, "status went backwards: %s -> %s", "terminal", "running")
}
