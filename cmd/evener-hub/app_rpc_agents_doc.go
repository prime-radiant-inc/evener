package hub

import (
	"context"
	"errors"
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
		return appwire.AgentsDocResponse{}, &agentsDocStepError{step: "read", err: err}
	}
	return appwire.AgentsDocResponse{Path: path, Exists: true, Content: string(b)}, nil
}

// writeAgentsDoc replaces the file atomically (temp + rename, mode 0644,
// parent created), the same way registry.WriteConfigFile lands
// providers.toml beside it: both follow a symlinked target first so a
// dotfiles-managed copy keeps being the source of truth (Jesse's ruling,
// 2026-09-07).
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
		return &agentsDocStepError{step: "resolve", err: err}
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &agentsDocStepError{step: "mkdir", err: err}
	}
	tmp, err := os.CreateTemp(dir, ".AGENTS.md-*.tmp")
	if err != nil {
		return &agentsDocStepError{step: "write", err: err}
	}
	// A save that dies partway (ENOSPC, EIO) has already created the temp
	// file, so clear it too: the real file is untouched either way, and a
	// half-written temp file has no business outliving the failure in the
	// user's config root. The rename takes the name with it, so this is a
	// no-op once the save has landed.
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := writeTempAgentsDoc(tmp, content); err != nil {
		return &agentsDocStepError{step: "write", err: err}
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return &agentsDocStepError{step: "rename", err: err}
	}
	return nil
}

// agentsDocStepError is a failed read or save of AGENTS.md, naming the step
// that failed: read, resolve, mkdir, write or rename.
type agentsDocStepError struct {
	step string
	err  error
}

func (e *agentsDocStepError) Error() string { return "AGENTS.md: " + e.step + ": " + e.err.Error() }

func (e *agentsDocStepError) Unwrap() error { return e.err }

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

// saveAgentsDoc writes content and reads the file back, and classifies the
// two failures apart. A write that never landed returns plain: nothing
// changed, so there is nothing to announce. A write that landed and then
// failed its read back is the applied-then-failed state (writeApplied), so
// the response describes what was just put on disk - the write is byte for
// byte, so that is the file - while the error still goes back to the
// requester that asked rather than a success fabricated from the input.
func saveAgentsDoc(path, content string) (appwire.AgentsDocResponse, error) {
	if err := writeAgentsDoc(path, content); err != nil {
		return appwire.AgentsDocResponse{}, err
	}
	resp, err := readAgentsDoc(path)
	if err != nil {
		return appwire.AgentsDocResponse{Path: path, Exists: true, Content: content}, writeApplied(err)
	}
	return resp, nil
}

// agentsDocStore owns the personal AGENTS.md file: one mutex serializes every
// read and save, and a save that reached the file emits exactly one committed
// event while that lock is still held, so the broadcast order matches the write
// order. The store is the only writer, so a handler cannot save without
// announcing it.
type agentsDocStore struct {
	path string

	mu          sync.Mutex
	onCommitted func(appwire.AgentsDocResponse)
}

func newAgentsDocStore(path string) *agentsDocStore {
	return &agentsDocStore{path: path}
}

// OnCommitted registers the store's one post-commit sink. It runs after a save
// that applied - including one whose read back failed, which still landed the
// new bytes - with the response the save answered. The callback runs inside the
// save's critical section, so it must not call back into the store; it exists to
// broadcast, and holding the lock across it keeps concurrent saves' broadcasts
// in the same order as their writes.
func (s *agentsDocStore) OnCommitted(fn func(appwire.AgentsDocResponse)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCommitted = fn
}

// Get reports the file as it is on disk, serialized against Set so a save's
// rename and read back cannot interleave with an editor read.
func (s *agentsDocStore) Get() (appwire.AgentsDocResponse, error) {
	if s == nil || s.path == "" {
		return appwire.AgentsDocResponse{}, errNoAgentsDocPath
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return readAgentsDoc(s.path)
}

// Set writes content and announces a save that applied. The broadcast carries
// the response the save answered: when the read back failed after the rename,
// that response describes the bytes the write landed, which is the state every
// other client needs even though the requester still gets the error. The save,
// its read back, and its commit callback are one critical section, so a later
// save cannot land its file and broadcast before this one has broadcast.
func (s *agentsDocStore) Set(content string) (appwire.AgentsDocResponse, error) {
	if s == nil || s.path == "" {
		return appwire.AgentsDocResponse{}, errNoAgentsDocPath
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resp, err := saveAgentsDoc(s.path, content)
	if writeDidApply(err) && s.onCommitted != nil {
		s.onCommitted(resp)
	}
	return resp, err
}

// registerAgentsDocHandlers serves evener/settings/agentsDoc/{get,set}. Writes
// serialize on one mutex so a save's rename, read back and broadcast land as
// one unit: two clients saving at once could otherwise rename in one order
// and broadcast in the other, leaving every client on content the file does
// not hold. There is no revision check by design (spec 2026-09-07 §1) - the
// last write wins, and every client hears about it. An empty path is a hub
// with no user config root to edit under, and both methods refuse.
func registerAgentsDocHandlers(server *appserver.Server, path string) {
	store := newAgentsDocStore(path)
	// The store owns the post-commit announcement (#6224): a save that applied
	// broadcasts the canonical response exactly once, and the handler below
	// cannot pair a save with its own duplicate broadcast.
	store.OnCommitted(func(resp appwire.AgentsDocResponse) {
		server.BroadcastAll(appwire.NotifyEvenerSettingsAgentsDocChanged, resp)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocGet,
		func(context.Context, appwire.EmptyParams) (appwire.AgentsDocResponse, error) {
			return store.Get()
		})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocSet,
		func(_ context.Context, params appwire.AgentsDocSetParams) (appwire.AgentsDocResponse, error) {
			return store.Set(params.Content)
		})
}
