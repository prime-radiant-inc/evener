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

// openInstallLockFile opens the lock file; a var so tests can inject
// filesystem errors (a read-only mount) the local filesystem cannot produce.
var openInstallLockFile = os.OpenFile

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
		if errors.Is(err, os.ErrPermission) || errors.Is(err, unix.EROFS) {
			return nil, fmt.Errorf("%w: mkdir install lock parent: %w", ErrInstallDirReadOnly, err)
		}
		return nil, fmt.Errorf("mkdir install lock parent: %w", err)
	}
	lockPath := filepath.Join(shareBinDir, ".install.lock")
	f, err := openInstallLockFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil && (errors.Is(err, os.ErrPermission) || errors.Is(err, unix.EROFS)) {
		// A root-owned or read-only installation cannot create or write the
		// lock file, but flock works on a read-only descriptor when the file
		// already exists.
		f, err = openInstallLockFile(lockPath, os.O_RDONLY, 0)
		if err != nil && errors.Is(err, os.ErrNotExist) {
			// No lock file exists and none can be created: no other
			// installer can hold one either, so the restart may proceed on
			// its digest re-verify. An existing unreadable lock falls
			// through to an ordinary error and aborts the restart.
			return nil, fmt.Errorf("%w: open install lock: %w", ErrInstallDirReadOnly, err)
		}
	}
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
