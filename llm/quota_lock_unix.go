//go:build darwin || linux

package llm

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// quotaLockPoll bounds how long a contended lock holder can delay a caller
// before the caller's own context gets a chance to end the wait.
const quotaLockPoll = 10 * time.Millisecond

// lockQuotaFile takes an exclusive advisory lock on the quota state's guard
// file. It polls without blocking so a context deadline — the run's real
// budget — can end the wait even if another process is stopped while holding
// the lock.
func lockQuotaFile(ctx context.Context, f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(quotaLockPoll):
			}
		default:
			return err
		}
	}
}

func unlockQuotaFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
