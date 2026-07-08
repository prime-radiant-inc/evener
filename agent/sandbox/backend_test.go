package sandbox

import (
	"slices"
	"testing"
)

func TestNewWrapperRejectsNonEnforcingBackend(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendNone}, "/usr/bin/bwrap", "/tmp/s"); err == nil {
		t.Error("expected NewWrapper to refuse the non-enforcing BackendNone")
	}
}

func TestNewWrapperAcceptsSeatbeltBackend(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendSeatbelt}, "/usr/bin/sandbox-exec", "/tmp/s"); err != nil {
		t.Errorf("NewWrapper must accept the seatbelt backend: %v", err)
	}
}

func TestNewWrapperRejectsRelativeBinaryPath(t *testing.T) {
	for _, b := range []Backend{BackendBwrap, BackendSeatbelt} {
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "bwrap", "/tmp/s"); err == nil {
			t.Fatalf("backend %v: expected NewWrapper to refuse a cwd-relative binary path (PATH-injection defense)", b)
		}
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "./bin/sandbox-exec", "/tmp/s"); err == nil {
			t.Fatalf("backend %v: expected NewWrapper to refuse a cwd-relative binary path (PATH-injection defense)", b)
		}
	}
}

func TestWrapPrependsBwrapAndSeparatesCommand(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/serf-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	argv := []string{"/bin/bash", "-c", "echo hi"}
	got := w.Wrap(argv, cwd)

	if got[0] != "/usr/bin/bwrap" {
		t.Errorf("wrapped argv must start with the bwrap binary, got %q", got[0])
	}
	// --argv0 preserves the command's own argv[0].
	if !hasSeq(got, "--argv0", "/bin/bash") {
		t.Errorf("expected --argv0 /bin/bash: %v", got)
	}
	// Everything after the LAST "--" is the original command, unmodified.
	sep := slices.Index(got, "--")
	if sep < 0 {
		t.Fatalf("wrapped argv missing the -- command separator: %v", got)
	}
	if !slices.Equal(got[sep+1:], argv) {
		t.Errorf("command after -- = %v, want %v", got[sep+1:], argv)
	}
	// The bwrap flags carry the confinement.
	if !hasSeq(got, "--unshare-pid") || !hasSeq(got, "--proc", "/proc") {
		t.Errorf("wrapped argv missing confinement flags: %v", got)
	}
}

func TestWrapNilIsIdentity(t *testing.T) {
	var w *Wrapper
	argv := []string{"/bin/echo", "hi"}
	if got := w.Wrap(argv, "/somewhere"); !slices.Equal(got, argv) {
		t.Errorf("nil wrapper must be identity, got %v", got)
	}
}
