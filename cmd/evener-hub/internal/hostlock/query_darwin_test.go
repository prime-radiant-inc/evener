//go:build darwin

package hostlock

import (
	"syscall"
	"testing"
)

const lockQueryTestName = "TestDarwinFlockQuery"

func TestDarwinFlockQuery(t *testing.T)         { runLockQueryProof(t) }
func checkLockOwnerEvidence(*testing.T, string) {}
func logLockQueryPlatform(t *testing.T, path string) {
	t.Helper()
	kernel, err := syscall.Sysctl("kern.version")
	if err != nil {
		t.Fatal(err)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		t.Fatal(err)
	}
	var fsType []byte
	for _, c := range fs.Fstypename {
		if c == 0 {
			break
		}
		fsType = append(fsType, byte(c))
	}
	t.Logf("kernel=%s temp_filesystem=%s", kernel, fsType)
	t.Log("Evidence applies only to this kernel and temp filesystem, not macOS network filesystems")
}
