//go:build unix

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// installLockPollInterval paces ctx-aware lock acquisition: non-blocking
// try + sleep keeps the wait cancellable without a signalfd/kqueue.
const installLockPollInterval = 50 * time.Millisecond

// acquireInstallLock takes an exclusive flock on a lock file inside
// shareBinDir, serializing concurrent installs into one prefix across
// processes (hub self-update vs `evener upgrade` vs another hub).
// hubUpdateMu only covers threads of one process. The lock file lives in
// the managed dir so it moves with custom EVENER_SHARE_BINDIR layouts.
func acquireInstallLock(shareBinDir string) (release func(), err error) {
	return acquireInstallLockCtx(context.Background(), shareBinDir)
}

// acquireInstallLockCtx is acquireInstallLock with ctx-aware waiting: a
// blocking LOCK_EX would ignore the upgrade's overall deadline and strand
// the RPC past every timeout while another holder stalls. LOCK_NB polls
// observe cancellation and return ctx.Err instead.
func acquireInstallLockCtx(ctx context.Context, shareBinDir string) (release func(), err error) {
	if err := os.MkdirAll(shareBinDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir install lock parent: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(shareBinDir, ".install.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open install lock: %w", err)
	}
	for {
		flockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if flockErr == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		// Only contention retries: a permission error, bad fd, or
		// anything else fails fast instead of spinning to the deadline.
		if !errors.Is(flockErr, unix.EWOULDBLOCK) && !errors.Is(flockErr, unix.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("flock install lock: %w", flockErr)
		}
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(installLockPollInterval):
		}
	}
}
