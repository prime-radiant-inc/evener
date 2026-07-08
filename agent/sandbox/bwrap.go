package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// buildBwrapArgv turns a ResolvedPolicy into the bubblewrap flag vector (the
// arguments AFTER the bwrap binary and BEFORE the "--" that precedes the real
// command). It implements the spec's flat-roots model: a read baseline (`--ro-bind
// / /` for read-anywhere modes, `--tmpfs /` + per-root `--ro-bind` for the
// worktree-only restricted mode), writable `--bind` roots layered on top, git
// config/hook surfaces re-mounted read-only, the secrets + pseudo-fs denylist
// masked, a fresh PID-namespace `/proc`, a minimal `/dev`, a non-shared `/tmp`
// with the session tmp bound writable inside it, and `--unshare-net` when egress
// is denied.
//
// It is NOT the ~2700-line codex synthetic-mount machinery: masks and protections
// are applied as flat, ordered mounts (later mounts win over earlier ones), with
// no per-symlink TOCTOU snapshotting or proxy routing. It stats the host to decide
// dir-vs-file for each mask (a tmpfs hides a directory; a read-only /dev/null bind
// hides a file), so its output depends on the on-disk shape of the resolved roots.
func buildBwrapArgv(rp ResolvedPolicy, sessionTmp, cwd string) []string {
	var a []string
	add := func(xs ...string) { a = append(a, xs...) }

	// Hardening base. --unshare-user is requested explicitly rather than relying
	// on bwrap's auto-enable (skipped when the caller is uid 0). --unshare-pid +
	// a fresh --proc make host process state invisible. --die-with-parent kills
	// the sandbox if serf exits; --new-session severs the controlling terminal
	// (defeats TIOCSTI input injection).
	add("--unshare-user")
	add("--unshare-pid")
	add("--die-with-parent")
	add("--new-session")
	if !rp.Network {
		// net=off: no network namespace interfaces at all — no TCP, no UDP, no DNS.
		add("--unshare-net")
	}

	// Filesystem read baseline.
	sp := rp.Spawned
	if sp.Read == ReadAnywhere {
		// Reads anywhere minus the masks applied below: bind the whole tree
		// read-only, then re-open writable roots and re-mask denied subpaths.
		add("--ro-bind", "/", "/")
	} else {
		// Worktree-only (restricted): start from an empty root and add ONLY the
		// resolved read roots (worktree + system roots the process needs to run).
		add("--tmpfs", "/")
		for _, r := range sp.ReadRoots {
			if pathExists(r) {
				add("--ro-bind", r, r)
			}
		}
	}

	// A minimal /dev (null/zero/full/random/urandom/tty, no /dev/mem) and a fresh
	// pid-namespace /proc. These overlay whatever the read baseline exposed, so
	// host /dev/mem and host process state are gone regardless of the base.
	add("--dev", "/dev")
	add("--proc", "/proc")

	// A non-shared /tmp, with the per-session tmp bound writable inside it. The
	// tmpfs discards everything else under /tmp; the session dir is a real host
	// directory (cleaned at session end) reachable at its host path.
	add("--tmpfs", "/tmp")
	if sessionTmp != "" {
		add("--bind", sessionTmp, sessionTmp)
	}

	// Writable roots (worktree, git-metadata write subset, extra roots). Bound
	// after the read baseline so they win; a non-existent root is skipped (bwrap
	// requires bind targets to exist — the parent writable root already covers
	// leaves git creates lazily).
	for _, w := range sp.WriteRoots {
		if pathExists(w) {
			add("--bind", w, w)
		}
	}

	// Cache roots served read-real / write-private via overlay: warm reads from
	// the real cache, writes land in a per-mount tmpfs upper discarded at session
	// end, so a sandboxed build can never poison a cache a later build consumes.
	// Only on an overlay-capable host (this dev box's bubblewrap lacks overlay, so
	// CacheStrategy is CacheSessionPrivate here and the env floor redirects the
	// cache vars into the session tmp instead — same no-poisoning floor, cold).
	if rp.CacheStrategy == CacheOverlay {
		for _, c := range rp.CacheRoots {
			if pathExists(c) {
				add("--overlay-src", c, "--tmp-overlay", c)
			}
		}
	}

	// Re-protect git config + hook surfaces read-only. They sit INSIDE the
	// writable worktree/gitdir, so without this a writable-root bind would make
	// .git/config and .git/hooks writable and a planted hook would fire later,
	// unsandboxed. Applied after the writable binds so the protection wins.
	for _, p := range rp.Git.ProtectedPaths {
		maskReadOnly(&a, p)
	}

	// Mask the secrets + pseudo-fs denylist last so masks win over every bind.
	// The pseudo-fs floor entries handled by a namespace mount are skipped: /proc
	// is the fresh pid-ns mount (--proc), and /dev/* are excluded by the minimal
	// --dev (host /dev/mem is absent, /dev/fd is a safe symlink to the sandbox's
	// own /proc/self/fd). Re-masking them would clobber those mounts (bwrap cannot
	// bind over the /dev symlink). /sys and /run/user still need explicit masks.
	for _, m := range rp.MaskedPaths {
		if maskHandledByNamespace(m) {
			continue
		}
		maskInvisible(&a, m)
	}

	// Enter the working directory (bound above) so relative paths resolve.
	if cwd != "" {
		add("--chdir", cwd)
	}
	return a
}

// maskInvisible appends the flags that make path unreadable inside the sandbox:
// an empty tmpfs over a directory (its contents vanish) or a read-only /dev/null
// bind over a file. A path that does not exist on the host needs no mask — there
// is nothing to hide, and the enclosing read baseline never exposed it.
func maskInvisible(a *[]string, path string) {
	fi, err := os.Lstat(path)
	if err != nil {
		return
	}
	if fi.IsDir() {
		*a = append(*a, "--tmpfs", path)
		return
	}
	*a = append(*a, "--ro-bind", "/dev/null", path)
}

// maskReadOnly appends the flags that make a git config/hook surface readable but
// write-denied. An existing path is re-bound read-only (writes fail EROFS, and
// the mountpoint cannot be unlinked). A missing path is pinned so it cannot be
// CREATED under the writable parent: a directory surface (hooks) as a read-only
// empty tmpfs, a file surface (config, config.worktree, the gitdir pointer) as a
// read-only /dev/null bind.
func maskReadOnly(a *[]string, path string) {
	if pathExists(path) {
		*a = append(*a, "--ro-bind", path, path)
		return
	}
	if filepath.Base(path) == "hooks" {
		*a = append(*a, "--tmpfs", path, "--remount-ro", path)
		return
	}
	*a = append(*a, "--ro-bind", "/dev/null", path)
}

// maskHandledByNamespace reports whether a masked path is already isolated by a
// namespace mount rather than an explicit mask: /proc (the fresh pid-ns --proc)
// and everything under /dev (the minimal --dev, which omits /dev/mem and makes
// /dev/fd a safe self-referential symlink). Masking these again would clobber the
// namespace mount, so the caller skips them.
func maskHandledByNamespace(path string) bool {
	return path == "/proc" || path == "/dev" || strings.HasPrefix(path, "/dev/")
}

// pathExists reports whether path exists on the host (following symlinks).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
