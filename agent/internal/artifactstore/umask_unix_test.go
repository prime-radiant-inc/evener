//go:build unix

package artifactstore

import "syscall"

// relaxUmask restores a permissive umask inside the restrictive-umask helper
// process.
//
// The helper runs the whole test binary under `umask 0777`, and a coverage-
// instrumented binary writes its counter and meta-data files during exit —
// AFTER the test function returns. Those writes inherit the process umask, so
// they land as mode 000 and the runtime fails with "error generating coverage
// report: ... permission denied", failing the outer test even though the
// assertions passed. The suite is green under `go test` and red only under
// `-coverprofile`, which is a bad way to find out.
//
// Relaxing the umask once the mode assertion has been made keeps the test
// honest — the store still creates its file under 0777 — while leaving the
// exiting runtime able to write.
func relaxUmask() { syscall.Umask(0o022) }
