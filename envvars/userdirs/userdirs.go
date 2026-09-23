// Package userdirs resolves Evener's user-level configuration and state
// paths.
package userdirs

import (
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

// ConfigRoot resolves the Evener config root from an XDG config base and a
// caller-supplied home-directory lookup. A failed home lookup produces an
// empty path so callers that cannot safely fall back do not scan a relative
// directory by accident.
func ConfigRoot(xdgConfigHome string, userHomeDir func() (string, error)) string {
	base := xdgConfigHome
	if base == "" {
		home, err := userHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "evener")
}

// Subdir derives a child directory or file from a config root. It preserves an
// unavailable root as unavailable instead of turning it into a relative path.
func Subdir(root, name string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, name)
}

// DefaultConfigRoot resolves the user config root from the process environment.
func DefaultConfigRoot() string {
	return ConfigRoot(envvars.XDGConfigHome.Getenv(), os.UserHomeDir)
}

// StateHomeForBucketDir reports the XDG-style state home that a project
// bucket directory lives under, or "" when stateDir is not laid out as
// <stateHome>/evener/projects/<project-id>.
//
// The daemon runs every session with its state dir set to that bucket
// directory — what agent.RuntimeDir computes and what EVENER_STATE_DIR
// carries — so anything sweeping a whole state root from such a base must
// up-walk to the state home before it can see the sibling buckets. The two
// path components checked (evener, projects) are the runtime's own
// structural layout, not a bucket-naming assumption: any directory under
// <stateHome>/evener/projects is a bucket whatever its name looks like.
func StateHomeForBucketDir(stateDir string) string {
	// Clean first: a trailing separator defeats a Dir-based walk (Dir of
	// "…/b/" is "…/b", so Base would read the bucket's own name where the
	// walk expects "projects"). Cleaning here keeps every caller honest —
	// both the agent's and the doctor's sweeps spell the state dir, and
	// "--state-dir $DIR/" is a trivial spelling to hit.
	stateDir = filepath.Clean(stateDir)
	projects := filepath.Dir(stateDir)
	if filepath.Base(projects) != "projects" {
		return ""
	}
	evener := filepath.Dir(projects)
	if filepath.Base(evener) != "evener" {
		return ""
	}
	return filepath.Dir(evener)
}
