//go:build darwin

package sandbox

import "path/filepath"

// pathToSeatbelt is the sandbox-exec binary, HARD-CODED to /usr/bin. It is never
// resolved via PATH or exec.LookPath: a spawned command could otherwise drop a
// fake "sandbox-exec" on the PATH and neuter the sandbox. If /usr/bin/sandbox-exec
// itself is tampered with, the attacker already has root.
const pathToSeatbelt = "/usr/bin/sandbox-exec"

// realCanonicalizer resolves a root or exclusion to the canonical macOS path
// Seatbelt matches on (following symlinks and firmlinks: /tmp -> /private/tmp,
// /var -> /private/var). It resolves the longest existing prefix and re-appends
// any not-yet-existing tail, so a protected surface that does not exist yet
// (e.g. .git/config.worktree) still carries the same canonical prefix as its
// granting root — otherwise its require-not exclusion would silently miss and
// leave the surface writable. A path with no resolvable ancestor is returned
// cleaned, never dropped (a dropped write root loses a grant; a dropped exclusion
// widens the policy).
func realCanonicalizer(p string) string {
	return canonicalizeLongestPrefix(p, filepath.EvalSymlinks)
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
