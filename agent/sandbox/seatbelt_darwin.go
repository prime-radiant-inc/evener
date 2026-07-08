//go:build darwin

package sandbox

import "path/filepath"

// pathToSeatbelt is the sandbox-exec binary, HARD-CODED to /usr/bin. It is never
// resolved via PATH or exec.LookPath: a spawned command could otherwise drop a
// fake "sandbox-exec" on the PATH and neuter the sandbox. If /usr/bin/sandbox-exec
// itself is tampered with, the attacker already has root.
const pathToSeatbelt = "/usr/bin/sandbox-exec"

// realCanonicalizer resolves a root to its canonical macOS path (following
// symlinks and firmlinks: /tmp -> /private/tmp, $HOME under /Users), which is
// what Seatbelt matches on. A path that cannot be resolved (does not exist yet,
// or a broken link) is returned unchanged rather than dropped — a dropped write
// root would silently lose a grant and a dropped exclusion would silently widen
// the policy.
func realCanonicalizer(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// seatbeltWrap prepends the sandbox-exec invocation to argv so the command — and
// every descendant it forks — runs confined to rp's spawned-layer policy. cwd is
// accepted for signature symmetry with the bwrap path and the non-darwin stub;
// the sandbox chdir is the spawned command's own cmd.Dir, and rp already carries
// concrete absolute roots, so no chdir flag is needed. It never errors on
// darwin.
func seatbeltWrap(argv []string, rp ResolvedPolicy, sessionTmp, cwd string) ([]string, error) {
	_ = cwd
	text, params := SeatbeltPolicy(rp, sessionTmp, realCanonicalizer)
	return seatbeltArgs(pathToSeatbelt, text, params, argv), nil
}
