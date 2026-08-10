//go:build !linux && !darwin

package execenv

import (
	"os"
	"time"
)

func waitForStreamPipeClose(*os.File, time.Duration) (bool, bool, error) {
	return false, false, nil
}
