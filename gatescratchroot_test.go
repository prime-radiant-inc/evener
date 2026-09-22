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
}

func TestGateScratchRootFallsBackToTMPDIR(t *testing.T) {
	ambient := t.TempDir()
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("root writes through 0500 directories")
	}
	for name, candidate := range map[string]string{
		// macOS and most non-Linux hosts have no /dev/shm at all.
		"missing":    filepath.Join(t.TempDir(), "absent"),
		"unwritable": readOnly,
	} {
		t.Run(name, func(t *testing.T) {
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
