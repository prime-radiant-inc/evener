package sandbox

import (
	"slices"
	"testing"
)

func TestNewWrapperRejectsNonBwrapBackend(t *testing.T) {
	for _, b := range []Backend{BackendNone, BackendSeatbelt} {
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "/usr/bin/bwrap", "/tmp/s"); err == nil {
			t.Errorf("backend %v: expected NewWrapper to refuse a non-bwrap backend", b)
		}
	}
}

func TestNewWrapperRejectsRelativeBwrapPath(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendBwrap}, "bwrap", "/tmp/s"); err == nil {
		t.Fatal("expected NewWrapper to refuse a cwd-relative bwrap path (PATH-injection defense)")
	}
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendBwrap}, "./bin/bwrap", "/tmp/s"); err == nil {
		t.Fatal("expected NewWrapper to refuse a cwd-relative bwrap path (PATH-injection defense)")
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
