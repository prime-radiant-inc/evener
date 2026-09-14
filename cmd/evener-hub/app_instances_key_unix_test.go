//go:build unix

package hub

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The write side keeps the read side's O_NOFOLLOW discipline: a symlink planted
// at the key path is never followed, so its target is not read as the hub's key
// and not written through. The repair replaces the link itself with a fresh
// 0600 key, atomically. (On this platform the old plain O_CREATE|O_EXCL create
// also refused the link with EEXIST and removed it afterwards; what the change
// adds is that no window exists in which the path holds no key.)
func TestInstances_EndpointFingerprintReplacesASymlinkKeyFile(t *testing.T) {
	newWork := func(t *testing.T) *instancesFixture {
		t.Helper()
		f := newInstancesFixture(t, nil)
		if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return f
	}
	assertReplacedByAKey := func(t *testing.T, f *instancesFixture) {
		t.Helper()
		keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
		info, err := os.Lstat(keyPath)
		if err != nil {
			t.Fatalf("Lstat(%s): %v", keyPath, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("key path mode = %v, want the link replaced by a regular file", info.Mode())
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("replacement key is %04o, want 0600", perm)
		}
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", keyPath, err)
		}
		if len(raw) == 0 {
			t.Fatal("the replacement key is empty")
		}
		temps, err := filepath.Glob(filepath.Join(f.stateDir, ".*.tmp"))
		if err != nil {
			t.Fatalf("Glob: %v", err)
		}
		if len(temps) != 0 {
			t.Fatalf("temp files left behind by the publish: %v", temps)
		}
	}

	t.Run("dangling link", func(t *testing.T) {
		f := newWork(t)
		target := filepath.Join(f.stateDir, "missing-target")
		keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
		if err := os.Symlink(target, keyPath); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		got := entry(t, f.ctl.List(), "work").EndpointFingerprint
		if got == "" {
			t.Fatal("a symlinked key path left the hub serving no fingerprint")
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%s) = %v, want the dangling target left uncreated", target, err)
		}
		assertReplacedByAKey(t, f)
		if again := entry(t, f.ctl.List(), "work").EndpointFingerprint; again != got {
			t.Fatalf("the fingerprint moved between listings: %q then %q", got, again)
		}
	})

	t.Run("link to another file", func(t *testing.T) {
		f := newWork(t)
		const targetKey = "a-key-an-operator-pointed-the-link-at"
		target := filepath.Join(f.stateDir, "operator-key")
		if err := os.WriteFile(target, []byte(targetKey), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", target, err)
		}
		keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
		if err := os.Symlink(target, keyPath); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got == "" {
			t.Fatal("a symlinked key path left the hub serving no fingerprint")
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", target, err)
		}
		if string(raw) != targetKey {
			t.Fatalf("the link target is now %q, want it untouched: the link must not be written through", raw)
		}
		key, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", keyPath, err)
		}
		if string(key) == targetKey {
			t.Fatal("the link's target content was adopted as the hub's key")
		}
		assertReplacedByAKey(t, f)
	})
}

// A usable key is never replaced: the repair returns the key already at the
// path, byte for byte, so a hub that finds another hub's key keeps keying the
// digests that hub served - two hubs must not each key their own - and leaves
// no temp file behind.
func TestInstances_EndpointFingerprintRepairReusesAUsableKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, endpointFingerprintKeyFile)
	pinned := []byte("a-key-another-hub-already-published")
	if err := os.WriteFile(path, pinned, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}

	first, err := repairEndpointFingerprintKey(path)
	if err != nil {
		t.Fatalf("repairEndpointFingerprintKey: %v", err)
	}
	if !bytes.Equal(first, pinned) {
		t.Fatalf("repair returned %q, want the usable key already at the path", first)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(raw, pinned) {
		t.Fatalf("repair replaced a usable key: %q", raw)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("temp files left behind by the publish: %v", temps)
	}
}

const (
	// fingerprintWriteFaultHelperVar gates the re-executed helper below.
	fingerprintWriteFaultHelperVar = "EVENER_HUB_TEST_FINGERPRINT_WRITE_FAULT_HELPER"
	// fingerprintWriteFaultHelperPath is the key path the helper repairs.
	fingerprintWriteFaultHelperPath = "EVENER_HUB_TEST_FINGERPRINT_WRITE_FAULT_PATH"
	// fingerprintWriteFaultLimit is an RLIMIT_FSIZE smaller than the 43-byte
	// key the hub generates, so the write that lands a key fails partway.
	fingerprintWriteFaultLimit = 16
)

// A write that fails partway - a full filesystem, a quota, a size limit - must
// not leave a partial key at the key path. Any nonempty file is a key to
// readEndpointFingerprintKey, so a truncated leftover would key every digest
// with low-entropy material until someone noticed, which is the shape the old
// write-in-place repair produced. The key is written to a temp file and only a
// completed, synced one is published, so a failed write leaves the path as it
// was.
//
// The failing write is produced in a re-executed copy of this binary: the
// helper lowers RLIMIT_FSIZE below the key length before calling the repair, so
// the write fails deterministically rather than by filling a real disk.
func TestInstances_EndpointFingerprintFailedWriteLeavesNoPartialKey(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, endpointFingerprintKeyFile)

	cmd := exec.Command(exe, "-test.run=^TestEndpointFingerprintWriteFaultHelper$")
	cmd.Env = append(os.Environ(),
		fingerprintWriteFaultHelperVar+"=1",
		fingerprintWriteFaultHelperPath+"="+path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the size-limited repair did not fail the way the helper needs: %v\n%s", err, out)
	}

	if info, statErr := os.Lstat(path); statErr == nil {
		t.Fatalf("a failed key write left %s (%d bytes) at the key path; want no partial key published", info.Mode(), info.Size())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Lstat(%s): %v", path, statErr)
	}
	temps, globErr := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if globErr != nil {
		t.Fatalf("Glob: %v", globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("the failed publish left temp files behind: %v", temps)
	}
}

// TestEndpointFingerprintWriteFaultHelper is the re-executed half of
// TestInstances_EndpointFingerprintFailedWriteLeavesNoPartialKey: it caps this
// process's file size below the key length, ignores the SIGXFSZ the kernel
// raises for the oversized write, and requires the repair to report the
// failure. The parent asserts what the failure left behind.
func TestEndpointFingerprintWriteFaultHelper(t *testing.T) {
	if os.Getenv(fingerprintWriteFaultHelperVar) == "" {
		t.Skip("re-executed helper for TestInstances_EndpointFingerprintFailedWriteLeavesNoPartialKey")
	}
	path := os.Getenv(fingerprintWriteFaultHelperPath)
	if path == "" {
		t.Fatal("the helper was started without a key path")
	}
	signal.Ignore(syscall.SIGXFSZ)
	limit := &syscall.Rlimit{Cur: fingerprintWriteFaultLimit, Max: fingerprintWriteFaultLimit}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, limit); err != nil {
		t.Fatalf("Setrlimit(RLIMIT_FSIZE): %v", err)
	}
	if _, err := repairEndpointFingerprintKey(path); err == nil {
		t.Fatal("the size-limited repair succeeded, so this helper cannot pin the partial-write behavior")
	}
}
