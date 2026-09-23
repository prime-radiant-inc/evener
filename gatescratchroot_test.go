package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateScratchRootLib picks the directory the gate mints its scratch under:
// a RAM-backed candidate when it is usable, the ambient TMPDIR otherwise.
const gateScratchRootLib = "scripts/lib/gate-scratch-root.sh"

// gateScratchRoot sources the library in a minimal environment and returns what
// gate_scratch_root prints for candidate and minKB, with TMPDIR set to tmpdir.
func gateScratchRoot(t *testing.T, tmpdir, candidate, minKB string) string {
	t.Helper()
	if _, err := os.Stat(gateScratchRootLib); err != nil {
		t.Fatalf("stat %s: %v", gateScratchRootLib, err)
	}
	cmd := exec.Command("sh", "-c", `. "$1" && gate_scratch_root "$2" "$3"`, "sh", gateScratchRootLib, candidate, minKB)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TMPDIR=" + tmpdir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate_scratch_root %q %q: %v\n%s", candidate, minKB, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGateScratchRootUsesAUsableCandidate(t *testing.T) {
	ambient, candidate := t.TempDir(), t.TempDir()
	if got := gateScratchRoot(t, ambient, candidate, "1"); got != candidate {
		t.Fatalf("gate_scratch_root = %q, want the candidate %q", got, candidate)
	}
	// The exec probe cleans up after itself: the gate mints its scratch here.
	if entries, err := os.ReadDir(candidate); err != nil || len(entries) != 0 {
		t.Fatalf("candidate after the probe holds %v (err %v), want it empty", entries, err)
	}
}

func TestGateScratchRootFallsBackToTMPDIR(t *testing.T) {
	ambient := t.TempDir()
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]string{
		// macOS and most non-Linux hosts have no /dev/shm at all.
		"missing":    filepath.Join(t.TempDir(), "absent"),
		"unwritable": readOnly,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "unwritable" && os.Getuid() == 0 {
				t.Skip("root writes through 0500 directories")
			}
			if got := gateScratchRoot(t, ambient, candidate, "1"); got != ambient {
				t.Fatalf("gate_scratch_root = %q, want the ambient TMPDIR %q", got, ambient)
			}
		})
	}
	// A container's default /dev/shm is 64MB; filling it would turn into
	// ENOSPC failures in whatever test happened to write next.
	t.Run("too small", func(t *testing.T) {
		if got := gateScratchRoot(t, ambient, t.TempDir(), "999999999999"); got != ambient {
			t.Fatalf("gate_scratch_root = %q, want the ambient TMPDIR %q", got, ambient)
		}
	})
}

func TestGateScratchRootDefaultsToSlashTmpWithoutTMPDIR(t *testing.T) {
	if got := gateScratchRoot(t, "", filepath.Join(t.TempDir(), "absent"), "1"); got != "/tmp" {
		t.Fatalf("gate_scratch_root = %q, want /tmp", got)
	}
}

// TestGateScratchRootRefusesANoexecCandidate pins the check Docker's default
// /dev/shm needs: it is mounted noexec, and go test runs the binaries it builds
// from TMPDIR, so a writable, roomy but noexec candidate must fall back too.
// The candidate is a real noexec tmpfs, mounted inside an unprivileged user
// and mount namespace; hosts that do not allow one skip.
func TestGateScratchRootRefusesANoexecCandidate(t *testing.T) {
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not available")
	}
	ambient, candidate := t.TempDir(), t.TempDir()
	// Probe the whole capability the test needs, a tmpfs mount inside the
	// namespace, so a host that allows the namespace but blocks mount skips.
	probe := exec.Command("unshare", "-rm", "sh", "-c", `mount -t tmpfs tmpfs "$1" && umount "$1"`, "sh", t.TempDir())
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("cannot mount a tmpfs in an unprivileged user+mount namespace: %v: %s", err, out)
	}
	script := `mount -t tmpfs -o noexec tmpfs "$1" && . "$2" && gate_scratch_root "$1" 1`
	cmd := exec.Command("unshare", "-rm", "sh", "-c", script, "sh", candidate, gateScratchRootLib)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TMPDIR=" + ambient}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate_scratch_root on a noexec tmpfs: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != ambient {
		t.Fatalf("gate_scratch_root chose %q for a noexec candidate, want the ambient TMPDIR %q", got, ambient)
	}
}
