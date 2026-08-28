//go:build !linux && !darwin

package execenv

import "os"

// Non-Unix platforms do not expose a portable wait-status signal name through
// os.ProcessState. The shell outcome still reports a negative exit code as a
// signal termination; the platform-specific name is omitted when unavailable.
func processSignalName(*os.ProcessState) string { return "" }
