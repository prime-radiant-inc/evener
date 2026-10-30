//go:build linux

package hostlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const lockQueryTestName = "TestLinuxLockQuery"

func TestLinuxLockQuery(t *testing.T) { runLockQueryProof(t) }
func logLockQueryPlatform(t *testing.T, path string) {
	t.Helper()
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		t.Fatal(err)
	}
	t.Logf("temp_filesystem_type=%#x, runtime evidence is scoped to this filesystem", fs.Type)
}

func checkLockOwnerEvidence(t *testing.T, path string) {
	t.Helper()
	// Read only this process's descriptor evidence, never ambient /proc/locks.
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if err != nil || target != path {
			continue
		}
		data, err := os.ReadFile("/proc/self/fdinfo/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for line := range strings.SplitSeq(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 9 && fields[0] == "lock:" && fields[2] == "FLOCK" && fields[3] == "ADVISORY" && fields[4] == "WRITE" && fields[5] == strconv.Itoa(os.Getpid()) && fields[len(fields)-2] == "0" && fields[len(fields)-1] == "EOF" {
				found = true
			}
		}
		if !found {
			t.Fatalf("numeric FLOCK owner evidence missing from own fdinfo: %s", data)
		}
		return
	}
	t.Fatal("owned lock descriptor not found")
}

func TestLinuxMarkerFailureClosesFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(file.Fd(), unix.F_OFD_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireLock(path)
	if release != nil {
		release()
		t.Fatal("contender acquired marker")
	}
	if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
		t.Fatalf("second-stage error: %v", err)
	}
	control, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := unix.Flock(int(control.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("failed contender leaked first-stage flock: %v", err)
	}
	if held, err := IsHeld(path); err != nil || !held {
		t.Fatalf("failed contender removed real marker: held=%v err=%v", held, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if held, err := IsHeld(path); err != nil || held {
		t.Fatalf("marker not released: held=%v err=%v", held, err)
	}
}

func TestLinuxQuerySameDescriptionAndError(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := lockMarker(file); err != nil {
		t.Fatal(err)
	}
	if held, err := flockIsHeld(file); err != nil || !held {
		t.Fatalf("same-description marker hidden: held=%v err=%v", held, err)
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(file.Fd(), unix.F_GETLK, &lock); err != nil || lock.Pid != -1 {
		t.Fatalf("marker query: %+v err=%v", lock, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := flockIsHeld(file); !errors.Is(err, unix.EBADF) {
		t.Fatal(fmt.Errorf("query error lost: %w", err))
	}
}
