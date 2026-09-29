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

// delegateSessionDir returns the delegation's own session state directory,
// <stateDir>/sessions/<childSessionID>, which holds the child's job store and
// the artifacts subdirectory.
func delegateSessionDir(stateDir, childSessionID string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("delegate artifacts dir requires a durable state directory")
	}
	if err := schema.ValidateSessionID(childSessionID); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, sessionsSubdir, childSessionID), nil
}

// delegateArtifactsDir returns the durable per-delegation artifacts directory:
// <stateDir>/sessions/<childSessionID>/artifacts.
//
// It lives under the delegation's state — the same <stateDir>/sessions dir that
// holds the child session's job store and sits beside its transcript — and never
// under a /tmp scratch base, so it survives for as long as the delegate's own
// state does and is removed with it. The session id is validated before it is
// joined to any path.
func delegateArtifactsDir(stateDir, childSessionID string) (string, error) {
	base, err := delegateSessionDir(stateDir, childSessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, delegateArtifactsSubdir), nil
}

// ensureDelegateArtifactsDir creates the delegate's artifacts directory and
// returns its path under stateDir. It is created once at delegation creation; a
// repeated call is a no-op.
func ensureDelegateArtifactsDir(stateDir, childSessionID string) (string, error) {
	dir, err := delegateArtifactsDir(stateDir, childSessionID)
	if err != nil {
		return "", err
	}
	// Record which entries already existed before MkdirAll so a failure never
	// removes state this call did not create — a pre-existing file, directory, or
	// symlink at either path must be left alone.
	parent := filepath.Dir(dir)
	_, statErr := os.Stat(parent)
	parentExisted := statErr == nil
	_, leafErr := os.Lstat(dir)
	leafExisted := leafErr == nil
	if err := os.MkdirAll(dir, 0o700); err != nil {
		// Take back only what this call created: the leaf, and the session dir
		// when it did not pre-exist (os.Remove removes a directory only if it is
		// empty, so a pre-existing populated dir is left alone).
		if !leafExisted {
			_ = os.Remove(dir)
		}
		if !parentExisted {
			_ = os.Remove(parent)
		}
		return "", fmt.Errorf("delegate artifacts dir: %w", err)
	}
	return dir, nil
}

// removeDelegateArtifacts removes a delegation's artifacts directory. It is the
// delegate-specific complement of RemoveSessionArtifacts: that helper drops the
// whole <stateDir>/sessions/<id> tree, while this removes only the artifacts
// subdirectory, so a disposal that must preserve the child's transcript and
// metadata (its resumability evidence) can still take the artifacts with it. It
// then tries to remove the session directory as well, which succeeds only while
// that directory is empty — so a child that never wrote anything leaves no
// residue, while one with a job store, transcript, or metadata keeps its
// evidence. A missing directory is not an error.
func removeDelegateArtifacts(stateDir, childSessionID string) error {
	dir, err := delegateArtifactsDir(stateDir, childSessionID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if sessionDir, err := delegateSessionDir(stateDir, childSessionID); err == nil {
		_ = os.Remove(sessionDir)
	}
	return nil
}

// advertiseArtifactsDir returns dir only while it still names an existing
// directory, and "" otherwise. A create result must never advertise a path a
// failure path already removed; this reconciles that invariant for every
// exit — construct, attach, adopt, or start-input failure — without each
// caller having to know which disposal ran. Lstat plus IsDir rejects a regular
// file or symlink, which is not a directory a delegate could write reports into.
func advertiseArtifactsDir(dir string) string {
	if dir == "" {
		return ""
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return dir
}
