//go:build !unix

package dev

import "os/exec"

// Windows has no POSIX process groups, and this helper is repo tooling that
// runs on the unix dev and CI hosts; the build only has to hold together for
// the cross-compile vet. The bound still stops the process it started, which
// is all a platform without groups can offer.
func isolateProcessGroup(*exec.Cmd) bool { return false }

func stopProcessGroup(cmd *exec.Cmd, _ bool) { _ = cmd.Process.Kill() }
