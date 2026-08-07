package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

var (
	bwrapEvalSymlinks = filepath.EvalSymlinks
	bwrapStat         = os.Stat
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

	// A read-granted root that falls under /tmp was just shadowed by the /tmp
	// tmpfs. Re-bind such roots read-only (mirroring the writable re-bind below) so
	// a read-only worktree or read grant under /tmp survives — otherwise --chdir
	// into a /tmp cwd aborts the sandbox. Write roots under /tmp are re-bound
	// writable below and win (later mounts win), so a root that is both read- and
	// write-granted ends up writable. The session tmp (already bound writable) and
	// the /tmp tmpfs root itself are left untouched.
	reboundRO := make(map[string]bool)
	for _, r := range append([]string{cwd}, sp.ReadRoots...) {
		if r == "" || r == "/tmp" || !pathUnder(r, "/tmp") {
			continue
		}
		if r == sessionTmp || (sessionTmp != "" && pathUnder(r, sessionTmp)) {
			continue
		}
		if reboundRO[r] || !pathExists(r) {
			continue
		}
		reboundRO[r] = true
		add("--ro-bind", r, r)
	}

	// Writable roots (worktree, git-metadata write subset, extra roots). Bound
	// after the read baseline so they win; a non-existent root is skipped, because
	// bwrap requires bind targets to exist. Where a writable PARENT root covers it
	// (a main checkout's whole worktree, a linked worktree's per-worktree git dir),
	// a leaf git creates lazily — packed-refs and its packed-refs.lock /
	// packed-refs.new siblings, index.lock, logs — is still creatable through that
	// parent, so skipping the leaf costs nothing.
	for _, w := range sp.WriteRoots {
		if pathExists(w) {
			add("--bind", w, w)
		}
	}

	// A linked worktree's / submodule's COMMON dir has no writable parent, so its
	// granted metadata entries would be bound leaf by leaf — and a bind target must
	// exist, so every entry git had not created yet (packed-refs and its
	// packed-refs.lock / packed-refs.new siblings, logs, index, rr-cache) was
	// skipped and git could not create it. Permission to CREATE a name belongs to
	// the parent directory, and pre-mounting the name would only turn git's
	// O_CREAT|O_EXCL into EEXIST, so the grant is taken at DIRECTORY level: bind
	// the common dir writable, and let the ProtectedPaths re-binds below land on
	// top of it — the same shape a linked worktree's per-worktree git dir already
	// uses. Seatbelt reaches the same OUTCOME by matching path strings, so it keeps
	// its narrower named grant; this is parity of outcome, not of mechanism.
	if cd := commonDirWriteGrant(rp); cd != "" {
		add("--bind", cd, cd)
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
	// unsandboxed. Applied after the writable binds — including the common-dir
	// grant above — so the protection wins.
	protectionRoots := bwrapWriteRoots(rp)
	for _, p := range rp.Git.ProtectedPaths {
		maskReadOnly(&a, p, protectionRoots)
	}

	// Mask the secrets + pseudo-fs denylist last so masks win over every bind.
	// The pseudo-fs floor entries handled by a namespace mount are skipped: /proc
	// is the fresh pid-ns mount (--proc), and /dev/* are excluded by the minimal
	// --dev (host /dev/mem is absent, /dev/fd is a safe symlink to the sandbox's
	// own /proc/self/fd). Re-masking them would clobber those mounts (bwrap cannot
	// bind over the /dev symlink). /sys and /run/user still need explicit masks.
	// masked tracks resolved mask targets so aliases of the same path (e.g.
	// /var/run/docker.sock -> /run/docker.sock) are not double-mounted.
	masked := make(map[string]bool)
	for _, m := range rp.MaskedPaths {
		if maskHandledByNamespace(m) {
			continue
		}
		maskInvisible(&a, masked, m)
	}

	// Enter the working directory (bound above) so relative paths resolve.
	if cwd != "" {
		add("--chdir", cwd)
	}
	return a
}

// maskInvisible appends the flags that make path unreadable inside the sandbox:
// an empty tmpfs over a directory (its contents vanish) or a read-only /dev/null
// bind over a file. It masks the SYMLINK-RESOLVED real target, not the literal
// path, for two reasons: (1) a symlinked credential dir (~/.ssh -> real dir,
// common with dotfile managers) or a symlinked path component (/var/run -> /run)
// would otherwise make bwrap refuse to mount through the symlink and abort the
// whole sandbox; (2) masking the real inode hides the secret through every alias
// that reaches it. A path that does not exist (or a broken symlink) needs no mask
// — there is nothing to hide. seen dedups masks that resolve to the same target.
func maskInvisible(a *[]string, seen map[string]bool, path string) {
	resolved, err := bwrapEvalSymlinks(path)
	if err != nil {
		return
	}
	if seen[resolved] {
		return
	}
	fi, err := bwrapStat(resolved)
	if err != nil {
		return
	}
	seen[resolved] = true
	if fi.IsDir() {
		*a = append(*a, "--tmpfs", resolved)
		return
	}
	*a = append(*a, "--ro-bind", "/dev/null", resolved)
}

// maskReadOnly appends the flags that make a git config/hook surface readable but
// write-denied. An existing path is re-bound read-only (writes fail EROFS, and
// the mountpoint cannot be unlinked). A missing path is pinned so it cannot be
// CREATED — but ONLY when it sits under a writable root: a missing surface whose
// parent is read-only (e.g. a linked worktree's common .git, which is read-granted
// but never writable) can never be created anyway, and trying to pin it makes
// bwrap fail to create the mountpoint under the read-only parent (EROFS) and abort
// the sandbox. A directory surface (hooks) is pinned as a read-only empty tmpfs, a
// file surface (config, config.worktree, the gitdir pointer) as a read-only
// /dev/null bind.
func maskReadOnly(a *[]string, path string, writeRoots []string) {
	if pathExists(path) {
		*a = append(*a, "--ro-bind", path, path)
		return
	}
	if !isUnderAnyRoot(path, writeRoots) {
		return
	}
	if filepath.Base(path) == "hooks" {
		*a = append(*a, "--tmpfs", path, "--remount-ro", path)
		return
	}
	*a = append(*a, "--ro-bind", "/dev/null", path)
}

// commonDirWriteGrant returns the shared common git dir when the bwrap argv must
// bind it WRITABLE as a whole directory, or "" when it must not.
//
// It applies exactly to the layouts whose common dir lives outside the worktree
// (linked worktree, submodule) in a mode that already grants writes inside that
// common dir. A main checkout is excluded: its .git already sits under the
// worktree write root. A read-only mode is excluded for free — it has no write
// roots under the common dir, so nothing here can widen it.
func commonDirWriteGrant(rp ResolvedPolicy) string {
	cd := rp.Git.CommonDir
	if cd == "" || !pathExists(cd) {
		return ""
	}
	if isUnderAnyRoot(cd, rp.Spawned.WriteRoots) {
		return "" // already writable through a parent root (main checkout)
	}
	for _, w := range rp.Spawned.WriteRoots {
		if pathUnder(w, cd) {
			return cd
		}
	}
	return ""
}

// bwrapWriteRoots returns the roots the bwrap argv actually binds writable: the
// resolved spawned write roots plus the directory-level common-dir grant. It is
// what decides whether a MISSING protected surface can be created inside the
// sandbox, so both the argv's protection pass and the pre-creation step ask it —
// the common-dir grant is an argv-local widening the resolved policy does not
// carry, and a protection pass reading rp.Spawned.WriteRoots alone would leave
// the common dir's own config/hook surfaces creatable.
func bwrapWriteRoots(rp ResolvedPolicy) []string {
	cd := commonDirWriteGrant(rp)
	if cd == "" {
		return rp.Spawned.WriteRoots
	}
	return append(append([]string{}, rp.Spawned.WriteRoots...), cd)
}

// isUnderAnyRoot reports whether path is at or beneath any of roots.
func isUnderAnyRoot(path string, roots []string) bool {
	for _, r := range roots {
		if r != "" && (path == r || pathUnder(path, r)) {
			return true
		}
	}
	return false
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
