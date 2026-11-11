//go:build !darwin && !linux

package llm

import (
	"context"
	"errors"
	"os"
)

// This platform has no flock(2). The error keeps the lock attempt failing
// closed here, and QuotaCoordinator.Wait degrades to the caller's own cooldown
// so a model call never depends on cross-process coordination being available.
func lockQuotaFile(context.Context, *os.File) error {
	return errors.New("quota coordination requires flock")
}

func unlockQuotaFile(*os.File) error { return nil }
