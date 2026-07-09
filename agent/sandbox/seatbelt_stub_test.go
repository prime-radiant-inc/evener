//go:build !darwin

package sandbox

import (
	"errors"
	"testing"
)

// TestSeatbeltStubRefuses pins that on a non-darwin host the seatbelt wrap stub
// refuses rather than returning an unconfined argv.
func TestSeatbeltStubRefuses(t *testing.T) {
	t.Parallel()
	argv, err := seatbeltWrap([]string{"/bin/echo", "hi"}, ResolvedPolicy{Backend: BackendSeatbelt}, "/tmp/s", "/work")
	if !errors.Is(err, errSeatbeltUnavailable) {
		t.Errorf("stub must return errSeatbeltUnavailable, got %v", err)
	}
	if argv != nil {
		t.Errorf("stub must return a nil argv, got %v", argv)
	}
}

// TestWrapSeatbeltPanicsOnNonDarwin proves the fail-closed guard: if a seatbelt
// policy somehow reaches the kernel wrapper on a host that cannot enforce it (an
// impossible state — the resolver only selects seatbelt on darwin), Wrap panics
// rather than returning the command UNCONFINED.
func TestWrapSeatbeltPanicsOnNonDarwin(t *testing.T) {
	t.Parallel()
	w, err := NewWrapper(ResolvedPolicy{Backend: BackendSeatbelt}, "/usr/bin/sandbox-exec", "/tmp/s")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("Wrap must panic (fail closed) for a seatbelt policy on a non-darwin host")
		}
	}()
	_ = w.Wrap([]string{"/bin/echo", "hi"}, "/work")
}
