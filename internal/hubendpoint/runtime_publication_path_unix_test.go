//go:build unix

package hubendpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublishSymlinkDotdotUsesDestinationParent(t *testing.T) {
	for _, crossDevice := range []bool{false, true} {
		name := "raw-parent-absent"
		if crossDevice {
			name = "raw-parent-other-device"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			launch := filepath.Join(root, "launch")
			physical := filepath.Join(root, "physical")
			if crossDevice {
				var err error
				physical, err = os.MkdirTemp("/dev/shm", "evener-endpoint-publication-")
				if err != nil {
					t.Skipf("cross-device temp fixture unavailable: %v", err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(physical) })
				a, err := os.Stat(root)
				if err != nil {
					t.Fatal(err)
				}
				b, err := os.Stat(physical)
				if err != nil {
					t.Fatal(err)
				}
				if a.Sys().(*syscall.Stat_t).Dev == b.Sys().(*syscall.Stat_t).Dev {
					t.Skip("fixture paths are on the same device")
				}
			}
			for _, dir := range []string{filepath.Join(launch, "state"), filepath.Join(physical, "inner")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if crossDevice {
				if err := os.Mkdir(filepath.Join(physical, "state"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(physical, "inner"), filepath.Join(launch, "link")); err != nil {
				t.Fatal(err)
			}
			// The existing destination grammar is lexical Join, unlike opening
			// the raw root. Declare the intended directory independently.
			stateRoot := launch + "/link/../state"
			destination := filepath.Join(launch, "state", "hub-endpoint.json")
			r := testRecord(stateRoot)
			remove, err := Publish(stateRoot, r)
			if err != nil {
				t.Fatalf("publication must stage and sync beside %q, not raw root %q: %v", destination, stateRoot, err)
			}
			info, err := os.Stat(destination)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("private publication at intended destination: info=%v err=%v", info, err)
			}
			next := r
			next.BootID = "boot-two"
			retire, err := Publish(stateRoot, next)
			if err != nil {
				t.Fatal(err)
			}
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			got, err := Read(stateRoot)
			if err != nil || got.BootID != next.BootID || got.HubStateRoot != stateRoot {
				t.Fatalf("atomic replacement/root intent: got=%+v err=%v", got, err)
			}
			if err := retire(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retirement left destination: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(launch, "state"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("publication left temp files: entries=%v err=%v", entries, err)
			}
			if !crossDevice {
				if _, err := os.Lstat(filepath.Join(physical, "state")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("publication created the raw physical root: %v", err)
				}
			} else {
				entries, err := os.ReadDir(filepath.Join(physical, "state"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("publication wrote outside destination device: entries=%v err=%v", entries, err)
				}
			}
		})
	}
}
