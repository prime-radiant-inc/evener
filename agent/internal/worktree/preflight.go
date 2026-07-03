package worktree

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// requiredGitMajor and requiredGitMinor are the floor `git worktree add
// --lock --reason` needs (spec §3 step 6: the --reason-on-add series landed
// in git 2.33, July 2021). There is no degraded mode below this floor (spec
// §8) — atomic locked-create is load-bearing for the occupancy-lock
// invariant §5 relies on.
const (
	requiredGitMajor = 2
	requiredGitMinor = 33
)

// gitVersionRe pulls the "X.Y" out of git's "git version X.Y.Z[...]"
// banner. It tolerates any trailing suffix (patch level, ".windows.1",
// vendor parentheticals like Apple's "(Apple Git-146)") by only anchoring on
// the "version" keyword and the leading major.minor pair.
var gitVersionRe = regexp.MustCompile(`version\s+(\d+)\.(\d+)`)

// parseGitVersion extracts the major.minor pair from git's `version`
// banner. ok is false when s does not contain a recognizable "version X.Y"
// substring, or when either number overflows int.
func parseGitVersion(s string) (major, minor int, ok bool) {
	m := gitVersionRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(m[1])
	mnr, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, mnr, true
}

// CheckGitVersion runs `git version` through run and reports an error
// unless the reported version is at least requiredGitMajor.requiredGitMinor
// (spec §3 step 6, §8). Memoizing the result per session so this only runs
// once is the caller's responsibility — CheckGitVersion always re-runs.
func CheckGitVersion(run GitRunner) error {
	out, err := run("version")
	if err != nil {
		return fmt.Errorf("checking git version: %w", err)
	}
	major, minor, ok := parseGitVersion(out)
	if !ok {
		return fmt.Errorf("could not parse a git version from %q; serf requires git >= %d.%d for `git worktree add --lock --reason`",
			strings.TrimSpace(out), requiredGitMajor, requiredGitMinor)
	}
	if major < requiredGitMajor || (major == requiredGitMajor && minor < requiredGitMinor) {
		return fmt.Errorf("git %d.%d is too old; serf requires git >= %d.%d for `git worktree add --lock --reason` (no degraded mode below this floor)",
			major, minor, requiredGitMajor, requiredGitMinor)
	}
	return nil
}
