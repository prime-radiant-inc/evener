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

// readOnlyMountAt reports whether dir lies on the mount path lookup actually
// reaches. A mount that a later mount covers still has an entry in
// /proc/self/mountinfo, so the longest mount-point prefix can name a hidden
// mount; walking the mount tree instead follows the traversal the kernel
// performs, crossing the first mount point on the way and, where mounts are
// stacked on one point, the last one mounted. A mount stacked exactly on the
// namespace root is not reached, because path lookup starts at the root dentry
// without crossing a mount there. A path on a read-only mount cannot be written
// through this namespace, but the mount only pins the root it was attached at;
// ordinary directories beneath it can still be replaced through another,
// writable mount of the same host filesystem. No matching mount is not evidence
// of a read-only one, so it reports false and the ownership check decides.
func readOnlyMountAt(mounts []mountPoint, dir string) bool {
	reached, ok := mountReachedAt(mounts, dir)
	return ok && reached.readOnly
}

// mountTree indexes a parsed table: the root of the mount tree and each mount's
// children. A table without exactly one root (a filtered or malformed
// mountinfo) is not a tree this check can resolve.
func mountTree(mounts []mountPoint) (root mountPoint, children map[int][]mountPoint, ok bool) {
	byID := make(map[int]mountPoint, len(mounts))
	for _, mount := range mounts {
		byID[mount.id] = mount
	}
	children = make(map[int][]mountPoint, len(mounts))
	for _, mount := range mounts {
		if _, present := byID[mount.parentID]; !present || mount.parentID == mount.id {
			if ok {
				return mountPoint{}, nil, false
			}
			root, ok = mount, true
			continue
		}
		children[mount.parentID] = append(children[mount.parentID], mount)
	}
	return root, children, ok
}

// mountReachedAt returns the mount path lookup reaches for dir by walking the
// mount tree from its root. At each mount it crosses the first child mount point
// on the way to dir (a shorter prefix), because a mount covers everything beneath
// the point it was attached at; among mounts stacked on the same point the last
// one mounted is on top. A table whose tree cannot be resolved (no root, or more
// than one) proves nothing, so it reports no mount.
func mountReachedAt(mounts []mountPoint, dir string) (mountPoint, bool) {
	if len(mounts) == 0 {
		return mountPoint{}, false
	}
	dir = filepath.Clean(dir)
	root, children, ok := mountTree(mounts)
	if !ok || !identifier.PathUnder(dir, root.path) {
		return mountPoint{}, false
	}
	current := root
	for {
		next, found := mountPoint{}, false
		for _, child := range children[current.id] {
			if !identifier.PathUnder(dir, child.path) {
				continue
			}
			// Path lookup starts at the root dentry without crossing a mount
			// there, so a mount stacked exactly on the namespace root is never
			// reached. Deeper stacks are followed as each component resolves.
			if current.id == root.id && child.path == root.path {
				continue
			}
			// A shorter prefix is attached higher in the tree, so path lookup
			// reaches it before any deeper child of the same mount; an equal
			// prefix is a stacked mount, and the later mount (higher ID) is the
			// one on top.
			if !found || len(child.path) < len(next.path) ||
				(child.path == next.path && child.id > next.id) {
				next, found = child, true
			}
		}
		if !found {
			return current, true
		}
		current = next
	}
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
