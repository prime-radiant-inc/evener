package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sessionTmpPrefix names every per-session tmp dir so the age-sweep can recognize
// serf's own dirs and never touch anything else under the base tmp.
const sessionTmpPrefix = "serf-sandbox-"

// crashedSessionMaxAge is how old a leftover session tmp dir must be before the
// next serf start sweeps it. A live session's dir is far younger; only dirs a
// crashed session failed to clean up age past this.
var crashedSessionMaxAge = 24 * time.Hour

// SessionTmp is a sandboxed session's per-session writable scratch directory. It
// is the child's TMPDIR and, under a session-private cache strategy, the redirect
// target for GOCACHE/npm_config_cache/CARGO_HOME. It is NOT the shared /tmp: the
// bwrap view mounts a fresh tmpfs over /tmp and binds only this dir writable, so
// one session cannot see another's scratch.
type SessionTmp struct {
	// Dir is the absolute path of the per-session directory.
	Dir string
}

// NewSessionTmp creates a fresh per-session tmp dir under base (os.TempDir() when
// base is empty) and first age-sweeps stale dirs left by crashed sessions. The
// returned dir must be Cleanup()'d at session end.
func NewSessionTmp(base string) (*SessionTmp, error) {
	if base == "" {
		base = os.TempDir()
	}
	sweepCrashedSessions(base)
	dir, err := os.MkdirTemp(base, sessionTmpPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("sandbox: create session tmp: %w", err)
	}
	return &SessionTmp{Dir: dir}, nil
}

// Cleanup removes the session tmp dir and everything under it. It is safe to call
// on a nil receiver or an empty dir.
func (s *SessionTmp) Cleanup() error {
	if s == nil || s.Dir == "" {
		return nil
	}
	return os.RemoveAll(s.Dir)
}

// sweepCrashedSessions removes session tmp dirs under base older than
// crashedSessionMaxAge. It only ever touches dirs carrying sessionTmpPrefix, so a
// user's own files under the base tmp are never at risk. Errors are ignored — a
// best-effort sweep must never block a new session from starting.
func sweepCrashedSessions(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-crashedSessionMaxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), sessionTmpPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}
