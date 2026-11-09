package skill

import (
	"os"
	"strconv"
	"strings"
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

// mountPoint is one parsed mountinfo entry: the mount's place in the mount
// tree, the root it covers within its filesystem, the path it covers, and
// whether it refuses writes.
type mountPoint struct {
	id       int
	parentID int
	fsRoot   string
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
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		parentID, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		fsRoot := unescapeMountInfo(fields[3])
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
		points = append(points, mountPoint{id: id, parentID: parentID, fsRoot: fsRoot, path: point, readOnly: readOnly})
	}
	return points
}

// mountRoot returns the single root mount of a parsed table. A table without
// exactly one root (a filtered or malformed mountinfo) is not a tree this check
// can resolve.
func mountRoot(mounts []mountPoint) (root mountPoint, ok bool) {
	byID := make(map[int]mountPoint, len(mounts))
	for _, mount := range mounts {
		byID[mount.id] = mount
	}
	for _, mount := range mounts {
		if _, present := byID[mount.parentID]; !present || mount.parentID == mount.id {
			if ok {
				return mountPoint{}, false
			}
			root, ok = mount, true
		}
	}
	return root, ok
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
