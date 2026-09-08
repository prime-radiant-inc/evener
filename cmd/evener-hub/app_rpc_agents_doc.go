package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/internal/appserver"
)

// agentsDocPath is the personal AGENTS.md under the user config root - the
// same file agent.LoadInstructionDocs reads at session init.
func agentsDocPath(configRoot string) string {
	return filepath.Join(configRoot, agent.UserDocFile)
}

// hubAgentsDocPath is the personal AGENTS.md this hub edits and hands to the
// sessions it spawns, or "" when there is none to point at. The root has to be
// absolute: a relative one - what cmdutil.DefaultConfigRoot() substitutes when
// neither XDG_CONFIG_HOME nor a home directory resolves - would resolve against
// whatever directory the process happens to sit in, and a repository's own
// .config/evener/AGENTS.md must never become the user's standing instructions.
// An empty result means "no personal doc": the spawn and resume builders then
// omit --agents-doc and the settings handlers refuse, which is the rule
// agent.personalDocPath already applies on the daemon side.
func hubAgentsDocPath(cfg hubcore.WebConfig) string {
	root := cfg.LaunchConfigRoot
	if root == "" {
		// userdirs, not cmdutil: this one reports an unresolvable root as
		// empty instead of substituting a relative path.
		root = userdirs.DefaultConfigRoot()
	}
	if root == "" || !filepath.IsAbs(root) {
		return ""
	}
	return agentsDocPath(root)
}

// errNoAgentsDocPath is what the settings handlers answer when no user config
// root resolves. Reading or writing a relative AGENTS.md instead would put the
// process working directory in charge of the user's personal instructions.
var errNoAgentsDocPath = errors.New("personal AGENTS.md is unavailable: no user config root could be resolved")

// readAgentsDoc reports the file as it is on disk. A missing file is the
// empty document, not an error: the settings section shows an empty editor
// and the first save creates it.
func readAgentsDoc(path string) (appwire.AgentsDocResponse, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return appwire.AgentsDocResponse{Path: path}, nil
	}
	if err != nil {
		return appwire.AgentsDocResponse{}, fmt.Errorf("AGENTS.md: read: %w", err)
	}
	return appwire.AgentsDocResponse{Path: path, Exists: true, Content: string(b)}, nil
}

// writeAgentsDoc replaces the file atomically (temp + rename, mode 0644,
// parent created), the same way registry.WriteConfigFile lands
// providers.toml beside it, except that this writer follows a symlinked
// AGENTS.md first so a dotfiles-managed copy keeps being the source of truth
// (Jesse's ruling, 2026-09-07; providers.toml follows in its own PR).
// Content is written byte for byte: this is the user's own prose, and
// trimming or appending a newline would make the editor disagree with the
// file it just saved. The temp file is created exclusively, under a name
// nothing else holds, and chmodded outright: a stale temp file - a symlink
// into someone else's file, or a leftover whose own mode a plain write would
// have kept - then has no say in where the save goes or what it lands as,
// and neither does the umask the hub happens to run under.
func writeAgentsDoc(path, content string) error {
	// Only a path with nothing at the end of it - no file yet, or a link whose
	// target is gone - falls back to path itself, so a first save creates the
	// file where the config root says it is and a broken link is replaced. A
	// link that is there but unresolvable (a loop, a directory along the way the
	// hub cannot search) is a real target the save cannot reach: renaming over
	// it would sever the dotfiles link this writer follows in order to keep.
	target := path
	resolved, err := filepath.EvalSymlinks(path)
	switch {
	case err == nil:
		target = resolved
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("AGENTS.md: resolve: %w", err)
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("AGENTS.md: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".AGENTS.md-*.tmp")
	if err != nil {
		return fmt.Errorf("AGENTS.md: write: %w", err)
	}
	// A save that dies partway (ENOSPC, EIO) has already created the temp
	// file, so clear it too: the real file is untouched either way, and a
	// half-written temp file has no business outliving the failure in the
	// user's config root. The rename takes the name with it, so this is a
	// no-op once the save has landed.
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := writeTempAgentsDoc(tmp, content); err != nil {
		return fmt.Errorf("AGENTS.md: write: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("AGENTS.md: rename: %w", err)
	}
	return nil
}

// writeTempAgentsDoc lands the content in the open temp file and closes it,
// whichever step fails.
func writeTempAgentsDoc(tmp *os.File, content string) (err error) {
	defer func() {
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		return err
	}
	return tmp.Sync()
}

// registerAgentsDocHandlers serves evener/settings/agentsDoc/{get,set}. Writes
// serialize on one mutex so a save's rename, read back and broadcast land as
// one unit: two clients saving at once could otherwise rename in one order
// and broadcast in the other, leaving every client on content the file does
// not hold. There is no revision check by design (spec 2026-09-07 §1) - the
// last write wins, and every client hears about it. An empty path is a hub
// with no user config root to edit under, and both methods refuse.
func registerAgentsDocHandlers(server *appserver.Server, path string) {
	var mu sync.Mutex
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocGet,
		func(context.Context, appwire.EmptyParams) (appwire.AgentsDocResponse, error) {
			if path == "" {
				return appwire.AgentsDocResponse{}, errNoAgentsDocPath
			}
			mu.Lock()
			defer mu.Unlock()
			return readAgentsDoc(path)
		})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocSet,
		func(_ context.Context, params appwire.AgentsDocSetParams) (appwire.AgentsDocResponse, error) {
			if path == "" {
				return appwire.AgentsDocResponse{}, errNoAgentsDocPath
			}
			mu.Lock()
			defer mu.Unlock()
			if err := writeAgentsDoc(path, params.Content); err != nil {
				return appwire.AgentsDocResponse{}, err
			}
			resp, err := readAgentsDoc(path)
			if err != nil {
				// The rename landed, so the save APPLIED. Surfacing the
				// re-read failure would tell the requester its write was
				// rejected and leave every other client on the old content,
				// so describe what was just put on disk instead - the write
				// is byte for byte, so this is the file (same reasoning as
				// the post-rename path in registerKeybindingsHandlers). The
				// response says nothing went wrong, so the log is the only
				// place the failure is visible at all.
				server.Logf("AGENTS.md read back after write failed: %v", err)
				resp = appwire.AgentsDocResponse{Path: path, Exists: true, Content: params.Content}
			}
			server.BroadcastAll(appwire.NotifyEvenerSettingsAgentsDocChanged, resp)
			return resp, nil
		})
}
