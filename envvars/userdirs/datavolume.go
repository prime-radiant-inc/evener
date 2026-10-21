package userdirs

import (
	"path/filepath"
	"strings"
)

// dataVolumePrefix is the APFS data-volume mount point macOS firmlinks a file's
// root-level spelling onto. filepath.EvalSymlinks does not collapse a firmlink,
// so both spellings reach comparisons unresolved.
const dataVolumePrefix = "/System/Volumes/Data"

// StripDataVolumeAlias reduces a path to its plain spelling, so the
// /System/Volumes/Data firmlink alias and the direct path compare equal. The
// sandbox credential guard and the plugin store's managed-content containment
// both compare paths from the filesystem, where a firmlink survives
// EvalSymlinks.
func StripDataVolumeAlias(p string) string {
	c := filepath.Clean(p)
	if c == dataVolumePrefix {
		return "/"
	}
	if rest, ok := strings.CutPrefix(c, dataVolumePrefix+"/"); ok {
		return "/" + rest
	}
	return c
}
