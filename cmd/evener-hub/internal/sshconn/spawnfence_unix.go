//go:build linux || darwin

package sshconn

// The platform arm of §3's local process boundary: agent/execenv owns the
// kernel half (the cgroup on Linux, the launcher's (pgid, session id) pair on
// Darwin), and this adapter is the one place its identity and handle are mapped
// onto the fence's neutral surface.

import (
	"fmt"
	"syscall"

	"primeradiant.com/evener/agent/execenv"
)

// defaultSpawnBoundary pre-creates this platform's boundary. An empty root
// means execenv's own delegated subtree on Linux (the boundary never lands at
// the mount root); Darwin ignores root because the pair is the launcher's own.
func defaultSpawnBoundary(root string) (SpawnBoundary, error) {
	boundary, err := execenv.CreateBoundary(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSpawnBoundaryUnavailable, err)
	}
	return unixBoundary{boundary: boundary}, nil
}

// unixBoundary adapts *execenv.Boundary to the fence's SpawnBoundary surface.
type unixBoundary struct {
	boundary *execenv.Boundary
}

func (u unixBoundary) Identity() BoundaryID {
	id := u.boundary.Identity()
	var platform string
	switch id.Platform {
	case execenv.BoundaryPlatformLinux:
		platform = BoundaryPlatformLinux
	case execenv.BoundaryPlatformDarwin:
		platform = BoundaryPlatformDarwin
	}
	return BoundaryID{
		Platform:  platform,
		CgroupID:  id.CgroupID,
		PGID:      id.PGID,
		SessionID: id.SessionID,
	}
}

func (u unixBoundary) Enforcing() bool { return u.boundary.Enforcing() }

func (u unixBoundary) SpawnAttr() (*syscall.SysProcAttr, func(), error) {
	return u.boundary.SpawnAttr()
}

func (u unixBoundary) Observe(pid int) (string, error) { return u.boundary.Observe(pid) }

func (u unixBoundary) Close() error { return u.boundary.Close() }
