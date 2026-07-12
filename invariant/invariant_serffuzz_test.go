//go:build serffuzz

package invariant

import (
	"fmt"
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

// FuzzInvariantHold checks both sides of the fuzz-build contract: satisfied
// invariants are inert, while violations panic with the exact formatted detail.
func FuzzInvariantHold(f *testing.F) {
	f.Add(true, "status: %s / %d", "running", int64(7))
	f.Add(false, "status: %s / %d", "terminal", int64(-1))
	f.Add(false, "%[2]d:%[1]s", "job", int64(42))

	f.Fuzz(func(t *testing.T, cond bool, format, detail string, number int64) {
		want := "invariant violated: " + fmt.Sprintf(format, detail, number)
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			Hold(cond, format, detail, number)
		}()

		if cond {
			if recovered != nil {
				t.Fatalf("Hold(true) panicked: %v", recovered)
			}
			return
		}

		got, ok := recovered.(string)
		if !ok {
			t.Fatalf("Hold(false) panic = %#v (%T), want string %q", recovered, recovered, want)
		}
		if got != want {
			t.Fatalf("Hold(false) panic = %q, want %q", got, want)
		}
	})
}
