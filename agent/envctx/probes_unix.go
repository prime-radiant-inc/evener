//go:build darwin || linux

package envctx

import "golang.org/x/sys/unix"

func diskProbe(path string) string {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return ""
	}
	used := float64(st.Blocks-st.Bavail) / float64(st.Blocks)
	return diskWarning(used)
}
