//go:build unix

package artifactstore

import (
	"os"
	"syscall"

	"primeradiant.com/evener/agent/internal/proclive"
)

// deadStoreDir reports whether name is a store directory this user owns and
// whose marked owner process is definitely gone. A directory whose pid still
// answers is never reclaimed, even when the pid is a reused one: keeping an
// abandoned directory is safe, removing one a live process owns is not.
func deadStoreDir(name string, info os.FileInfo) bool {
	pid, ok := storeOwnerPID(name)
	if !ok || pid == os.Getpid() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return false
	}
	return !proclive.Alive(pid)
}
