// Package legacypaths rewrites absolute path prefixes left over from the
// Serf→Evener rename (see cmd/evener-migrate and internal/plugins). Any
// state written before a machine was migrated may still carry paths rooted
// under the old "serf" locations; this package supplies the one primitive
// both the migration tool (rewriting file contents) and the plugins package
// (rewriting stored path fields) use to repair them.
package legacypaths

import "strings"

// Rewrite replaces every occurrence of oldRoot in content with newRoot, but
// only when oldRoot is immediately followed by a path separator or any
// other byte that could not extend the same path component (e.g. the
// closing quote of a JSON string, or the end of content). This prevents a
// false match against an unrelated, longer path component — oldRoot
// "/a/.serf" must not match inside "/a/.serfbackup".
//
// It reports how many replacements were made. An empty oldRoot, or
// oldRoot == newRoot, is a no-op.
func Rewrite(content, oldRoot, newRoot string) (string, int) {
	if oldRoot == "" || oldRoot == newRoot {
		return content, 0
	}

	var b strings.Builder
	n := 0
	rest := content
	for {
		i := strings.Index(rest, oldRoot)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		end := i + len(oldRoot)
		b.WriteString(rest[:i])
		if end == len(rest) || !isPathContinuation(rest[end]) {
			b.WriteString(newRoot)
			n++
		} else {
			b.WriteString(rest[i:end])
		}
		rest = rest[end:]
	}
	return b.String(), n
}

// isPathContinuation reports whether b could be part of the same path
// component as the byte before it (i.e. it does not mark a path boundary).
func isPathContinuation(b byte) bool {
	return b == '_' || b == '-' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
