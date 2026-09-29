package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/schema"
)

// delegateArtifactsSubdir names the per-delegation artifacts directory inside
// the delegate's own session state directory.
const delegateArtifactsSubdir = "artifacts"

// delegateArtifactsDir returns the durable per-delegation artifacts directory:
// <stateDir>/sessions/<childSessionID>/artifacts.
//
// It lives under the delegation's state — the same <stateDir>/sessions dir that
// holds the child session's job store and sits beside its transcript — and never
// under a /tmp scratch base, so it survives for as long as the delegate's own
// state does and is removed with it. The session id is validated before it is
// joined to any path.
func delegateArtifactsDir(stateDir, childSessionID string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("delegate artifacts dir requires a durable state directory")
	}
	if err := schema.ValidateSessionID(childSessionID); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, sessionsSubdir, childSessionID, delegateArtifactsSubdir), nil
}

// ensureDelegateArtifactsDir creates the delegate's artifacts directory and
// returns its absolute path. It is created once at delegation creation; a
// repeated call is a no-op.
func ensureDelegateArtifactsDir(stateDir, childSessionID string) (string, error) {
	dir, err := delegateArtifactsDir(stateDir, childSessionID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		// MkdirAll may have created the session-directory chain before failing on
		// the leaf. Take back the (empty) session dir it left so a failed creation
		// leaves no residue; os.Remove only removes an empty directory, so a
		// pre-existing populated dir is left alone.
		_ = os.Remove(filepath.Dir(dir))
		return "", fmt.Errorf("delegate artifacts dir: %w", err)
	}
	return dir, nil
}

// removeDelegateArtifacts removes a delegation's artifacts directory. It is the
// delegate-specific complement of RemoveSessionArtifacts: that helper drops the
// whole <stateDir>/sessions/<id> tree, while this removes only the artifacts
// subdirectory, so a disposal that must preserve the child's transcript and
// metadata (its resumability evidence) can still take the artifacts with it. A
// missing directory is not an error.
func removeDelegateArtifacts(stateDir, childSessionID string) error {
	dir, err := delegateArtifactsDir(stateDir, childSessionID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
