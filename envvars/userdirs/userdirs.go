// Package userdirs resolves Evener's user-level configuration and state
// paths.
package userdirs

import (
	"errors"
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

// ErrNoStateHome reports that neither XDG_STATE_HOME nor a home directory is
// available, so there is no honest place for Evener's state. Callers surface
// it rather than writing under a temporary or relative directory.
var ErrNoStateHome = errors.New("no state home: neither XDG_STATE_HOME nor a home directory is available")

// StateHome resolves the XDG state home (the parent of the "evener" state
// root) from an XDG state base and a caller-supplied home-directory lookup.
// It returns ErrNoStateHome when the base is empty and the home lookup fails
// or yields nothing.
func StateHome(xdgStateHome string, userHomeDir func() (string, error)) (string, error) {
	if xdgStateHome != "" {
		return xdgStateHome, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", errors.Join(ErrNoStateHome, err)
	}
	if home == "" {
		return "", ErrNoStateHome
	}
	return filepath.Join(home, ".local", "state"), nil
}

// StateRoot resolves Evener's machine-generated state root,
// <state home>/evener. See StateHome for the error contract.
func StateRoot(xdgStateHome string, userHomeDir func() (string, error)) (string, error) {
	base, err := StateHome(xdgStateHome, userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "evener"), nil
}

// Subdir derives a child directory or file from a config root. It preserves an
// unavailable root as unavailable instead of turning it into a relative path.
func Subdir(root, name string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, name)
}

// PluginStore is Evener's plugin store under a config root.
func PluginStore(root string) string { return Subdir(root, "plugins") }

// The plugin store's directories of installed plugin files: copies fetched from
// marketplaces, and the plugins Evener bundles. The rest of the store is
// metadata (the registries and marketplace clones).
const (
	PluginCacheDirName   = "cache"
	PluginBundledDirName = "bundled"
)

// PluginContentDirs are the store's directories of installed plugin files.
func PluginContentDirs(store string) []string {
	return []string{Subdir(store, PluginCacheDirName), Subdir(store, PluginBundledDirName)}
}

// UserSkills is the user's own skills directory under a config root.
func UserSkills(root string) string { return Subdir(root, "skills") }

// DefaultConfigRoot resolves the user config root from the process environment.
func DefaultConfigRoot() string {
	return ConfigRoot(envvars.XDGConfigHome.Getenv(), os.UserHomeDir)
}

// DefaultStateHome resolves the XDG state home from the process environment.
func DefaultStateHome() (string, error) {
	return StateHome(envvars.XDGStateHome.Getenv(), os.UserHomeDir)
}

// DefaultStateRoot resolves the Evener state root from the process
// environment: $XDG_STATE_HOME/evener, or ~/.local/state/evener.
//
// It holds machine-generated, non-config state: the auth token, the past-
// session index, the hub lock, and the daemon rendezvous/log directory. It is
// the evener-wide counterpart to DefaultConfigRoot (user-editable config, e.g.
// providers.toml) and to agent.RuntimeDir (per-project session state, also
// under $XDG_STATE_HOME/evener). EVENER_STATE_DIR does NOT override this root:
// that variable is a per-invocation project/session state override, a
// different concept from this evener-wide root, and XDG_STATE_HOME is already
// the standard override for it.
func DefaultStateRoot() (string, error) {
	return StateRoot(envvars.XDGStateHome.Getenv(), os.UserHomeDir)
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

// HomeRootFile is one file or directory that used to live directly under a
// legacy (~/.serf) or interim (~/.evener) home root, and the XDG root it
// belongs under in the final layout.
type HomeRootFile struct {
	Name  string // basename under the home root
	State bool   // true: XDG state root; false: XDG config root
	Kind  string // migration report label
}

// HomeRootFiles is the complete inventory of what ~/.serf and ~/.evener held.
// providers.toml/credentials.toml/hub.toml/launch.toml are user-editable
// configuration and move to the XDG config root; auth-token/index.db/hub.lock/
// run/deletions are machine-generated (or, for hub.lock and run,
// runtime-coordination) state and move to the XDG state root. evener-migrate
// moves these and the launch check uses the same list to decide whether a
// home root still holds unmigrated data, so both always agree on what
// "migrated" means.
var HomeRootFiles = []HomeRootFile{
	{Name: "providers.toml", Kind: "provider config"},
	{Name: "credentials.toml", Kind: "credentials"},
	{Name: "hub.toml", Kind: "hub config"},
	{Name: "launch.toml", Kind: "launch config"},
	{Name: "auth-token", State: true, Kind: "hub auth token"},
	{Name: "index.db", State: true, Kind: "past-session index"},
	{Name: "hub.lock", State: true, Kind: "hub lock"},
	{Name: "run", State: true, Kind: "daemon rendezvous/log directory"},
	{Name: "deletions", State: true, Kind: "deletion-fence state"},
}
