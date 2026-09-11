//go:build unix

package selfupdate

import (
	"fmt"
	"os"
	"syscall"
)

// Restart replaces the current process image with binary, keeping the PID,
// the environment, and args as argv[1:]. Go opens files and sockets with
// O_CLOEXEC, so the hub's flock and listener are released by the exec and
// re-acquired by the new image. It only returns when the exec itself fails.
func Restart(binary string, args []string) error {
	argv := append([]string{binary}, args...)
	if err := syscall.Exec(binary, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", binary, err)
	}
	return nil
}

// RestartSupported reports whether Restart can exec in place on this build.
func RestartSupported() bool { return true }
