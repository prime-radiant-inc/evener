package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestNewWrapperProvisionsExportedSubtrees pins the wrapper-boundary half of issue
// #495: the wrapper's writable bind/grant roots are the container's two exported
// subtrees, and the environment floor names them as $TMPDIR and
// $EVENER_SCRATCH_DIR, so a container directory that exists but was not laid out by
// NewSessionScratch must still come out with both, rather than a sandbox whose
// exported temp/scratch paths do not exist.
func TestNewWrapperProvisionsExportedSubtrees(t *testing.T) {
	// An Evener scratch directory handed to the wrapper is settled at the live mode:
	// traversable by everyone so the exported subtrees are reachable, writable by
	// nobody so no artifact can be dropped beside them.
	dir := filepath.Join(t.TempDir(), sessionScratchPrefix+"wrapped")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, sessionScratchSetupMode) })
	rp := ResolvedPolicy{Mode: ModeWorkspaceWrite, Backend: BackendBwrap}
	if _, err := NewWrapper(rp, "/usr/bin/bwrap", dir); err != nil {
		t.Fatalf("NewWrapper must provision an existing container: %v", err)
	}
	containerInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if containerInfo.Mode().Perm()&0o001 == 0 {
		t.Fatalf("wrapper container mode = %04o, must be traversable by other users", containerInfo.Mode().Perm())
	}
	if containerInfo.Mode().Perm()&0o066 != 0 {
		t.Fatalf("wrapper container mode = %04o, must not become group/other readable or writable", containerInfo.Mode().Perm())
	}
	if containerInfo.Mode().Perm()&0o200 != 0 {
		t.Fatalf("wrapper container mode = %04o, must not be writable, even by its owner", containerInfo.Mode().Perm())
	}
	for _, sub := range []struct {
		path string
		mode os.FileMode
	}{
		{SessionScratchPrivateDir(dir), sessionScratchPrivateMode},
		{SessionScratchTmpDir(dir), sessionScratchTmpMode},
	} {
		info, err := os.Lstat(sub.path)
		if err != nil || !info.IsDir() {
			t.Fatalf("the wrapper must provision %q so $TMPDIR/$EVENER_SCRATCH_DIR exist: info=%v err=%v", sub.path, info, err)
		}
		if info.Mode().Perm() != sub.mode.Perm() {
			t.Fatalf("%q mode = %04o, want %04o", sub.path, info.Mode().Perm(), sub.mode.Perm())
		}
		if got, want := info.Mode()&os.ModeSticky != 0, sub.mode&os.ModeSticky != 0; got != want {
			t.Fatalf("%q sticky = %v, want %v", sub.path, got, want)
		}
	}
}

// TestNewWrapperRefusesOccupiedSubtreeName: the wrapper runs on a directory a
// caller owns, so a non-directory entry at a subtree name is refused and never
// unlinked — the same data-preservation rule the scratch migration follows.
func TestNewWrapperRefusesOccupiedSubtreeName(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, sessionScratchTmpName)
	if err := os.WriteFile(occupied, []byte("caller data"), 0o600); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{Mode: ModeWorkspaceWrite, Backend: BackendBwrap}
	if _, err := NewWrapper(rp, "/usr/bin/bwrap", dir); err == nil {
		t.Fatal("NewWrapper must refuse a container whose subtree name is occupied by a non-directory")
	}
	if data, err := os.ReadFile(occupied); err != nil || string(data) != "caller data" {
		t.Fatalf("NewWrapper must not unlink the caller's entry: %q err %v", data, err)
	}
}

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
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
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

// TestConfineWrapsBwrapAndLeavesDir pins that Confine rewrites the command's argv
// with the backend invocation but does NOT touch cmd.Dir for bwrap — bwrap carries
// the working directory in its own argv (--chdir), so the caller's cmd.Dir is left
// as-is. (The Seatbelt cmd.Dir path is exercised on darwin by TestConfineSetsSeatbeltDir.)
func TestConfineWrapsBwrapAndLeavesDir(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	orig := slices.Clone(cmd.Args)
	w.Confine(cmd, cwd)

	if cmd.Args[0] != "/usr/bin/bwrap" || cmd.Path != "/usr/bin/bwrap" {
		t.Errorf("Confine must prepend the bwrap binary: args[0]=%q path=%q", cmd.Args[0], cmd.Path)
	}
	sep := slices.Index(cmd.Args, "--")
	if sep < 0 || !slices.Equal(cmd.Args[sep+1:], orig) {
		t.Errorf("original command must survive after --: %v", cmd.Args)
	}
	if cmd.Dir != "" {
		t.Errorf("Confine must not set cmd.Dir for bwrap (uses --chdir), got %q", cmd.Dir)
	}
}

// TestConfineNilIsIdentity pins that a nil wrapper leaves the command untouched,
// so an unsandboxed spawn is byte-identical to before.
func TestConfineNilIsIdentity(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(cmd.Args)
	w.Confine(cmd, "/somewhere")
	if !slices.Equal(cmd.Args, want) || cmd.Dir != "" {
		t.Errorf("nil wrapper must be identity, got args=%v dir=%q", cmd.Args, cmd.Dir)
	}
}

// TestConfineTrustedInfraKeepsNetworkUnderNetOff pins kata 83pm: MCP servers are
// trusted infrastructure (launched only from config layers the model cannot
// write), so under a net=off session policy Confine still severs the network
// (--unshare-net) for an ordinary spawned process, but ConfineTrustedInfra does
// NOT — while both retain identical filesystem confinement (the base hardening
// flags and the worktree bind survive either path).
func TestConfineTrustedInfraKeepsNetworkUnderNetOff(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, false)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	ordinary := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	w.Confine(ordinary, cwd)
	if !slices.Contains(ordinary.Args, "--unshare-net") {
		t.Errorf("model-authored spawn must be network-severed under net=off: %v", ordinary.Args)
	}

	infra := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	w.ConfineTrustedInfra(infra, cwd)
	if slices.Contains(infra.Args, "--unshare-net") {
		t.Errorf("trusted-infrastructure spawn (MCP) must keep network under net=off: %v", infra.Args)
	}
	// Filesystem confinement is unchanged by the network carve-out.
	if !slices.Contains(infra.Args, "--unshare-pid") || !hasSeq(infra.Args, "--bind", cwd, cwd) {
		t.Errorf("trusted-infrastructure spawn must keep filesystem confinement: %v", infra.Args)
	}
}

// TestConfineTrustedInfraNilIsIdentity mirrors TestConfineNilIsIdentity for the
// trusted-infra entry point.
func TestConfineTrustedInfraNilIsIdentity(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(cmd.Args)
	w.ConfineTrustedInfra(cmd, "/somewhere")
	if !slices.Equal(cmd.Args, want) || cmd.Dir != "" {
		t.Errorf("nil wrapper must be identity, got args=%v dir=%q", cmd.Args, cmd.Dir)
	}
}
