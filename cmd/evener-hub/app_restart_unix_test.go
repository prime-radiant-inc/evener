//go:build !windows

package hub

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestHubUpdateCheckRefusesNonRegularLaunchPath(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateAvailable(t)
	path := filepath.Join(t.TempDir(), "evener")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = path
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	done := make(chan appwire.UpdateCheckResponse, 1)
	go func() {
		resp, _ := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
		done <- resp
	}()
	select {
	case resp := <-done:
		if resp.OnDiskError == "" {
			t.Fatal("check accepted a FIFO launch path")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("check blocked on a FIFO launch path")
	}
}

func TestHubRestartRefusesNonRegularLaunchPath(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })
	path := filepath.Join(t.TempDir(), "evener")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = path
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	done := make(chan error, 1)
	go func() {
		_, err := hubRestart(context.Background(), appwire.HubRestartParams{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("restart accepted a FIFO launch path")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart blocked on a FIFO launch path")
	}
}
