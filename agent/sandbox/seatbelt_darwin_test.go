//go:build darwin

package sandbox

import (
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
