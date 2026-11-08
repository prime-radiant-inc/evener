package skill

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/evener/identifier"
)

// readMountInfo is the /proc/self/mountinfo boundary. A platform without it
// (or a restricted proc view that hides it) reports no read-only mounts, so the
// ancestor chain is verified by ownership exactly as it was before this check.
// A seam so tests can describe the mount table they need without creating
// mounts; a failed read is the same evidence as an empty table.
var readMountInfo = func() []byte {
	text, _ := os.ReadFile("/proc/self/mountinfo")
	return text
}

// mountPoint is one parsed mountinfo entry: the path the mount covers and
// whether it refuses writes.
type mountPoint struct {
	path     string
	readOnly bool
}

// parseMountPoints reads the mount table a chain check consults. It is parsed
// once per ancestor chain, not once per ancestor.
func parseMountPoints(text string) []mountPoint {
	var points []mountPoint
	for line := range strings.SplitSeq(text, "\n") {
		// The kernel separates mountinfo fields with single ASCII spaces and
		// escapes raw whitespace in path fields, so a Unicode-aware split would
		// let a mount point containing U+00A0 shift every later field (a writable
		// mount read as read-only).
		fields := strings.Split(line, " ")
		if len(fields) < 6 {
			continue
		}
		point := unescapeMountInfo(fields[4])
		if point == "" {
			continue
		}
		readOnly := false
		for option := range strings.SplitSeq(fields[5], ",") {
			if option == "ro" {
				readOnly = true
				break
			}
		}
		points = append(points, mountPoint{path: point, readOnly: readOnly})
	}
	return points
}

// readOnlyMountAt reports whether dir lies on a read-only mount: the mount whose
// mount point is the longest path-boundary prefix of dir decides, so a read-only
// mount nested under a writable one is honoured and the reverse is not. A path
// on a read-only mount cannot be replaced or modified by any process confined to
// this mount namespace, whoever owns it: the mount pins the inode and the kernel
// refuses writes. That matters for the sandbox's read-only root bind, whose owner
// is host root and therefore appears as the unmapped overflow uid in a user
// namespace, which the ownership predicate must otherwise refuse. No matching
// mount is not evidence of a read-only one; it falls back to the ownership check.
func readOnlyMountAt(mounts []mountPoint, dir string) bool {
	dir = filepath.Clean(dir)
	best, bestReadOnly := "", false
	for _, mount := range mounts {
		if !identifier.PathUnder(dir, mount.path) || len(mount.path) < len(best) {
			continue
		}
		best = mount.path
		bestReadOnly = mount.readOnly
	}
	return bestReadOnly
}

// unescapeMountInfo decodes the octal escapes mountinfo applies to path fields
// (space, tab, newline, and backslash are the ones the kernel emits).
func unescapeMountInfo(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+3 < len(value) {
			if code, err := strconv.ParseUint(value[i+1:i+4], 8, 8); err == nil {
				decoded.WriteByte(byte(code))
				i += 3
				continue
			}
		}
		decoded.WriteByte(value[i])
	}
	return decoded.String()
}
