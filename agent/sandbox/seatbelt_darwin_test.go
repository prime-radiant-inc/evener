//go:build darwin

package sandbox

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestSeatbeltWrapArgvShape (darwin, runs on paradise-park during make test)
// pins the sandbox-exec invocation shape: the hard-coded /usr/bin path, the -p
// policy, the -D dir params, the -- separator, then the original command.
func TestSeatbeltWrapArgvShape(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode:    ModeWorkspaceWrite,
		Network: true,
		Spawned: AccessScope{Read: ReadAnywhere, WriteRoots: []string{"/work/tree"}},
	}
	argv, err := seatbeltWrap([]string{"/bin/echo", "hi"}, rp, "/serf-session-tmp", "/work/tree")
	if err != nil {
		t.Fatalf("seatbeltWrap: %v", err)
	}
	if argv[0] != "/usr/bin/sandbox-exec" {
		t.Errorf("must exec the hard-coded /usr/bin/sandbox-exec, got %q", argv[0])
	}
	if argv[1] != "-p" {
		t.Errorf("second arg must be -p, got %q", argv[1])
	}
	// The policy is the third arg.
	if !strings.Contains(argv[2], "(deny default)") {
		t.Errorf("policy arg missing the base: %q", argv[2][:min(len(argv[2]), 80)])
	}
	// -D params precede the -- separator.
	sep := slices.Index(argv, "--")
	if sep < 0 {
		t.Fatalf("missing -- separator: %v", argv)
	}
	sawDParam := false
	for _, a := range argv[3:sep] {
		if strings.HasPrefix(a, "-DWRITABLE_ROOT_0=") {
			sawDParam = true
		}
	}
	if !sawDParam {
		t.Errorf("expected a -DWRITABLE_ROOT_0= dir param before --: %v", argv[3:sep])
	}
	// The command after -- is the original argv, unmodified.
	if !slices.Equal(argv[sep+1:], []string{"/bin/echo", "hi"}) {
		t.Errorf("command after -- = %v, want [/bin/echo hi]", argv[sep+1:])
	}
}

// TestConfineSetsSeatbeltDir (darwin, runs on paradise-park during make test)
// pins the Seatbelt half of Confine: sandbox-exec has no chdir flag, so Confine
// must set cmd.Dir to the worktree — otherwise the confined child inherits serf's
// process cwd — AND prepend the sandbox-exec invocation to the argv.
func TestConfineSetsSeatbeltDir(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode:    ModeWorkspaceWrite,
		Network: true,
		Spawned: AccessScope{Read: ReadAnywhere, WriteRoots: []string{"/work/tree"}},
		Git:     GitLayout{WorktreeRoot: "/work/tree"},
	}
	w, err := NewWrapper(rp, "/usr/bin/sandbox-exec", "/serf-session-tmp")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	w.Confine(cmd, "/work/tree")

	if cmd.Dir != "/work/tree" {
		t.Errorf("Confine must set cmd.Dir to the worktree for Seatbelt, got %q", cmd.Dir)
	}
	if cmd.Args[0] != "/usr/bin/sandbox-exec" || cmd.Path != "/usr/bin/sandbox-exec" {
		t.Errorf("Confine must prepend sandbox-exec: args[0]=%q path=%q", cmd.Args[0], cmd.Path)
	}
	sep := slices.Index(cmd.Args, "--")
	if sep < 0 || !slices.Equal(cmd.Args[sep+1:], []string{"/bin/echo", "hi"}) {
		t.Errorf("original command must survive after --: %v", cmd.Args)
	}
}
