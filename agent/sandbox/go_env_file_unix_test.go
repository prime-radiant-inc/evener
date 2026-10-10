//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// GOENV can name anything, and the probe reads it before any sandbox exists, so
// the host read takes only a bounded regular file: a FIFO would block session
// start and /dev/zero would never end.
func TestHostProbeReadsOnlyABoundedRegularGoEnvFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO support: %v", err)
	}
	huge := filepath.Join(dir, "huge")
	if err := os.WriteFile(huge, []byte(strings.Repeat("x", goEnvFileLimit+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(dir, "env")
	if err := os.WriteFile(small, []byte("GOPATH=/from/file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, "/dev/zero", dir, huge} {
		done := make(chan error, 1)
		go func() { _, err := (hostProbeSystem{}).readFile(path); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("readFile(%q) must refuse anything but a small regular file", path)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("readFile(%q) blocked", path)
		}
	}
	if data, err := (hostProbeSystem{}).readFile(small); err != nil || string(data) != "GOPATH=/from/file\n" {
		t.Errorf("readFile of a small regular file = %q, %v", data, err)
	}
}
