package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// RootGuard decides whether a host-derived path may become a sandbox grant root.
// It is the single implementation of that check: the session infrastructure
// grant (hook and MCP-server paths) and the restricted-mode developer-toolchain
// grant both run their candidates through it.
//
// It holds the shared, multi-tenant anchors in canonical (symlink-resolved) form
// so the checks compare like with like.
type RootGuard struct {
	// shared are locations that hold MANY tenants' data: the user's home (every
	// other project, every credential the denylist does not name, every other
	// session's transcripts), the session's own worktree (whose PARENT typically
	// holds every other lane), and the temp roots (every other session's
	// scratch). A root at or ABOVE any of these is refused.
	shared []string
	// sharedTree is every ancestor-or-self of every shared anchor, stat'd once at
	// construction. Permit compares a candidate against these with os.SameFile,
	// which is what makes the check independent of how a path is SPELLED.
	sharedTree []os.FileInfo
}

// NewRootGuard builds a guard over the given shared anchors. Empty and
// unresolvable anchors are dropped and cleaned respectively — never silently
// turned into a relative path that would match nothing.
func NewRootGuard(anchors ...string) RootGuard {
	var g RootGuard
	for _, a := range anchors {
		c := CanonicalPath(a)
		if c == "" {
			continue
		}
		g.shared = append(g.shared, c)
		// Stat the anchor and every directory above it. A candidate root is "at or
		// above this anchor" exactly when it is the SAME FILE as one of them —
		// however either path happens to be spelled.
		for dir := c; ; dir = filepath.Dir(dir) {
			if fi, err := os.Stat(dir); err == nil {
				g.sharedTree = append(g.sharedTree, fi)
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	return g
}

// Permit returns root if it is safe to grant, else "".
//
// A root is refused when it is at or ABOVE any shared anchor (home, the
// worktree, a temp root), or when it has fewer than two path components. Both
// shapes hand a spawned process a whole multi-tenant tree: "/Users" and "/home"
// are ANCESTORS of a home directory rather than equal to one, so an equality
// check misses them, and Resolve's filterMasked cannot help either — it drops
// roots at or BENEATH a masked path, and an ancestor is above them. The result
// would be read/exec of every home on the machine (and every other worktree
// lane) minus only the named credential directories.
//
// A misconfigured hook, MCP server, or developer directory that names such a
// path stays unreachable under a sandbox — the same outcome as before the grant
// existed — rather than silently gutting the mode. Everything a real
// installation needs is unaffected: a plugin under the registry root, a plugin
// under ~/.claude or ~/.config, an MCP program in /opt/<vendor>/... or inside
// the worktree, and the developer toolchain under /Applications/Xcode.app or
// /Library/Developer are all at or below their anchors, not above them.
//
// The anchor check is made twice, on purpose. The textual pass catches the normal
// case. The os.SameFile pass then catches every path that names an anchor (or an
// anchor's ancestor) under a DIFFERENT SPELLING, which the textual pass cannot see
// and canonicalization does not fix: on a case-insensitive filesystem
// "/Users/JESSE" and "/Users/jesse" are one directory, EvalSymlinks returns
// whichever spelling it was handed, and Seatbelt's own subpath matching is
// case-insensitive — so the textual check would pass a root the kernel then honours
// as the whole home tree. Unicode NFC-vs-NFD spellings, which APFS also treats as
// one file, have the same shape.
//
// SameFile is used rather than case-folding because it asks the FILESYSTEM what it
// considers the same file instead of guessing its collation and normalization
// rules. A runtime.GOOS test would get both directions wrong: macOS volumes can be
// case-sensitive, and Linux can mount case-insensitive filesystems.
func (g RootGuard) Permit(root string) string {
	if root == "" || !filepath.IsAbs(root) {
		return ""
	}
	root = StripDataVolumeAlias(filepath.Clean(root))
	if PathDepth(root) < 2 {
		return "" // "/", "/Users", "/home", "/private", "/var", "/opt", "/Volumes", …
	}
	for _, anchor := range g.shared {
		if pathUnder(anchor, root) { // root is at or above this anchor
			return ""
		}
	}
	if fi, err := os.Stat(root); err == nil {
		for _, anchor := range g.sharedTree {
			if os.SameFile(fi, anchor) { // the same directory, differently spelled
				return ""
			}
		}
	}
	return root
}

// CanonicalPath resolves p to its absolute, SYMLINK-RESOLVED, alias-stripped
// form. An unresolvable path is cleaned, never dropped — for an anchor, a
// dropped value would silently weaken the guard.
//
// Resolving symlinks before the safety checks is load-bearing: filepath.Abs does
// not follow links and os.Stat follows the link while leaving the textual path
// alone, so a link named in config (`plugin -> /Users/jesse`) would sail past a
// check on its own spelling while granting the target's contents. It also puts
// macOS paths in the canonical form the anchors use (/tmp -> /private/tmp,
// /var -> /private/var), so an ancestor check cannot miss on spelling alone.
func CanonicalPath(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return StripDataVolumeAlias(resolved)
	}
	return StripDataVolumeAlias(abs)
}

// CanonicalDir resolves path to its canonical form and returns "" unless it
// names an existing DIRECTORY. It is how a grant candidate that must be a
// directory (a plugin dir, a developer-tools root) is admitted.
func CanonicalDir(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "" // missing, or a broken link: nothing to grant
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.IsDir() {
		return ""
	}
	return StripDataVolumeAlias(resolved)
}

// StripDataVolumeAlias reduces a path to its plain spelling, so the
// /System/Volumes/Data firmlink alias and the direct path compare equal in every
// guard check. Firmlinks are not symlinks, so EvalSymlinks does NOT collapse
// them (the Seatbelt backend denies both spellings for exactly this reason).
// Left uncollapsed, the alias would walk straight past the guard:
// "/System/Volumes/Data/Users" is four components deep and is not an ancestor of
// the canonical "/Users/<user>" home, so it would pass both checks while
// granting every home on the machine.
func StripDataVolumeAlias(p string) string {
	c := filepath.Clean(p)
	if c == dataVolumePrefix {
		return "/"
	}
	if rest, ok := strings.CutPrefix(c, dataVolumePrefix+"/"); ok {
		return filepath.Clean("/" + rest)
	}
	return c
}

// PathDepth counts a cleaned absolute path's components ("/" is 0, "/Users" is
// 1, "/Users/jesse" is 2).
func PathDepth(p string) int {
	trimmed := strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "/") + 1
}
